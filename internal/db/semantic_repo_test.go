package db_test

import (
	"testing"

	"github.com/shakestzd/htmlgraph/internal/db"
)

// ---------------------------------------------------------------------------
// sanitizeFTSQuery
// ---------------------------------------------------------------------------

func TestSanitizeFTSQuery_Empty(t *testing.T) {
	for _, input := range []string{"", "   ", "\t\n"} {
		if got := db.SanitizeFTSQuery(input); got != "" {
			t.Errorf("SanitizeFTSQuery(%q) = %q, want empty", input, got)
		}
	}
}

func TestSanitizeFTSQuery_FTSOperators(t *testing.T) {
	for _, op := range []string{"AND", "OR", "NOT", "NEAR", "and", "Or", "near"} {
		if got := db.SanitizeFTSQuery(op); got != "" {
			t.Errorf("SanitizeFTSQuery(%q) = %q, want empty (operator stripped)", op, got)
		}
	}
}

func TestSanitizeFTSQuery_SpecialChars(t *testing.T) {
	got := db.SanitizeFTSQuery(`hello(world) "foo:bar" {baz}`)
	// All special chars replaced with spaces, words get prefix *.
	for _, bad := range []string{"(", ")", "\"", ":", "{", "}"} {
		if containsStr(got, bad) {
			t.Errorf("SanitizeFTSQuery output %q still contains %q", got, bad)
		}
	}
	if got == "" {
		t.Error("expected non-empty result for input with words")
	}
}

func TestSanitizeFTSQuery_DashPrefix(t *testing.T) {
	got := db.SanitizeFTSQuery("-excluded term")
	// The dash should be stripped; both words should appear as prefix terms.
	if containsStr(got, "-") {
		t.Errorf("SanitizeFTSQuery output %q still contains dash", got)
	}
	if got == "" {
		t.Error("expected non-empty result")
	}
}

func TestSanitizeFTSQuery_NormalQuery(t *testing.T) {
	got := db.SanitizeFTSQuery("authentication flow")
	if got == "" {
		t.Fatal("expected non-empty result")
	}
	// Each word should have a prefix * appended.
	if got != "authentication* flow*" {
		t.Errorf("got %q, want %q", got, "authentication* flow*")
	}
}

// ---------------------------------------------------------------------------
// normalizeJSONTags
// ---------------------------------------------------------------------------

func TestNormalizeJSONTags_ValidArray(t *testing.T) {
	got := db.NormalizeJSONTags(`["tag1","tag2","tag3"]`)
	if got != "tag1 tag2 tag3" {
		t.Errorf("got %q, want %q", got, "tag1 tag2 tag3")
	}
}

func TestNormalizeJSONTags_Empty(t *testing.T) {
	for _, input := range []string{"", "  ", "null", "[]"} {
		if got := db.NormalizeJSONTags(input); got != "" {
			t.Errorf("NormalizeJSONTags(%q) = %q, want empty", input, got)
		}
	}
}

func TestNormalizeJSONTags_Malformed(t *testing.T) {
	// Malformed JSON returns the input as-is (fallback).
	input := "not-json-at-all"
	got := db.NormalizeJSONTags(input)
	if got != input {
		t.Errorf("got %q, want %q (fallback)", got, input)
	}
}

func TestNormalizeJSONTags_TagsWithCommas(t *testing.T) {
	got := db.NormalizeJSONTags(`["a,b","c"]`)
	if got != "a,b c" {
		t.Errorf("got %q, want %q", got, "a,b c")
	}
}

// ---------------------------------------------------------------------------
// SemanticSearch — happy path with real FTS5 table
// ---------------------------------------------------------------------------

