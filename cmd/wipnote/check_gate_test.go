package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/guardprofile"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/shakestzd/wipnote/internal/commitqueue"
	"github.com/shakestzd/wipnote/internal/gate"
)

func setupGateTestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{
		".wipnote/features",
		".wipnote/bugs",
		".wipnote/spikes",
		"plugin/config",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/gatetest\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin", "config", "quality-gate-flake-allowlist.json"), []byte(`[
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
		t.Fatalf("write allowlist: %v", err)
	}
	tmpBase := execCapableBase(t)
	if err := os.MkdirAll(filepath.Join(tmpBase, "gotmp-exec"), 0o755); err != nil {
		t.Fatalf("mkdir external gotmp-exec: %v", err)
	}
	t.Setenv("TMPDIR", filepath.Join(tmpBase, "gotmp-exec"))
	t.Setenv("GOTMPDIR", filepath.Join(tmpBase, "gotmp-exec"))
	// NOTE: GOCACHE is intentionally NOT overridden here. A per-test override
	// gave each gate test a fresh, empty cache that recompiled stdlib (~35s).
	// TestMain now exports one shared warm GOCACHE that the nested `go test`
	// inherits, turning the nested build into a cache hit (~5s).
	return root
}

func openGateTestDB(t *testing.T, projectRoot string) *sql.DB {
	t.Helper()
	dbPath, err := storage.CanonicalDBPath(projectRoot)
	if err != nil {
		t.Fatalf("CanonicalDBPath: %v", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		t.Fatalf("EnsureDBDir: %v", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	return database
}

func TestRunSessionGate_WritesSessionLocalRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}

	projectRoot := setupGateTestProject(t)
	result, err := runSessionGate(projectRoot, "sess-gate-pass", "", "check", guardprofile.PhaseQuality, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("runSessionGate: %v", err)
	}
	if !result.Passed {
		t.Fatal("expected passing gate")
	}

	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	record, err := dbpkg.LatestGateRecordForSession(database, "sess-gate-pass")
	if err != nil {
		t.Fatalf("LatestGateRecordForSession: %v", err)
	}
	if record == nil {
		t.Fatal("expected gate record")
	}
	if record.Status != "pass" {
		t.Fatalf("status = %q, want pass", record.Status)
	}
	if record.ProjectType != "go" {
		t.Fatalf("project type = %q, want go", record.ProjectType)
	}
	if !record.SignatureValid() {
		t.Fatal("expected valid signature")
	}
	if got, want := record.Source, "check"; got != want {
		t.Fatalf("source = %q, want %q", got, want)
	}
	if !strings.Contains(record.GateCommand, "-buildvcs=false") {
		t.Fatalf("gate command = %q, want buildvcs flag", record.GateCommand)
	}
}

func TestMatchGateAllowlist_ListenerSandboxOnly(t *testing.T) {
	entries := []gateAllowlistEntry{
		{
			ID:            "listener-socket-sandbox",
			MatchAll:      []string{"listen tcp", "socket: operation not permitted"},
			Justification: "Test fixture listener sandbox justification",
		},
		{
			ID:            "broad-failure",
			MatchAll:      []string{"socket: operation not permitted"},
			Justification: "Should not match on its own",
		},
	}

	hits := matchGateAllowlist("go test", "listen tcp 127.0.0.1:0: socket: operation not permitted", entries)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d: %+v", len(hits), hits)
	}
	if hits[0].ID != "listener-socket-sandbox" {
		t.Fatalf("first hit = %q, want listener-socket-sandbox", hits[0].ID)
	}
}

