package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/shakestzd/wipnote/core/agent"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/observe/otel/sink/ndjson"
)

// mhKV builds a string KeyValue proto for OTLP payloads.
func mhKV(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key:   k,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}},
	}
}

// mhResource builds a Resource proto with the given attributes.
func mhResource(attrs ...*commonpb.KeyValue) *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: attrs}
}

// postProto serialises msg and POSTs it to url with application/x-protobuf.
func postProto(t *testing.T, url string, msg proto.Message) *http.Response {
	t.Helper()
	body, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	resp.Body.Close()
	return resp
}

// TestMultiHarnessIngestion verifies that the per-session collector mux
// correctly routes payloads from all three harnesses (Codex, Gemini, Claude)
// through the adapter registry and into the ndjson sink.
func TestMultiHarnessIngestion(t *testing.T) {
	// --- Set up the project dir and ndjson sink ---
	projectDir := t.TempDir()
	sessionID := "mh-test-session"
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll session dir: %v", err)
	}

	snk, err := ndjson.New(projectDir, sessionID)
	if err != nil {
		t.Fatalf("ndjson.New: %v", err)
	}

	lastActivity := &atomic.Int64{}
	lastActivity.Store(time.Now().UnixMilli())

	mux := buildCollectorMux(snk, lastActivity)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	now := time.Now().UnixNano()

	// --- POST 1: Codex logs payload ---
	codexLogs := &logspb.LogsData{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: mhResource(
				mhKV("service.name", "codex-cli"),
				mhKV("service.version", "0.1.0"),
			),
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope: &commonpb.InstrumentationScope{Name: "codex"},
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(now),
					Attributes: []*commonpb.KeyValue{
						mhKV("event.name", "codex.user_prompt"),
						mhKV("conversation.id", "codex-test-123"),
					},
				}},
			}},
		}},
	}
	resp := postProto(t, srv.URL+"/v1/logs", codexLogs)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Codex /v1/logs: status=%d, want 200", resp.StatusCode)
	}

	// --- POST 2: Gemini metrics payload ---
	geminiMetrics := &metricspb.MetricsData{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: mhResource(
				mhKV("service.name", "gemini-cli"),
				mhKV("service.version", "0.1.0"),
			),
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope: &commonpb.InstrumentationScope{Name: "gemini_cli"},
				Metrics: []*metricspb.Metric{{
					Name: "gemini_cli.session.count",
					Data: &metricspb.Metric_Sum{
						Sum: &metricspb.Sum{
							DataPoints: []*metricspb.NumberDataPoint{{
								TimeUnixNano: uint64(now),
								Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 1},
								Attributes: []*commonpb.KeyValue{
									mhKV("session.id", "gemini-test-456"),
								},
							}},
						},
					},
				}},
			}},
		}},
	}
	resp = postProto(t, srv.URL+"/v1/metrics", geminiMetrics)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Gemini /v1/metrics: status=%d, want 200", resp.StatusCode)
	}

	// --- POST 3: Claude traces payload ---
	claudeTraces := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: mhResource(
				mhKV("service.name", "claude-code"),
				mhKV("service.version", "2.1.42"),
			),
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code"},
				Spans: []*tracepb.Span{{
					TraceId:           bytes.Repeat([]byte{0xab}, 16),
					SpanId:            bytes.Repeat([]byte{0xcd}, 8),
					Name:              "claude_code.interaction",
					StartTimeUnixNano: uint64(now),
					EndTimeUnixNano:   uint64(now + 1_000_000_000),
					Attributes: []*commonpb.KeyValue{
						mhKV("session.id", "claude-test-789"),
					},
				}},
			}},
		}},
	}
	resp = postProto(t, srv.URL+"/v1/traces", claudeTraces)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Claude /v1/traces: status=%d, want 200", resp.StatusCode)
	}

	// Flush the sink to ensure buffered writes are on disk before reading.
	// The new buffered ndjson.Sink only auto-flushes after FlushThreshold (64)
	// events or the periodic SyncInterval (2s) — an explicit Flush is required
	// in tests that send fewer than FlushThreshold signals.
	if err := snk.Flush(); err != nil {
		t.Fatalf("flush ndjson sink: %v", err)
	}

	// --- Read the ndjson output and assert ---
	eventsPath := filepath.Join(sessDir, "events.ndjson")
	f, err := os.Open(eventsPath)
	if err != nil {
		t.Fatalf("open events.ndjson: %v", err)
	}
	defer f.Close()

	type signalRecord struct {
		Harness   string `json:"harness"`
		SessionID string `json:"session_id"`
		Kind      string `json:"kind"`
	}

	var records []signalRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec signalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Errorf("unmarshal ndjson line: %v — raw: %q", err, line)
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning events.ndjson: %v", err)
	}

	if len(records) == 0 {
		t.Fatal("events.ndjson is empty — no signals were persisted")
	}

	type want struct {
		harness   string
		sessionID string
	}
	assertions := []want{
		{"codex", "codex-test-123"},
		{"gemini_cli", "gemini-test-456"},
		{"claude_code", "claude-test-789"},
	}

	for _, a := range assertions {
		found := false
		for _, rec := range records {
			if rec.Harness == a.harness && rec.SessionID == a.sessionID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no signal with harness=%q session_id=%q in %d records",
				a.harness, a.sessionID, len(records))
		}
	}
}

