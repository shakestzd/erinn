package main

// bug-74a7bda7 (roborev HIGH follow-up): under the read-only dashboard mux
// topology the child server passes a read-only handle for read routes and a
// SEPARATE writable handle for mutating routes. roborev found that mutating
// HTTP endpoints (plan feedback/finalize/delete/chat, manual session ingest)
// were still wired to the read-only handle and would fail SQLITE_READONLY.
//
// These tests reproduce the exact production wiring (read-only `database` +
// writable `writeDB`) and assert:
//
//   - POST /api/plans/{id}/feedback succeeds (writes land via writeDB), with
//     NO "attempt to write a readonly database" / SQLITE_READONLY error, and
//   - a read endpoint (GET /api/plans/{id}/feedback) still works on the
//     read-only handle and observes the write.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/db"

	_ "modernc.org/sqlite"
)

// newSplitHandleDBPath builds a schema'd temp-file DB (via the writable
// schema-creating Open, exactly like runServeChild) and returns its path so
// callers can open the production handle pair (read-only mux + dedicated
// writable) against the same file.
func newSplitHandleDBPath(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "split-handle.db")
	w, err := db.Open(dbPath) // create schema + migrations
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	w.Close()
	return dbPath
}

// TestPlanFeedbackPOST_WritesViaWritableHandle_UnderReadOnlyMux drives
// planRouter with the production split-handle wiring and asserts the POST
// feedback path commits through the writable handle while the read-only mux
// handle rejects direct writes.
func TestPlanFeedbackPOST_WritesViaWritableHandle_UnderReadOnlyMux(t *testing.T) {
	dbPath := newSplitHandleDBPath(t)

	roDB, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer roDB.Close()
	writeDB, err := db.OpenWritable(dbPath)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer writeDB.Close()

	// Sanity: the read-only mux handle must reject writes.
	if _, err := roDB.Exec(
		`INSERT INTO plan_feedback (plan_id, section, action, value, question_id)
		 VALUES ('p','design','approve','true','')`,
	); err == nil {
		t.Fatal("read-only handle accepted an INSERT; want SQLITE_READONLY")
	}

	const planID = "plan-ro-topology"
	wipnoteDir := writeTempPlanHTML(t, planID)
	// Production wiring: read routes on roDB, mutating routes on writeDB.
	router := planRouter(roDB, writeDB, wipnoteDir)

	// POST /feedback — mutating route, must succeed via writeDB.
	reqBody, _ := json.Marshal(planFeedbackRequest{
		Section: "design", Action: "approve", Value: "true",
	})
	postReq := httptest.NewRequest(http.MethodPost,
		"/api/plans/"+planID+"/feedback", bytes.NewReader(reqBody))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	router(postRec, postReq)

	if postRec.Code != http.StatusOK {
		t.Fatalf("POST /feedback: got %d, want 200; body: %s",
			postRec.Code, postRec.Body.String())
	}
	if b := postRec.Body.String(); strings.Contains(b, "readonly") ||
		strings.Contains(b, "SQLITE_READONLY") || strings.Contains(b, "read-only") {
		t.Fatalf("POST /feedback leaked a read-only error: %s", b)
	}

	// The write must actually be visible through the canonical YAML feedback.
	entries, err := readPlanFeedbackEntries(wipnoteDir, planID)
	if err != nil {
		t.Fatalf("read feedback: %v", err)
	}
	if len(entries) != 1 || entries[0].Section != "design" {
		t.Fatalf("feedback not persisted via writable handle: %+v", entries)
	}

	// GET /feedback — read route on the read-only handle still works.
	getReq := httptest.NewRequest(http.MethodGet,
		"/api/plans/"+planID+"/feedback", nil)
	getRec := httptest.NewRecorder()
	router(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /feedback on read-only handle: got %d, want 200; body: %s",
			getRec.Code, getRec.Body.String())
	}
}

// seedDiscoverableTranscript writes a real Claude Code JSONL transcript into
// a fake $HOME/.claude/projects/<encoded>/<sessionID>.jsonl so that
// ingest.DiscoverSessions("") (used by ingestSession) finds it and the
// handler proceeds PAST the not_found early-return into its actual
// ensureSession/storeParseResult/UpdateTranscriptSync writes. The JSONL
// shape mirrors internal/ingest/parser_test.go (a user prompt + an
// assistant reply with a tool_use), which ParseFile turns into >=1 message
// so ingestSession does NOT take the "len(result.Messages)==0" early
// return either. Returns the session id.
func seedDiscoverableTranscript(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionID := "sess-ro-topology-ingest-1"
	// Any project dir name works (ingestSession passes projectFilter="");
	// the encoded form just needs to be a directory under projects/.
	projDir := filepath.Join(home, ".claude", "projects", "-tmp-ro-topology")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}
	lines := []string{
		`{"type":"user","uuid":"u1","parentUuid":null,"message":{"role":"user","content":"please fix the dashboard feed"},"timestamp":"2026-05-01T20:00:00.000Z","sessionId":"` + sessionID + `"}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"On it."},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/mock/feed.go"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}},"timestamp":"2026-05-01T20:00:01.000Z","sessionId":"` + sessionID + `"}`,
	}
	jsonl := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(projDir, sessionID+".jsonl"),
		[]byte(jsonl), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return sessionID
}

