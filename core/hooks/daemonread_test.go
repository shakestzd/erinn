package hooks

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/daemon/readsrv"
	"github.com/shakestzd/wipnote/core/db/writequeue"
)

// startReadDaemon starts a real daemon listener serving canonical work-item
// reads from wipnoteDir, and returns its socket path plus a stop func. Stopping
// it is how the "daemon killed mid-session" case is produced.
// The socket is bound under a SHORT temp dir rather than under projectRoot:
// a Unix socket path must fit sockaddr_un.sun_path (104 bytes on darwin), and
// t.TempDir() embeds the full test name, which overflows it. The daemon takes
// an explicit socket path, so the canonical corpus and the socket need not be
// co-located for a test.
func startReadDaemon(t *testing.T, projectRoot string) (string, func()) {
	t.Helper()
	sockDir, err := os.MkdirTemp(shortTempBase(), "wnd")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "writer.sock")

	q := writequeue.New(writequeue.Config{Capacity: 8})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	cache := readsrv.NewCache(filepath.Join(projectRoot, ".wipnote"))
	ln, lerr := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: sock,
		Queue:      q,
		Applier:    daemon.RejectingApplier,
		Reader:     readsrv.Reader(cache),
	})
	if lerr != nil {
		t.Fatalf("new listener: %v", lerr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = ln.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	stopped := false
	return sock, func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		_ = ln.Close()
		q.Stop(time.Second)
	}
}

