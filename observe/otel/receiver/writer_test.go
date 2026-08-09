package receiver_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/receiver"
)

// newWriter opens a fresh SQLite DB with the OTel schema and returns
// both a writer and a reader handle. The reader is a second *sql.DB
// for assertions (we can't query through the writer's MaxOpenConns=1
// while a transaction is open in a concurrent test).
func newWriter(t *testing.T) (*receiver.Writer, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "otel.db")
	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	readDB.Close()
	w, err := receiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w, dbPath
}

func sigFixture(session, prompt string, overrides ...func(*otel.UnifiedSignal)) otel.UnifiedSignal {
	s := otel.UnifiedSignal{
		Harness:       otel.HarnessClaude,
		SignalID:      "sig-" + session + "-" + prompt,
		Kind:          otel.KindLog,
		CanonicalName: otel.CanonicalAPIRequest,
		NativeName:    "api_request",
		Timestamp:     time.Unix(0, 1735000000000000000),
		SessionID:     session,
		PromptID:      prompt,
		Model:         "claude-haiku-4-5-20251001",
		Tokens: otel.TokenCounts{
			Input: 10, Output: 577, CacheRead: 23276, CacheCreation: 2261,
		},
		CostUSD:    0.00804885,
		CostSource: otel.CostSourceVendor,
		DurationMs: 5835,
		RawAttrs:   map[string]any{"request_id": "req_011"},
	}
	for _, fn := range overrides {
		fn(&s)
	}
	return s
}

func TestWriter_InsertsSignalAndPlaceholderSession(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()

	res := map[string]any{
		"service.name":    "claude-code",
		"service.version": "2.1.42",
		"terminal.type":   "iTerm.app",
	}
	inserted, err := w.WriteBatch(ctx, otel.HarnessClaude, res,
		[]otel.UnifiedSignal{sigFixture("sess-A", "prompt-1")})
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if inserted != 1 {
		t.Errorf("inserted = %d, want 1", inserted)
	}

	// Session placeholder created by the writer.
	var agent, status string
	if err := w.DB().QueryRow(
		"SELECT agent_assigned, status FROM sessions WHERE session_id='sess-A'",
	).Scan(&agent, &status); err != nil {
		t.Fatalf("lookup session placeholder: %v", err)
	}
	if agent != "claude_code" || status != "active" {
		t.Errorf("placeholder session = (%q, %q)", agent, status)
	}

	// Resource attributes recorded.
	var val string
	if err := w.DB().QueryRow(
		"SELECT value FROM otel_resource_attrs WHERE session_id='sess-A' AND key='terminal.type'",
	).Scan(&val); err != nil {
		t.Fatalf("resource attr lookup: %v", err)
	}
	if val != "iTerm.app" {
		t.Errorf("terminal.type = %q", val)
	}

	// Signal row has the token + cost data preserved.
	var tokensIn, tokensOut int64
	var cost float64
	if err := w.DB().QueryRow(
		"SELECT tokens_in, tokens_out, cost_usd FROM otel_signals WHERE signal_id='sig-sess-A-prompt-1'",
	).Scan(&tokensIn, &tokensOut, &cost); err != nil {
		t.Fatalf("signal lookup: %v", err)
	}
	if tokensIn != 10 || tokensOut != 577 {
		t.Errorf("tokens = (%d, %d)", tokensIn, tokensOut)
	}
	if cost != 0.00804885 {
		t.Errorf("cost = %v", cost)
	}
}

func TestWriter_IdempotentOnDuplicateSignalID(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()
	sig := sigFixture("sess-B", "prompt-1")
	batch := []otel.UnifiedSignal{sig}

	n1, _ := w.WriteBatch(ctx, otel.HarnessClaude, map[string]any{"service.name": "claude-code"}, batch)
	n2, _ := w.WriteBatch(ctx, otel.HarnessClaude, map[string]any{"service.name": "claude-code"}, batch)
	if n1 != 1 || n2 != 0 {
		t.Errorf("insert counts (%d, %d), want (1, 0)", n1, n2)
	}

	var count int
	if err := w.DB().QueryRow(
		"SELECT COUNT(*) FROM otel_signals WHERE session_id='sess-B'").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("duplicate-insert produced %d rows, want 1", count)
	}
}

func TestWriter_DropsUnknownMetrics(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()

	unknownMetric := sigFixture("sess-unknown-metric", "p1", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-unknown-metric"
		s.Kind = otel.KindMetric
		s.CanonicalName = otel.CanonicalUnknown
		s.NativeName = "gemini_cli.ui.flicker.count"
	})
	n, err := w.WriteBatch(ctx, otel.HarnessGemini, map[string]any{"service.name": "gemini-cli"}, []otel.UnifiedSignal{unknownMetric})
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if n != 0 {
		t.Fatalf("inserted = %d, want 0", n)
	}

	var count int
	if err := w.DB().QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = 'sess-unknown-metric'`).Scan(&count); err != nil {
		t.Fatalf("count signals: %v", err)
	}
	if count != 0 {
		t.Errorf("unknown metric rows = %d, want 0", count)
	}
}

func TestWriter_PrunesMetricsPerSession(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()

	const total = 5005
	batch := make([]otel.UnifiedSignal, 0, total)
	for i := 0; i < total; i++ {
		i := i
		batch = append(batch, sigFixture("sess-metric-cap", fmt.Sprintf("p-%04d", i), func(s *otel.UnifiedSignal) {
			s.SignalID = fmt.Sprintf("sig-metric-cap-%04d", i)
			s.Kind = otel.KindMetric
			s.CanonicalName = otel.CanonicalTokenUsage
			s.NativeName = "gemini_cli.token.usage"
			s.Timestamp = time.UnixMicro(int64(i + 1))
		}))
	}

	n, err := w.WriteBatch(ctx, otel.HarnessGemini, map[string]any{"service.name": "gemini-cli"}, batch)
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if n != total {
		t.Fatalf("inserted = %d, want %d before pruning", n, total)
	}

	var count int
	if err := w.DB().QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = 'sess-metric-cap' AND kind = 'metric'`).Scan(&count); err != nil {
		t.Fatalf("count metrics: %v", err)
	}
	if count != 5000 {
		t.Fatalf("metric rows = %d, want 5000", count)
	}

	var oldest string
	if err := w.DB().QueryRow(`
		SELECT signal_id
		FROM otel_signals
		WHERE session_id = 'sess-metric-cap' AND kind = 'metric'
		ORDER BY ts_micros ASC
		LIMIT 1`).Scan(&oldest); err != nil {
		t.Fatalf("oldest metric: %v", err)
	}
	if oldest != "sig-metric-cap-0005" {
		t.Errorf("oldest retained metric = %q, want sig-metric-cap-0005", oldest)
	}
}

