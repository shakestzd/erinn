package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// stop_hotpath_test.go — feat-c08d1ba1 slice-6.
//
// Proves the Stop hook's ~5.45s uncontended baseline is gone: the per-item git
// auto-commit of done-but-uncommitted artifacts (reconcileDoneButUncommitted,
// profiled at ~7.2s for 200 dirty done-features) was moved OFF the Stop hot path
// onto the serve-side drain (ReconcileDoneButUncommittedForProject). These tests
// assert three things:
//
//  1. WARM-DB TIMING: Stop returns in <1s even with many dirty done-feature
//     artifacts present and NO write lock held — i.e. the synchronous reconcile
//     git loop no longer runs on Stop.
//  2. NOT-ON-HOT-PATH: Stop leaves those dirty artifacts UNCOMMITTED (it must not
//     fork git per item) — proving the cost was removed, not merely sped up.
//  3. DEFERRED WORK COMPLETES: the serve-drain entry point
//     ReconcileDoneButUncommittedForProject still auto-commits them, so nothing
//     is silently dropped.

// stopWarmDBBound is the done_when ceiling for an uncontended Stop on a warm DB.
// Slice-6's target is "Stop <1s on warm DB (no lock)". We assert a hard 1s.
const stopWarmDBBound = time.Second

// seedDirtyDoneFeatures inserts n done features into the DB and writes their
// on-disk .wipnote/<type>s/<id>.html artifacts uncommitted, so the OLD Stop path
// would fork git status/add/diff/commit once per item. Returns the project root
// (a real git repo) and the session ID Stop will resolve.
func seedDirtyDoneFeatures(t *testing.T, td *testDB, n int) (projectDir, sessionID string) {
	t.Helper()
	root := t.TempDir()
	gitInitRepo(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("feat-%08d", i)
		td.addFeature(id, "feature", "done item", "done")
		writeArtifact(t, root, "feature", id)
	}
	// Stop resolves the session via the event's SessionID; setupTestDB seeds
	// "test-sess", so reuse it as the live session for the Stop call.
	return root, "test-sess"
}

func dirtyArtifactPaths(t *testing.T, root string, n int) []string {
	t.Helper()
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		paths[i] = filepath.Join(root, ".wipnote", "features", fmt.Sprintf("feat-%08d.html", i))
	}
	return paths
}

func countDirty(t *testing.T, root string, paths []string) int {
	t.Helper()
	dirty := 0
	for _, p := range paths {
		out, _ := exec.Command("git", "-C", root, "status", "--porcelain", "--", p).CombinedOutput()
		if strings.TrimSpace(string(out)) != "" {
			dirty++
		}
	}
	return dirty
}

// TestStop_WarmDB_NoLock_UnderOneSecond is the slice-6 done_when timing test:
// with many dirty done-feature artifacts present and NO write lock held, Stop
// must return in <1s — i.e. the synchronous per-item reconcile git loop is gone.
func TestStop_WarmDB_NoLock_UnderOneSecond(t *testing.T) {
	clearNestedEnv(t)
	td := setupTestDB(t)

	const nDone = 60 // each item is a multi-fork git commit on the OLD path
	projectDir, sessionID := seedDirtyDoneFeatures(t, td, nDone)

	// Live session HTML so FinalizeSessionHTML does real (cheap) work too.
	CreateSessionHTML(projectDir, &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		CreatedAt:     time.Now().UTC(),
		Status:        "active",
		Model:         "sonnet-4",
	})

	event := &CloudEvent{
		SessionID:            sessionID,
		CWD:                  projectDir,
		LastAssistantMessage: "done",
	}

	start := time.Now()
	if _, err := Stop(event, td.DB); err != nil {
		t.Fatalf("Stop returned error on warm uncontended path: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed >= stopWarmDBBound {
		t.Fatalf("Stop took %v on a warm DB with %d dirty done-features; slice-6 bound is <%v "+
			"(the synchronous reconcile git-per-item loop must be off the hot path)",
			elapsed, nDone, stopWarmDBBound)
	}
}

// TestStop_DoesNotCommitDoneArtifacts_OnHotPath proves the cost was REMOVED, not
// merely fast: after Stop, the dirty done-feature artifacts are still
// uncommitted, because Stop no longer runs reconcileDoneButUncommitted.
func TestStop_DoesNotCommitDoneArtifacts_OnHotPath(t *testing.T) {
	clearNestedEnv(t)
	td := setupTestDB(t)

	const nDone = 5
	projectDir, sessionID := seedDirtyDoneFeatures(t, td, nDone)
	paths := dirtyArtifactPaths(t, projectDir, nDone)

	if got := countDirty(t, projectDir, paths); got != nDone {
		t.Fatalf("precondition: expected %d dirty artifacts, got %d", nDone, got)
	}

	event := &CloudEvent{SessionID: sessionID, CWD: projectDir, LastAssistantMessage: "done"}
	if _, err := Stop(event, td.DB); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Hot path must NOT have committed them (that work is deferred to serve).
	if got := countDirty(t, projectDir, paths); got != nDone {
		t.Fatalf("Stop auto-committed %d/%d done artifacts on the hot path; the per-item "+
			"git loop must be deferred to the serve drain, not run on Stop",
			nDone-got, nDone)
	}
}

// TestReconcileDrain_DeferredAutoCommit_Completes proves the deferred work is not
// silently dropped: the serve-drain entry point ReconcileDoneButUncommittedForProject
// auto-commits the same dirty done-feature artifacts the Stop hot path skipped.
func TestReconcileDrain_DeferredAutoCommit_Completes(t *testing.T) {
	clearNestedEnv(t)
	td := setupTestDB(t)

	const nDone = 5
	projectDir, _ := seedDirtyDoneFeatures(t, td, nDone)
	paths := dirtyArtifactPaths(t, projectDir, nDone)

	if got := countDirty(t, projectDir, paths); got != nDone {
		t.Fatalf("precondition: expected %d dirty artifacts, got %d", nDone, got)
	}

	// The serve-side drain performs the deterministic auto-commit off the hot path.
	committed := ReconcileDoneButUncommittedForProject(td.DB, projectDir)
	if len(committed) != nDone {
		t.Fatalf("deferred drain committed %d items, expected %d: %v", len(committed), nDone, committed)
	}

	// Every artifact must now be committed (working tree clean for each path).
	if got := countDirty(t, projectDir, paths); got != 0 {
		t.Fatalf("deferred drain left %d/%d artifacts uncommitted — deferred work was dropped",
			got, nDone)
	}

	// Idempotent: a second drain commits nothing (no wedge, no HEAD churn).
	if again := ReconcileDoneButUncommittedForProject(td.DB, projectDir); len(again) != 0 {
		t.Fatalf("second deferred drain re-committed %v; must be a no-op", again)
	}
}
