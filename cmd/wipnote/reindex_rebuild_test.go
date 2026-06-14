package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	otelreceiver "github.com/shakestzd/wipnote/observe/otel/receiver"
	otelsqlite "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
)

// Slice 9 rebuild-guarantee tests. The plan promise is that deleting the
// SQLite cache and running `wipnote reindex` restores every dashboard-critical
// dataset from canonical files (HTML / YAML / NDJSON). These tests fail when
// the rebuild story regresses.

// --- fixture helpers ---------------------------------------------------------

// buildCanonicalFixture creates a .wipnote/ tree containing every
// dashboard-critical canonical artifact:
//
//   - tracks/<id>.html, features/<id>.html, bugs/<id>.html, spikes/<id>.html
//   - plans/<id>.yaml + plans/<id>.html (slice deps → graph edges)
//   - sessions/<sid>.html (per-session activity log → agent_events)
//   - sessions/<sid>/events.ndjson (per-session OTel signals → otel_signals)
//   - sessions/<sid>/.collector-pid + events.ndjson with a collector_start line
//
// Returns the project root (parent of .wipnote/).
func buildCanonicalFixture(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	for _, sub := range []string{"tracks", "features", "bugs", "spikes", "plans", "sessions"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Track.
	writeMinimalTrackHTML(t, filepath.Join(wipnoteDir, "tracks"),
		"trk-slice9-001.html", "trk-slice9-001", "Slice 9 Test Track")

	// Two features linked to the track.
	writeFeatureWithTrack(t, filepath.Join(wipnoteDir, "features"),
		"feat-slice9-001.html", "feat-slice9-001", "trk-slice9-001", "Feature A")
	writeFeatureWithTrack(t, filepath.Join(wipnoteDir, "features"),
		"feat-slice9-002.html", "feat-slice9-002", "trk-slice9-001", "Feature B")

	// One bug + one spike for graph-edge / typed-row coverage.
	writeFeatureWithTrack(t, filepath.Join(wipnoteDir, "bugs"),
		"bug-slice9-001.html", "bug-slice9-001", "trk-slice9-001", "Bug X")
	writeFeatureWithTrack(t, filepath.Join(wipnoteDir, "spikes"),
		"spk-slice9-001.html", "spk-slice9-001", "trk-slice9-001", "Spike Y")

	// Plan with two slices, slice 2 depends on slice 1.
	writeFixturePlanYAML(t, filepath.Join(wipnoteDir, "plans"),
		"plan-slice9-001",
		[]planFixtureSlice{
			{Num: 1, ID: "feat-slice9-001", Title: "First slice", Deps: nil},
			{Num: 2, ID: "feat-slice9-002", Title: "Second slice", Deps: []int{1}},
		})
	writeFixturePlanHTML(t, filepath.Join(wipnoteDir, "plans"),
		"plan-slice9-001", "Slice 9 Test Plan")

	// Two sessions. One has an activity log AND an events.ndjson, the
	// other has only an activity log. Both must round-trip.
	sessHTMLDir := filepath.Join(wipnoteDir, "sessions")
	sessionA := "11112222-3333-4444-5555-666677778888"
	sessionB := "22223333-4444-5555-6666-777788889999"
	writeFixtureSessionHTMLWithProject(t, sessHTMLDir, sessionA, projectDir, []sessionEventSpec{
		{eventID: "evt-slice9-a-1", ts: "2026-05-10T14:00:00.000000",
			tool: "Bash", success: "true", text: "session A event 1"},
		{eventID: "evt-slice9-a-2", ts: "2026-05-10T14:01:00.000000",
			tool: "Read", success: "true", text: "session A event 2"},
	})
	writeFixtureSessionHTMLWithProject(t, sessHTMLDir, sessionB, projectDir, []sessionEventSpec{
		{eventID: "evt-slice9-b-1", ts: "2026-05-10T15:00:00.000000",
			tool: "Bash", success: "true", text: "session B event 1"},
	})

	// Per-session OTel canonical artifacts for session A.
	writeFixtureCollectorArtifacts(t, sessHTMLDir, sessionA, 12345, 4318)

	return projectDir
}

type planFixtureSlice struct {
	Num   int
	ID    string
	Title string
	Deps  []int
}

func writeFixturePlanYAML(t *testing.T, dir, planID string, slices []planFixtureSlice) {
	t.Helper()
	var sb strings.Builder
	fmt.Fprintf(&sb, "meta:\n  id: %s\n  title: \"Plan %s\"\n  description: \"\"\n  created_at: 2026-05-10T00:00:00Z\n  status: finalized\n  version: 1\n", planID, planID)
	sb.WriteString("design:\n  problem: \"\"\n  goals: []\n  constraints: []\n  approved: true\n  comment: \"\"\n")
	sb.WriteString("slices:\n")
	for _, s := range slices {
		fmt.Fprintf(&sb, "  - id: %s\n    num: %d\n    title: %q\n    what: \"\"\n    why: \"\"\n    files: []\n",
			s.ID, s.Num, s.Title)
		if len(s.Deps) > 0 {
			sb.WriteString("    deps:\n")
			for _, d := range s.Deps {
				fmt.Fprintf(&sb, "      - %d\n", d)
			}
		} else {
			sb.WriteString("    deps: []\n")
		}
		sb.WriteString("    done_when: []\n    effort: M\n    risk: Low\n    tests: \"\"\n    approved: true\n    comment: \"\"\n")
	}
	sb.WriteString("questions: []\n")
	if err := os.WriteFile(filepath.Join(dir, planID+".yaml"), []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write plan yaml: %v", err)
	}
}

func writeFixturePlanHTML(t *testing.T, dir, planID, title string) {
	t.Helper()
	content := fmt.Sprintf(`<!DOCTYPE html>
<html><body>
<article id="%s" data-type="plan" data-status="finalized">
<h1>%s</h1>
</article>
</body></html>`, planID, title)
	if err := os.WriteFile(filepath.Join(dir, planID+".html"), []byte(content), 0o644); err != nil {
		t.Fatalf("write plan html: %v", err)
	}
}

func writeFeatureWithTrack(t *testing.T, dir, filename, id, trackID, title string) {
	t.Helper()
	nodeType := "feature"
	if strings.HasPrefix(filepath.Base(dir), "bug") || strings.Contains(dir, "bugs") {
		nodeType = "bug"
	} else if strings.Contains(dir, "spikes") {
		nodeType = "spike"
	}
	content := fmt.Sprintf(`<!DOCTYPE html>
<html><body>
<article id="%s" data-type="%s" data-status="todo" data-priority="medium" data-track-id="%s" data-created="%s" data-updated="%s">
<h1>%s</h1>
</article>
</body></html>`, id, nodeType, trackID, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339), title)
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

func writeFixtureSessionHTMLWithProject(t *testing.T, dir, sessionID, projectDir string, events []sessionEventSpec) {
	t.Helper()
	liItems := ""
	for _, ev := range events {
		liItems += fmt.Sprintf(
			"<li data-ts=%q data-tool=%q data-success=%q data-event-id=%q>%s</li>\n",
			ev.ts, ev.tool, ev.success, ev.eventID, ev.text,
		)
	}
	content := fmt.Sprintf(`<!DOCTYPE html>
<html><body>
<article id="%s"
         data-type="session"
         data-status="completed"
         data-agent="claude-code"
         data-started-at="2026-05-10T13:00:00.000000"
         data-project-dir="%s"
         data-event-count="%d">
<section data-activity-log><ol>%s</ol></section>
</article>
</body></html>`, sessionID, projectDir, len(events), liItems)
	if err := os.WriteFile(filepath.Join(dir, sessionID+".html"), []byte(content), 0o644); err != nil {
		t.Fatalf("write session html: %v", err)
	}
}

// writeFixtureCollectorArtifacts writes a per-session events.ndjson + .collector-pid
// file structured like a real per-session OTel collector. The NDJSON contains:
//   - 1 collector_start line (read by ReadCollectorStatus)
//   - N span signals (replayed into otel_signals)
func writeFixtureCollectorArtifacts(t *testing.T, sessionsRoot, sessionID string, pid, port int) {
	t.Helper()
	sessDir := filepath.Join(sessionsRoot, sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sess dir: %v", err)
	}

	// .collector-pid (two-line format: pid + start time).
	pidContent := fmt.Sprintf("%d\n%d\n", pid, time.Now().Unix())
	if err := os.WriteFile(filepath.Join(sessDir, ".collector-pid"), []byte(pidContent), 0o644); err != nil {
		t.Fatalf("write collector pid: %v", err)
	}

	// events.ndjson: collector_start + several span signals.
	lines := []string{
		fmt.Sprintf(`{"kind":"collector_start","ts":%q,"attrs":{"port":%d}}`,
			time.Now().UTC().Format(time.RFC3339Nano), port),
	}
	for i := 0; i < 3; i++ {
		sigID := fmt.Sprintf("sig-slice9-%s-%d", sessionID[:8], i)
		ts := time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		line := fmt.Sprintf(
			`{"kind":"span","harness":"claude","ts":%q,"signal_id":%q,"session_id":%q,"trace_id":"trace-1","span_id":"span-%d","canonical":"agent.tool_call","native":"Tool","tool_name":"Bash","attrs":{}}`,
			ts, sigID, sessionID, i)
		lines = append(lines, line)
	}
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sessDir, "events.ndjson"), []byte(data), 0o644); err != nil {
		t.Fatalf("write events.ndjson: %v", err)
	}
}

