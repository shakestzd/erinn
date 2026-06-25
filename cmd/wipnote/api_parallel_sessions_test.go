package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// insertParallelSession inserts a session for parallel-session grouping tests.
func insertParallelSession(t *testing.T, database *sql.DB, sid, agent, projectDir, familyID, status, model string) {
	t.Helper()
	now := time.Now().UTC()
	sess := &models.Session{
		SessionID:       sid,
		AgentAssigned:   agent,
		CreatedAt:       now,
		Status:          status,
		SessionFamilyID: familyID,
		Model:           model,
		ProjectDir:      projectDir,
	}
	if err := dbpkg.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession %s: %v", sid, err)
	}
	if familyID != "" {
		if err := dbpkg.SetSessionFamilyID(database, sid, familyID); err != nil {
			t.Fatalf("SetSessionFamilyID %s: %v", sid, err)
		}
	}
}

// TestSessionsAPI_IncludesExecutionRootAndFamily asserts that /api/sessions
// response includes exec_root and session_family_id fields for each session.
func TestSessionsAPI_IncludesExecutionRootAndFamily(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := "/test/project"
	insertParallelSession(t, database, "sess-a1", "claude-code", projectDir, "fam-001", "active", "claude-sonnet-4-5")

	for i := 0; i < 5; i++ {
		_, _ = database.Exec(
			`INSERT INTO messages (session_id, role, content, ordinal) VALUES (?, ?, ?, ?)`,
			"sess-a1", "user", "hello", i+1,
		)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	sessionsHandler(database, projectDir, "/test/project/.wipnote")(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}

	var sessions []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected at least one session in response")
	}

	s := sessions[0]

	famID, hasFam := s["session_family_id"]
	if !hasFam {
		t.Error("session_family_id field missing from sessions API response")
	}
	if famID != "fam-001" {
		t.Errorf("session_family_id = %v, want fam-001", famID)
	}

	if _, ok := s["exec_root"]; !ok {
		t.Error("exec_root field missing from sessions API response")
	}
	if _, ok := s["canonical_project"]; !ok {
		t.Error("canonical_project field missing from sessions API response")
	}
	if _, ok := s["harness"]; !ok {
		t.Error("harness field missing from sessions API response")
	}
}

// TestDashboardParallelSessionsAPI asserts that /api/sessions/parallel returns
// active sessions grouped canonical project -> session family -> session with
// identity fields for plan-c3bbb1ed consumption.
//
// Documented default grouping:
//
//	Level 1: canonical_project (sessions.project_dir)
//	Level 2: session_family_id
//	Level 3: individual session with full identity fields
func TestDashboardParallelSessionsAPI(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := "/repo/myproject"
	insertParallelSession(t, database, "s-claude", "claude-code", projectDir, "fam-x", "active", "claude-sonnet")
	insertParallelSession(t, database, "s-codex", "codex", projectDir, "fam-x", "active", "gpt-4o")
	insertParallelSession(t, database, "s-solo", "gemini", projectDir, "fam-y", "active", "gemini-2")

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/parallel", nil)
	w := httptest.NewRecorder()
	parallelSessionsHandler(database)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, ok := resp["groups"]; !ok {
		t.Error("response missing 'groups' field")
	}
	if _, ok := resp["active_count"]; !ok {
		t.Error("response missing 'active_count' field")
	}

	groups, ok := resp["groups"].([]any)
	if !ok || len(groups) == 0 {
		t.Fatal("groups is not a non-empty array")
	}

	g := groups[0].(map[string]any)
	if _, ok := g["canonical_project"]; !ok {
		t.Error("group missing canonical_project")
	}
	if _, ok := g["families"]; !ok {
		t.Error("group missing families")
	}

	families := g["families"].([]any)
	for _, fAny := range families {
		f := fAny.(map[string]any)
		if _, ok := f["session_family_id"]; !ok {
			t.Error("family missing session_family_id")
		}
		sessionsAny, ok := f["sessions"].([]any)
		if !ok {
			t.Error("family missing sessions array")
			continue
		}
		for _, sAny := range sessionsAny {
			sess := sAny.(map[string]any)
			for _, field := range []string{
				"session_id", "harness", "model", "exec_root", "claim_collision",
			} {
				if _, ok := sess[field]; !ok {
					t.Errorf("session in parallel API missing field %q", field)
				}
			}
		}
	}
}

