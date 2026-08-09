package workitem_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// The three analytics entry points used by `wipnote recommend` each re-read the
// whole corpus from disk (bug-1a51ab15), so one invocation paid four full
// parses. The fix threads an already-loaded node set through the *In variants.
//
// These tests pin the property that actually matters — the *In variants derive
// everything from the slice they are handed and never touch the filesystem — by
// deleting the project directory after loading. Any regression that reintroduces
// a disk read inside them turns the populated results below into empty ones.

// loadThenDeleteProject creates the given work items, loads the corpus once,
// then removes the project directory so any later disk read finds nothing.
func loadThenDeleteProject(t *testing.T, build func(p *workitem.Project)) []*models.Node {
	t.Helper()
	p := newTestProject(t)
	build(p)

	nodes, err := graph.LoadAll(p.ProjectDir)
	if err != nil {
		t.Fatalf("graph.LoadAll: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("fixture loaded zero nodes — test would be vacuous")
	}

	if err := os.RemoveAll(p.ProjectDir); err != nil {
		t.Fatalf("remove project dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.ProjectDir, "features")); !os.IsNotExist(err) {
		t.Fatalf("project dir still readable after removal — test would be vacuous")
	}
	return nodes
}

func TestFindBottlenecksIn_UsesSuppliedNodesNotDisk(t *testing.T) {
	const trackID = "trk-overload"
	nodes := loadThenDeleteProject(t, func(p *workitem.Project) {
		for range [3]int{} {
			f, err := p.Features.Create("Track Feature", workitem.FeatWithTrack(trackID))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if _, err := p.Features.Start(f.ID); err != nil {
				t.Fatalf("Start: %v", err)
			}
		}
	})

	bottlenecks := workitem.FindBottlenecksIn(nodes)

	found := false
	for _, b := range bottlenecks {
		if b.ItemID == trackID && b.Type == "track" {
			found = true
		}
	}
	if !found {
		t.Errorf("FindBottlenecksIn lost the overloaded track %s after the project dir was deleted "+
			"— it is reading from disk instead of the supplied node set; got %+v", trackID, bottlenecks)
	}
}

func TestRecommendNextWorkIn_UsesSuppliedNodesNotDisk(t *testing.T) {
	nodes := loadThenDeleteProject(t, func(p *workitem.Project) {
		if _, err := p.Features.Create("Todo Feature"); err != nil {
			t.Fatalf("Create: %v", err)
		}
	})

	recs := workitem.RecommendNextWorkIn(nodes)

	if len(recs) == 0 {
		t.Error("RecommendNextWorkIn returned nothing after the project dir was deleted " +
			"— it is reading from disk instead of the supplied node set")
	}
}

func TestGetParallelWorkIn_UsesSuppliedNodesNotDisk(t *testing.T) {
	const trackID = "trk-parallel"
	nodes := loadThenDeleteProject(t, func(p *workitem.Project) {
		for range [2]int{} {
			if _, err := p.Features.Create("Parallel Feature", workitem.FeatWithTrack(trackID)); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
	})

	sets := workitem.GetParallelWorkIn(nodes)

	found := false
	for _, s := range sets {
		if s.TrackID == trackID && len(s.Items) == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("GetParallelWorkIn lost the parallel set for %s after the project dir was deleted "+
			"— it is reading from disk instead of the supplied node set; got %+v", trackID, sets)
	}
}