func TestRunSessionGate_ReportsAndPersistsAllowlistHits(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	hits := []gateAllowlistHit{{
		ID:            "listener-socket-sandbox",
		Command:       "go test",
		Justification: "Some harnesses forbid listener binds, which makes otherwise healthy Go tests fail with a socket sandbox error instead of a product regression. Allow only this environmental class so the gate reports the sandbox limitation explicitly.",
	}}
	var stdout strings.Builder
	writeGateAllowlistHits(&stdout, hits)
	if !strings.Contains(stdout.String(), "Environment allowlist hits") {
		t.Fatalf("expected allowlist section in stdout, got: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "listener-socket-sandbox") {
		t.Fatalf("expected listener allowlist id in stdout, got: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "socket sandbox error") {
		t.Fatalf("expected justification in stdout, got: %s", stdout.String())
	}

	result := &gateRunResult{
		Plan:          gatePlan{ProjectType: "go"},
		Commands:      []string{"go build -buildvcs=false ./...", "go vet ./...", "go test -buildvcs=false -short ./..."},
		Passed:        false,
		AllowlistHits: hits,
		OutputSummary: "go test failed",
	}
	record, err := persistGateRecord(projectRoot, "sess-gate-listener", "", "check", result)
	if err != nil {
		t.Fatalf("persistGateRecord: %v", err)
	}
	if record == nil {
		t.Fatal("expected persisted gate record")
	}

	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	record, err = dbpkg.LatestGateRecordForSession(database, "sess-gate-listener")
	if err != nil {
		t.Fatalf("LatestGateRecordForSession: %v", err)
	}
	if record == nil {
		t.Fatal("expected persisted gate record")
	}
	if record.AllowlistHitCount != len(result.AllowlistHits) {
		t.Fatalf("allowlist hit count = %d, want %d", record.AllowlistHitCount, len(result.AllowlistHits))
	}
	if !strings.Contains(record.AllowlistHitsJSON, "listener-socket-sandbox") {
		t.Fatalf("allowlist hits JSON = %s, want listener entry", record.AllowlistHitsJSON)
	}
}

func TestGateCommandAllowlisted(t *testing.T) {
	hits := []gateAllowlistHit{{
		ID:            "listener-socket-sandbox",
		Command:       "go test",
		Justification: "listener sandbox",
	}}

	if gateCommandAllowlisted(nil, hits) {
		t.Fatal("nil error must not be treated as allowlisted")
	}
	if gateCommandAllowlisted(os.ErrPermission, nil) {
		t.Fatal("error without allowlist hits must not be treated as allowlisted")
	}
	if !gateCommandAllowlisted(os.ErrPermission, hits) {
		t.Fatal("expected matching error + allowlist hits to be treated as allowlisted")
	}
}

func TestLoadGateAllowlist_RequiresJustification(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "plugin", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "plugin", "config", "quality-gate-flake-allowlist.json"), []byte(`[
  {
    "id": "tmp-noexec",
    "match_all": ["/tmp/", "permission denied"],
    "justification": ""
  }
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadGateAllowlist(projectRoot)
	if err == nil {
		t.Fatal("expected missing justification to fail")
	}
	if !strings.Contains(err.Error(), "missing justification") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckCompletionGateRecord_RequiresCurrentSessionRecord(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	if _, err := database.Exec(`INSERT OR REPLACE INTO feature_files (id, feature_id, file_path, operation, session_id) VALUES (?, ?, ?, ?, ?)`,
		"ff-1", "feat-gate", "main.go", "write", "sess-prev"); err != nil {
		t.Fatalf("insert feature file: %v", err)
	}

	err := checkCompletionGateRecord(database, projectRoot, "sess-current", "feat-gate")
	if err == nil {
		t.Fatal("expected completion gate refusal without current-session record")
	}
	if !strings.Contains(err.Error(), "wipnote check --gate") {
		t.Fatalf("expected remediation command, got: %v", err)
	}
}

func TestFailIfPendingDeferredArtifactCommits(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	tmpOutbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpOutbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })

	ob, err := openCommitOutbox(projectRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	if err := ob.Append(commitqueue.Intent{
		RepoRoot:   projectRoot,
		RelPaths:   []string{".wipnote/features/feat-gate.html"},
		Message:    "wipnote: complete feat-gate",
		WorkItemID: "feat-gate",
		Action:     "complete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	err = failIfPendingDeferredArtifactCommits(projectRoot, "feat-gate", &strings.Builder{})
	if err == nil {
		t.Fatal("expected pending deferred artifact commit to block the gate")
	}
	if !strings.Contains(err.Error(), "wipnote commit-queue flush") {
		t.Fatalf("error should suggest flush remediation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "feat-gate") {
		t.Fatalf("error should mention the pending work item, got: %v", err)
	}
}

// TestFailIfPendingDeferredArtifactCommits_UnrelatedWorkItemDoesNotBlock is
// the regression test for bug-a5a846bc (#150): a pending deferred artifact
// commit intent that belongs to a DIFFERENT (unrelated, e.g. previously
// completed) work item must not block the gate for the current item — it
// should only surface as a non-blocking advisory.
func TestFailIfPendingDeferredArtifactCommits_UnrelatedWorkItemDoesNotBlock(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	tmpOutbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpOutbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })

	ob, err := openCommitOutbox(projectRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	if err := ob.Append(commitqueue.Intent{
		RepoRoot:   projectRoot,
		RelPaths:   []string{".wipnote/features/feat-old-unrelated.html"},
		Message:    "wipnote: complete feat-old-unrelated",
		WorkItemID: "feat-old-unrelated",
		Action:     "complete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var advisory strings.Builder
	err = failIfPendingDeferredArtifactCommits(projectRoot, "feat-current", &advisory)
	if err != nil {
		t.Fatalf("unrelated work item's deferred intent must not block the gate, got: %v", err)
	}
	if !strings.Contains(advisory.String(), "advisory:") {
		t.Fatalf("expected non-blocking advisory line, got: %q", advisory.String())
	}
	if !strings.Contains(advisory.String(), "1 repo-wide pending") {
		t.Fatalf("expected advisory to report repo-wide pending count, got: %q", advisory.String())
	}
}

// TestFailIfPendingDeferredArtifactCommits_EmptyWorkItemDoesNotBlock verifies
// that when no work item can be resolved for the current gate run (empty
// workItemID), repo-wide backlog is reported as an advisory only, never as a
// block — there is no current item to scope the check to.
func TestFailIfPendingDeferredArtifactCommits_EmptyWorkItemDoesNotBlock(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	tmpOutbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpOutbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })

	ob, err := openCommitOutbox(projectRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	if err := ob.Append(commitqueue.Intent{
		RepoRoot:   projectRoot,
		RelPaths:   []string{".wipnote/features/feat-gate.html"},
		Message:    "wipnote: complete feat-gate",
		WorkItemID: "feat-gate",
		Action:     "complete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := failIfPendingDeferredArtifactCommits(projectRoot, "", &strings.Builder{}); err != nil {
		t.Fatalf("empty work item id must not block on repo-wide backlog, got: %v", err)
	}
}

func TestFailIfPendingDeferredArtifactCommits_NormalizesWindowsPaths(t *testing.T) {
	err := failIfPendingDeferredArtifactCommitsWithIntentForTest(
		t,
		commitqueue.Intent{
			RelPaths:   []string{`.wipnote\features\feat-win.html`},
			Message:    "wipnote: complete feat-win",
			WorkItemID: "feat-win",
			Action:     "complete",
		},
	)
	if err == nil {
		t.Fatal("expected Windows-style deferred artifact path to block the gate")
	}
	if !strings.Contains(err.Error(), "feat-win") {
		t.Fatalf("error should mention the pending work item, got: %v", err)
	}
}

func TestFailIfPendingDeferredArtifactCommits_BlocksDeadLetteredArtifactIntent(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	tmpOutbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpOutbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })

	ob, err := openCommitOutbox(projectRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	if err := ob.Append(commitqueue.Intent{
		RepoRoot:   projectRoot,
		RelPaths:   []string{".wipnote/features/feat-dead.html"},
		Message:    "wipnote: complete feat-dead",
		WorkItemID: "feat-dead",
		Action:     "complete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	res, err := ob.Flush(func(commitqueue.Intent) error {
		return fmt.Errorf("forced failure")
	}, 1)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if res.DeadLettered != 1 {
		t.Fatalf("DeadLettered = %d, want 1", res.DeadLettered)
	}

	err = failIfPendingDeferredArtifactCommits(projectRoot, "feat-dead", &strings.Builder{})
	if err == nil {
		t.Fatal("expected dead-lettered deferred artifact commit to block the gate")
	}
	if !strings.Contains(err.Error(), "feat-dead") {
		t.Fatalf("error should mention the dead-lettered work item, got: %v", err)
	}
	if strings.Contains(err.Error(), "commit-queue flush") {
		t.Fatalf("dead-letter-only error should not suggest flush remediation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "clear the dead-letter entry") {
		t.Fatalf("dead-letter error should explain dead-letter remediation, got: %v", err)
	}
}

func TestFailIfPendingDeferredArtifactCommits_IgnoresCleanDeadLetteredArtifactIntent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-deadletter-clean-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	projectRoot := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", "feat-clean.html"), []byte(`<article id="feat-clean"></article>`), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	gitMustCommitInitial(t, projectRoot)

	tmpOutbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpOutbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })

	ob, err := openCommitOutbox(projectRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	if err := ob.Append(commitqueue.Intent{
		RepoRoot:   projectRoot,
		RelPaths:   []string{".wipnote/features/feat-clean.html"},
		Message:    "wipnote: complete feat-clean",
		WorkItemID: "feat-clean",
		Action:     "complete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	res, err := ob.Flush(func(commitqueue.Intent) error {
		return fmt.Errorf("forced failure")
	}, 1)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if res.DeadLettered != 1 {
		t.Fatalf("DeadLettered = %d, want 1", res.DeadLettered)
	}

	if err := failIfPendingDeferredArtifactCommits(projectRoot, "feat-clean", &strings.Builder{}); err != nil {
		t.Fatalf("clean dead-lettered artifact should not block the gate: %v", err)
	}
}

func TestFailIfPendingDeferredArtifactCommits_BlocksDeadLetteredArtifactIntentWithWindowsPathWhenSlashPathDirty(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wipnote-deadletter-win-*")
	if err != nil {
		t.Fatalf("MkdirTemp /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	projectRoot := setupWorktreeGitRepoIn(t, tmpDir)
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}
	artifactPath := filepath.Join(wipnoteDir, "features", "feat-win.html")
	if err := os.WriteFile(artifactPath, []byte(`<article id="feat-win">clean</article>`), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	gitMustCommitInitial(t, projectRoot)
	if err := os.WriteFile(artifactPath, []byte(`<article id="feat-win">dirty</article>`), 0o644); err != nil {
		t.Fatalf("rewrite artifact: %v", err)
	}

	tmpOutbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpOutbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })

	ob, err := openCommitOutbox(projectRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	if err := ob.Append(commitqueue.Intent{
		RepoRoot:   projectRoot,
		RelPaths:   []string{`.wipnote\features\feat-win.html`},
		Message:    "wipnote: complete feat-win",
		WorkItemID: "feat-win",
		Action:     "complete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	res, err := ob.Flush(func(commitqueue.Intent) error {
		return fmt.Errorf("forced failure")
	}, 1)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if res.DeadLettered != 1 {
		t.Fatalf("DeadLettered = %d, want 1", res.DeadLettered)
	}

	err = failIfPendingDeferredArtifactCommits(projectRoot, "feat-win", &strings.Builder{})
	if err == nil {
		t.Fatal("expected dirty slash-path artifact to block the dead-letter gate even when the intent path uses backslashes")
	}
	if !strings.Contains(err.Error(), "feat-win") {
		t.Fatalf("error should mention the dead-lettered work item, got: %v", err)
	}
}

func TestCheckCmd_GateFailsWhenDeferredArtifactIntentPending(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	tmpOutbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpOutbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })

	ob, err := openCommitOutbox(projectRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	if err := ob.Append(commitqueue.Intent{
		RepoRoot:   projectRoot,
		RelPaths:   []string{".wipnote/features/feat-gate.html"},
		Message:    "wipnote: complete feat-gate",
		WorkItemID: "feat-gate",
		Action:     "complete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	projectDirFlag = projectRoot
	t.Cleanup(func() { projectDirFlag = "" })
	cmd := checkCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// #150 scopes the block to the resolved work item: pass --work-item
	// explicitly so it matches the pending intent's WorkItemID and the
	// expected block still occurs deterministically in this test.
	cmd.SetArgs([]string{"--gate", "--work-item", "feat-gate"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected --gate to fail when deferred artifact intents are pending")
	}
	if !strings.Contains(err.Error(), "quality gate blocked by 1 unresolved deferred work-item artifact commit intent") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "wipnote commit-queue flush") {
		t.Fatalf("error should suggest flush remediation, got: %v", err)
	}
}

func failIfPendingDeferredArtifactCommitsWithIntentForTest(t *testing.T, intent commitqueue.Intent) error {
	t.Helper()
	projectRoot := setupGateTestProject(t)
	tmpOutbox := t.TempDir()
	origOutboxPath := commitOutboxPath
	commitOutboxPath = func(string) (string, error) {
		return filepath.Join(tmpOutbox, "commit-outbox.ndjson"), nil
	}
	t.Cleanup(func() { commitOutboxPath = origOutboxPath })

	ob, err := openCommitOutbox(projectRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	intent.RepoRoot = projectRoot
	if err := ob.Append(intent); err != nil {
		t.Fatalf("Append: %v", err)
	}
	return failIfPendingDeferredArtifactCommits(projectRoot, intent.WorkItemID, &strings.Builder{})
}

func TestCheckCompletionGateRecord_AcceptsMatchingSessionAfterRecheck(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}

	projectRoot := setupGateTestProject(t)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	if _, err := database.Exec(`INSERT OR REPLACE INTO feature_files (id, feature_id, file_path, operation, session_id) VALUES (?, ?, ?, ?, ?)`,
		"ff-2", "feat-gate", "main.go", "write", "sess-gate-ok"); err != nil {
		t.Fatalf("insert feature file: %v", err)
	}

	initial, err := runSessionGate(projectRoot, "sess-gate-ok", "feat-gate", "check", guardprofile.PhaseQuality, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("initial runSessionGate: %v", err)
	}
	if !initial.Passed || initial.Record == nil {
		t.Fatalf("expected initial passing record, got %+v", initial)
	}

	if err := checkCompletionGateRecord(database, projectRoot, "sess-gate-ok", "feat-gate"); err != nil {
		t.Fatalf("expected matching gate record to pass, got: %v", err)
	}

	count, err := dbpkg.CountGateRecords(database, "sess-gate-ok")
	if err != nil {
		t.Fatalf("CountGateRecords: %v", err)
	}
	if count < 2 {
		t.Fatalf("expected recheck to write a second gate record, got %d", count)
	}
}

func TestLoadGateAllowlist_MissingFileReturnsEmpty(t *testing.T) {
	projectRoot := t.TempDir()
	entries, err := loadGateAllowlist(projectRoot)
	if err != nil {
		t.Fatalf("loadGateAllowlist: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected nil or empty allowlist, got %d entries", len(entries))
	}
}

// TestDetectGatePlan_NoManifest_IsNoOp verifies that a directory with no
// recognised project manifest resolves to a zero-command passing plan rather
// than a hard error (Fix 1).
func TestDetectGatePlan_NoManifest_IsNoOp(t *testing.T) {
	projectRoot := t.TempDir() // empty dir — no go.mod, package.json, etc.
	plan, err := detectGatePlan(projectRoot, projectRoot, guardprofile.PhaseQuality)
	if err != nil {
		t.Fatalf("detectGatePlan on manifest-less dir returned error: %v", err)
	}
	if len(plan.Commands) != 0 {
		t.Fatalf("expected zero commands for no-op plan, got %d", len(plan.Commands))
	}
}

// TestRunSessionGate_NoManifest_SkippedNotPassed is the regression test for
// bug-1b2b1529 (#153): runSessionGate on a manifest-less directory must NOT
// record a silent PASS. It should (a) return no error (recording state is
// not itself a failure), (b) return a non-passing, Skipped result, and (c)
// persist a "skipped" (not "pass") gate record to the DB, so a no-op gate run
// can never be mistaken for a validated green gate.
func TestRunSessionGate_NoManifest_SkippedNotPassed(t *testing.T) {
	// Set up a project root that has .wipnote/ structure but NO go.mod /
	// package.json / pyproject.toml / Cargo.toml / mix.exs.
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote", "features"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	result, err := runSessionGate(projectRoot, "sess-noop-manifest", "feat-noop", "check", guardprofile.PhaseQuality, os.Stdout, &stderr)
	if err != nil {
		t.Fatalf("runSessionGate on manifest-less root: %v", err)
	}
	if result.Passed {
		t.Fatal("expected manifest-less gate run to NOT pass")
	}
	if !result.Skipped {
		t.Fatal("expected manifest-less gate run to be marked Skipped")
	}
	if !strings.Contains(stderr.String(), "WARN") {
		t.Fatalf("expected a loud WARN on stderr, got: %q", stderr.String())
	}

	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	record, err := dbpkg.LatestGateRecordForSession(database, "sess-noop-manifest")
	if err != nil {
		t.Fatalf("LatestGateRecordForSession: %v", err)
	}
	if record == nil {
		t.Fatal("expected gate record to be persisted")
	}
	// gate_records.status is DB-constrained to ('pass','fail'); a skipped run
	// persists as "fail" — never "pass" — so it can't be mistaken for a
	// validated green gate. The Skipped/WARN assertions above are what
	// distinguish "nothing ran" from "something ran and failed" for callers
	// inspecting the in-memory RunResult.
	if record.Status != "fail" {
		t.Fatalf("gate record status = %q, want fail", record.Status)
	}
	if !record.SignatureValid() {
		t.Fatal("gate record signature invalid")
	}
	if !strings.Contains(record.OutputSummary, "skipped") {
		t.Fatalf("output summary should indicate skipped, got %q", record.OutputSummary)
	}
}

// TestRunSessionGate_ElixirProject_RunsMixGate is the regression test for
// bug-1b2b1529 (#153): a project with mix.exs at its root must run the
// Elixir mix gate (compile --warnings-as-errors, test, format
// --check-formatted) rather than falling through to the no-manifest skip
// path.
func TestRunSessionGate_ElixirProject_RunsMixGate(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote", "features"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "mix.exs"), []byte("defmodule Test.MixProject do\nend\n"), 0o644); err != nil {
		t.Fatalf("write mix.exs: %v", err)
	}

	plan, err := detectGatePlan(projectRoot, projectRoot, guardprofile.PhaseQuality)
	if err != nil {
		t.Fatalf("detectGatePlan: %v", err)
	}
	if plan.ProjectType != "elixir" {
		t.Fatalf("project type = %q, want elixir", plan.ProjectType)
	}
	var names []string
	for _, c := range plan.Commands {
		names = append(names, c.Name)
	}
	wantAny := func(sub string) bool {
		for _, n := range names {
			if strings.Contains(n, sub) {
				return true
			}
		}
		return false
	}
	if !wantAny("mix compile") {
		t.Errorf("expected mix compile in plan commands, got %v", names)
	}
	if !wantAny("mix test") {
		t.Errorf("expected mix test in plan commands, got %v", names)
	}
	if !wantAny("mix format") {
		t.Errorf("expected mix format --check-formatted in plan commands, got %v", names)
	}
}

// TestRunSessionGate_GoProject_StillRuns verifies the regression case: a real Go
// project still runs go build / go vet / go test (Fix 1 must not regress the
// happy path).
func TestRunSessionGate_GoProject_StillRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}

	projectRoot := setupGateTestProject(t) // creates go.mod + main.go
	result, err := runSessionGate(projectRoot, "sess-go-regression", "", "check", guardprofile.PhaseQuality, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("runSessionGate on Go project: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected Go project gate to pass, summary: %s", result.OutputSummary)
	}
	if len(result.Commands) == 0 {
		t.Fatal("expected Go gate to run commands")
	}
	hasGoTest := false
	for _, cmd := range result.Commands {
		if strings.Contains(cmd, "go test") {
			hasGoTest = true
		}
	}
	if !hasGoTest {
		t.Fatalf("expected go test in gate commands, got %v", result.Commands)
	}
}

// TestDetectGatePlan_SelectsPhaseGuards is the regression for roborev #3703:
// gate-plan detection must honor the requested phase, so `check --gate`
// (PhaseQuality) and the completion re-check (PhaseCompletion) run different
// guard groups. Previously both resolved PhaseQuality and the completion phase
// was never enforced.
func TestDetectGatePlan_SelectsPhaseGuards(t *testing.T) {
	root := t.TempDir()
	p := &guardprofile.Profile{Guards: map[string][]guardprofile.Guard{
		guardprofile.PhaseQuality:    {{Name: "q-test", Cmd: "echo quality"}},
		guardprofile.PhaseCompletion: {{Name: "c-test", Cmd: "echo completion"}},
	}}
	p.Approved = guardprofile.Approval{Signature: guardprofile.Signature(p), By: "t", At: "2026-01-01T00:00:00Z"}
	if err := writeGuardProfile(root, p); err != nil {
		t.Fatal(err)
	}

	qPlan, err := detectGatePlan(root, root, guardprofile.PhaseQuality)
	if err != nil {
		t.Fatalf("quality detectGatePlan: %v", err)
	}
	if len(qPlan.GuardNames) != 1 || qPlan.GuardNames[0] != "q-test" {
		t.Errorf("quality phase guards = %v, want [q-test]", qPlan.GuardNames)
	}

	cPlan, err := detectGatePlan(root, root, guardprofile.PhaseCompletion)
	if err != nil {
		t.Fatalf("completion detectGatePlan: %v", err)
	}
	if len(cPlan.GuardNames) != 1 || cPlan.GuardNames[0] != "c-test" {
		t.Errorf("completion phase guards = %v, want [c-test]", cPlan.GuardNames)
	}
}

func TestNodeGateCommands_SkipsMissingScripts(t *testing.T) {
	projectRoot := t.TempDir()
	manifest := `{
  "name": "node-gate",
  "scripts": {
    "build": "echo build",
    "test": "echo test"
  }
}`
	if err := os.WriteFile(filepath.Join(projectRoot, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	commands, err := nodeGateCommands(filepath.Join(projectRoot, "package.json"))
	if err != nil {
		t.Fatalf("nodeGateCommands: %v", err)
	}
	if len(commands) != 2 {
		t.Fatalf("expected 2 commands, got %d: %+v", len(commands), commands)
	}
	if commands[0].Name != "npm run build" || commands[1].Name != "npm test" {
		t.Fatalf("unexpected command order: %+v", commands)
	}
	for _, cmd := range commands {
		if cmd.Name == "npm run lint" {
			t.Fatalf("lint script should have been skipped: %+v", commands)
		}
	}
}

func TestDetectGatePlan_NodeProjectWithMissingScripts(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "package.json"), []byte(`{
  "name": "node-gate",
  "scripts": {
    "lint": "echo lint"
  }
}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	plan, err := detectGatePlan(projectRoot, projectRoot, guardprofile.PhaseQuality)
	if err != nil {
		t.Fatalf("detectGatePlan: %v", err)
	}
	if plan.ProjectType != "node" {
		t.Fatalf("project type = %q, want node", plan.ProjectType)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d: %+v", len(plan.Commands), plan.Commands)
	}
	if plan.Commands[0].Name != "npm run lint" {
		t.Fatalf("command = %q, want npm run lint", plan.Commands[0].Name)
	}
}

// TestPersistGateRecord_ExplicitWorkItemID verifies that when a non-empty
// workItemID is provided, persistGateRecord stores it on the record verbatim
// (feat-cecb2f2b: --work-item flag path).
func TestPersistGateRecord_ExplicitWorkItemID(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	result := &gateRunResult{
		Plan:          gatePlan{ProjectType: "go"},
		Commands:      []string{"go build -buildvcs=false ./..."},
		Passed:        true,
		AllowlistHits: nil,
		OutputSummary: "all commands passed",
	}
	const wantWorkItem = "feat-explicit-123"
	record, err := persistGateRecord(projectRoot, "sess-explicit-wi", wantWorkItem, "check", result)
	if err != nil {
		t.Fatalf("persistGateRecord: %v", err)
	}
	if record == nil {
		t.Fatal("expected a record")
	}
	if record.WorkItemID != wantWorkItem {
		t.Errorf("work_item_id = %q, want %q", record.WorkItemID, wantWorkItem)
	}
	if !record.SignatureValid() {
		t.Error("signature invalid after explicit work item set")
	}

	database := openGateTestDB(t, projectRoot)
	defer database.Close()
	stored, err := dbpkg.LatestGateRecordForSession(database, "sess-explicit-wi")
	if err != nil {
		t.Fatalf("LatestGateRecordForSession: %v", err)
	}
	if stored == nil {
		t.Fatal("expected stored record")
	}
	if stored.WorkItemID != wantWorkItem {
		t.Errorf("stored work_item_id = %q, want %q", stored.WorkItemID, wantWorkItem)
	}
}

// TestResolveGateWorkItem_FlagTakesPrecedence verifies that an explicit
// --work-item flag value overrides session-resolved attribution
// (feat-cecb2f2b: resolution path 1).
func TestResolveGateWorkItem_FlagTakesPrecedence(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	// Seed the work item so the --work-item flag validates as existing
	// (bug-fddf5820, finding 4: the flag is now checked against the DB). Seed it
	// as 'done' so it cannot be picked up by the most-recent-in-progress
	// fallback path and pollute sibling tests that share the read index.
	database := openGateTestDB(t, projectRoot)
	_, err := database.Exec(`INSERT INTO features (id, type, title, status, priority, created_at, updated_at)
		VALUES ('feat-flag-explicit', 'feature', 'Flag test', 'done', 'medium', '2026-06-10T00:00:00Z', '2026-06-10T00:01:00Z')`)
	if err != nil {
		database.Close()
		t.Fatalf("insert feature: %v", err)
	}
	database.Close()

	var stderr strings.Builder
	got := resolveGateWorkItem(projectRoot, "sess-any", dbpkg.AgentRootSentinel, "feat-flag-explicit", &stderr)
	if got != "feat-flag-explicit" {
		t.Errorf("resolveGateWorkItem with flag = %q, want feat-flag-explicit", got)
	}
	if !strings.Contains(stderr.String(), "--work-item flag") {
		t.Errorf("expected --work-item flag attribution in stderr, got: %s", stderr.String())
	}
}

// TestResolveGateWorkItem_FlagNonexistentWarns verifies that an explicit
// --work-item ID that does not exist in the project index is still recorded
// (preserving the return value) but emits a not-found warning rather than
// silently accepting it (bug-fddf5820, finding 4).
func TestResolveGateWorkItem_FlagNonexistentWarns(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	// Force creation of the DB so validation has an index to consult.
	openGateTestDB(t, projectRoot).Close()

	var stderr strings.Builder
	got := resolveGateWorkItem(projectRoot, "sess-any", dbpkg.AgentRootSentinel, "feat-does-not-exist", &stderr)
	if got != "feat-does-not-exist" {
		t.Errorf("resolveGateWorkItem with unknown flag = %q, want feat-does-not-exist", got)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("expected not-found warning in stderr, got: %s", stderr.String())
	}
}

// TestResolveGateWorkItem_FallbackToMostRecentInProgress verifies that when
// neither the --work-item flag nor session attribution resolves a work item,
// the last-resort fallback finds the most recent in-progress item
// (feat-cecb2f2b: resolution path 3).
func TestResolveGateWorkItem_FallbackToMostRecentInProgress(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	// Seed an in-progress feature directly into the DB.
	database := openGateTestDB(t, projectRoot)
	_, err := database.Exec(`INSERT INTO features (id, type, title, status, priority, created_at, updated_at)
		VALUES ('feat-fallback-latest', 'feature', 'Fallback test', 'in-progress', 'medium', '2026-06-10T00:00:00Z', '2026-06-10T00:01:00Z')`)
	if err != nil {
		database.Close()
		t.Fatalf("insert feature: %v", err)
	}
	database.Close()

	var stderr strings.Builder
	// Use a session that has no active work item claim and no flag value.
	got := resolveGateWorkItem(projectRoot, "sess-no-claim", dbpkg.AgentRootSentinel, "", &stderr)
	if got != "feat-fallback-latest" {
		t.Errorf("resolveGateWorkItem fallback = %q, want feat-fallback-latest", got)
	}
	if !strings.Contains(stderr.String(), "most recent in-progress") {
		t.Errorf("expected last-resort attribution in stderr, got: %s", stderr.String())
	}
}

// TestValidateCompletionGateRecord_CrossSessionMatchByWorkItemID verifies that
// a gate record from a different session can satisfy the completion gate when
// the work_item_id matches — the bug-35857288 cross-session fallback with
// non-empty attribution (feat-cecb2f2b: the empty work_item_id prevented this
// path from working before).
func TestValidateCompletionGateRecord_CrossSessionMatchByWorkItemID(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}

	projectRoot := setupGateTestProject(t)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()

	// Insert a passing gate record attributed to the work item but from
	// a DIFFERENT session than the one that will call validateCompletionGateRecord.
	// Do not pre-call EnsureSignature() — InsertGateRecord sets CheckedAt then
	// computes the signature to keep them consistent.
	priorRecord := &dbpkg.GateRecord{
		SessionID:     "sess-gate-producer",
		WorkItemID:    "feat-cross-session-test",
		ProjectType:   "go",
		GateCommand:   "go build ./...",
		Status:        "pass",
		Source:        "check",
		OutputSummary: "all commands passed",
	}
	if err := dbpkg.InsertGateRecord(database, priorRecord); err != nil {
		t.Fatalf("insert prior record: %v", err)
	}

	// The completing session is different. With empty work_item_id the
	// cross-session fallback would fail; with the explicit work item it must
	// find the record, then run the re-check gate and pass.
	// validateCompletionGateRecord calls runSessionGate internally for the
	// re-check; it runs against projectRoot (a real Go project from setupGateTestProject).
	err := validateCompletionGateRecord(projectRoot, database, "sess-completing", "feat-cross-session-test")
	if err != nil {
		t.Fatalf("expected cross-session match to pass, got: %v", err)
	}
}

// TestRunGoGates_GoTestIncludesTimeout verifies that runGoGates (the non-gate
// wipnote check path) builds a go test command that includes -timeout=300s
// (bug-a8ae8cd7). It does so by intercepting exec via a fake "go" script on
// PATH that records its argv to a file, then inspects what was captured.
func TestRunGoGates_GoTestIncludesTimeout(t *testing.T) {
	// Set up a temporary directory for the fake "go" binary.
	fakeDir := t.TempDir()
	argsFile := filepath.Join(fakeDir, "recorded-args.txt")

	// Write a fake "go" shell script that appends argv to argsFile and exits 0.
	fakeGo := filepath.Join(fakeDir, "go")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argsFile + "\nexit 0\n"
	if err := os.WriteFile(fakeGo, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	// Prepend fakeDir to PATH so our fake "go" is found first.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeDir+":"+origPath)

	// runGoGates uses goDir = filepath.Join(root, "packages", "go"), so create it.
	projectRoot := t.TempDir()
	goDir := filepath.Join(projectRoot, "packages", "go")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatalf("mkdir goDir: %v", err)
	}

	ctx := context.Background()
	_ = runGoGates(ctx, projectRoot, false /* skipTests */)

	// Read the recorded args.
	argsData, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	recorded := string(argsData)

	// The go test invocation must include -timeout=300s.
	if !strings.Contains(recorded, gate.GoTestTimeoutArg) {
		t.Fatalf("runGoGates go test invocation missing %s. Recorded args:\n%s",
			gate.GoTestTimeoutArg, recorded)
	}
}

// TestIsWipnoteSelfRepo_MatchesOwnModule and its sibling below are the
// regression tests for bug-b3d49476 (#154): the internal launch-readiness
// roster must only surface inside wipnote's own repository, detected by
// comparing the project's go.mod module line against wipnote's own module
// path.
func TestIsWipnoteSelfRepo_MatchesOwnModule(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "go.mod"), []byte("module github.com/shakestzd/wipnote\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if !isWipnoteSelfRepo(projectRoot) {
		t.Fatal("expected wipnote's own module path to be recognised as the self repo")
	}
}

