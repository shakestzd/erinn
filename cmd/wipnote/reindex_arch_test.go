package main

import (
	"os"
	"path/filepath"
	"testing"

	corearch "github.com/shakestzd/wipnote/core/arch"
	dbpkg "github.com/shakestzd/wipnote/core/db"
)

func TestReindexArchCards_HappyPath(t *testing.T) {
	dir := t.TempDir()
	archDir := filepath.Join(dir, "arch")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatalf("mkdir arch: %v", err)
	}

	// Write two valid cards.
	card1 := []byte("---\nname: auth-card\nkind: invariant\ncreated_by: agent\n---\nAuth tokens must not be logged.\n")
	card2 := []byte("---\nname: db-map\nkind: subsystem-map\ncreated_by: agent\n---\nThe DB layer is in internal/db.\n")
	os.WriteFile(filepath.Join(archDir, "auth-card.md"), card1, 0o644)
	os.WriteFile(filepath.Join(archDir, "db-map.md"), card2, 0o644)

	db, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	total, upserted, errs := reindexArchCards(db, dir, true)
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if upserted != 2 {
		t.Errorf("upserted = %d, want 2", upserted)
	}
	if errs != 0 {
		t.Errorf("errs = %d, want 0", errs)
	}

	// Verify data landed in SQLite.
	row := db.QueryRow("SELECT slug, kind FROM arch_cards WHERE slug = 'auth-card'")
	var slug, kind string
	if err := row.Scan(&slug, &kind); err != nil {
		t.Fatalf("query arch_cards: %v", err)
	}
	if slug != "auth-card" || kind != "invariant" {
		t.Errorf("got slug=%q kind=%q", slug, kind)
	}
}

func TestReindexArchCards_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	// No arch directory at all.

	db, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	total, upserted, errs := reindexArchCards(db, dir, false)
	if total != 0 || upserted != 0 || errs != 0 {
		t.Errorf("expected (0,0,0), got (%d,%d,%d)", total, upserted, errs)
	}
}

func TestReindexArchCards_InvalidCard(t *testing.T) {
	dir := t.TempDir()
	archDir := filepath.Join(dir, "arch")
	os.MkdirAll(archDir, 0o755)

	// Write a card without frontmatter.
	os.WriteFile(filepath.Join(archDir, "bad.md"), []byte("no frontmatter here"), 0o644)

	db, _ := dbpkg.Open(":memory:")
	defer db.Close()

	total, upserted, errs := reindexArchCards(db, dir, true)
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if upserted != 0 {
		t.Errorf("upserted = %d, want 0", upserted)
	}
	if errs != 1 {
		t.Errorf("errs = %d, want 1", errs)
	}
}

func TestReindexArchCards_Idempotent(t *testing.T) {
	dir := t.TempDir()
	archDir := filepath.Join(dir, "arch")
	os.MkdirAll(archDir, 0o755)

	card := []byte("---\nname: stable\nkind: hazard\ncreated_by: agent\n---\nNever do the bad thing.\n")
	os.WriteFile(filepath.Join(archDir, "stable.md"), card, 0o644)

	db, _ := dbpkg.Open(":memory:")
	defer db.Close()

	// Run twice — should not error on second pass (upsert).
	reindexArchCards(db, dir, false)
	total, upserted, errs := reindexArchCards(db, dir, false)
	if errs != 0 {
		t.Errorf("second reindex errs = %d, want 0", errs)
	}
	if total != 1 || upserted != 1 {
		t.Errorf("second reindex (total=%d, upserted=%d), want (1,1)", total, upserted)
	}

	// Exactly one row in the DB.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM arch_cards").Scan(&count)
	if count != 1 {
		t.Errorf("arch_cards row count = %d, want 1", count)
	}
}

func TestReindexArchCards_ImportedYAMLAndLineageEdges(t *testing.T) {
	dir := t.TempDir()
	archDir := filepath.Join(dir, "arch")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatalf("mkdir arch: %v", err)
	}

	card := []byte("name: auth-learning\nkind: decision\ncreated_by: agent\nlinks:\n  - feat-12345678\nbody: |\n  Prefer the import-compatible YAML arch format.\n")
	if err := os.WriteFile(filepath.Join(archDir, "auth-learning.yaml"), card, 0o644); err != nil {
		t.Fatalf("write import-compatible yaml arch card: %v", err)
	}

	db, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	total, upserted, errs := reindexArchCards(db, dir, false)
	if total != 1 || upserted != 1 || errs != 0 {
		t.Fatalf("reindex = (%d,%d,%d), want (1,1,0)", total, upserted, errs)
	}

	var relType, fromType, toType string
	if err := db.QueryRow(`SELECT relationship_type, from_node_type, to_node_type
		FROM graph_edges WHERE from_node_id = ? AND to_node_id = ?`,
		corearch.ArchNodeID("auth-learning"), "feat-12345678",
	).Scan(&relType, &fromType, &toType); err != nil {
		t.Fatalf("query graph edge: %v", err)
	}
	if relType != "learned_from" || fromType != "arch" || toType != "feature" {
		t.Fatalf("edge = (%s,%s,%s), want (learned_from,arch,feature)", relType, fromType, toType)
	}

	var reverseRel, reverseToType string
	if err := db.QueryRow(`SELECT relationship_type, to_node_type
		FROM graph_edges WHERE from_node_id = ? AND to_node_id = ?`,
		"feat-12345678", corearch.ArchNodeID("auth-learning"),
	).Scan(&reverseRel, &reverseToType); err != nil {
		t.Fatalf("query reverse graph edge: %v", err)
	}
	if reverseRel != "has_learning" || reverseToType != "arch" {
		t.Fatalf("reverse edge = (%s,%s), want (has_learning,arch)", reverseRel, reverseToType)
	}
}

func TestReindexArchCards_HTMLLedgerRow(t *testing.T) {
	dir := t.TempDir()
	card := &corearch.Card{
		Name:      "html-ledger-card",
		Kind:      corearch.KindDecision,
		CreatedBy: "agent",
		Links:     []string{"feat-87654321"},
		Body:      "HTML ledger rows should reindex into arch_cards.",
	}
	if err := corearch.WriteLedger(filepath.Join(dir, corearch.LedgerFilename), []*corearch.Card{card}); err != nil {
		t.Fatalf("write architecture ledger: %v", err)
	}

	db, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	total, upserted, errs := reindexArchCards(db, dir, false)
	if total != 1 || upserted != 1 || errs != 0 {
		t.Fatalf("reindex = (%d,%d,%d), want (1,1,0)", total, upserted, errs)
	}

	var slug, createdBy string
	if err := db.QueryRow(`SELECT slug, created_by FROM arch_cards WHERE slug = ?`, "html-ledger-card").Scan(&slug, &createdBy); err != nil {
		t.Fatalf("query arch_cards: %v", err)
	}
	if slug != "html-ledger-card" || createdBy != "agent" {
		t.Fatalf("arch_cards row = (%s,%s), want (html-ledger-card,agent)", slug, createdBy)
	}
}
