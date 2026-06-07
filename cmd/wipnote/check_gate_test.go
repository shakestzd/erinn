package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/guardprofile"
	"github.com/shakestzd/wipnote/core/storage"
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
	for _, dir := range []string{"gotmp-exec", "gocache"} {
		if err := os.MkdirAll(filepath.Join(tmpBase, dir), 0o755); err != nil {
			t.Fatalf("mkdir external %s: %v", dir, err)
		}
	}
	t.Setenv("TMPDIR", filepath.Join(tmpBase, "gotmp-exec"))
	t.Setenv("GOTMPDIR", filepath.Join(tmpBase, "gotmp-exec"))
	t.Setenv("GOCACHE", filepath.Join(tmpBase, "gocache"))
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
		Commands:      []string{"go build -buildvcs=false ./...", "go vet ./...", "go test -buildvcs=false ./..."},
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

func TestCheckCompletionGateRecord_AcceptsMatchingSessionAfterRecheck(t *testing.T) {
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

// TestRunSessionGate_NoManifest_PassAndPersist verifies that runSessionGate on a
// manifest-less directory: (a) returns no error, (b) returns a passing result,
// and (c) writes a valid passing gate record to the DB (Fix 1).
func TestRunSessionGate_NoManifest_PassAndPersist(t *testing.T) {
	// Set up a project root that has .wipnote/ structure but NO go.mod /
	// package.json / pyproject.toml / Cargo.toml.
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote", "features"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := runSessionGate(projectRoot, "sess-noop-manifest", "feat-noop", "check", guardprofile.PhaseQuality, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("runSessionGate on manifest-less root: %v", err)
	}
	if !result.Passed {
		t.Fatal("expected no-op plan to pass")
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
	if record.Status != "pass" {
		t.Fatalf("gate record status = %q, want pass", record.Status)
	}
	if !record.SignatureValid() {
		t.Fatal("gate record signature invalid")
	}
	if !strings.Contains(record.OutputSummary, "no-op") {
		t.Fatalf("output summary should indicate no-op, got %q", record.OutputSummary)
	}
}

// TestRunSessionGate_GoProject_StillRuns verifies the regression case: a real Go
// project still runs go build / go vet / go test (Fix 1 must not regress the
// happy path).
func TestRunSessionGate_GoProject_StillRuns(t *testing.T) {
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
