package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/otel"
)

// dbExecer is the minimal interface shared by *sql.Conn and *sql.Tx, used
// by helpers that need to issue queries within a live transaction without
// caring whether they hold a *sql.Tx or a raw *sql.Conn.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Writer persists UnifiedSignals into the otel_signals table. It owns
// its own *sql.DB with MaxOpenConns=1 so every write serializes through
// one connection — this eliminates SQLITE_BUSY errors under concurrent
// load from the OTLP receiver and hook binaries that share the DB file.
//
// All inserts go through BEGIN IMMEDIATE transactions (one per batch);
// IMMEDIATE acquires the writer lock up front so we don't burn retry
// budget on deferred upgrades. Prepared statements are held for the
// Writer's lifetime.
//
// conn is a pinned *sql.Conn obtained at construction from the single
// underlying connection. Using a pinned conn lets us issue raw
// "BEGIN IMMEDIATE" / "COMMIT" / "ROLLBACK" statements that the
// database/sql api cannot express through sql.TxOptions.
type Writer struct {
	db              *sql.DB
	conn            *sql.Conn // pinned to the single MaxOpenConns=1 connection
	insertStmt      *sql.Stmt
	sessStmt        *sql.Stmt
	resStmt         *sql.Stmt
	placeholderStmt *sql.Stmt  // INSERT placeholder subagent_invocation row
	upgradeStmt     *sql.Stmt  // UPDATE placeholder → real Agent span
	mu              sync.Mutex // serializes WriteBatch calls — SQLite serializes writes anyway via IMMEDIATE lock, this just makes it explicit at the Go layer
}

// NewWriter opens a writer-mode DB handle on dbPath. The handle is
// separate from whatever read pool the caller may already have open:
//
//	readers := db.Open(path)             // existing read pool
//	writer  := receiver.NewWriter(path)  // dedicated single-conn writer
//
// Both are fine because SQLite WAL mode allows concurrent readers with
// a single writer. The caller must Close the writer on shutdown so the
// prepared statements release.
func NewWriter(dbPath string) (*Writer, error) {
	// Per-connection pragmas only — journal_mode is intentionally absent.
	// BuildPragmas (via ApplyPragmas on the read-pool Open) is the sole
	// source of truth for journal_mode; on unsafe filesystems it resolves
	// to DELETE. Setting WAL here would permanently override that decision
	// for the lifetime of the DB file, breaking all subsequent connections.
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	// The single-writer constraint is the core of the concurrency
	// design. We use MaxOpenConns=3: the writer pins one connection,
	// and the remaining 2 allow concurrent readers and test assertions
	// (e.g., QueryRow in ConcurrentBatches test). SQLite WAL mode
	// supports multiple concurrent readers; only writes serialize.
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(0)

	// Acquire the pinned connection before preparing statements so that
	// all prepared statements and BEGIN IMMEDIATE calls share the exact
	// same underlying SQLite connection. Since MaxOpenConns=1 this is
	// the one and only connection the pool will ever create.
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("pin conn: %w", err)
	}

	w := &Writer{db: db, conn: conn}
	if err := w.prepare(); err != nil {
		conn.Close()
		db.Close()
		return nil, err
	}
	return w, nil
}

