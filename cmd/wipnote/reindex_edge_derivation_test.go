package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/arch"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// Canonical-versus-index edge census (feat-d1439606).
//
// TestReindex_EdgeCensusIdenticalOnRebuildFromUnchangedHTML pins rebuild-to-
// rebuild IDEMPOTENCE: destroy the index, rebuild, get the same edges. That is
// a real invariant and it caught real bugs, but it is satisfied by a reindex
// that drops the same edge every single time. An 828-declared-versus-82-indexed
// gap survived it for months (bug-10e166d8) precisely because both sides of its
// comparison were reindex output.
//
// This test compares reindex output against the CANONICAL SOURCES instead. It
// re-derives, from the fixture's own .wipnote/ files, the edge set graph_edges
// is contractually obliged to hold, then diffs that against the table in both
// directions:
//
//	→ nothing declared-and-transformable may be missing from the index
//	→ nothing in the index may be underivable from a canonical source
//
// The second direction is what makes a rogue writer fail a test instead of
// accumulating silently — the drift class that motivated deleting the table.
//
// The derivation contract has four canonical sources and one gate:
//
//  1. work-item and plan HTML  <nav data-graph-edges>  — human-declared edges
//  2. plan YAML slices          — planned_in + blocked_by slice ordering
//  3. the architecture ledger   — learned_from / has_learning links
//  4. the target-validity gate  — live target indexes as declared; absent but
//     session-shaped target is TOMBSTONED, not dropped; absent work-item target
//     is a genuine dangling reference and IS dropped.
//
// Session records are the fourth source in the arch card's framing, but they
// contribute no rows of their own: reversing implemented_in is done at query
// time (see SessionImplements), never stored. Nothing here expects such a row,
// so if one is ever written again it fails as underivable — which is the point.

// Fixture identities. Named rather than generated so a failure message points
// at a case, not at a UUID.
const (
	deriveTrackID   = "trk-derive-001"
	deriveFeatureA  = "feat-derive-001"
	deriveFeatureB  = "feat-derive-002"
	deriveBugID     = "bug-derive-001"
	deriveSpikeID   = "spk-derive-001"
	derivePlanID    = "plan-derive-001"
	deriveArchSlug  = "derive-gate-card"
	deriveArchNode  = "arch:" + deriveArchSlug
	deriveGhostItem = "feat-ghost-9999" // declared, never exists — must be dropped

	// Two session-shaped targets with opposite fates. The live one has a
	// session HTML file, so reindexSessions puts it in the sessions table and
	// collectSessionIDs registers it. The pruned one has no file anywhere —
	// exactly the state bug-10e166d8 measured for 153 of 156 declared targets.
	deriveLiveSession   = "11112222-3333-4444-5555-666677778888"
	derivePrunedSession = "99998888-7777-6666-5555-444433332222"

	// The other session-id shape in use — 28 undashed hex chars. Most live
	// records in this repo's .wipnote/sessions/ use it, so the end-to-end path
	// must cover it and not just the dashed UUID.
	derivePrunedSessionAlt = "019f424e188c60f444c8eaca668b"

	// A session destined to have a row in the canonical sessions ledger
	// (feat-1b08a194). Today it is indistinguishable from a pruned session —
	// no ledger exists, so it tombstones — and the expectation below says so.
	//
	// WHEN feat-1b08a194 LANDS this case flips: give it a ledger row, and
	// deriveExpectedEdges must move it out of deriveSessionShapedTargets into
	// the valid-id set, at which point the edge indexes as EdgeTargetLive with
	// NO tombstone marker. That is the acceptance criterion for the gate half
	// of that feature, and it should fail here first.
	deriveLedgerSession = "55556666-7777-8888-9999-aaaabbbbcccc"
)

// deriveSessionShapedTargets is the fixture's own statement of which targets
// are sessions. The expectation deliberately does NOT call the production
// shape check: if it did, a gate that classified `feat-ghost-9999` as a session
// would classify it that way on both sides and the test would agree with the
// bug.
//
// deriveLedgerSession sits here only because no ledger exists yet — see its
// declaration for what moves when feat-1b08a194 lands.
var deriveSessionShapedTargets = map[string]bool{
	deriveLiveSession:      true,
	derivePrunedSession:    true,
	derivePrunedSessionAlt: true,
	deriveLedgerSession:    true,
}