// TestSessionIngestPOST_WritesViaWritableHandle_UnderReadOnlyMux drives the
// manual-ingest mutating route with the production split-handle wiring
// (read-only mux handle + dedicated writable handle) against a SEEDED,
// discoverable transcript so ingestSession actually performs its
// ensureSession/storeParseResult/UpdateTranscriptSync writes (it does NOT
// take the not_found or empty-messages early returns).
//
// Regression-proof: the test asserts the session + message rows are visible
// when queried through the READ-ONLY handle. That can only be true if the
// write went through `writeDB` against the same DB file. If the handler had
// (incorrectly) been wired to use the read-only handle for the write, the
// write would fail SQLITE_READONLY, no rows would exist, and the row-count
// assertions below would fail. A guard sub-assertion additionally proves
// the test has teeth by showing the same ingest fails to persist when the
// write handle is itself read-only.
func TestSessionIngestPOST_WritesViaWritableHandle_UnderReadOnlyMux(t *testing.T) {
	sessionID := seedDiscoverableTranscript(t)
	dbPath := newSplitHandleDBPath(t)

	roDB, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer roDB.Close()
	writeDB, err := db.OpenWritable(dbPath)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer writeDB.Close()

	// Production wiring: read-only mux handle first, writable second.
	handler := sessionIngestHandler(roDB, writeDB)
	req := httptest.NewRequest(http.MethodPost,
		"/api/sessions/"+sessionID+"/ingest", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /ingest: got %d, want 200; body: %s",
			rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "readonly") ||
		strings.Contains(body, "SQLITE_READONLY") ||
		strings.Contains(body, "read-only") {
		t.Fatalf("ingest leaked a read-only write error: %s", body)
	}
	if strings.Contains(body, `"status":"not_found"`) {
		t.Fatalf("ingest hit not_found — transcript was not discovered, "+
			"the write path was never exercised: %s", body)
	}

	// Regression-proof assertion: the rows must be visible via the
	// READ-ONLY handle, which is only possible if the write committed
	// through writeDB to the same file.
	var sessRows int
	if err := roDB.QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&sessRows); err != nil {
		t.Fatalf("count sessions via read-only handle: %v", err)
	}
	if sessRows != 1 {
		t.Fatalf("sessions row count via read-only handle = %d, want 1 "+
			"(ingest write did not reach the shared DB file via writeDB)",
			sessRows)
	}
	var msgRows int
	if err := roDB.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID,
	).Scan(&msgRows); err != nil {
		t.Fatalf("count messages via read-only handle: %v", err)
	}
	if msgRows < 1 {
		t.Fatalf("messages row count via read-only handle = %d, want >=1 "+
			"(storeParseResult write did not reach the DB via writeDB)",
			msgRows)
	}

	// Teeth check: the same ingest, but with a read-only handle as the
	// WRITE handle, must NOT persist new rows on a fresh DB — proving the
	// assertions above would fail if production used the read-only handle
	// for writes.
	badPath := newSplitHandleDBPath(t)
	badRO, err := db.OpenReadOnly(badPath)
	if err != nil {
		t.Fatalf("OpenReadOnly (teeth): %v", err)
	}
	defer badRO.Close()
	badHandler := sessionIngestHandler(badRO, badRO) // write handle is read-only
	badReq := httptest.NewRequest(http.MethodPost,
		"/api/sessions/"+sessionID+"/ingest", bytes.NewBufferString(`{}`))
	badRec := httptest.NewRecorder()
	badHandler.ServeHTTP(badRec, badReq)

	verifyRO, err := db.OpenReadOnly(badPath)
	if err != nil {
		t.Fatalf("OpenReadOnly (teeth verify): %v", err)
	}
	defer verifyRO.Close()
	var badSessRows int
	if err := verifyRO.QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&badSessRows); err != nil {
		t.Fatalf("count sessions (teeth verify): %v", err)
	}
	if badSessRows != 0 {
		t.Fatalf("teeth check failed: a read-only write handle persisted "+
			"%d session row(s); the main assertion would not catch a "+
			"read-only-handle regression", badSessRows)
	}
}
