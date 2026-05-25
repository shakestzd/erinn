package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/db"
)

// setupPlanTestDB creates an in-memory DB with plan_feedback schema and inserts
// a test plan feature row. Returns the DB and plan ID.
func setupPlanTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	planID := "plan-route-test"
	_, err = database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES (?, 'plan', 'Route Test Plan', 'in-progress')`,
		planID,
	)
	if err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	return database, planID
}

// writeTempPlanHTML creates a temporary .wipnote/plans directory with a
// minimal plan HTML file and a matching YAML file. Returns the wipnoteDir.
func writeTempPlanHTML(t *testing.T, planID string) string {
	t.Helper()
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	html := `<!DOCTYPE html><html><body>` +
		`<article id="` + planID + `" data-type="plan" data-status="draft">` +
		`<header><h1>Test Plan</h1></header>` +
		`</article></body></html>`
	htmlPath := filepath.Join(plansDir, planID+".html")
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		t.Fatalf("write plan html: %v", err)
	}
	// Write a matching YAML file so parsePlanHTMLStatus (which now reads YAML)
	// can find the status. meta.status defaults to "draft" to match the HTML.
	yamlContent := "meta:\n  id: " + planID + "\n  title: Test Plan\n  status: draft\n  version: 1\n"
	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write plan yaml: %v", err)
	}
	return dir
}

// ---- extractPlanID ----------------------------------------------------------

func TestExtractPlanID(t *testing.T) {
	cases := []struct {
		path    string
		suffix  string
		want    string
		wantErr bool
	}{
		{"/api/plans/plan-abc/status", "/status", "plan-abc", false},
		{"/api/plans/plan-xyz/feedback", "/feedback", "plan-xyz", false},
		{"/api/plans/plan-123/finalize", "/finalize", "plan-123", false},
		{"/api/plans//status", "/status", "", true},
		{"/api/plans/plan-a/b/status", "/status", "", true},
		{"/other/path/status", "/status", "", true},
	}
	for _, tc := range cases {
		got, err := extractPlanID(tc.path, tc.suffix)
		if tc.wantErr {
			if err == nil {
				t.Errorf("extractPlanID(%q, %q): expected error, got %q", tc.path, tc.suffix, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("extractPlanID(%q, %q): unexpected error: %v", tc.path, tc.suffix, err)
			continue
		}
		if got != tc.want {
			t.Errorf("extractPlanID(%q, %q) = %q, want %q", tc.path, tc.suffix, got, tc.want)
		}
	}
}

// ---- planFileHandler --------------------------------------------------------

func TestPlanFileHandler_Serves(t *testing.T) {
	planID := "plan-file-test"
	wipnoteDir := writeTempPlanHTML(t, planID)

	handler := planFileHandler(wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/plans/"+planID+".html", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct == "" {
		t.Error("expected non-empty Content-Type")
	}
}

func TestPlanFileHandler_NotFound(t *testing.T) {
	wipnoteDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	handler := planFileHandler(wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/plans/plan-missing.html", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestPlanFileHandler_RejectsPathTraversal(t *testing.T) {
	wipnoteDir := t.TempDir()
	handler := planFileHandler(wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/plans/../secret.html", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	// http.NewRequest cleans the path, so we get a 404 (no file) not 400.
	// Acceptable: the traversal attempt is blocked either way.
	if w.Code == http.StatusOK {
		t.Error("expected non-200 for path traversal attempt")
	}
}

// ---- planStatusHandler ------------------------------------------------------

func TestPlanStatusHandler_OK(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	wipnoteDir := writeTempPlanHTML(t, planID)

	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("store feedback: %v", err)
	}

	handler := planStatusHandler(database, wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/plans/"+planID+"/status", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp planStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PlanID != planID {
		t.Errorf("plan_id: got %q, want %q", resp.PlanID, planID)
	}
	if resp.Status != "draft" {
		t.Errorf("status: got %q, want draft", resp.Status)
	}
	if resp.ApprovedCount != 1 {
		t.Errorf("approved_count: got %d, want 1", resp.ApprovedCount)
	}
}

func TestPlanStatusHandler_PlanNotFound(t *testing.T) {
	database, _ := setupPlanTestDB(t)
	wipnoteDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	handler := planStatusHandler(database, wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/plans/plan-missing/status", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

// ---- planFeedbackSubmitHandler ----------------------------------------------

func TestPlanFeedbackSubmitHandler_StoresFeedback(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	handler := planFeedbackSubmitHandler(database)

	body, _ := json.Marshal(planFeedbackRequest{
		Section: "design",
		Action:  "approve",
		Value:   "true",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/plans/"+planID+"/feedback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	entries, err := db.GetPlanFeedback(database, planID)
	if err != nil {
		t.Fatalf("get feedback: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("feedback count: got %d, want 1", len(entries))
	}
	if entries[0].Section != "design" || entries[0].Value != "true" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
}

func TestPlanFeedbackSubmitHandler_MissingFields(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	handler := planFeedbackSubmitHandler(database)

	body, _ := json.Marshal(map[string]string{"section": "design"}) // missing action
	req := httptest.NewRequest(http.MethodPost, "/api/plans/"+planID+"/feedback", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

// ---- planFinalizeHandler ----------------------------------------------------

func TestPlanFinalizeHandler_NotApproved(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	wipnoteDir := writeTempPlanHTML(t, planID)

	handler := planFinalizeHandler(database, wipnoteDir)
	req := httptest.NewRequest(http.MethodPost, "/api/plans/"+planID+"/finalize", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestPlanFinalizeHandler_Success(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	wipnoteDir := writeTempPlanHTML(t, planID)

	for _, section := range []string{"design", "outline"} {
		if err := db.StorePlanFeedback(database, planID, section, "approve", "true", ""); err != nil {
			t.Fatalf("store feedback: %v", err)
		}
	}

	handler := planFinalizeHandler(database, wipnoteDir)
	req := httptest.NewRequest(http.MethodPost, "/api/plans/"+planID+"/finalize", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "finalized" {
		t.Errorf("status field: got %v, want finalized", resp["status"])
	}

	// Verify HTML file was updated on disk.
	htmlStatus, err := parsePlanHTMLStatus(filepath.Join(wipnoteDir, "plans", planID+".html"))
	if err != nil {
		t.Fatalf("parsePlanHTMLStatus: %v", err)
	}
	if htmlStatus != "finalized" {
		t.Errorf("HTML data-status: got %q, want finalized", htmlStatus)
	}
}

// ---- planFeedbackReadHandler ------------------------------------------------

func TestPlanFeedbackReadHandler_StructuredResponse(t *testing.T) {
	database, planID := setupPlanTestDB(t)

	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("store approve: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "design", "comment", "looks good", ""); err != nil {
		t.Fatalf("store comment: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "outline", "answer", "async", "delivery-mode"); err != nil {
		t.Fatalf("store answer: %v", err)
	}

	handler := planFeedbackReadHandler(database)
	req := httptest.NewRequest(http.MethodGet, "/api/plans/"+planID+"/feedback", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp planFeedbackResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PlanID != planID {
		t.Errorf("plan_id: got %q, want %q", resp.PlanID, planID)
	}
	design, ok := resp.Sections["design"]
	if !ok {
		t.Fatal("missing 'design' section in response")
	}
	if !design.Approved {
		t.Error("design.approved: expected true")
	}
	if design.Comment != "looks good" {
		t.Errorf("design.comment: got %q, want 'looks good'", design.Comment)
	}
	if resp.Questions["delivery-mode"] != "async" {
		t.Errorf("questions[delivery-mode]: got %q, want async", resp.Questions["delivery-mode"])
	}
}

// ---- buildFeedbackResponse --------------------------------------------------

func TestBuildFeedbackResponse_AllApproved(t *testing.T) {
	entries := []db.PlanFeedback{
		{PlanID: "p1", Section: "a", Action: "approve", Value: "true"},
		{PlanID: "p1", Section: "b", Action: "approve", Value: "true"},
	}
	resp := buildFeedbackResponse("p1", entries)
	if resp.Status != "approved" {
		t.Errorf("status: got %q, want approved", resp.Status)
	}
}

func TestBuildFeedbackResponse_NotAllApproved(t *testing.T) {
	entries := []db.PlanFeedback{
		{PlanID: "p1", Section: "a", Action: "approve", Value: "true"},
		{PlanID: "p1", Section: "b", Action: "approve", Value: "false"},
	}
	resp := buildFeedbackResponse("p1", entries)
	if resp.Status != "draft" {
		t.Errorf("status: got %q, want draft", resp.Status)
	}
}

// ---- buildFeedbackResponse: chat messages ----------------------------------

func TestBuildFeedbackResponse_IncludesChatMessages(t *testing.T) {
	messagesJSON := `[{"role":"user","content":"hello","timestamp":"2026-04-07T10:00:00Z"},{"role":"assistant","content":"hi there","timestamp":"2026-04-07T10:00:01Z"}]`
	entries := []db.PlanFeedback{
		{PlanID: "p1", Section: "design", Action: "approve", Value: "true"},
		{PlanID: "p1", Section: "chat", Action: "messages", Value: messagesJSON},
	}
	resp := buildFeedbackResponse("p1", entries)

	if len(resp.ChatMessages) != 2 {
		t.Fatalf("chat_messages count: got %d, want 2", len(resp.ChatMessages))
	}
	if resp.ChatMessages[0].Role != "user" || resp.ChatMessages[0].Content != "hello" {
		t.Errorf("chat_messages[0]: got %+v", resp.ChatMessages[0])
	}
	if resp.ChatMessages[1].Role != "assistant" || resp.ChatMessages[1].Content != "hi there" {
		t.Errorf("chat_messages[1]: got %+v", resp.ChatMessages[1])
	}
}

func TestBuildFeedbackResponse_ChatDoesNotAffectApprovalStatus(t *testing.T) {
	// Chat section entries (session_id, messages) should not count as
	// "sections" for approval status calculation.
	messagesJSON := `[{"role":"user","content":"q","timestamp":"2026-04-07T10:00:00Z"}]`
	entries := []db.PlanFeedback{
		{PlanID: "p1", Section: "a", Action: "approve", Value: "true"},
		{PlanID: "p1", Section: "b", Action: "approve", Value: "true"},
		{PlanID: "p1", Section: "chat", Action: "messages", Value: messagesJSON},
		{PlanID: "p1", Section: "chat", Action: "session_id", Value: "sess-123"},
	}
	resp := buildFeedbackResponse("p1", entries)

	// Should be "approved" because a and b are both approved.
	// Chat section must not interfere.
	if resp.Status != "approved" {
		t.Errorf("status: got %q, want approved (chat should not affect approval)", resp.Status)
	}
	// Chat section should not appear in sections map.
	if _, hasChatSection := resp.Sections["chat"]; hasChatSection {
		t.Error("sections map should not contain 'chat' key")
	}
}

func TestBuildFeedbackResponse_NoChatMessages(t *testing.T) {
	entries := []db.PlanFeedback{
		{PlanID: "p1", Section: "design", Action: "approve", Value: "true"},
	}
	resp := buildFeedbackResponse("p1", entries)
	if len(resp.ChatMessages) != 0 {
		t.Errorf("chat_messages: expected empty, got %d", len(resp.ChatMessages))
	}
}

// ---- planChatHandler -------------------------------------------------------

func TestPlanChatHandler_MethodNotAllowed(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	wipnoteDir := writeTempPlanHTML(t, planID)

	router := planRouter(database, database, wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/plans/"+planID+"/chat", nil)
	w := httptest.NewRecorder()
	router(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func TestPlanChatHandler_EmptyMessage(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	wipnoteDir := writeTempPlanHTML(t, planID)

	router := planRouter(database, database, wipnoteDir)
	body, _ := json.Marshal(map[string]string{"message": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/plans/"+planID+"/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestPlanChatHandler_InvalidJSON(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	wipnoteDir := writeTempPlanHTML(t, planID)

	router := planRouter(database, database, wipnoteDir)
	req := httptest.NewRequest(http.MethodPost, "/api/plans/"+planID+"/chat",
		bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestPlanChatHandler_Unavailable(t *testing.T) {
	// When claude is not on PATH and no API key, should return 503.
	// This test only works reliably when claude is NOT installed,
	// which is the CI default. Skip if claude is available.
	database, planID := setupPlanTestDB(t)
	wipnoteDir := writeTempPlanHTML(t, planID)

	// Override PATH to ensure claude is not found.
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("ANTHROPIC_API_KEY", "")

	router := planRouter(database, database, wipnoteDir)
	body, _ := json.Marshal(map[string]string{"message": "hello"})
	req := httptest.NewRequest(http.MethodPost, "/api/plans/"+planID+"/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] == "" {
		t.Error("expected error message in response")
	}
}

func TestExtractPlanID_Chat(t *testing.T) {
	got, err := extractPlanID("/api/plans/plan-chat-test/chat", "/chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plan-chat-test" {
		t.Errorf("got %q, want plan-chat-test", got)
	}
}

// ---- planYAMLHandler --------------------------------------------------------

func writeTempPlanYAML(t *testing.T, planID, content string) string {
	t.Helper()
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	path := filepath.Join(plansDir, planID+".yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write plan yaml: %v", err)
	}
	return dir
}

func TestPlanYAMLEndpoint_Found(t *testing.T) {
	planID := "plan-yaml-test"
	yamlContent := "meta:\n  id: " + planID + "\ntitle: Test Plan\n"
	wipnoteDir := writeTempPlanYAML(t, planID, yamlContent)

	handler := planYAMLHandler(wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/plans/"+planID+"/yaml", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type: got %q, want text/plain; charset=utf-8", ct)
	}
	if w.Body.String() != yamlContent {
		t.Errorf("body: got %q, want %q", w.Body.String(), yamlContent)
	}
}

func TestPlanYAMLEndpoint_NotFound(t *testing.T) {
	wipnoteDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}

	handler := planYAMLHandler(wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/plans/nonexistent/yaml", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestPlanYAMLEndpoint_MethodNotAllowed(t *testing.T) {
	wipnoteDir := t.TempDir()

	handler := planYAMLHandler(wipnoteDir)
	req := httptest.NewRequest(http.MethodPost, "/api/plans/some-plan/yaml", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

// ---- planRenderHandler full-fidelity embed (feat-801f2273) ---------------------

// writeFullPlanYAML writes a YAML plan with slices carrying deps, a critic
// revision (issue) and an open question so the rendered page exercises the full
// review surface: triage badges, question-count pill, dep graph, slice controls.
func writeFullPlanYAML(t *testing.T, planID string) string {
	t.Helper()
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	yaml := `meta:
  id: ` + planID + `
  title: Full Fidelity Plan
  status: in-progress
  version: 2