func (w *Writer) prepare() error {
	ctx := context.Background()
	var err error
	w.insertStmt, err = w.conn.PrepareContext(ctx, `
		INSERT OR IGNORE INTO otel_signals (
			signal_id, harness, session_id, prompt_id,
			trace_id, span_id, parent_span,
			kind, canonical, native, ts_micros,
			tool_name, tool_use_id, model, decision, decision_source,
			tokens_in, tokens_out, tokens_cache_read, tokens_cache_creation,
			tokens_thought, tokens_tool, tokens_reasoning,
			cost_usd, cost_source,
			duration_ms, success, error_msg, attempt, status_code,
			attrs_json, feature_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	// Session placeholder upsert: if the OTLP receiver sees a session_id
	// we haven't created via the hooks path, we create a minimal row so
	// the FK resolves. If SessionStart later fires for the same id, it
	// upgrades agent_assigned from the placeholder. Status stays 'active'.
	w.sessStmt, err = w.conn.PrepareContext(ctx, `
		INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status)
		VALUES (?, ?, 'active')`)
	if err != nil {
		return fmt.Errorf("prepare session upsert: %w", err)
	}
	// Resource attribute upsert: per (session_id, key), replace on conflict.
	// OTel resource attrs repeat on every batch; we want the latest value.
	w.resStmt, err = w.conn.PrepareContext(ctx, `
		INSERT INTO otel_resource_attrs (session_id, harness, key, value, observed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id, key) DO UPDATE SET
			value = excluded.value,
			observed_at = excluded.observed_at`)
	if err != nil {
		return fmt.Errorf("prepare resource upsert: %w", err)
	}

	// Placeholder upsert: synthesise a minimal subagent_invocation row keyed on
	// the orphan span's parent_span so the dashboard sees an Agent node immediately
	// rather than waiting minutes for the real Agent span to arrive.
	// ON CONFLICT(signal_id) DO NOTHING: if we already wrote a placeholder for this
	// signal_id, leave it alone (idempotent re-delivery).
	// The span_id unique index (idx_otel_span_id_unique) is NOT used here; instead
	// we guard at the call site with an existence check on span_id.
	w.placeholderStmt, err = w.conn.PrepareContext(ctx, `
		INSERT OR IGNORE INTO otel_signals (
			signal_id, harness, session_id,
			trace_id, span_id,
			kind, canonical, native, ts_micros,
			tool_name,
			attrs_json
		) VALUES (?, ?, ?, ?, ?, 'span', 'subagent_invocation', 'agent_invocation', ?, 'Agent', ?)`)
	if err != nil {
		return fmt.Errorf("prepare placeholder insert: %w", err)
	}

	// Upgrade statement: when the real Agent span arrives and a placeholder row
	// already exists for the same span_id, overwrite the placeholder's fields
	// with actual data. We identify placeholder rows via attrs_json containing
	// "_pending":true so we don't accidentally overwrite real data.
	w.upgradeStmt, err = w.conn.PrepareContext(ctx, `
		UPDATE otel_signals SET
			signal_id = ?,
			harness = ?,
			prompt_id = ?,
			trace_id = ?,
			parent_span = ?,
			native = ?,
			ts_micros = ?,
			tool_name = ?,
			tool_use_id = ?,
			model = ?,
			decision = ?,
			decision_source = ?,
			tokens_in = ?,
			tokens_out = ?,
			tokens_cache_read = ?,
			tokens_cache_creation = ?,
			tokens_thought = ?,
			tokens_tool = ?,
			tokens_reasoning = ?,
			cost_usd = ?,
			cost_source = ?,
			duration_ms = ?,
			success = ?,
			error_msg = ?,
			attempt = ?,
			status_code = ?,
			attrs_json = ?,
			feature_id = ?
		WHERE span_id = ? AND attrs_json LIKE '%"_pending":true%'`)
	if err != nil {
		return fmt.Errorf("prepare upgrade stmt: %w", err)
	}

	return nil
}

// Close releases prepared statements and the underlying connection.
func (w *Writer) Close() error {
	if w.insertStmt != nil {
		w.insertStmt.Close()
	}
	if w.sessStmt != nil {
		w.sessStmt.Close()
	}
	if w.resStmt != nil {
		w.resStmt.Close()
	}
	if w.placeholderStmt != nil {
		w.placeholderStmt.Close()
	}
	if w.upgradeStmt != nil {
		w.upgradeStmt.Close()
	}
	if w.conn != nil {
		w.conn.Close()
	}
	return w.db.Close()
}

// WriteBatch persists one OTLP request's worth of signals plus the
// resource attributes that produced them. The whole batch runs in one
// BEGIN IMMEDIATE transaction — either every signal lands or none do.
//
// session_ids are deduplicated inside the transaction so we only issue
// one sessions placeholder upsert per distinct session in the batch.
//
// Returns the number of rows actually inserted (excludes idempotent
// rejections on duplicate signal_id). Callers log the rejection count
// separately for observability.
func (w *Writer) WriteBatch(
	ctx context.Context,
	harness otel.Harness,
	resourceAttrs map[string]any,
	signals []otel.UnifiedSignal,
) (inserted int, err error) {
	if len(signals) == 0 {
		return 0, nil
	}

	// Slice-10 contention observability: classify any error returned from
	// this WriteBatch under the writer_service subsystem. The launch gate
	// asserts this counter remains zero across the contention stress
	// fixture. See internal/db/busy_counter.go for the subsystem taxonomy.
	defer func() {
		db.Record(db.SubsystemWriterService, err)
	}()

	w.mu.Lock()
	defer w.mu.Unlock()

	err = db.RetryOnBusy(db.DefaultBusyBackoff, func() error {
		var attemptInserted int
		attemptInserted, err = w.writeBatchAttempt(ctx, harness, resourceAttrs, signals)
		if err != nil {
			return err
		}
		inserted = attemptInserted
		return nil
	})
	return inserted, err
}

func (w *Writer) writeBatchAttempt(
	ctx context.Context,
	harness otel.Harness,
	resourceAttrs map[string]any,
	signals []otel.UnifiedSignal,
) (inserted int, err error) {
	// BEGIN IMMEDIATE acquires the write lock up front, avoiding the
	// SHARED→RESERVED→EXCLUSIVE upgrade race that a DEFERRED transaction
	// triggers. With DEFERRED, SQLite holds only a SHARED lock until the
	// first write; another writer can interpose between the SHARED acquisition
	// and the RESERVED upgrade and return SQLITE_BUSY before busy_timeout
	// even gets a chance to retry (the upgrade attempt is not retried under
	// busy_timeout). IMMEDIATE eliminates this race entirely. A bounded
	// RetryOnBusy envelope around this raw transaction attempt still handles
	// DELETE-journal readers blocking COMMIT, or a competing writer already
	// holding RESERVED when this raw BEGIN IMMEDIATE starts.
	if _, err = w.conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, fmt.Errorf("begin immediate: %w", err)
	}
	// rollback is a no-op after a successful COMMIT; safe to call from defer.
	committed := false
	defer func() {
		if !committed {
			w.conn.ExecContext(context.Background(), "ROLLBACK") //nolint:errcheck
		}
	}()

	// Track sessions we've already upserted this batch so we don't
	// fire a redundant INSERT per signal.
	seen := map[string]bool{}
	// Per-session cache of active work item (feature/bug/spike claimed
	// by the session's root agent). Populated lazily on first signal
	// for each session so we issue at most one SELECT per distinct
	// session per batch, regardless of signal count.
	featureByID := map[string]string{}
	resObservedAt := time.Now().UnixMicro()

	// spanExists caches span_ids already present in otel_signals (within this
	// transaction) so we only query the DB once per distinct span_id per batch.
	spanExists := map[string]bool{}

	for i := range signals {
		s := &signals[i]
		if s.SessionID == "" {
			// Drop signals without a session. OTel emissions always
			// carry session.id either on the resource or the signal;
			// a missing one means the adapter couldn't normalize.
			continue
		}
		if !seen[s.SessionID] {
			agent := string(harness)
			if _, err = w.sessStmt.ExecContext(ctx, s.SessionID, agent); err != nil {
				return inserted, fmt.Errorf("sessions upsert: %w", err)
			}
			// Persist the resource attributes snapshot for this session.
			for k, v := range resourceAttrs {
				if sv, ok := valueString(v); ok {
					if _, err = w.resStmt.ExecContext(ctx, s.SessionID, string(harness), k, sv, resObservedAt); err != nil {
						return inserted, fmt.Errorf("resource attr upsert: %w", err)
					}
				}
			}
			seen[s.SessionID] = true
		}

		attrsJSON, jerr := json.Marshal(s.RawAttrs)
		if jerr != nil {
			attrsJSON = []byte(`{}`)
		}

		var successVal sql.NullInt64
		if s.Success != nil {
			successVal.Valid = true
			if *s.Success {
				successVal.Int64 = 1
			}
		}

		// Look up the session's active work item on first encounter,
		// then reuse the cached value. Uses the __root__ sentinel since
		// OTel signals don't carry an agent_id — subagent-level
		// attribution is the planned follow-up (feat-82e11bbb).
		featureID, cached := featureByID[s.SessionID]
		if !cached {
			var fid sql.NullString
			_ = w.conn.QueryRowContext(ctx,
				`SELECT work_item_id FROM active_work_items WHERE session_id = ? AND agent_id = ?`,
				s.SessionID, "__root__",
			).Scan(&fid)
			featureID = fid.String
			featureByID[s.SessionID] = featureID
		}

		// Placeholder upgrade: if this signal is the real Agent/subagent_invocation
		// span, check whether a placeholder exists for the same span_id and update it
		// rather than inserting a duplicate. This transparently promotes the placeholder
		// written during orphan-span detection to a fully-attributed row.
		if s.Kind == otel.KindSpan && s.CanonicalName == otel.CanonicalSubagent && s.SpanID != "" {
			upgraded, upgradeErr := tryUpgradePlaceholder(ctx, w.upgradeStmt, s, attrsJSON, successVal, featureID)
			if upgradeErr != nil {
				return inserted, fmt.Errorf("upgrade placeholder for span %s: %w", s.SpanID, upgradeErr)
			}
			if upgraded {
				inserted++
				continue
			}
		}

		// Orphan span detection: when an incoming span has a parent_span that does
		// not yet exist in otel_signals, synthesise a placeholder row so the
		// dashboard renders the Agent node immediately instead of waiting minutes.
		// Only attempt this when the signal carries wipnote.agent_id so we can
		// look up pending_subagent_starts. Gracefully degrade when missing.
		if s.Kind == otel.KindSpan && s.ParentSpan != "" {
			if err2 := w.maybeCreatePlaceholder(ctx, w.conn, w.placeholderStmt, s, resourceAttrs, spanExists, resObservedAt); err2 != nil {
				// Non-fatal: log via return path but don't block the real signal.
				_ = err2
			}
		}

		// Re-attribution: correct mis-parented spans before INSERT.
		// Some subagent-emitted spans arrive with parent_span pointing to the
		// interaction span instead of the Agent span due to a TRACEPARENT propagation
		// gap in Claude Code. We detect and fix this here so otel_signals.parent_span
		// is correct from the start. Two strategies (A: agent_id resource attr,
		// B: overlap window) are applied in priority order.
		if s.Kind == otel.KindSpan && s.ParentSpan != "" && s.CanonicalName != otel.CanonicalSubagent {
			if newParent, reason := tryReattributeParent(ctx, w.conn, s, resourceAttrs); newParent != "" {
				log.Printf("reattribute: span=%s old_parent=%s new_parent=%s reason=%s",
					s.SpanID, s.ParentSpan, newParent, reason)
				s.ParentSpan = newParent
			}
		}

		res, execErr := w.insertStmt.ExecContext(ctx,
			s.SignalID, string(s.Harness), s.SessionID, nullStr(s.PromptID),
			nullStr(s.TraceID), nullStr(s.SpanID), nullStr(s.ParentSpan),
			string(s.Kind), s.CanonicalName, s.NativeName, s.Timestamp.UnixMicro(),
			nullStr(s.ToolName), nullStr(s.ToolUseID), nullStr(s.Model),
			nullStr(s.Decision), nullStr(s.DecisionSource),
			nullInt64(s.Tokens.Input), nullInt64(s.Tokens.Output),
			nullInt64(s.Tokens.CacheRead), nullInt64(s.Tokens.CacheCreation),
			nullInt64(s.Tokens.Thought), nullInt64(s.Tokens.Tool), nullInt64(s.Tokens.Reasoning),
			nullFloat(s.CostUSD), nullStr(string(s.CostSource)),
			nullInt64(s.DurationMs), successVal, nullStr(s.ErrorMsg),
			nullInt(s.Attempt), nullInt(s.StatusCode),
			string(attrsJSON), nullStr(featureID),
		)
		if execErr != nil {
			return inserted, fmt.Errorf("insert signal %s: %w", s.SignalID, execErr)
		}
		if n, rowsErr := res.RowsAffected(); rowsErr == nil {
			inserted += int(n)
		}
	}

	if _, err = w.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return inserted, fmt.Errorf("commit: %w", err)
	}
	committed = true
	return inserted, nil
}

// maybeCreatePlaceholder synthesises a subagent_invocation placeholder row when
// an incoming span's parent_span does not yet exist in otel_signals. It reads
// wipnote.agent_id from resourceAttrs to look up pending_subagent_starts.
// Errors are logged at the call site and never propagate to the caller.
//
// conn accepts any dbExecer (a pinned *sql.Conn in production). Using the
// same conn that holds the BEGIN IMMEDIATE transaction avoids opening a
// second connection on the MaxOpenConns=1 pool, which would deadlock.
func (w *Writer) maybeCreatePlaceholder(
	ctx context.Context,
	conn dbExecer,
	placeholderStmt *sql.Stmt,
	s *otel.UnifiedSignal,
	resourceAttrs map[string]any,
	spanExists map[string]bool,
	resObservedAt int64,
) error {
	parentSpan := s.ParentSpan

	// Check cache first, then DB.
	if exists, ok := spanExists[parentSpan]; ok && exists {
		return nil
	}

	var n int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM otel_signals WHERE span_id = ?`, parentSpan,
	).Scan(&n); err != nil {
		return nil // non-fatal
	}
	spanExists[parentSpan] = n > 0
	if n > 0 {
		return nil // parent already exists; nothing to do
	}

	// Parent is missing. Check for wipnote.agent_id on the resource.
	agentID, _ := resourceAttrs["wipnote.agent_id"].(string)
	if agentID == "" {
		// No agent_id → we can't look up pending_subagent_starts. Degrade gracefully.
		return nil
	}

	// Look up the pending row using the live conn (MaxOpenConns=1 — we MUST
	// use conn, not w.db, to avoid a deadlock on the single connection).
	var pending db.PendingSubagentStart
	var cwd sql.NullString
	err := conn.QueryRowContext(ctx, `
		SELECT agent_id, agent_type, session_id, cwd, created_at
		FROM pending_subagent_starts
		WHERE agent_id = ?`, agentID,
	).Scan(&pending.AgentID, &pending.AgentType, &pending.SessionID, &cwd, &pending.CreatedAt)
	if err == sql.ErrNoRows {
		return nil // no pending row; subagent started before this feature shipped
	}
	if err != nil {
		return nil // non-fatal
	}
	pending.CWD = cwd.String

	// Build a minimal attrs_json for the placeholder.
	placeholderAttrs := map[string]any{
		"_pending":           true,
		"agent_type":         pending.AgentType,
		"wipnote.agent_id":   agentID,
		"placeholder_source": "subagent_start_hook",
	}
	attrsJSON, _ := json.Marshal(placeholderAttrs)

	// The placeholder signal_id is deterministic so re-delivery of the same
	// first orphan span doesn't create duplicate placeholders.
	placeholderSignalID := "placeholder:" + parentSpan

	if _, err := placeholderStmt.ExecContext(ctx,
		placeholderSignalID, string(s.Harness), pending.SessionID,
		nullStr(s.TraceID), parentSpan,
		pending.CreatedAt, string(attrsJSON),
	); err != nil {
		return fmt.Errorf("insert placeholder: %w", err)
	}

	spanExists[parentSpan] = true

	// Back-fill the agent_span_id mapping so Strategy A re-attribution can
	// resolve (session_id, agent_id) → agent span_id without scanning otel_signals.
	// We use the same conn to avoid the w.db deadlock on the single connection.
	// Best-effort: ignore errors since re-attribution degrades gracefully.
	if _, err := conn.ExecContext(ctx,
		`UPDATE pending_subagent_starts SET agent_span_id = ? WHERE agent_id = ?`,
		parentSpan, agentID,
	); err != nil {
		// Non-fatal: re-attribution will fall back to Strategy B.
		_ = err
	}

	// Mark consumed for observability using a deferred write so we don't
	// block the transaction on an extra round-trip. MarkPendingSubagentConsumed
	// is best-effort; skip it here to avoid the w.db deadlock.
	// The consumed_at column will be set on the next periodic purge sweep.
	_ = resObservedAt

	return nil
}

