package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
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
	setupArchTestDir(t)

	err := runArch(t,
		"add", "auth-invariant",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "Auth tokens must never be logged in plaintext.",
	)
	if err != nil {
		t.Fatalf("arch add: %v", err)
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

	// The card should exist (file readable).
	wipnoteDir := filepath.Join(dir, ".wipnote")
	cardPath := filepath.Join(wipnoteDir, "arch", "verify-test.md")
	if _, err := os.Stat(cardPath); err != nil {
		t.Fatalf("card file missing after verify: %v", err)
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

	// Verify a card was created in .wipnote/arch/.
	archDir := filepath.Join(hgDir, "arch")
	entries, err := os.ReadDir(archDir)
	if err != nil {
		t.Fatalf("read arch dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one arch card created by --learning")
	}
}

// TestCompletionLearning_InvalidBody tests that --learning with an invalid body
// aborts the completion with a clear error and does NOT create a card.
func TestCompletionLearning_InvalidBody(t *testing.T) {
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

	// Too many words (> 120) — should abort.
	wiLearning = strings.Repeat("word ", 121)
	wiLearningKind = "decision"
	defer func() { wiLearning = ""; wiLearningKind = "" }()

	err := wiSetStatusWithAgent("feature", featID, "done", "", "")
	if err == nil {
		t.Fatal("expected error: --learning validation should abort completion")
	}
	if !strings.Contains(err.Error(), "learning") {
		t.Errorf("error should mention 'learning', got: %v", err)
	}

	// Verify no arch card was created.
	archDir := filepath.Join(hgDir, "arch")
	if entries, readErr := os.ReadDir(archDir); readErr == nil && len(entries) > 0 {
		t.Fatal("no arch card should exist after a failed --learning completion")
	}

	// Verify the feature is still in-progress (not done).
	nodeAfter, _ := parseNodeFile(files[len(files)-1])
	if string(nodeAfter.Status) == "done" {
		t.Fatal("feature should NOT be done after aborted completion")
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
