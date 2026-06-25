package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corearch "github.com/shakestzd/wipnote/core/arch"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	iarch "github.com/shakestzd/wipnote/internal/arch"
)

// setupArchTestDir creates a temporary directory with a .wipnote structure
// that findWipnoteDir can locate.
func setupArchTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("create .wipnote: %v", err)
	}
	// Override the project dir so findWipnoteDir resolves to our temp dir.
	t.Setenv("WIPNOTE_PROJECT_DIR", dir)
	return dir
}

// runArch executes an arch subcommand against a test .wipnote directory.
func runArch(t *testing.T, args ...string) error {
	t.Helper()
	root := buildRoot()
	root.SetArgs(append([]string{"arch"}, args...))
	return root.Execute()
}

// runArchCapture executes an arch subcommand and returns captured stdout plus any error.
func runArchCapture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := buildRoot()
	root.SetOut(&buf)
	root.SetArgs(append([]string{"arch"}, args...))
	err := root.Execute()
	return buf.String(), err
}

func TestArchAdd_HappyPath(t *testing.T) {
	dir := setupArchTestDir(t)

	err := runArch(t,
		"add", "auth-invariant",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "Auth tokens must never be logged in plaintext.",
	)
	if err != nil {
		t.Fatalf("arch add: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".wipnote", corearch.LedgerFilename)); err != nil {
		t.Fatalf("expected architecture ledger: %v", err)
	}
}

func TestArchAdd_InvalidKind(t *testing.T) {
	setupArchTestDir(t)

	err := runArch(t,
		"add", "some-card",
		"--kind", "not-a-kind",
		"--created-by", "agent",
		"--body", "Body here.",
	)
	if err == nil {
		t.Fatal("expected error for invalid kind")
	}
}

func TestArchAdd_DuplicateSlug(t *testing.T) {
	setupArchTestDir(t)

	args := []string{
		"add", "my-card",
		"--kind", "hazard",
		"--created-by", "agent",
		"--body", "Body here.",
	}
	if err := runArch(t, args...); err != nil {
		t.Fatalf("first add: %v", err)
	}
	err := runArch(t, args...)
	if err == nil {
		t.Fatal("expected error on duplicate slug")
	}
}

func TestArchList_Empty(t *testing.T) {
	setupArchTestDir(t)
	if err := runArch(t, "list"); err != nil {
		t.Fatalf("arch list (empty): %v", err)
	}
}

func TestArchShow_NotFound(t *testing.T) {
	setupArchTestDir(t)
	err := runArch(t, "show", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent card")
	}
}

func TestArchShow_HappyPath(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "db-hazard",
		"--kind", "hazard",
		"--created-by", "agent",
		"--body", "Never run raw SQL against the replica.",
	); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := runArch(t, "show", "db-hazard"); err != nil {
		t.Fatalf("show: %v", err)
	}
}

func TestArchEdit_HappyPath(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "edit-test",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "Original body.",
	); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := runArch(t, "edit", "edit-test", "--body", "Updated body."); err != nil {
		t.Fatalf("edit: %v", err)
	}
}

func TestArchValidate_Valid(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "good-card",
		"--kind", "decision",
		"--created-by", "agent",
		"--body", "We use SQLite as the read index.",
	); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := runArch(t, "validate", "good-card"); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestArchDeprecate_WithSuccessor(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "old-card",
		"--kind", "hazard",
		"--created-by", "agent",
		"--body", "Old hazard.",
	); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := runArch(t, "deprecate", "old-card", "--superseded-by", "new-card"); err != nil {
		t.Fatalf("deprecate: %v", err)
	}
}

func TestArchDeprecate_Outright(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "retire-me",
		"--kind", "hazard",
		"--created-by", "agent",
		"--body", "This will be retired.",
	); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := runArch(t, "deprecate", "retire-me"); err != nil {
		t.Fatalf("deprecate outright: %v", err)
	}
}