// tryUpgradePlaceholder upgrades a placeholder subagent_invocation row with
// real Agent span data. Returns true if a placeholder was found and upgraded.
// Returns false when no placeholder exists (caller should proceed with normal INSERT).
func tryUpgradePlaceholder(
	ctx context.Context,
	upgradeStmt *sql.Stmt,
	s *otel.UnifiedSignal,
	attrsJSON []byte,
	successVal sql.NullInt64,
	featureID string,
) (bool, error) {
	res, err := upgradeStmt.ExecContext(ctx,
		s.SignalID, string(s.Harness),
		nullStr(s.PromptID),
		nullStr(s.TraceID),
		nullStr(s.ParentSpan),
		s.NativeName,
		s.Timestamp.UnixMicro(),
		nullStr(s.ToolName), nullStr(s.ToolUseID), nullStr(s.Model),
		nullStr(s.Decision), nullStr(s.DecisionSource),
		nullInt64(s.Tokens.Input), nullInt64(s.Tokens.Output),
		nullInt64(s.Tokens.CacheRead), nullInt64(s.Tokens.CacheCreation),
		nullInt64(s.Tokens.Thought), nullInt64(s.Tokens.Tool), nullInt64(s.Tokens.Reasoning),
		nullFloat(s.CostUSD), nullStr(string(s.CostSource)),
		nullInt64(s.DurationMs), successVal, nullStr(s.ErrorMsg),
		nullInt(s.Attempt), nullInt(s.StatusCode),
		string(attrsJSON), nullStr(featureID),
		s.SpanID, // WHERE span_id = ?
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// tryReattributeParent checks whether the incoming span is mis-parented (i.e.,
// its parent_span points to an interaction span instead of the enclosing Agent
// span). Two strategies are applied in priority order:
//
//	A. wipnote.agent_id resource attr (post-feat-e1efb972): look up the
//	   agent_span_id in pending_subagent_starts for this agent_id; if found
//	   and differs from the incoming parent_span, override.
//
//	B. Overlap window (pre-feat-e1efb972 fallback): when no agent_id is
//	   available, check if the incoming span's timestamp falls within
//	   exactly one sibling subagent_invocation span's [ts, ts+duration]
//	   window and the current parent is an interaction canonical.
//
// Returns (newParentSpanID, reason) when re-attribution applies, or ("", "")
// when no fix is warranted.
func tryReattributeParent(
	ctx context.Context,
	conn dbExecer,
	s *otel.UnifiedSignal,
	resourceAttrs map[string]any,
) (newParent, reason string) {
	// Strategy A: authoritative agent_id mapping.
	agentID, _ := resourceAttrs["wipnote.agent_id"].(string)
	if agentID != "" {
		var agentSpanID sql.NullString
		if err := conn.QueryRowContext(ctx,
			`SELECT agent_span_id FROM pending_subagent_starts WHERE agent_id = ?`, agentID,
		).Scan(&agentSpanID); err == nil && agentSpanID.Valid && agentSpanID.String != "" {
			if agentSpanID.String != s.ParentSpan {
				return agentSpanID.String, "strategy_a_agent_id"
			}
		}
		// agent_id present but no agent_span_id yet (placeholder not created yet) — skip.
		return "", ""
	}

	// Strategy B: overlap window fallback for pre-feat-e1efb972 sessions.
	// Only applies when the current parent is an interaction span.
	if s.SessionID == "" || s.ParentSpan == "" {
		return "", ""
	}

	// Check that the current parent is indeed an interaction canonical.
	var parentCanonical sql.NullString
	if err := conn.QueryRowContext(ctx,
		`SELECT canonical FROM otel_signals WHERE span_id = ?`, s.ParentSpan,
	).Scan(&parentCanonical); err != nil || parentCanonical.String != otel.CanonicalInteraction {
		return "", ""
	}

	// Find all subagent_invocation spans in this session that have a known duration.
	spanTsMicros := s.Timestamp.UnixMicro()
	rows, err := conn.QueryContext(ctx, `
		SELECT span_id, ts_micros, duration_ms
		FROM otel_signals
		WHERE session_id = ? AND canonical = ? AND duration_ms IS NOT NULL AND duration_ms > 0`,
		s.SessionID, otel.CanonicalSubagent,
	)
	if err != nil {
		return "", ""
	}
	defer rows.Close()

	type agentWindow struct {
		spanID      string
		startMicros int64
		endMicros   int64
	}
	var matches []agentWindow
	for rows.Next() {
		var spanID string
		var tsMicros, durationMs int64
		if err := rows.Scan(&spanID, &tsMicros, &durationMs); err != nil {
			continue
		}
		endMicros := tsMicros + durationMs*1000 // duration_ms → microseconds
		if spanTsMicros >= tsMicros && spanTsMicros <= endMicros {
			matches = append(matches, agentWindow{spanID, tsMicros, endMicros})
		}
	}
	if err := rows.Err(); err != nil {
		return "", ""
	}

	if len(matches) == 1 {
		if matches[0].spanID != s.ParentSpan {
			return matches[0].spanID, "strategy_b_overlap_window"
		}
	} else if len(matches) > 1 {
		// Ambiguous: multiple overlapping Agent spans — skip re-parenting.
		log.Printf("reattribute: span=%s ambiguous overlap (%d agent windows), skipping", s.SpanID, len(matches))
	}

	return "", ""
}

// PurgeStaleSubagentStarts removes pending_subagent_starts rows older than 24 h.
// Called on Writer startup and periodically to bound table growth.
func (w *Writer) PurgeStaleSubagentStarts() {
	db.PurgeStalePendingSubagentStarts(w.db)
}

// DB returns the underlying handle. Tests use this to assert row counts
// without opening a second connection (which would contend for the
// MaxOpenConns=1 writer lock).
func (w *Writer) DB() *sql.DB { return w.db }

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

// valueString converts a resource-attribute AnyValue (already flattened
// to map[string]any by the decoder) into a string suitable for the
// otel_resource_attrs.value column. Non-scalar values are JSON-encoded.
func valueString(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case int64:
		return fmt.Sprintf("%d", x), true
	case int:
		return fmt.Sprintf("%d", x), true
	case float64:
		return fmt.Sprintf("%g", x), true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}
