package gate

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/guardprofile"
	"github.com/shakestzd/wipnote/core/storage"
)

func execCapableBase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("GOTMPDIR")
	if base == "" {
		base = os.Getenv("TMPDIR")
	}
	if base == "" {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "wipnote-gotmp-")
	if err != nil {
		t.Fatalf("mkdir exec-capable tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func setupGateTestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{".wipnote/features", ".wipnote/bugs", ".wipnote/spikes", "plugin/config"} {
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

func TestRunSession_WritesSessionLocalRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real Go gate commands")
	}
	projectRoot := setupGateTestProject(t)
	result, err := RunSession(RunOptions{ProjectRoot: projectRoot, SessionID: "sess-gate-pass", Source: "check", Phase: guardprofile.PhaseQuality, Harness: "codex", Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		t.Fatalf("RunSession: %v", err)
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
	if record == nil || record.Status != "pass" || !record.SignatureValid() {
		t.Fatalf("unexpected record: %+v", record)
	}
	if record.Harness != "codex" {
		t.Fatalf("harness = %q, want codex", record.Harness)
	}
}

func TestDetectPlan_NoManifest_IsNoOp(t *testing.T) {
	projectRoot := t.TempDir()
	plan, err := DetectPlan(projectRoot, projectRoot, guardprofile.PhaseQuality)
	if err != nil {
		t.Fatalf("DetectPlan: %v", err)
	}
	if len(plan.Commands) != 0 {
		t.Fatalf("expected zero commands, got %d", len(plan.Commands))
	}
}

func TestResolveWorkItem_FlagTakesPrecedence(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	uniqueDir := t.TempDir()
	uniqueSuffix := filepath.Base(filepath.Dir(uniqueDir)) + "-" + filepath.Base(uniqueDir)
	featureID := "feat-flag-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + uniqueSuffix
	database := openGateTestDB(t, projectRoot)
	_, err := database.Exec(`INSERT INTO features (id, type, title, status, priority, created_at, updated_at)
		VALUES (?, 'feature', 'Flag test', 'done', 'medium', '2026-06-10T00:00:00Z', '2026-06-10T00:01:00Z')`, featureID)
	if err != nil {
		database.Close()
		t.Fatalf("insert feature: %v", err)
	}
	database.Close()
	var stderr strings.Builder
	got := ResolveWorkItem(projectRoot, "sess-any", dbpkg.AgentRootSentinel, featureID, &stderr)
	if got != featureID {
		t.Fatalf("ResolveWorkItem = %q", got)
	}
	if !strings.Contains(stderr.String(), "--work-item flag") {
		t.Fatalf("missing attribution note: %s", stderr.String())
	}
}

func TestReportGuardProfileDrift_ReportsMismatch(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	database := openGateTestDB(t, projectRoot)
	defer database.Close()
	prof := &guardprofile.Profile{Guards: map[string][]guardprofile.Guard{guardprofile.PhaseQuality: {{Name: "g", Cmd: "true"}}}}
	sig := guardprofile.Signature(prof)
	prof.Approved = guardprofile.Approval{Signature: sig, By: "t", At: "2026-01-01T00:00:00Z"}
	if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "guards:\n  quality:\n    - name: g\n      cmd: true\napproved:\n  signature: " + sig + "\n"
	if err := os.WriteFile(filepath.Join(projectRoot, ".wipnote", "guard-profile.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &dbpkg.GateRecord{SessionID: "sess-drift", ProjectType: "go", GateCommand: "true", Status: "pass", Source: "check", ProfileSignature: "sha256:staleoldsignature", GuardsRunJSON: `["g"]`}
	if err := dbpkg.InsertGateRecord(database, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var buf bytes.Buffer
	ReportGuardProfileDrift(database, projectRoot, "sess-drift", &buf)
	if !strings.Contains(buf.String(), "drift") {
		t.Fatalf("expected drift notice, got %q", buf.String())
	}
}

// TestDetectPlan_ApprovedProfile_GoTestCommandIncludesTimeout asserts that when
// an approved guard profile is present, the resolved quality plan's shell-form
// go test command also includes -timeout=300s (bug-a8ae8cd7). DetectPlan renders
// approved-profile guards as ["sh", "-c", g.Cmd], so the timeout must appear in
// the shell command string, not as a separate argv element.
func TestDetectPlan_ApprovedProfile_GoTestCommandIncludesTimeout(t *testing.T) {
	projectRoot := setupGateTestProject(t)

	// Write an approved profile whose go test command includes -timeout=300s.
	prof := &guardprofile.Profile{
		Guards: map[string][]guardprofile.Guard{
			guardprofile.PhaseQuality: {
				{Name: "go-build", Cmd: "go build ./..."},
				{Name: "go-test", Cmd: "go test -buildvcs=false -short " + GoTestTimeoutArg + " ./..."},
			},
		},
	}
	sig := guardprofile.Signature(prof)
	prof.Approved = guardprofile.Approval{Signature: sig, By: "test", At: "2026-01-01T00:00:00Z"}
	yamlContent := "guards:\n  quality:\n    - name: go-build\n      cmd: go build ./...\n" +
		"    - name: go-test\n      cmd: go test -buildvcs=false -short " + GoTestTimeoutArg + " ./...\napproved:\n  signature: " + sig + "\n"
	if err := os.WriteFile(filepath.Join(projectRoot, ".wipnote", "guard-profile.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write guard profile: %v", err)
	}

	plan, err := DetectPlan(projectRoot, projectRoot, guardprofile.PhaseQuality)
	if err != nil {
		t.Fatalf("DetectPlan: %v", err)
	}
	if !plan.UsedProfile {
		t.Fatal("expected DetectPlan to use the approved profile")
	}

	// Approved-profile commands are shell-wrapped: ["sh", "-c", "<cmd>"].
	// The timeout must appear in the shell command string.
	var foundGoTest bool
	for _, cmd := range plan.Commands {
		if len(cmd.Args) >= 3 && cmd.Args[0] == "sh" && cmd.Args[1] == "-c" &&
			strings.Contains(cmd.Args[2], "go test") {
			foundGoTest = true
			if !strings.Contains(cmd.Args[2], GoTestTimeoutArg) {
				t.Fatalf("approved-profile go test shell command missing %s. Command: %q", GoTestTimeoutArg, cmd.Args[2])
			}
		}
	}
	if !foundGoTest {
		t.Fatal("no go test command found in resolved approved-profile plan")
	}
}

func TestDetectPlan_GoProject_TestCommandIncludesTimeout(t *testing.T) {
	projectRoot := setupGateTestProject(t)
	plan, err := DetectPlan(projectRoot, projectRoot, guardprofile.PhaseQuality)
	if err != nil {
		t.Fatalf("DetectPlan: %v", err)
	}
	if len(plan.Commands) == 0 {
		t.Fatal("expected commands in plan")
	}

	// Find the go test command
	var testCmd *Command
	for i := range plan.Commands {
		if plan.Commands[i].Name == "go test" {
			testCmd = &plan.Commands[i]
			break
		}
	}
	if testCmd == nil {
		t.Fatal("expected go test command in plan")
	}

	// Check that -timeout is present and has a value >= 300s
	var hasTimeout bool
	var timeoutValue string
	for i, arg := range testCmd.Args {
		if strings.HasPrefix(arg, "-timeout") {
			hasTimeout = true
			if arg == "-timeout" && i+1 < len(testCmd.Args) {
				timeoutValue = testCmd.Args[i+1]
			} else if strings.Contains(arg, "=") {
				parts := strings.SplitN(arg, "=", 2)
				timeoutValue = parts[1]
			}
			break
		}
	}
	if !hasTimeout {
		t.Fatalf("go test command missing -timeout flag. Args: %v", testCmd.Args)
	}
	if timeoutValue == "" {
		t.Fatalf("go test command has -timeout but no value. Args: %v", testCmd.Args)
	}
	if timeoutValue != "300s" {
		t.Fatalf("go test command timeout = %q, want 300s", timeoutValue)
	}
}
