package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Edge-census regression tests for bug-d5eaf6a4 and bug-6ec28063.
//
// The canonical-store promise is that .wipnote/*.html is the single source of
// truth and SQLite is a throwaway read index. That promise only holds if a
// rebuild from unchanged HTML reproduces the edge set exactly. Two defects
// broke it silently, in opposite directions:
//
//	bug-d5eaf6a4 — reindexEdges never globbed plans/, and no pass ever put a
//	  plan ID into validIDs, so edges sourced from a plan were never scanned
//	  AND edges pointing at a plan failed indexNodeEdges' target-validity gate.
//	  Permanent loss: no number of rebuilds brought them back.
//	bug-6ec28063 — collectSessionIDs ran before reindexSessions had populated
//	  the sessions table, so on a from-scratch DB no session ID was a valid
//	  target and every work-item → implemented_in → session edge was dropped.
//	  Cold-rebuild-only: the edges reappeared on the next warm pass.
//
// The fixture below is deliberately shaped to exercise both directions at once.

const censusSessionID = "aaaabbbb-cccc-dddd-eeee-ffff00001111"

// buildEdgeCensusFixture writes a .wipnote/ tree whose canonical HTML declares
// edges in every direction that has historically been lost: plan-sourced,
// plan-targeted, and session-targeted. Returns the project root.
func buildEdgeCensusFixture(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	for _, sub := range []string{"tracks", "features", "bugs", "spikes", "plans", "sessions"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Track → contains → {feature, plan}. The plan target is the half of
	// bug-d5eaf6a4 that a plans/ glob alone would not fix.
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "tracks"), "trk-census-001", "track", "", map[string][]string{
		"contains": {"feat-census-001", "plan-census-001"},
	})

	// Feature → planned_in → plan (plan-targeted) and → implemented_in →
	// session (session-targeted, the bug-6ec28063 direction).
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "features"), "feat-census-001", "feature", "trk-census-001", map[string][]string{
		"part_of":        {"trk-census-001"},
		"planned_in":     {"plan-census-001"},
		"implemented_in": {censusSessionID},
	})

	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "bugs"), "bug-census-001", "bug", "trk-census-001", map[string][]string{
		"part_of":        {"trk-census-001"},
		"relates_to":     {"plan-census-001"},
		"implemented_in": {censusSessionID},
	})

	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "spikes"), "spk-census-001", "spike", "trk-census-001", map[string][]string{
		"implemented_in": {censusSessionID},
	})

	// Plan-SOURCED edges: the direction reindexEdges never scanned at all.
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "plans"), "plan-census-001", "plan", "", map[string][]string{
		"contains":   {"feat-census-001"},
		"blocks":     {"bug-census-001"},
		"relates_to": {"spk-census-001"},
	})
	writeFixturePlanYAML(t, filepath.Join(wipnoteDir, "plans"), "plan-census-001",
		[]planFixtureSlice{{Num: 1, ID: "feat-census-001", Title: "Only slice"}})

	writeFixtureSessionHTMLWithProject(t, filepath.Join(wipnoteDir, "sessions"), censusSessionID, projectDir,
		[]sessionEventSpec{
			{eventID: "evt-census-1", ts: "2026-08-06T10:00:00.000000",
				tool: "Bash", success: "true", text: "census event 1"},
		})

	return projectDir
}

