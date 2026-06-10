package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runArchBootstrapCapture runs "arch bootstrap" and returns captured stdout + error.
func runArchBootstrapCapture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := buildRoot()
	root.SetOut(&buf)
	root.SetArgs(append([]string{"arch", "bootstrap"}, args...))
	err := root.Execute()
	return buf.String(), err
}

// TestArchBootstrap_BriefHasSections verifies the three required sections are present.
func TestArchBootstrap_BriefHasSections(t *testing.T) {
	dir := setupArchTestDir(t)

	// Create a minimal repo-like structure so git works.
	// Bootstrap runs git log; an empty dir with no git repo will gracefully skip hotspots.
	_ = dir

	out, err := runArchBootstrapCapture(t)
	if err != nil {
		t.Fatalf("arch bootstrap: %v", err)
	}

	// Section 1: Repo layout
	if !strings.Contains(out, "## Repo Layout") {
		t.Errorf("brief missing '## Repo Layout' section:\n%s", out)
	}

	// Section 2: Existing docs (CLAUDE.md / AGENTS.md pointers)
	if !strings.Contains(out, "## Existing Docs") {
		t.Errorf("brief missing '## Existing Docs' section:\n%s", out)
	}

	// Section 3: Lineage Hotspots
	if !strings.Contains(out, "## Lineage Hotspots") {
		t.Errorf("brief missing '## Lineage Hotspots' section:\n%s", out)
	}

	// Section 4: Authoring instructions
	if !strings.Contains(out, "## Authoring Instructions") {
		t.Errorf("brief missing '## Authoring Instructions' section:\n%s", out)
	}
}

// TestArchBootstrap_InstructionsMentionArchAdd verifies the brief instructs the
// calling agent to use "wipnote arch add" and "wipnote arch validate".
func TestArchBootstrap_InstructionsMentionArchAdd(t *testing.T) {
	setupArchTestDir(t)

	out, err := runArchBootstrapCapture(t)
	if err != nil {
		t.Fatalf("arch bootstrap: %v", err)
	}

	if !strings.Contains(out, "wipnote arch add") {
		t.Errorf("brief must mention 'wipnote arch add':\n%s", out)
	}
	if !strings.Contains(out, "wipnote arch validate") {
		t.Errorf("brief must mention 'wipnote arch validate':\n%s", out)
	}
}

// TestArchBootstrap_InstructionsMentionKinds verifies the brief documents the
// valid card kinds so the calling agent knows the allowed values.
func TestArchBootstrap_InstructionsMentionKinds(t *testing.T) {
	setupArchTestDir(t)

	out, err := runArchBootstrapCapture(t)
	if err != nil {
		t.Fatalf("arch bootstrap: %v", err)
	}

	for _, kind := range []string{"subsystem-map", "invariant", "hazard", "decision"} {
		if !strings.Contains(out, kind) {
			t.Errorf("brief must mention card kind %q:\n%s", kind, out)
		}
	}
}

// TestArchBootstrap_GracefulNoGit verifies bootstrap works when there is no git
// repo (hotspot section shows a graceful message instead of crashing).
func TestArchBootstrap_GracefulNoGit(t *testing.T) {
	// setupArchTestDir creates a temp dir with .wipnote/ but no .git.
	setupArchTestDir(t)

	out, err := runArchBootstrapCapture(t)
	if err != nil {
		t.Fatalf("arch bootstrap (no git): %v", err)
	}

	// Must still have the sections — hotspots section just shows no data.
	if !strings.Contains(out, "## Lineage Hotspots") {
		t.Errorf("brief missing hotspots section even without git:\n%s", out)
	}
}

// TestArchBootstrap_ExistingDocsPointed verifies that when CLAUDE.md / AGENTS.md
// exist at the repo root, the brief includes them by name (or their content).
func TestArchBootstrap_ExistingDocsPointed(t *testing.T) {
	dir := setupArchTestDir(t)

	// Create a CLAUDE.md at the project root (dir).
	claudeMd := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("# Project Instructions\n\nBuild with `go build`."), 0o644); err != nil {
		t.Fatalf("create CLAUDE.md: %v", err)
	}

	out, err := runArchBootstrapCapture(t)
	if err != nil {
		t.Fatalf("arch bootstrap: %v", err)
	}

	if !strings.Contains(out, "CLAUDE.md") {
		t.Errorf("brief should reference CLAUDE.md:\n%s", out)
	}
}

