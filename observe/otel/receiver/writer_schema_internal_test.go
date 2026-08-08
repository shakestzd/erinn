package receiver

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

// sqlInsertSignalColumns extracts sqlInsertSignal's own column list so this
// test can compare it against requiredOtelSignalsColumns without hand-typing
// the column list a third time.
func sqlInsertSignalColumns(t *testing.T) []string {
	t.Helper()
	open := strings.Index(sqlInsertSignal, "(")
	closeIdx := strings.Index(sqlInsertSignal, ") VALUES")
	if open < 0 || closeIdx < 0 || closeIdx <= open {
		t.Fatalf("could not locate column list in sqlInsertSignal")
	}
	raw := sqlInsertSignal[open+1 : closeIdx]
	fields := strings.Split(raw, ",")
	cols := make([]string, 0, len(fields))
	ws := regexp.MustCompile(`\s+`)
	for _, f := range fields {
		f = ws.ReplaceAllString(strings.TrimSpace(f), "")
		if f != "" {
			cols = append(cols, f)
		}
	}
	return cols
}

// TestRequiredOtelSignalsColumnsMatchInsert keeps requiredOtelSignalsColumns
// (verifyOtelSignalsSchema's fail-loudly check, bug-286ce8f7) honest against
// sqlInsertSignal's actual column list. Without this, the two lists could
// silently drift apart — a future column added to the INSERT and forgotten
// here would defeat the whole point of the startup check: it would go on
// verifying a schema shape that is no longer what the writer actually needs.
func TestRequiredOtelSignalsColumnsMatchInsert(t *testing.T) {
	insertCols := sqlInsertSignalColumns(t)

	want := append([]string(nil), insertCols...)
	got := append([]string(nil), requiredOtelSignalsColumns...)
	sort.Strings(want)
	sort.Strings(got)

	if len(want) != len(got) {
		t.Fatalf("sqlInsertSignal has %d columns, requiredOtelSignalsColumns has %d — they must match exactly.\ninsert: %v\nrequired: %v",
			len(want), len(got), insertCols, requiredOtelSignalsColumns)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("column mismatch at sorted position %d: sqlInsertSignal has %q, requiredOtelSignalsColumns has %q.\ninsert: %v\nrequired: %v",
				i, want[i], got[i], insertCols, requiredOtelSignalsColumns)
		}
	}
}

// TestVerifyOtelSignalsSchema_MissingColumn confirms the fail-loudly check
// names the specific missing column rather than letting the writer start
// and fail row by row later — the exact defect bug-286ce8f7 found live
// (otel_signals.agent_id absent, thousands of swallowed per-insert errors).
func TestVerifyOtelSignalsSchema_MissingColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "schema_check.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if err := verifyOtelSignalsSchema(database); err != nil {
		t.Fatalf("verifyOtelSignalsSchema on a fully-migrated DB: %v", err)
	}

	if _, err := database.Exec(`DROP INDEX IF EXISTS idx_otel_agent_ts`); err != nil {
		t.Fatalf("drop idx_otel_agent_ts: %v", err)
	}
	if _, err := database.Exec(`ALTER TABLE otel_signals DROP COLUMN agent_id`); err != nil {
		t.Fatalf("drop agent_id: %v", err)
	}

	err = verifyOtelSignalsSchema(database)
	if err == nil {
		t.Fatal("verifyOtelSignalsSchema returned nil after agent_id was dropped, want an error naming the missing column")
	}
	if !strings.Contains(err.Error(), "agent_id") {
		t.Errorf("error = %q, want it to name the missing column %q", err.Error(), "agent_id")
	}
}