// writeCensusNodeHTML writes a work-item HTML file carrying a
// <nav data-graph-edges> block — the canonical on-disk edge representation
// that htmlparse.ParseFile reads back.
func writeCensusNodeHTML(t *testing.T, dir, id, nodeType, trackID string, edges map[string][]string) {
	t.Helper()

	// Sort relationship types so the file content is deterministic.
	rels := make([]string, 0, len(edges))
	for rel := range edges {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	var nav strings.Builder
	nav.WriteString("<nav data-graph-edges>\n")
	for _, rel := range rels {
		fmt.Fprintf(&nav, "  <section data-edge-type=%q><ul>\n", rel)
		for _, target := range edges[rel] {
			fmt.Fprintf(&nav, "    <li><a href=\"%s.html\" data-relationship=%q>%s</a></li>\n", target, rel, target)
		}
		nav.WriteString("  </ul></section>\n")
	}
	nav.WriteString("</nav>")

	trackAttr := ""
	if trackID != "" {
		trackAttr = fmt.Sprintf(" data-track-id=%q", trackID)
	}
	now := time.Now().Format(time.RFC3339)
	content := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>%s</title></head>
<body>
  <article id="%s" data-type="%s" data-status="todo" data-priority="medium"%s
           data-created="%s" data-updated="%s">
    <header><h1>%s</h1></header>
%s
  </article>
</body>
</html>`, id, id, nodeType, trackAttr, now, now, id, nav.String())

	if err := os.WriteFile(filepath.Join(dir, id+".html"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
}

// censusEdge is one row of the edge census, keyed the way the canonical HTML
// declares it. Node types are excluded so the census compares what the store
// asserts, not how a particular pass happened to label it.
type censusEdge struct {
	from string
	rel  string
	to   string
}

func readEdgeCensus(t *testing.T, projectDir string) map[censusEdge]int {
	t.Helper()
	db := openCachedDB(t, projectDir)
	defer db.Close()

	rows, err := db.Query(`SELECT from_node_id, relationship_type, to_node_id FROM graph_edges`)
	if err != nil {
		t.Fatalf("query graph_edges: %v", err)
	}
	defer rows.Close()

	census := map[censusEdge]int{}
	for rows.Next() {
		var e censusEdge
		if err := rows.Scan(&e.from, &e.rel, &e.to); err != nil {
			t.Fatalf("scan edge: %v", err)
		}
		census[e]++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate edges: %v", err)
	}
	return census
}

func formatCensus(census map[censusEdge]int) string {
	lines := make([]string, 0, len(census))
	for e, n := range census {
		lines = append(lines, fmt.Sprintf("  %s -%s-> %s (x%d)", e.from, e.rel, e.to, n))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func deleteCacheDB(t *testing.T, projectDir string) {
	t.Helper()
	dbPath := cachedDBPath(t, projectDir)
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove cache db: %v", err)
	}
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
}

// TestReindex_EdgeCensusIdenticalOnRebuildFromUnchangedHTML is the exit
// criterion for the edge-layer repair: destroying the read index and rebuilding
// it from byte-for-byte unchanged HTML must reproduce the same edge census.
//
// The comparison is steady-state vs from-scratch, not run-1 vs run-2, because
// run 1 on an empty DB is itself a cold rebuild — comparing two cold runs would
// pass vacuously while both silently dropped the same edges. Reindexing twice
// first lets any warm-only edge (bug-6ec28063's implemented_in → session) land,
// so the from-scratch run has something to fail against.
func TestReindex_EdgeCensusIdenticalOnRebuildFromUnchangedHTML(t *testing.T) {
	projectDir := buildEdgeCensusFixture(t)
	setupReindexTestEnv(t, projectDir)

	runReindexInDir(t, projectDir)
	runReindexInDir(t, projectDir)
	steady := readEdgeCensus(t, projectDir)

	if len(steady) == 0 {
		t.Fatalf("steady-state census is empty — fixture declares no edges at all")
	}

	deleteCacheDB(t, projectDir)
	runReindexInDir(t, projectDir)
	rebuilt := readEdgeCensus(t, projectDir)

	for e, want := range steady {
		if got := rebuilt[e]; got != want {
			t.Errorf("edge lost or duplicated on rebuild: %s -%s-> %s: steady=%d rebuilt=%d",
				e.from, e.rel, e.to, want, got)
		}
	}
	for e, got := range rebuilt {
		if _, ok := steady[e]; !ok {
			t.Errorf("edge invented by rebuild: %s -%s-> %s (x%d)", e.from, e.rel, e.to, got)
		}
	}
	if t.Failed() {
		t.Logf("steady-state census (%d distinct edges):\n%s", len(steady), formatCensus(steady))
		t.Logf("rebuilt census (%d distinct edges):\n%s", len(rebuilt), formatCensus(rebuilt))
	}
}

// TestReindex_PlanEdgesSurviveRebuild pins the bug-d5eaf6a4 half directly. The
// census test above is a stability invariant and would still pass if plan edges
// were dropped consistently on every run; this one asserts they are present at
// all, in both directions.
func TestReindex_PlanEdgesSurviveRebuild(t *testing.T) {
	projectDir := buildEdgeCensusFixture(t)
	setupReindexTestEnv(t, projectDir)
	runReindexInDir(t, projectDir)

	census := readEdgeCensus(t, projectDir)
	for _, want := range []censusEdge{
		// Plan-sourced: reindexEdges must glob plans/.
		{from: "plan-census-001", rel: "contains", to: "feat-census-001"},
		{from: "plan-census-001", rel: "blocks", to: "bug-census-001"},
		{from: "plan-census-001", rel: "relates_to", to: "spk-census-001"},
		// Plan-targeted: collectPlanIDs must register the plan ID in validIDs.
		{from: "trk-census-001", rel: "contains", to: "plan-census-001"},
		{from: "feat-census-001", rel: "planned_in", to: "plan-census-001"},
		{from: "bug-census-001", rel: "relates_to", to: "plan-census-001"},
	} {
		if census[want] == 0 {
			t.Errorf("missing plan edge: %s -%s-> %s", want.from, want.rel, want.to)
		}
	}
	if t.Failed() {
		t.Logf("census (%d distinct edges):\n%s", len(census), formatCensus(census))
	}
}

// TestReindex_SessionEdgesSurviveColdRebuild pins the bug-6ec28063 half: on a
// from-scratch DB the sessions table is empty until reindexSessions runs, so
// implemented_in edges pointing at a session must not depend on a prior warm
// pass having populated it.
func TestReindex_SessionEdgesSurviveColdRebuild(t *testing.T) {
	projectDir := buildEdgeCensusFixture(t)
	setupReindexTestEnv(t, projectDir)

	// Exactly one pass, against a DB that does not exist yet.
	runReindexInDir(t, projectDir)

	census := readEdgeCensus(t, projectDir)
	for _, from := range []string{"feat-census-001", "bug-census-001", "spk-census-001"} {
		want := censusEdge{from: from, rel: "implemented_in", to: censusSessionID}
		if census[want] == 0 {
			t.Errorf("missing session edge after cold rebuild: %s -implemented_in-> %s", from, censusSessionID)
		}
	}
	if t.Failed() {
		t.Logf("census (%d distinct edges):\n%s", len(census), formatCensus(census))
	}
}