// seedOverLimitMetrics creates the session (via one real WriteBatch call so
// the sessions FK row exists) then inserts metric rows directly, bypassing
// WriteBatch/pruneMetricSignals entirely, so the session can be pushed
// artificially over metricRowsPerSessionLimit without the writer's own
// pruning intervening during setup.
func seedOverLimitMetrics(t *testing.T, w *receiver.Writer, sessionID string, n int) {
	t.Helper()
	ctx := context.Background()
	if _, err := w.WriteBatch(ctx, otel.HarnessClaude, nil,
		[]otel.UnifiedSignal{sigFixture(sessionID, "seed-log")}); err != nil {
		t.Fatalf("seed session via WriteBatch: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := w.DB().Exec(`
			INSERT INTO otel_signals (signal_id, harness, session_id, kind, canonical, native, ts_micros, attrs_json)
			VALUES (?, 'claude_code', ?, 'metric', 'token_usage', 'claude_code.token.usage', ?, '{}')`,
			fmt.Sprintf("seed-metric-%s-%d", sessionID, i), sessionID, i+1,
		); err != nil {
			t.Fatalf("seed metric row %d: %v", i, err)
		}
	}
}

func countMetricRows(t *testing.T, w *receiver.Writer, sessionID string) int {
	t.Helper()
	var n int
	if err := w.DB().QueryRow(
		`SELECT COUNT(*) FROM otel_signals WHERE session_id = ? AND kind = 'metric'`, sessionID,
	).Scan(&n); err != nil {
		t.Fatalf("count metric rows: %v", err)
	}
	return n
}

// TestWriter_SkipsPruneWhenBatchHasNoMetricSignal is the regression guard for
// bug-e764997c: pruneMetricSignals used to run unconditionally once per
// session per WriteBatch call, regardless of what kind of signal was being
// written. On the real corpus that meant an expensive DELETE+ORDER BY/LIMIT
// query ran on every single non-metric signal too, dominating a full
// reindexOtelEvents run. A batch containing only a log signal for a session
// that is already over the metric cap must leave the over-limit metric rows
// untouched -- nothing metric-shaped changed, so there is nothing to prune.
func TestWriter_SkipsPruneWhenBatchHasNoMetricSignal(t *testing.T) {
	w, _ := newWriter(t)
	const sessionID = "sess-no-metric-in-batch"
	const overLimitBy = 37
	seedOverLimitMetrics(t, w, sessionID, 5000+overLimitBy)

	before := countMetricRows(t, w, sessionID)
	if before != 5000+overLimitBy {
		t.Fatalf("setup: metric rows = %d, want %d", before, 5000+overLimitBy)
	}

	ctx := context.Background()
	if _, err := w.WriteBatch(ctx, otel.HarnessClaude, nil,
		[]otel.UnifiedSignal{sigFixture(sessionID, "log-only")}); err != nil {
		t.Fatalf("WriteBatch (log signal): %v", err)
	}

	after := countMetricRows(t, w, sessionID)
	if after != before {
		t.Errorf("metric rows after log-only batch = %d, want unchanged %d (prune should have been skipped)", after, before)
	}
}

// TestWriter_PrunesWhenBatchHasMetricSignal proves the guard added for
// bug-e764997c doesn't just skip pruning unconditionally: a batch that DOES
// write a metric-kind signal for an over-limit session must still trigger
// pruneMetricSignals and bring the session back down to the cap, exactly as
// before the fix.
func TestWriter_PrunesWhenBatchHasMetricSignal(t *testing.T) {
	w, _ := newWriter(t)
	const sessionID = "sess-metric-in-batch"
	seedOverLimitMetrics(t, w, sessionID, 5000+37)

	ctx := context.Background()
	if _, err := w.WriteBatch(ctx, otel.HarnessClaude, nil,
		[]otel.UnifiedSignal{sigFixture(sessionID, "p-new", func(s *otel.UnifiedSignal) {
			s.SignalID = "sig-" + sessionID + "-new-metric"
			s.Kind = otel.KindMetric
			s.CanonicalName = otel.CanonicalTokenUsage
			s.Timestamp = time.UnixMicro(999999)
		})}); err != nil {
		t.Fatalf("WriteBatch (metric signal): %v", err)
	}

	after := countMetricRows(t, w, sessionID)
	if after != 5000 {
		t.Errorf("metric rows after metric-bearing batch = %d, want 5000 (pruned to cap)", after)
	}
}

func TestWriter_BatchMultipleSessions(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()

	batch := []otel.UnifiedSignal{
		sigFixture("sess-C", "p1"),
		sigFixture("sess-D", "p1"),
		sigFixture("sess-C", "p2"),
	}
	n, err := w.WriteBatch(ctx, otel.HarnessClaude, map[string]any{"service.name": "claude-code"}, batch)
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if n != 3 {
		t.Errorf("inserted = %d, want 3", n)
	}

	// Exactly one placeholder session per distinct ID.
	var c int
	if err := w.DB().QueryRow("SELECT COUNT(*) FROM sessions WHERE session_id IN ('sess-C','sess-D')").Scan(&c); err != nil {
		t.Fatalf("count: %v", err)
	}
	if c != 2 {
		t.Errorf("session count = %d, want 2", c)
	}
}

// TestWriter_ConcurrentBatches verifies the MaxOpenConns=1 invariant
// prevents SQLITE_BUSY under concurrent writers. Two goroutines each
// insert a batch; both must succeed, and the final row count must be
// the sum without loss.
func TestWriter_ConcurrentBatches(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()
	res := map[string]any{"service.name": "claude-code"}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			batch := make([]otel.UnifiedSignal, 20)
			for i := range batch {
				batch[i] = sigFixture(
					"sess-G",
					"p-g1",
					func(s *otel.UnifiedSignal) {
						s.SignalID = "g" + string(rune('0'+g)) + "-" + string(rune('0'+i%10)) + "-" + string(rune('a'+i/10))
					},
				)
			}
			if _, err := w.WriteBatch(ctx, otel.HarnessClaude, res, batch); err != nil {
				errs <- err
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent write failed: %v", e)
	}

	var c int
	if err := w.DB().QueryRow("SELECT COUNT(*) FROM otel_signals WHERE session_id='sess-G'").Scan(&c); err != nil {
		t.Fatalf("count: %v", err)
	}
	if c != 40 {
		t.Errorf("concurrent batches produced %d rows, want 40", c)
	}
}

func TestWriter_DropsSignalWithEmptySessionID(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()
	batch := []otel.UnifiedSignal{
		sigFixture("", "p1"),       // dropped — no session
		sigFixture("sess-F", "p1"), // kept
	}
	n, err := w.WriteBatch(ctx, otel.HarnessClaude, map[string]any{"service.name": "claude-code"}, batch)
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("inserted = %d, want 1 (empty-session dropped)", n)
	}
}

// TestWriter_OrphanSpanCreatePlaceholder verifies that when an incoming span's
// parent_span does not exist in otel_signals, and the resource carries
// wipnote.agent_id matching a pending_subagent_starts row, a placeholder
// subagent_invocation row is synthesised for the parent_span immediately.
func TestWriter_OrphanSpanCreatePlaceholder(t *testing.T) {
	w, dbPath := newWriter(t)
	ctx := context.Background()

	sessionID := "sess-placeholder-test"
	agentID := "agent-orphan-abc"
	parentSpanID := "parent-span-orphan-111"

	// Seed the session row and pending_subagent_starts entry.
	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open readDB: %v", err)
	}
	defer readDB.Close()

	if _, err := readDB.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status) VALUES (?, 'claude-code', 'active')`,
		sessionID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	pending := &db.PendingSubagentStart{
		AgentID:   agentID,
		AgentType: "patch-coder",
		SessionID: sessionID,
		CWD:       "/mock/test",
		CreatedAt: time.Now().UnixMicro(),
	}
	if err := db.UpsertPendingSubagentStart(readDB, pending); err != nil {
		t.Fatalf("UpsertPendingSubagentStart: %v", err)
	}

	// Build an orphan span: parent_span set but no parent row in otel_signals.
	orphanSpan := sigFixture(sessionID, "prompt-orphan", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-orphan-child-1"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalToolExecution
		s.NativeName = "claude_code.tool_execution"
		s.SpanID = "child-span-orphan-222"
		s.ParentSpan = parentSpanID // orphan: parent doesn't exist yet
		s.TraceID = "trace-abc-123"
	})

	resourceAttrs := map[string]any{
		"service.name":     "claude-code",
		"wipnote.agent_id": agentID, // triggers placeholder path
	}
	n, err := w.WriteBatch(ctx, otel.HarnessClaude, resourceAttrs, []otel.UnifiedSignal{orphanSpan})
	if err != nil {
		t.Fatalf("WriteBatch orphan span: %v", err)
	}
	// The child span itself should be inserted (n=1).
	if n != 1 {
		t.Errorf("inserted = %d, want 1 (child span)", n)
	}

	// A placeholder row should now exist for parentSpanID.
	var placeholderCount int
	if err := w.DB().QueryRow(
		`SELECT COUNT(*) FROM otel_signals WHERE span_id = ? AND canonical = 'subagent_invocation'`,
		parentSpanID,
	).Scan(&placeholderCount); err != nil {
		t.Fatalf("count placeholder row: %v", err)
	}
	if placeholderCount != 1 {
		t.Errorf("placeholder row count = %d, want 1 (span_id=%q)", placeholderCount, parentSpanID)
	}

	// Placeholder attrs_json should contain "_pending":true.
	var attrsJSON string
	if err := w.DB().QueryRow(
		`SELECT attrs_json FROM otel_signals WHERE span_id = ? AND canonical = 'subagent_invocation'`,
		parentSpanID,
	).Scan(&attrsJSON); err != nil {
		t.Fatalf("select placeholder attrs_json: %v", err)
	}
	if !strings.Contains(attrsJSON, `"_pending":true`) {
		t.Errorf("placeholder attrs_json missing _pending:true, got: %s", attrsJSON)
	}
}

// TestWriter_RealAgentSpanUpgradesPlaceholder verifies that when the real
// subagent_invocation Agent span arrives after a placeholder was created for
// the same span_id, the placeholder is upgraded (not duplicated) with real data.
func TestWriter_RealAgentSpanUpgradesPlaceholder(t *testing.T) {
	w, dbPath := newWriter(t)
	ctx := context.Background()

	sessionID := "sess-upgrade-test"
	agentID := "agent-upgrade-xyz"
	agentSpanID := "agent-span-real-333" // this will be the placeholder span_id AND real span_id

	// Seed session and pending row.
	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open readDB: %v", err)
	}
	defer readDB.Close()

	if _, err := readDB.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status) VALUES (?, 'claude-code', 'active')`,
		sessionID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	pending := &db.PendingSubagentStart{
		AgentID:   agentID,
		AgentType: "feature-coder",
		SessionID: sessionID,
		CWD:       "/tmp/upgrade-test",
		CreatedAt: time.Now().Add(-30 * time.Second).UnixMicro(), // started 30s ago
	}
	if err := db.UpsertPendingSubagentStart(readDB, pending); err != nil {
		t.Fatalf("UpsertPendingSubagentStart: %v", err)
	}

	resourceAttrs := map[string]any{
		"service.name":     "claude-code",
		"wipnote.agent_id": agentID,
	}

	// Step 1: Send an orphan child span. This triggers placeholder creation for agentSpanID.
	childSpan := sigFixture(sessionID, "prompt-upgrade", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-child-for-upgrade"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalToolExecution
		s.NativeName = "claude_code.tool_execution"
		s.SpanID = "child-span-upgrade-444"
		s.ParentSpan = agentSpanID // orphan parent
		s.TraceID = "trace-upgrade-999"
	})

	if _, err := w.WriteBatch(ctx, otel.HarnessClaude, resourceAttrs, []otel.UnifiedSignal{childSpan}); err != nil {
		t.Fatalf("WriteBatch child span: %v", err)
	}

	// Verify placeholder exists.
	var placeholderCount int
	if err := w.DB().QueryRow(
		`SELECT COUNT(*) FROM otel_signals WHERE span_id = ? AND attrs_json LIKE '%"_pending":true%'`,
		agentSpanID,
	).Scan(&placeholderCount); err != nil {
		t.Fatalf("count placeholder: %v", err)
	}
	if placeholderCount != 1 {
		t.Fatalf("expected 1 placeholder row, got %d", placeholderCount)
	}

	// Step 2: Send the real Agent/subagent_invocation span with the same span_id.
	realAgentSpan := sigFixture(sessionID, "prompt-upgrade", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-real-agent-upgrade"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalSubagent // triggers upgrade path
		s.NativeName = "claude_code.agent_turn"
		s.SpanID = agentSpanID // same span_id as placeholder
		s.ParentSpan = ""      // root agent span has no parent
		s.TraceID = "trace-upgrade-999"
		s.Model = "claude-sonnet-4-6"
		s.Tokens = otel.TokenCounts{Input: 500, Output: 1200}
		s.CostUSD = 0.0123
		s.CostSource = otel.CostSourceVendor
		s.DurationMs = 45000
	})

	n2, err := w.WriteBatch(ctx, otel.HarnessClaude, resourceAttrs, []otel.UnifiedSignal{realAgentSpan})
	if err != nil {
		t.Fatalf("WriteBatch real agent span: %v", err)
	}
	// Upgrade counts as 1 modification.
	if n2 != 1 {
		t.Errorf("upgrade inserted count = %d, want 1", n2)
	}

	// Total row count for this span_id must be exactly 1 (no duplicate).
	var totalRows int
	if err := w.DB().QueryRow(
		`SELECT COUNT(*) FROM otel_signals WHERE span_id = ?`, agentSpanID,
	).Scan(&totalRows); err != nil {
		t.Fatalf("count rows for span_id: %v", err)
	}
	if totalRows != 1 {
		t.Errorf("expected exactly 1 row for span_id=%q, got %d (duplicate!)", agentSpanID, totalRows)
	}

	// Verify the row now has real data (not placeholder values).
	var model string
	var tokensIn, tokensOut int64
	var attrsJSON string
	if err := w.DB().QueryRow(
		`SELECT COALESCE(model,''), COALESCE(tokens_in,0), COALESCE(tokens_out,0), attrs_json
		 FROM otel_signals WHERE span_id = ?`,
		agentSpanID,
	).Scan(&model, &tokensIn, &tokensOut, &attrsJSON); err != nil {
		t.Fatalf("select upgraded row: %v", err)
	}
	if model != "claude-sonnet-4-6" {
		t.Errorf("model after upgrade = %q, want %q", model, "claude-sonnet-4-6")
	}
	if tokensIn != 500 || tokensOut != 1200 {
		t.Errorf("tokens after upgrade = (%d, %d), want (500, 1200)", tokensIn, tokensOut)
	}
	// After upgrade, attrs_json should NOT contain _pending:true.
	if strings.Contains(attrsJSON, `"_pending":true`) {
		t.Errorf("upgraded row still contains _pending:true in attrs_json: %s", attrsJSON)
	}
}

// TestWriter_ReattributesByAgentIDResourceAttr verifies Strategy A re-attribution:
// a child span arriving with wipnote.agent_id and a wrong parent_span (pointing
// at the interaction) gets re-parented to the correct Agent span_id.
func TestWriter_ReattributesByAgentIDResourceAttr(t *testing.T) {
	w, dbPath := newWriter(t)
	ctx := context.Background()

	sessionID := "sess-reattrib-a"
	agentID := "agent-reattrib-aaa"
	agentSpanID := "agent-span-reattrib-aaa-111"
	interactionSpanID := "interaction-span-reattrib-aaa-000"
	traceID := "trace-reattrib-aaa"

	// Seed session.
	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open readDB: %v", err)
	}
	defer readDB.Close()
	if _, err := readDB.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status) VALUES (?, 'claude-code', 'active')`,
		sessionID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Seed pending_subagent_starts WITH agent_span_id already set (simulates
	// that the placeholder was already created by a prior span batch).
	if _, err := readDB.Exec(`
		INSERT OR REPLACE INTO pending_subagent_starts
			(agent_id, agent_type, session_id, created_at, agent_span_id)
		VALUES (?, 'feature-coder', ?, ?, ?)`,
		agentID, sessionID, time.Now().UnixMicro(), agentSpanID,
	); err != nil {
		t.Fatalf("seed pending_subagent_starts: %v", err)
	}

	// Seed the Agent span row so it exists in otel_signals.
	if _, err := readDB.Exec(`
		INSERT OR IGNORE INTO otel_signals
			(signal_id, harness, session_id, trace_id, span_id, kind, canonical, native, ts_micros, attrs_json)
		VALUES ('sig-agent-real', 'claude_code', ?, ?, ?, 'span', 'subagent_invocation', 'agent_invocation', ?, '{}')`,
		sessionID, traceID, agentSpanID, time.Now().Add(-5*time.Second).UnixMicro(),
	); err != nil {
		t.Fatalf("seed agent span: %v", err)
	}

	// Seed the interaction span (this is the wrong parent the mis-parented span points to).
	if _, err := readDB.Exec(`
		INSERT OR IGNORE INTO otel_signals
			(signal_id, harness, session_id, trace_id, span_id, kind, canonical, native, ts_micros, attrs_json)
		VALUES ('sig-interaction', 'claude_code', ?, ?, ?, 'span', 'interaction', 'interaction', ?, '{}')`,
		sessionID, traceID, interactionSpanID, time.Now().Add(-10*time.Second).UnixMicro(),
	); err != nil {
		t.Fatalf("seed interaction span: %v", err)
	}

	// Build the mis-parented child span: parent_span points at interaction, not Agent.
	childSpan := sigFixture(sessionID, "prompt-reattrib-a", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-child-reattrib-a"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalToolExecution
		s.NativeName = "claude_code.tool_execution"
		s.SpanID = "child-span-reattrib-a-222"
		s.ParentSpan = interactionSpanID // WRONG — should be agentSpanID
		s.TraceID = traceID
		s.ToolName = "Edit"
	})

	resourceAttrs := map[string]any{
		"service.name":     "claude-code",
		"wipnote.agent_id": agentID, // Strategy A trigger
	}
	n, err := w.WriteBatch(ctx, otel.HarnessClaude, resourceAttrs, []otel.UnifiedSignal{childSpan})
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("inserted = %d, want 1", n)
	}

	// The inserted row must have parent_span = agentSpanID (re-attributed), not interactionSpanID.
	var parentSpan string
	if err := w.DB().QueryRow(
		`SELECT COALESCE(parent_span,'') FROM otel_signals WHERE signal_id='sig-child-reattrib-a'`,
	).Scan(&parentSpan); err != nil {
		t.Fatalf("lookup parent_span: %v", err)
	}
	if parentSpan != agentSpanID {
		t.Errorf("parent_span = %q, want %q (Strategy A re-attribution failed)", parentSpan, agentSpanID)
	}
}

