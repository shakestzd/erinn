package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/guardprofile"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// seedFeatureFile records a feature_files row at the same DB path production
// (storage.CanonicalDBPath, pinned via WIPNOTE_DB_PATH in testHgDirWithDB)
// resolves, so the provenance gate sees the item as code-bearing.
func seedFeatureFile(t *testing.T, hgDir, itemID, filePath string) {
	t.Helper()
	database, err := dbpkg.Open(filepath.Join(hgDir, ".db", "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	rowID := "ff-" + itemID + "-" + strings.NewReplacer("/", "_", ".", "_").Replace(filePath)
	if err := dbpkg.UpsertFeatureFile(database, &models.FeatureFile{
		ID:        rowID,
		FeatureID: itemID,
		FilePath:  filePath,
		Operation: "edit",
		SessionID: "test-session-prov",
	}); err != nil {
		t.Fatalf("upsert feature_file: %v", err)
	}
}

// seedProvCommit records a git_commits row linked to the item.
func seedProvCommit(t *testing.T, hgDir, itemID, sha string) {
	t.Helper()
	database, err := dbpkg.Open(filepath.Join(hgDir, ".db", "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := dbpkg.InsertGitCommit(database, &models.GitCommit{
		CommitHash: sha,
		SessionID:  "test-session-prov",
		FeatureID:  itemID,
		Message:    "impl",
		Timestamp:  time.Now(),
	}); err != nil {
		t.Fatalf("insert git commit: %v", err)
	}
}

// createItem creates a work item of the given type and returns its ID.
func createItem(t *testing.T, hgDir, typeName, title, trackID string) string {
	t.Helper()
	if err := testCreate(typeName, title, trackID, "medium", false, false); err != nil {
		t.Fatalf("create %s: %v", typeName, err)
	}
	dirName := typeName + "s"
	prefix := map[string]string{"feature": "feat-", "bug": "bug-", "spike": "spk-"}[typeName]
	files, _ := filepath.Glob(filepath.Join(hgDir, dirName, prefix+"*.html"))
	if len(files) == 0 {
		t.Fatalf("no %s file created", typeName)
	}
	node, _ := htmlparse.ParseFile(files[len(files)-1])
	return node.ID
}

func prepProject(t *testing.T) (tmpDir, hgDir string) {
	t.Helper()
	const sessionID = "test-session-prov"
	tmpDir, hgDir = testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = "" })
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_CACHE_DIR", tmpDir)
	return tmpDir, hgDir
}

func seedPassingGateRecord(t *testing.T, projectRoot, sessionID, workItemID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectRoot, "plugin", "config"), 0o755); err != nil {
		t.Fatalf("mkdir gate config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "go.mod"), []byte("module example.com/provenancegatetest\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "plugin", "config", "quality-gate-flake-allowlist.json"), []byte(`[
  {
    "id": "tmp-noexec",
    "match_all": ["/tmp/", "permission denied"],
    "justification": "Test fixture justification"
  },
  {
    "id": "listener-socket-sandbox",
    "match_all": ["listen tcp", "socket: operation not permitted"],
    "justification": "Test fixture listener sandbox justification"
  }
]`), 0o644); err != nil {
		t.Fatalf("write gate allowlist: %v", err)
	}

	tmpBase := execCapableBase(t)
	for _, dir := range []string{"gotmp-exec", "gocache"} {
		if err := os.MkdirAll(filepath.Join(tmpBase, dir), 0o755); err != nil {
			t.Fatalf("mkdir external %s: %v", dir, err)
		}
	}
	t.Setenv("TMPDIR", filepath.Join(tmpBase, "gotmp-exec"))
	t.Setenv("GOTMPDIR", filepath.Join(tmpBase, "gotmp-exec"))
	t.Setenv("GOCACHE", filepath.Join(tmpBase, "gocache"))

	result, err := runSessionGate(projectRoot, sessionID, workItemID, "check", guardprofile.PhaseQuality, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("runSessionGate: %v", err)
	}
	if result == nil || !result.Passed || result.Record == nil || !result.Record.SignatureValid() {
		t.Fatalf("expected valid passing gate record, got %+v", result)
	}
}