// buildEdgeDerivationFixture writes a synthetic .wipnote/ tree covering every
// case in the derivation contract, and returns the project root. It is
// synthetic rather than the live repo so the expected set is a closed,
// deterministic function of files this test wrote.
func buildEdgeDerivationFixture(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	for _, sub := range []string{"tracks", "features", "bugs", "spikes", "plans", "sessions", "arch"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "tracks"), deriveTrackID, "track", "",
		map[string][]censusEdgeSpec{
			"contains": plainEdges(deriveFeatureA, deriveFeatureB, deriveBugID, deriveSpikeID, derivePlanID),
		})

	// Feature A carries the full spread: a normal edge with properties, a
	// planned_in the plan YAML will overwrite, a live-session edge, a
	// pruned-session edge (tombstone expected), and a dangling work-item
	// reference (drop expected).
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "features"), deriveFeatureA, "feature", deriveTrackID,
		map[string][]censusEdgeSpec{
			"part_of": {{
				target: deriveTrackID,
				props:  map[string]string{"asserted-by": "human"},
			}},
			"planned_in":     plainEdges(derivePlanID),
			"implemented_in": plainEdges(deriveLiveSession, derivePrunedSession),
			"relates_to":     plainEdges(deriveGhostItem),
		})

	// Feature B is slice 2 of the plan and depends on slice 1, so the plan-YAML
	// pass must synthesize a blocked_by edge that no HTML declares.
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "features"), deriveFeatureB, "feature", deriveTrackID,
		map[string][]censusEdgeSpec{
			"part_of":    plainEdges(deriveTrackID),
			"planned_in": plainEdges(derivePlanID),
		})

	// A pruned-session edge whose source is a bug rather than a feature: the
	// tombstone is a property of the target, not of the source's type.
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "bugs"), deriveBugID, "bug", deriveTrackID,
		map[string][]censusEdgeSpec{
			"part_of": plainEdges(deriveTrackID),
			"caused_by": {{
				target: deriveFeatureA,
				props:  map[string]string{"tag": "regression", "similarity_score": "0.910"},
			}},
			// Both pruned-session shapes: the dashed UUID and the 28-hex form.
			// Covering only one would leave the other silently erased.
			"implemented_in": plainEdges(derivePrunedSession, derivePrunedSessionAlt),
		})

	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "spikes"), deriveSpikeID, "spike", deriveTrackID,
		map[string][]censusEdgeSpec{
			"part_of":    plainEdges(deriveTrackID),
			"relates_to": plainEdges(deriveBugID),
			// Destined for a canonical sessions-ledger row; tombstoned until
			// that ledger exists.
			"implemented_in": plainEdges(deriveLedgerSession),
		})

	// Plan-SOURCED HTML edges, plus the YAML the slice pass reads.
	writeCensusNodeHTML(t, filepath.Join(wipnoteDir, "plans"), derivePlanID, "plan", "",
		map[string][]censusEdgeSpec{
			"contains": plainEdges(deriveFeatureA, deriveFeatureB),
		})
	writeFixturePlanYAML(t, filepath.Join(wipnoteDir, "plans"), derivePlanID, []planFixtureSlice{
		{Num: 1, ID: deriveFeatureA, Title: "Foundation slice"},
		{Num: 2, ID: deriveFeatureB, Title: "Dependent slice", Deps: []int{1}},
	})

	// Architecture ledger: a learning link produces a learned_from/has_learning
	// pair that no work-item HTML declares and no writer should ever put there.
	writeFixtureArchCard(t, filepath.Join(wipnoteDir, "arch"), deriveArchSlug, deriveFeatureA)

	// Only the live session gets an HTML file. The pruned one is declared by
	// canonical HTML and exists nowhere else.
	writeFixtureSessionHTMLWithProject(t, filepath.Join(wipnoteDir, "sessions"), deriveLiveSession, projectDir,
		[]sessionEventSpec{
			{eventID: "evt-derive-1", ts: "2026-08-06T10:00:00.000000",
				tool: "Bash", success: "true", text: "derive event 1"},
		})

	return projectDir
}