func TestIsWipnoteSelfRepo_UnrelatedProjectIsFalse(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "go.mod"), []byte("module example.com/some-user-project\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if isWipnoteSelfRepo(projectRoot) {
		t.Fatal("expected unrelated user project module path to NOT be recognised as the self repo")
	}
}

func TestIsWipnoteSelfRepo_NoGoModIsFalse(t *testing.T) {
	projectRoot := t.TempDir()
	if isWipnoteSelfRepo(projectRoot) {
		t.Fatal("expected a directory with no go.mod to NOT be recognised as the self repo")
	}
}

// TestCheckCmd_GatePassing_DoesNotLeakLaunchReadinessRosterInUnrelatedProject
// is the end-to-end regression test for bug-b3d49476 (#154): a passing
// `check --gate` run in an unrelated (non-wipnote) Go project must not print
// wipnote's own internal launch-readiness roster (its plan-*, `go test
// ./cmd/wipnote/` directive).
func TestCheckCmd_GatePassing_DoesNotLeakLaunchReadinessRosterInUnrelatedProject(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}

	projectRoot := setupGateTestProject(t) // module example.com/gatetest — NOT wipnote's own module
	projectDirFlag = projectRoot
	t.Cleanup(func() { projectDirFlag = "" })
	cmd := checkCmd()
	cmd.SetArgs([]string{"--gate"})

	var execErr error
	// printContentionGateReminder writes straight to os.Stdout (not the
	// cobra-scoped writer), so it must be captured via a real stdout pipe to
	// actually exercise the leak this test guards against.
	captured := captureStdout(t, func() {
		execErr = cmd.Execute()
	})
	if execErr != nil {
		t.Fatalf("expected --gate to pass for a clean example.com project, got: %v", execErr)
	}
	if strings.Contains(captured, "Launch readiness") {
		t.Fatalf("unrelated project's --gate output must not leak wipnote's internal launch-readiness roster, got: %q", captured)
	}
}
