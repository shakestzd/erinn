package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	dir := setupArchTestDir(t)
	wipnoteDir := filepath.Join(dir, ".wipnote")

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

	// Read the arch dir to confirm the retired card was written.
	archPath := filepath.Join(wipnoteDir, "arch", "retired-one.md")
	data, err := os.ReadFile(archPath)
	if err != nil {
		t.Fatalf("read retired card: %v", err)
	}
	if !strings.Contains(string(data), "retired: true") {
		t.Errorf("expected 'retired: true' in card file, got:\n%s", data)
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