// TestWriter_ReattributesByOverlapWindow verifies Strategy B re-attribution:
// a span without wipnote.agent_id, whose parent is an interaction span, gets
// re-parented to the single Agent span whose window contains its timestamp.
func TestWriter_ReattributesByOverlapWindow(t *testing.T) {
	w, dbPath := newWriter(t)
	ctx := context.Background()

	sessionID := "sess-reattrib-b"
	agentSpanID := "agent-span-reattrib-bbb-111"
	interactionSpanID := "interaction-span-reattrib-bbb-000"
	traceID := "trace-reattrib-bbb"

	// Anchor times: agent started 30s ago and ran for 60s.
	now := time.Now()
	agentStart := now.Add(-30 * time.Second)
	agentDurationMs := int64(60_000) // 60 seconds

	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open readDB: %v", err)
	}
	defer readDB.Close()
	if _, err := readDB.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status) VALUES (?, 'claude-code', 'active')`,
		sessionID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Seed the Agent span with a known time window.
	if _, err := readDB.Exec(`
		INSERT OR IGNORE INTO otel_signals
			(signal_id, harness, session_id, trace_id, span_id, kind, canonical, native, ts_micros, duration_ms, attrs_json)
		VALUES ('sig-agent-b', 'claude_code', ?, ?, ?, 'span', 'subagent_invocation', 'agent_invocation', ?, ?, '{}')`,
		sessionID, traceID, agentSpanID, agentStart.UnixMicro(), agentDurationMs,
	); err != nil {
		t.Fatalf("seed agent span: %v", err)
	}

	// Seed the interaction span (wrong parent pointer).
	if _, err := readDB.Exec(`
		INSERT OR IGNORE INTO otel_signals
			(signal_id, harness, session_id, trace_id, span_id, kind, canonical, native, ts_micros, attrs_json)
		VALUES ('sig-interaction-b', 'claude_code', ?, ?, ?, 'span', 'interaction', 'interaction', ?, '{}')`,
		sessionID, traceID, interactionSpanID, now.Add(-60*time.Second).UnixMicro(),
	); err != nil {
		t.Fatalf("seed interaction span: %v", err)
	}

	// Build the mis-parented child span: timestamp falls INSIDE the agent window.
	childTs := agentStart.Add(5 * time.Second) // 5s after agent start → inside window
	childSpan := sigFixture(sessionID, "prompt-reattrib-b", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-child-reattrib-b"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalToolExecution
		s.NativeName = "claude_code.tool_execution"
		s.SpanID = "child-span-reattrib-b-222"
		s.ParentSpan = interactionSpanID // WRONG — should be agentSpanID
		s.TraceID = traceID
		s.Timestamp = childTs
		s.ToolName = "Bash"
	})

	// No wipnote.agent_id — Strategy B should kick in.
	resourceAttrs := map[string]any{
		"service.name": "claude-code",
	}
	n, err := w.WriteBatch(ctx, otel.HarnessClaude, resourceAttrs, []otel.UnifiedSignal{childSpan})
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("inserted = %d, want 1", n)
	}

	// The inserted row must have parent_span = agentSpanID (re-attributed).
	var parentSpan string
	if err := w.DB().QueryRow(
		`SELECT COALESCE(parent_span,'') FROM otel_signals WHERE signal_id='sig-child-reattrib-b'`,
	).Scan(&parentSpan); err != nil {
		t.Fatalf("lookup parent_span: %v", err)
	}
	if parentSpan != agentSpanID {
		t.Errorf("parent_span = %q, want %q (Strategy B re-attribution failed)", parentSpan, agentSpanID)
	}
}

// TestWriter_DoesNotReattributeWhenAmbiguous verifies that when two Agent spans
// overlap in time and both could contain the incoming span's timestamp, Strategy B
// does NOT re-parent (ambiguous case — log warning only).
func TestWriter_DoesNotReattributeWhenAmbiguous(t *testing.T) {
	w, dbPath := newWriter(t)
	ctx := context.Background()

	sessionID := "sess-reattrib-ambig"
	agentSpanID1 := "agent-span-ambig-111"
	agentSpanID2 := "agent-span-ambig-222"
	interactionSpanID := "interaction-span-ambig-000"
	traceID := "trace-reattrib-ambig"

	now := time.Now()
	agentStart := now.Add(-30 * time.Second)
	agentDurationMs := int64(60_000) // both agents span the same wide window

	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open readDB: %v", err)
	}
	defer readDB.Close()
	if _, err := readDB.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status) VALUES (?, 'claude-code', 'active')`,
		sessionID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Seed TWO overlapping Agent spans, both covering the same broad time window.
	for i, spanID := range []string{agentSpanID1, agentSpanID2} {
		if _, err := readDB.Exec(`
			INSERT OR IGNORE INTO otel_signals
				(signal_id, harness, session_id, trace_id, span_id, kind, canonical, native, ts_micros, duration_ms, attrs_json)
			VALUES (?, 'claude_code', ?, ?, ?, 'span', 'subagent_invocation', 'agent_invocation', ?, ?, '{}')`,
			fmt.Sprintf("sig-agent-ambig-%d", i+1), sessionID, traceID, spanID,
			agentStart.UnixMicro(), agentDurationMs,
		); err != nil {
			t.Fatalf("seed agent span %d: %v", i+1, err)
		}
	}

	// Seed the interaction span.
	if _, err := readDB.Exec(`
		INSERT OR IGNORE INTO otel_signals
			(signal_id, harness, session_id, trace_id, span_id, kind, canonical, native, ts_micros, attrs_json)
		VALUES ('sig-interaction-ambig', 'claude_code', ?, ?, ?, 'span', 'interaction', 'interaction', ?, '{}')`,
		sessionID, traceID, interactionSpanID, now.Add(-60*time.Second).UnixMicro(),
	); err != nil {
		t.Fatalf("seed interaction span: %v", err)
	}

	// Build a mis-parented child span inside both agent windows.
	childTs := agentStart.Add(5 * time.Second)
	childSpan := sigFixture(sessionID, "prompt-ambig", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-child-reattrib-ambig"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalToolExecution
		s.NativeName = "claude_code.tool_execution"
		s.SpanID = "child-span-reattrib-ambig-333"
		s.ParentSpan = interactionSpanID // WRONG, but ambiguous → should NOT be changed
		s.TraceID = traceID
		s.Timestamp = childTs
		s.ToolName = "Bash"
	})

	// No wipnote.agent_id — only Strategy B is attempted.
	resourceAttrs := map[string]any{
		"service.name": "claude-code",
	}
	n, err := w.WriteBatch(ctx, otel.HarnessClaude, resourceAttrs, []otel.UnifiedSignal{childSpan})
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("inserted = %d, want 1", n)
	}

	// The inserted row must have parent_span = interactionSpanID (unchanged — ambiguous).
	var parentSpan string
	if err := w.DB().QueryRow(
		`SELECT COALESCE(parent_span,'') FROM otel_signals WHERE signal_id='sig-child-reattrib-ambig'`,
	).Scan(&parentSpan); err != nil {
		t.Fatalf("lookup parent_span: %v", err)
	}
	if parentSpan != interactionSpanID {
		t.Errorf("parent_span = %q, want %q (ambiguous case should NOT re-parent)", parentSpan, interactionSpanID)
	}
}