// 1. Code-bearing feature, zero commits, no flag → blocked non-zero.
func TestProvenanceGate_CodeBearingFeatureZeroCommits_Blocked(t *testing.T) {
	_, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Code Feature", trackID)
	seedFeatureFile(t, hgDir, id, "internal/foo/bar.go")

	wiAcceptedAdvisory = ""
	err := runWiSetStatus("feature", id, "done")
	if err == nil {
		t.Fatalf("expected completion to be blocked for zero-commit code-bearing feature")
	}
	if !strings.Contains(err.Error(), "accepted-advisory") {
		t.Errorf("error should mention --accepted-advisory remediation, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status == models.StatusDone {
		t.Errorf("item must not be marked done when gate blocks")
	}
}

// 2. Code-bearing BUG and SPIKE with zero commits → also blocked (type-agnostic).
func TestProvenanceGate_CodeBearingBugAndSpikeZeroCommits_Blocked(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion provenance gate")
	}

	for _, tc := range []struct{ typeName, dir string }{
		{"bug", "bugs"},
		{"spike", "spikes"},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			_, hgDir := prepProject(t)
			trackID := testSetupTrack(t, hgDir)
			id := createItem(t, hgDir, tc.typeName, "Code "+tc.typeName, trackID)
			seedFeatureFile(t, hgDir, id, "cmd/wipnote/thing.go")

			wiAcceptedAdvisory = ""
			err := runWiSetStatus(tc.typeName, id, "done")
			if err == nil {
				t.Fatalf("expected %s completion blocked (type-agnostic provenance gate)", tc.typeName)
			}
			node, _ := htmlparse.ParseFile(filepath.Join(hgDir, tc.dir, id+".html"))
			if node.Status == models.StatusDone {
				t.Errorf("%s must not be done when gate blocks", tc.typeName)
			}
		})
	}
}

// 3. Same with --accepted-advisory → completes, reason persisted + in check.
func TestProvenanceGate_AdvisoryOverrideCompletesAndPersists(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion provenance gate")
	}

	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Advisory Feature", trackID)
	seedFeatureFile(t, hgDir, id, "internal/foo/bar.go")
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)

	const reason = "infra-only refactor, no source commit by design"
	wiAcceptedAdvisory = reason
	t.Cleanup(func() { wiAcceptedAdvisory = "" })

	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("expected completion to succeed with --accepted-advisory, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("item should be done after advisory override, status=%s", node.Status)
	}
	got := acceptedAdvisoryOf(node)
	if got != reason {
		t.Errorf("accepted_advisory not persisted on artifact: got %q want %q", got, reason)
	}

	// Surfaced by `wipnote check accepted-advisory` (compliance output).
	var sb strings.Builder
	if err := runCheckAcceptedAdvisory(&sb); err != nil {
		t.Fatalf("runCheckAcceptedAdvisory: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, id) || !strings.Contains(out, reason) {
		t.Errorf("check accepted-advisory output missing id/reason:\n%s", out)
	}
}

// 4. Pure-.wipnote/doc item, zero commits → completes normally (exempt).
func TestProvenanceGate_PureWipnoteItemExempt(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion provenance gate")
	}

	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Docs Feature", trackID)
	// Only a .wipnote path touched → NOT code-bearing.
	seedFeatureFile(t, hgDir, id, ".wipnote/features/"+id+".html")
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)

	wiAcceptedAdvisory = ""
	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("pure-.wipnote item should complete normally, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("exempt item should be done, status=%s", node.Status)
	}
}

// 5. Item with >=1 commit row → completes normally.
func TestProvenanceGate_HasCommitsCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion provenance gate")
	}

	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Committed Feature", trackID)
	seedFeatureFile(t, hgDir, id, "internal/foo/bar.go")
	seedProvCommit(t, hgDir, id, "deadbeefcafe0001")
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)

	wiAcceptedAdvisory = ""
	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("item with linked commit should complete, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("item with commit should be done, status=%s", node.Status)
	}
}