design:
  problem: Ship the embed at full fidelity.
  goals:
    - Render scoped CSS.
slices:
  - num: 1
    title: First slice
    approval_status: approved
    files:
      - a.go
    critic_revisions:
      - source: opus
        severity: HIGH
        summary: Watch the brace matcher.
  - num: 2
    title: Second slice
    approval_status: pending
    deps:
      - 1
    questions:
      - id: q1
        text: Which scope container?
`
	if err := os.WriteFile(filepath.Join(plansDir, planID+".yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return dir
}

func TestPlanRenderHandler_FullFidelityEmbed(t *testing.T) {
	database, _ := setupPlanTestDB(t)
	planID := "plan-render-full"
	wipnoteDir := writeFullPlanYAML(t, planID)

	handler := planRenderHandler(database, wipnoteDir)
	req := httptest.NewRequest(http.MethodGet, "/api/plans/"+planID+"/render", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	out := w.Body.String()

	// 1. CSS must be SCOPED, not stripped. Phase A.2 keeps the full scoped CSS
	//    (including .plan-sidebar rules) but no longer emits the sidebar element.
	scopedMarkers := []string{
		planEmbedScope + "{",              // :root mapped onto the container
		planEmbedScope + " .plan-sidebar", // sidebar CSS rules scoped (element not emitted)
		planEmbedScope + " .slice-card",   // slice-card component CSS present
	}
	for _, m := range scopedMarkers {
		if !strings.Contains(out, m) {
			t.Errorf("expected scoped CSS marker %q in render output (full-fidelity); missing", m)
		}
	}
	// The plan's --accent custom property must survive.
	if !strings.Contains(out, "--accent:") {
		t.Error("expected --accent custom property in scoped CSS")
	}
	// The raw unscoped :root / body resets must NOT leak into the dashboard.
	if strings.Contains(out, ">:root") || strings.Contains(out, "\n:root") || strings.Contains(out, "<style>:root") {
		t.Errorf("unscoped :root leaked into embed output")
	}

	// 2. Phase A.2: embed is CONTENT ONLY — .plan-sidebar element must NOT be emitted.
	//    The dashboard builds its own per-slice nav from the injected slice-card data.
	if strings.Contains(out, `class="plan-sidebar"`) {
		t.Error("embed must not include .plan-sidebar element (content-only embed, Phase A.2)")
	}
	// Triage badge data must remain accessible via the slice cards (not the nav).
	if !strings.Contains(out, "badge-issues") {
		t.Error("expected .badge-issues in slice card (dashboard reads this for triage nav)")
	}
	if !strings.Contains(out, "badge-questions") {
		t.Error("expected .badge-questions in slice card (dashboard reads this for triage nav)")
	}

	// 3. #graph-data [data-node] bridge divs must be present (dep-graph + dashboard nav source).
	if !strings.Contains(out, `id="graph-data"`) {
		t.Error("expected #graph-data bridge divs in embed (dep-graph and slice triage data)")
	}

	// 4. The slice cards and dependency graph must render.
	if !strings.Contains(out, "slice-card") {
		t.Error("expected slice cards in embed")
	}
	if !strings.Contains(out, "dep-graph") {
		t.Error("expected dependency graph in embed")
	}

	// 5. Interactivity scripts (D3/dagre/plan JS) must still be emitted.
	if !strings.Contains(out, "<script") {
		t.Error("expected scripts (dep-graph/plan JS) preserved in embed")
	}
}

// ---- validSectionRe regex coverage (slice-4) -----------------------------------

func TestValidSectionRe_AcceptsSliceLevel(t *testing.T) {
	if !validSectionRe.MatchString("slice-3") {
		t.Error("expected validSectionRe to accept 'slice-3'")
	}
	if !validSectionRe.MatchString("slice-1") {
		t.Error("expected validSectionRe to accept 'slice-1'")
	}
	if !validSectionRe.MatchString("slice-99") {
		t.Error("expected validSectionRe to accept 'slice-99'")
	}
}

func TestValidSectionRe_AcceptsSliceQuestion(t *testing.T) {
	if !validSectionRe.MatchString("slice-3-question-q-error-handling") {
		t.Error("expected validSectionRe to accept 'slice-3-question-q-error-handling'")
	}
	if !validSectionRe.MatchString("slice-1-question-my-question") {
		t.Error("expected validSectionRe to accept 'slice-1-question-my-question'")
	}
}

func TestValidSectionRe_AcceptsSliceQuestion_Underscores(t *testing.T) {
	// Underscores are normalized to hyphens before the regex check.
	// 'slice_3_question_q_foo' normalizes to 'slice-3-question-q-foo'.
	section := "slice_3_question_q_foo"
	// Apply the same normalization logic as planFeedbackSubmitHandler.
	if rest, ok := strings.CutPrefix(section, "slice_"); ok {
		section = "slice-" + rest
	}
	// Also normalize remaining underscores in the question part.
	section = strings.ReplaceAll(section, "_", "-")
	if !validSectionRe.MatchString(section) {
		t.Errorf("expected normalized %q to match validSectionRe", section)
	}
}

func TestValidSectionRe_RejectsBadFormats(t *testing.T) {
	bad := []string{
		"slice-",                // no num
		"slice-abc",             // non-numeric num
		"slice-1-question-",     // no question ID
		"slice-1-questionn-foo", // typo: extra 'n'
		"slice-",
		"slice",
	}
	for _, s := range bad {
		if validSectionRe.MatchString(s) {
			t.Errorf("expected validSectionRe to REJECT %q, but it matched", s)
		}
	}
}

// ---- HTTP API integration tests (slice-4) --------------------------------------

// TestAPI_PostFeedback_SliceQuestionSection_Returns200 is the regression test
// mandated by the plan's done_when: POST /api/plans/<id>/feedback with
// section='slice-3-question-q-foo' must return 200, NOT 400.
func TestAPI_PostFeedback_SliceQuestionSection_Returns200(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	handler := planFeedbackSubmitHandler(database)

	body, _ := json.Marshal(planFeedbackRequest{
		Section: "slice-3-question-q-foo",
		Action:  "answer",
		Value:   "yes",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/plans/"+planID+"/feedback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST feedback with slice-question section: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// TestAPI_PostFeedback_SliceLevel_Returns200 verifies that section='slice-1' is
// accepted and stored correctly.
func TestAPI_PostFeedback_SliceLevel_Returns200(t *testing.T) {
	database, planID := setupPlanTestDB(t)
	handler := planFeedbackSubmitHandler(database)

	body, _ := json.Marshal(planFeedbackRequest{
		Section: "slice-1",
		Action:  "approve",
		Value:   "true",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/plans/"+planID+"/feedback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST feedback with slice-level section: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
}