// TestParallelHarnessLineage validates the slice-10 end-to-end harness:
// one canonical project, three harnesses (Claude/Codex/Gemini) plus one
// subagent, overlapping work-item claims (slice-5 warn-and-allow), one
// resumed/continued session (slice-4 session-family), lineage chain intact.
//
// Builds ON plan-ae0c37b2 contention fixture by reusing dbpkg.Open (same
// migration path), not duplicating it.
//
// PROFILE: CI-safe. All assertions are in-process (no real harness binaries).
func TestParallelHarnessLineage(t *testing.T) {
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "")

	dbPath := filepath.Join(t.TempDir(), "parallel.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	canonicalProject := "/projects/wipnote"
	featureID := "feat-lineage-test"
	familyID := "fam-parallel-001"
	now := time.Now().UTC()

	roots := []struct {
		sid     string
		harness string
	}{
		{"sess-claude-root", "claude-code"},
		{"sess-codex-root", "codex"},
		{"sess-gemini-root", "gemini"},
	}
	for _, r := range roots {
		sess := &models.Session{
			SessionID:       r.sid,
			AgentAssigned:   r.harness,
			CreatedAt:       now,
			Status:          "active",
			SessionFamilyID: familyID,
			ProjectDir:      canonicalProject,
			ActiveFeatureID: featureID,
		}
		if err := dbpkg.InsertSession(database, sess); err != nil {
			t.Fatalf("InsertSession %s: %v", r.sid, err)
		}
		if err := dbpkg.SetSessionFamilyID(database, r.sid, familyID); err != nil {
			t.Fatalf("SetSessionFamilyID %s: %v", r.sid, err)
		}
	}

	subSessID := "sess-claude-subagent"
	subSess := &models.Session{
		SessionID:       subSessID,
		AgentAssigned:   "claude-code",
		CreatedAt:       now,
		Status:          "active",
		SessionFamilyID: familyID,
		ProjectDir:      canonicalProject,
		ParentSessionID: "sess-claude-root",
		IsSubagent:      true,
	}
	if err := dbpkg.InsertSession(database, subSess); err != nil {
		t.Fatalf("InsertSession subagent: %v", err)
	}

	trace := &models.LineageTrace{
		TraceID:       "trace-sub-001",
		RootSessionID: "sess-claude-root",
		SessionID:     subSessID,
		AgentName:     "patch-coder",
		Depth:         1,
		Path:          []string{"sess-claude-root", subSessID},
		FeatureID:     featureID,
		StartedAt:     now,
		Status:        "active",
	}
	if err := dbpkg.InsertLineageTrace(database, trace); err != nil {
		t.Fatalf("InsertLineageTrace: %v", err)
	}

	// Seed a feature row so claims FK constraint (work_item_id -> features.id) is satisfied.
	_, err = database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES (?, 'feature', 'Lineage Test Feature', 'in-progress')`,
		featureID)
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}

	// Overlapping claims: slice-5 warn-and-allow collision.
	// Use the real claims schema: owner_session_id, lease_expires_at required.
	for _, claimSess := range []string{"sess-claude-root", "sess-codex-root"} {
		expires := now.Add(10 * time.Minute).Format(time.RFC3339)
		_, err := database.Exec(
			`INSERT INTO claims
				(claim_id, work_item_id, owner_session_id, owner_agent,
				 status, leased_at, lease_expires_at, last_heartbeat_at)
			 VALUES (?, ?, ?, 'claude-code', 'in_progress', ?, ?, ?)`,
			"claim-"+claimSess, featureID, claimSess,
			now.Format(time.RFC3339), expires, now.Format(time.RFC3339))
		if err != nil {
			t.Fatalf("insert claim %s: %v", claimSess, err)
		}
	}

	// Assert /api/sessions/parallel grouping (slice-7).
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/parallel", nil)
	w := httptest.NewRecorder()
	parallelSessionsHandler(database)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("parallel sessions: status %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Groups []projectGroup `json:"groups"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode project groups: %v", err)
	}
	var pg *projectGroup
	for i := range resp.Groups {
		if resp.Groups[i].CanonicalProject == canonicalProject {
			pg = &resp.Groups[i]
			break
		}
	}
	if pg == nil {
		t.Fatalf("canonical project %q not in groups", canonicalProject)
	}
	if pg.SessionCount < 3 {
		t.Errorf("project group: SessionCount=%d, want >= 3", pg.SessionCount)
	}
	if !pg.HasCollision {
		t.Error("project group: HasCollision=false, want true (Claude+Codex both claim feat-lineage-test)")
	}

	// Assert lineage chain: subagent traces to root.
	traces, err := dbpkg.GetLineageByRoot(database, "sess-claude-root")
	if err != nil {
		t.Fatalf("GetLineageByRoot: %v", err)
	}
	foundTrace := false
	for _, tr := range traces {
		if tr.SessionID == subSessID && tr.RootSessionID == "sess-claude-root" && tr.Depth == 1 {
			foundTrace = true
			break
		}
	}
	if !foundTrace {
		t.Errorf("lineage: subagent %q not found under claude root", subSessID)
	}

	// Assert resumed session inherits family via the REAL resume path (slice-4).
	//
	// Register the original session's family in the family index (mirrors what the
	// hook handler / launcher does on a fresh start), then resolve a new "resumed"
	// session using resolveSessionFamilyID with isResume=true and the concrete
	// resumeID — this exercises GetClaimIdentity → SessionFamilyFor path added in
	// Batch B (resolveSessionFamilyID rule 2a). The resolved family must match the
	// original session's family.
	resumeProjectDir := t.TempDir() // isolated project dir for family index files
	originalSessID := "sess-claude-root"
	// Register the original session's family in the family index.
	if err := agent.RegisterSessionFamily(resumeProjectDir, originalSessID, familyID); err != nil {
		t.Fatalf("RegisterSessionFamily: %v", err)
	}
	// Now resolve a new session ID as a resume of the original.
	resumedSessID := "sess-claude-resumed"
	resolvedFamilyID := resolveSessionFamilyID(resumeProjectDir, resumedSessID, originalSessID, true /*isResume*/)
	if resolvedFamilyID != familyID {
		t.Errorf("resumed session family: got %q, want %q (resolveSessionFamilyID rule 2a failed)", resolvedFamilyID, familyID)
	}
	// Also verify via MostRecentSessionFamily ("resume last" path, rule 2b).
	resumedLastFamilyID := resolveSessionFamilyID(resumeProjectDir, "sess-claude-resumed-last", "", true /*isResume*/)
	if resumedLastFamilyID != familyID {
		t.Errorf("resumed-last session family: got %q, want %q (resolveSessionFamilyID rule 2b failed)", resumedLastFamilyID, familyID)
	}
}

