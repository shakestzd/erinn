package receiver_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/otel"
	"github.com/shakestzd/htmlgraph/internal/otel/receiver"
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
// htmlgraph.agent_id matching a pending_subagent_starts row, a placeholder
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
		AgentType: "haiku-coder",
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
		"service.name":       "claude-code",
		"htmlgraph.agent_id": agentID, // triggers placeholder path
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
		AgentType: "sonnet-coder",
		SessionID: sessionID,
		CWD:       "/tmp/upgrade-test",
		CreatedAt: time.Now().Add(-30 * time.Second).UnixMicro(), // started 30s ago
	}
	if err := db.UpsertPendingSubagentStart(readDB, pending); err != nil {
		t.Fatalf("UpsertPendingSubagentStart: %v", err)
	}

	resourceAttrs := map[string]any{
		"service.name":       "claude-code",
		"htmlgraph.agent_id": agentID,
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
		s.CanonicalName = otel.CanonicalSubagent   // triggers upgrade path
		s.NativeName = "claude_code.agent_turn"
		s.SpanID = agentSpanID                     // same span_id as placeholder
		s.ParentSpan = ""                           // root agent span has no parent
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
// a child span arriving with htmlgraph.agent_id and a wrong parent_span (pointing
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
		VALUES (?, 'sonnet-coder', ?, ?, ?)`,
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
		"service.name":       "claude-code",
		"htmlgraph.agent_id": agentID, // Strategy A trigger
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
// a span without htmlgraph.agent_id, whose parent is an interaction span, gets
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

	// No htmlgraph.agent_id — Strategy B should kick in.
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

	// No htmlgraph.agent_id — only Strategy B is attempted.
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

// TestWriter_OrphanSpanNoAgentIDGracefulDegrade verifies that an orphan span
// without htmlgraph.agent_id in resource attrs does NOT synthesise a placeholder
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

	// No htmlgraph.agent_id in resource attrs — should not create placeholder.
	resourceAttrs := map[string]any{
		"service.name": "claude-code",
		// intentionally omitting htmlgraph.agent_id
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