func TestArchList_HidesRetiredByDefault(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "active-one",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "Active invariant.",
	); err != nil {
		t.Fatalf("add active: %v", err)
	}

	if err := runArch(t,
		"add", "retired-one",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "Soon retired.",
	); err != nil {
		t.Fatalf("add retired: %v", err)
	}

	if err := runArch(t, "deprecate", "retired-one"); err != nil {
		t.Fatalf("deprecate: %v", err)
	}

	// Run list (default — no --all) and verify filtering.
	out, err := runArchCapture(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "active-one") {
		t.Errorf("list output missing active-one:\n%s", out)
	}
	if strings.Contains(out, "retired-one") {
		t.Errorf("list output should not show retired-one:\n%s", out)
	}
}

func TestArchBodyWordLimitRejected(t *testing.T) {
	setupArchTestDir(t)

	longBody := strings.Repeat("word ", 121)
	err := runArch(t,
		"add", "too-long",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", longBody,
	)
	if err == nil {
		t.Fatal("expected error for body exceeding 120-word limit")
	}
}

// TestArchVerify tests the `arch verify` subcommand which re-pins verified_at
// to the current HEAD SHA.
func TestArchVerify_RePinsHead(t *testing.T) {
	dir := setupArchTestDir(t)

	// Create a card with an old verified_at.
	if err := runArch(t,
		"add", "verify-test",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "This card will be verified.",
		"--verified-at", "deadbeef1234567",
	); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Run verify — it should succeed even without a real git repo (falls back gracefully).
	// In unit tests there is no git repo, so headSHA will be empty.
	err := runArch(t, "verify", "verify-test")
	if err != nil {
		t.Fatalf("arch verify: %v", err)
	}

	// The ledger should exist and remain readable after verify.
	wipnoteDir := filepath.Join(dir, ".wipnote")
	ledgerPath := filepath.Join(wipnoteDir, corearch.LedgerFilename)
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("architecture ledger missing after verify: %v", err)
	}
}

// TestArchVerify_NotFound tests that verify returns an error for missing cards.
func TestArchVerify_NotFound(t *testing.T) {
	setupArchTestDir(t)

	err := runArch(t, "verify", "nonexistent-card")
	if err == nil {
		t.Fatal("expected error for nonexistent card")
	}
}

// testCreateStandalone creates a standalone feature (no plan/track required) for tests.
func testCreateStandalone(typeName, title string) error {
	return runWiCreate(typeName, title, &wiCreateOpts{
		standaloneReason: "unit-test",
		description:      "test description",
		noLink:           true,
	})
}

// TestCompletionLearning_HappyPath tests that --learning creates a card
// and the completion proceeds normally.
func TestCompletionLearning_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("drives architecture card creation lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", tmpDir)

	// Create and complete a feature with --learning.
	if err := testCreateStandalone("feature", "Test Learning Feature"); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(files) == 0 {
		t.Fatal("no feature file created")
	}

	node, err := parseNodeFile(files[len(files)-1])
	if err != nil {
		t.Fatalf("parse feature: %v", err)
	}
	featID := node.ID

	if err := wiSetStatusWithAgent("feature", featID, "in-progress", "", ""); err != nil {
		t.Fatalf("start feature: %v", err)
	}

	// Set the learning body.
	const learningBody = "The connection pool must be pre-warmed before serving traffic."
	wiLearning = learningBody
	wiLearningKind = "invariant"
	defer func() { wiLearning = ""; wiLearningKind = "" }()

	// Allow dirty / no source commits gate.
	wiAcceptedAdvisory = "unit test"
	wiAllowDirtyComplete = true
	defer func() { wiAcceptedAdvisory = ""; wiAllowDirtyComplete = false }()

	if err := wiSetStatusWithAgent("feature", featID, "done", "", ""); err != nil {
		t.Fatalf("complete feature with --learning: %v", err)
	}

	// Verify the learning landed in the canonical architecture ledger.
	ledgerCards, err := corearch.ReadLedger(filepath.Join(hgDir, corearch.LedgerFilename))
	if err != nil {
		t.Fatalf("read architecture ledger: %v", err)
	}
	if len(ledgerCards) == 0 {
		t.Fatal("expected at least one ledger row created by --learning")
	}
	if ledgerCards[0].Links[0] != featID {
		t.Fatalf("ledger link = %q, want %q", ledgerCards[0].Links[0], featID)
	}
}