func TestSemanticSearch_HappyPath(t *testing.T) {
	database := openTestDB(t)

	// Insert a feature into the features table so the LEFT JOIN works.
	_, err := database.Exec(`INSERT INTO features (id, type, title, status, priority)
		VALUES ('feat-aaa00001', 'feature', 'Auth Flow', 'in-progress', 'high')`)
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}

	entry := &db.SemanticEntry{
		FeatureID:   "feat-aaa00001",
		Title:       "Auth Flow",
		Description: "Implement authentication with OAuth2",
		Content:     "",
		Tags:        "auth oauth security",
		TrackTitle:  "Security Track",
	}
	if err := db.UpsertSemanticEntry(database, entry); err != nil {
		t.Fatalf("UpsertSemanticEntry: %v", err)
	}

	results, err := db.SemanticSearch(database, "authentication", 10)
	if err != nil {
		t.Fatalf("SemanticSearch: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].FeatureID != "feat-aaa00001" {
		t.Errorf("got feature_id %q, want feat-aaa00001", results[0].FeatureID)
	}
	if results[0].Type != "feature" {
		t.Errorf("got type %q, want feature", results[0].Type)
	}
}

func TestSemanticSearch_EmptyIndex(t *testing.T) {
	database := openTestDB(t)

	results, err := db.SemanticSearch(database, "anything", 10)
	if err != nil {
		t.Fatalf("SemanticSearch on empty index: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSemanticSearch_EmptyQuery(t *testing.T) {
	database := openTestDB(t)

	results, err := db.SemanticSearch(database, "", 10)
	if err != nil {
		t.Fatalf("SemanticSearch with empty query: %v", err)
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

	// Insert three features with overlapping keywords.
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
	// The related result should NOT include the queried feature.
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
		FeatureID:   "feat-del00001",
		Title:       "Deletable Feature",
		Description: "This will be deleted",
		Tags:        "test",
	}

	// Insert.
	if err := db.UpsertSemanticEntry(database, entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Verify present.
	var count int
	database.QueryRow(`SELECT COUNT(*) FROM semantic_index WHERE feature_id = ?`,
		"feat-del00001").Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", count)
	}

	// Update (upsert again with different title).
	entry.Title = "Updated Title"
	if err := db.UpsertSemanticEntry(database, entry); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	database.QueryRow(`SELECT COUNT(*) FROM semantic_index WHERE feature_id = ?`,
		"feat-del00001").Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 row after re-upsert, got %d", count)
	}

	// Delete.
	if err := db.DeleteSemanticEntry(database, "feat-del00001"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	database.QueryRow(`SELECT COUNT(*) FROM semantic_index WHERE feature_id = ?`,
		"feat-del00001").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 rows after delete, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// RebuildSemanticIndex
// ---------------------------------------------------------------------------

func TestRebuildSemanticIndex_WithFeatures(t *testing.T) {
	database := openTestDB(t)

	// Insert a track.
	database.Exec(`INSERT INTO tracks (id, type, title, status, priority)
		VALUES ('trk-rebuild01', 'track', 'Test Track', 'todo', 'medium')`)

	// Insert features with the track.
	for _, f := range []struct {
		id, title, tags string
	}{
		{"feat-rb00001", "Cache Invalidation", `["cache","performance"]`},
		{"feat-rb00002", "Rate Limiting", `["api","security"]`},
	} {
		database.Exec(`INSERT INTO features (id, type, title, status, priority, track_id, tags)
			VALUES (?, 'feature', ?, 'todo', 'medium', 'trk-rebuild01', ?)`,
			f.id, f.title, f.tags)
	}

	count, err := db.RebuildSemanticIndex(database)
	if err != nil {
		t.Fatalf("RebuildSemanticIndex: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 indexed features, got %d", count)
	}

	// Verify search finds the indexed features.
	results, err := db.SemanticSearch(database, "cache", 10)
	if err != nil {
		t.Fatalf("SemanticSearch after rebuild: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected search results after rebuild")
	}
}

func TestRebuildSemanticIndex_Empty(t *testing.T) {
	database := openTestDB(t)

	count, err := db.RebuildSemanticIndex(database)
	if err != nil {
		t.Fatalf("RebuildSemanticIndex on empty DB: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