// writeFixtureArchCard writes a legacy-format architecture card linking to one
// work item. The ledger and the legacy directory feed the same reindex pass.
func writeFixtureArchCard(t *testing.T, dir, slug, link string) {
	t.Helper()
	content := fmt.Sprintf(`---
name: %s
kind: hazard
created_by: %s
links:
  - %s
---
The target-validity gate refuses dangling references by design.
`, slug, link, link)
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write arch card %s: %v", slug, err)
	}
}

// --- the derivation, re-expressed ---------------------------------------------

// derivedEdgeSet maps an edge identity to its canonical metadata JSON ("" for
// none). Keyed by identity rather than by row so a lost property reports as
// "metadata diverges" instead of masquerading as one edge missing plus one
// invented.
type derivedEdgeSet map[censusEdge]string

// deriveExpectedEdges walks the fixture's canonical files and applies the
// documented derivation transform, in the order reindex applies its passes.
// This is a re-expression of the contract, not a call into it: the production
// passes are never invoked, so a pass that stops running fails this test.
func deriveExpectedEdges(t *testing.T, projectDir string) derivedEdgeSet {
	t.Helper()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")

	validIDs := deriveValidIDs(t, wipnoteDir)
	expected := derivedEdgeSet{}

	// Pass 1 — human-declared edges in work-item and plan HTML, through the
	// target-validity gate.
	for _, sub := range []string{"tracks", "features", "bugs", "spikes", "plans"} {
		for _, node := range deriveParseNodes(t, filepath.Join(wipnoteDir, sub)) {
			if !validIDs[node.ID] {
				// An edge from an unindexed source is not a canonical
				// declaration; reindexEdges gates the source too.
				continue
			}
			for _, edges := range node.Edges {
				for _, e := range edges {
					target := e.TargetID
					props := e.Properties
					switch {
					case validIDs[target]:
						// Live target: exactly as declared. Unchanged by the
						// tombstone policy — the three surviving live-session
						// edges in the real corpus must not move.
					case deriveSessionShapedTargets[target]:
						props = deriveWithTombstone(props)
					default:
						continue // dangling work-item reference — dropped
					}
					expected[censusEdge{from: node.ID, rel: string(e.Relationship), to: target}] =
						deriveMarshalMeta(t, props)
				}
			}
		}
	}

	// Pass 2 — architecture ledger learning links. Emitted in both directions
	// and NOT gated on validIDs, matching reindexArchCards.
	for _, card := range deriveParseArchCards(t, filepath.Join(wipnoteDir, "arch")) {
		node := "arch:" + card.Name
		for _, link := range card.Links {
			if !deriveIsWorkItemID(link) {
				continue
			}
			expected[censusEdge{from: node, rel: "learned_from", to: link}] = ""
			expected[censusEdge{from: link, rel: "has_learning", to: node}] = ""
		}
	}

	// Pass 3 — plan YAML slice ordering. Runs last in reindex, so its metadata
	// WINS over an HTML-declared planned_in edge of the same identity.
	for _, plan := range derivePlans(t, filepath.Join(wipnoteDir, "plans")) {
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
			expected[censusEdge{from: s.ID, rel: "planned_in", to: plan.Meta.ID}] =
				deriveMarshalMeta(t, map[string]string{"slice_num": fmt.Sprintf("%d", s.Num)})
			for _, depNum := range s.Deps {
				depID := bySliceNum[depNum]
				if depID == "" {
					continue
				}
				expected[censusEdge{from: s.ID, rel: "blocked_by", to: depID}] =
					deriveMarshalMeta(t, map[string]string{
						"origin":        "plan_slice_deps",
						"plan_id":       plan.Meta.ID,
						"slice_num":     fmt.Sprintf("%d", s.Num),
						"dep_slice_num": fmt.Sprintf("%d", depNum),
					})
			}
		}
	}

	return expected
}