// CodeBearingPaths unit: .wipnote paths excluded, source paths returned.
func TestCodeBearingPaths_ExcludesWipnote(t *testing.T) {
	if testing.Short() {
		t.Skip("drives provenance path classification integration flow")
	}

	tmpDir, hgDir := prepProject(t)
	id := "feat-cbtest01"
	seedFeatureFile(t, hgDir, id, ".wipnote/features/"+id+".html")
	seedFeatureFile(t, hgDir, id, "internal/db/lineage_repo.go")

	database, err := dbpkg.Open(filepath.Join(hgDir, ".db", "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	paths, err := dbpkg.CodeBearingPaths(database, id, tmpDir)
	if err != nil {
		t.Fatalf("CodeBearingPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != "internal/db/lineage_repo.go" {
		t.Errorf("expected only the non-.wipnote source path, got %v", paths)
	}

	none, err := dbpkg.CodeBearingPaths(database, "feat-nonexistent", tmpDir)
	if err != nil {
		t.Fatalf("CodeBearingPaths(empty): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("expected no code-bearing paths for unknown item, got %v", none)
	}
}

// TestCodeBearingPaths_ExcludesTmpScratch verifies that absolute paths outside
// the project root (e.g. /tmp/foo.yaml) are not counted as code-bearing, while
// an in-project path is still returned.
func TestCodeBearingPaths_ExcludesTmpScratch(t *testing.T) {
	tmpDir, hgDir := prepProject(t)
	id := "feat-cbtest-tmp"
	seedFeatureFile(t, hgDir, id, "/tmp/foo.yaml")
	seedFeatureFile(t, hgDir, id, "internal/db/lineage_repo.go")

	database, err := dbpkg.Open(filepath.Join(hgDir, ".db", "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	paths, err := dbpkg.CodeBearingPaths(database, id, tmpDir)
	if err != nil {
		t.Fatalf("CodeBearingPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != "internal/db/lineage_repo.go" {
		t.Errorf("expected only in-project path, got %v", paths)
	}
}

// TestGateRecordGuard_AdvisoryBypassesGateRecord verifies Fix 2: when
// --accepted-advisory is set, the gate-record guard (guard 4) is skipped so
// `feature/bug/spike complete --allow-dirty --accepted-advisory "<reason>"` can
// succeed even when no passing gate record exists in the DB.
func TestGateRecordGuard_AdvisoryBypassesGateRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("drives completion gate bypass flow")
	}

	for _, tc := range []string{"feature", "bug", "spike"} {
		t.Run(tc, func(t *testing.T) {
			_, hgDir := prepProject(t)
			trackID := testSetupTrack(t, hgDir)
			id := createItem(t, hgDir, tc, "Advisory Bypass "+tc, trackID)
			// Make the item code-bearing so the gate-record guard fires at all.
			seedFeatureFile(t, hgDir, id, "internal/foo/bar.go")
			// Seed a linked commit so the provenance gate (guard 3) also passes.
			seedProvCommit(t, hgDir, id, "cafebabe00000001")

			// Do NOT call seedPassingGateRecord — gate-record guard must be
			// bypassed by --accepted-advisory alone.
			const reason = "no gate record available; accepted-advisory bypasses guard 4"
			wiAcceptedAdvisory = reason
			wiAllowDirtyComplete = true
			t.Cleanup(func() {
				wiAcceptedAdvisory = ""
				wiAllowDirtyComplete = false
			})

			if err := runWiSetStatus(tc, id, "done"); err != nil {
				t.Fatalf("expected complete to succeed with --accepted-advisory, got: %v", err)
			}
			dirName := tc + "s"
			node, _ := htmlparse.ParseFile(filepath.Join(hgDir, dirName, id+".html"))
			if node.Status != models.StatusDone {
				t.Errorf("%s should be done after advisory bypass, status=%s", tc, node.Status)
			}
		})
	}
}

// TestCodeBearingPaths_ExcludesUnresolvedPrefix verifies that paths with the
// "unresolved:" sentinel prefix are not counted as code-bearing.
func TestCodeBearingPaths_ExcludesUnresolvedPrefix(t *testing.T) {
	tmpDir, hgDir := prepProject(t)
	id := "feat-cbtest-unresolved"
	seedFeatureFile(t, hgDir, id, "unresolved:/tmp/foo.yaml")
	seedFeatureFile(t, hgDir, id, "internal/db/lineage_repo.go")

	database, err := dbpkg.Open(filepath.Join(hgDir, ".db", "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	paths, err := dbpkg.CodeBearingPaths(database, id, tmpDir)
	if err != nil {
		t.Fatalf("CodeBearingPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != "internal/db/lineage_repo.go" {
		t.Errorf("expected only in-project path, got %v", paths)
	}
}
