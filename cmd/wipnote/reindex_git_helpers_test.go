package main

import (
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// This file was reindex_incremental_test.go. The incremental reindex path it
// covered — reparse only the .wipnote HTML files git reports as changed since
// the metadata key last_indexed_commit — was deleted with the persistent
// project database that gave it meaning (feat-fc3cc9e0). Incrementality was
// defined relative to an index that survived between runs; the projection now
// starts empty in every process, so a git-diff-scoped pass would build a
// projection containing only the changed files rather than a complete one.
//
// Removed along with the path: TestIncrementalReindex_ParsesChangedFiles,
// _DeletesRemovedFiles, _PropagatesTrackIDAfterMove, _DirtyDeletion,
// TestIdFromHTMLPath and TestDeduplicatePaths, each of which drove a function
// that no longer exists. What survives here are the two git helpers that
// outlived the incremental path — reindexCommitTrailers still uses both — and
// the metadata accessors they pair with.

// TestGetSetMetadata covers the key/value accessors reindexCommitTrailers uses
// to remember how far it has scanned.
func TestGetSetMetadata(t *testing.T) {
	database := openReindexTestDB(t)

	// Missing key returns empty string, no error.
	val, err := dbpkg.GetMetadata(database, "missing_key")
	if err != nil {
		t.Fatalf("GetMetadata missing key: %v", err)
	}
	if val != "" {
		t.Errorf("GetMetadata missing key: got %q, want %q", val, "")
	}

	// Set and read back.
	if err := dbpkg.SetMetadata(database, metaKeyLastTrailerScanCommit, "abc123"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	val, err = dbpkg.GetMetadata(database, metaKeyLastTrailerScanCommit)
	if err != nil {
		t.Fatalf("GetMetadata after set: %v", err)
	}
	if val != "abc123" {
		t.Errorf("GetMetadata: got %q, want %q", val, "abc123")
	}

	// Overwrite.
	if err := dbpkg.SetMetadata(database, metaKeyLastTrailerScanCommit, "def456"); err != nil {
		t.Fatalf("SetMetadata overwrite: %v", err)
	}
	val, err = dbpkg.GetMetadata(database, metaKeyLastTrailerScanCommit)
	if err != nil {
		t.Fatalf("GetMetadata after overwrite: %v", err)
	}
	if val != "def456" {
		t.Errorf("GetMetadata overwrite: got %q, want %q", val, "def456")
	}
}

func TestGitHeadCommit_NoGitRepo(t *testing.T) {
	commit := gitHeadCommit("/tmp/definitely-not-a-git-repo-xyz123")
	// Ensure no panic; result is empty on error.
	if commit != "" {
		t.Logf("gitHeadCommit returned %q for non-repo (non-fatal)", commit)
	}
}

func TestGitCommitExists_InvalidCommit(t *testing.T) {
	exists := gitCommitExists("/tmp", "0000000000000000000000000000000000000000")
	if exists {
		t.Error("gitCommitExists: expected false for bogus commit in non-repo")
	}
}
