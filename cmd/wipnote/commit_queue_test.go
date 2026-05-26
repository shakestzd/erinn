package main

import (
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/internal/commitqueue"
)

// TestRecordCommitIntentAppendsAfterCanonicalWrite verifies the producer half
// of the outbox model: recording an intent appends to the outbox without
// touching git. The outbox path seam is redirected to a temp dir.
func TestRecordCommitIntentAppendsAfterCanonicalWrite(t *testing.T) {
	tmp := t.TempDir()
	orig := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmp, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = orig })

	if err := recordCommitIntent("/repo",
		[]string{".wipnote/features/feat-1.html"},
		"wipnote: complete feat-1", "feat-1", "complete"); err != nil {
		t.Fatalf("recordCommitIntent: %v", err)
	}

	ob, err := openCommitOutbox("/repo")
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	pending, err := ob.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending intent, got %d", len(pending))
	}
	if pending[0].WorkItemID != "feat-1" || pending[0].Message != "wipnote: complete feat-1" {
		t.Fatalf("intent fields wrong: %+v", pending[0])
	}
}

// TestOutboxCommitterIsIdempotentForNonGit ensures the production committer
// treats a non-git repo root as a benign no-op (success) so it does not poison
// the queue.
func TestOutboxCommitterIsIdempotentForNonGit(t *testing.T) {
	// t.TempDir() is not a git repo.
	intent := commitqueue.Intent{
		RepoRoot: t.TempDir(),
		RelPaths: []string{".wipnote/features/feat-1.html"},
		Message:  "wipnote: complete feat-1",
	}
	if err := outboxCommitter(intent); err != nil {
		t.Fatalf("outboxCommitter on non-git root should be a no-op, got: %v", err)
	}
}

// TestIsNothingToCommit covers the idempotency string match used to treat an
// already-committed artifact as success.
func TestIsNothingToCommit(t *testing.T) {
	cases := map[string]bool{
		"nothing to commit, working tree clean": true,
		"no changes added to commit":            true,
		"fatal: index locked":                   false,
	}
	for out, want := range cases {
		if got := isNothingToCommit(out); got != want {
			t.Fatalf("isNothingToCommit(%q) = %v, want %v", out, got, want)
		}
	}
}
