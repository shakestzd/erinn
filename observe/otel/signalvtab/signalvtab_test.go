package signalvtab

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// writeShard creates <root>/<id>/events.ndjson containing lines verbatim.
func writeShard(t *testing.T, root, id string, lines []string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, ShardFile), []byte(body), 0o644); err != nil {
		t.Fatalf("write shard %s: %v", id, err)
	}
}

// signalJSON builds one NDJSON line in the shape observe/otel/sink/ndjson writes.
func signalJSON(session, signalID, kind, canonical string, durationMs int64) string {
	return fmt.Sprintf(
		`{"kind":%q,"harness":"claude_code","ts":"2026-08-09T10:00:00.000Z","signal_id":%q,"session_id":%q,"canonical":%q,"native":%q,"duration_ms":%d,"attrs":{"k":"v"}}`,
		kind, signalID, session, canonical, canonical, durationMs)
}

// corpus lays out three shards. Session "sess-A" deliberately appears in two
// of them, and shard "shard-2" deliberately holds two different sessions —
// both shapes are present in the real corpus.
func corpus(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeShard(t, root, "shard-1", []string{
		signalJSON("sess-A", "a1", "span", "tool.execution", 10),
		signalJSON("sess-A", "a2", "span", "tool.execution", 20),
		signalJSON("sess-A", "a3", "metric", "tokens", 0),
	})
	writeShard(t, root, "shard-2", []string{
		signalJSON("sess-A", "a4", "span", "tool.execution", 30),
		signalJSON("sess-B", "b1", "span", "llm.request", 40),
	})
	writeShard(t, root, "shard-3", []string{
		signalJSON("sess-C", "c1", "log", "tool_result", 0),
	})
	// A session directory with no shard file: the sessions tree also holds
	// directories keyed by harness session id that carry only state files.
	if err := os.MkdirAll(filepath.Join(root, "no-shard-here"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func openCorpus(t *testing.T, root string) (*sql.DB, *Module) {
	t.Helper()
	db, mod, err := OpenIsolated(root)
	if err != nil {
		t.Fatalf("OpenIsolated: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, mod
}

func queryStrings(t *testing.T, db *sql.DB, q string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// TestShardPushdownOpensExactlyOneFile is the point of the whole exercise: a
// shard-scoped query must not scan the directory.
//
// A full scan returns the same rows, so row equality alone would pass with
// pushdown removed. The file-open counter is what makes this assertion real —
// see TestShardPushdownAssertionIsNotVacuous, which runs the identical query
// with pushdown disabled and shows the counter is the only thing that moves.
func TestShardPushdownOpensExactlyOneFile(t *testing.T) {
	root := corpus(t)
	db, mod := openCorpus(t, root)

	mod.Stats().Reset()
	got := queryStrings(t, db, `SELECT signal_id FROM signals WHERE shard = ? ORDER BY signal_id`, "shard-2")

	want := []string{"a4", "b1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rows = %v, want %v", got, want)
	}
	st := mod.Stats().Snapshot()
	if st.FilesOpened != 1 {
		t.Errorf("FilesOpened = %d, want 1 — the shard constraint did not select a single file", st.FilesOpened)
	}
	if st.LinesRead != 2 {
		t.Errorf("LinesRead = %d, want 2 — lines outside the selected shard were read", st.LinesRead)
	}
}

// TestShardPushdownAssertionIsNotVacuous proves the assertion above depends on
// pushdown. With pushdown disabled the same query returns byte-identical rows
// (SQLite re-applies the WHERE clause itself) while opening every shard — so a
// row-only assertion would have passed on a table that pushes down nothing.
func TestShardPushdownAssertionIsNotVacuous(t *testing.T) {
	root := corpus(t)
	db, mod := openCorpus(t, root)

	mod.Stats().Reset()
	withPushdown := queryStrings(t, db, `SELECT signal_id FROM signals WHERE shard = ? ORDER BY signal_id`, "shard-2")
	pushed := mod.Stats().Snapshot()

	mod.SetPushdownDisabled(true)
	mod.Stats().Reset()
	withoutPushdown := queryStrings(t, db, `SELECT signal_id FROM signals WHERE shard = ? ORDER BY signal_id`, "shard-2")
	unpushed := mod.Stats().Snapshot()
	mod.SetPushdownDisabled(false)

	if strings.Join(withPushdown, ",") != strings.Join(withoutPushdown, ",") {
		t.Fatalf("rows differ with and without pushdown: %v vs %v — pushdown must not change results, only work",
			withPushdown, withoutPushdown)
	}
	if pushed.FilesOpened != 1 {
		t.Errorf("with pushdown: FilesOpened = %d, want 1", pushed.FilesOpened)
	}
	if unpushed.FilesOpened != 3 {
		t.Errorf("without pushdown: FilesOpened = %d, want 3 — if this is 1 the pushdown switch is not doing anything and the test above proves nothing",
			unpushed.FilesOpened)
	}
}

// TestMalformedLineDoesNotAbortScan: a shard is an append-only log that a
// crash can truncate mid-line. One bad line must not cost the rest of the
// file, and must not disappear without a trace.
func TestMalformedLineDoesNotAbortScan(t *testing.T) {
	root := t.TempDir()
	writeShard(t, root, "shard-1", []string{
		signalJSON("sess-A", "a1", "span", "tool.execution", 10),
		`{"kind":"span","signal_id":"broken","session_id":"sess-A",`, // truncated mid-object
		signalJSON("sess-A", "a3", "span", "tool.execution", 30),
	})
	db, mod := openCorpus(t, root)

	var reported []string
	mod.SetMalformedHandler(func(path string, line int, err error) {
		reported = append(reported, fmt.Sprintf("%s:%d", filepath.Base(filepath.Dir(path)), line))
	})

	mod.Stats().Reset()
	got := queryStrings(t, db, `SELECT signal_id FROM signals ORDER BY signal_id`)

	want := []string{"a1", "a3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rows = %v, want %v — the scan stopped at the malformed line instead of skipping it", got, want)
	}
	st := mod.Stats().Snapshot()
	if st.LinesMalformed != 1 {
		t.Errorf("LinesMalformed = %d, want 1", st.LinesMalformed)
	}
	if len(reported) != 1 || reported[0] != "shard-1:2" {
		t.Errorf("malformed handler saw %v, want [shard-1:2] — the skip was silent", reported)
	}
}

// TestSessionIDSpansShardsAndShardHoldsSessions pins the layout fact that
// makes session_id unusable as a file selector: the shard directory is named
// with the wipnote session id, while session_id on each line is the harness's
// own id. They are different identifiers with a many-to-many relationship.
func TestSessionIDSpansShardsAndShardHoldsSessions(t *testing.T) {
	root := corpus(t)
	db, mod := openCorpus(t, root)

	shards := queryStrings(t, db, `SELECT DISTINCT shard FROM signals WHERE session_id = ? ORDER BY shard`, "sess-A")
	if strings.Join(shards, ",") != "shard-1,shard-2" {
		t.Errorf("session sess-A found in shards %v, want [shard-1 shard-2]", shards)
	}

	sessions := queryStrings(t, db, `SELECT DISTINCT session_id FROM signals WHERE shard = ? ORDER BY session_id`, "shard-2")
	if strings.Join(sessions, ",") != "sess-A,sess-B" {
		t.Errorf("shard-2 holds sessions %v, want [sess-A sess-B]", sessions)
	}

	// The session filter is a row prefilter, not a file selector: every
	// shard is still opened.
	mod.Stats().Reset()
	ids := queryStrings(t, db, `SELECT signal_id FROM signals WHERE session_id = ? ORDER BY signal_id`, "sess-B")
	if strings.Join(ids, ",") != "b1" {
		t.Errorf("rows = %v, want [b1]", ids)
	}
	st := mod.Stats().Snapshot()
	if st.FilesOpened != 3 {
		t.Errorf("FilesOpened = %d, want 3 — session_id cannot select files", st.FilesOpened)
	}
	if st.RowsPrefiltered != 5 {
		t.Errorf("RowsPrefiltered = %d, want 5 — non-matching lines should be rejected before JSON decoding", st.RowsPrefiltered)
	}
	if st.RowsEmitted != 1 {
		t.Errorf("RowsEmitted = %d, want 1", st.RowsEmitted)
	}
}

// TestLineLongerThanScannerLimit guards the trap the prototype was one
// attribute bag away from hitting: bufio.Scanner's default 64KiB token limit.
// The largest line in the corpus measured for this work was 63,782 bytes.
func TestLineLongerThanScannerLimit(t *testing.T) {
	root := t.TempDir()
	// Sized from the read buffer so the line must be assembled across several
	// reads no matter how readBufSize is later tuned.
	big := strings.Repeat("x", readBufSize*2)
	line := fmt.Sprintf(
		`{"kind":"span","harness":"claude_code","ts":"2026-08-09T10:00:00Z","signal_id":"big","session_id":"sess-A","canonical":"tool.execution","native":"tool.execution","attrs":{"blob":%q}}`,
		big)
	writeShard(t, root, "shard-1", []string{
		signalJSON("sess-A", "a1", "span", "tool.execution", 10),
		line,
		signalJSON("sess-A", "a3", "span", "tool.execution", 30),
	})
	db, mod := openCorpus(t, root)

	mod.Stats().Reset()
	got := queryStrings(t, db, `SELECT signal_id FROM signals ORDER BY signal_id`)
	if strings.Join(got, ",") != "a1,a3,big" {
		t.Fatalf("rows = %v, want [a1 a3 big] — a long line broke the scan", got)
	}
	if n := mod.Stats().Snapshot().LinesMalformed; n != 0 {
		t.Errorf("LinesMalformed = %d, want 0 — the long line was mangled, not just read", n)
	}

	// And the oversized attribute bag survives intact.
	attrs := queryStrings(t, db, `SELECT attrs_json FROM signals WHERE signal_id = 'big'`)
	if len(attrs) != 1 || !strings.Contains(attrs[0], big) {
		t.Errorf("attrs_json for the long line did not round-trip (len %d)", len(attrs[0]))
	}
}

// TestModuleArgumentQuotesAreStripped: SQLite hands module arguments to
// xCreate as raw text with the caller's quote characters still attached.
func TestModuleArgumentQuotesAreStripped(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`"/a/b"`, "/a/b"},
		{`'/a/b'`, "/a/b"},
		{"`/a/b`", "/a/b"},
		{`/a/b`, "/a/b"},
		{`  "/a/b"  `, "/a/b"},
		{`"`, `"`},
		{``, ``},
	} {
		if got := unquoteArg(tc.in); got != tc.want {
			t.Errorf("unquoteArg(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// End to end: a real path reaches the table through a quoted argument.
	root := corpus(t)
	db, _ := openCorpus(t, root)
	if got := queryStrings(t, db, `SELECT COUNT(*) FROM signals`); len(got) != 1 || got[0] != "6" {
		t.Errorf("COUNT(*) = %v, want [6] — the quoted directory argument did not resolve", got)
	}
}

// TestRegisterAfterConnectionIsTooLate documents the registration-order trap
// in executable form: a Ping opens a pooled connection, and a module
// registered afterwards is absent from it.
func TestRegisterAfterConnectionIsTooLate(t *testing.T) {
	root := corpus(t)

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// The mistake: touch the pool first.
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}

	name := "wipnote_signals_toolate"
	if err := RegisterAs(db, name, NewModule("")); err != nil {
		t.Fatalf("RegisterAs: %v", err)
	}
	_, err = db.Exec(fmt.Sprintf("CREATE VIRTUAL TABLE signals USING %s(%q)", name, root))
	if err == nil {
		t.Fatal("CREATE VIRTUAL TABLE succeeded after a pre-registration Ping; " +
			"if the driver has changed to install modules on existing connections, Open's ordering comment is stale")
	}
	if !strings.Contains(err.Error(), "no such module") {
		t.Errorf("error = %v, want it to mention \"no such module\"", err)
	}
}

// TestMissingShardIsSkipped: shards are live files that retention and
// archival remove underneath a running query.
func TestMissingShardIsSkipped(t *testing.T) {
	root := corpus(t)
	db, mod := openCorpus(t, root)

	mod.Stats().Reset()
	got := queryStrings(t, db, `SELECT signal_id FROM signals WHERE shard = ?`, "shard-does-not-exist")
	if len(got) != 0 {
		t.Errorf("rows = %v, want none", got)
	}
	st := mod.Stats().Snapshot()
	if st.FilesOpened != 0 || st.FilesMissing != 1 {
		t.Errorf("FilesOpened=%d FilesMissing=%d, want 0 and 1", st.FilesOpened, st.FilesMissing)
	}
}

// TestNullAndTypedColumns: absent optional fields must read as NULL, not as
// zero, so ported queries keep the semantics otel_signals gave them.
func TestNullAndTypedColumns(t *testing.T) {
	root := t.TempDir()
	writeShard(t, root, "shard-1", []string{
		`{"kind":"span","harness":"claude_code","ts":"2026-08-09T10:00:00.5Z","signal_id":"s1","session_id":"sess-A","canonical":"tool.execution","native":"tool.execution","duration_ms":42,"success":true,"cost_usd":0.25,"tokens_input":7,"attrs":{"k":"v"},"resource_attrs":{"service.name":"claude-code"}}`,
		`{"kind":"log","harness":"claude_code","ts":"2026-08-09T10:00:01Z","signal_id":"s2","session_id":"sess-A","canonical":"tool_result","native":"tool_result"}`,
	})
	db, _ := openCorpus(t, root)

	var (
		durS1, tokS1           sql.NullInt64
		costS1                 sql.NullFloat64
		okS1                   sql.NullBool
		attrsS1, resS1         sql.NullString
		tsMicrosS1             sql.NullInt64
		durS2, tokS2           sql.NullInt64
		costS2                 sql.NullFloat64
		okS2                   sql.NullBool
		attrsS2, resS2, toolS2 sql.NullString
		tsMicrosS2             sql.NullInt64
	)
	row := db.QueryRow(`SELECT duration_ms, tokens_in, cost_usd, success, attrs_json, resource_attrs_json, ts_micros FROM signals WHERE signal_id='s1'`)
	if err := row.Scan(&durS1, &tokS1, &costS1, &okS1, &attrsS1, &resS1, &tsMicrosS1); err != nil {
		t.Fatalf("scan s1: %v", err)
	}
	if !durS1.Valid || durS1.Int64 != 42 {
		t.Errorf("s1 duration_ms = %v, want 42", durS1)
	}
	if !tokS1.Valid || tokS1.Int64 != 7 {
		t.Errorf("s1 tokens_in = %v, want 7", tokS1)
	}
	if !costS1.Valid || costS1.Float64 != 0.25 {
		t.Errorf("s1 cost_usd = %v, want 0.25", costS1)
	}
	if !okS1.Valid || !okS1.Bool {
		t.Errorf("s1 success = %v, want true", okS1)
	}
	if !attrsS1.Valid || !strings.Contains(attrsS1.String, `"k":"v"`) {
		t.Errorf("s1 attrs_json = %v", attrsS1)
	}
	if !resS1.Valid || !strings.Contains(resS1.String, "claude-code") {
		t.Errorf("s1 resource_attrs_json = %v", resS1)
	}
	// 2026-08-09T10:00:00.5Z
	if !tsMicrosS1.Valid || tsMicrosS1.Int64%1_000_000 != 500_000 {
		t.Errorf("s1 ts_micros = %v, want sub-second precision preserved", tsMicrosS1)
	}

	row = db.QueryRow(`SELECT duration_ms, tokens_in, cost_usd, success, attrs_json, resource_attrs_json, tool_name, ts_micros FROM signals WHERE signal_id='s2'`)
	if err := row.Scan(&durS2, &tokS2, &costS2, &okS2, &attrsS2, &resS2, &toolS2, &tsMicrosS2); err != nil {
		t.Fatalf("scan s2: %v", err)
	}
	for name, v := range map[string]bool{
		"duration_ms": durS2.Valid, "tokens_in": tokS2.Valid, "cost_usd": costS2.Valid,
		"success": okS2.Valid, "attrs_json": attrsS2.Valid, "resource_attrs_json": resS2.Valid,
		"tool_name": toolS2.Valid,
	} {
		if v {
			t.Errorf("s2 %s is non-NULL; absent fields must read as NULL, not zero", name)
		}
	}
	if !tsMicrosS2.Valid {
		t.Errorf("s2 ts_micros is NULL but ts was present")
	}
}

// TestColumnPruningKeepsResults: the ColUsed-driven decode must be an
// optimisation only. Whether or not attrs are decoded, the other columns are
// identical, and asking for attrs still yields them.
func TestColumnPruningKeepsResults(t *testing.T) {
	root := corpus(t)
	db, _ := openCorpus(t, root)

	lean := queryStrings(t, db, `SELECT signal_id FROM signals ORDER BY signal_id`)
	fat := queryStrings(t, db, `SELECT signal_id FROM signals WHERE attrs_json IS NOT NULL ORDER BY signal_id`)
	if strings.Join(lean, ",") != strings.Join(fat, ",") {
		t.Errorf("pruned scan returned %v but attrs-carrying scan returned %v", lean, fat)
	}

	// And the bit maths behind the pruning decision.
	if (plan{colUsed: 0}).needsAttrs() {
		t.Error("needsAttrs() true for a query that reads no columns")
	}
	if (plan{colUsed: 1 << ColSignalID}).needsAttrs() {
		t.Error("needsAttrs() true for a query that only reads signal_id")
	}
	if !(plan{colUsed: 1 << ColAttrsJSON}).needsAttrs() {
		t.Error("needsAttrs() false when attrs_json is selected")
	}
	if !(plan{colUsed: 1 << ColResourceAttrsJSON}).needsAttrs() {
		t.Error("needsAttrs() false when resource_attrs_json is selected")
	}
}

// TestAggregateAcrossAllShards is the dashboard's shape: no filter, group by.
func TestAggregateAcrossAllShards(t *testing.T) {
	root := corpus(t)
	db, mod := openCorpus(t, root)

	mod.Stats().Reset()
	rows, err := db.Query(`SELECT kind, COUNT(*), SUM(duration_ms) FROM signals GROUP BY kind ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][2]int64{}
	for rows.Next() {
		var kind string
		var n int64
		var sum sql.NullInt64
		if err := rows.Scan(&kind, &n, &sum); err != nil {
			t.Fatal(err)
		}
		got[kind] = [2]int64{n, sum.Int64}
	}
	want := map[string][2]int64{
		"log":    {1, 0},
		"metric": {1, 0},
		"span":   {4, 100},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("kind %q = %v, want %v", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d kinds, want %d: %v", len(got), len(want), got)
	}
	if n := mod.Stats().Snapshot().FilesOpened; n != 3 {
		t.Errorf("FilesOpened = %d, want 3 for an unfiltered aggregate", n)
	}
}

// TestReadOnly: the table declares no xUpdate, so writes must be refused
// rather than silently dropped.
func TestReadOnly(t *testing.T) {
	root := corpus(t)
	db, _ := openCorpus(t, root)
	if _, err := db.Exec(`DELETE FROM signals WHERE shard = 'shard-1'`); err == nil {
		t.Error("DELETE succeeded against a read-only virtual table")
	}
	if _, err := db.Exec(`INSERT INTO signals (shard) VALUES ('x')`); err == nil {
		t.Error("INSERT succeeded against a read-only virtual table")
	}
}
