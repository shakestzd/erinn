package claimledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/shakestzd/wipnote/core/filelock"
)

// DirName is the git-tracked directory holding the claim ledger shards.
//
// It is deliberately NOT under .wipnote/sessions/: that tree is gitignored
// runtime telemetry, so a ledger placed there would vanish on a fresh clone —
// which would defeat the entire point of recording durable claim history.
const DirName = "claims"

// ArchiveFilename is the compacted ledger holding episodes rolled up out of
// closed sessions' shards. It lives in the same directory and is read by the
// same reader, so archived episodes stay queryable.
const ArchiveFilename = "archive.html"

// shardPrefix namespaces per-session shard filenames so no session ID can ever
// produce a file that collides with ArchiveFilename.
const shardPrefix = "sess-"

// ErrNoOpenEpisode is returned by Close when the ledger holds no open episode
// matching the given session/agent/work item. Callers treat it as benign: it
// means the episode was already closed (a repeated complete, or a reconcile
// that ran first), not that anything failed.
var ErrNoOpenEpisode = errors.New("claimledger: no open episode")

// OnCommit, when non-nil, is invoked after a successful ledger mutation with
// the wipnote dir, the repo-relative path of the file that changed, and the
// transition verb. cmd/wipnote sets it to the commit-queue producer so ledger
// writes get committed like every other canonical artifact.
//
// It is a package-level seam (the same shape as core/hooks.ReapCollectorFn)
// because core/ may not import internal/commitqueue, while every writer — CLI
// and hook handler alike — runs inside the wipnote binary that does.
var OnCommit func(wipnoteDir, relPath, action string)

// Store reads and writes the claim ledger for one project.
type Store struct {
	wipnoteDir string
	dir        string
}

// NewStore returns a Store rooted at the claims subdirectory of wipnoteDir.
// The directory is created lazily on first write, not here — a read-only
// consumer must not leave an empty directory behind.
func NewStore(wipnoteDir string) *Store {
	return &Store{wipnoteDir: wipnoteDir, dir: filepath.Join(wipnoteDir, DirName)}
}

// Dir returns the absolute path to the claims directory.
func (s *Store) Dir() string { return s.dir }

// ShardPath returns the ledger file for a root session.
func (s *Store) ShardPath(rootSessionID string) string {
	return filepath.Join(s.dir, shardPrefix+slugify(rootSessionID)+".html")
}

// ArchivePath returns the compacted archive ledger path.
func (s *Store) ArchivePath() string { return filepath.Join(s.dir, ArchiveFilename) }

