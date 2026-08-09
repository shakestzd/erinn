package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

// TestRenderKanban_DataContractAndOverflowSafety asserts the embedded
// dashboard JS/CSS honor the Tier 4 rendering contract. A full headless
// browser smoke is out of scope here (no JS test harness in-tree), so per the
// plan this is a Go-level assertion on the rendered template/data contract:
// buildKanbanCard must consume the four signals (owner badge, step progress,
// conflict badge, last-activity/file) and the CSS must prevent text overflow.
func TestRenderKanban_DataContractAndOverflowSafety(t *testing.T) {
	js, err := dashboardFS.ReadFile("dashboard/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	app := string(js)
	for _, needle := range []string{
		"kanban-badge-conflict",               // conflict badge (who ⚠ semantics)
		"f.file_conflict",                     // conflict signal consumed
		"f.active_session",                    // owner badge
		"f.active_owner_harness",              // owner CLI/harness
		"kanban-badge-steps",                  // step progress
		"f.steps_completed",                   // step counters
		"f.step_tracking_supported === false", // honest unsupported state
		"steps (not live)",                    // no false live-steps wording
		"f.last_activity_age_seconds",         // last-activity age
		"f.last_touched_file",                 // last touched file
		"track-stat-active",                   // track-group header counts
		"step-provenance",                     // work-detail step provenance
	} {
		if !strings.Contains(app, needle) {
			t.Errorf("app.js missing required Kanban contract token %q", needle)
		}
	}

	css, err := dashboardFS.ReadFile("dashboard/css/components.css")
	if err != nil {
		t.Fatalf("read components.css: %v", err)
	}
	c := string(css)
	// Overflow safety: the badge and file-line rules must clip, not wrap.
	for _, rule := range []string{
		".kanban-badge {",
		"text-overflow: ellipsis;",
		".kanban-card-file {",
		".kanban-card-exec {",
	} {
		if !strings.Contains(c, rule) {
			t.Errorf("components.css missing overflow-safe Kanban rule %q", rule)
		}
	}
	if !strings.Contains(c, "flex-wrap: wrap;") {
		t.Errorf("components.css: kanban-card-exec must flex-wrap to avoid horizontal overflow")
	}

	// Regression: work-detail must still render the FULL step list and the
	// feature activity panels. The provenance addition must be additive
	// (guarded), not a replacement of the existing step rendering.
	for _, needle := range []string{
		"node.steps.forEach(function(step) {", // full step list loop intact
		"step.description || step.step_id",    // step text still rendered
		"if (provBits.length > 0) {",          // provenance is additive/guarded
		"function renderWorkDetail(",          // detail renderer intact
	} {
		if !strings.Contains(app, needle) {
			t.Errorf("app.js work-detail regression: missing %q", needle)
		}
	}
}

// TestFeatureActivityRouter_RegressionStillServes confirms the existing
// /api/features/{id}/activity panel keeps working after the Tier 4 changes
// (the work-detail feature-activity timeline must still render).
func TestFeatureActivityRouter_RegressionStillServes(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	const fid = "feat-act-reg"
	mustExec(t, database,
		`INSERT INTO features (id, type, title, status) VALUES (?, 'feature', 'Activity Reg', 'in-progress')`, fid)
	mustExec(t, database,
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at)
		 VALUES ('sess-act', 'claude-code', 'active', CURRENT_TIMESTAMP)`)
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, session_id, feature_id, event_type, tool_name, timestamp)
		 VALUES ('evt-act-1', 'claude-code', 'sess-act', ?, 'start', 'Read', CURRENT_TIMESTAMP)`, fid)

	mux := buildSingleProjectMux(database, nil, t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/api/features/"+fid+"/activity", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("activity route: got %d, want 200; body %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["feature_id"] != fid {
		t.Errorf("feature_id: got %v, want %s", resp["feature_id"], fid)
	}
	if tot, _ := resp["total_events"].(float64); tot < 1 {
		t.Errorf("total_events: got %v, want >=1", resp["total_events"])
	}
}

// TestFeaturesHandler_WorkBoardSignals verifies the Tier 4 execution-visibility
// signals are merged into the /api/features payload: active owner session +
// liveness, step counters, last activity, last touched file, moved/reassigned
// metadata, and the conflict signal. READ-ONLY — the handler performs no writes.
func TestFeaturesHandler_WorkBoardSignals(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	const fid = "feat-wb-1"
	mustExec(t, database,
		`INSERT INTO features (id, type, title, status, steps_total, steps_completed)
		 VALUES (?, 'feature', 'Work Board Feature', 'in-progress', 5, 2)`, fid)

	// Owner session with a fresh claim heartbeat => live.
	mustExec(t, database,
		`INSERT INTO sessions (session_id, agent_assigned, status, model, created_at)
		 VALUES ('sess-wb', 'claude-code', 'active', 'opus-4', CURRENT_TIMESTAMP)`)
	mustExec(t, database,
		`INSERT INTO active_work_items (session_id, agent_id, work_item_id, claimed_at)
		 VALUES ('sess-wb', 'claude-code', ?, CURRENT_TIMESTAMP)`, fid)
	// last_heartbeat_at MUST be RFC3339 — SessionLivenessByHeartbeat parses it
	// strictly with time.Parse(time.RFC3339, …); SQLite's CURRENT_TIMESTAMP
	// space form would fail to parse and (falsely) report not-live.
	mustExec(t, database,
		`INSERT INTO claims (claim_id, work_item_id, owner_session_id, owner_agent, status, lease_expires_at, last_heartbeat_at)
		 VALUES ('clm-wb', ?, 'sess-wb', 'claude-code', 'in_progress', datetime('now','+1 hour'), strftime('%Y-%m-%dT%H:%M:%SZ','now'))`, fid)
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, session_id, feature_id, event_type, tool_name, timestamp)
		 VALUES ('evt-wb-1', 'claude-code', 'sess-wb', ?, 'claim.handoff', 'wipnote', CURRENT_TIMESTAMP)`, fid)
	mustExec(t, database,
		`INSERT INTO feature_files (id, feature_id, file_path, operation, session_id, first_seen, last_seen, created_at)
		 VALUES ('ff-wb-1', ?, 'cmd/wipnote/api.go', 'modified', 'sess-wb', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, fid)

	mux := buildSingleProjectMux(database, nil, t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/api/features", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/features: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var feats []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&feats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var f map[string]any
	for _, c := range feats {
		if c["id"] == fid {
			f = c
			break
		}
	}
	if f == nil {
		t.Fatalf("feature %s not in payload", fid)
	}

	if f["active_session"] != "sess-wb" {
		t.Errorf("active_session: got %v, want sess-wb", f["active_session"])
	}
	if f["active_session_live"] != true {
		t.Errorf("active_session_live: got %v, want true (fresh heartbeat)", f["active_session_live"])
	}
	if f["active_owner_harness"] != "claude-code" {
		t.Errorf("active_owner_harness: got %v, want claude-code", f["active_owner_harness"])
	}
	if f["steps_total"].(float64) != 5 || f["steps_completed"].(float64) != 2 {
		t.Errorf("step counters: got %v/%v, want 2/5", f["steps_completed"], f["steps_total"])
	}
	if age, ok := f["last_activity_age_seconds"].(float64); !ok || age < 0 {
		t.Errorf("last_activity_age_seconds: got %v, want >=0", f["last_activity_age_seconds"])
	}
	if f["last_touched_file"] != "cmd/wipnote/api.go" {
		t.Errorf("last_touched_file: got %v, want cmd/wipnote/api.go", f["last_touched_file"])
	}
	if f["moved_recently"] != true {
		t.Errorf("moved_recently: got %v, want true (claim.handoff event in window)", f["moved_recently"])
	}
	if f["reassigned_recently"] != true {
		t.Errorf("reassigned_recently: got %v, want true (claim.handoff in window)", f["reassigned_recently"])
	}
	if _, ok := f["file_conflict"]; !ok {
		t.Errorf("file_conflict signal missing from payload")
	}
	// Claude-code => step tracking IS supported (honest live state).
	if f["step_tracking_supported"] != true {
		t.Errorf("step_tracking_supported: got %v, want true for claude-code", f["step_tracking_supported"])
	}
}

// TestFeaturesHandler_NoFalseLiveSteps_CodexHarness proves the dashboard
// contract never implies live step tracking for a harness that cannot emit
// step events. Codex owns the work item; step_tracking_supported MUST be false
// and the detail MUST explain the unsupported state.
func TestFeaturesHandler_NoFalseLiveSteps_CodexHarness(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	const fid = "feat-codex-1"
	mustExec(t, database,
		`INSERT INTO features (id, type, title, status, steps_total, steps_completed)
		 VALUES (?, 'feature', 'Codex Owned', 'in-progress', 3, 0)`, fid)
	mustExec(t, database,
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at)
		 VALUES ('sess-cx', 'codex', 'active', CURRENT_TIMESTAMP)`)
	mustExec(t, database,
		`INSERT INTO active_work_items (session_id, agent_id, work_item_id, claimed_at)
		 VALUES ('sess-cx', 'codex', ?, CURRENT_TIMESTAMP)`, fid)

	mux := buildSingleProjectMux(database, nil, t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/api/features", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var feats []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&feats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var f map[string]any
	for _, c := range feats {
		if c["id"] == fid {
			f = c
		}
	}
	if f == nil {
		t.Fatalf("feature %s missing", fid)
	}
	if f["step_tracking_supported"] != false {
		t.Fatalf("step_tracking_supported: got %v, want false for codex (no false live-steps state)", f["step_tracking_supported"])
	}
	detail, _ := f["step_tracking_detail"].(string)
	if detail == "" {
		t.Fatalf("step_tracking_detail must document the unsupported state, got empty")
	}
}