// TestCompletionLearning_InvalidBody tests that --learning with an invalid body
// does NOT abort completion — the item completes successfully and a warning is
// emitted to stderr. The learning card is not attached (non-fatal contract).
func TestCompletionLearning_InvalidBody(t *testing.T) {
	if testing.Short() {
		t.Skip("drives architecture card creation lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", tmpDir)

	if err := testCreateStandalone("feature", "Test Invalid Learning"); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(files) == 0 {
		t.Fatal("no feature file created")
	}
	node, _ := parseNodeFile(files[len(files)-1])
	featID := node.ID

	if err := wiSetStatusWithAgent("feature", featID, "in-progress", "", ""); err != nil {
		t.Fatalf("start feature: %v", err)
	}

	// Too many words (> 120) — validation fails but completion is non-fatal.
	wiLearning = strings.Repeat("word ", 121)
	wiLearningKind = "decision"
	defer func() { wiLearning = ""; wiLearningKind = "" }()

	// Capture stderr to verify the warning is emitted.
	stderrR, stderrW, _ := os.Pipe()
	origStderr := os.Stderr
	os.Stderr = stderrW

	err := wiSetStatusWithAgent("feature", featID, "done", "", "")

	stderrW.Close()
	var stderrBuf bytes.Buffer
	io.Copy(&stderrBuf, stderrR)
	os.Stderr = origStderr

	// Completion must SUCCEED despite the invalid learning body.
	if err != nil {
		t.Fatalf("completion should succeed even with invalid --learning: %v", err)
	}

	// Verify a warning was emitted mentioning "learning".
	stderrStr := stderrBuf.String()
	if !strings.Contains(stderrStr, "learning") {
		t.Errorf("warning should mention 'learning', stderr was: %q", stderrStr)
	}

	// Verify no ledger was created (learning was skipped, not attached).
	if _, statErr := os.Stat(filepath.Join(hgDir, corearch.LedgerFilename)); !os.IsNotExist(statErr) {
		t.Fatal("no architecture ledger should exist when --learning validation fails")
	}

	// Verify the feature IS done (completion succeeded).
	nodeAfter, _ := parseNodeFile(files[len(files)-1])
	if string(nodeAfter.Status) != "done" {
		t.Fatal("feature should be done even when --learning validation fails (non-fatal)")
	}
}

// TestCompletionLearning_InvalidKind tests that --learning-kind with an invalid kind
// does NOT abort completion — the item completes successfully and a warning is
// emitted to stderr. The learning card is not attached (non-fatal contract).
func TestCompletionLearning_InvalidKind(t *testing.T) {
	if testing.Short() {
		t.Skip("drives architecture card creation lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", tmpDir)

	if err := testCreateStandalone("feature", "Test Invalid Learning Kind"); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(files) == 0 {
		t.Fatal("no feature file created")
	}
	node, _ := parseNodeFile(files[len(files)-1])
	featID := node.ID

	if err := wiSetStatusWithAgent("feature", featID, "in-progress", "", ""); err != nil {
		t.Fatalf("start feature: %v", err)
	}

	// Invalid learning kind — validation fails but completion is non-fatal.
	wiLearning = "This is a valid learning body."
	wiLearningKind = "invalid-kind"
	defer func() { wiLearning = ""; wiLearningKind = "" }()

	// Capture stderr to verify the warning is emitted.
	stderrR, stderrW, _ := os.Pipe()
	origStderr := os.Stderr
	os.Stderr = stderrW

	err := wiSetStatusWithAgent("feature", featID, "done", "", "")

	stderrW.Close()
	var stderrBuf bytes.Buffer
	io.Copy(&stderrBuf, stderrR)
	os.Stderr = origStderr

	// Completion must SUCCEED despite the invalid learning kind.
	if err != nil {
		t.Fatalf("completion should succeed even with invalid --learning-kind: %v", err)
	}

	// Verify a warning was emitted mentioning the invalid kind.
	stderrStr := stderrBuf.String()
	if !strings.Contains(stderrStr, "invalid-kind") {
		t.Errorf("warning should mention the invalid kind value, stderr was: %q", stderrStr)
	}

	// Verify no ledger was created (learning was skipped, not attached).
	if _, statErr := os.Stat(filepath.Join(hgDir, corearch.LedgerFilename)); !os.IsNotExist(statErr) {
		t.Fatal("no architecture ledger should exist when --learning-kind validation fails")
	}

	// Verify the feature IS done (completion succeeded).
	nodeAfter, _ := parseNodeFile(files[len(files)-1])
	if string(nodeAfter.Status) != "done" {
		t.Fatal("feature should be done even when --learning-kind validation fails (non-fatal)")
	}
}

// TestDriftNudge_BestEffort tests that drift-nudge failures do NOT fail
// the completion (nudge is best-effort).
func TestDriftNudge_BestEffort(t *testing.T) {
	// Nudge operates on cards retrieved from an arch store; an empty store
	// means 0 cards matched, and nudge is silently skipped. This test ensures
	// the completion path returns nil even when there are no matched cards.
	var buf bytes.Buffer
	emitDriftNudge(&buf, []string{"cmd/wipnote/arch_cmds.go"}, "", func(_, _ string) ([]string, error) {
		return nil, fmt.Errorf("git error: intentional test failure")
	})
	// The function must not panic or return an error even when the runner fails.
}

// parseNodeFile parses an HTML work-item file and returns its node.
func parseNodeFile(path string) (*models.Node, error) {
	return htmlparse.ParseFile(path)
}

// TestCompletionLearning_PathsAreRepoRelative verifies that normalizeLearningPaths
// discards absolute/tmp paths and only keeps repo-relative survivors.
func TestCompletionLearning_PathsAreRepoRelative(t *testing.T) {
	tmpDir := t.TempDir()
	// Initialize a git repo so NormalizeToRepoRelative can find a worktree root.
	initBareGitRepo(t, tmpDir, map[string]string{"placeholder": "x"})

	repoRelative := "cmd/wipnote/arch_cmds.go"
	garbage := []string{
		"/tmp/claude-transcript-abc123.jsonl",
		"/home/user/.claude/projects/foo/bar.jsonl",
		tmpDir + "/some/absolute/path.go",
		"",
	}
	input := append([]string{repoRelative}, garbage...)

	// NormalizeToRepoRelative will return the file as-is (already relative).
	result := normalizeLearningPaths(tmpDir, input)

	// The relative path must survive.
	found := false
	for _, p := range result {
		if p == repoRelative {
			found = true
		}
		// No absolute paths or unresolved: prefixes should remain.
		if filepath.IsAbs(p) {
			t.Errorf("absolute path leaked into normalized paths: %q", p)
		}
		if strings.HasPrefix(p, "unresolved:") {
			t.Errorf("unresolved: path leaked into normalized paths: %q", p)
		}
	}
	if !found {
		t.Errorf("expected repo-relative path %q to survive normalization; got %v", repoRelative, result)
	}
}

// TestArchResolve_ForBugItem_FallsBackToCommitFiles verifies that resolveWorkItemPaths
// falls back to tier-2 (git_commits -> diff-tree) when feature_files is empty.
// Setup: a real git repo with a bug commit touching a file, an arch card covering
// that file, a test DB with git_commits rows but empty feature_files.
func TestArchResolve_ForBugItem_FallsBackToCommitFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create the .wipnote directory structure.
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"arch", "bugs", "features"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Create a git repo with one commit touching a specific file.
	commitHash := initBareGitRepo(t, tmpDir, map[string]string{
		"cmd/serve_child.go": "package main\n",
	})

	// Create an arch card whose glob covers the committed file.
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	card := &corearch.Card{
		Name:      "serve-area",
		Kind:      corearch.KindSubsystemMap,
		Paths:     []string{"cmd/serve_child.go"},
		CreatedBy: "test",
		Body:      "Serve child process handles the dashboard.",
	}
	if err := store.Create(card); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	// Create a test DB with a git_commits row for the bug item, but no feature_files.
	dbPath := filepath.Join(tmpDir, "test.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	bugID := "bug-test-fallback"
	// Insert a feature row to satisfy FK constraints.
	if err := dbpkg.UpsertFeature(database, &dbpkg.Feature{
		ID: bugID, Type: "bug", Title: "Test Bug",
		Status: "todo", Priority: "medium",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertFeature: %v", err)
	}
	// Insert a git_commits row linking the commit to the bug ID.
	_, err = database.Exec(
		`INSERT OR IGNORE INTO git_commits (commit_hash, session_id, feature_id, message, timestamp) VALUES (?, 'test-sess', ?, 'fix: test commit', ?)`,
		commitHash, bugID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert git_commits: %v", err)
	}
	// Confirm feature_files is empty for this bug.
	rows, err := dbpkg.ListFilesByFeature(database, bugID)
	if err != nil {
		t.Fatalf("ListFilesByFeature: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty feature_files, got %d rows", len(rows))
	}
	database.Close()

	// Use WIPNOTE_PROJECT_DIR so findWipnoteDir resolves to our test dir.
	t.Setenv("WIPNOTE_PROJECT_DIR", tmpDir)

	// resolveWorkItemPaths should fall back to tier-2 and return the committed file.
	filePaths, err := resolveWorkItemPaths(bugID, wipnoteDir)
	if err != nil {
		t.Fatalf("resolveWorkItemPaths: %v", err)
	}
	if len(filePaths) == 0 {
		t.Fatal("expected tier-2 fallback to return file paths, got none")
	}
	found := false
	for _, fp := range filePaths {
		if fp == "cmd/serve_child.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected cmd/serve_child.go in resolved paths; got %v", filePaths)
	}

	// Verify that the arch card matches the resolved paths.
	cards, err := store.List(false)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	matched := iarch.MatchCards(cards, filePaths)
	if len(matched) == 0 {
		t.Errorf("expected serve-area card to match resolved paths %v, got no matches", filePaths)
	}
}

// TestArchAdd_RejectsAbsolutePath verifies that `arch add` returns an error
// when a path-class error (absolute path) is passed via --paths.
func TestArchAdd_RejectsAbsolutePath(t *testing.T) {
	setupArchTestDir(t)

	err := runArch(t,
		"add", "absolute-paths-card",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "Body here.",
		"--paths", "/absolute/path/to/file.go",
	)
	if err == nil {
		t.Fatal("expected error when adding card with absolute path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error should mention absolute, got: %v", err)
	}
}

// TestArchAdd_AcceptsDiffTreePaths verifies that repo-relative paths produced
// by diff-tree (as the bootstrap/fallback chain generates them) pass validation.
func TestArchAdd_AcceptsDiffTreePaths(t *testing.T) {
	setupArchTestDir(t)

	// diff-tree emits paths like "cmd/wipnote/arch_cmds.go" — repo-relative, no leading slash.
	err := runArch(t,
		"add", "diff-tree-card",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "Diff-tree derived paths must validate clean.",
		"--paths", "cmd/wipnote/arch_cmds.go,core/arch/card.go",
	)
	if err != nil {
		t.Fatalf("arch add with diff-tree-style paths should succeed, got: %v", err)
	}
}

// TestArchRepair_FixesMixedCard verifies the repair pass on a fixture card that
// contains garbage (absolute + worktree) paths mixed with valid repo-relative paths.
// After repair: garbage dropped/recovered (or just dropped), valid retained, file rewritten.
func TestArchRepair_FixesMixedCard(t *testing.T) {
	dir := setupArchTestDir(t)
	wipnoteDir := filepath.Join(dir, ".wipnote")

	// Write a card with mixed paths directly to the arch store (bypass Create validation).
	archDir := filepath.Join(wipnoteDir, "arch")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatalf("mkdir arch: %v", err)
	}
	raw := []byte("---\n" +
		"name: mixed-card\n" +
		"kind: invariant\n" +
		"created_by: agent\n" +
		"paths:\n" +
		"    - /absolute/garbage/path.go\n" +
		"    - core/arch/card.go\n" +
		"    - /workspaces/wipnote/.claude/worktrees/dead-branch/file.go\n" +
		"links:\n" +
		"    - feat-test1234\n" +
		"---\n" +
		"Invariant body here.\n")
	cardFile := filepath.Join(archDir, "mixed-card.md")
	if err := os.WriteFile(cardFile, raw, 0o644); err != nil {
		t.Fatalf("write fixture card: %v", err)
	}

	// Run repair (no --dry-run).
	if err := runArch(t, "repair"); err != nil {
		t.Fatalf("arch repair: %v", err)
	}

	// Read the repaired card.
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	card, err := store.Get("mixed-card")
	if err != nil {
		t.Fatalf("Get mixed-card after repair: %v", err)
	}

	// The valid path must survive.
	found := false
	for _, p := range card.Paths {
		if p == "core/arch/card.go" {
			found = true
		}
		// No garbage paths should remain.
		if filepath.IsAbs(p) {
			t.Errorf("absolute path not removed after repair: %q", p)
		}
		if strings.HasPrefix(p, "unresolved:") {
			t.Errorf("unresolved: path not removed after repair: %q", p)
		}
	}
	if !found {
		t.Errorf("valid path core/arch/card.go should survive repair; paths = %v", card.Paths)
	}

	// The card must now pass validation (no error-class paths).
	if err := corearch.Validate(card); err != nil {
		t.Errorf("card should pass validation after repair, got: %v", err)
	}
}

// TestArchRepair_DryRun verifies that --dry-run prints what would change
// but does not rewrite the card file.
func TestArchRepair_DryRun(t *testing.T) {
	dir := setupArchTestDir(t)
	wipnoteDir := filepath.Join(dir, ".wipnote")

	archDir := filepath.Join(wipnoteDir, "arch")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatalf("mkdir arch: %v", err)
	}
	// Card with one garbage absolute path.
	raw := []byte("---\n" +
		"name: dry-run-card\n" +
		"kind: decision\n" +
		"created_by: agent\n" +
		"paths:\n" +
		"    - /absolute/garbage.go\n" +
		"    - cmd/wipnote/main.go\n" +
		"---\n" +
		"Decision body.\n")
	cardFile := filepath.Join(archDir, "dry-run-card.md")
	if err := os.WriteFile(cardFile, raw, 0o644); err != nil {
		t.Fatalf("write fixture card: %v", err)
	}
	originalContent, _ := os.ReadFile(cardFile)

	if err := runArch(t, "repair", "--dry-run"); err != nil {
		t.Fatalf("arch repair --dry-run: %v", err)
	}

	// File must be unchanged.
	afterContent, _ := os.ReadFile(cardFile)
	if string(afterContent) != string(originalContent) {
		t.Errorf("--dry-run should not modify the file; content changed")
	}
}

// TestArchDiffTreePaths_ValidateClean proves that diff-tree-derived paths
// (repo-relative, no leading slash, no "../") pass the validator.
func TestArchDiffTreePaths_ValidateClean(t *testing.T) {
	diffTreePaths := []string{
		"cmd/wipnote/arch_cmds.go",
		"core/arch/card.go",
		"internal/arch/match.go",
		"plugin/skills/arch-bootstrap/SKILL.md",
		".wipnote/arch/serve-writequeue-hazard.md",
	}
	card := &corearch.Card{
		Name:      "diff-tree-test",
		Kind:      corearch.KindInvariant,
		Paths:     diffTreePaths,
		CreatedBy: "test",
		Body:      "Diff-tree paths validate clean.",
	}
	if err := corearch.Validate(card); err != nil {
		t.Errorf("diff-tree-derived paths should validate clean, got: %v", err)
	}
	warns := corearch.ValidatePaths(diffTreePaths)
	if len(warns) != 0 {
		t.Errorf("diff-tree paths should produce no warnings, got: %v", warns)
	}
}
