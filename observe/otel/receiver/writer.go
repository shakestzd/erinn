package receiver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/observe/otel"
)

// dbExecer is the minimal interface shared by *sql.Conn and *sql.Tx, used
// by helpers that need to issue queries within a live transaction without
// caring whether they hold a *sql.Tx or a raw *sql.Conn.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// metricRowsPerSessionLimit bounds high-cardinality metric streams in the
// derived SQLite index. Canonical NDJSON remains the durable replay source;
// SQLite keeps recent metric rows for dashboard queries and drops the tail.
const metricRowsPerSessionLimit = 5000

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
	db   *sql.DB
	conn *sql.Conn // pinned to the single connection (own-pool mode only; nil in shared mode)
	// shared is true when the Writer borrows an externally-owned *sql.DB
	// (feat-075c110d: the daemon's single writable handle) instead of
	// opening + pinning its own. In shared mode the Writer does NOT pin a
	// lifetime connection and does NOT hold lifetime prepared statements;
	// each WriteBatch acquires the pool's connection for the duration of
	// the batch and releases it on completion. The shared handle is
	// opened with MaxOpenConns=1 by the owner, so the database/sql pool
	// itself serializes every caller (the writer batches, the socket-op
	// applier, and the maintenance loops) onto ONE physical SQLite
	// connection — making concurrent BEGIN IMMEDIATE structurally
	// impossible and eliminating the cross-handle SQLITE_BUSY thrash.
	shared          bool
	ownDB           bool      // true when the Writer opened db itself and must Close it
	insertStmt      *sql.Stmt // own-pool mode only: lifetime statements pinned to conn
	sessStmt        *sql.Stmt
	resStmt         *sql.Stmt
	placeholderStmt *sql.Stmt  // INSERT placeholder subagent_invocation row
	upgradeStmt     *sql.Stmt  // UPDATE placeholder → real Agent span
	mu              sync.Mutex // serializes WriteBatch calls — SQLite serializes writes anyway via IMMEDIATE lock, this just makes it explicit at the Go layer
}

// batchStmts is the per-batch set of prepared statements used in shared
// mode. They are prepared on the acquired connection and closed when the
// batch completes, mirroring the lifetime statements held in own-pool mode.
type batchStmts struct {
	insert      *sql.Stmt
	sess        *sql.Stmt
	res         *sql.Stmt
	placeholder *sql.Stmt
	upgrade     *sql.Stmt
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

	// bug-74a7bda7 cold-start race: the DSN deliberately omits journal_mode
	// so the read-pool Open's BuildPragmas stays the single source of truth.
	// But if the Writer is the very first connection to a brand-new DB file
	// (it opens before the read pool in runServeChild), the file starts in
	// the SQLite default (DELETE) and stays there until some WAL-safe
	// connection flips it. On a WAL-safe filesystem that means the writer
	// runs in DELETE for the cold-start window, re-introducing the
	// shared/reserved contention this bug is about. So: assert journal_mode
	// once, here, and ONLY promote DELETE→WAL when the backing filesystem is
	// WAL-safe. On WAL-unsafe filesystems (overlayfs/FUSE/9p/NFS — the
	// devcontainer case) we accept DELETE silently: no error, no log spam.
	// This never forces WAL on overlayfs and never touches isUnsafeForMmap.
	assertWriterJournalMode(conn, dbPath)

	w := &Writer{db: db, conn: conn, ownDB: true}
	if err := w.prepare(); err != nil {
		conn.Close()
		db.Close()
		return nil, err
	}
	return w, nil
}

// NewWriterFromDB constructs a Writer that BORROWS an existing writable
// *sql.DB instead of opening + pinning its own pool. This is the
// daemon's single-writer path (feat-075c110d): serve_child opens ONE
// writable handle with MaxOpenConns=1 (which also runs migrations) and
// shares it across every write path — the socket-op applier, the OTel
// sink (this Writer), the indexer, and the maintenance loops. Because
// the underlying pool caps at a single physical connection, the
// database/sql pool serializes every caller onto that one connection, so
// two concurrent BEGIN IMMEDIATE transactions can never exist and the
// cross-handle SQLITE_BUSY contention is eliminated at the root.
//
// Unlike NewWriter, this constructor does NOT pin a lifetime connection
// (that would starve the applier + maintenance of the single pooled
// connection) and does NOT prepare lifetime statements. Each WriteBatch
// acquires the pool connection for its duration, prepares the batch's
// statements on it, runs the BEGIN IMMEDIATE transaction, and releases
// the connection back to the pool so the next caller can proceed.
//
// The caller OWNS the *sql.DB lifecycle: Writer.Close does NOT close it.
func NewWriterFromDB(database *sql.DB) (*Writer, error) {
	if database == nil {
		return nil, fmt.Errorf("NewWriterFromDB: nil *sql.DB")
	}
	return &Writer{db: database, shared: true, ownDB: false}, nil
}