// TestParallelSessionsAPI_ActiveWorkItemsOwnership asserts that work_item_id
// in /api/sessions/parallel is resolved from active_work_items (preferred) with
// fallback to sessions.active_feature_id. This verifies the fix for roborev
// job-3095 slice-7 finding: sessions that claim via active_work_items were
// previously reported as having no work_item_id (and thus no claim status/
// collision) in the parallel endpoint.
func TestParallelSessionsAPI_ActiveWorkItemsOwnership(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "awi.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := "/repo/awi-project"
	// Insert two sessions: one claims via active_work_items, one via
	// sessions.active_feature_id (legacy). Both should surface work_item_id.
	insertParallelSession(t, database, "s-awi-1", "claude-code", projectDir, "fam-awi", "active", "claude-haiku")
	insertParallelSession(t, database, "s-legacy", "codex", projectDir, "fam-awi", "active", "gpt-4o-mini")

	// Seed the feature row for FK constraint.
	_, _ = database.Exec(`INSERT INTO features (id, type, title, status) VALUES (?, 'feature', 'AWI Test', 'in-progress')`, "feat-awi-001")

	// Write via active_work_items (the Batch B path — SetActiveWorkItem).
	if err := dbpkg.SetActiveWorkItem(database, "s-awi-1", dbpkg.AgentRootSentinel, "feat-awi-001"); err != nil {
		t.Fatalf("SetActiveWorkItem: %v", err)
	}
	// Write legacy path directly on sessions table for s-legacy.
	_, err = database.Exec(`UPDATE sessions SET active_feature_id = ? WHERE session_id = ?`, "feat-awi-001", "s-legacy")
	if err != nil {
		t.Fatalf("UPDATE active_feature_id: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/parallel", nil)
	w := httptest.NewRecorder()
	parallelSessionsHandler(database)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	var resp struct {
		Groups []projectGroup `json:"groups"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Groups) == 0 {
		t.Fatal("no project groups returned")
	}

	// Collect all sessions from all groups and families.
	byID := map[string]parallelSessionIdentity{}
	for _, pg := range resp.Groups {
		for _, fg := range pg.Families {
			for _, s := range fg.Sessions {
				byID[s.SessionID] = s
			}
		}
	}

	// s-awi-1 must surface feat-awi-001 via active_work_items.
	if s, ok := byID["s-awi-1"]; !ok {
		t.Error("s-awi-1 not in response")
	} else if s.WorkItemID != "feat-awi-001" {
		t.Errorf("s-awi-1 work_item_id = %q, want feat-awi-001 (active_work_items path)", s.WorkItemID)
	}

	// s-legacy must surface feat-awi-001 via sessions.active_feature_id fallback.
	if s, ok := byID["s-legacy"]; !ok {
		t.Error("s-legacy not in response")
	} else if s.WorkItemID != "feat-awi-001" {
		t.Errorf("s-legacy work_item_id = %q, want feat-awi-001 (active_feature_id fallback)", s.WorkItemID)
	}
}

// TestParallelSessionsAPI_SubagentVisibility asserts that /api/sessions/parallel
// includes subagent rows and sets exec_root from parent_session_id, as required
// by the documented response contract. Fixes roborev job-3095 slice-7 finding
// where is_subagent=FALSE excluded subagents from Level 3 data entirely.
func TestParallelSessionsAPI_SubagentVisibility(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "subagent.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := "/repo/subagent-project"
	now := time.Now().UTC()

	// Insert root session.
	root := &models.Session{
		SessionID:     "s-root",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
		ProjectDir:    projectDir,
	}
	if err := dbpkg.InsertSession(database, root); err != nil {
		t.Fatalf("InsertSession root: %v", err)
	}

	// Insert subagent session under root.
	sub := &models.Session{
		SessionID:       "s-subagent",
		AgentAssigned:   "claude-code",
		CreatedAt:       now.Add(time.Second),
		Status:          "active",
		ProjectDir:      projectDir,
		ParentSessionID: "s-root",
		IsSubagent:      true,
	}
	if err := dbpkg.InsertSession(database, sub); err != nil {
		t.Fatalf("InsertSession subagent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/parallel", nil)
	w := httptest.NewRecorder()
	parallelSessionsHandler(database)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	var resp struct {
		Groups []projectGroup `json:"groups"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byID := map[string]parallelSessionIdentity{}
	for _, pg := range resp.Groups {
		for _, fg := range pg.Families {
			for _, s := range fg.Sessions {
				byID[s.SessionID] = s
			}
		}
	}

	// Root session must appear.
	if s, ok := byID["s-root"]; !ok {
		t.Error("root session s-root not in parallel response")
	} else if s.ExecRoot != "s-root" {
		t.Errorf("s-root exec_root = %q, want s-root", s.ExecRoot)
	}

	// Subagent must appear with exec_root pointing to root (not itself).
	if s, ok := byID["s-subagent"]; !ok {
		t.Error("subagent s-subagent not in parallel response (subagent visibility fix required)")
	} else if s.ExecRoot != "s-root" {
		t.Errorf("s-subagent exec_root = %q, want s-root (parent_session_id)", s.ExecRoot)
	}
}

// TestSessionsAPI_CollisionFieldPresent verifies claim_collision and
// claim_status are present in sessions list (stable contract for plan-c3bbb1ed).
func TestSessionsAPI_CollisionFieldPresent(t *testing.T) {
	if testing.Short() {
		t.Skip("drives sessions API integration flow")
	}

	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := "/proj/alpha"
	insertParallelSession(t, database, "sess-col1", "claude-code", projectDir, "fam-col", "active", "claude-haiku")
	for i := 0; i < 5; i++ {
		_, _ = database.Exec(
			`INSERT INTO messages (session_id, role, content, ordinal) VALUES (?, ?, ?, ?)`,
			"sess-col1", "user", "msg", i+1,
		)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	sessionsHandler(database, projectDir, "/proj/alpha/.wipnote")(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	var sessions []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected sessions")
	}

	s := sessions[0]
	if _, ok := s["claim_collision"]; !ok {
		t.Error("claim_collision missing from sessions API")
	}
	if _, ok := s["claim_status"]; !ok {
		t.Error("claim_status missing from sessions API")
	}
}

// TestSessionsAPI_IncludesFilesArray verifies that /api/sessions includes a
// files[] array per session (feat-f1c6f92e Tier 0). When feature_files rows
// exist for a session they must appear in the response; when none exist the
// field must still be present as an empty array (never absent/null).
func TestSessionsAPI_IncludesFilesArray(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := "/test/files-array"

	insertParallelSession(t, database, "sess-files-1", "claude-code", projectDir, "fam-files", "active", "claude-sonnet-4-5")
	for i := 0; i < 5; i++ {
		_, _ = database.Exec(
			`INSERT INTO messages (session_id, role, content, ordinal) VALUES (?, ?, ?, ?)`,
			"sess-files-1", "user", "hello", i+1,
		)
	}

	// Seed a feature and a feature_files row for this session.
	now := time.Now().UTC()
	_ = dbpkg.UpsertFeature(database, &dbpkg.Feature{
		ID: "feat-files-test", Type: "feature", Title: "Files Test",
		Status: "in-progress", Priority: "medium",
		CreatedAt: now, UpdatedAt: now,
	})
	_ = dbpkg.UpsertFeatureFile(database, &models.FeatureFile{
		ID:        "ff-api-1",
		FeatureID: "feat-files-test",
		FilePath:  "/src/main.go",
		Operation: "write",
		SessionID: "sess-files-1",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	sessionsHandler(database, projectDir, "/test/files-array/.wipnote")(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}

	var sessions []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected at least one session")
	}

	s := sessions[0]

	// files[] must be present.
	rawFiles, ok := s["files"]
	if !ok {
		t.Fatal("files field missing from /api/sessions response")
	}

	// files[] must be a JSON array ([]interface{} after decode).
	files, ok := rawFiles.([]interface{})
	if !ok {
		t.Fatalf("files field type %T, want []interface{}", rawFiles)
	}

	// Expect 1 file for this session.
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	f := files[0].(map[string]interface{})
	if f["file_path"] != "/src/main.go" {
		t.Errorf("file_path = %v, want /src/main.go", f["file_path"])
	}
	if f["operation"] != "write" {
		t.Errorf("operation = %v, want write", f["operation"])
	}
}

// TestSessionsAPI_FilesArrayEmptyWhenNoFiles verifies that sessions with no
// feature_files rows still have files:[] (empty array, not null/absent).
func TestSessionsAPI_FilesArrayEmptyWhenNoFiles(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := "/test/no-files"
	insertParallelSession(t, database, "sess-nofiles", "claude-code", projectDir, "fam-nf", "active", "claude-haiku")
	for i := 0; i < 5; i++ {
		_, _ = database.Exec(
			`INSERT INTO messages (session_id, role, content, ordinal) VALUES (?, ?, ?, ?)`,
			"sess-nofiles", "user", "hello", i+1,
		)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	sessionsHandler(database, projectDir, "/test/no-files/.wipnote")(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	var sessions []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected sessions")
	}

	s := sessions[0]
	rawFiles, ok := s["files"]
	if !ok {
		t.Fatal("files field missing when session has no feature_files")
	}
	files, ok := rawFiles.([]interface{})
	if !ok {
		t.Fatalf("files field type %T, want []interface{}", rawFiles)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files for session with no feature_files, got %d", len(files))
	}
}

// TestSessionsAPI_DotProjectDirVisible verifies bug-fc3b5559 fix #1: sessions
// stored with project_dir='.' (SessionStart-hook path, via NormalizeProjectDir)
// are included when querying by the absolute project dir. The DB is per-project
// so '.' is unambiguous within it.
func TestSessionsAPI_DotProjectDirVisible(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := "/abs/path/to/project"

	// Insert a session stored with project_dir='.' (as the hook does) with enough
	// messages to pass the msg_count >= 5 threshold.
	insertParallelSession(t, database, "sess-dot", "claude-code", ".", "fam-dot", "completed", "claude-sonnet-4-5")
	for i := 0; i < 5; i++ {
		_, _ = database.Exec(
			`INSERT INTO messages (session_id, role, content, ordinal) VALUES (?, ?, ?, ?)`,
			"sess-dot", "user", "hello", i+1,
		)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	sessionsHandler(database, projectDir, projectDir+"/.wipnote")(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	var sessions []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s["session_id"] == "sess-dot" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("session with project_dir='.' not returned by sessionsHandler for abs projectDir %q", projectDir)
	}
}

// TestSessionsAPI_ActiveSessionUnderMsgThreshold verifies bug-fc3b5559 fix #2:
// an active session with fewer than 5 messages is visible (msg_count threshold
// must not exclude active sessions). A stale completed session with <5 msgs
// remains hidden.
func TestSessionsAPI_ActiveSessionUnderMsgThreshold(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	projectDir := "/proj/threshold-test"

	// Active session with only 2 messages — should be visible.
	insertParallelSession(t, database, "sess-active-few", "claude-code", projectDir, "fam-th", "active", "claude-haiku")
	for i := 0; i < 2; i++ {
		_, _ = database.Exec(
			`INSERT INTO messages (session_id, role, content, ordinal) VALUES (?, ?, ?, ?)`,
			"sess-active-few", "user", "hi", i+1,
		)
	}

	// Completed session with 3 messages — should stay hidden (below threshold).
	insertParallelSession(t, database, "sess-done-few", "claude-code", projectDir, "fam-th2", "completed", "claude-haiku")
	for i := 0; i < 3; i++ {
		_, _ = database.Exec(
			`INSERT INTO messages (session_id, role, content, ordinal) VALUES (?, ?, ?, ?)`,
			"sess-done-few", "user", "bye", i+1,
		)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	sessionsHandler(database, projectDir, projectDir+"/.wipnote")(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	var sessions []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}

	foundActive, foundDone := false, false
	for _, s := range sessions {
		switch s["session_id"] {
		case "sess-active-few":
			foundActive = true
		case "sess-done-few":
			foundDone = true
		}
	}
	if !foundActive {
		t.Error("active session with <5 msgs should be visible but was not returned")
	}
	if foundDone {
		t.Error("completed session with <5 msgs should be hidden but was returned")
	}
}
