package lineage

import (
	"testing"

	"github.com/shakestzd/wipnote/core/projection"
)

func TestProjectionWalkCarriesEdgeMetadata(t *testing.T) {
	snap := &projection.Snapshot{
		Nodes: map[string]projection.Node{
			"feat-root":  {ID: "feat-root", Type: "feature", Title: "Root"},
			"bug-child":  {ID: "bug-child", Type: "bug", Title: "Child"},
			"spk-parent": {ID: "spk-parent", Type: "spike", Title: "Parent"},
		},
		Out: map[string][]projection.Edge{
			"feat-root": {{
				FromID:       "feat-root",
				FromType:     "feature",
				ToID:         "bug-child",
				ToType:       "bug",
				Relationship: "relates_to",
				Metadata:     map[string]string{"similarity_score": "0.82"},
			}},
			"spk-parent": {{
				FromID:       "spk-parent",
				FromType:     "spike",
				ToID:         "feat-root",
				ToType:       "feature",
				Relationship: "spawned_from",
			}},
		},
		In: map[string][]projection.Edge{
			"feat-root": {{
				FromID:       "spk-parent",
				FromType:     "spike",
				ToID:         "feat-root",
				ToType:       "feature",
				Relationship: "spawned_from",
			}},
		},
	}
	forward := ForwardProjectionWalk(snap, "feat-root", AllRels, 3)
	if len(forward) != 1 || forward[0].ID != "bug-child" {
		t.Fatalf("forward = %#v", forward)
	}
	if forward[0].Metadata["similarity_score"] != "0.82" {
		t.Fatalf("metadata = %#v", forward[0].Metadata)
	}
	backward := BackwardProjectionWalk(snap, "feat-root", AllRels, 3)
	if len(backward) != 1 || backward[0].ID != "spk-parent" {
		t.Fatalf("backward = %#v", backward)
	}
	if backward[0].Title != "Parent" || forward[0].Title != "Child" {
		t.Fatalf("titles not resolved: forward=%#v backward=%#v", forward, backward)
	}
}