// TestParallelHarness_MainStaysClean asserts that main stays clean: the
// canonical project root and worktree root appear as SEPARATE canonical
// projects in /api/sessions/parallel so tooling can isolate worktree sessions.
//
// Extends plan-ae0c37b2 contention fixture (same dbpkg.Open path).
//
// PROFILE: CI-safe (in-process, no real git or harness processes).
func TestParallelHarness_MainStaysClean(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mainstays.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	mainRoot := "/projects/wipnote"
	worktreeRoot := "/projects/wipnote/.claude/worktrees/feat-xyz"
	now := time.Now().UTC()

	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     "sess-main-root",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
		ProjectDir:    mainRoot,
	}); err != nil {
		t.Fatalf("InsertSession main: %v", err)
	}
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     "sess-worktree",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
		ProjectDir:    worktreeRoot,
	}); err != nil {
		t.Fatalf("InsertSession worktree: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/parallel", nil)
	w := httptest.NewRecorder()
	parallelSessionsHandler(database)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("parallel handler: status=%d", w.Code)
	}

	var resp2 struct {
		Groups []projectGroup `json:"groups"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}

	foundMain, foundWorktree := false, false
	for _, g := range resp2.Groups {
		switch g.CanonicalProject {
		case mainRoot:
			foundMain = true
		case worktreeRoot:
			foundWorktree = true
		}
	}
	if !foundMain {
		t.Errorf("main root %q not in project groups", mainRoot)
	}
	if !foundWorktree {
		t.Errorf("worktree root %q not separate project group (main isolation broken)", worktreeRoot)
	}
}
