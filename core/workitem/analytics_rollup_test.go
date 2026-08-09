//go:build sqlitelegacy

package workitem_test

import (
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// These tests exercise the recommend/bottleneck consumers of feat-7ee73444
// rollups (feat-f9118b9c) through the REAL Start/Complete/Edit code paths —
// seeding otel_signals and letting ApplyRollup write the artifact, not
// stamping the Node struct directly — so a broken wire between rollup.go and
// analytics.go shows up here the same way it would in production. seedSession
// and seedSignal are defined in rollup_test.go (same package).

// TestFindBottlenecks_FlagsReopenedItemWithMeasuredFailure is the sharp,
// non-stale case: an item is completed with a real (nonzero) failure rate,
// then reopened (Start, which never touches rollup properties — see
// TestRollup_RecomputeReplacesOnReopen in rollup_test.go). It is fresh —
// Start sets UpdatedAt to now — so the staleness bottleneck must NOT fire,
// but the thrash bottleneck must, because the rollup it carries predates
// this run.
func TestFindBottlenecks_FlagsReopenedItemWithMeasuredFailure(t *testing.T) {
	p := newTestProject(t)
	id := mustCreateFeature(t, p, "Reopened thrasher")
	seedSession(t, p.DB, "sess-thrash", id)

	yes, no := true, false
	seedSignal(t, p.DB, "sig-1", "sess-thrash", id, "log", "tool_result", 1_000_000, &yes, nil, nil, nil)
	seedSignal(t, p.DB, "sig-2", "sess-thrash", id, "log", "tool_result", 2_000_000, &no, nil, nil, nil)

	if _, err := p.Features.Complete(id); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := p.Features.Start(id); err != nil {
		t.Fatalf("Start (reopen): %v", err)
	}

	bottlenecks, err := workitem.FindBottlenecks(p.ProjectDir)
	if err != nil {
		t.Fatalf("FindBottlenecks: %v", err)
	}

	var found *workitem.Bottleneck
	for i := range bottlenecks {
		if bottlenecks[i].ItemID == id {
			found = &bottlenecks[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a thrash bottleneck for %s, got: %+v", id, bottlenecks)
	}
	if found.Rollup == nil || !found.Rollup.Measured {
		t.Errorf("bottleneck for %s carries no measured rollup", id)
	}
	if found.Rollup != nil && found.Rollup.FailureRate <= 0 {
		t.Errorf("expected a nonzero failure rate, got %v", found.Rollup.FailureRate)
	}
	if !strings.Contains(found.Reason, "failure rate") {
		t.Errorf("reason %q does not mention the failure rate", found.Reason)
	}
}

// TestFindBottlenecks_CleanReopenedItemNotFlagged is the counterpart: an item
// completed with signals but ZERO failures (a real measured clean result)
// must NOT be flagged as thrashing just because it carries a rollup. This is
// what makes the previous test non-vacuous: a check that fired on ANY
// rollup — measured-clean or not — would pass both tests.
func TestFindBottlenecks_CleanReopenedItemNotFlagged(t *testing.T) {
	p := newTestProject(t)
	id := mustCreateFeature(t, p, "Reopened clean item")
	seedSession(t, p.DB, "sess-clean", id)

	yes := true
	seedSignal(t, p.DB, "sig-1", "sess-clean", id, "log", "tool_result", 1_000_000, &yes, nil, nil, nil)

	if _, err := p.Features.Complete(id); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := p.Features.Start(id); err != nil {
		t.Fatalf("Start (reopen): %v", err)
	}

	bottlenecks, err := workitem.FindBottlenecks(p.ProjectDir)
	if err != nil {
		t.Fatalf("FindBottlenecks: %v", err)
	}
	for _, b := range bottlenecks {
		if b.ItemID == id {
			t.Errorf("clean reopened item %s incorrectly flagged as thrashing: %s", id, b.Reason)
		}
	}
}

// TestFindBottlenecks_NeverCompletedItemHasNoRollupSignal is the absence
// case: an in-progress item that has never been completed carries no rollup
// at all, and must not be flagged by the thrash check (there is nothing
// measured to flag) or panic on the nil case.
func TestFindBottlenecks_NeverCompletedItemHasNoRollupSignal(t *testing.T) {
	p := newTestProject(t)
	f, err := p.Features.Create("Never completed")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := p.Features.Start(f.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}

	bottlenecks, err := workitem.FindBottlenecks(p.ProjectDir)
	if err != nil {
		t.Fatalf("FindBottlenecks: %v", err)
	}
	for _, b := range bottlenecks {
		if b.ItemID == f.ID {
			t.Errorf("never-completed item %s incorrectly flagged: %s", f.ID, b.Reason)
		}
	}
}

// TestRecommendNextWorkIn_DeprioritizesResetItemWithMeasuredThrash exercises
// the `wipnote feature reset` shape end to end (Complete, then Edit back to
// todo — the one real path by which a todo item can carry a rollup, per
// cmd/wipnote/feature_reset.go, which never touches rollup properties): the
// reset item keeps its old rollup, sorts behind a same-priority same-track
// clean todo peer, and its Reason and Rollup both surface the history.
func TestRecommendNextWorkIn_DeprioritizesResetItemWithMeasuredThrash(t *testing.T) {
	p := newTestProject(t)
	trackID := "trk-shared"

	thrasher, err := p.Features.Create("Thrashed then reset", workitem.FeatWithTrack(trackID))
	if err != nil {
		t.Fatalf("Create thrasher: %v", err)
	}
	seedSession(t, p.DB, "sess-reset", thrasher.ID)
	yes, no := true, false
	seedSignal(t, p.DB, "sig-1", "sess-reset", thrasher.ID, "log", "tool_result", 1_000_000, &yes, nil, nil, nil)
	seedSignal(t, p.DB, "sig-2", "sess-reset", thrasher.ID, "log", "tool_result", 2_000_000, &no, nil, nil, nil)
	if _, err := p.Features.Start(thrasher.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := p.Features.Complete(thrasher.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Reset back to todo — mirrors cmd/wipnote/feature_reset.go.
	if err := p.Features.Edit(thrasher.ID).SetStatus(string(models.StatusTodo)).SetAgent("").Save(); err != nil {
		t.Fatalf("reset to todo: %v", err)
	}

	clean, err := p.Features.Create("Never attempted", workitem.FeatWithTrack(trackID))
	if err != nil {
		t.Fatalf("Create clean: %v", err)
	}

	recs, err := workitem.RecommendNextWork(p.ProjectDir)
	if err != nil {
		t.Fatalf("RecommendNextWork: %v", err)
	}

	thrasherIdx, cleanIdx := -1, -1
	var thrasherRec, cleanRec workitem.Recommendation
	for i, r := range recs {
		if r.ItemID == thrasher.ID {
			thrasherIdx = i
			thrasherRec = r
		}
		if r.ItemID == clean.ID {
			cleanIdx = i
			cleanRec = r
		}
	}
	if thrasherIdx == -1 || cleanIdx == -1 {
		t.Fatalf("expected both items recommended, got: %+v", recs)
	}
	if cleanIdx >= thrasherIdx {
		t.Errorf("clean item (idx %d) did not sort ahead of thrashed item (idx %d)", cleanIdx, thrasherIdx)
	}
	if thrasherRec.Rollup == nil || !thrasherRec.Rollup.Measured {
		t.Error("reset item lost its rollup signal")
	}
	if !strings.Contains(thrasherRec.Reason, "prior-run thrash") {
		t.Errorf("reset item reason %q does not mention prior-run thrash", thrasherRec.Reason)
	}
	if cleanRec.Rollup != nil {
		t.Errorf("never-attempted item unexpectedly carries a rollup: %+v", cleanRec.Rollup)
	}
}
