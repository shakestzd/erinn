package readsrv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeItem writes a minimal canonical work-item HTML file and returns its
// path. It mirrors the shape htmlparse.ParseFile reads from the real writer.
func writeItem(t *testing.T, wipnoteDir, coll, id, typ, status, title, trackID string) string {
	t.Helper()
	dir := filepath.Join(wipnoteDir, coll)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>%s</title></head>
<body>
    <article id="%s"
             data-type="%s"
             data-status="%s"
             data-priority="high"
             data-created="2026-08-01T10:00:00"
             data-updated="2026-08-01T10:00:00" data-track-id="%s">
        <header><h1>%s</h1></header>
    </article>
</body>
</html>
`, title, id, typ, status, trackID, title)
	path := filepath.Join(dir, id+".html")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// agedCache returns a Cache whose clock is far enough ahead of the fixtures'
// mtimes that nothing is racily-clean. Without this every entry would be
// unstable and every read a miss, which would make the cache tests vacuous —
// they would pass whether or not the cache worked.
func agedCache(wipnoteDir string) *Cache {
	c := NewCache(wipnoteDir)
	c.now = func() time.Time { return time.Now().Add(time.Hour) }
	return c
}

func TestGetServesFromCacheOnSecondRead(t *testing.T) {
	dir := t.TempDir()
	writeItem(t, dir, "features", "feat-11111111", "feature", "todo", "First", "trk-aaaaaaaa")
	c := agedCache(dir)

	item, found, hit := c.Get("feat-11111111")
	if !found || hit {
		t.Fatalf("first read: found=%v hit=%v, want found=true hit=false", found, hit)
	}
	if item.Status != "todo" || item.TrackID != "trk-aaaaaaaa" {
		t.Fatalf("first read: %+v", item)
	}

	_, found, hit = c.Get("feat-11111111")
	if !found || !hit {
		t.Fatalf("second read: found=%v hit=%v, want found=true hit=true", found, hit)
	}
}

// TestStatusChangeInvalidatesCachedEntry is the failure this whole design
// exists to prevent: a guard asking for a work item's status after it changed
// must not be answered from the parse that preceded the change.
func TestStatusChangeInvalidatesCachedEntry(t *testing.T) {
	dir := t.TempDir()
	path := writeItem(t, dir, "features", "feat-22222222", "feature", "todo", "Second", "")
	c := agedCache(dir)

	if item, _, _ := c.Get("feat-22222222"); item.Status != "todo" {
		t.Fatalf("seed read status = %q, want todo", item.Status)
	}

	// Rewrite with a DIFFERENT status and a distinct mtime.
	writeItem(t, dir, "features", "feat-22222222", "feature", "in-progress", "Second", "")
	bumpMtime(t, path, 5*time.Second)

	item, found, hit := c.Get("feat-22222222")
	if !found {
		t.Fatal("post-write read: not found")
	}
	if hit {
		t.Fatal("post-write read was a cache HIT — a stale status was served to the caller")
	}
	if item.Status != "in-progress" {
		t.Fatalf("post-write status = %q, want in-progress", item.Status)
	}
}

// TestSameSizeRewriteIsInvalidated covers the case naive size-only checking
// misses: "todo" and "done" are both four bytes, so only the mtime moves.
func TestSameSizeRewriteIsInvalidated(t *testing.T) {
	dir := t.TempDir()
	path := writeItem(t, dir, "features", "feat-33333333", "feature", "todo", "Third", "")
	c := agedCache(dir)
	if _, _, _ = c.Get("feat-33333333"); false {
		t.Fatal("unreachable")
	}

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	writeItem(t, dir, "features", "feat-33333333", "feature", "done", "Third", "")
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if before.Size() != after.Size() {
		t.Fatalf("fixture no longer exercises the same-size case: %d vs %d", before.Size(), after.Size())
	}
	bumpMtime(t, path, 5*time.Second)

	item, _, hit := c.Get("feat-33333333")
	if hit {
		t.Fatal("same-size rewrite was served from cache — stale status")
	}
	if item.Status != "done" {
		t.Fatalf("status = %q, want done", item.Status)
	}
}

// TestRacilyCleanEntryIsNeverServedFromCache proves the same-tick guard: an
// entry parsed while its own mtime is still inside racyWindow must be
// re-parsed on every read, because a rewrite in that window could be
// indistinguishable by (mtime, size).
func TestRacilyCleanEntryIsNeverServedFromCache(t *testing.T) {
	dir := t.TempDir()
	writeItem(t, dir, "features", "feat-44444444", "feature", "todo", "Fourth", "")

	// A clock at "now" makes the just-written file racily clean.
	c := NewCache(dir)
	c.now = time.Now

	if _, found, _ := c.Get("feat-44444444"); !found {
		t.Fatal("seed read not found")
	}
	if _, _, hit := c.Get("feat-44444444"); hit {
		t.Fatal("racily-clean entry was served from cache")
	}

	// Once the window has demonstrably closed, the same entry caches normally.
	c.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, _, hit := c.Get("feat-44444444"); hit {
		t.Fatal("first read after window close should re-parse and store")
	}
	if _, _, hit := c.Get("feat-44444444"); !hit {
		t.Fatal("second read after window close should hit")
	}
}

func TestDeletedItemIsNotServedFromCache(t *testing.T) {
	dir := t.TempDir()
	path := writeItem(t, dir, "features", "feat-55555555", "feature", "todo", "Fifth", "")
	c := agedCache(dir)
	if _, found, _ := c.Get("feat-55555555"); !found {
		t.Fatal("seed read not found")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, found, _ := c.Get("feat-55555555"); found {
		t.Fatal("deleted work item still served from cache")
	}
}

func TestScanDetectsAddedAndRemovedItems(t *testing.T) {
	dir := t.TempDir()
	writeItem(t, dir, "features", "feat-66666666", "feature", "in-progress", "Six", "trk-bbbbbbbb")
	c := agedCache(dir)

	scan := c.scanCollections([]string{"features"})
	if len(scan.items) != 1 {
		t.Fatalf("initial scan: %d items, want 1", len(scan.items))
	}

	// ADD: a new file must appear even though no known entry changed.
	writeItem(t, dir, "features", "feat-77777777", "feature", "todo", "Seven", "trk-bbbbbbbb")
	scan = c.scanCollections([]string{"features"})
	if len(scan.items) != 2 {
		t.Fatalf("after add: %d items, want 2", len(scan.items))
	}
	// in-progress must sort ahead of todo.
	if scan.items[0].Status != "in-progress" {
		t.Fatalf("ordering: first item status = %q, want in-progress", scan.items[0].Status)
	}

	// REMOVE: the entry must be pruned, and a later point lookup must miss.
	if err := os.Remove(filepath.Join(dir, "features", "feat-66666666.html")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	scan = c.scanCollections([]string{"features"})
	if len(scan.items) != 1 {
		t.Fatalf("after remove: %d items, want 1", len(scan.items))
	}
	if _, found, _ := c.Get("feat-66666666"); found {
		t.Fatal("removed item still resolvable after prune")
	}
}

func TestNestedLayoutIsResolved(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "bugs", "bug-88888888")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := writeItem(t, dir, "bugs", "bug-88888888", "bug", "todo", "Nested", "")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.Remove(src); err != nil {
		t.Fatalf("remove flat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "index.html"), data, 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	c := agedCache(dir)
	item, found, _ := c.Get("bug-88888888")
	if !found || item.Type != "bug" {
		t.Fatalf("nested lookup: found=%v item=%+v", found, item)
	}
	scan := c.scanCollections([]string{"bugs"})
	if len(scan.items) != 1 || scan.items[0].ID != "bug-88888888" {
		t.Fatalf("nested scan: %+v", scan.items)
	}
}

func TestUnknownIDPrefixIsNotFound(t *testing.T) {
	c := agedCache(t.TempDir())
	if _, found, _ := c.Get("wat-99999999"); found {
		t.Fatal("unknown prefix resolved")
	}
}

// bumpMtime pushes a file's mtime forward so the test does not depend on
// filesystem timestamp resolution to observe a change.
func bumpMtime(t *testing.T, path string, d time.Duration) {
	t.Helper()
	ts := time.Now().Add(d)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// BenchmarkScanCold measures the full canonical parse — what a hook would pay
// on every tool call if it parsed canonical state itself. BenchmarkScanWarm
// measures the revalidated read the daemon serves instead. The gap between
// them is the entire justification for this feature, so it is measured here
// rather than asserted in prose.
//
// Set WIPNOTE_BENCH_DIR to a real .wipnote directory to benchmark against a
// live corpus; otherwise a synthetic 1,000-item corpus is generated.
func BenchmarkScanCold(b *testing.B) {
	dir := benchCorpus(b)
	for b.Loop() {
		NewCache(dir).scanCollections(allCollections)
	}
}

func BenchmarkScanWarm(b *testing.B) {
	dir := benchCorpus(b)
	c := NewCache(dir)
	c.now = func() time.Time { return time.Now().Add(time.Hour) }
	c.scanCollections(allCollections) // prime
	for b.Loop() {
		c.scanCollections(allCollections)
	}
}

func BenchmarkGetWarm(b *testing.B) {
	dir := benchCorpus(b)
	c := NewCache(dir)
	c.now = func() time.Time { return time.Now().Add(time.Hour) }
	scan := c.scanCollections(allCollections)
	if len(scan.items) == 0 {
		b.Skip("empty corpus")
	}
	id := scan.items[0].ID
	for b.Loop() {
		c.Get(id)
	}
}

// benchCorpus returns a directory holding a work-item corpus, generating a
// synthetic one when no live corpus is supplied.
func benchCorpus(b *testing.B) string {
	if live := os.Getenv("WIPNOTE_BENCH_DIR"); live != "" {
		return live
	}
	dir := b.TempDir()
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("feat-%08x", i)
		writeBenchItem(b, dir, id)
	}
	return dir
}

func writeBenchItem(b *testing.B, wipnoteDir, id string) {
	b.Helper()
	d := filepath.Join(wipnoteDir, "features")
	if err := os.MkdirAll(d, 0o755); err != nil {
		b.Fatalf("mkdir: %v", err)
	}
	body := fmt.Sprintf(`<!DOCTYPE html><html><head><title>%s</title></head><body>
<article id="%s" data-type="feature" data-status="todo" data-priority="high"
 data-created="2026-08-01T10:00:00" data-updated="2026-08-01T10:00:00">
<header><h1>%s</h1></header><section class="content"><p>%s</p></section></article></body></html>`,
		id, id, id, id)
	if err := os.WriteFile(filepath.Join(d, id+".html"), []byte(body), 0o644); err != nil {
		b.Fatalf("write: %v", err)
	}
}
