package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

func TestRenderSessionShow_IncludesAdherence(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	session := &models.Session{
		SessionID:     "sess-render",
		AgentAssigned: "codex",
		Status:        "completed",
		CreatedAt:     now,
		Adherence: &models.SessionAdherence{
			Score:  75,
			Passed: 2,
			Warned: 1,
			Failed: 1,
			Checks: []models.SessionAdherenceCheck{
				{Key: "gate_ran", Status: models.SessionAdherencePass, Summary: "Latest gate record passed."},
			},
		},
	}

	var buf bytes.Buffer
	if err := renderSessionShow(&buf, database, session); err != nil {
		t.Fatalf("renderSessionShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Adherence 75%") {
		t.Fatalf("expected adherence summary in output:\n%s", out)
	}
	if !strings.Contains(out, "gate_ran") {
		t.Fatalf("expected adherence check details in output:\n%s", out)
	}
}

func TestSessionAdherenceTrendHandler(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     "sess-trend",
		AgentAssigned: "gemini",
		Status:        "completed",
		CreatedAt:     now,
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	record := &dbpkg.GateRecord{
		SessionID:         "sess-trend",
		WorkItemID:        "feat-trend",
		Harness:           "gemini_cli",
		ProjectType:       "go",
		GateCommand:       "go build ./... && go vet ./... && go test ./...",
		Status:            "pass",
		CheckedAt:         now,
		AllowlistHitsJSON: "[]",
	}
	record.EnsureSignature()
	if err := dbpkg.InsertGateRecord(database, record); err != nil {
		t.Fatalf("InsertGateRecord: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp) VALUES (?, ?, ?, ?, ?)`,
		"abc9999", "sess-trend", "feat-trend", "feat: trend", now.Format(time.RFC3339)); err != nil {
		t.Fatalf("insert git commit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", "feat-trend.html"), []byte(`<article id="feat-trend" data-type="feature" data-status="done" data-claimed-by-session="sess-trend"><header><h1>Trend</h1></header><section data-content><p>x</p></section></article>`), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := sessionAdherenceTrendHandler(database, projectDir, wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/adherence-trend", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Points []models.SessionAdherenceTrendPoint `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Points) != 1 {
		t.Fatalf("points len = %d, want 1", len(body.Points))
	}
	if body.Points[0].Harness != "gemini" {
		t.Fatalf("harness = %q, want gemini", body.Points[0].Harness)
	}
	if body.Points[0].Score == 0 {
		t.Fatalf("score = %d, want non-zero", body.Points[0].Score)
	}
}

func TestSessionAdherenceTrendHandler_MissingGitCommitsTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "features"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     "sess-trend-empty",
		AgentAssigned: "gemini",
		Status:        "completed",
		CreatedAt:     now,
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	record := &dbpkg.GateRecord{
		SessionID:         "sess-trend-empty",
		WorkItemID:        "feat-trend-empty",
		Harness:           "gemini_cli",
		ProjectType:       "go",
		GateCommand:       "go build ./... && go vet ./... && go test ./...",
		Status:            "pass",
		CheckedAt:         now,
		AllowlistHitsJSON: "[]",
	}
	record.EnsureSignature()
	if err := dbpkg.InsertGateRecord(database, record); err != nil {
		t.Fatalf("InsertGateRecord: %v", err)
	}
	if _, err := database.Exec(`DROP TABLE git_commits`); err != nil {
		t.Fatalf("drop git_commits: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", "feat-trend-empty.html"), []byte(`<article id="feat-trend-empty" data-type="feature" data-status="done" data-claimed-by-session="sess-trend-empty"><header><h1>Trend</h1></header><section data-content><p>x</p></section></article>`), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := sessionAdherenceTrendHandler(database, projectDir, wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/adherence-trend", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Points []models.SessionAdherenceTrendPoint `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Points) != 1 {
		t.Fatalf("points len = %d, want 1", len(body.Points))
	}
	if body.Points[0].Score == 0 {
		t.Fatalf("score = %d, want derived scorecard with graceful empty fallback", body.Points[0].Score)
	}
}