// deriveValidIDs rebuilds the target-validity whitelist from files on disk:
// every work item, track, and plan with a canonical file, plus every session
// with a session record. Sessions are the only ephemeral members, which is why
// they are the only ones a tombstone can apply to.
func deriveValidIDs(t *testing.T, wipnoteDir string) map[string]bool {
	t.Helper()
	valid := map[string]bool{}
	for _, sub := range []string{"tracks", "features", "bugs", "spikes", "plans"} {
		for _, node := range deriveParseNodes(t, filepath.Join(wipnoteDir, sub)) {
			valid[node.ID] = true
		}
	}
	// A plan can exist as YAML before its HTML is rendered.
	yamls, _ := filepath.Glob(filepath.Join(wipnoteDir, "plans", "*.yaml"))
	for _, f := range yamls {
		valid[strings.TrimSuffix(filepath.Base(f), ".yaml")] = true
	}
	sessions, _ := filepath.Glob(filepath.Join(wipnoteDir, "sessions", "*.html"))
	for _, f := range sessions {
		valid[strings.TrimSuffix(filepath.Base(f), ".html")] = true
	}
	return valid
}

func deriveParseNodes(t *testing.T, dir string) []*models.Node {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.html"))
	sort.Strings(files)
	out := make([]*models.Node, 0, len(files))
	for _, f := range files {
		node, err := htmlparse.ParseFile(f)
		if err != nil {
			t.Fatalf("parse canonical node %s: %v", f, err)
		}
		out = append(out, node)
	}
	return out
}

func deriveParseArchCards(t *testing.T, dir string) []*arch.Card {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	sort.Strings(files)
	out := make([]*arch.Card, 0, len(files))
	for _, f := range files {
		card, err := arch.ParseFile(f)
		if err != nil {
			t.Fatalf("parse arch card %s: %v", f, err)
		}
		out = append(out, card)
	}
	return out
}

func derivePlans(t *testing.T, dir string) []*planyaml.PlanYAML {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	sort.Strings(files)
	out := make([]*planyaml.PlanYAML, 0, len(files))
	for _, f := range files {
		plan, err := planyaml.Load(f)
		if err != nil {
			t.Fatalf("load plan yaml %s: %v", f, err)
		}
		if plan != nil && plan.Meta.ID != "" {
			out = append(out, plan)
		}
	}
	return out
}

