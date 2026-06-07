package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

// setupProvenanceDB creates an in-memory DB with test fixtures for provenance tests.
func setupProvenanceDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	featureID := "feat-prov-test"
	_, err = database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES (?, 'feature', 'Provenance Test Feature', 'in-progress')`,
		featureID,
	)
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	return database, featureID
}

// setupProvenanceDBWithData creates a DB with commits, files, and sessions.
func setupProvenanceDBWithData(t *testing.T) (*sql.DB, string) {
	t.Helper()
	database, featureID := setupProvenanceDB(t)

	// Insert a commit linked to the feature.
	_, err := database.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp)
		 VALUES ('abc123def456', 'sess-001', ?, 'test commit', '2026-04-01T10:00:00Z')`,
		featureID,
	)
	if err != nil {
		t.Fatalf("insert commit: %v", err)
	}

	// Insert a file linked to the feature.
	_, err = database.Exec(
		`INSERT INTO feature_files (id, feature_id, file_path, operation, session_id, first_seen, last_seen, created_at)
		 VALUES ('ff-001', ?, 'cmd/wipnote/main.go', 'modified', 'sess-001', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		featureID,
	)
	if err != nil {
		t.Fatalf("insert feature_file: %v", err)
	}

	// Insert a session linked to the feature via agent_events.
	_, err = database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at) VALUES ('sess-001', 'claude', 'completed', '2026-04-01T09:00:00Z')`,
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO agent_events (event_id, agent_id, session_id, feature_id, event_type, timestamp)
		 VALUES ('evt-001', 'claude', 'sess-001', ?, 'start', '2026-04-01T09:00:00Z')`,
		featureID,
	)
	if err != nil {
		t.Fatalf("insert agent_event: %v", err)
	}

	return database, featureID
}

// ---- GET /api/provenance/{id} -----------------------------------------------

func TestProvenanceHandler_KnownFeature(t *testing.T) {
	database, featureID := setupProvenanceDBWithData(t)
	mux := buildSingleProjectMux(database, nil, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/provenance/"+featureID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/provenance/%s: got %d, want 200; body: %s", featureID, w.Code, w.Body.String())
	}
	var resp provenanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Node.ID != featureID {
		t.Errorf("node.id: got %q, want %q", resp.Node.ID, featureID)
	}
	if resp.Node.Type == "" {
		t.Error("node.type: expected non-empty")
	}
	if resp.Upstream == nil {
		t.Error("upstream: expected non-nil slice")
	}
	if resp.Downstream == nil {
		t.Error("downstream: expected non-nil slice")
	}
}

func TestProvenanceHandler_UnknownID(t *testing.T) {
	database, _ := setupProvenanceDB(t)
	mux := buildSingleProjectMux(database, nil, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/provenance/feat-does-not-exist", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/provenance/unknown: got %d, want 404; body: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected non-empty error field in 404 response")
	}
}

// ---- GET /api/graph/commits?feature=X ----------------------------------------

func TestCommitsForFeatureHandler_ReturnsCommits(t *testing.T) {
	database, featureID := setupProvenanceDBWithData(t)
	mux := buildSingleProjectMux(database, nil, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/graph/commits?feature="+featureID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/commits: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var commits []commitResult
	if err := json.NewDecoder(w.Body).Decode(&commits); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("commits count: got %d, want 1", len(commits))
	}
	if commits[0].CommitHash != "abc123def456" {
		t.Errorf("commit_hash: got %q, want abc123def456", commits[0].CommitHash)
	}
}

func TestCommitsForFeatureHandler_EmptyResult(t *testing.T) {
	database, _ := setupProvenanceDB(t)
	mux := buildSingleProjectMux(database, nil, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/graph/commits?feature=feat-no-commits", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/commits (empty): got %d, want 200", w.Code)
	}
	var commits []commitResult
	if err := json.NewDecoder(w.Body).Decode(&commits); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if commits == nil {
		t.Error("expected non-nil (empty) slice, got null")
	}
	if len(commits) != 0 {
		t.Errorf("expected 0 commits, got %d", len(commits))
	}
}

// ---- GET /api/graph/files?feature=X ------------------------------------------

func TestFilesForFeatureHandler_ReturnsFiles(t *testing.T) {
	database, featureID := setupProvenanceDBWithData(t)
	mux := buildSingleProjectMux(database, nil, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/graph/files?feature="+featureID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/files: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var files []fileResult
	if err := json.NewDecoder(w.Body).Decode(&files); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files count: got %d, want 1", len(files))
	}
	if files[0].FilePath != "cmd/wipnote/main.go" {
		t.Errorf("file_path: got %q, want cmd/wipnote/main.go", files[0].FilePath)
	}
}

// ---- GET /api/graph/sessions?feature=X ----------------------------------------

func TestSessionsForFeatureHandler_ReturnsSessions(t *testing.T) {
	database, featureID := setupProvenanceDBWithData(t)
	mux := buildSingleProjectMux(database, nil, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/graph/sessions?feature="+featureID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/sessions: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var sessions []sessionResult
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions count: got %d, want 1", len(sessions))
	}
	if sessions[0].SessionID != "sess-001" {
		t.Errorf("session_id: got %q, want sess-001", sessions[0].SessionID)
	}
}

// TestProvenanceHandler_CommitNodeResolvesDerivedEdges verifies that
// clicking a commit node returns its downstream feature + session links,
// derived from git_commits rather than graph_edges. Regression for roborev
// finding that commit/file/agent drill-downs were empty.
func TestProvenanceHandler_CommitNodeResolvesDerivedEdges(t *testing.T) {
	database, featureID := setupProvenanceDBWithData(t)
	mux := buildSingleProjectMux(database, nil, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/provenance/abc123def456", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/provenance/abc123def456: got %d, body: %s", w.Code, w.Body.String())
	}
	var resp provenanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Node.Type != "commit" {
		t.Errorf("expected node.type=commit, got %q", resp.Node.Type)
	}
	// The commit was committed_for feat-prov-test and produced_by sess-001.
	sawFeature, sawSession := false, false
	for _, l := range resp.Downstream {
		if l.ID == featureID && l.Relationship == "committed_for" {
			sawFeature = true
		}
		if l.ID == "sess-001" && l.Relationship == "produced_by" {
			sawSession = true
		}
	}
	if !sawFeature {
		t.Errorf("expected committed_for edge to %s in downstream; got %+v", featureID, resp.Downstream)
	}
	if !sawSession {
		t.Errorf("expected produced_by edge to sess-001 in downstream; got %+v", resp.Downstream)
	}
}

// TestProvenanceHandler_AgentNodeResolves verifies that agent nodes resolve
// (previously returned 404 because resolveProvenanceNode skipped agents).
func TestProvenanceHandler_AgentNodeResolves(t *testing.T) {
	database, featureID := setupProvenanceDB(t)
	_, err := database.Exec(
		`INSERT INTO agent_lineage_trace (trace_id, session_id, root_session_id, agent_name, feature_id) VALUES (?, ?, ?, ?, ?)`,
		"tr-1", "sess-agent", "sess-agent", "wipnote:feature-coder", featureID,
	)
	if err != nil {
		t.Fatalf("seed lineage: %v", err)
	}
	mux := buildSingleProjectMux(database, nil, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/provenance/wipnote:feature-coder", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET agent provenance: got %d, body: %s", w.Code, w.Body.String())
	}
	var resp provenanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Node.Type != "agent" {
		t.Errorf("expected node.type=agent, got %q", resp.Node.Type)
	}
	sawRanAs, sawWorkedOn := false, false
	for _, l := range resp.Downstream {
		if l.ID == "sess-agent" && l.Relationship == "ran_as" {
			sawRanAs = true
		}
		if l.ID == featureID && l.Relationship == "worked_on" {
			sawWorkedOn = true
		}
	}
	if !sawRanAs {
		t.Errorf("expected ran_as edge to sess-agent; got %+v", resp.Downstream)
	}
	if !sawWorkedOn {
		t.Errorf("expected worked_on edge to %s; got %+v", featureID, resp.Downstream)
	}
}
