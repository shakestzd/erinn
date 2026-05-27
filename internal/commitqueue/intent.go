// Package commitqueue implements a durable, serialized outbox for wipnote
// artifact commits.
//
// MODEL: Instead of every agent/CLI invocation writing to git directly on its
// hot path, a canonical `.wipnote` HTML/YAML write completes FIRST, then a
// "commit intent" is appended to an outbox. A single serialized committer
// (`wipnote commit-queue flush`) later drains the outbox in FIFO order under
// the repo-scoped advisory git lock, batching the pending artifact commits.
//
// LOCATION: The outbox is a derived, local, never-committed artifact. It lives
// in the per-user cache dir alongside the SQLite read-index and the
// git-mutation lock (~/.cache/wipnote/<path-hash>/), NOT inside `.wipnote/`.
// Putting it in `.wipnote/` would make the outbox itself need committing —
// recursion. The path is derived from internal/storage so the path-hash keying
// is reused, not re-invented.
//
// FORMAT: append-only NDJSON (one JSON Intent per line). Append-only means a
// crash mid-write loses at most the last partial line; earlier intents are
// intact. The drain re-reads the whole file, processes survivors, and rewrites
// the remainder — so an interrupted flush re-flushes safely because the
// underlying artifact commit is idempotent ("nothing to commit" → no-op).
//
// DEAD-LETTER: each intent carries an Attempts counter. After MaxAttempts
// consecutive failures the intent is moved to a sibling dead-letter NDJSON file
// and the drain continues with the next intent, so one poison commit never
// freezes the ordered queue.
package commitqueue

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// MaxAttempts is the consecutive-failure threshold after which an intent is
// dead-lettered rather than retried again on the next flush.
const MaxAttempts = 5

// Intent is a single pending artifact-commit request recorded after the
// canonical `.wipnote` write completed. One Intent == one NDJSON line.
//
// RepoRoot anchors the git command (git -C <RepoRoot>). RelPaths are the
// artifact paths to stage/commit, relative to RepoRoot. Message is the full
// commit subject. WorkItemID and Action are carried for observability/auditing.
// EnqueuedAt records when the intent was appended; Attempts counts how many
// times a flush has tried (and failed) to commit it.
type Intent struct {
	RepoRoot   string    `json:"repo_root"`
	RelPaths   []string  `json:"rel_paths"`
	Message    string    `json:"message"`
	WorkItemID string    `json:"work_item_id,omitempty"`
	Action     string    `json:"action,omitempty"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	Attempts   int       `json:"attempts,omitempty"`
}

// Validate reports the first structural problem with an intent, or nil. An
// intent with no repo root, no paths, or an empty message can never produce a
// meaningful commit and is rejected at record time rather than poisoning the
// queue later.
func (i Intent) Validate() error {
	if strings.TrimSpace(i.RepoRoot) == "" {
		return fmt.Errorf("commitqueue: intent missing repo_root")
	}
	if len(i.RelPaths) == 0 {
		return fmt.Errorf("commitqueue: intent missing rel_paths")
	}
	for _, rel := range i.RelPaths {
		// An empty/blank rel_path is dangerous: the committer joins it to RepoRoot
		// (filepath.Join(root, "") == root), so `git add -- <root>` would stage the
		// WHOLE repository under a queued artifact commit (roborev #3723). Require
		// each path to be non-empty, relative, and contained within the repo.
		if strings.TrimSpace(rel) == "" {
			return fmt.Errorf("commitqueue: intent has an empty rel_path (would stage the whole repo)")
		}
		if filepath.IsAbs(rel) {
			return fmt.Errorf("commitqueue: rel_path %q must be repo-relative, not absolute", rel)
		}
		clean := filepath.Clean(rel)
		// "." / "./" / "a/.." all clean to "." which joins to RepoRoot — the same
		// stage-the-whole-repo footgun as an empty path (roborev #3726).
		if clean == "." {
			return fmt.Errorf("commitqueue: rel_path %q resolves to the repo root (would stage the whole repo)", rel)
		}
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("commitqueue: rel_path %q escapes the repository", rel)
		}
	}
	if strings.TrimSpace(i.Message) == "" {
		return fmt.Errorf("commitqueue: intent missing message")
	}
	return nil
}

// marshalLine encodes an intent as a single NDJSON line (no trailing newline).
func marshalLine(i Intent) ([]byte, error) {
	if i.EnqueuedAt.IsZero() {
		i.EnqueuedAt = time.Now().UTC()
	}
	return json.Marshal(i)
}

// parseLine decodes one NDJSON line into an Intent. Blank lines yield
// (zero, false, nil) so callers can skip them; malformed JSON yields an error.
func parseLine(line []byte) (Intent, bool, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return Intent{}, false, nil
	}
	var i Intent
	if err := json.Unmarshal([]byte(trimmed), &i); err != nil {
		return Intent{}, false, fmt.Errorf("commitqueue: parse intent line: %w", err)
	}
	return i, true, nil
}
