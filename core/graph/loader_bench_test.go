package graph

import (
	"os"
	"path/filepath"
	"testing"
)

// wipnoteDirForBench resolves this repo's OWN .wipnote/ directory for
// self-referential dogfooding benchmarks: core/graph lives at
// <repo>/core/graph, so the repo root's canonical store is two levels up.
// Skips (rather than failing) when that tree isn't present -- e.g. core/
// checked out or vendored on its own, away from the parent wipnote repo.
func wipnoteDirForBench(tb testing.TB) string {
	tb.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", ".wipnote"))
	if err != nil {
		tb.Skipf("resolve .wipnote dir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		tb.Skipf(".wipnote dir not found at %s (core/ checked out without its parent repo?): %v", dir, err)
	}
	return dir
}

// BenchmarkLoadAll measures the pure in-memory cost of LoadAll -- every
// features/bugs/spikes/tracks/plans/specs HTML file parsed via
// htmlparse.ParseFile, plus archive ledger rows, with zero git subprocess
// calls and zero SQLite writes -- against this repo's own real, current-size
// canonical store.
//
// This replaces a stale prose claim in learning-spk-badb1d4a ("48ms
// parallel, 121ms serial" against a 1,028-file corpus): LoadDir has no
// goroutines, so no parallel code path exists to produce a 48ms figure --
// it described execution that isn't in the source. Two independent
// re-measurements the night that was caught (93.6ms and 107.6ms, current
// corpus of ~1,058 files) both land near the cited serial figure instead.
// Committing this benchmark is the fix for that class of drift: a prose
// number in a knowledge base goes stale invisibly, a benchmark fails loudly
// when the code or corpus it describes changes.
func BenchmarkLoadAll(b *testing.B) {
	wipnoteDir := wipnoteDirForBench(b)

	// One untimed warmup pass so the OS file cache is warm before the
	// timed loop -- b.N repeats hit the same files repeatedly regardless,
	// so this just avoids charging the very first iteration for cold I/O.
	if _, err := LoadAll(wipnoteDir); err != nil {
		b.Fatalf("warmup LoadAll: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LoadAll(wipnoteDir); err != nil {
			b.Fatalf("LoadAll: %v", err)
		}
	}
}

// TestLoadAll_BenchSmoke is a fast (non-benchmark) sanity check that
// BenchmarkLoadAll's target function still returns a plausible node count
// against the real corpus, so a change that breaks LoadAll entirely fails
// `go test` too, not only a benchmark run that CI may not invoke by default.
func TestLoadAll_BenchSmoke(t *testing.T) {
	wipnoteDir := wipnoteDirForBench(t)
	nodes, err := LoadAll(wipnoteDir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(nodes) == 0 {
		t.Error("LoadAll returned zero nodes against the real corpus")
	}
}
