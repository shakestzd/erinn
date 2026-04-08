package db_test

import (
	"testing"

	"github.com/shakestzd/htmlgraph/internal/db"
)

// ---------------------------------------------------------------------------
// SanitizeFTSQuery
// ---------------------------------------------------------------------------

func TestSanitizeFTSQuery_Empty(t *testing.T) {
	if got := db.SanitizeFTSQuery(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if got := db.SanitizeFTSQuery("   "); got != "" {
		t.Errorf("expected empty for whitespace, got %q", got)
	}
}

func TestSanitizeFTSQuery_FTSOperators(t *testing.T) {
	// AND/OR/NOT/NEAR should be stripped.
	if got := db.SanitizeFTSQuery("AND OR NOT NEAR"); got != "" {
		t.Errorf("expected empty for pure operators, got %q", got)
	}
}

func TestSanitizeFTSQuery_SpecialChars(t *testing.T) {
	got := db.SanitizeFTSQuery(`hello (world) "test"`)
	if got != "hello* world* test*" {
		t.Errorf("expected special chars stripped, got %q", got)
	}
}

func TestSanitizeFTSQuery_Hyphen(t *testing.T) {
	got := db.SanitizeFTSQuery("in-progress")
	if got != "in* progress*" {
		t.Errorf("expected hyphen split, got %q", got)
	}
}

func TestSanitizeFTSQuery_NormalQuery(t *testing.T) {
	got := db.SanitizeFTSQuery("authentication flow")
	if got != "authentication* flow*" {
		t.Errorf("expected prefix wildcards, got %q", got)
	}
}

func TestSanitizeFTSQuery_MixedOperatorsAndTerms(t *testing.T) {
	got := db.SanitizeFTSQuery("cache AND session NOT debug")
	if got != "cache* session* debug*" {
		t.Errorf("expected operators filtered, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// NormalizeJSONTags
// ---------------------------------------------------------------------------

func TestNormalizeJSONTags_ValidArray(t *testing.T) {
	got := db.NormalizeJSONTags(`["tag1","tag2","tag3"]`)
	if got != "tag1 tag2 tag3" {
		t.Errorf("expected space-separated tags, got %q", got)
	}
}

func TestNormalizeJSONTags_Empty(t *testing.T) {
	for _, input := range []string{"", "null", "[]"} {
		if got := db.NormalizeJSONTags(input); got != "" {
			t.Errorf("NormalizeJSONTags(%q) = %q, want empty", input, got)
		}
	}
}

func TestNormalizeJSONTags_Malformed(t *testing.T) {
	// Non-JSON input falls back to returning as-is.
	got := db.NormalizeJSONTags("plain text tags")
	if got != "plain text tags" {
		t.Errorf("expected fallback to raw text, got %q", got)
	}
}

func TestNormalizeJSONTags_TagsWithCommas(t *testing.T) {
	got := db.NormalizeJSONTags(`["tag, with comma","normal"]`)
	if got != "tag, with comma normal" {
		t.Errorf("expected comma preserved inside tag, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// SemanticSearch
// ---------------------------------------------------------------------------

func TestSemanticSearch_HappyPath(t *testing.T) {
	database := openTestDB(t)

	// Insert a feature into the features table and semantic index.
	database.Exec(`INSERT INTO features (id, type, title, status, priority)
		VALUES ('feat-search01', 'feature', 'OAuth2 Login Flow', 'todo', 'high')`)

	entry := &db.SemanticEntry{
		FeatureID:   "feat-search01",
		Title:       "OAuth2 Login Flow",
		Description: "Implement OAuth2 authentication with token exchange",
		Tags:        "auth oauth security",
	}
	if err := db.UpsertSemanticEntry(database, entry); err != nil {
		t.Fatalf("UpsertSemanticEntry: %v", err)
	}

	results, err := db.SemanticSearch(database, "oauth authentication", 10)
	if err != nil {
		t.Fatalf("SemanticSearch: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].FeatureID != "feat-search01" {
		t.Errorf("expected feat-search01, got %s", results[0].FeatureID)
	}
}

func TestSemanticSearch_EmptyIndex(t *testing.T) {
	database := openTestDB(t)

	results, err := db.SemanticSearch(database, "anything", 10)
	if err != nil {
		t.Fatalf("SemanticSearch on empty index: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results on empty index, got %d", len(results))
	}
}

func TestSemanticSearch_EmptyQuery(t *testing.T) {
	database := openTestDB(t)

	results, err := db.SemanticSearch(database, "", 10)
	if err != nil {
		t.Fatalf("SemanticSearch empty query: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results for empty query, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// SemanticRelated
// ---------------------------------------------------------------------------

func TestSemanticRelated_HappyPath(t *testing.T) {
	database := openTestDB(t)

	entries := []struct {
		id, title, desc, tags string
	}{
		{"feat-rel00001", "OAuth2 Authentication", "Implement OAuth2 login flow with token exchange", "auth oauth"},
		{"feat-rel00002", "OAuth2 Token Refresh", "Add automatic OAuth2 token refresh and authentication handling", "auth oauth"},
		{"feat-rel00003", "Database Migration Tool", "Schema migration utility for PostgreSQL", "database migration"},
	}
	for _, f := range entries {
		database.Exec(`INSERT INTO features (id, type, title, status, priority)
			VALUES (?, 'feature', ?, 'todo', 'medium')`, f.id, f.title)

		entry := &db.SemanticEntry{
			FeatureID:   f.id,
			Title:       f.title,
			Description: f.desc,
			Tags:        f.tags,
		}
		if err := db.UpsertSemanticEntry(database, entry); err != nil {
			t.Fatalf("UpsertSemanticEntry(%s): %v", f.id, err)
		}
	}

	results, err := db.SemanticRelated(database, "feat-rel00001", 10)
	if err != nil {
		t.Fatalf("SemanticRelated: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 related result")
	}
	for _, r := range results {
		if r.FeatureID == "feat-rel00001" {
			t.Error("related results should exclude the queried feature")
		}
	}
}

func TestSemanticRelated_FeatureNotInIndex(t *testing.T) {
	database := openTestDB(t)

	_, err := db.SemanticRelated(database, "feat-nonexist", 10)
	if err == nil {
		t.Error("expected error for feature not in index")
	}
}

// ---------------------------------------------------------------------------
// Upsert + Delete round-trip
// ---------------------------------------------------------------------------

func TestUpsertAndDeleteSemanticEntry(t *testing.T) {
	database := openTestDB(t)

	entry := &db.SemanticEntry{
		FeatureID:   "feat-upsert01",
		Title:       "Test Feature",
		Description: "A test feature for upsert",
		Tags:        "test upsert",
	}

	// Insert.
	if err := db.UpsertSemanticEntry(database, entry); err != nil {
		t.Fatalf("UpsertSemanticEntry (insert): %v", err)
	}

	// Verify searchable.
	results, err := db.SemanticSearch(database, "upsert", 10)
	if err != nil {
		t.Fatalf("search after insert: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result after insert, got %d", len(results))
	}

	// Update.
	entry.Title = "Updated Feature"
	if err := db.UpsertSemanticEntry(database, entry); err != nil {
		t.Fatalf("UpsertSemanticEntry (update): %v", err)
	}

	// Delete.
	if err := db.DeleteSemanticEntry(database, "feat-upsert01"); err != nil {
		t.Fatalf("DeleteSemanticEntry: %v", err)
	}

	results, err = db.SemanticSearch(database, "upsert", 10)
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results after delete, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// RebuildSemanticIndex
// ---------------------------------------------------------------------------

func TestRebuildSemanticIndex_WithFeatures(t *testing.T) {
	database := openTestDB(t)

	database.Exec(`INSERT INTO features (id, type, title, status, priority, description)
		VALUES ('feat-rebuild01', 'feature', 'Rebuild Test', 'done', 'high', 'Feature for rebuild test')`)
	database.Exec(`INSERT INTO tracks (id, type, title, status, priority)
		VALUES ('trk-rebuild01', 'track', 'Test Track', 'in-progress', 'medium')`)

	count, err := db.RebuildSemanticIndex(database)
	if err != nil {
		t.Fatalf("RebuildSemanticIndex: %v", err)
	}
	if count < 2 {
		t.Errorf("expected at least 2 indexed items (feature + track), got %d", count)
	}

	// Verify the feature is searchable.
	results, err := db.SemanticSearch(database, "rebuild", 10)
	if err != nil {
		t.Fatalf("search after rebuild: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least 1 result after rebuild")
	}
}

func TestRebuildSemanticIndex_Empty(t *testing.T) {
	database := openTestDB(t)

	count, err := db.RebuildSemanticIndex(database)
	if err != nil {
		t.Fatalf("RebuildSemanticIndex on empty DB: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 indexed on empty DB, got %d", count)
	}
}
