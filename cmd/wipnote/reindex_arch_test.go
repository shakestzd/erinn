package main

import (
	"database/sql"
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

	// The card is readable from the canonical store, which is where every
	// reader now looks.
	card := mustReadArchCard(t, dir, "auth-card")
	if string(card.Kind) != "invariant" {
		t.Errorf("card kind = %q, want invariant", card.Kind)
	}

	// ...and it was NOT mirrored into SQLite. Reindex stopped writing that
	// table (spk-e6e82b5a); a row here means the mirror came back.
	assertArchCardsTableEmpty(t, db)
}

// mustReadArchCard reads a card through the canonical store.
func mustReadArchCard(t *testing.T, wipnoteDir, slug string) *corearch.Card {
	t.Helper()
	store, err := corearch.NewStore(wipnoteDir)
	if err != nil {
		t.Fatalf("open arch store: %v", err)
	}
	card, err := store.Get(slug)
	if err != nil {
		t.Fatalf("read card %q from canonical store: %v", slug, err)
	}
	return card
}

// assertArchCardsTableEmpty fails if reindex wrote to the retired mirror.
func assertArchCardsTableEmpty(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM arch_cards").Scan(&count); err != nil {
		t.Fatalf("count arch_cards: %v", err)
	}
	if count != 0 {
		t.Errorf("arch_cards has %d rows — reindex is mirroring cards into "+
			"SQLite again; every reader now goes to the canonical store", count)
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

	// Still nothing in the retired mirror after a second pass.
	assertArchCardsTableEmpty(t, db)
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

func TestReindexArchCards_SpecPrefixLineageEdges(t *testing.T) {
	dir := t.TempDir()
	card := &corearch.Card{
		Name:      "spec-learning",
		Kind:      corearch.KindDecision,
		CreatedBy: "agent",
		Links:     []string{"spec-12345678"},
		Body:      "Spec-linked learning should participate in lineage.",
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

	var toType string
	if err := db.QueryRow(`SELECT to_node_type FROM graph_edges WHERE from_node_id = ? AND to_node_id = ?`,
		corearch.ArchNodeID("spec-learning"), "spec-12345678",
	).Scan(&toType); err != nil {
		t.Fatalf("query spec graph edge: %v", err)
	}
	if toType != "spec" {
		t.Fatalf("spec edge to_node_type = %q, want spec", toType)
	}
}

func TestReindexArchCards_HTMLLedgerRow(t *testing.T) {
	dir := t.TempDir()
	card := &corearch.Card{
		Name:      "html-ledger-card",
		Kind:      corearch.KindDecision,
		CreatedBy: "agent",
		Links:     []string{"feat-87654321"},
		Body:      "HTML ledger rows stay canonical and are not mirrored.",
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

	got := mustReadArchCard(t, dir, "html-ledger-card")
	if got.CreatedBy != "agent" {
		t.Fatalf("card created_by = %q, want agent", got.CreatedBy)
	}
	assertArchCardsTableEmpty(t, db)
}
