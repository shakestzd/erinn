package claimledger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), ".wipnote"))
}

func mustOpen(t *testing.T, s *Store, root, session, agent, item string, start time.Time) Episode {
	t.Helper()
	e, written, err := s.Open(root, Episode{
		WorkItemID: item,
		SessionID:  session,
		AgentID:    agent,
		StartedAt:  start,
	})
	if err != nil {
		t.Fatalf("Open(%s/%s/%s): %v", session, agent, item, err)
	}
	if !written {
		t.Fatalf("Open(%s/%s/%s): expected a new row, got a renewal no-op", session, agent, item)
	}
	return e
}

func TestOpenAndCloseRoundTrip(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)

	ep := mustOpen(t, s, "root-1", "sess-a", "__root__", "feat-aaa", start)

	got, err := s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after Open: got %d episodes, want 1", len(got))
	}
	if !got[0].IsOpen() {
		t.Errorf("episode should be open after Open: end=%v outcome=%q", got[0].EndedAt, got[0].Outcome)
	}
	if got[0].WorkItemID != "feat-aaa" || got[0].AgentID != "__root__" || got[0].SessionID != "sess-a" {
		t.Errorf("round-trip lost identity: %+v", got[0])
	}
	if !got[0].StartedAt.Equal(start) {
		t.Errorf("start round-trip: got %v, want %v", got[0].StartedAt, start)
	}

	closed, err := s.Close("root-1", "sess-a", "__root__", "feat-aaa", OutcomeCompleted, end)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closed.ID != ep.ID {
		t.Errorf("Close hit the wrong episode: got %s, want %s", closed.ID, ep.ID)
	}

	got, err = s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard after Close: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("close must UPDATE the row in place, not append: got %d episodes, want 1", len(got))
	}
	if got[0].IsOpen() {
		t.Errorf("episode still open after Close")
	}
	if got[0].Outcome != OutcomeCompleted {
		t.Errorf("outcome: got %q, want %q", got[0].Outcome, OutcomeCompleted)
	}
	if !got[0].EndedAt.Equal(end) {
		t.Errorf("end: got %v, want %v", got[0].EndedAt, end)
	}
}

// TestEpisodeAddressableByFragment pins the acceptance criterion that every
// episode is addressable by fragment: the row must carry id="<episode-id>".
func TestEpisodeAddressableByFragment(t *testing.T) {
	s := testStore(t)
	ep := mustOpen(t, s, "root-1", "sess-a", "__root__", "feat-aaa", time.Now().UTC())

	raw, err := os.ReadFile(s.ShardPath("root-1"))
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	want := fmt.Sprintf(`id="%s"`, ep.ID)
	if !strings.Contains(string(raw), want) {
		t.Errorf("shard has no fragment anchor %s; episode is not addressable", want)
	}
}

// TestRenewalsProduceNothing is the load-bearing filter. Re-opening the same
// claim — which is what every re-`start` and every lease renewal looks like —
// must write no row and change no byte. Renewal traffic dwarfs real claims, so
// a regression here buries the signal the whole feature exists to produce.
func TestRenewalsProduceNothing(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	mustOpen(t, s, "root-1", "sess-a", "ag-1", "feat-aaa", start)

	before, err := os.ReadFile(s.ShardPath("root-1"))
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}

	for i := 0; i < 25; i++ {
		_, written, err := s.Open("root-1", Episode{
			WorkItemID: "feat-aaa",
			SessionID:  "sess-a",
			AgentID:    "ag-1",
			StartedAt:  start.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("renewal %d: %v", i, err)
		}
		if written {
			t.Fatalf("renewal %d wrote a new row; renewals must produce no rows", i)
		}
	}

	after, err := os.ReadFile(s.ShardPath("root-1"))
	if err != nil {
		t.Fatalf("read shard after renewals: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("25 renewals mutated the shard; the file must be byte-identical")
	}

	eps, err := s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard: %v", err)
	}
	if len(eps) != 1 {
		t.Errorf("got %d episodes after 1 claim + 25 renewals, want 1", len(eps))
	}
}

