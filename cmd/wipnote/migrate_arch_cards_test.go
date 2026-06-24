package main

import (
	"os"
	"path/filepath"
	"testing"

	corearch "github.com/shakestzd/wipnote/core/arch"
)

// sampleMDCard is a minimal legacy frontmatter card.
const sampleMDCard = `---
name: test-hazard
kind: hazard
created_by: test-agent
paths:
  - cmd/wipnote/migrate.go
---
This is a test hazard body for migration testing.
`

// setupArchMigrateEnv creates a temp project with a .wipnote/arch/ directory
// containing one .md card. Returns the project dir and the path to the .md file.
func setupArchMigrateEnv(t *testing.T) (projectDir string, mdPath string) {
	t.Helper()
	projectDir = t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	archDir := filepath.Join(wipnoteDir, "arch")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatalf("create arch dir: %v", err)
	}
	mdPath = filepath.Join(archDir, "test-hazard.md")
	if err := os.WriteFile(mdPath, []byte(sampleMDCard), 0o644); err != nil {
		t.Fatalf("write md card: %v", err)
	}
	// Override the project dir so findWipnoteDir resolves to our temp dir.
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	return projectDir, mdPath
}

// TestMigrateArchCards_HappyPath verifies that:
//  1. The card is present in the ledger after migration.
//  2. The .md file is removed after migration.
//  3. A second run is a no-op (idempotent).
func TestMigrateArchCards_HappyPath(t *testing.T) {
	projectDir, mdPath := setupArchMigrateEnv(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")

	// --- First run: should migrate ---
	if err := runMigrateArchCards(false); err != nil {
		t.Fatalf("first runMigrateArchCards: %v", err)
	}

	// Card must now be in the ledger.
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	card, err := store.Get("test-hazard")
	if err != nil {
		t.Fatalf("store.Get after migration: %v", err)
	}
	if card.Kind != corearch.KindHazard {
		t.Errorf("card.Kind: got %q, want %q", card.Kind, corearch.KindHazard)
	}

	// The .md source file must be gone.
	if _, statErr := os.Stat(mdPath); !os.IsNotExist(statErr) {
		t.Errorf("expected .md file to be removed after migration; stat err: %v", statErr)
	}

	// --- Second run: must be a no-op (skipped) ---
	// No .md files remain, so the second call should report nothing to migrate.
	if err := runMigrateArchCards(false); err != nil {
		t.Fatalf("second runMigrateArchCards: %v", err)
	}

	// The card should still be in the ledger unchanged.
	card2, err := store.Get("test-hazard")
	if err != nil {
		t.Fatalf("store.Get after second run: %v", err)
	}
	if card2.Name != card.Name {
		t.Errorf("card name changed after second run: %q vs %q", card2.Name, card.Name)
	}
}

// TestMigrateArchCards_IdempotentWithExistingLedgerEntry verifies that when
// the .md file is present AND the card is already in the ledger (e.g.
// a partial migration that left the .md behind), the card is skipped.
func TestMigrateArchCards_IdempotentWithExistingLedgerEntry(t *testing.T) {
	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	archDir := filepath.Join(wipnoteDir, "arch")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatalf("create arch dir: %v", err)
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	// Pre-populate the ledger WITHOUT a .md file so store.Create succeeds.
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	existing := &corearch.Card{
		Name:      "test-hazard",
		Kind:      corearch.KindHazard,
		CreatedBy: "pre-seeded",
		Body:      "pre-seeded body",
	}
	if err := store.Create(existing); err != nil {
		t.Fatalf("pre-seed Create: %v", err)
	}

	// NOW write the .md file — simulating a partial migration where the
	// card is in the ledger but the .md was never cleaned up.
	mdPath := filepath.Join(archDir, "test-hazard.md")
	if err := os.WriteFile(mdPath, []byte(sampleMDCard), 0o644); err != nil {
		t.Fatalf("write .md card: %v", err)
	}

	// Migration should skip (card is already in ledger).
	if err := runMigrateArchCards(false); err != nil {
		t.Fatalf("runMigrateArchCards: %v", err)
	}

	// .md file must still exist (not removed on skip).
	if _, statErr := os.Stat(mdPath); statErr != nil {
		t.Errorf("expected .md file to remain when card was skipped; stat err: %v", statErr)
	}

	// Ledger entry should be the pre-seeded one (not overwritten).
	card, err := store.Get("test-hazard")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if card.CreatedBy != "pre-seeded" {
		t.Errorf("card.CreatedBy: got %q, want %q", card.CreatedBy, "pre-seeded")
	}
}

// TestMigrateArchCards_DryRun verifies that --dry-run makes no changes to
// the ledger and leaves the .md file intact.
func TestMigrateArchCards_DryRun(t *testing.T) {
	projectDir, mdPath := setupArchMigrateEnv(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")

	if err := runMigrateArchCards(true); err != nil {
		t.Fatalf("dry-run runMigrateArchCards: %v", err)
	}

	// .md file must still exist after a dry run.
	if _, statErr := os.Stat(mdPath); statErr != nil {
		t.Errorf("expected .md file to remain after dry run; stat err: %v", statErr)
	}

	// The ledger file must NOT exist (or must not contain test-hazard) after a
	// dry run. We use ReadLedger directly to check the canonical ledger, not
	// store.Get (which falls back to the .md file itself).
	ledgerPath := corearch.LedgerPath(wipnoteDir)
	ledgerCards, readErr := corearch.ReadLedger(ledgerPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("ReadLedger: %v", readErr)
	}
	for _, c := range ledgerCards {
		if c.Name == "test-hazard" {
			t.Error("expected test-hazard to be absent from ledger after dry run, but it was found")
		}
	}
}
