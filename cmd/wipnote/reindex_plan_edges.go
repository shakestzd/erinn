package main

import (
	"database/sql"
	"fmt"
	"path/filepath"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// reindexPlanEdges walks every plan YAML in .wipnote/plans/ and rebuilds the
// graph edges that derive from the plan structure:
//
//  1. slice.feature_id → planned_in → plan.id   (one per slice with a feature)
//  2. slice.feature_id → blocked_by → dep_slice.feature_id
//     (one per slice dep, where dep is referenced by slice number)
//
// Both edges are idempotent (INSERT OR REPLACE inside dbpkg.InsertEdge),
// matching the canonical-first guarantee of slice 9 (feat-229f3333): the YAML
// file is the source of truth, the SQLite graph_edges row is a derived index
// row that can be destroyed and rebuilt at will.
//
// The blocked_by edges are tagged with metadata.origin = graph.EdgeOriginPlanSlice
// because they encode plan authoring order, not an asserted cross-project
// dependency — the same relationship_type a human uses via `wipnote link add
// --rel blocks`. graph.FindBottlenecks filters on that tag so a plan's
// foundational slice does not read as a project-wide bottleneck merely
// because seven later slices declared it as a dep (bug-d0489158).
//
// Returns (planFiles, edgesUpserted, errors).
func reindexPlanEdges(database *sql.DB, wipnoteDir string) (int, int, int) {
	pattern := filepath.Join(wipnoteDir, "plans", "*.yaml")
	files, _ := filepath.Glob(pattern)

	var total, upserted, errCount int
	for _, f := range files {
		total++
		plan, err := planyaml.Load(f)
		if err != nil {
			errCount++
			continue
		}
		if plan == nil || plan.Meta.ID == "" {
			continue
		}
		// Build a slice-number → feature-id map for dependency lookup.
		bySliceNum := map[int]string{}
		for _, s := range plan.Slices {
			if s.Num > 0 && s.ID != "" {
				bySliceNum[s.Num] = s.ID
			}
		}
		for _, s := range plan.Slices {
			if s.ID == "" {
				continue
			}
			// planned_in: slice feature → plan
			edgeID := fmt.Sprintf("%s-planned_in-%s", s.ID, plan.Meta.ID)
			if err := dbpkg.InsertEdge(database,
				edgeID,
				s.ID, inferNodeTypeFromID(s.ID),
				plan.Meta.ID, "plan",
				string("planned_in"),
				map[string]string{"slice_num": fmt.Sprintf("%d", s.Num)},
			); err == nil {
				upserted++
			} else {
				errCount++
			}
			// blocked_by: slice feature → dep slice feature
			for _, depNum := range s.Deps {
				depID, ok := bySliceNum[depNum]
				if !ok || depID == "" {
					continue
				}
				depEdgeID := fmt.Sprintf("%s-blocked_by-%s", s.ID, depID)
				if err := dbpkg.InsertEdge(database,
					depEdgeID,
					s.ID, inferNodeTypeFromID(s.ID),
					depID, inferNodeTypeFromID(depID),
					string("blocked_by"),
					map[string]string{
						"origin":        graph.EdgeOriginPlanSlice,
						"plan_id":       plan.Meta.ID,
						"slice_num":     fmt.Sprintf("%d", s.Num),
						"dep_slice_num": fmt.Sprintf("%d", depNum),
					},
				); err == nil {
					upserted++
				} else {
					errCount++
				}
			}
		}
	}
	return total, upserted, errCount
}