// TestNewWriter_DoesNotForceWAL verifies that NewWriter does not hardcode
// journal_mode=WAL in its DSN. On filesystems where BuildPragmas resolves to
// DELETE (e.g. overlayfs, virtiofs, tmpfs — common in CI and devcontainers),
// the DB file must remain in DELETE mode after NewWriter returns. If someone
// re-introduces _pragma=journal_mode(WAL) in the DSN this test will fail.
func TestNewWriter_DoesNotForceWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wal_check.db")

	// db.Open creates the schema and runs ApplyPragmas (which may set DELETE).
	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	readDB.Close()

	// Open NewWriter — the bug was that this call hardcoded WAL in the DSN,
	// permanently switching the file to WAL even when BuildPragmas said DELETE.
	w, err := receiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	w.Close()

	// Read journal_mode from a fresh connection (independent of the writer).
	probe, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("probe open: %v", err)
	}
	defer probe.Close()

	var mode string
	if err := probe.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}

	// The expected mode is whatever BuildPragmas resolves to for this path.
	want := db.BuildPragmas(dbPath)["journal_mode"]
	if mode != strings.ToLower(want) {
		t.Errorf("journal_mode = %q after NewWriter, want %q (BuildPragmas decision); "+
			"NewWriter must not hardcode WAL in its DSN", mode, strings.ToLower(want))
	}
}

