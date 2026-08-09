package main

import (
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
)

// TestNormalizeFilesInput covers the six required cases for --files normalization.
// Each test uses a fixed repoRoot so the function is deterministic without touching git.
func TestNormalizeFilesInput(t *testing.T) {
	const repoRoot = "/workspaces/repo"

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "absolute paths inside repo become relative",
			input: "/workspaces/repo/cmd/foo.go,/workspaces/repo/internal/bar.go",
			want:  "cmd/foo.go,internal/bar.go",
		},
		{
			name:  "already-relative paths pass through unchanged",
			input: "cmd/foo.go,internal/bar.go",
			want:  "cmd/foo.go,internal/bar.go",
		},
		{
			name:  "outside-repo absolute path gets unresolved prefix",
			input: "/home/user/external.txt",
			want:  "unresolved:/home/user/external.txt",
		},
		{
			name:  "whitespace around segments is stripped",
			input: "cmd/foo.go, internal/bar.go ",
			want:  "cmd/foo.go,internal/bar.go",
		},
		{
			name:  "empty input returns empty string",
			input: "",
			want:  "",
		},
		{
			name:  "empty segments from leading and trailing commas are dropped",
			input: ",cmd/foo.go,",
			want:  "cmd/foo.go",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeFilesInput(tc.input, repoRoot)
			if got != tc.want {
				t.Errorf("normalizeFilesInput(%q, %q) = %q, want %q",
					tc.input, repoRoot, got, tc.want)
			}
		})
	}
}

// TestBugCreate_NoCausedByEdgeWhenActiveFeaturePresent proves that creating a
// bug while a feature is in-progress does NOT auto-attach a caused_by edge.
// Before the fix (bug-f9039e2b), the else branch fell through to
// detectActiveFeature and called autoCausedByEdge, fabricating a false causal
// edge. After the fix, no caused_by edge is written unless the caller passes
// --caused-by explicitly.
func TestBugCreate_NoCausedByEdgeWhenActiveFeaturePresent(t *testing.T) {
	// test-session-caused-by-bug — session-shaped id required by the canonical session ledger
	// that testHgDirWithDB now seeds.
	const sessionID = "019ee378-abcd-7000-8000-000000000303"

	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)
	t.Setenv("WIPNOTE_AGENT_ID", "")

	// Create a track for the items.
	trackID := testSetupTrack(t, hgDir)

	// Create a feature and record it as the active work item in the DB,
	// simulating a session where a feature is already in-progress.
	if err := testCreate("feature", "Active Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, err := htmlparse.ParseFile(featFiles[0])
	if err != nil {
		t.Fatalf("parse feature: %v", err)
	}
	activeFeatureID := featNode.ID

	// Write the active feature into the DB exactly as runWiSetStatus does,
	// so detectActiveFeature would find it if the fallback were still present.
	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := dbpkg.SetActiveWorkItem(database, sessionID, "", activeFeatureID); err != nil {
		database.Close()
		t.Fatalf("SetActiveWorkItem: %v", err)
	}
	// Legacy dual-write for GetActiveWorkItemWithFallback.
	_, _ = database.Exec(
		`UPDATE sessions SET active_feature_id = ? WHERE session_id = ?`,
		activeFeatureID, sessionID,
	)
	database.Close()

	// Create a bug WITHOUT --caused-by while the feature is "active".
	bugOpts := &wiCreateOpts{
		trackID:     trackID,
		priority:    "medium",
		description: "a bug that has nothing to do with the active feature",
	}
	if err := runWiCreate("bug", "Unrelated Bug", bugOpts); err != nil {
		t.Fatalf("create bug: %v", err)
	}

	// Parse the created bug and assert no caused_by edge exists.
	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	if len(bugFiles) != 1 {
		t.Fatalf("expected 1 bug file, got %d", len(bugFiles))
	}
	bugNode, err := htmlparse.ParseFile(bugFiles[0])
	if err != nil {
		t.Fatalf("parse bug: %v", err)
	}
	if causedByEdges, ok := bugNode.Edges["caused_by"]; ok && len(causedByEdges) > 0 {
		t.Errorf("bug must NOT have a caused_by edge when --caused-by is omitted; got caused_by → %v",
			causedByEdges[0].TargetID)
	}
}

// TestBugCreate_ExplicitCausedByIsHonored proves the --caused-by flag still
// wires the caused_by edge correctly after the implicit-fallback removal.
func TestBugCreate_ExplicitCausedByIsHonored(t *testing.T) {
	// test-session-explicit-caused-by — session-shaped id required by the canonical session ledger
	// that testHgDirWithDB now seeds.
	const sessionID = "019ee378-abcd-7000-8000-000000000304"

	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)

	trackID := testSetupTrack(t, hgDir)

	// Create a feature to reference as --caused-by.
	if err := testCreate("feature", "Causal Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	causalFeatureID := featNode.ID

	bugOpts := &wiCreateOpts{
		trackID:     trackID,
		priority:    "medium",
		description: "a bug explicitly caused by the feature",
		causedBy:    causalFeatureID,
	}
	if err := runWiCreate("bug", "Explicitly Caused Bug", bugOpts); err != nil {
		t.Fatalf("create bug: %v", err)
	}

	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	if len(bugFiles) != 1 {
		t.Fatalf("expected 1 bug file, got %d", len(bugFiles))
	}
	bugNode, err := htmlparse.ParseFile(bugFiles[0])
	if err != nil {
		t.Fatalf("parse bug: %v", err)
	}
	causedByEdges, ok := bugNode.Edges["caused_by"]
	if !ok || len(causedByEdges) == 0 {
		t.Fatalf("bug must have caused_by edge when --caused-by is passed; edges = %v", bugNode.Edges)
	}
	if causedByEdges[0].TargetID != causalFeatureID {
		t.Errorf("caused_by target = %q, want %q", causedByEdges[0].TargetID, causalFeatureID)
	}
}
