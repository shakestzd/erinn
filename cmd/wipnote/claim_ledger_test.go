package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/claimledger"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/worktree"
)

// seedEpisode writes one closed episode straight into the canonical ledger.
func seedEpisode(t *testing.T, wipnoteDir, root, session, agent, item string, start, end time.Time, outcome claimledger.Outcome) {
	t.Helper()
	store := claimledger.NewStore(wipnoteDir)
	if _, written, err := store.Open(root, claimledger.Episode{
		WorkItemID: item,
		SessionID:  session,
		AgentID:    agent,
		StartedAt:  start,
	}); err != nil {
		t.Fatalf("open episode %s/%s: %v", agent, item, err)
	} else if !written {
		t.Fatalf("open episode %s/%s: expected a new row", agent, item)
	}
	if end.IsZero() {
		return
	}
	if _, err := store.Close(root, session, agent, item, outcome, end); err != nil {
		t.Fatalf("close episode %s/%s: %v", agent, item, err)
	}
}

// TestOverlappingAgentsResolveToTheirOwnWorkItem is the case the whole feature
// exists for: two agents holding DIFFERENT work items over OVERLAPPING windows.
// Single-slot current state cannot represent this at all — whichever agent
// wrote last wins the slot and the other's signals are misattributed.
//
//	ag-alpha  ├──────── feat-alpha ────────┤        (10:00 → 11:00)
//	ag-beta          ├──────── feat-beta ────────┤  (10:30 → 11:30)
//	                 ^        ^          ^      ^
//	                10:45   (overlap)  11:15  11:45
func TestOverlappingAgentsResolveToTheirOwnWorkItem(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	setupReindexTestEnv(t, repoRoot)

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	seedEpisode(t, wipnoteDir, "root-1", "sess-1", "ag-alpha", "feat-alpha",
		base, base.Add(60*time.Minute), claimledger.OutcomeCompleted)
	seedEpisode(t, wipnoteDir, "root-1", "sess-1", "ag-beta", "feat-beta",
		base.Add(30*time.Minute), base.Add(90*time.Minute), claimledger.OutcomeReleased)

	runReindexInDir(t, repoRoot)
	database := openCachedDB(t, repoRoot)

	cases := []struct {
		name  string
		agent string
		at    time.Time
		want  string
	}{
		{"alpha before beta starts", "ag-alpha", base.Add(15 * time.Minute), "feat-alpha"},
		{"alpha inside the overlap", "ag-alpha", base.Add(45 * time.Minute), "feat-alpha"},
		{"beta inside the overlap", "ag-beta", base.Add(45 * time.Minute), "feat-beta"},
		{"beta after alpha ended", "ag-beta", base.Add(75 * time.Minute), "feat-beta"},
		{"alpha after alpha ended", "ag-alpha", base.Add(75 * time.Minute), ""},
		{"beta before beta started", "ag-beta", base.Add(15 * time.Minute), ""},
		{"alpha before anything", "ag-alpha", base.Add(-time.Minute), ""},
		{"beta after everything", "ag-beta", base.Add(120 * time.Minute), ""},
		// The end bound is exclusive, so the instant alpha ends belongs to no
		// alpha episode — otherwise back-to-back episodes would both match.
		{"alpha exactly at its end", "ag-alpha", base.Add(60 * time.Minute), ""},
		{"alpha exactly at its start", "ag-alpha", base, "feat-alpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dbpkg.WorkItemForAgentAt(database, tc.agent, tc.at)
			if err != nil {
				t.Fatalf("WorkItemForAgentAt: %v", err)
			}
			if got != tc.want {
				t.Errorf("agent %s at %s: got %q, want %q",
					tc.agent, tc.at.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

// TestEpisodeSurvivesFullCacheWipe pins the durability promise: the read index
// is disposable, the HTML is not. Delete the whole SQLite cache, rebuild, and
// the interval must still resolve.
func TestEpisodeSurvivesFullCacheWipe(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	setupReindexTestEnv(t, repoRoot)

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	seedEpisode(t, wipnoteDir, "root-1", "sess-1", "ag-1", "feat-durable",
		base, base.Add(time.Hour), claimledger.OutcomeCompleted)

	runReindexInDir(t, repoRoot)
	database := openCachedDB(t, repoRoot)
	if got, _ := dbpkg.WorkItemForAgentAt(database, "ag-1", base.Add(30*time.Minute)); got != "feat-durable" {
		t.Fatalf("before wipe: got %q, want feat-durable", got)
	}
	database.Close()

	// Nuke the entire derived index, WAL sidecars included.
	dbPath := os.Getenv("WIPNOTE_DB_PATH")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s%s: %v", dbPath, suffix, err)
		}
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("cache was not actually wiped: %v", err)
	}

	runReindexInDir(t, repoRoot)
	rebuilt := openCachedDB(t, repoRoot)
	got, err := dbpkg.WorkItemForAgentAt(rebuilt, "ag-1", base.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("WorkItemForAgentAt after rebuild: %v", err)
	}
	if got != "feat-durable" {
		t.Errorf("after full cache wipe + rebuild: got %q, want feat-durable — the episode did not survive", got)
	}
}

// TestClaimLedgerDirectoryIsGitTracked guards the storage decision that the
// brief called out explicitly: the ledger must NOT land somewhere gitignored,
// or it would not survive a fresh clone and the whole feature would be moot.
func TestClaimLedgerDirectoryIsGitTracked(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	// Install the REAL managed ignore block rather than a hand-written copy, so
	// the assertion tracks production rules if they ever change.
	worktree.EnsureWipnoteGitignore(repoRoot)
	if _, err := os.Stat(filepath.Join(wipnoteDir, ".gitignore")); err != nil {
		t.Fatalf("managed .wipnote/.gitignore was not installed: %v", err)
	}

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	seedEpisode(t, wipnoteDir, "root-1", "sess-1", "ag-1", "feat-a", base, time.Time{}, "")

	store := claimledger.NewStore(wipnoteDir)
	shard := store.ShardPath("root-1")
	if _, err := os.Stat(shard); err != nil {
		t.Fatalf("shard not written: %v", err)
	}

	if ignored := gitCheckIgnore(t, repoRoot, shard); ignored {
		t.Errorf("claim ledger shard %s is gitignored — history would not survive a fresh clone",
			store.RelPath(shard))
	}
	// The lock sidecar is derived and MUST be ignored, or every session would
	// dirty the tree with a zero-byte file.
	if ignored := gitCheckIgnore(t, repoRoot, shard+".lock"); !ignored {
		t.Errorf("lock sidecar %s.lock is NOT gitignored — it would be committed",
			store.RelPath(shard))
	}
}

// TestArchivedEpisodesStayQueryable pins the archive acceptance criterion:
// compaction moves rows out of the shard into archive.html, and the read index
// must still resolve them.
func TestArchivedEpisodesStayQueryable(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	setupReindexTestEnv(t, repoRoot)

	base := time.Now().UTC().Add(-60 * 24 * time.Hour)
	seedEpisode(t, wipnoteDir, "root-old", "sess-old", "ag-old", "feat-archived",
		base, base.Add(time.Hour), claimledger.OutcomeCompleted)

	store := claimledger.NewStore(wipnoteDir)
	dead := func(string) bool { return false } // nothing is live
	res, err := store.Archive(time.Now().UTC().Add(-24*time.Hour), dead, true)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 archive candidate, got %d", len(res.Candidates))
	}
	if _, statErr := os.Stat(store.ShardPath("root-old")); !os.IsNotExist(statErr) {
		t.Errorf("shard was not removed after archiving: %v", statErr)
	}
	if _, statErr := os.Stat(store.ArchivePath()); statErr != nil {
		t.Fatalf("archive ledger missing: %v", statErr)
	}

	runReindexInDir(t, repoRoot)
	database := openCachedDB(t, repoRoot)
	got, qErr := dbpkg.WorkItemForAgentAt(database, "ag-old", base.Add(30*time.Minute))
	if qErr != nil {
		t.Fatalf("WorkItemForAgentAt: %v", qErr)
	}
	if got != "feat-archived" {
		t.Errorf("archived episode is no longer queryable: got %q, want feat-archived", got)
	}
}

// TestArchiveSkipsLiveAndOpenShards proves both eligibility guards bite: a live
// session and an open episode each block compaction on their own.
func TestArchiveSkipsLiveAndOpenShards(t *testing.T) {
	_, wipnoteDir := setupArchiveRepo(t)
	old := time.Now().UTC().Add(-60 * 24 * time.Hour)

	seedEpisode(t, wipnoteDir, "root-live", "sess-live", "ag-1", "feat-live",
		old, old.Add(time.Hour), claimledger.OutcomeCompleted)
	seedEpisode(t, wipnoteDir, "root-open", "sess-open", "ag-2", "feat-open",
		old, time.Time{}, "")
	seedEpisode(t, wipnoteDir, "root-done", "sess-done", "ag-3", "feat-done",
		old, old.Add(time.Hour), claimledger.OutcomeCompleted)

	store := claimledger.NewStore(wipnoteDir)
	live := func(root string) bool { return root == "root-live" }
	res, err := store.Archive(time.Now().UTC().Add(-24*time.Hour), live, false)
	if err != nil {
		t.Fatalf("Archive dry run: %v", err)
	}
	if len(res.Candidates) != 1 {
		var got []string
		for _, c := range res.Candidates {
			got = append(got, c.RootSessionID)
		}
		t.Fatalf("expected only root-done eligible, got %v", got)
	}
	if res.Candidates[0].RootSessionID != "root-done" {
		t.Errorf("wrong candidate: got %s, want root-done", res.Candidates[0].RootSessionID)
	}
}

// TestReconcileClosesDeadSessionEpisodes covers the "session died without
// releasing" answer: an episode left open by a session that is no longer live
// gets an end with outcome "expired".
func TestReconcileClosesDeadSessionEpisodes(t *testing.T) {
	_, wipnoteDir := setupArchiveRepo(t)
	start := time.Now().UTC().Add(-2 * time.Hour)

	seedEpisode(t, wipnoteDir, "root-dead", "sess-dead", "ag-1", "feat-orphan", start, time.Time{}, "")
	seedEpisode(t, wipnoteDir, "root-live", "sess-live", "ag-2", "feat-running", start, time.Time{}, "")

	store := claimledger.NewStore(wipnoteDir)
	live := func(root string) bool { return root == "root-live" }
	res, err := store.Reconcile(live, time.Now().UTC())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Episodes != 1 || res.Sessions != 1 {
		t.Fatalf("Reconcile closed %d episode(s) in %d session(s), want 1 and 1", res.Episodes, res.Sessions)
	}

	dead, err := store.ReadShard("root-dead")
	if err != nil {
		t.Fatalf("ReadShard root-dead: %v", err)
	}
	if len(dead) != 1 || dead[0].IsOpen() {
		t.Fatalf("dead session's episode was not closed: %+v", dead)
	}
	if dead[0].Outcome != claimledger.OutcomeExpired {
		t.Errorf("outcome: got %q, want %q", dead[0].Outcome, claimledger.OutcomeExpired)
	}

	alive, err := store.ReadShard("root-live")
	if err != nil {
		t.Fatalf("ReadShard root-live: %v", err)
	}
	if len(alive) != 1 || !alive[0].IsOpen() {
		t.Errorf("a LIVE session's episode was closed by reconcile: %+v", alive)
	}
}

// TestOpenEpisodeIsQueryableAsOpenEnded pins the read semantics for an episode
// whose session has not yet been reconciled: it matches any instant at or after
// its start, so attribution keeps working for a running agent.
func TestOpenEpisodeIsQueryableAsOpenEnded(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	setupReindexTestEnv(t, repoRoot)

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	seedEpisode(t, wipnoteDir, "root-1", "sess-1", "ag-1", "feat-running", base, time.Time{}, "")

	runReindexInDir(t, repoRoot)
	database := openCachedDB(t, repoRoot)

	if got, _ := dbpkg.WorkItemForAgentAt(database, "ag-1", base.Add(99*time.Hour)); got != "feat-running" {
		t.Errorf("open episode long after its start: got %q, want feat-running", got)
	}
	if got, _ := dbpkg.WorkItemForAgentAt(database, "ag-1", base.Add(-time.Second)); got != "" {
		t.Errorf("open episode BEFORE its start: got %q, want empty", got)
	}
}

// TestSameAgentIDInDifferentSessionsIsDisambiguated guards a real ambiguity:
// "__root__" is the agent id of EVERY root session, so agent alone cannot
// identify a claimant when two sessions run concurrently.
func TestSameAgentIDInDifferentSessionsIsDisambiguated(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	setupReindexTestEnv(t, repoRoot)

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	root := dbpkg.AgentRootSentinel
	seedEpisode(t, wipnoteDir, "root-a", "sess-a", root, "feat-a", base, base.Add(time.Hour), claimledger.OutcomeCompleted)
	seedEpisode(t, wipnoteDir, "root-b", "sess-b", root, "feat-b", base, base.Add(time.Hour), claimledger.OutcomeCompleted)

	runReindexInDir(t, repoRoot)
	database := openCachedDB(t, repoRoot)

	at := base.Add(30 * time.Minute)
	gotA, err := dbpkg.WorkItemForSessionAgentAt(database, "sess-a", root, at)
	if err != nil {
		t.Fatalf("session-scoped query: %v", err)
	}
	if gotA != "feat-a" {
		t.Errorf("sess-a/%s: got %q, want feat-a", root, gotA)
	}
	gotB, err := dbpkg.WorkItemForSessionAgentAt(database, "sess-b", root, at)
	if err != nil {
		t.Fatalf("session-scoped query: %v", err)
	}
	if gotB != "feat-b" {
		t.Errorf("sess-b/%s: got %q, want feat-b", root, gotB)
	}
}

// TestReindexPurgesEpisodesForDeletedLedger proves the read index does not keep
// ghosts: removing the canonical file removes the rows on the next pass.
func TestReindexPurgesEpisodesForDeletedLedger(t *testing.T) {
	repoRoot, wipnoteDir := setupArchiveRepo(t)
	setupReindexTestEnv(t, repoRoot)

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	seedEpisode(t, wipnoteDir, "root-1", "sess-1", "ag-1", "feat-ghost", base, base.Add(time.Hour), claimledger.OutcomeCompleted)

	runReindexInDir(t, repoRoot)
	database := openCachedDB(t, repoRoot)
	if got, _ := dbpkg.WorkItemForAgentAt(database, "ag-1", base.Add(30*time.Minute)); got != "feat-ghost" {
		t.Fatalf("setup: got %q, want feat-ghost", got)
	}
	database.Close()

	store := claimledger.NewStore(wipnoteDir)
	if err := os.Remove(store.ShardPath("root-1")); err != nil {
		t.Fatalf("remove shard: %v", err)
	}

	runReindexInDir(t, repoRoot)
	after := openCachedDB(t, repoRoot)
	if got, _ := dbpkg.WorkItemForAgentAt(after, "ag-1", base.Add(30*time.Minute)); got != "" {
		t.Errorf("episode survived deletion of its canonical file: got %q", got)
	}
}

// gitCheckIgnore reports whether git would ignore path in repoRoot. It asks
// git rather than re-implementing pattern matching, so the assertion is against
// the real rules the production tree uses.
func gitCheckIgnore(t *testing.T, repoRoot, path string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repoRoot, "check-ignore", "-q", "--", path)
	err := cmd.Run()
	if err == nil {
		return true // exit 0 == ignored
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false // exit 1 == not ignored
	}
	t.Fatalf("git check-ignore %s: %v", path, err)
	return false
}