// TestNewWriter_AppliesPendingMigrations reproduces bug-286ce8f7 (filed
// against team-lead's bug-6f0f0b3a): NewWriter used to open its DB handle via
// a raw sql.Open and never ran schema migrations at all, silently depending
// on some OTHER writer (serve_child's db.Open, a hook) having already
// migrated the same file first. cmd/wipnote/reindex_otel_events.go calls
// NewWriter directly with no such guarantee, so a DB that fell behind (or a
// fresh file nothing else has touched yet) got a writer whose INSERT
// referenced a column — otel_signals.agent_id — the physical schema did not
// have, failing on every single insert.
//
// This seeds a DB missing that column (the same setup
// core/db.TestMigrateFromAlreadyCurrentDB_MissingLaterColumn uses), then
// confirms NewWriter itself repairs the schema before returning, with no
// other writer or `wipnote serve` needing to have opened the file first.

// otelAttributionStepVersion is the version of the migration step that owns
// otel_signals.agent_id (019_otel_signals_attribution_columns). Kept in sync
// with the constant of the same name in core/db's migration tests.
const otelAttributionStepVersion = 19

func TestNewWriter_AppliesPendingMigrations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing_agent_id.db")

	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed db.Open: %v", err)
	}
	if _, err := seed.Exec(`DROP INDEX IF EXISTS idx_otel_agent_ts`); err != nil {
		t.Fatalf("drop idx_otel_agent_ts: %v", err)
	}
	if _, err := seed.Exec(`ALTER TABLE otel_signals DROP COLUMN agent_id`); err != nil {
		t.Fatalf("drop agent_id to simulate a DB that fell behind: %v", err)
	}
	// Rewind to just below the step that OWNS agent_id
	// (019_otel_signals_attribution_columns), not to CurrentSchemaVersion-1.
	// The latter worked only while 019 happened to be the newest step: any later
	// step left the rewind above 019, so the owning migration never re-ran and
	// this test failed on a repair it never actually asked for.
	if _, err := seed.Exec(fmt.Sprintf("PRAGMA user_version = %d", otelAttributionStepVersion-1)); err != nil {
		t.Fatalf("rewind user_version: %v", err)
	}
	seed.Close()

	// The real regression check: NewWriter (own-pool mode, exactly what
	// reindex_otel_events.go calls) must repair the schema itself.
	w, err := receiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter did not repair a DB with a pending migration: %v", err)
	}
	defer w.Close()

	ctx := context.Background()
	sig := sigFixture("sess-migrated-writer", "prompt-migrated-writer", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-migrated-writer"
		s.RawAttrs = map[string]any{"agent_id": "otel-success@sess-migrated-writer"}
	})
	if _, err := w.WriteBatch(ctx, otel.HarnessClaude, map[string]any{"service.name": "claude-code"}, []otel.UnifiedSignal{sig}); err != nil {
		t.Fatalf("WriteBatch after repair: %v", err)
	}

	probe, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("probe open: %v", err)
	}
	defer probe.Close()

	var version int
	if err := probe.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if want := db.CurrentSchemaVersion(); version != want {
		t.Errorf("user_version after NewWriter = %d, want %d", version, want)
	}

	var agentID sql.NullString
	if err := probe.QueryRow(
		`SELECT agent_id FROM otel_signals WHERE signal_id = ?`, "sig-migrated-writer",
	).Scan(&agentID); err != nil {
		t.Fatalf("select agent_id: %v", err)
	}
	if !agentID.Valid || agentID.String == "" {
		t.Error("agent_id is NULL/empty after WriteBatch on a repaired writer — migration did not actually apply before inserts started")
	}
}