// deriveIsWorkItemID mirrors archLinkNodeType's prefix test: a link the arch
// pass cannot type produces no edge.
func deriveIsWorkItemID(id string) bool {
	for _, p := range []string{"feat-", "bug-", "spk-", "trk-", "plan-", "spec-", "spc-"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// deriveWithTombstone states the tombstone marker literally rather than
// importing the production constant, so a silent rename of the key is a test
// failure and a deliberate one is a visible diff here.
func deriveWithTombstone(props map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range props {
		out[k] = v
	}
	out["tombstoned"] = "session"
	return out
}

func deriveMarshalMeta(t *testing.T, props map[string]string) string {
	t.Helper()
	if len(props) == 0 {
		return ""
	}
	raw, err := json.Marshal(props)
	if err != nil {
		t.Fatalf("marshal expected edge metadata: %v", err)
	}
	return string(raw)
}

// --- the tests ----------------------------------------------------------------

// TestReindex_IndexMatchesCanonicalDerivation is the contract test: after a full
// rebuild, graph_edges must equal canonical declarations plus the documented
// derivations, with nothing extra in either direction.
func TestReindex_IndexMatchesCanonicalDerivation(t *testing.T) {
	if testing.Short() {
		t.Skip("drives reindex integration flow")
	}

	projectDir := buildEdgeDerivationFixture(t)
	setupReindexTestEnv(t, projectDir)
	runReindexInDir(t, projectDir)

	expected := deriveExpectedEdges(t, projectDir)
	actual := readEdgeCensus(t, projectDir)

	assertDerivationCasesPresent(t, expected)
	compareDerivationToIndex(t, expected, actual)
}

// TestReindex_CanonicalDerivationSurvivesSecondPass runs the comparison again
// after a second reindex over unchanged files. purgeStaleEntries runs BEFORE
// the edge passes re-insert anything, so a purge that judged targets by
// validIDs alone would delete every tombstone here and the policy would undo
// itself on the second rebuild.
func TestReindex_CanonicalDerivationSurvivesSecondPass(t *testing.T) {
	if testing.Short() {
		t.Skip("drives reindex integration flow")
	}

	projectDir := buildEdgeDerivationFixture(t)
	setupReindexTestEnv(t, projectDir)
	runReindexInDir(t, projectDir)
	runReindexInDir(t, projectDir)

	expected := deriveExpectedEdges(t, projectDir)
	actual := readEdgeCensus(t, projectDir)

	assertDerivationCasesPresent(t, expected)
	compareDerivationToIndex(t, expected, actual)
}

// TestReindex_TombstoneDistinguishedFromDroppedEdge is the acceptance criterion
// from bug-10e166d8, stated directly rather than inferred from a set diff: a
// pruned session survives WITH a marker, a live session survives WITHOUT one,
// and a nonexistent work item does not survive at all.
func TestReindex_TombstoneDistinguishedFromDroppedEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("drives reindex integration flow")
	}

	projectDir := buildEdgeDerivationFixture(t)
	setupReindexTestEnv(t, projectDir)
	runReindexInDir(t, projectDir)
	census := readEdgeCensus(t, projectDir)

	pruned := censusEdge{from: deriveFeatureA, rel: "implemented_in", to: derivePrunedSession}
	row, ok := census[pruned]
	if !ok {
		t.Fatalf("edge to a pruned session was DROPPED, not tombstoned: %s -%s-> %s\n"+
			"canonical HTML declares it and the work item is permanent; only the session aged out",
			pruned.from, pruned.rel, pruned.to)
	}
	var meta map[string]string
	if row.meta != "" {
		if err := json.Unmarshal([]byte(row.meta), &meta); err != nil {
			t.Fatalf("tombstoned edge metadata is not a string map: %s: %v", row.meta, err)
		}
	}
	if meta["tombstoned"] != "session" {
		t.Errorf("edge to a pruned session is not marked: %s -%s-> %s has metadata %s;\n"+
			"readers cannot tell it from a live neighbour whose title merely failed to load",
			pruned.from, pruned.rel, pruned.to, metaOrNone(row.meta))
	}

	// A live session must behave exactly as it did before the policy existed.
	live := censusEdge{from: deriveFeatureA, rel: "implemented_in", to: deriveLiveSession}
	liveRow, ok := census[live]
	if !ok {
		t.Errorf("edge to a LIVE session went missing: %s -%s-> %s", live.from, live.rel, live.to)
	} else if liveRow.meta != "" {
		t.Errorf("edge to a LIVE session gained metadata it never declared: %s -%s-> %s meta=%s",
			live.from, live.rel, live.to, liveRow.meta)
	}

	// A dangling work-item reference is still a defect, not a tombstone.
	ghost := censusEdge{from: deriveFeatureA, rel: "relates_to", to: deriveGhostItem}
	if row, ok := census[ghost]; ok {
		t.Errorf("dangling work-item reference was preserved: %s -%s-> %s (x%d meta=%s);\n"+
			"only session-shaped targets may tombstone — %s is not an ephemeral node, it is absent",
			ghost.from, ghost.rel, ghost.to, row.count, metaOrNone(row.meta), deriveGhostItem)
	}

	// The handoff case. Tombstoned today because no canonical sessions ledger
	// exists; when feat-1b08a194 lands this assertion is the one that should
	// fail first, and the fix is to give the fixture a ledger row and move the
	// id into the valid-id set rather than to relax the assertion.
	ledger := censusEdge{from: deriveSpikeID, rel: "implemented_in", to: deriveLedgerSession}
	ledgerRow, ok := census[ledger]
	if !ok {
		t.Errorf("edge to %s went missing entirely: %s -%s-> %s",
			deriveLedgerSession, ledger.from, ledger.rel, ledger.to)
	} else if !strings.Contains(ledgerRow.meta, `"tombstoned"`) {
		t.Errorf("edge to a session with no canonical ledger row is not tombstoned: %s -%s-> %s meta=%s.\n"+
			"If the sessions ledger (feat-1b08a194) has landed, this is the expected flip: give the fixture\n"+
			"a ledger row for %s and move it out of deriveSessionShapedTargets into the valid-id set, so it\n"+
			"classifies EdgeTargetLive with no marker. Do not simply drop this assertion.",
			ledger.from, ledger.rel, ledger.to, metaOrNone(ledgerRow.meta), deriveLedgerSession)
	}
}