// RelPath returns path relative to the repo root (the parent of .wipnote), for
// git staging and commit intents. Absolute host paths must never leak into
// wipnote descriptions or commit messages, so callers use this, not the
// absolute path.
func (s *Store) RelPath(path string) string {
	repoRoot := filepath.Dir(s.wipnoteDir)
	if rel, err := filepath.Rel(repoRoot, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(filepath.Join(filepath.Base(s.wipnoteDir), DirName, filepath.Base(path)))
}

// slugify maps a session ID onto a safe single path component. Session IDs are
// UUIDs in practice, but they arrive from harness payloads and environment
// variables, so anything outside the allowlist — including "/" and ".." — is
// folded to "-" rather than trusted into a path.
func slugify(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if strings.Trim(out, "-") == "" {
		return "unknown"
	}
	return out
}

// NewEpisodeID mints a fragment-safe episode identifier.
func NewEpisodeID() string { return "ep-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16] }

// Open records the START of a claim episode: one appended row with no end.
//
// It is IDEMPOTENT by design, and that is the single most important property in
// this package. `wipnote feature start` runs again on every re-claim and every
// lease renewal, and the PreToolUse heartbeat fires constantly; if any of those
// appended a row, renewal traffic would bury the actual claim signal. Open
// therefore writes NOTHING when the shard already holds an open episode for the
// same (session, agent, work item) — and the heartbeat path never calls into
// this package at all, so there is no renewal entry point to get wrong.
//
// Returns the episode that is now open, and whether a new row was written.
func (s *Store) Open(rootSessionID string, e Episode) (Episode, bool, error) {
	if e.ID == "" {
		e.ID = NewEpisodeID()
	}
	if e.RootSessionID == "" {
		e.RootSessionID = rootSessionID
	}
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now().UTC()
	}
	e.EndedAt = time.Time{}
	e.Outcome = ""
	if err := e.Validate(); err != nil {
		return Episode{}, false, err
	}

	path := s.ShardPath(rootSessionID)
	release := filelock.Guard(path)
	defer release()

	existing, err := readShard(path)
	if err != nil {
		return Episode{}, false, err
	}
	if open, ok := findOpen(existing, e.SessionID, e.AgentID, e.WorkItemID); ok {
		// Already held — a renewal, not a new episode. No row, no update.
		return open, false, nil
	}

	if err := appendRowLocked(path, e); err != nil {
		return Episode{}, false, err
	}
	s.notify(path, "claim")
	return e, true, nil
}

// Close records the END of the open episode for (session, agent, work item).
//
// This is a read-modify-write: the row is updated IN PLACE so one episode stays
// one row. It is rarer than Open by construction (an episode is opened before
// it can be closed, and renewals open nothing), so paying the serialize cost
// here keeps it off the frequent path.
//
// Returns ErrNoOpenEpisode when nothing matched.
func (s *Store) Close(rootSessionID, sessionID, agentID, workItemID string, outcome Outcome, endedAt time.Time) (Episode, error) {
	if !outcome.valid() {
		return Episode{}, fmt.Errorf("claimledger: invalid outcome %q", outcome)
	}
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}

	path := s.ShardPath(rootSessionID)
	release := filelock.Guard(path)
	defer release()

	episodes, err := readShard(path)
	if err != nil {
		return Episode{}, err
	}
	idx := -1
	for i := range episodes {
		if episodes[i].IsOpen() &&
			episodes[i].SessionID == sessionID &&
			episodes[i].AgentID == agentID &&
			episodes[i].WorkItemID == workItemID {
			// Keep the LATEST open match. More than one open episode for the same
			// triple should not exist (Open refuses to create it), but if a cache
			// wipe or a hand edit produced one, closing the newest matches what
			// GetActiveClaim does for the SQLite claim row.
			if idx < 0 || episodes[i].StartedAt.After(episodes[idx].StartedAt) {
				idx = i
			}
		}
	}
	if idx < 0 {
		return Episode{}, ErrNoOpenEpisode
	}

	if endedAt.Before(episodes[idx].StartedAt) {
		endedAt = episodes[idx].StartedAt
	}
	episodes[idx].EndedAt = endedAt
	episodes[idx].Outcome = outcome
	if err := writeAllLocked(path, episodes); err != nil {
		return Episode{}, err
	}
	s.notify(path, "release")
	return episodes[idx], nil
}

// CloseAllForSession closes every open episode in a root session's shard in ONE
// read-modify-write.
//
// This is the answer to "what happens to an episode whose session dies without
// releasing": SessionEnd calls it with OutcomeAbandoned, and the stale-session
// reaper calls it with OutcomeExpired, so a crashed agent's interval still gets
// an end.
//
// onlySessionID, when non-empty, restricts the close to that session's rows. A
// shard is keyed by ROOT session and can hold rows from several sessions in the
// same tree, so an ending CHILD session must not close its parent's or its
// siblings' episodes. Empty means every open episode in the shard, which is what
// Reconcile wants once the whole tree is dead.
//
// Returns the number of episodes closed.
func (s *Store) CloseAllForSession(rootSessionID, onlySessionID string, outcome Outcome, endedAt time.Time) (int, error) {
	if !outcome.valid() {
		return 0, fmt.Errorf("claimledger: invalid outcome %q", outcome)
	}
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}

	path := s.ShardPath(rootSessionID)
	release := filelock.Guard(path)
	defer release()

	episodes, err := readShard(path)
	if err != nil {
		return 0, err
	}
	closed := 0
	for i := range episodes {
		if !episodes[i].IsOpen() {
			continue
		}
		if onlySessionID != "" && episodes[i].SessionID != onlySessionID {
			continue
		}
		end := endedAt
		if end.Before(episodes[i].StartedAt) {
			end = episodes[i].StartedAt
		}
		episodes[i].EndedAt = end
		episodes[i].Outcome = outcome
		closed++
	}
	if closed == 0 {
		return 0, nil
	}
	if err := writeAllLocked(path, episodes); err != nil {
		return 0, err
	}
	s.notify(path, "release")
	return closed, nil
}