// TestWriter_OrphanSpanNoAgentIDGracefulDegrade verifies that an orphan span
// without wipnote.agent_id in resource attrs does NOT synthesise a placeholder
// (graceful degradation for pre-upgrade sessions).
func TestWriter_OrphanSpanNoAgentIDGracefulDegrade(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()

	sessionID := "sess-no-agent-id"
	orphanParentSpan := "parent-span-no-agent-999"

	orphanSpan := sigFixture(sessionID, "prompt-no-agent", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-orphan-no-agent"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalToolExecution
		s.NativeName = "claude_code.tool_execution"
		s.SpanID = "child-span-no-agent-555"
		s.ParentSpan = orphanParentSpan
	})

	// No wipnote.agent_id in resource attrs — should not create placeholder.
	resourceAttrs := map[string]any{
		"service.name": "claude-code",
		// intentionally omitting wipnote.agent_id
	}
	n, err := w.WriteBatch(ctx, otel.HarnessClaude, resourceAttrs, []otel.UnifiedSignal{orphanSpan})
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if n != 1 {
		t.Errorf("inserted = %d, want 1", n)
	}

	// No placeholder should be created for the missing parent.
	var count int
	if err := w.DB().QueryRow(
		`SELECT COUNT(*) FROM otel_signals WHERE span_id = ?`, orphanParentSpan,
	).Scan(&count); err != nil {
		t.Fatalf("count parent rows: %v", err)
	}
	if count != 0 {
		t.Errorf("placeholder created unexpectedly for no-agent-id orphan: count=%d", count)
	}
}