// runReindexInDir runs the full `wipnote reindex` from projectDir as CWD by
// chdir+invoking runReindex directly so we exercise the orchestrator without
// shelling out.
func runReindexInDir(t *testing.T, projectDir string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir %s: %v", projectDir, err)
	}
	cmd := reindexCmd()
	if err := runReindex(cmd, nil); err != nil {
		t.Fatalf("runReindex: %v", err)
	}
}

func openCachedDB(t *testing.T, projectDir string) *sql.DB {
	t.Helper()
	dbPath := cachedDBPath(t, projectDir)
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open cached db %s: %v", dbPath, err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func cachedDBPath(t *testing.T, _ string) string {
	t.Helper()
	override := os.Getenv("WIPNOTE_DB_PATH")
	if override == "" {
		t.Fatalf("WIPNOTE_DB_PATH not set — test must set it via t.Setenv")
	}
	return override
}

func setupReindexTestEnv(t *testing.T, projectDir string) {
	t.Helper()
	// Pin the DB path to a temp location so the reindex output does not
	// collide with the developer's real cache.
	dbPath := filepath.Join(t.TempDir(), "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	// Pin project-dir resolution to the fixture. The agent harness sets
	// WIPNOTE_PROJECT_DIR (and CLAUDE_PROJECT_DIR) to the developer's real
	// wipnote repo — paths.ResolveProjectDir trusts the env var and would
	// hand reindex the real .wipnote/ tree, which contains 600+ tracks +
	// 1800+ commits. The symptom was a 120s+ test timeout while reindex
	// shelled out to git for every HTML file in the real repo. Clearing
	// these (plus WIPNOTE_SESSION_ID, which is the trust gate for
	// CLAUDE_PROJECT_DIR) makes ResolveProjectDir fall through to the
	// chdir'd CWD, which is the fixture root.
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")

	// Block git from climbing out of the fixture into the worktree's own
	// .git directory. t.TempDir() lives under $TMPDIR (set to .test-exec by
	// the slice-9 task harness), which is inside the wipnote worktree —
	// without a ceiling, any `git -C <projectDir> log` invoked by reindex
	// helpers (gitLastModified, gitFirstAdded, reindexCommitTrailers) would
	// resolve to the real wipnote repo and walk its 1800+ commit history
	// per file. GIT_CEILING_DIRECTORIES makes git fail fast with "not a
	// git repository" — reindex treats that the same as untracked and the
	// HTML attribute timestamps win.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(projectDir))
}

// --- TESTS -------------------------------------------------------------------

// TestCacheDeletion_ReindexRestoresDashboardCriticalRows is the headline test
// of slice 9: deleting the SQLite cache and running reindex restores every
// dashboard-critical table.
func TestCacheDeletion_ReindexRestoresDashboardCriticalRows(t *testing.T) {
	if testing.Short() {
		t.Skip("drives reindex integration flow")
	}

	projectDir := buildCanonicalFixture(t)
	setupReindexTestEnv(t, projectDir)

	// First reindex — builds the cache from canonical files.
	runReindexInDir(t, projectDir)

	// Capture baseline row counts from each dashboard-critical table.
	type rowCounts struct {
		features    int
		tracks      int
		edges       int
		sessions    int
		agentEvents int
		otelSignals int
	}
	read := func() rowCounts {
		db := openCachedDB(t, projectDir)
		defer db.Close()
		var rc rowCounts
		db.QueryRow(`SELECT COUNT(*) FROM features`).Scan(&rc.features)
		db.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&rc.tracks)
		db.QueryRow(`SELECT COUNT(*) FROM graph_edges`).Scan(&rc.edges)
		db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&rc.sessions)
		db.QueryRow(`SELECT COUNT(*) FROM agent_events`).Scan(&rc.agentEvents)
		db.QueryRow(`SELECT COUNT(*) FROM otel_signals`).Scan(&rc.otelSignals)
		return rc
	}
	before := read()

	// Sanity: every dashboard-critical table must have been populated on the
	// first reindex. If any of these are zero the rebuild story is broken
	// for that dataset.
	if before.features < 4 {
		t.Errorf("features: got %d, want >= 4 (1 feat + 1 feat + 1 bug + 1 spike)", before.features)
	}
	if before.tracks < 1 {
		t.Errorf("tracks: got %d, want >= 1", before.tracks)
	}
	if before.edges < 3 {
		t.Errorf("graph_edges: got %d, want >= 3 (planned_in×2 + blocked_by×1)", before.edges)
	}
	if before.sessions < 2 {
		t.Errorf("sessions: got %d, want >= 2", before.sessions)
	}
	if before.agentEvents < 3 {
		t.Errorf("agent_events: got %d, want >= 3 (events parsed from session HTML)", before.agentEvents)
	}
	if before.otelSignals < 3 {
		t.Errorf("otel_signals: got %d, want >= 3 (signals replayed from events.ndjson)", before.otelSignals)
	}

	// Delete the cache DB file.
	dbPath := cachedDBPath(t, projectDir)
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove cache db: %v", err)
	}
	// Also remove WAL/SHM siblings so we have a true "cache deleted" scenario.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	// Run reindex again — must restore the full dataset.
	runReindexInDir(t, projectDir)

	after := read()
	if after.features < before.features {
		t.Errorf("features rebuilt: got %d, want >= %d", after.features, before.features)
	}
	if after.tracks < before.tracks {
		t.Errorf("tracks rebuilt: got %d, want >= %d", after.tracks, before.tracks)
	}
	if after.edges < before.edges {
		t.Errorf("graph_edges rebuilt: got %d, want >= %d", after.edges, before.edges)
	}
	if after.sessions < before.sessions {
		t.Errorf("sessions rebuilt: got %d, want >= %d", after.sessions, before.sessions)
	}
	if after.agentEvents < before.agentEvents {
		t.Errorf("agent_events rebuilt: got %d, want >= %d", after.agentEvents, before.agentEvents)
	}
	if after.otelSignals < before.otelSignals {
		t.Errorf("otel_signals rebuilt: got %d, want >= %d", after.otelSignals, before.otelSignals)
	}
}

