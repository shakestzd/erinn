package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/htmlgraph/internal/terminal"
)

// mockTerminalManager is a test double implementing terminalManager interface.
type mockTerminalManager struct {
	lastReq      terminal.StartRequest
	lastDir      string
	returnID     string
	returnPort   int
	returnPid    int
	returnErr    error
	sessions     []terminal.SessionView
	stopByIDErr  error
	stopByPIDErr error
	stopAllCalled bool
	stoppedID    string
	stoppedPID   int
}

func (m *mockTerminalManager) Start(req terminal.StartRequest, defaultDir string) (string, int, int, error) {
	m.lastDir = defaultDir
	m.lastReq = req
	return m.returnID, m.returnPort, m.returnPid, m.returnErr
}

func (m *mockTerminalManager) StopByID(id string) error {
	m.stoppedID = id
	return m.stopByIDErr
}

func (m *mockTerminalManager) StopByPID(pid int) error {
	m.stoppedPID = pid
	return m.stopByPIDErr
}

func (m *mockTerminalManager) StopAll() {
	m.stopAllCalled = true
}

func (m *mockTerminalManager) Sessions() []terminal.SessionView {
	return m.sessions
}

// TestTerminalStartHandler_EmptyBody verifies that POST {} returns 200 and spawns
// the default claude --dev session in the server's projectDir (back-compat).
func TestTerminalStartHandler_EmptyBody(t *testing.T) {
	mock := &mockTerminalManager{returnID: "test-uuid-1234", returnPort: 9999, returnPid: 1234}
	handler := handleTerminalStart("/srv/project", mock)

	req := httptest.NewRequest(http.MethodPost, "/api/terminal/start", bytes.NewBufferString("{}"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}

	// Empty body should default to agent=claude, mode=dev.
	if mock.lastReq.Agent != "" {
		t.Errorf("expected Agent to be empty (handler passes through; terminal applies default), got %q", mock.lastReq.Agent)
	}
	if mock.lastReq.Mode != "" {
		t.Errorf("expected Mode to be empty (handler passes through; terminal applies default), got %q", mock.lastReq.Mode)
	}
	if mock.lastDir != "/srv/project" {
		t.Errorf("expected projectDir /srv/project, got %q", mock.lastDir)
	}

	var resp terminalStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Port != 9999 {
		t.Errorf("port: got %d, want 9999", resp.Port)
	}
	if resp.Pid != 1234 {
		t.Errorf("pid: got %d, want 1234", resp.Pid)
	}
	if resp.ID != "test-uuid-1234" {
		t.Errorf("id: got %q, want test-uuid-1234", resp.ID)
	}
	if resp.State != "pending" {
		t.Errorf("state: got %q, want pending", resp.State)
	}
	// Empty body → handler echoes back defaults.
	if resp.Agent != "claude" {
		t.Errorf("agent in response: got %q, want claude", resp.Agent)
	}
	if resp.Mode != "dev" {
		t.Errorf("mode in response: got %q, want dev", resp.Mode)
	}
}

// TestTerminalStartHandler_CustomAgent verifies that custom agent/mode/cwd/work_item
// fields are decoded and passed through to the manager correctly.
func TestTerminalStartHandler_CustomAgent(t *testing.T) {
	mock := &mockTerminalManager{returnID: "test-uuid-5678", returnPort: 8888, returnPid: 5678}
	handler := handleTerminalStart("/srv/project", mock)

	body := `{"agent":"codex","mode":"dev","cwd":"/mock/test","work_item":"feat-abc"}`
	req := httptest.NewRequest(http.MethodPost, "/api/terminal/start", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}

	if mock.lastReq.Agent != "codex" {
		t.Errorf("agent: got %q, want codex", mock.lastReq.Agent)
	}
	if mock.lastReq.Mode != "dev" {
		t.Errorf("mode: got %q, want dev", mock.lastReq.Mode)
	}
	if mock.lastReq.CWD != "/mock/test" {
		t.Errorf("cwd: got %q, want /mock/test", mock.lastReq.CWD)
	}
	if mock.lastReq.WorkItem != "feat-abc" {
		t.Errorf("work_item: got %q, want feat-abc", mock.lastReq.WorkItem)
	}

	var resp terminalStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.WorkItem != "feat-abc" {
		t.Errorf("work_item in response: got %q, want feat-abc", resp.WorkItem)
	}
	if resp.Agent != "codex" {
		t.Errorf("agent in response: got %q, want codex", resp.Agent)
	}
}

// TestTerminalStartHandler_MethodNotAllowed verifies that GET returns 405.
func TestTerminalStartHandler_MethodNotAllowed(t *testing.T) {
	mock := &mockTerminalManager{}
	handler := handleTerminalStart("/srv/project", mock)

	req := httptest.NewRequest(http.MethodGet, "/api/terminal/start", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", rec.Code)
	}
}

// TestHandleTerminalSessions_ReturnsJSON verifies GET /api/terminal/sessions returns correct JSON.
func TestHandleTerminalSessions_ReturnsJSON(t *testing.T) {
	now := time.Now()
	mock := &mockTerminalManager{
		sessions: []terminal.SessionView{
			{ID: "uuid-1", Agent: "claude", Mode: "dev", CWD: "/proj1", WorkItem: "feat-1", Port: 9001, StartedAt: now, State: "live"},
			{ID: "uuid-2", Agent: "codex", Mode: "normal", CWD: "/proj2", WorkItem: "", Port: 9002, StartedAt: now, State: "pending"},
		},
	}
	handler := handleTerminalSessions(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/terminal/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}

	var sessions []terminal.SessionView
	if err := json.Unmarshal(rec.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].ID != "uuid-1" {
		t.Errorf("session[0].ID: got %q, want uuid-1", sessions[0].ID)
	}
	if sessions[0].Agent != "claude" {
		t.Errorf("session[0].Agent: got %q, want claude", sessions[0].Agent)
	}
	if sessions[0].State != "live" {
		t.Errorf("session[0].State: got %q, want live", sessions[0].State)
	}
	if sessions[1].ID != "uuid-2" {
		t.Errorf("session[1].ID: got %q, want uuid-2", sessions[1].ID)
	}
}

// TestHandleTerminalStopByID verifies POST /api/terminal/stop with {id:...} calls StopByID.
func TestHandleTerminalStopByID(t *testing.T) {
	mock := &mockTerminalManager{}
	handler := handleTerminalStop(mock)

	body := `{"id":"some-uuid-1234"}`
	req := httptest.NewRequest(http.MethodPost, "/api/terminal/stop", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}
	if mock.stoppedID != "some-uuid-1234" {
		t.Errorf("stoppedID: got %q, want some-uuid-1234", mock.stoppedID)
	}
}

// TestHandleTerminalStopByPID verifies back-compat: POST with {pid:...} calls StopByPID.
func TestHandleTerminalStopByPID(t *testing.T) {
	mock := &mockTerminalManager{}
	handler := handleTerminalStop(mock)

	body := `{"pid":1234}`
	req := httptest.NewRequest(http.MethodPost, "/api/terminal/stop", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}
	if mock.stoppedPID != 1234 {
		t.Errorf("stoppedPID: got %d, want 1234", mock.stoppedPID)
	}
}

// TestHandleTerminalStopAll verifies POST /api/terminal/stop-all kills all live sessions.
func TestHandleTerminalStopAll(t *testing.T) {
	mock := &mockTerminalManager{}
	handler := handleTerminalStopAll(mock)

	req := httptest.NewRequest(http.MethodPost, "/api/terminal/stop-all", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}
	if !mock.stopAllCalled {
		t.Error("expected StopAll to be called")
	}
}

// TestHandleTerminalStart_CwdKindMainIgnored verifies that cwd_kind="main" with a work_item
// does NOT attempt worktree resolution; it uses the projectDir as-is.
func TestHandleTerminalStart_CwdKindMainIgnored(t *testing.T) {
	mock := &mockTerminalManager{returnID: "uuid-main", returnPort: 7777, returnPid: 7001}
	handler := handleTerminalStart("/srv/project", mock)

	body := `{"agent":"codex","work_item":"feat-abc","cwd_kind":"main"}`
	req := httptest.NewRequest(http.MethodPost, "/api/terminal/start", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}

	// For cwd_kind="main", the CWD in the StartRequest should be empty (no resolution).
	if mock.lastReq.CWD != "" {
		t.Errorf("expected empty CWD for cwd_kind=main, got %q", mock.lastReq.CWD)
	}
}

// TestHandleTerminalStart_CwdKindInvalid verifies that an invalid cwd_kind returns 400.
func TestHandleTerminalStart_CwdKindInvalid(t *testing.T) {
	mock := &mockTerminalManager{}
	handler := handleTerminalStart("/srv/project", mock)

	body := `{"agent":"codex","work_item":"feat-abc","cwd_kind":"invalid-kind"}`
	req := httptest.NewRequest(http.MethodPost, "/api/terminal/start", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 for invalid cwd_kind", rec.Code)
	}
}

// TestHandleTerminalStart_CwdKindFeatureWorktreeNoWorkItem verifies that cwd_kind="feature-worktree"
// without a work_item is treated as "main" (no worktree resolution attempt).
func TestHandleTerminalStart_CwdKindFeatureWorktreeNoWorkItem(t *testing.T) {
	mock := &mockTerminalManager{returnID: "uuid-nowi", returnPort: 6666, returnPid: 6001}
	handler := handleTerminalStart("/srv/project", mock)

	body := `{"agent":"codex","cwd_kind":"feature-worktree"}`
	req := httptest.NewRequest(http.MethodPost, "/api/terminal/start", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Without work_item, should fall through and call Manager.Start normally.
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}
	// CWD should be empty (no resolution attempted when work_item is absent).
	if mock.lastReq.CWD != "" {
		t.Errorf("expected empty CWD without work_item, got %q", mock.lastReq.CWD)
	}
}

// TestHandleTerminalStart_CwdKindFieldInStruct verifies the terminalStartRequest struct
// has CwdKind field and that it's decoded from JSON "cwd_kind".
func TestHandleTerminalStart_CwdKindFieldInStruct(t *testing.T) {
	// The JSON decoder should pick up cwd_kind into the CwdKind field.
	// We verify this by sending a request with cwd_kind and verifying the handler
	// does not error on decoding.
	mock := &mockTerminalManager{returnID: "uuid-ck", returnPort: 5555, returnPid: 5001}
	handler := handleTerminalStart("/srv/project", mock)

	body := `{"agent":"gemini","work_item":"feat-xyz","cwd_kind":"main"}`
	req := httptest.NewRequest(http.MethodPost, "/api/terminal/start", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}

	var resp terminalStartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.WorkItem != "feat-xyz" {
		t.Errorf("work_item: got %q, want feat-xyz", resp.WorkItem)
	}
}

// TestHandleTerminalStopAll_EmptyBody verifies stop-all works with empty body (sendBeacon compat).
func TestHandleTerminalStopAll_EmptyBody(t *testing.T) {
	mock := &mockTerminalManager{}
	handler := handleTerminalStopAll(mock)

	req := httptest.NewRequest(http.MethodPost, "/api/terminal/stop-all", bytes.NewBufferString(""))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body)
	}
	if !mock.stopAllCalled {
		t.Error("expected StopAll to be called")
	}
}