// TestWriter_ResolvesAgentIDFromRawAttrs verifies that a span carrying its
// own native "agent_id" attribute — Claude Code's per-span attribution on
// claude_code.llm_request / claude_code.tool spans, passed through unmodified
// by observe/otel/adapter/claude.go — lands straight in the new
// otel_signals.agent_id column (feat-be696acc).
func TestWriter_ResolvesAgentIDFromRawAttrs(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()

	sig := sigFixture("sess-rawattr", "prompt-1", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-rawattr-llm"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalAPIRequest
		s.NativeName = "claude_code.llm_request"
		s.SpanID = "span-rawattr-llm-1"
		s.RawAttrs = map[string]any{"agent_id": "otel-success@session-abc"}
	})

	if _, err := w.WriteBatch(ctx, otel.HarnessClaude, map[string]any{"service.name": "claude-code"}, []otel.UnifiedSignal{sig}); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	var agentID sql.NullString
	if err := w.DB().QueryRow(
		`SELECT agent_id FROM otel_signals WHERE signal_id = ?`, "sig-rawattr-llm",
	).Scan(&agentID); err != nil {
		t.Fatalf("lookup agent_id: %v", err)
	}
	if !agentID.Valid || agentID.String != "otel-success@session-abc" {
		t.Errorf("agent_id = %v, want %q", agentID, "otel-success@session-abc")
	}
}

// TestWriter_ResolvesAgentIDFromParentSpanOneHop verifies the one-hop parent
// rescue for children that don't carry agent_id themselves (e.g.
// claude_code.tool.execution under claude_code.tool), and — just as
// importantly — that it stops at exactly one hop: a grandchild whose
// immediate parent ALSO lacks agent_id must NOT reach past it to the
// grandparent. Parent and child are placed in the same batch with the child
// FIRST in the array, proving resolution doesn't depend on insertion order
// (spans typically export child-before-parent, since a span completes before
// the parent enclosing it does).
func TestWriter_ResolvesAgentIDFromParentSpanOneHop(t *testing.T) {
	w, _ := newWriter(t)
	ctx := context.Background()

	sessionID := "sess-onehop"

	parent := sigFixture(sessionID, "prompt-1", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-onehop-parent"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalToolResult
		s.NativeName = "claude_code.tool"
		s.SpanID = "span-onehop-parent"
		s.RawAttrs = map[string]any{"agent_id": "agent-onehop-parent"}
	})
	child := sigFixture(sessionID, "prompt-1", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-onehop-child"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalToolExecution
		s.NativeName = "claude_code.tool.execution"
		s.SpanID = "span-onehop-child"
		s.ParentSpan = "span-onehop-parent"
		s.RawAttrs = nil // no agent_id of its own — must inherit via one hop
	})
	grandchild := sigFixture(sessionID, "prompt-1", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-onehop-grandchild"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalToolExecution
		s.NativeName = "claude_code.tool.execution"
		s.SpanID = "span-onehop-grandchild"
		s.ParentSpan = "span-onehop-child" // child ALSO lacks its own agent_id
		s.RawAttrs = nil
	})

	if _, err := w.WriteBatch(ctx, otel.HarnessClaude, map[string]any{"service.name": "claude-code"},
		[]otel.UnifiedSignal{child, parent, grandchild}); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	var childAgent sql.NullString
	if err := w.DB().QueryRow(`SELECT agent_id FROM otel_signals WHERE signal_id = ?`, "sig-onehop-child").Scan(&childAgent); err != nil {
		t.Fatalf("lookup child agent_id: %v", err)
	}
	if !childAgent.Valid || childAgent.String != "agent-onehop-parent" {
		t.Errorf("child agent_id = %v, want one-hop-inherited %q", childAgent, "agent-onehop-parent")
	}

	var grandchildAgent sql.NullString
	if err := w.DB().QueryRow(`SELECT agent_id FROM otel_signals WHERE signal_id = ?`, "sig-onehop-grandchild").Scan(&grandchildAgent); err != nil {
		t.Fatalf("lookup grandchild agent_id: %v", err)
	}
	if grandchildAgent.Valid && grandchildAgent.String != "" {
		t.Errorf("grandchild agent_id = %v, want NULL (root) — must not walk past its NULL-agent_id parent to the grandparent", grandchildAgent)
	}
}

