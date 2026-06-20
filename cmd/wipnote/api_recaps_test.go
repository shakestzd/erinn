package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

// setupRecapTestDB creates an in-memory DB with the recaps schema and inserts
// two test recap rows. Returns the DB and both recap IDs.
func setupRecapTestDB(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	recapID1 := "recap-feat-abc12345"
	recapID2 := "recap-feat-def67890"
	now := time.Now().UTC()

	if err := db.UpsertRecap(database, &db.RecapRow{
		ID:         recapID1,
		Kind:       "work_item",
		Input:      "feat-abc12345",
		GitRange:   "main..HEAD",
		Grounded:   true,
		Title:      "First Recap",
		Outcome:    "Added something useful",
		WorkItemID: "feat-abc12345",
		CreatedAt:  &now,
	}); err != nil {
		t.Fatalf("upsert recap1: %v", err)
	}

	if err := db.UpsertRecap(database, &db.RecapRow{
		ID:         recapID2,
		Kind:       "work_item",
		Input:      "feat-def67890",
		GitRange:   "main..HEAD",
		Grounded:   false,
		Title:      "Second Recap",
		Outcome:    "Fixed some things",
		WorkItemID: "feat-def67890",
		CreatedAt:  &now,
	}); err != nil {
		t.Fatalf("upsert recap2: %v", err)
	}

	return database, recapID1, recapID2
}

// writeTempRecapHTML creates a temporary .wipnote/recaps directory with a
// minimal recap HTML file containing a <style> block. Returns the wipnoteDir.
func writeTempRecapHTML(t *testing.T, recapID string) string {
	t.Helper()
	dir := t.TempDir()
	recapsDir := filepath.Join(dir, "recaps")
	if err := os.MkdirAll(recapsDir, 0o755); err != nil {
		t.Fatalf("mkdir recaps: %v", err)
	}
	html := `<!DOCTYPE html><html><head>` +
		`<style>body { color: red; } .recap-header { font-size: 1rem; }</style>` +
		`</head><body>` +
		`<div class="recap-layout">` +
		`<h1>Test Recap: ` + recapID + `</h1>` +
		`<p>Outcome content here.</p>` +
		`</div>` +
		`</body></html>`
	htmlPath := filepath.Join(recapsDir, recapID+".html")
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		t.Fatalf("write recap html: %v", err)
	}
	return dir
}

// TestServeRecapsAPI_List tests that the list endpoint returns expected JSON
// with fields {id, title, workItem, created} read from the SQLite recaps table.
func TestServeRecapsAPI_List(t *testing.T) {
	database, recapID1, recapID2 := setupRecapTestDB(t)

	handler := recapsListHandler(database)
	req := httptest.NewRequest(http.MethodGet, "/api/recaps", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var items []recapListItem
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("item count: got %d, want 2", len(items))
	}

	// Both IDs should appear in the response.
	ids := make(map[string]bool)
	for _, item := range items {
		ids[item.ID] = true
	}
	if !ids[recapID1] {
		t.Errorf("expected %s in response, got %v", recapID1, ids)
	}
	if !ids[recapID2] {
		t.Errorf("expected %s in response, got %v", recapID2, ids)
	}

	// Each item must have required fields populated.
	for _, item := range items {
		if item.ID == "" {
			t.Error("item.ID is empty")
		}
		if item.Title == "" {
			t.Errorf("item.Title is empty for %s", item.ID)
		}
	}
}

// TestServeRecapsAPI_ListEmpty verifies an empty DB returns an empty JSON array (not null).
func TestServeRecapsAPI_ListEmpty(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	handler := recapsListHandler(database)
	req := httptest.NewRequest(http.MethodGet, "/api/recaps", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var items []recapListItem
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty array, got %d items", len(items))
	}
}