// TestReClaimAfterCloseOpensNewEpisode guards the other side of the renewal
// filter: once an episode is closed, claiming the same item again must start a
// SECOND episode rather than being swallowed as a renewal.
func TestReClaimAfterCloseOpensNewEpisode(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	mustOpen(t, s, "root-1", "sess-a", "ag-1", "feat-aaa", start)
	if _, err := s.Close("root-1", "sess-a", "ag-1", "feat-aaa", OutcomeReleased, start.Add(time.Hour)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mustOpen(t, s, "root-1", "sess-a", "ag-1", "feat-aaa", start.Add(2*time.Hour))

	eps, err := s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("re-claim after release: got %d episodes, want 2", len(eps))
	}
	if eps[0].IsOpen() || !eps[1].IsOpen() {
		t.Errorf("expected [closed, open], got [open=%v, open=%v]", eps[0].IsOpen(), eps[1].IsOpen())
	}
}

// TestConcurrentWritersInOneSession is the contention case: several agents in
// one root session share one shard file, so every Open goes through the same
// guard. All of them must land.
func TestConcurrentWritersInOneSession(t *testing.T) {
	s := testStore(t)
	const writers = 12
	start := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, written, err := s.Open("root-1", Episode{
				WorkItemID: fmt.Sprintf("feat-%03d", i),
				SessionID:  "sess-a",
				AgentID:    fmt.Sprintf("ag-%03d", i),
				StartedAt:  start.Add(time.Duration(i) * time.Second),
			})
			if err != nil {
				errs[i] = err
				return
			}
			if !written {
				errs[i] = fmt.Errorf("writer %d was treated as a renewal", i)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	eps, err := s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard: %v", err)
	}
	if len(eps) != writers {
		t.Fatalf("concurrent writers: got %d episodes, want %d — a row was lost", len(eps), writers)
	}
	seen := make(map[string]bool, writers)
	for _, e := range eps {
		seen[e.WorkItemID] = true
	}
	for i := 0; i < writers; i++ {
		if !seen[fmt.Sprintf("feat-%03d", i)] {
			t.Errorf("writer %d's row is missing", i)
		}
	}
}

// TestConcurrentOpenAndCloseInterleave mixes the append path and the
// read-modify-write path on one shard. The rewrite must not clobber a row
// appended concurrently.
func TestConcurrentOpenAndCloseInterleave(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	const n = 8

	for i := 0; i < n; i++ {
		mustOpen(t, s, "root-1", "sess-a", fmt.Sprintf("closer-%d", i), "feat-close", start)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = s.Close("root-1", "sess-a", fmt.Sprintf("closer-%d", i), "feat-close", OutcomeCompleted, start.Add(time.Hour))
		}(i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _ = s.Open("root-1", Episode{
				WorkItemID: "feat-open",
				SessionID:  "sess-a",
				AgentID:    fmt.Sprintf("opener-%d", i),
				StartedAt:  start.Add(time.Duration(i) * time.Second),
			})
		}(i)
	}
	wg.Wait()

	eps, err := s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard: %v", err)
	}
	if len(eps) != 2*n {
		t.Fatalf("interleaved open/close: got %d episodes, want %d — a concurrent rewrite lost an appended row", len(eps), 2*n)
	}
	closed := 0
	for _, e := range eps {
		if !e.IsOpen() {
			closed++
		}
	}
	if closed != n {
		t.Errorf("got %d closed episodes, want %d", closed, n)
	}
}