// TestWriter_FeatureIDJoinsOnResolvedAgentID is the load-bearing test for
// feat-be696acc: it proves the feature_id lookup actually joins
// active_work_items on the SIGNAL's resolved agent_id, not that it merely
// falls back to __root__. Both a non-root agent claim AND a DIFFERENT
// __root__ claim are seeded in the same session; if the join logic
// regressed to always reading __root__ (the pre-fix behavior), this test
// would observe the root feature and fail — a test that only passes because
// everything collapses to __root__ proves nothing, so this one is built to
// catch exactly that regression.
func TestWriter_FeatureIDJoinsOnResolvedAgentID(t *testing.T) {
	w, dbPath := newWriter(t)
	ctx := context.Background()

	sessionID := "sess-join-agent"
	agentID := "otel-success@session-join"

	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open readDB: %v", err)
	}
	defer readDB.Close()
	if _, err := readDB.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status) VALUES (?, 'claude-code', 'active')`,
		sessionID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Seed two DIFFERENT claims in the same session: root has one work item,
	// the named agent has a different one. If the lookup ever falls back to
	// __root__ instead of joining on the resolved agent, this test observes
	// the wrong (root) feature and fails.
	if err := db.SetActiveWorkItem(readDB, sessionID, db.AgentRootSentinel, "feat-root-claim"); err != nil {
		t.Fatalf("seed root claim: %v", err)
	}
	if err := db.SetActiveWorkItem(readDB, sessionID, agentID, "feat-agent-claim"); err != nil {
		t.Fatalf("seed agent claim: %v", err)
	}

	sig := sigFixture(sessionID, "prompt-join", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-join-agent"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalAPIRequest
		s.NativeName = "claude_code.llm_request"
		s.SpanID = "span-join-agent"
		s.RawAttrs = map[string]any{"agent_id": agentID}
	})

	if _, err := w.WriteBatch(ctx, otel.HarnessClaude, map[string]any{"service.name": "claude-code"}, []otel.UnifiedSignal{sig}); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	var featureID string
	if err := w.DB().QueryRow(
		`SELECT COALESCE(feature_id, '') FROM otel_signals WHERE signal_id = ?`, "sig-join-agent",
	).Scan(&featureID); err != nil {
		t.Fatalf("lookup feature_id: %v", err)
	}
	if featureID != "feat-agent-claim" {
		t.Errorf("feature_id = %q, want %q (the agent's own claim, not the root's %q)", featureID, "feat-agent-claim", "feat-root-claim")
	}
}

// TestWriter_FeatureIDFallsBackToRootWhenAgentHasNoClaim proves the OTHER
// half of the join: when the resolved agent has no claim of its own,
// feature_id falls back to the session's __root__ claim rather than going
// NULL. This is deliberately the SAME observable outcome as today's
// pre-fix behavior, and that's the point — see bug-190950e0 (WIPNOTE_AGENT_ID
// propagation for Agent-Teams sessions is separately broken, so until it
// lands, active_work_items holds essentially only __root__ claims and this
// fallback is what keeps dashboards showing the same thing they show today,
// not a regression to no attribution at all).
func TestWriter_FeatureIDFallsBackToRootWhenAgentHasNoClaim(t *testing.T) {
	w, dbPath := newWriter(t)
	ctx := context.Background()

	sessionID := "sess-join-fallback"
	agentID := "agent-with-no-claim-of-its-own"

	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open readDB: %v", err)
	}
	defer readDB.Close()
	if _, err := readDB.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status) VALUES (?, 'claude-code', 'active')`,
		sessionID,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// Only the root claims anything in this session — agentID never ran
	// `wipnote <type> start`.
	if err := db.SetActiveWorkItem(readDB, sessionID, db.AgentRootSentinel, "feat-root-only"); err != nil {
		t.Fatalf("seed root claim: %v", err)
	}

	sig := sigFixture(sessionID, "prompt-fallback", func(s *otel.UnifiedSignal) {
		s.SignalID = "sig-join-fallback"
		s.Kind = otel.KindSpan
		s.CanonicalName = otel.CanonicalAPIRequest
		s.NativeName = "claude_code.llm_request"
		s.SpanID = "span-join-fallback"
		s.RawAttrs = map[string]any{"agent_id": agentID}
	})

	if _, err := w.WriteBatch(ctx, otel.HarnessClaude, map[string]any{"service.name": "claude-code"}, []otel.UnifiedSignal{sig}); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	var featureID string
	if err := w.DB().QueryRow(
		`SELECT COALESCE(feature_id, '') FROM otel_signals WHERE signal_id = ?`, "sig-join-fallback",
	).Scan(&featureID); err != nil {
		t.Fatalf("lookup feature_id: %v", err)
	}
	if featureID != "feat-root-only" {
		t.Errorf("feature_id = %q, want fallback to root claim %q", featureID, "feat-root-only")
	}

	// The row's agent_id column should still reflect the SIGNAL's own
	// resolved identity, not root — the fallback only affects feature_id.
	var agentCol sql.NullString
	if err := w.DB().QueryRow(
		`SELECT agent_id FROM otel_signals WHERE signal_id = ?`, "sig-join-fallback",
	).Scan(&agentCol); err != nil {
		t.Fatalf("lookup agent_id: %v", err)
	}
	if !agentCol.Valid || agentCol.String != agentID {
		t.Errorf("agent_id = %v, want %q (feature_id fallback must not overwrite the resolved agent identity)", agentCol, agentID)
	}
}
