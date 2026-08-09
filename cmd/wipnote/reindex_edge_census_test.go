package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/htmlparse"
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

// censusEdgeSpec is one declared edge in the fixture: a target plus the edge
// properties the canonical HTML asserts for it. Properties are the second
// dimension of the invariant (bug-eb141e88) — edge existence surviving a
// rebuild is worth little if every attribute on the edge is silently dropped.
type censusEdgeSpec struct {
	target string
	props  map[string]string
}

// plainEdges builds specs for targets that carry no properties.
func plainEdges(targets ...string) []censusEdgeSpec {
	specs := make([]censusEdgeSpec, 0, len(targets))
	for _, tgt := range targets {
		specs = append(specs, censusEdgeSpec{target: tgt})
	}
	return specs
}

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
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "tracks"), "trk-census-001", "track", "", map[string][]censusEdgeSpec{
		"contains": plainEdges("feat-census-001", "plan-census-001"),
	})

	// Feature → planned_in → plan (plan-targeted) and → implemented_in →
	// session (session-targeted, the bug-6ec28063 direction).
	//
	// part_of and blocked_by additionally carry properties, in both encodings:
	// the attribute form for ordinary keys and the JSON escape hatch for a key
	// an attribute name cannot express.
	//
	// planned_in deliberately carries none: reindexPlanEdges owns the
	// "<featID>-planned_in-<planID>" edge_id and replaces its metadata from the
	// plan YAML on every pass, so it is not a witness for the HTML round-trip.
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "features"), "feat-census-001", "feature", "trk-census-001", map[string][]censusEdgeSpec{
		"part_of": {{
			target: "trk-census-001",
			props:  map[string]string{"slice-num": "1", "Awkward Key": "kept"},
		}},
		"planned_in": plainEdges("plan-census-001"),
		"blocked_by": {{
			target: "bug-census-001",
			props: map[string]string{
				"origin":    "plan_slice_deps",
				"plan_id":   "plan-census-001",
				"slice_num": "2",
			},
		}},
		"implemented_in": plainEdges(censusSessionID),
	})

	// The dedup heuristic's edge: similarity_score is the only confidence
	// signal in the schema and `wipnote lineage` renders it.
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "bugs"), "bug-census-001", "bug", "trk-census-001", map[string][]censusEdgeSpec{
		"part_of": plainEdges("trk-census-001"),
		"relates_to": {{
			target: "plan-census-001",
			props:  map[string]string{"tag": "needs-triage-dup", "similarity_score": "0.842"},
		}},
		"implemented_in": plainEdges(censusSessionID),
	})

	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "spikes"), "spk-census-001", "spike", "trk-census-001", map[string][]censusEdgeSpec{
		"implemented_in": plainEdges(censusSessionID),
	})

	// Plan-SOURCED edges: the direction reindexEdges never scanned at all.
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "plans"), "plan-census-001", "plan", "", map[string][]censusEdgeSpec{
		"contains":   plainEdges("feat-census-001"),
		"blocks":     plainEdges("bug-census-001"),
		"relates_to": plainEdges("spk-census-001"),
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
func writeCensusNodeHTML(t *testing.T, dir, id, nodeType, trackID string, edges map[string][]censusEdgeSpec) {
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
		for _, spec := range edges[rel] {
			fmt.Fprintf(&nav, "    <li><a href=\"%s.html\" data-relationship=%q%s>%s</a></li>\n",
				spec.target, rel, censusPropMarkup(t, spec.props), spec.target)
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

// censusPropMarkup renders edge properties into the canonical anchor markup,
// hand-written so the fixture states the on-disk format independently of the
// writer under test. Keys an attribute name cannot express take the JSON
// escape hatch, matching core/htmlparse/edge_props.go.
func censusPropMarkup(t *testing.T, props map[string]string) string {
	t.Helper()
	if len(props) == 0 {
		return ""
	}

	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	overflow := map[string]string{}
	for _, k := range keys {
		if !htmlparse.EdgePropKeyIsAttrSafe(k) {
			overflow[k] = props[k]
			continue
		}
		fmt.Fprintf(&sb, " data-%s=%q", k, html.EscapeString(props[k]))
	}
	if len(overflow) > 0 {
		payload, err := json.Marshal(overflow)
		if err != nil {
			t.Fatalf("marshal edge props: %v", err)
		}
		fmt.Fprintf(&sb, " %s=%q", htmlparse.EdgePropsAttr, html.EscapeString(string(payload)))
	}
	return sb.String()
}

// censusEdge is one row of the edge census, keyed the way the canonical HTML
// declares it. Node types are excluded so the census compares what the store
// asserts, not how a particular pass happened to label it.
type censusEdge struct {
	from string
	rel  string
	to   string
}

// censusRow is what the store holds for one edge identity: how many rows carry
// it, and the metadata JSON on it. Metadata is a value rather than part of the
// key so a dropped property reports as "metadata lost" instead of masquerading
// as one edge missing and another invented.
type censusRow struct {
	count int
	meta  string
}

func readEdgeCensus(t *testing.T, projectDir string) map[censusEdge]censusRow {
	t.Helper()
	db := openCachedDB(t, projectDir)
	defer db.Close()

	rows, err := db.Query(`SELECT from_node_id, relationship_type, to_node_id, metadata FROM graph_edges`)
	if err != nil {
		t.Fatalf("query graph_edges: %v", err)
	}
	defer rows.Close()

	census := map[censusEdge]censusRow{}
	for rows.Next() {
		var e censusEdge
		var meta sql.NullString
		if err := rows.Scan(&e.from, &e.rel, &e.to, &meta); err != nil {
			t.Fatalf("scan edge: %v", err)
		}
		row := census[e]
		row.count++
		row.meta = normalizeEdgeMeta(t, meta)
		census[e] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate edges: %v", err)
	}
	return census
}

// normalizeEdgeMeta canonicalises the metadata column for comparison: NULL and
// an empty payload both read as "", and a present payload is re-marshalled so
// key order cannot make two equal property sets compare unequal.
func normalizeEdgeMeta(t *testing.T, meta sql.NullString) string {
	t.Helper()
	if !meta.Valid || strings.TrimSpace(meta.String) == "" {
		return ""
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(meta.String), &parsed); err != nil {
		// Not a string map — compare the raw text rather than losing it.
		return meta.String
	}
	if len(parsed) == 0 {
		return ""
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("re-marshal edge metadata: %v", err)
	}
	return string(out)
}

func formatCensus(census map[censusEdge]censusRow) string {
	lines := make([]string, 0, len(census))
	for e, row := range census {
		meta := row.meta
		if meta == "" {
			meta = "-"
		}
		lines = append(lines, fmt.Sprintf("  %s -%s-> %s (x%d) meta=%s", e.from, e.rel, e.to, row.count, meta))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// deleteCacheDB used to remove the per-project SQLite cache (plus its -wal and
// -shm sidecars) so the next pass was a genuine from-scratch rebuild. No such
// file exists any more (feat-fc3cc9e0) and there is nothing to delete: every
// projection is built in memory from canonical files and destroyed with the
// process, so EVERY rebuild is already from scratch.
//
// It is kept as a no-op rather than deleted from the call sites because the
// call sites still read correctly — "destroy the index here, then rebuild" is
// exactly what the tests below are demonstrating, and the line marks where the
// destruction used to be needed to make the following rebuild cold.
func deleteCacheDB(t *testing.T, _ string) {
	t.Helper()
}

// TestReindex_EdgeCensusIdenticalOnRebuildFromUnchangedHTML is the exit
// criterion for the edge-layer repair: destroying the read index and rebuilding
// it from byte-for-byte unchanged HTML must reproduce the same edge census —
// identity AND metadata.
//
// The comparison is steady-state vs from-scratch, not run-1 vs run-2, because
// run 1 on an empty DB is itself a cold rebuild — comparing two cold runs would
// pass vacuously while both silently dropped the same edges. Reindexing twice
// first lets any warm-only edge (bug-6ec28063's implemented_in → session) land,
// so the from-scratch run has something to fail against.
//
// The same vacuity trap applies to metadata (bug-eb141e88): before edge
// properties round-tripped through HTML, every census row on both sides had
// NULL metadata and the comparison agreed about nothing. The guard below
// asserts the fixture's properties are actually in the store before comparing,
// so "both sides dropped them" cannot read as success.
func TestReindex_EdgeCensusIdenticalOnRebuildFromUnchangedHTML(t *testing.T) {
	projectDir := buildEdgeCensusFixture(t)
	setupReindexTestEnv(t, projectDir)

	runReindexInDir(t, projectDir)
	runReindexInDir(t, projectDir)
	steady := readEdgeCensus(t, projectDir)

	if len(steady) == 0 {
		t.Fatalf("steady-state census is empty — fixture declares no edges at all")
	}
	assertCensusCarriesProperties(t, steady)

	deleteCacheDB(t, projectDir)
	runReindexInDir(t, projectDir)
	rebuilt := readEdgeCensus(t, projectDir)

	for e, want := range steady {
		got := rebuilt[e]
		if got.count != want.count {
			t.Errorf("edge lost or duplicated on rebuild: %s -%s-> %s: steady=%d rebuilt=%d",
				e.from, e.rel, e.to, want.count, got.count)
			continue
		}
		if got.meta != want.meta {
			t.Errorf("edge metadata changed on rebuild: %s -%s-> %s:\n steady=%s\nrebuilt=%s",
				e.from, e.rel, e.to, metaOrNone(want.meta), metaOrNone(got.meta))
		}
	}
	for e, got := range rebuilt {
		if _, ok := steady[e]; !ok {
			t.Errorf("edge invented by rebuild: %s -%s-> %s (x%d)", e.from, e.rel, e.to, got.count)
		}
	}
	if t.Failed() {
		t.Logf("steady-state census (%d distinct edges):\n%s", len(steady), formatCensus(steady))
		t.Logf("rebuilt census (%d distinct edges):\n%s", len(rebuilt), formatCensus(rebuilt))
	}
}

func metaOrNone(meta string) string {
	if meta == "" {
		return "(none)"
	}
	return meta
}

// assertCensusCarriesProperties fails if the fixture's declared edge properties
// are not present in the store at all. Without it the census comparison would
// be satisfied by a rebuild that dropped every property on both sides — which
// is exactly the state bug-eb141e88 describes.
func assertCensusCarriesProperties(t *testing.T, census map[censusEdge]censusRow) {
	t.Helper()
	for _, want := range []struct {
		edge  censusEdge
		props map[string]string
	}{
		{
			// Dedup heuristic: similarity_score is the only confidence signal
			// in the schema, and `wipnote lineage` renders it.
			edge:  censusEdge{from: "bug-census-001", rel: "relates_to", to: "plan-census-001"},
			props: map[string]string{"tag": "needs-triage-dup", "similarity_score": "0.842"},
		},
		{
			// Origin stamp: graph.FindBottlenecks filters on it, so losing it
			// silently un-filters the bottleneck report.
			edge:  censusEdge{from: "feat-census-001", rel: "blocked_by", to: "bug-census-001"},
			props: map[string]string{"origin": "plan_slice_deps", "plan_id": "plan-census-001", "slice_num": "2"},
		},
		{
			// The JSON escape hatch travels the same road.
			edge:  censusEdge{from: "feat-census-001", rel: "part_of", to: "trk-census-001"},
			props: map[string]string{"slice-num": "1", "Awkward Key": "kept"},
		},
	} {
		row, ok := census[want.edge]
		if !ok {
			t.Fatalf("fixture edge missing from census: %s -%s-> %s",
				want.edge.from, want.edge.rel, want.edge.to)
		}
		if row.meta == "" {
			t.Fatalf("edge properties never reached the store: %s -%s-> %s has no metadata "+
				"(canonical HTML declares %v)", want.edge.from, want.edge.rel, want.edge.to, want.props)
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(row.meta), &got); err != nil {
			t.Fatalf("edge metadata is not a string map: %s: %v", row.meta, err)
		}
		if !reflect.DeepEqual(got, want.props) {
			t.Fatalf("edge properties differ from canonical HTML: %s -%s-> %s\n got %#v\nwant %#v",
				want.edge.from, want.edge.rel, want.edge.to, got, want.props)
		}
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
		if census[want].count == 0 {
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
		if census[want].count == 0 {
			t.Errorf("missing session edge after cold rebuild: %s -implemented_in-> %s", from, censusSessionID)
		}
	}
	if t.Failed() {
		t.Logf("census (%d distinct edges):\n%s", len(census), formatCensus(census))
	}
}