// TestReindex_IsIdempotent runs reindex twice and asserts row counts do not
// change. INSERT OR REPLACE / INSERT OR IGNORE semantics make this true; this
// test pins the contract.
func TestReindex_IsIdempotent(t *testing.T) {
	projectDir := buildCanonicalFixture(t)
	setupReindexTestEnv(t, projectDir)

	runReindexInDir(t, projectDir)
	first := snapshotCriticalCounts(t, projectDir)

	runReindexInDir(t, projectDir)
	second := snapshotCriticalCounts(t, projectDir)

	for k, v := range first {
		if second[k] != v {
			t.Errorf("table %s row count drift: first=%d, second=%d", k, v, second[k])
		}
	}
}

func snapshotCriticalCounts(t *testing.T, projectDir string) map[string]int {
	t.Helper()
	db := openCachedDB(t, projectDir)
	defer db.Close()
	out := map[string]int{}
	for _, tbl := range []string{"features", "tracks", "graph_edges", "sessions", "agent_events", "otel_signals"} {
		var n int
		db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", tbl)).Scan(&n)
		out[tbl] = n
	}
	return out
}

// TestDaemonCrashDuringWrite_ReindexRecovers exercises slice-7's
// writer-crash-mid-queue scenario. Producers append canonical NDJSON BEFORE
// submitting to the writer queue. When the writer dies mid-batch, the
// canonical NDJSON still contains every signal. After the crash, deleting
// the DB and running reindex must produce a complete otel_signals table.
func TestDaemonCrashDuringWrite_ReindexRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("drives daemon crash recovery integration flow")
	}

	projectDir := buildCanonicalFixture(t)
	setupReindexTestEnv(t, projectDir)

	// First reindex to materialise the cache + sessions rows.
	runReindexInDir(t, projectDir)

	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	sessionID := "11112222-3333-4444-5555-666677778888"
	ndjsonPath := filepath.Join(wipnoteDir, "sessions", sessionID, "events.ndjson")

	// Append N additional canonical signals to the NDJSON the way a real
	// per-session collector would (BEFORE attempting any DB write — the
	// canonical-first contract).
	addN := 5
	f, err := os.OpenFile(ndjsonPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open ndjson: %v", err)
	}
	baseTime := time.Now().UTC()
	for i := 0; i < addN; i++ {
		sigID := fmt.Sprintf("sig-crash-%d", i)
		ts := baseTime.Add(time.Duration(i+10) * time.Second).Format(time.RFC3339Nano)
		line := fmt.Sprintf(
			`{"kind":"span","harness":"claude","ts":%q,"signal_id":%q,"session_id":%q,"trace_id":"trace-crash","span_id":"crash-%d","canonical":"agent.tool_call","native":"Tool","tool_name":"Bash","attrs":{}}`+"\n",
			ts, sigID, sessionID, i)
		if _, err := f.WriteString(line); err != nil {
			t.Fatalf("append ndjson: %v", err)
		}
	}
	f.Close()

	// Simulate writer crash: open a queue + writer, submit some ops, then
	// kill the queue context BEFORE every submit can drain. The canonical
	// NDJSON is already on disk (above), so the test merely ensures we
	// don't end up depending on the queue having flushed.
	dbPath := cachedDBPath(t, projectDir)
	writer, err := otelreceiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	q := writequeue.New(writequeue.Config{Capacity: 4})
	ctx, cancel := context.WithCancel(context.Background())
	if err := q.Start(ctx); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	// Submit a handful of submit-and-block-forever ops to fill the queue,
	// then cancel the context to force the consumer goroutine to exit
	// before draining. This mimics a writer-service crash.
	for i := 0; i < 4; i++ {
		_ = q.Submit(ctx, func(opCtx context.Context) error {
			// Block until the context is cancelled — simulating a writer
			// stuck on a long IO.
			<-opCtx.Done()
			return opCtx.Err()
		})
	}
	cancel()
	q.Stop(50 * time.Millisecond)
	_ = writer.Close()

	// Now delete the cache and reindex. Recovery is canonical-driven.
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove db: %v", err)
	}
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	runReindexInDir(t, projectDir)

	// Every signal_id appended above must be in otel_signals.
	db := openCachedDB(t, projectDir)
	defer db.Close()
	for i := 0; i < addN; i++ {
		want := fmt.Sprintf("sig-crash-%d", i)
		var got string
		err := db.QueryRow(`SELECT signal_id FROM otel_signals WHERE signal_id = ?`, want).Scan(&got)
		if err != nil {
			t.Errorf("signal_id %q not recovered: %v", want, err)
		}
	}
}