// writeCanonicalItem writes a canonical work-item HTML file under projectRoot.
func writeCanonicalItem(t *testing.T, projectRoot, coll, id, typ, status, title string) string {
	t.Helper()
	dir := filepath.Join(projectRoot, ".wipnote", coll)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>%s</title></head>
<body>
    <article id="%s" data-type="%s" data-status="%s" data-priority="high"
             data-created="2026-08-01T10:00:00" data-updated="2026-08-01T10:00:00">
        <header><h1>%s</h1></header>
    </article>
</body>
</html>
`, title, id, typ, status, title)
	path := filepath.Join(dir, id+".html")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// shortTempBase returns the shortest writable temp base available, so bound
// socket paths stay inside sun_path.
func shortTempBase() string {
	if fi, err := os.Stat("/tmp"); err == nil && fi.IsDir() {
		return "/tmp"
	}
	return os.TempDir()
}

func newDaemonProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	return root
}

// TestLauncherSessionReadsCanonicalStateViaDaemon is the positive case: under a
// launcher guarantee the hook's work-item read is answered from canonical HTML
// with no derived index involved at all (the DB handle passed in is nil).
func TestLauncherSessionReadsCanonicalStateViaDaemon(t *testing.T) {
	ResetDaemonGuaranteeBreach()
	t.Cleanup(ResetDaemonGuaranteeBreach)

	root := newDaemonProject(t)
	writeCanonicalItem(t, root, "features", "feat-aaaaaaaa", "feature", "in-progress", "Alpha")
	sock, stop := startReadDaemon(t, root)
	defer stop()
	t.Setenv(DaemonSocketEnv, sock)

	item, found := LookupWorkItem(nil, "feat-aaaaaaaa")
	if !found {
		t.Fatal("work item not found via daemon")
	}
	if item.Status != "in-progress" || item.Title != "Alpha" {
		t.Fatalf("item = %+v", item)
	}
	if breach := DaemonGuaranteeBreach(); breach != "" {
		t.Fatalf("unexpected breach: %s", breach)
	}
}

// TestLauncherSessionWithDeadDaemonReportsLoudly is the named verification:
// break the daemon, watch the failure be LOUD rather than a silent fallback.
//
// The assertion is deliberately two-sided. It is not enough that a block result
// appears — the test also proves the hook did NOT answer from the derived
// index, by seeding the index with a row that a silent fallback would happily
// have returned.
func TestLauncherSessionWithDeadDaemonReportsLoudly(t *testing.T) {
	ResetDaemonGuaranteeBreach()
	t.Cleanup(ResetDaemonGuaranteeBreach)

	td := setupTestDB(t)
	td.addFeature("feat-bbbbbbbb", "feature", "Beta from the index", "in-progress")

	root := newDaemonProject(t)
	writeCanonicalItem(t, root, "features", "feat-bbbbbbbb", "feature", "in-progress", "Beta")
	sock, stop := startReadDaemon(t, root)
	t.Setenv(DaemonSocketEnv, sock)

	// Prove the read works while the daemon is alive, so a later failure cannot
	// be blamed on the fixture.
	if _, found := LookupWorkItem(td.DB, "feat-bbbbbbbb"); !found {
		t.Fatal("precondition: read failed while the daemon was alive")
	}

	// BREAK IT: the daemon dies mid-session.
	stop()

	item, found := LookupWorkItem(td.DB, "feat-bbbbbbbb")
	if found {
		t.Fatalf("read succeeded after the daemon died — it fell back silently and returned %+v", item)
	}
	if item.Title == "Beta from the index" {
		t.Fatal("the hook answered from the derived index: that is the silent divergence this forbids")
	}

	breach := DaemonGuaranteeBreach()
	if breach == "" {
		t.Fatal("no breach latched — the failure was silent")
	}
	blocked := DaemonGuaranteeBlockResult()
	if blocked == nil || blocked.Decision != "block" {
		t.Fatalf("block result = %+v, want a block decision", blocked)
	}
	if blocked.Reason == "" {
		t.Fatal("block carried no reason — nothing would be shown to the agent")
	}
}

// TestNonLauncherSessionStillWorks is the second named verification: a session
// no launcher started must behave exactly as it did before this feature, with
// no daemon anywhere and no breach latched.
func TestNonLauncherSessionStillWorks(t *testing.T) {
	ResetDaemonGuaranteeBreach()
	t.Cleanup(ResetDaemonGuaranteeBreach)

	td := setupTestDB(t)
	td.addFeature("feat-cccccccc", "feature", "Gamma", "in-progress")

	// No DaemonSocketEnv: no launcher, therefore no guarantee.
	os.Unsetenv(DaemonSocketEnv)
	if contract, _ := DaemonContractForProcess(); contract != DaemonContractNone {
		t.Fatalf("contract = %v, want DaemonContractNone", contract)
	}

	item, found := LookupWorkItem(td.DB, "feat-cccccccc")
	if !found {
		t.Fatal("unguaranteed session could not read the derived index")
	}
	if item.Status != "in-progress" || item.Title != "Gamma" {
		t.Fatalf("item = %+v", item)
	}
	if breach := DaemonGuaranteeBreach(); breach != "" {
		t.Fatalf("unguaranteed session latched a breach: %s", breach)
	}
	if DaemonGuaranteeBlockResult() != nil {
		t.Fatal("unguaranteed session produced a block result")
	}
}

// TestStaleEntryIsInvalidatedEndToEnd is the third named verification, driven
// all the way through the socket: a work item's status is changed on disk after
// the daemon has already parsed and cached it, and the next hook read must
// return the NEW status.
func TestStaleEntryIsInvalidatedEndToEnd(t *testing.T) {
	ResetDaemonGuaranteeBreach()
	t.Cleanup(ResetDaemonGuaranteeBreach)

	root := newDaemonProject(t)
	path := writeCanonicalItem(t, root, "features", "feat-dddddddd", "feature", "todo", "Delta")
	// Age the seed file past the racily-clean window BEFORE the first read.
	// Without this the entry is unstable and gets re-parsed on every read, so
	// the test would pass even with the mtime/size revalidation removed — it
	// would assert nothing about invalidation. Aging it forces the entry to be
	// genuinely cached, which is the state the rewrite below must invalidate.
	agedTo := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, agedTo, agedTo); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	sock, stop := startReadDaemon(t, root)
	defer stop()
	t.Setenv(DaemonSocketEnv, sock)

	if item, found := LookupWorkItem(nil, "feat-dddddddd"); !found || item.Status != "todo" {
		t.Fatalf("seed read: found=%v item=%+v", found, item)
	}
	// Prove the entry really is cached now, so the assertion below is about
	// invalidation rather than about a cache that never held anything.
	if _, stats, err := daemon.NewReadClientForSocket(sock).GetWorkItem(context.Background(), "feat-dddddddd"); err != nil {
		t.Fatalf("cache-warm probe: %v", err)
	} else if stats.Hits != 1 {
		t.Fatalf("precondition: entry was not cached (stats %+v)", stats)
	}

	// The write a guard must never be answered across.
	writeCanonicalItem(t, root, "features", "feat-dddddddd", "feature", "in-progress", "Delta")
	ts := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	item, found := LookupWorkItem(nil, "feat-dddddddd")
	if !found {
		t.Fatal("post-write read not found")
	}
	if item.Status != "in-progress" {
		t.Fatalf("served status %q after the item moved to in-progress — that is a stale answer to a guard", item.Status)
	}
}

// TestListWorkItemsMatchesAcrossBothContracts pins the two paths to the same
// answer. If they ever disagree, the read protocol has started to drift from
// the index it replaces — and drift is the defect class this exists to remove.
func TestListWorkItemsMatchesAcrossBothContracts(t *testing.T) {
	ResetDaemonGuaranteeBreach()
	t.Cleanup(ResetDaemonGuaranteeBreach)

	td := setupTestDB(t)
	td.addFeature("feat-eeeeeeee", "feature", "Echo", "in-progress")
	td.addFeature("bug-ffffffff", "bug", "Foxtrot", "todo")

	root := newDaemonProject(t)
	writeCanonicalItem(t, root, "features", "feat-eeeeeeee", "feature", "in-progress", "Echo")
	writeCanonicalItem(t, root, "bugs", "bug-ffffffff", "bug", "todo", "Foxtrot")
	sock, stop := startReadDaemon(t, root)
	defer stop()

	args := daemon.WorkItemListArgs{Statuses: []string{"in-progress", "todo"}}

	os.Unsetenv(DaemonSocketEnv)
	fromIndex := ListWorkItems(td.DB, args)

	t.Setenv(DaemonSocketEnv, sock)
	fromDaemon := ListWorkItems(nil, args)

	if len(fromIndex) != len(fromDaemon) {
		t.Fatalf("length mismatch: index %d, daemon %d", len(fromIndex), len(fromDaemon))
	}
	for i := range fromIndex {
		if fromIndex[i].ID != fromDaemon[i].ID || fromIndex[i].Status != fromDaemon[i].Status {
			t.Fatalf("row %d diverged: index %+v, daemon %+v", i, fromIndex[i], fromDaemon[i])
		}
	}
}

// TestReadResponseCarriesCacheAccounting keeps the invalidation assertions
// above non-vacuous: it proves the daemon really does cache, so a later test
// asserting a re-parse is asserting something.
func TestReadResponseCarriesCacheAccounting(t *testing.T) {
	root := newDaemonProject(t)
	writeCanonicalItem(t, root, "features", "feat-12121212", "feature", "todo", "Hotel")
	sock, stop := startReadDaemon(t, root)
	defer stop()

	// Age the file past the racily-clean window so the second read may cache.
	path := filepath.Join(root, ".wipnote", "features", "feat-12121212.html")
	ts := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	client := daemon.NewReadClientForSocket(sock)
	if _, stats, err := client.GetWorkItem(context.Background(), "feat-12121212"); err != nil {
		t.Fatalf("first get: %v", err)
	} else if stats.Misses != 1 {
		t.Fatalf("first get stats = %+v, want one miss", stats)
	}
	if _, stats, err := client.GetWorkItem(context.Background(), "feat-12121212"); err != nil {
		t.Fatalf("second get: %v", err)
	} else if stats.Hits != 1 {
		t.Fatalf("second get stats = %+v, want one hit", stats)
	}
}

// TestBreachReasonNamesTheContract keeps the failure message actionable: it
// must say which contract the session was under, or an agent reading it cannot
// tell a broken guarantee from an ordinary missing daemon.
func TestBreachReasonNamesTheContract(t *testing.T) {
	ResetDaemonGuaranteeBreach()
	t.Cleanup(ResetDaemonGuaranteeBreach)

	t.Setenv(DaemonSocketEnv, filepath.Join(t.TempDir(), "absent.sock"))
	_, _ = LookupWorkItem(nil, "feat-99999999")

	reason := DaemonGuaranteeBreach()
	if reason == "" {
		t.Fatal("no breach latched for an absent socket under a guarantee")
	}
	for _, want := range []string{"launcher", "Pausing", daemon.ReadOpWorkItemGet} {
		if !strings.Contains(reason, want) {
			t.Fatalf("breach reason missing %q:\n%s", want, reason)
		}
	}
}

// BenchmarkLookupWorkItemViaDaemon measures the END-TO-END cost a hook pays:
// dial, request, canonical revalidation, response. This is the number that
// matters, because the alternative a hook faces is not "a fast in-process cache
// hit" but "parse the whole canonical corpus yourself".
func BenchmarkLookupWorkItemViaDaemon(b *testing.B) {
	root := b.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".wipnote", "features"), 0o755); err != nil {
		b.Fatalf("mkdir: %v", err)
	}
	body := `<!DOCTYPE html><html><head><title>B</title></head><body>
<article id="feat-babababa" data-type="feature" data-status="todo" data-priority="high"
 data-created="2026-08-01T10:00:00" data-updated="2026-08-01T10:00:00">
<header><h1>B</h1></header></article></body></html>`
	path := filepath.Join(root, ".wipnote", "features", "feat-babababa.html")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		b.Fatalf("write: %v", err)
	}
	aged := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, aged, aged); err != nil {
		b.Fatalf("chtimes: %v", err)
	}

	sockDir, err := os.MkdirTemp(shortTempBase(), "wnb")
	if err != nil {
		b.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "writer.sock")

	q := writequeue.New(writequeue.Config{Capacity: 8})
	if err := q.Start(context.Background()); err != nil {
		b.Fatalf("queue start: %v", err)
	}
	defer q.Stop(time.Second)
	cache := readsrv.NewCache(filepath.Join(root, ".wipnote"))
	ln, err := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: sock, Queue: q, Applier: daemon.RejectingApplier, Reader: readsrv.Reader(cache),
	})
	if err != nil {
		b.Fatalf("listener: %v", err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ln.Serve(ctx) }()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	b.Setenv(DaemonSocketEnv, sock)
	for b.Loop() {
		if _, found := LookupWorkItem(nil, "feat-babababa"); !found {
			b.Fatal("not found")
		}
	}
}

// countingSocket binds a listener that counts accepted connections, so a test
// can prove a code path did NOT dial rather than merely that it returned the
// right answer.
func countingSocket(t *testing.T) (string, func() int) {
	t.Helper()
	dir, err := os.MkdirTemp(shortTempBase(), "wnc")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "count.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	var n atomic.Int64
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = conn.Close()
		}
	}()
	return sock, func() int { return int(n.Load()) }
}

// TestUnguaranteedSessionNeverDialsTheDaemon is the degrade-quiet branch proved
// by absence of I/O, not by its return value.
//
// This is the branch an entire harness will live on: Codex's plugin model is
// install-based with no per-launch scoping, so its hooks fire in bare CLI, IDE
// and desktop sessions that never touched a launcher. That path must cost
// nothing and must never reach for a socket.
//
// The test sets the marker first and reads (proving the counter observes real
// dials), then clears it and reads again (proving the count does not move). A
// test that only cleared the marker would pass even if the dial code were
// unreachable for some unrelated reason.
func TestUnguaranteedSessionNeverDialsTheDaemon(t *testing.T) {
	ResetDaemonGuaranteeBreach()
	t.Cleanup(ResetDaemonGuaranteeBreach)

	td := setupTestDB(t)
	td.addFeature("feat-d1a1d1a1", "feature", "Dialled", "in-progress")
	sock, dials := countingSocket(t)

	// Guaranteed: the read reaches for the socket.
	t.Setenv(DaemonSocketEnv, sock)
	_, _ = LookupWorkItem(td.DB, "feat-d1a1d1a1")
	guaranteedDials := dials()
	if guaranteedDials == 0 {
		t.Fatal("precondition: guaranteed read never dialled, so the counter proves nothing")
	}
	ResetDaemonGuaranteeBreach()

	// Unguaranteed: no marker, so no dial may occur at all.
	os.Unsetenv(DaemonSocketEnv)
	item, found := LookupWorkItem(td.DB, "feat-d1a1d1a1")
	if !found || item.Title != "Dialled" {
		t.Fatalf("unguaranteed read did not answer from the index: found=%v item=%+v", found, item)
	}
	_ = ListWorkItems(td.DB, daemon.WorkItemListArgs{Statuses: []string{"in-progress"}})

	if got := dials(); got != guaranteedDials {
		t.Fatalf("unguaranteed path dialled the daemon: %d dials before, %d after", guaranteedDials, got)
	}
	if breach := DaemonGuaranteeBreach(); breach != "" {
		t.Fatalf("unguaranteed path latched a breach: %s", breach)
	}
}

// TestUnguaranteedSessionIgnoresALiveDaemon is the case a reachability-probe
// design would get wrong, and the one that matters most now that degrade-quiet
// is an entire harness's hot path.
//
// A live daemon is running for the project — started by somebody else's
// launcher-backed session — and canonical state DISAGREES with the derived
// index. A hook with no launcher marker must read the index anyway. It must not
// opportunistically use a daemon it can see, because "the daemon answered" is
// not the same fact as "a launcher promised this session a daemon", and
// deciding the contract by what happens to be reachable is inference, which is
// exactly what the policy forbids.
func TestUnguaranteedSessionIgnoresALiveDaemon(t *testing.T) {
	ResetDaemonGuaranteeBreach()
	t.Cleanup(ResetDaemonGuaranteeBreach)

	td := setupTestDB(t)
	td.addFeature("feat-e1e1e1e1", "feature", "From the index", "todo")

	root := newDaemonProject(t)
	writeCanonicalItem(t, root, "features", "feat-e1e1e1e1", "feature", "in-progress", "From canonical")
	_, stop := startReadDaemon(t, root)
	defer stop()

	// No marker: this session was not started by a launcher.
	os.Unsetenv(DaemonSocketEnv)

	item, found := LookupWorkItem(td.DB, "feat-e1e1e1e1")
	if !found {
		t.Fatal("unguaranteed read found nothing")
	}
	if item.Title != "From the index" || item.Status != "todo" {
		t.Fatalf("unguaranteed session used the live daemon instead of the index: %+v", item)
	}
}

// BenchmarkUnguaranteedLookupBranch measures what the degrade-quiet branch
// costs a hook to SELECT — the env read and nothing else. The index query it
// then performs is the pre-existing cost this feature does not change, so the
// benchmark stops at the branch.
func BenchmarkUnguaranteedLookupBranch(b *testing.B) {
	b.Setenv(DaemonSocketEnv, "")
	for b.Loop() {
		if c, _ := DaemonContractForProcess(); c != DaemonContractNone {
			b.Fatal("expected the unguaranteed contract")
		}
	}
}
