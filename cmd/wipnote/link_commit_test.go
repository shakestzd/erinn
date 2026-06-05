package main

import (
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/models"
)

// TestInsertGitCommitResult_IdempotentLink verifies that inserting the same
// commit twice returns n=0 on the second call (idempotent / INSERT OR IGNORE).
func TestInsertGitCommitResult_IdempotentLink(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	commit := &models.GitCommit{
		CommitHash: "abc1234500000000000000000000000000000000",
		SessionID:  "manual",
		FeatureID:  "bug-0a812209",
		Message:    "feat(bug-0a812209): add link-commit (#121)",
		Timestamp:  time.Now().UTC(),
	}

	n1, err := dbpkg.InsertGitCommitResult(database, commit)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("expected 1 row inserted, got %d", n1)
	}

	n2, err := dbpkg.InsertGitCommitResult(database, commit)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("expected 0 rows on duplicate, got %d", n2)
	}
}

// TestGetCommitsByFeature_SatisfiesProvenanceGate verifies that after linking a
// commit the provenance gate function (GetCommitsByFeature) returns a non-empty
// slice, which is exactly the condition that unblocks feature/bug completion.
func TestGetCommitsByFeature_SatisfiesProvenanceGate(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	featureID := "bug-deadbeef"
	commit := &models.GitCommit{
		CommitHash: "deadbeef00000000000000000000000000000000",
		SessionID:  "manual",
		FeatureID:  featureID,
		Message:    "fix(bug-deadbeef): worktree branch commit (#121)",
		Timestamp:  time.Now().UTC(),
	}

	if err := dbpkg.InsertGitCommit(database, commit); err != nil {
		t.Fatalf("insert: %v", err)
	}

	commits, err := dbpkg.GetCommitsByFeature(database, featureID)
	if err != nil {
		t.Fatalf("GetCommitsByFeature: %v", err)
	}
	if len(commits) == 0 {
		t.Fatal("expected >=1 commit after linking, got 0 — provenance gate would still block")
	}
	if commits[0].CommitHash != commit.CommitHash {
		t.Errorf("commit hash mismatch: got %q, want %q", commits[0].CommitHash, commit.CommitHash)
	}
	if commits[0].FeatureID != featureID {
		t.Errorf("feature_id mismatch: got %q, want %q", commits[0].FeatureID, featureID)
	}
}