// TestTornPriorWriteDoesNotCorruptOrSwallow simulates a crash mid-append by
// hand-truncating the file to a partial row, then appending. The earlier rows
// must survive intact and the new row must be readable — it must NOT merge into
// the corrupt fragment and disappear with it.
func TestTornPriorWriteDoesNotCorruptOrSwallow(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	mustOpen(t, s, "root-1", "sess-a", "ag-1", "feat-one", start)
	mustOpen(t, s, "root-1", "sess-a", "ag-2", "feat-two", start.Add(time.Minute))

	path := s.ShardPath("root-1")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	// Cut off the footer and part of a third row: the exact shape of a process
	// killed partway through writing a row.
	body := strings.TrimSuffix(string(raw), ledgerFooter)
	torn := body + `<tr id="ep-torn" data-episode-id="ep-torn" data-work-item="feat-tor`
	if err := os.WriteFile(path, []byte(torn), 0o644); err != nil {
		t.Fatalf("write torn shard: %v", err)
	}

	fresh := mustOpen(t, s, "root-1", "sess-a", "ag-3", "feat-three", start.Add(2*time.Minute))

	eps, err := s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard after torn append: %v", err)
	}
	byItem := make(map[string]Episode, len(eps))
	for _, e := range eps {
		byItem[e.WorkItemID] = e
	}
	for _, want := range []string{"feat-one", "feat-two", "feat-three"} {
		if _, ok := byItem[want]; !ok {
			t.Errorf("%s missing after torn-write repair (got %d episodes: %v)", want, len(eps), keys(byItem))
		}
	}
	if len(eps) != 3 {
		t.Errorf("got %d episodes, want 3: %v", len(eps), keys(byItem))
	}
	if got := byItem["feat-three"].ID; got != fresh.ID {
		t.Errorf("new episode id round-trip: got %q, want %q — the row fused with the corrupt tail", got, fresh.ID)
	}
	assertNoTornResidue(t, s.ShardPath("root-1"), len(eps), "ep-torn")
}

// assertNoTornResidue checks the repair actually REMOVED the corrupt fragment
// rather than merely parsing around it. Two independent signals:
//
//   - the torn episode's marker must be gone from the bytes; leaving it means
//     every future append stacks more garbage into the table, and
//   - the number of "<tr " openers must equal the number of parsed episodes;
//     an extra opener means a fragment fused with a real row, which is the
//     swallow failure the guard exists to prevent.
func assertNoTornResidue(t *testing.T, path string, wantRows int, marker string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	if marker != "" && strings.Contains(string(raw), marker) {
		t.Errorf("corrupt fragment %q is still in the file; the torn tail was appended to, not repaired", marker)
	}
	body := strings.TrimPrefix(strings.TrimSuffix(string(raw), ledgerFooter), ledgerHeader)
	if openers := strings.Count(body, "<tr "); openers != wantRows {
		t.Errorf("table has %d <tr openers but %d parseable episodes — a fragment fused with a real row\n%s",
			openers, wantRows, body)
	}
	if lines := strings.Count(body, "\n"); lines != wantRows {
		t.Errorf("table has %d lines but %d episodes — the one-row-one-line invariant is broken", lines, wantRows)
	}
}

// TestTornWriteWithoutTrailingNewline covers the harsher variant: the crash
// left a fragment that does not even end at a line boundary, and the fragment
// contains an unterminated tag. Appending naively would fuse the two rows.
func TestTornWriteWithoutTrailingNewline(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	mustOpen(t, s, "root-1", "sess-a", "ag-1", "feat-keep", start)

	path := s.ShardPath("root-1")
	raw, _ := os.ReadFile(path)
	torn := strings.TrimSuffix(string(raw), ledgerFooter) + `<tr id="ep-tornx" data-episode-id=`
	if err := os.WriteFile(path, []byte(torn), 0o644); err != nil {
		t.Fatalf("write torn shard: %v", err)
	}

	fresh := mustOpen(t, s, "root-1", "sess-a", "ag-2", "feat-new", start.Add(time.Minute))

	eps, err := s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("got %d episodes, want 2 (feat-keep + feat-new)", len(eps))
	}
	got := map[string]Episode{}
	for _, e := range eps {
		got[e.WorkItemID] = e
	}
	if _, ok := got["feat-keep"]; !ok {
		t.Error("the row written before the torn write was lost")
	}
	if got["feat-new"].ID != fresh.ID {
		t.Errorf("the row written after the torn write did not survive intact: got id %q, want %q",
			got["feat-new"].ID, fresh.ID)
	}
	assertNoTornResidue(t, path, len(eps), "ep-tornx")
}