// assertDerivationCasesPresent guards against a vacuous comparison. If the
// fixture stopped producing a case — a tombstone, a synthesized plan edge, a
// learning edge — the two sides could agree about nothing and still pass.
func assertDerivationCasesPresent(t *testing.T, expected derivedEdgeSet) {
	t.Helper()
	for _, want := range []struct {
		label string
		edge  censusEdge
	}{
		{"normal HTML-declared edge", censusEdge{from: deriveBugID, rel: "caused_by", to: deriveFeatureA}},
		{"edge to a live session", censusEdge{from: deriveFeatureA, rel: "implemented_in", to: deriveLiveSession}},
		{"tombstoned edge to a pruned session", censusEdge{from: deriveFeatureA, rel: "implemented_in", to: derivePrunedSession}},
		{"tombstoned edge to a pruned session in the 28-hex id shape", censusEdge{from: deriveBugID, rel: "implemented_in", to: derivePrunedSessionAlt}},
		{"edge to a session awaiting a canonical ledger row (feat-1b08a194)", censusEdge{from: deriveSpikeID, rel: "implemented_in", to: deriveLedgerSession}},
		{"plan-YAML slice ordering edge", censusEdge{from: deriveFeatureB, rel: "blocked_by", to: deriveFeatureA}},
		{"arch-ledger learning edge", censusEdge{from: deriveArchNode, rel: "learned_from", to: deriveFeatureA}},
		{"arch-ledger reverse learning edge", censusEdge{from: deriveFeatureA, rel: "has_learning", to: deriveArchNode}},
	} {
		if _, ok := expected[want.edge]; !ok {
			t.Fatalf("the derivation no longer produces a %s (%s -%s-> %s) — "+
				"the census would compare vacuously",
				want.label, want.edge.from, want.edge.rel, want.edge.to)
		}
	}
	// The dropped case must be absent from the expectation, or "index does not
	// contain it" would be trivially satisfied.
	dropped := censusEdge{from: deriveFeatureA, rel: "relates_to", to: deriveGhostItem}
	if _, ok := expected[dropped]; ok {
		t.Fatalf("the derivation expects the dangling reference %s -%s-> %s to survive — "+
			"the drop case is no longer being tested", dropped.from, dropped.rel, dropped.to)
	}
}

// compareDerivationToIndex diffs both directions. Every failure names the edge
// and which side it is on: a count alone would say "something drifted" without
// saying what, which is the reporting gap that let the 828-versus-82 divergence
// sit unexamined.
func compareDerivationToIndex(t *testing.T, expected derivedEdgeSet, actual map[censusEdge]censusRow) {
	t.Helper()

	for edge, wantMeta := range expected {
		got, ok := actual[edge]
		if !ok {
			t.Errorf("MISSING FROM INDEX: %s -%s-> %s\n"+
				"  canonical sources derive this edge (metadata %s) but graph_edges has no row for it",
				edge.from, edge.rel, edge.to, metaOrNone(wantMeta))
			continue
		}
		if got.count != 1 {
			t.Errorf("DUPLICATED IN INDEX: %s -%s-> %s appears %d times; the derivation yields exactly one row",
				edge.from, edge.rel, edge.to, got.count)
		}
		if got.meta != wantMeta {
			t.Errorf("METADATA DIVERGES: %s -%s-> %s\n"+
				"  canonical derivation: %s\n"+
				"  index:                %s",
				edge.from, edge.rel, edge.to, metaOrNone(wantMeta), metaOrNone(got.meta))
		}
	}

	for edge, got := range actual {
		if _, ok := expected[edge]; !ok {
			t.Errorf("NOT DERIVABLE FROM ANY CANONICAL SOURCE: %s -%s-> %s (x%d meta=%s)\n"+
				"  no work-item/plan HTML declares it, no plan YAML slice implies it, no arch card links it;\n"+
				"  a writer is putting rows into graph_edges that a rebuild cannot reproduce",
				edge.from, edge.rel, edge.to, got.count, metaOrNone(got.meta))
		}
	}

	if t.Failed() {
		t.Logf("canonical derivation (%d edges):\n%s", len(expected), formatDerivation(expected))
		t.Logf("index after rebuild (%d edges):\n%s", len(actual), formatCensus(actual))
	}
}

func formatDerivation(expected derivedEdgeSet) string {
	lines := make([]string, 0, len(expected))
	for e, meta := range expected {
		lines = append(lines, fmt.Sprintf("  %s -%s-> %s meta=%s", e.from, e.rel, e.to, metaOrNone(meta)))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