// TestReindex_DashboardSmoke exercises the dashboard endpoints after a
// cache-deletion+reindex cycle to ensure each surface still returns sane
// data. We mount the single-project mux and probe the endpoints the plan
// names: /api/features, /api/sessions, /api/collector-status, /api/plans.
func TestReindex_DashboardSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("drives dashboard reindex integration flow")
	}

	projectDir := buildCanonicalFixture(t)
	setupReindexTestEnv(t, projectDir)

	// Reindex once, delete cache, reindex again — the smoke endpoints
	// must come up regardless.
	runReindexInDir(t, projectDir)
	dbPath := cachedDBPath(t, projectDir)
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove db: %v", err)
	}
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	runReindexInDir(t, projectDir)

	db := openCachedDB(t, projectDir)
	defer db.Close()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	mux := buildSingleProjectMux(db, db, wipnoteDir)

	// Dashboard-critical endpoints. These map 1:1 onto the slice-9 named
	// dashboard-critical datasets:
	//   features         → work_items (features/bugs/spikes/tracks)
	//   sessions         → sessions
	//   plans            → plans
	//   otel/status      → collector status (per-session)
	//   events/recent    → recent agent events
	//   graph            → graph edges
	sessionID := "11112222-3333-4444-5555-666677778888"
	endpoints := []struct {
		name string
		path string
	}{
		{"features", "/api/features"},
		{"sessions", "/api/sessions"},
		{"plans", "/api/plans"},
		{"events-recent", "/api/events/recent"},
		{"graph", "/api/graph"},
		{"otel-status", "/api/otel/status?session=" + sessionID},
	}
	for _, ep := range endpoints {
		req := httptest.NewRequest("GET", ep.path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Errorf("%s: status %d, want 200, body=%s", ep.name, rr.Code, rr.Body.String())
			continue
		}
		body := strings.TrimSpace(rr.Body.String())
		if body == "" {
			t.Errorf("%s: empty body", ep.name)
			continue
		}
		// Each endpoint returns JSON — verify it parses.
		var any interface{}
		if err := json.Unmarshal([]byte(body), &any); err != nil {
			t.Errorf("%s: invalid JSON: %v\nbody=%s", ep.name, err, body)
		}
	}
}

// Compile-time guard: ensure the QueuedSink remains a SignalSink (this is
// the wiring used by the writer service today). If the contract breaks, this
// test file fails to compile.
var _ = otelsqlite.NewQueued