// TestTruncatedHeaderRewritesDocument covers a crash during file creation:
// the file is shorter than the header and can hold no complete row, so the
// append must rewrite the document instead of splicing into the header.
func TestTruncatedHeaderRewritesDocument(t *testing.T) {
	s := testStore(t)
	path := s.ShardPath("root-1")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(ledgerHeader[:20]), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	mustOpen(t, s, "root-1", "sess-a", "ag-1", "feat-aaa", time.Now().UTC())

	eps, err := s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard: %v", err)
	}
	if len(eps) != 1 || eps[0].WorkItemID != "feat-aaa" {
		t.Fatalf("got %d episodes %v, want 1 feat-aaa", len(eps), eps)
	}
}

// TestNewlineInFieldCannotBreakRowFraming proves the one-row-one-line invariant
// is enforced rather than assumed — an embedded newline would give the
// torn-write repair a false boundary inside a live row.
func TestNewlineInFieldCannotBreakRowFraming(t *testing.T) {
	s := testStore(t)
	_, _, err := s.Open("root-1", Episode{
		WorkItemID: "feat-aaa",
		SessionID:  "sess-a",
		AgentID:    "ag\n-1",
		StartedAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	raw, err := os.ReadFile(s.ShardPath("root-1"))
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	body := strings.TrimPrefix(strings.TrimSuffix(string(raw), ledgerFooter), ledgerHeader)
	if n := strings.Count(body, "\n"); n != 1 {
		t.Errorf("one row must be exactly one line: found %d newlines in %q", n, body)
	}
}

// TestCloseAllForSessionIsSessionScoped guards the sibling-clobber failure: a
// shard is keyed by ROOT session and can hold rows from several sessions in the
// same tree, so a CHILD session ending must close only its own episodes.
func TestCloseAllForSessionIsSessionScoped(t *testing.T) {
	s := testStore(t)
	start := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	mustOpen(t, s, "root-1", "root-1", "__root__", "feat-parent", start)
	mustOpen(t, s, "root-1", "child-a", "ag-a", "feat-child-a", start)
	mustOpen(t, s, "root-1", "child-b", "ag-b", "feat-child-b", start)

	closed, err := s.CloseAllForSession("root-1", "child-a", OutcomeAbandoned, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("CloseAllForSession: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed %d episodes, want exactly 1 (only child-a's)", closed)
	}

	eps, err := s.ReadShard("root-1")
	if err != nil {
		t.Fatalf("ReadShard: %v", err)
	}
	bySession := map[string]Episode{}
	for _, e := range eps {
		bySession[e.SessionID] = e
	}
	if bySession["child-a"].IsOpen() {
		t.Error("child-a's own episode was not closed")
	}
	if !bySession["root-1"].IsOpen() {
		t.Error("the PARENT session's episode was closed by a child session ending")
	}
	if !bySession["child-b"].IsOpen() {
		t.Error("a SIBLING session's episode was closed by child-a ending")
	}

	// An empty session filter means the whole shard — what Reconcile wants once
	// the entire tree is dead.
	closed, err = s.CloseAllForSession("root-1", "", OutcomeExpired, start.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("CloseAllForSession(all): %v", err)
	}
	if closed != 2 {
		t.Errorf("unscoped close closed %d episodes, want the 2 still open", closed)
	}
}

func TestCloseWithNoOpenEpisode(t *testing.T) {
	s := testStore(t)
	if _, err := s.Close("root-1", "sess-a", "ag-1", "feat-aaa", OutcomeCompleted, time.Now().UTC()); err == nil {
		t.Fatal("Close on an empty ledger should report ErrNoOpenEpisode")
	} else if err != ErrNoOpenEpisode {
		t.Fatalf("got %v, want ErrNoOpenEpisode", err)
	}
}

func TestSlugifyRejectsPathTraversal(t *testing.T) {
	s := testStore(t)
	path := s.ShardPath("../../etc/passwd")
	if filepath.Dir(path) != s.Dir() {
		t.Errorf("shard escaped the claims dir: %s", path)
	}
	if strings.Contains(filepath.Base(path), "/") || strings.Contains(filepath.Base(path), "..") {
		t.Errorf("shard filename retains traversal characters: %s", filepath.Base(path))
	}
}

func TestShardPathNeverCollidesWithArchive(t *testing.T) {
	s := testStore(t)
	if s.ShardPath("archive") == s.ArchivePath() {
		t.Error("a session named \"archive\" collides with the archive ledger")
	}
}

func keys(m map[string]Episode) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
