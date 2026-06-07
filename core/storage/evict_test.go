package storage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/storage"
)

// mkProject creates a fake project-cache subdir with a single file of the
// given size and stamps both file and dir mtimes to the requested time.
func mkProject(t *testing.T, dir string, size int64, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, storage.DBFileName)
	if err := os.WriteFile(f, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestEvict_EmptyCache(t *testing.T) {
	root := t.TempDir()
	res, err := storage.Evict(root, storage.EvictOptions{MaxAge: 90 * 24 * time.Hour, MaxSize: 1 << 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("expected no removals, got %v", res.Removed)
	}
	if res.RemainingBytes != 0 || res.RemainingDirs != 0 {
		t.Errorf("expected zero remaining, got %+v", res)
	}
}

func TestEvict_MissingCache(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	res, err := storage.Evict(root, storage.EvictOptions{MaxAge: 90 * 24 * time.Hour, MaxSize: 1 << 30})
	if err != nil {
		t.Fatalf("missing cache should not error: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("expected no removals, got %v", res.Removed)
	}
}

func TestEvict_MaxAge(t *testing.T) {
	root := t.TempDir()
	fresh := filepath.Join(root, strings.Repeat("a", 16))
	old := filepath.Join(root, strings.Repeat("b", 16))
	mkProject(t, fresh, 100, time.Now())
	mkProject(t, old, 100, time.Now().Add(-100*24*time.Hour))

	res, err := storage.Evict(root, storage.EvictOptions{MaxAge: 90 * 24 * time.Hour, MaxSize: 10 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 {
		t.Fatalf("want 1 removed, got %d (%v)", len(res.Removed), res.Removed)
	}
	if filepath.Base(res.Removed[0]) != strings.Repeat("b", 16) {
		t.Errorf("wrong dir removed: %s", res.Removed[0])
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh dir should still exist: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old dir should be gone, stat err: %v", err)
	}
}

func TestEvict_MaxSize(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, strings.Repeat("a", 16))
	b := filepath.Join(root, strings.Repeat("b", 16))
	c := filepath.Join(root, strings.Repeat("c", 16))
	mkProject(t, a, 1<<20, time.Now().Add(-3*time.Hour))
	mkProject(t, b, 1<<20, time.Now().Add(-2*time.Hour))
	mkProject(t, c, 1<<20, time.Now().Add(-1*time.Hour))

	// 1.5 MiB cap with 3x1MiB entries → must evict the two oldest.
	res, err := storage.Evict(root, storage.EvictOptions{MaxAge: 365 * 24 * time.Hour, MaxSize: (1 << 20) + (1 << 19)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 2 {
		t.Fatalf("want 2 removed, got %d (%v)", len(res.Removed), res.Removed)
	}
	if _, err := os.Stat(c); err != nil {
		t.Errorf("newest dir c should survive: %v", err)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("oldest dir a should be gone")
	}
}

func TestEvict_BothTriggers(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, strings.Repeat("a", 16))
	mid := filepath.Join(root, strings.Repeat("b", 16))
	fresh := filepath.Join(root, strings.Repeat("c", 16))
	mkProject(t, old, 1<<20, time.Now().Add(-100*24*time.Hour))
	mkProject(t, mid, 1<<20, time.Now().Add(-2*time.Hour))
	mkProject(t, fresh, 1<<20, time.Now().Add(-1*time.Hour))

	// max-age evicts old; survivors total 2 MiB. max-size 1.5 MiB then evicts mid.
	res, err := storage.Evict(root, storage.EvictOptions{MaxAge: 90 * 24 * time.Hour, MaxSize: (1 << 20) + (1 << 19)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 2 {
		t.Fatalf("want 2 removed, got %d (%v)", len(res.Removed), res.Removed)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("freshest dir should survive: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old dir should be gone")
	}
	if _, err := os.Stat(mid); !os.IsNotExist(err) {
		t.Errorf("mid dir should be gone (LRU after age sweep)")
	}
}

func TestEvict_DryRunDoesNotDelete(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, strings.Repeat("a", 16))
	mkProject(t, old, 100, time.Now().Add(-100*24*time.Hour))

	res, err := storage.Evict(root, storage.EvictOptions{MaxAge: 90 * 24 * time.Hour, MaxSize: 1 << 30, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 {
		t.Fatalf("dry-run should report 1 candidate, got %d", len(res.Removed))
	}
	if !res.DryRun {
		t.Error("DryRun flag should be set on result")
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("dry-run must not delete: %v", err)
	}
}

func TestEvict_IgnoresNonHexEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".last-prune"), []byte("123"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "not-a-hash"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, strings.Repeat("a", 16))
	mkProject(t, old, 100, time.Now().Add(-100*24*time.Hour))

	res, err := storage.Evict(root, storage.EvictOptions{MaxAge: 90 * 24 * time.Hour, MaxSize: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 {
		t.Fatalf("want only the hex dir removed, got %d (%v)", len(res.Removed), res.Removed)
	}
	if _, err := os.Stat(filepath.Join(root, ".last-prune")); err != nil {
		t.Errorf(".last-prune marker must not be touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "not-a-hash")); err != nil {
		t.Errorf("non-hex dir must not be touched: %v", err)
	}
}

func TestCacheStats_ReturnsEntries(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, strings.Repeat("a", 16))
	b := filepath.Join(root, strings.Repeat("b", 16))
	mkProject(t, a, 100, time.Now().Add(-1*time.Hour))
	mkProject(t, b, 200, time.Now())

	entries, err := storage.CacheStats(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	// Newest first.
	if entries[0].Hash != strings.Repeat("b", 16) {
		t.Errorf("expected newest entry first, got %s", entries[0].Hash)
	}
	if entries[0].Size != 200 {
		t.Errorf("size mismatch: got %d", entries[0].Size)
	}
}

func TestCacheStats_MissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	entries, err := storage.CacheStats(root)
	if err != nil {
		t.Fatalf("missing root should not error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want empty entries, got %v", entries)
	}
}

func TestMaybePruneOpportunistic_FirstRunCreatesMarker(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, strings.Repeat("a", 16))
	mkProject(t, old, 100, time.Now().Add(-100*24*time.Hour))

	res, ran, err := storage.MaybePruneOpportunistic(root, 7*24*time.Hour, storage.EvictOptions{MaxAge: 90 * 24 * time.Hour, MaxSize: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("first run must not prune (avoids surprising new users)")
	}
	if len(res.Removed) != 0 {
		t.Errorf("first run should not remove anything: %v", res.Removed)
	}
	if _, err := os.Stat(filepath.Join(root, ".last-prune")); err != nil {
		t.Errorf(".last-prune marker should be created: %v", err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("old dir should still exist after first run: %v", err)
	}
}

func TestMaybePruneOpportunistic_SkipsWhenMarkerFresh(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, strings.Repeat("a", 16))
	mkProject(t, old, 100, time.Now().Add(-100*24*time.Hour))
	marker := filepath.Join(root, ".last-prune")
	if err := os.WriteFile(marker, []byte("now"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(marker, now, now); err != nil {
		t.Fatal(err)
	}

	_, ran, err := storage.MaybePruneOpportunistic(root, 7*24*time.Hour, storage.EvictOptions{MaxAge: 90 * 24 * time.Hour, MaxSize: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("must skip prune while marker is fresh")
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("old dir must remain when prune is skipped: %v", err)
	}
}

func TestMaybePruneOpportunistic_RunsWhenMarkerStale(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, strings.Repeat("a", 16))
	mkProject(t, old, 100, time.Now().Add(-100*24*time.Hour))
	marker := filepath.Join(root, ".last-prune")
	if err := os.WriteFile(marker, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(marker, stale, stale); err != nil {
		t.Fatal(err)
	}

	res, ran, err := storage.MaybePruneOpportunistic(root, 7*24*time.Hour, storage.EvictOptions{MaxAge: 90 * 24 * time.Hour, MaxSize: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("must prune when marker is stale")
	}
	if len(res.Removed) != 1 {
		t.Errorf("want 1 evicted, got %d", len(res.Removed))
	}
	info, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("marker should still exist: %v", err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Errorf("marker mtime should have been refreshed, got age %v", time.Since(info.ModTime()))
	}
}

func TestEvict_ProtectedSurvivesAgeSweep(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, strings.Repeat("a", 16))
	mkProject(t, old, 100, time.Now().Add(-100*24*time.Hour))

	res, err := storage.Evict(root, storage.EvictOptions{
		MaxAge:    90 * 24 * time.Hour,
		MaxSize:   1 << 30,
		Protected: []string{old},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("protected dir must not be evicted by age, got %v", res.Removed)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("protected dir must still exist: %v", err)
	}
}

func TestEvict_ProtectedSurvivesSizeSweep(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, strings.Repeat("a", 16))
	b := filepath.Join(root, strings.Repeat("b", 16))
	c := filepath.Join(root, strings.Repeat("c", 16))
	// a is the LRU and would normally be evicted first.
	mkProject(t, a, 1<<20, time.Now().Add(-3*time.Hour))
	mkProject(t, b, 1<<20, time.Now().Add(-2*time.Hour))
	mkProject(t, c, 1<<20, time.Now().Add(-1*time.Hour))

	res, err := storage.Evict(root, storage.EvictOptions{
		MaxAge:    365 * 24 * time.Hour,
		MaxSize:   (1 << 20) + (1 << 19),
		Protected: []string{a},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); err != nil {
		t.Errorf("protected LRU dir must survive size sweep: %v", err)
	}
	for _, p := range res.Removed {
		if p == a {
			t.Errorf("protected dir was incorrectly removed: %s", p)
		}
	}
}

func TestCacheRoot_ContainsWipnoteSegment(t *testing.T) {
	root, err := storage.CacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.ToSlash(root), "/wipnote") {
		t.Errorf("expected 'wipnote' segment in %s", root)
	}
}