// Shards returns the per-session shard paths, sorted, excluding the archive.
func (s *Store) Shards() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claimledger: read %s: %w", s.dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".html") || !strings.HasPrefix(name, shardPrefix) {
			continue
		}
		out = append(out, filepath.Join(s.dir, name))
	}
	return out, nil
}

// Files returns every ledger file to read, shards plus the archive. This is the
// set the reindex pass ingests — archived episodes are read by exactly the same
// code path as live ones, which is what keeps them queryable.
func (s *Store) Files() ([]string, error) {
	files, err := s.Shards()
	if err != nil {
		return nil, err
	}
	archive := s.ArchivePath()
	if _, statErr := os.Stat(archive); statErr == nil {
		files = append(files, archive)
	}
	return files, nil
}

// ReadAll returns every episode across shards and the archive.
func (s *Store) ReadAll() ([]Episode, error) {
	files, err := s.Files()
	if err != nil {
		return nil, err
	}
	var out []Episode
	for _, f := range files {
		eps, readErr := ReadFile(f)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, readErr
		}
		out = append(out, eps...)
	}
	sortEpisodes(out)
	return out, nil
}

// ReadShard returns the episodes recorded for one root session.
func (s *Store) ReadShard(rootSessionID string) ([]Episode, error) {
	return readShard(s.ShardPath(rootSessionID))
}

// RootSessionOf returns the SLUGIFIED root session key encoded in a shard's
// filename, or "" when the path is not a shard. It is a fallback identity only
// — see rawRootSession, which prefers the unslugified ID recorded in the rows.
func RootSessionOf(path string) string {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, shardPrefix) || !strings.HasSuffix(base, ".html") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(base, shardPrefix), ".html")
}

// rawRootSession returns the shard's root session ID as it was actually
// observed, taken from the rows rather than reconstructed from the filename.
//
// This distinction is load-bearing: slugify is lossy, so a filename cannot be
// turned back into a session ID, and callers that hand the result to the
// sessions/claims tables (liveness checks, reconcile, archive eligibility)
// would silently miss every session whose ID contained a folded character.
// Falls back to the filename key when the shard has no rows to read it from.
func rawRootSession(shardPath string, episodes []Episode) string {
	for _, e := range episodes {
		if e.RootSessionID != "" {
			return e.RootSessionID
		}
	}
	for _, e := range episodes {
		if e.SessionID != "" {
			return e.SessionID
		}
	}
	return RootSessionOf(shardPath)
}

func readShard(path string) ([]Episode, error) {
	eps, err := ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return eps, nil
}

func findOpen(eps []Episode, sessionID, agentID, workItemID string) (Episode, bool) {
	var best Episode
	found := false
	for _, e := range eps {
		if e.IsOpen() && e.SessionID == sessionID && e.AgentID == agentID && e.WorkItemID == workItemID {
			if !found || e.StartedAt.After(best.StartedAt) {
				best = e
				found = true
			}
		}
	}
	return best, found
}

func (s *Store) notify(path, action string) {
	if OnCommit == nil {
		return
	}
	OnCommit(s.wipnoteDir, s.RelPath(path), action)
}