// TestArchAdd_DuplicateGlobSetRejected verifies that adding a card with the exact
// same set of path globs (order-insensitive) as an existing card is rejected.
func TestArchAdd_DuplicateGlobSetRejected(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "first-card",
		"--kind", "subsystem-map",
		"--created-by", "agent",
		"--body", "First subsystem.",
		"--paths", "internal/**,cmd/**",
	); err != nil {
		t.Fatalf("add first card: %v", err)
	}

	// Same glob set (same order) — must be rejected.
	err := runArch(t,
		"add", "second-card",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "Second card, same globs.",
		"--paths", "internal/**,cmd/**",
	)
	if err == nil {
		t.Fatal("expected error for duplicate glob set (same order)")
	}
	if !strings.Contains(err.Error(), "glob") && !strings.Contains(err.Error(), "paths") {
		t.Errorf("error should mention glob/paths conflict, got: %v", err)
	}
}

// TestArchAdd_DuplicateGlobSetOrderInsensitive verifies that glob-set duplicate
// detection is order-insensitive.
func TestArchAdd_DuplicateGlobSetOrderInsensitive(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "card-a",
		"--kind", "subsystem-map",
		"--created-by", "agent",
		"--body", "Card A.",
		"--paths", "internal/**,cmd/**",
	); err != nil {
		t.Fatalf("add card-a: %v", err)
	}

	// Reversed order — must still be rejected.
	err := runArch(t,
		"add", "card-b",
		"--kind", "hazard",
		"--created-by", "agent",
		"--body", "Card B, reversed globs.",
		"--paths", "cmd/**,internal/**",
	)
	if err == nil {
		t.Fatal("expected error for duplicate glob set (reversed order)")
	}
}

// TestArchAdd_DifferentGlobSetAllowed verifies that overlapping but non-identical
// glob sets are allowed (dedup only triggers on exact-set equality).
func TestArchAdd_DifferentGlobSetAllowed(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "card-x",
		"--kind", "subsystem-map",
		"--created-by", "agent",
		"--body", "Card X.",
		"--paths", "internal/**,cmd/**",
	); err != nil {
		t.Fatalf("add card-x: %v", err)
	}

	// Different glob set (one element added) — must be allowed.
	if err := runArch(t,
		"add", "card-y",
		"--kind", "subsystem-map",
		"--created-by", "agent",
		"--body", "Card Y, different globs.",
		"--paths", "internal/**,cmd/**,core/**",
	); err != nil {
		t.Errorf("different glob set should be allowed, got: %v", err)
	}
}

// TestArchAdd_EmptyGlobSetDuplicateNotTriggered verifies that two cards with no
// paths set are NOT flagged as glob-set duplicates (empty set is common/expected).
func TestArchAdd_EmptyGlobSetDuplicateNotTriggered(t *testing.T) {
	setupArchTestDir(t)

	if err := runArch(t,
		"add", "no-paths-a",
		"--kind", "decision",
		"--created-by", "agent",
		"--body", "Decision without paths.",
	); err != nil {
		t.Fatalf("add no-paths-a: %v", err)
	}

	// Second card with no paths — must be allowed.
	if err := runArch(t,
		"add", "no-paths-b",
		"--kind", "invariant",
		"--created-by", "agent",
		"--body", "Invariant without paths.",
	); err != nil {
		t.Errorf("empty glob set should not trigger dedup, got: %v", err)
	}
}

// TestArchAdd_DuplicateSlugStillRejected verifies the pre-existing name uniqueness
// check still works after adding glob-set dedup.
func TestArchAdd_DuplicateSlugStillRejected(t *testing.T) {
	setupArchTestDir(t)

	args := []string{
		"add", "slug-dedup",
		"--kind", "hazard",
		"--created-by", "agent",
		"--body", "Body here.",
	}
	if err := runArch(t, args...); err != nil {
		t.Fatalf("first add: %v", err)
	}
	err := runArch(t, args...)
	if err == nil {
		t.Fatal("expected duplicate slug error")
	}
}