// TestServeRecapsAPI_ListMethodNotAllowed verifies only GET is accepted.
func TestServeRecapsAPI_ListMethodNotAllowed(t *testing.T) {
	database, _, _ := setupRecapTestDB(t)
	handler := recapsListHandler(database)
	req := httptest.NewRequest(http.MethodPost, "/api/recaps", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

// TestServeRecapsAPI_ListItemFields verifies the JSON field names match the
// spec: id, title, workItem, created.
func TestServeRecapsAPI_ListItemFields(t *testing.T) {
	database, recapID1, _ := setupRecapTestDB(t)

	handler := recapsListHandler(database)
	req := httptest.NewRequest(http.MethodGet, "/api/recaps", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	// Decode as raw map to verify JSON field names exactly.
	var raw []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var found map[string]any
	for _, item := range raw {
		if id, ok := item["id"].(string); ok && id == recapID1 {
			found = item
			break
		}
	}
	if found == nil {
		t.Fatalf("recap %s not found in response", recapID1)
	}

	for _, field := range []string{"id", "title", "workItem", "created"} {
		if _, ok := found[field]; !ok {
			t.Errorf("field %q missing from response item", field)
		}
	}
}

// TestServeRecapsAPI_Render tests that the render endpoint returns HTML
// containing the recap content for a fixture recap.
func TestServeRecapsAPI_Render(t *testing.T) {
	recapID := "recap-feat-render01"
	wipnoteDir := writeTempRecapHTML(t, recapID)

	handler := recapRenderHandler(wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/recaps/"+recapID+"/render", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "recap-layout") {
		t.Errorf("rendered HTML missing .recap-layout content")
	}
}

// TestServeRecapsAPI_RenderNotFound verifies 404 when recap HTML is absent.
func TestServeRecapsAPI_RenderNotFound(t *testing.T) {
	wipnoteDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "recaps"), 0o755); err != nil {
		t.Fatal(err)
	}

	handler := recapRenderHandler(wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/recaps/recap-missing/render", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

// TestServeRecapsAPI_RenderCSScoped asserts that rendered recap HTML has CSS
// scoped under the recap embed container selector — no global selectors bleed
// into the dashboard chrome.
func TestServeRecapsAPI_RenderCSScoped(t *testing.T) {
	recapID := "recap-feat-scope01"
	wipnoteDir := writeTempRecapHTML(t, recapID)

	handler := recapRenderHandler(wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/recaps/"+recapID+"/render", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("render status: got %d, want 200", w.Code)
	}

	html := w.Body.String()

	// The CSS block must exist and be scoped.
	styleStart := strings.Index(html, "<style>")
	if styleStart < 0 {
		t.Fatal("rendered HTML has no <style> block")
	}
	styleEnd := strings.Index(html[styleStart:], "</style>")
	if styleEnd < 0 {
		t.Fatal("rendered HTML <style> block not closed")
	}
	cssBlock := html[styleStart : styleStart+styleEnd+len("</style>")]

	// The CSS scope selector must appear in the scoped output.
	if !strings.Contains(cssBlock, recapEmbedScope) {
		t.Errorf("CSS not scoped: scope %q not found in style block: %s", recapEmbedScope, cssBlock)
	}

	// Bare "body {" must NOT appear — it must have been rewritten to the scope.
	if strings.Contains(cssBlock, "body {") {
		t.Errorf("CSS leaks: bare 'body {' found in scoped output: %s", cssBlock)
	}
}

// TestExtractRecapID verifies the recap ID extraction helper.
func TestExtractRecapID(t *testing.T) {
	cases := []struct {
		path    string
		suffix  string
		want    string
		wantErr bool
	}{
		{"/api/recaps/recap-abc/render", "/render", "recap-abc", false},
		{"/api/recaps/recap-xyz-123/render", "/render", "recap-xyz-123", false},
		{"/api/recaps//render", "/render", "", true},
		{"/api/recaps/recap-a/b/render", "/render", "", true},
		{"/other/path/render", "/render", "", true},
	}
	for _, tc := range cases {
		got, err := extractRecapID(tc.path, tc.suffix)
		if tc.wantErr {
			if err == nil {
				t.Errorf("extractRecapID(%q, %q): expected error, got %q", tc.path, tc.suffix, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("extractRecapID(%q, %q): unexpected error: %v", tc.path, tc.suffix, err)
			continue
		}
		if got != tc.want {
			t.Errorf("extractRecapID(%q, %q) = %q, want %q", tc.path, tc.suffix, got, tc.want)
		}
	}
}
