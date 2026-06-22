package apply

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
)

// openTempWriterDB opens a writable, migrated DB on a temp file (the same
// handle kind the daemon owns) and creates a tiny scratch table the SQL
// envelope tests write into.
func openTempWriterDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "writer.db"))
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE kv (k TEXT PRIMARY KEY, n INTEGER)`); err != nil {
		t.Fatalf("create scratch table: %v", err)
	}
	return d
}

// TestDerivedOp_SQLEnvelope_RoundTrip encodes an OpTypeSQL op with bind args,
// decodes it, runs the resulting applier against a temp-file DB, and asserts
// the row was written with the correct (parameter-bound) values. This proves
// the {sql,args} envelope crosses Encode/Decode intact and the applier Execs
// the PARAMETERIZED statement (args bound, never interpolated).
func TestDerivedOp_SQLEnvelope_RoundTrip(t *testing.T) {
	wDB := openTempWriterDB(t)

	op := DerivedOp{
		Type: OpTypeSQL,
		SQL:  `INSERT INTO kv (k, n) VALUES (?, ?)`,
		Args: []any{"alpha", 42},
	}
	payload, err := Encode(op)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := Decode(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Type != OpTypeSQL || decoded.SQL != op.SQL {
		t.Fatalf("round-trip lost type/sql: got type=%q sql=%q", decoded.Type, decoded.SQL)
	}
	if len(decoded.Args) != 2 {
		t.Fatalf("round-trip lost args: got %d want 2 (%v)", len(decoded.Args), decoded.Args)
	}

	applier := NewApplier(wDB)
	writeOp, err := applier(daemon.Envelope{OpType: OpTypeSQL, Payload: payload})
	if err != nil {
		t.Fatalf("applier build: %v", err)
	}
	if err := writeOp(context.Background()); err != nil {
		t.Fatalf("apply OpTypeSQL: %v", err)
	}

	var n int
	if err := wDB.QueryRow(`SELECT n FROM kv WHERE k = ?`, "alpha").Scan(&n); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if n != 42 {
		t.Fatalf("row n = %d, want 42 (args must bind as parameters)", n)
	}
}

// TestDerivedOp_SQLEnvelope_Int64PrecisionRoundTrip proves a large int64 bind
// arg survives encode→decode→apply EXACTLY (roborev-473 finding 6). A plain
// json.Unmarshal would widen the JSON number to float64, truncating any integer
// above 2^53 before it reaches the SQLite INTEGER bind; Decode's UseNumber()+
// NormalizeArgs path preserves it. The fractional case asserts non-integral
// numbers still bind as float64.
func TestDerivedOp_SQLEnvelope_Int64PrecisionRoundTrip(t *testing.T) {
	wDB := openTempWriterDB(t)

	const bigInt = int64(1)<<53 + 1 // 9007199254740993 — NOT representable as float64
	if float64(bigInt) == float64(bigInt-1) {
		// Sanity: confirm the value genuinely loses precision through float64,
		// so this test would actually catch a regression.
		t.Logf("note: %d collapses under float64 — exactly what we must avoid", bigInt)
	}

	op := DerivedOp{
		Type: OpTypeSQL,
		SQL:  `INSERT INTO kv (k, n) VALUES (?, ?)`,
		Args: []any{"big", bigInt},
	}
	payload, err := Encode(op)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := Decode(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The decoded arg must be an int64 carrying the exact value (not a float64).
	gotArg, ok := decoded.Args[1].(int64)
	if !ok {
		t.Fatalf("decoded big int arg has type %T, want int64 (precision-preserving)", decoded.Args[1])
	}
	if gotArg != bigInt {
		t.Fatalf("decoded big int arg = %d, want %d", gotArg, bigInt)
	}

	applier := NewApplier(wDB)
	writeOp, err := applier(daemon.Envelope{OpType: OpTypeSQL, Payload: payload})
	if err != nil {
		t.Fatalf("applier build: %v", err)
	}
	if err := writeOp(context.Background()); err != nil {
		t.Fatalf("apply OpTypeSQL: %v", err)
	}

	var n int64
	if err := wDB.QueryRow(`SELECT n FROM kv WHERE k = ?`, "big").Scan(&n); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if n != bigInt {
		t.Fatalf("row n = %d, want %d (int64 precision must survive the SQL envelope)", n, bigInt)
	}

	// Fractional case: a non-integral number must bind as float64, not be
	// coerced to an integer.
	const frac = 3.5
	op2 := DerivedOp{Type: OpTypeSQL, SQL: `INSERT INTO kv (k, n) VALUES (?, ?)`, Args: []any{"frac", frac}}
	payload2, err := Encode(op2)
	if err != nil {
		t.Fatalf("encode frac: %v", err)
	}
	decoded2, err := Decode(payload2)
	if err != nil {
		t.Fatalf("decode frac: %v", err)
	}
	if f, ok := decoded2.Args[1].(float64); !ok || f != frac {
		t.Fatalf("decoded fractional arg = %#v (type %T), want float64(%v)", decoded2.Args[1], decoded2.Args[1], frac)
	}
}

// TestDerivedOp_SQLEnvelope_Float64Preserved proves an INTEGRAL float64 bind
// arg (1.0) round-trips as float64 and binds as REAL — it is NOT coerced to an
// integer by the envelope (roborev-478 finding 3). It also proves a large int64
// still round-trips EXACTLY in the same envelope, and that the two yield
// DISTINCT op_ids (so float64(1.0) and int64(1) are never wrongly deduped).
func TestDerivedOp_SQLEnvelope_Float64Preserved(t *testing.T) {
	wDB := openTempWriterDB(t)
	// A REAL-typed scratch column so we can observe the stored affinity.
	if _, err := wDB.Exec(`CREATE TABLE rk (k TEXT PRIMARY KEY, r)`); err != nil {
		t.Fatalf("create rk: %v", err)
	}

	const one = float64(1.0)
	op := DerivedOp{Type: OpTypeSQL, SQL: `INSERT INTO rk (k, r) VALUES (?, ?)`, Args: []any{"one", one}}
	payload, err := Encode(op)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := Decode(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The decoded arg must STAY a float64 (not collapse to int64).
	f, ok := decoded.Args[1].(float64)
	if !ok {
		t.Fatalf("decoded float arg has type %T, want float64 (must not coerce 1.0 to int)", decoded.Args[1])
	}
	if f != one {
		t.Fatalf("decoded float arg = %v, want %v", f, one)
	}

	applier := NewApplier(wDB)
	writeOp, err := applier(daemon.Envelope{OpType: OpTypeSQL, Payload: payload})
	if err != nil {
		t.Fatalf("applier build: %v", err)
	}
	if err := writeOp(context.Background()); err != nil {
		t.Fatalf("apply OpTypeSQL: %v", err)
	}
	// typeof() must report 'real' — proving the value bound as a float, not an int.
	var typ string
	if err := wDB.QueryRow(`SELECT typeof(r) FROM rk WHERE k = ?`, "one").Scan(&typ); err != nil {
		t.Fatalf("read back typeof: %v", err)
	}
	if typ != "real" {
		t.Fatalf("stored typeof(r) = %q, want \"real\" (float64 must bind as REAL, not INTEGER)", typ)
	}

	// A large int64 still round-trips exact in the same envelope.
	const bigInt = int64(1)<<53 + 1
	op2 := DerivedOp{Type: OpTypeSQL, SQL: `INSERT INTO rk (k, r) VALUES (?, ?)`, Args: []any{"big", bigInt}}
	payload2, _ := Encode(op2)
	decoded2, err := Decode(payload2)
	if err != nil {
		t.Fatalf("decode big: %v", err)
	}
	if got, ok := decoded2.Args[1].(int64); !ok || got != bigInt {
		t.Fatalf("decoded big int arg = %#v (type %T), want int64(%d)", decoded2.Args[1], decoded2.Args[1], bigInt)
	}

	// float64(1.0) and int64(1) must produce DISTINCT op_ids.
	const sql = `INSERT INTO rk (k, r) VALUES (?, ?)`
	if sqlOpID(sql, "x", float64(1.0)) == sqlOpID(sql, "x", int64(1)) {
		t.Fatal("sqlOpID(.., float64(1.0)) collides with sqlOpID(.., int64(1)) — REAL vs INTEGER not distinguished")
	}
}

// TestDerivedOp_SQLEnvelope_EmptySQLErrors asserts a malformed OpTypeSQL (no
// statement) yields an applier error rather than a silent no-op.
func TestDerivedOp_SQLEnvelope_EmptySQLErrors(t *testing.T) {
	payload, _ := Encode(DerivedOp{Type: OpTypeSQL, SQL: ""})
	if _, err := NewApplier(nil)(daemon.Envelope{OpType: OpTypeSQL, Payload: payload}); err == nil {
		t.Fatal("OpTypeSQL with empty SQL must error")
	}
}

// TestRouteSQL_NoDaemon_ReturnsFalse asserts that with no daemon reachable
// (and auto-spawn forbidden) RouteSQL returns false promptly — no error, no
// panic — so the caller falls back to its direct write.
func TestRouteSQL_NoDaemon_ReturnsFalse(t *testing.T) {
	t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1") // deterministic: no spawn → straight miss
	projectRoot := t.TempDir()              // no writer.sock here

	start := time.Now()
	applied := RouteSQL(projectRoot, `INSERT INTO kv (k, n) VALUES (?, ?)`, "x", 1)
	elapsed := time.Since(start)

	if applied {
		t.Fatal("RouteSQL returned true with no daemon")
	}
	if elapsed > CLISubmitBudget+2*time.Second {
		t.Fatalf("RouteSQL took %v, exceeds bounded budget (must not hang)", elapsed)
	}
	// Empty projectRoot must also be a safe false (no panic).
	if RouteSQL("", `INSERT INTO kv (k, n) VALUES (?, ?)`, "y", 2) {
		t.Fatal("RouteSQL(\"\") returned true")
	}
}

// TestRouteSQLAsync_NoDaemon_ReturnsFalse asserts the enqueue-only helper also
// degrades to false (no panic) when the daemon cannot be reached.
func TestRouteSQLAsync_NoDaemon_ReturnsFalse(t *testing.T) {
	t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1")
	projectRoot := t.TempDir()

	start := time.Now()
	if RouteSQLAsync(projectRoot, `INSERT INTO kv (k, n) VALUES (?, ?)`, "x", 1) {
		t.Fatal("RouteSQLAsync returned true with no daemon")
	}
	if elapsed := time.Since(start); elapsed > AsyncEnqueueBudget+2*time.Second {
		t.Fatalf("RouteSQLAsync took %v, exceeds bounded budget (must not hang)", elapsed)
	}
}

// startSQLListener brings up a writer daemon bound to a temp project root with
// the SQL-capable applier over a temp-file DB (with a scratch kv table). The
// returned gate, when non-nil and held, lets the test wedge the single-writer
// worker to prove enqueue-only ack timing. Pass a nil gate for an unblocked
// writer.
func startSQLListener(t *testing.T, gate <-chan struct{}) (wDB *sql.DB, projectRoot, sock string) {
	t.Helper()
	projectRoot = t.TempDir()
	var err error
	wDB, err = db.Open(filepath.Join(projectRoot, "writer.db"))
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	t.Cleanup(func() { wDB.Close() })
	if _, err := wDB.Exec(`CREATE TABLE kv (k TEXT PRIMARY KEY, n INTEGER)`); err != nil {
		t.Fatalf("create scratch table: %v", err)
	}

	q := writequeue.New(writequeue.Config{Capacity: 16})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	t.Cleanup(func() { q.Stop(time.Second) })

	if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	sock = daemon.SocketPath(projectRoot)

	// Wrap the real SQL applier so each op optionally blocks on the gate,
	// wedging the single-writer worker to expose ack-on-apply vs ack-on-enqueue.
	base := NewApplier(wDB)
	wrapped := func(env daemon.Envelope) (writequeue.WriteOp, error) {
		op, err := base(env)
		if err != nil {
			return nil, err
		}
		if gate == nil {
			return op, nil
		}
		return func(ctx context.Context) error {
			<-gate
			return op(ctx)
		}, nil
	}

	ln, err := daemon.NewListener(daemon.ListenerConfig{SocketPath: sock, Queue: q, Applier: wrapped})
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ln.Serve(ctx) }()
	t.Cleanup(func() { ln.Close() })
	waitForSocket(t, sock)
	return wDB, projectRoot, sock
}

// TestRouteSQLAsync_AcksOnEnqueue_NotApply wedges the daemon's single writer
// with a blocked op, then submits a second op via RouteSQLAsync. The helper
// must return true in WELL under the applied-ack (~2s) budget — proving it acks
// on enqueue, not on apply. The applied-ack RouteSQL would block on the wedged
// writer until released.
func TestRouteSQLAsync_AcksOnEnqueue_NotApply(t *testing.T) {
	gate := make(chan struct{})
	_, projectRoot, _ := startSQLListener(t, gate)

	// Op 1: enters the worker and blocks on the gate, occupying the writer.
	if !RouteSQLAsync(projectRoot, `INSERT INTO kv (k, n) VALUES (?, ?)`, "occupy", 1) {
		close(gate)
		t.Fatal("RouteSQLAsync(occupy) returned false with a live daemon")
	}

	// Op 2: the writer is now wedged applying op 1, so op 2 is enqueued-only.
	// Must return true promptly (enqueue-only), nowhere near the applied budget.
	start := time.Now()
	ok := RouteSQLAsync(projectRoot, `INSERT INTO kv (k, n) VALUES (?, ?)`, "while-busy", 2)
	elapsed := time.Since(start)
	if !ok {
		close(gate)
		t.Fatal("RouteSQLAsync(while-busy) returned false; enqueue should still succeed")
	}
	if elapsed >= CLISubmitBudget {
		close(gate)
		t.Fatalf("RouteSQLAsync took %v (>= applied-ack budget %v) — it waited for apply, not enqueue", elapsed, CLISubmitBudget)
	}

	// Release the writer; both ops should eventually apply (FIFO).
	close(gate)
}

// TestRouteSQLAsync_LiveDaemon_Applies confirms the enqueue-only path still
// ultimately APPLIES through the single writer when it is NOT wedged: the row
// lands in the DB shortly after the (immediate) enqueue ack.
func TestRouteSQLAsync_LiveDaemon_Applies(t *testing.T) {
	wDB, projectRoot, _ := startSQLListener(t, nil)

	if !RouteSQLAsync(projectRoot, `INSERT INTO kv (k, n) VALUES (?, ?)`, "live", 7) {
		t.Fatal("RouteSQLAsync returned false with a live unblocked daemon")
	}
	// The ack is enqueue-only, so poll briefly for the async apply to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := wDB.QueryRow(`SELECT n FROM kv WHERE k = ?`, "live").Scan(&n); err == nil {
			if n != 7 {
				t.Fatalf("row n = %d, want 7", n)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("RouteSQLAsync row never applied within deadline")
}

// TestSQLOpID_TypeTagged proves sqlOpID hashes a TYPE-TAGGED serialization of
// (sql, args) so cross-type values cannot collide (roborev-473 finding 7). The
// old %v rendering hashed the string "1" and the int 1 identically, letting a
// later distinct SQL op be wrongly deduped against an earlier one and dropped.
func TestSQLOpID_TypeTagged(t *testing.T) {
	const sql1 = `INSERT INTO kv (k, n) VALUES (?, ?)`
	const sql2 = `UPDATE kv SET n = ? WHERE k = ?`

	// String "1" must NOT collide with int 1.
	if sqlOpID(sql1, "1") == sqlOpID(sql1, 1) {
		t.Fatal(`sqlOpID(sql, "1") collides with sqlOpID(sql, 1) — args not type-tagged`)
	}
	// Different statement, same arg → distinct key.
	if sqlOpID(sql1, 1) == sqlOpID(sql2, 1) {
		t.Fatal("sqlOpID(sql1, 1) collides with sqlOpID(sql2, 1) — statement not in key")
	}
	// Determinism: same (sql, args) yields the same key across calls.
	if sqlOpID(sql1, "a", 2, true) != sqlOpID(sql1, "a", 2, true) {
		t.Fatal("sqlOpID not deterministic for identical (sql, args)")
	}
	// int vs int64 of the SAME value collapse to the same key (both bind
	// identically and JSON-encode as the same number) — so a retry that happens
	// to widen an int to int64 still dedups.
	if sqlOpID(sql1, 1) != sqlOpID(sql1, int64(1)) {
		t.Fatal("sqlOpID(sql, int(1)) != sqlOpID(sql, int64(1)) — same value must dedup")
	}
	// A large int64 (beyond float64 exactness) still produces a stable key —
	// guarding the finding-6 normalization path through the op_id too.
	big := int64(1)<<53 + 1
	if sqlOpID(sql1, big) != sqlOpID(sql1, big) {
		t.Fatal("sqlOpID not stable for a large int64 arg")
	}
	if sqlOpID(sql1, big) == sqlOpID(sql1, big-1) {
		t.Fatal("sqlOpID collides for distinct large int64 args — precision lost in key")
	}
}

// TestRouteSQL_LiveDaemon_Applies confirms the APPLIED-ack helper returns true
// only after the write has committed on the daemon handle.
func TestRouteSQL_LiveDaemon_Applies(t *testing.T) {
	wDB, projectRoot, _ := startSQLListener(t, nil)

	if !RouteSQL(projectRoot, `INSERT INTO kv (k, n) VALUES (?, ?)`, "sync-live", 9) {
		t.Fatal("RouteSQL returned false with a live daemon")
	}
	// Applied-ack: the row must already be present (no polling needed).
	var n int
	if err := wDB.QueryRow(`SELECT n FROM kv WHERE k = ?`, "sync-live").Scan(&n); err != nil {
		t.Fatalf("row missing after applied-ack RouteSQL: %v", err)
	}
	if n != 9 {
		t.Fatalf("row n = %d, want 9", n)
	}
}
