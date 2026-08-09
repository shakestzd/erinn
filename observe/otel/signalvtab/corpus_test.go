package signalvtab

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

// TestCorpusMeasurements runs the Phase 1 query shapes against a real shard
// directory and reports timings, I/O, and heap behaviour. It is skipped unless
// WIPNOTE_VTAB_CORPUS points at a .wipnote/sessions directory, because the
// numbers only mean anything on a corpus with production-sized shards — the
// synthetic fixtures elsewhere in this package are three files of three lines.
//
//	WIPNOTE_VTAB_CORPUS=<project>/.wipnote/sessions go test ./otel/signalvtab/ \
//	    -run TestCorpusMeasurements -v
func TestCorpusMeasurements(t *testing.T) {
	root := os.Getenv("WIPNOTE_VTAB_CORPUS")
	if root == "" {
		t.Skip("set WIPNOTE_VTAB_CORPUS to a .wipnote/sessions directory to run corpus measurements")
	}

	shards := listCorpus(t, root)
	if len(shards) == 0 {
		t.Fatalf("no shards under %s", root)
	}
	var totalBytes int64
	for _, s := range shards {
		totalBytes += s.size
	}
	t.Logf("corpus: %d shards, %.1f MB total, largest %s at %.1f MB",
		len(shards), mb(totalBytes), shards[0].id, mb(shards[0].size))

	db, mod, err := OpenIsolated(root)
	if err != nil {
		t.Fatalf("OpenIsolated: %v", err)
	}
	defer db.Close()

	biggest := shards[0].id

	// A — shard-scoped aggregate. The one that must touch a single file.
	measure(t, mod, "A shard-scoped aggregate (largest shard)", func() {
		var n int64
		var sum sql.NullInt64
		if err := db.QueryRow(
			`SELECT COUNT(*), SUM(duration_ms) FROM signals WHERE shard = ?`, biggest,
		).Scan(&n, &sum); err != nil {
			t.Fatalf("query A: %v", err)
		}
		t.Logf("    rows=%d sum_duration_ms=%d", n, sum.Int64)
	})

	// A2 — the same shape against the smallest shard. On this corpus one
	// shard holds most of the bytes, so A alone understates what pushdown
	// buys; A2 is what a typical session-scoped dashboard query costs.
	smallest := shards[len(shards)-1].id
	measure(t, mod, "A2 shard-scoped aggregate (smallest shard)", func() {
		var n int64
		var sum sql.NullInt64
		if err := db.QueryRow(
			`SELECT COUNT(*), SUM(duration_ms) FROM signals WHERE shard = ?`, smallest,
		).Scan(&n, &sum); err != nil {
			t.Fatalf("query A2: %v", err)
		}
		t.Logf("    rows=%d sum_duration_ms=%d", n, sum.Int64)
	})

	// B — unfiltered group-by across every shard: the dashboard's worst case.
	measure(t, mod, "B unfiltered GROUP BY canonical (all shards)", func() {
		rows, err := db.Query(
			`SELECT canonical, COUNT(*) AS n FROM signals GROUP BY canonical ORDER BY n DESC LIMIT 5`)
		if err != nil {
			t.Fatalf("query B: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var name sql.NullString
			var n int64
			if err := rows.Scan(&name, &n); err != nil {
				t.Fatal(err)
			}
			t.Logf("    %-40s %d", name.String, n)
		}
	})

	// C — the same aggregate, but selecting an attribute bag so column
	// pruning is switched off. The delta is what pruning buys.
	measure(t, mod, "C same aggregate but touching attrs_json (pruning off)", func() {
		var n int64
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM signals WHERE attrs_json IS NOT NULL`).Scan(&n); err != nil {
			t.Fatalf("query C: %v", err)
		}
		t.Logf("    rows with attrs=%d", n)
	})

	// D — harness session filter. Cannot select files; measures the
	// byte-level prefilter against a full decode.
	var busiestSession string
	if err := db.QueryRow(
		`SELECT session_id FROM signals WHERE shard = ? GROUP BY session_id ORDER BY COUNT(*) DESC LIMIT 1`,
		biggest).Scan(&busiestSession); err != nil {
		t.Logf("could not determine busiest session: %v", err)
	}
	if busiestSession != "" {
		measure(t, mod, "D session_id filter across all shards ("+busiestSession+")", func() {
			var n int64
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM signals WHERE session_id = ?`, busiestSession).Scan(&n); err != nil {
				t.Fatalf("query D: %v", err)
			}
			t.Logf("    rows=%d", n)
		})
	}

	// E — full scan with no filter at all.
	measure(t, mod, "E COUNT(*) full scan", func() {
		var n int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM signals`).Scan(&n); err != nil {
			t.Fatalf("query E: %v", err)
		}
		t.Logf("    rows=%d", n)
	})

	// F — how many of those lines the ingest path would have kept. The
	// indexer drops every line whose kind is not span/metric/log, so the
	// virtual table is a strict superset of otel_signals and any ported
	// query needs the kind filter to reproduce the old numbers.
	rows, err := db.Query(`SELECT kind, COUNT(*) FROM signals GROUP BY kind ORDER BY 2 DESC`)
	if err != nil {
		t.Fatalf("query F: %v", err)
	}
	defer rows.Close()
	t.Logf("F rows per kind (only span/metric/log reach otel_signals)")
	for rows.Next() {
		var kind sql.NullString
		var n int64
		if err := rows.Scan(&kind, &n); err != nil {
			t.Fatal(err)
		}
		t.Logf("    %-20s %d", kind.String, n)
	}
}

type corpusShard struct {
	id   string
	size int64
}

func listCorpus(t *testing.T, root string) []corpusShard {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	var out []corpusShard
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fi, err := os.Stat(filepath.Join(root, e.Name(), ShardFile))
		if err != nil {
			continue
		}
		out = append(out, corpusShard{id: e.Name(), size: fi.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].size > out[j].size })
	return out
}

func mb(b int64) float64 { return float64(b) / (1 << 20) }

// measure runs fn while sampling the heap, then reports elapsed time, the
// module's I/O counters, and heap behaviour. Peak live heap is what answers
// "streaming or buffering": a streaming scan holds a bounded working set
// regardless of shard size, a buffering one holds the file.
func measure(t *testing.T, mod *Module, label string, fn func()) {
	t.Helper()

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	mod.Stats().Reset()

	stop := make(chan struct{})
	peak := make(chan uint64, 1)
	go func() {
		var max uint64
		var ms runtime.MemStats
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				peak <- max
				return
			case <-tick.C:
				runtime.ReadMemStats(&ms)
				if ms.HeapAlloc > max {
					max = ms.HeapAlloc
				}
			}
		}
	}()

	start := time.Now()
	fn()
	elapsed := time.Since(start)
	close(stop)
	peakHeap := <-peak

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	st := mod.Stats().Snapshot()

	t.Logf("%s", label)
	t.Logf("    elapsed=%v files_opened=%d lines_read=%d bytes_read=%.1fMB rows=%d prefiltered=%d malformed=%d",
		elapsed.Round(time.Millisecond), st.FilesOpened, st.LinesRead, mb(st.BytesRead),
		st.RowsEmitted, st.RowsPrefiltered, st.LinesMalformed)
	t.Logf("    heap: peak_live=%.1fMB  allocated_total=%.1fMB  (start_live=%.1fMB)",
		mb(int64(peakHeap)), mb(int64(after.TotalAlloc-before.TotalAlloc)), mb(int64(before.HeapAlloc)))
	if st.BytesRead > 0 {
		t.Logf("    throughput=%.1f MB/s  %.0f lines/s",
			mb(st.BytesRead)/elapsed.Seconds(), float64(st.LinesRead)/elapsed.Seconds())
	}
	_ = fmt.Sprint()
}