// assertWriterJournalMode promotes a fresh DB from the SQLite default
// (DELETE) to WAL when — and only when — the backing filesystem is WAL-safe.
// It is a no-op when the DB is already in WAL or when the filesystem is
// WAL-unsafe (the correct, filesystem-agnostic outcome). All failures are
// swallowed: journal-mode resolution is best-effort and the read-pool Open
// remains the authoritative pragma applier.
func assertWriterJournalMode(conn *sql.Conn, dbPath string) {
	ctx := context.Background()
	var mode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return
	}
	if strings.EqualFold(mode, "wal") {
		return // already WAL — nothing to do
	}
	if _, walSafe := db.ProbeFsType(dbPath); !walSafe {
		return // WAL-unsafe FS (overlayfs/FUSE/9p/NFS): DELETE is correct
	}
	// WAL-safe filesystem sitting in DELETE only because this writer is the
	// cold-start connection. Promote to WAL so readers and the writer stop
	// blocking each other for the rest of the DB file's life.
	_, _ = conn.ExecContext(ctx, "PRAGMA journal_mode = WAL")
}

// SQL for the five statements the Writer holds. Shared by both the
// own-pool lifetime-prepare path (prepare) and the shared-handle
// per-batch prepare path (prepareBatchStmts) so the two modes execute
// byte-identical SQL.
const (
	sqlInsertSignal = `
		INSERT OR IGNORE INTO otel_signals (
			signal_id, harness, session_id, prompt_id,
			trace_id, span_id, parent_span,
			kind, canonical, native, ts_micros,
			tool_name, tool_use_id, model, decision, decision_source,
			tokens_in, tokens_out, tokens_cache_read, tokens_cache_creation,
			tokens_thought, tokens_tool, tokens_reasoning,
			cost_usd, cost_source,
			duration_ms, success, error_msg, attempt, status_code,
			attrs_json, feature_id, agent_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	sqlSessionUpsert = `
		INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status)
		VALUES (?, ?, 'active')`

	sqlResourceUpsert = `
		INSERT INTO otel_resource_attrs (session_id, harness, key, value, observed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id, key) DO UPDATE SET
			value = excluded.value,
			observed_at = excluded.observed_at`

	sqlPlaceholderInsert = `
		INSERT OR IGNORE INTO otel_signals (
			signal_id, harness, session_id,
			trace_id, span_id,
			kind, canonical, native, ts_micros,
			tool_name,
			attrs_json
		) VALUES (?, ?, ?, ?, ?, 'span', 'subagent_invocation', 'agent_invocation', ?, 'Agent', ?)`

	sqlUpgradePlaceholder = `
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
			feature_id = ?,
			agent_id = ?
		WHERE span_id = ? AND attrs_json LIKE '%"_pending":true%'`
)

// stmtPreparer is satisfied by both *sql.Conn and *sql.DB, letting the
// statement-preparation helper serve the pinned-conn (own-pool) path and
// the per-batch acquired-conn (shared) path identically.
type stmtPreparer interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// prepareBatchStmts prepares the five statements on p (a pinned *sql.Conn
// in own-pool mode, or a per-batch acquired *sql.Conn in shared mode).
func prepareBatchStmts(ctx context.Context, p stmtPreparer) (*batchStmts, error) {
	bs := &batchStmts{}
	var err error
	if bs.insert, err = p.PrepareContext(ctx, sqlInsertSignal); err != nil {
		return nil, fmt.Errorf("prepare insert: %w", err)
	}
	if bs.sess, err = p.PrepareContext(ctx, sqlSessionUpsert); err != nil {
		bs.close()
		return nil, fmt.Errorf("prepare session upsert: %w", err)
	}
	if bs.res, err = p.PrepareContext(ctx, sqlResourceUpsert); err != nil {
		bs.close()
		return nil, fmt.Errorf("prepare resource upsert: %w", err)
	}
	if bs.placeholder, err = p.PrepareContext(ctx, sqlPlaceholderInsert); err != nil {
		bs.close()
		return nil, fmt.Errorf("prepare placeholder insert: %w", err)
	}
	if bs.upgrade, err = p.PrepareContext(ctx, sqlUpgradePlaceholder); err != nil {
		bs.close()
		return nil, fmt.Errorf("prepare upgrade stmt: %w", err)
	}
	return bs, nil
}

func (bs *batchStmts) close() {
	if bs == nil {
		return
	}
	for _, s := range []*sql.Stmt{bs.insert, bs.sess, bs.res, bs.placeholder, bs.upgrade} {
		if s != nil {
			s.Close()
		}
	}
}

// prepare prepares the lifetime statements on the pinned connection
// (own-pool mode only). Shared-handle writers prepare per-batch instead.
func (w *Writer) prepare() error {
	bs, err := prepareBatchStmts(context.Background(), w.conn)
	if err != nil {
		return err
	}
	w.insertStmt = bs.insert
	w.sessStmt = bs.sess
	w.resStmt = bs.res
	w.placeholderStmt = bs.placeholder
	w.upgradeStmt = bs.upgrade
	return nil
}

// Close releases prepared statements and the underlying connection.
//
// In shared mode the Writer holds no lifetime statements or pinned
// connection and does NOT own the *sql.DB (the daemon owns it), so Close
// is effectively a no-op — the owner closes the handle.
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
	if w.ownDB {
		return w.db.Close()
	}
	return nil
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
	// Resolve the connection + prepared statements for this batch.
	//
	//   - own-pool mode: the lifetime pinned conn + lifetime statements.
	//   - shared mode (NewWriterFromDB): acquire the pool's connection for
	//     the duration of this batch and prepare statements on it, then
	//     release on completion. The shared pool is MaxOpenConns=1, so this
	//     acquire blocks until any in-flight applier/maintenance write
	//     finishes — which is exactly the single-writer serialization we
	//     want (no two BEGIN IMMEDIATE can ever overlap).
	conn := w.conn
	stmts := &batchStmts{
		insert:      w.insertStmt,
		sess:        w.sessStmt,
		res:         w.resStmt,
		placeholder: w.placeholderStmt,
		upgrade:     w.upgradeStmt,
	}
	if w.shared {
		conn, err = w.db.Conn(ctx)
		if err != nil {
			return 0, fmt.Errorf("acquire shared conn: %w", err)
		}
		defer conn.Close() // release back to the pool
		stmts, err = prepareBatchStmts(ctx, conn)
		if err != nil {
			return 0, err
		}
		defer stmts.close()
	}

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
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, fmt.Errorf("begin immediate: %w", err)
	}
	// rollback is a no-op after a successful COMMIT; safe to call from defer.
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(context.Background(), "ROLLBACK") //nolint:errcheck
		}
	}()

	// Track sessions we've already upserted this batch so we don't
	// fire a redundant INSERT per signal.
	seen := map[string]bool{}
	// Cache of active work item per (session_id, agent_id) pair, keyed
	// internally by resolveFeatureID. Populated lazily so a batch touching
	// many agents issues at most one SELECT per distinct pair, regardless
	// of signal count.
	featureCache := map[string]string{}
	resObservedAt := time.Now().UnixMicro()

	// spanExists caches span_ids already present in otel_signals (within this
	// transaction) so we only query the DB once per distinct span_id per batch.
	spanExists := map[string]bool{}

	// agentIDByOwnSpan maps span_id -> the span's own native "agent_id"
	// attribute (feat-be696acc), built once up front from every signal in
	// this batch regardless of array order. A child span's parent is often
	// in the very same batch — sometimes later in the array than the child,
	// since OTel spans typically export on completion and children finish
	// before their parent — so resolveSignalAgentID's one-hop parent lookup
	// checks this map before falling back to an already-committed DB row.
	agentIDByOwnSpan := make(map[string]string, len(signals))
	for i := range signals {
		if signals[i].SpanID == "" {
			continue
		}
		if aid := rawAgentID(signals[i].RawAttrs); aid != "" {
			agentIDByOwnSpan[signals[i].SpanID] = aid
		}
	}

	for i := range signals {
		s := &signals[i]
		if s.SessionID == "" {
			// Drop signals without a session. OTel emissions always
			// carry session.id either on the resource or the signal;
			// a missing one means the adapter couldn't normalize.
			continue
		}
		if !shouldPersistSignal(s) {
			continue
		}
		if !seen[s.SessionID] {
			agent := string(harness)
			if _, err = stmts.sess.ExecContext(ctx, s.SessionID, agent); err != nil {
				return inserted, fmt.Errorf("sessions upsert: %w", err)
			}
			// Persist the resource attributes snapshot for this session.
			for k, v := range resourceAttrs {
				if sv, ok := valueString(v); ok {
					if _, err = stmts.res.ExecContext(ctx, s.SessionID, string(harness), k, sv, resObservedAt); err != nil {
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

		// Resolve this signal's own agent identity, then the work item that
		// agent has claimed (feat-be696acc). subagent_invocation spans are
		// resolved here, ahead of the placeholder-upgrade branch below,
		// because the re-attribution pass further down never touches their
		// parent_span (it explicitly skips CanonicalSubagent) — so it's
		// already final and safe to resolve early. Every other canonical is
		// re-resolved after re-attribution runs (see below), since
		// re-attribution can rewrite s.ParentSpan and the one-hop lookup
		// must see the corrected value, not the pre-correction one.
		var agentID, featureID string
		resolvedEarly := s.Kind == otel.KindSpan && s.CanonicalName == otel.CanonicalSubagent
		if resolvedEarly {
			agentID = resolveSignalAgentID(ctx, conn, s, agentIDByOwnSpan)
			featureID = resolveFeatureID(ctx, conn, s.SessionID, agentID, featureCache)
		}

		// Placeholder upgrade: if this signal is the real Agent/subagent_invocation
		// span, check whether a placeholder exists for the same span_id and update it
		// rather than inserting a duplicate. This transparently promotes the placeholder
		// written during orphan-span detection to a fully-attributed row.
		if s.Kind == otel.KindSpan && s.CanonicalName == otel.CanonicalSubagent && s.SpanID != "" {
			upgraded, upgradeErr := tryUpgradePlaceholder(ctx, stmts.upgrade, s, attrsJSON, successVal, featureID, agentID)
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
			if err2 := w.maybeCreatePlaceholder(ctx, conn, stmts.placeholder, s, resourceAttrs, spanExists, resObservedAt); err2 != nil {
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
			if newParent, reason := tryReattributeParent(ctx, conn, s, resourceAttrs); newParent != "" {
				log.Printf("reattribute: span=%s old_parent=%s new_parent=%s reason=%s",
					s.SpanID, s.ParentSpan, newParent, reason)
				s.ParentSpan = newParent
			}
		}

		// Everything except the early-resolved subagent_invocation branch
		// above resolves here, now that re-attribution has had its chance to
		// correct s.ParentSpan — see the comment on resolvedEarly.
		if !resolvedEarly {
			agentID = resolveSignalAgentID(ctx, conn, s, agentIDByOwnSpan)
			featureID = resolveFeatureID(ctx, conn, s.SessionID, agentID, featureCache)
		}

		res, execErr := stmts.insert.ExecContext(ctx,
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
			string(attrsJSON), nullStr(featureID), nullStr(agentID),
		)
		if execErr != nil {
			return inserted, fmt.Errorf("insert signal %s: %w", s.SignalID, execErr)
		}
		if n, rowsErr := res.RowsAffected(); rowsErr == nil {
			inserted += int(n)
		}
	}

	for sessionID := range seen {
		if err = pruneMetricSignals(ctx, conn, sessionID, metricRowsPerSessionLimit); err != nil {
			return inserted, fmt.Errorf("prune metric signals for session %s: %w", sessionID, err)
		}
	}

	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return inserted, fmt.Errorf("commit: %w", err)
	}
	committed = true
	return inserted, nil
}

func shouldPersistSignal(s *otel.UnifiedSignal) bool {
	if s.Kind == otel.KindMetric && s.CanonicalName == otel.CanonicalUnknown {
		return false
	}
	return true
}

func pruneMetricSignals(ctx context.Context, conn dbExecer, sessionID string, keep int) error {
	if keep <= 0 {
		_, err := conn.ExecContext(ctx,
			`DELETE FROM otel_signals WHERE session_id = ? AND kind = 'metric'`,
			sessionID,
		)
		return err
	}
	_, err := conn.ExecContext(ctx, `
		DELETE FROM otel_signals
		WHERE session_id = ?
		  AND kind = 'metric'
		  AND signal_id NOT IN (
			SELECT signal_id
			FROM otel_signals
			WHERE session_id = ? AND kind = 'metric'
			ORDER BY ts_micros DESC, created_at DESC, signal_id DESC
			LIMIT ?
		  )`,
		sessionID, sessionID, keep,
	)
	return err
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
	agentID string,
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
		string(attrsJSON), nullStr(featureID), nullStr(agentID),
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

// rawAgentID reads Claude Code's native "agent_id" span attribute straight
// off a signal's raw attributes (feat-be696acc). This is emitted directly by
// the harness's OTel SDK on claude_code.llm_request and claude_code.tool
// spans whenever the span belongs to a subagent or teammate — see
// observe/otel/adapter/claude.go, which copies OTLP attributes through
// unmodified. Distinct from the wipnote.agent_id RESOURCE attribute used
// elsewhere in this file for placeholder/re-attribution lookups: that one is
// wipnote's own hex identity propagated via CLAUDE_ENV_FILE (currently
// broken for Agent-Teams-style sessions — bug-190950e0); this one is
// harness-native, per-span, and does not depend on that propagation path.
func rawAgentID(attrs map[string]any) string {
	if attrs == nil {
		return ""
	}
	v, _ := attrs["agent_id"].(string)
	return v
}

// resolveSignalAgentID returns the agent that emitted s: its own native
// agent_id (rawAgentID) when present, else its immediate parent span's OWN
// native agent_id, else "" (root — no agent claimed this signal or its
// parent). Exactly one hop, and strictly one: the parent lookup reads the
// parent's raw attrs_json (its own native attribute), NEVER the parent's
// resolved/stored otel_signals.agent_id column — that column can itself hold
// a value the parent inherited from ITS parent, and reading it here would
// let inheritance cascade transitively across arbitrarily many generations,
// which is exactly the deep-tree walk this design deliberately avoids (see
// the primary-hypothesis research on feat-be696acc: parent chains are not
// reliably intact across older/other sessions). A child two levels below the
// nearest agent_id-bearing span is therefore correctly left unattributed
// (root), not silently inherited.
//
// The parent lookup checks agentIDByOwnSpan (this batch) before querying
// otel_signals, since a child's parent is frequently in the same batch —
// sometimes later in the array than the child, because spans typically
// export on completion and children finish before their enclosing parent.
func resolveSignalAgentID(ctx context.Context, conn dbExecer, s *otel.UnifiedSignal, agentIDByOwnSpan map[string]string) string {
	if aid := rawAgentID(s.RawAttrs); aid != "" {
		return aid
	}
	if s.ParentSpan == "" {
		return ""
	}
	if aid, ok := agentIDByOwnSpan[s.ParentSpan]; ok {
		return aid
	}
	var v sql.NullString
	_ = conn.QueryRowContext(ctx,
		`SELECT json_extract(attrs_json, '$.agent_id') FROM otel_signals WHERE span_id = ?`,
		s.ParentSpan,
	).Scan(&v)
	return v.String
}

// resolveFeatureID looks up the work item claimed by (sessionID, agentID) in
// active_work_items, falling back to the session's __root__ claim when the
// resolved agent has no claim of its own. That fallback is deliberate and
// currently load-bearing: until bug-190950e0 fixes WIPNOTE_AGENT_ID
// propagation for Agent-Teams-style sessions, every concurrent agent's own
// claim collapses to __root__ same as everyone else's, so this fallback
// reproduces today's (imperfect) behavior unchanged rather than silently
// dropping feature_id to NULL. Once that propagation gap is fixed and agents
// hold real per-agent claims, this same fallback still applies correctly for
// agents that genuinely never claimed a work item of their own.
//
// cache is keyed per (sessionID, agentID) so a batch spanning many
// concurrently active agents issues at most one SELECT per distinct pair.
func resolveFeatureID(ctx context.Context, conn dbExecer, sessionID, agentID string, cache map[string]string) string {
	lookupID := db.NormaliseAgentID(agentID)
	key := sessionID + "\x00" + lookupID
	if v, ok := cache[key]; ok {
		return v
	}
	var fid sql.NullString
	_ = conn.QueryRowContext(ctx,
		`SELECT work_item_id FROM active_work_items WHERE session_id = ? AND agent_id = ?`,
		sessionID, lookupID,
	).Scan(&fid)
	result := fid.String
	if result == "" && lookupID != db.AgentRootSentinel {
		result = resolveFeatureID(ctx, conn, sessionID, db.AgentRootSentinel, cache)
	}
	cache[key] = result
	return result
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
