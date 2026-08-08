package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
)

// richCRISPIPlanFixture is a synthetic but representative rich CRISPI plan
// HTML document: it carries the marker attributes that
// core/workitem.isCRISPIPlanFile looks for (data-zone=, class="slice-card",
// data-slice=, data-approval=), plus prose, JS, and CSS that a generic
// WriteNodeHTML re-render would drop. Used to regression-test bug-38c2e0ad:
// `link add`/`link remove` on a plan ID must route through PlanCollection's
// overrides (patchPlanHTML) instead of the generic Collection path, or this
// content is destroyed.
const richCRISPIPlanFixture = `<!DOCTYPE html>
<html>
<head><title>Rich CRISPI Plan</title></head>
<body>
<article id="plan-deadbeef" data-type="plan" data-status="todo" data-priority="medium">
  <header><h1>Rich CRISPI Plan</h1></header>
  <section data-zone="design">
    <h2>Design Discussion</h2>
    <p>This is the design discussion prose that only exists in the rich CRISPI
    rendering and has no equivalent field in the generic node template.</p>
  </section>
  <section data-zone="slices">
    <div class="slice-card" data-slice="slice-1" data-approval="pending">
      <span class="slice-name">First slice</span>
    </div>
    <div class="slice-card" data-slice="slice-2" data-approval="pending">
      <span class="slice-name">Second slice</span>
    </div>
  </section>
  <nav data-graph-edges></nav>
  <script>console.log("crispi interactive plan js");</script>
  <style>.slice-card { color: red; }</style>
</article>
</body>
</html>
`

// richCRISPIPlanMarkers are the fixture's telltale content fragments that a
// destructive generic re-render would drop.
var richCRISPIPlanMarkers = []string{
	`data-zone="design"`,
	`data-zone="slices"`,
	`class="slice-card"`,
	`data-slice="slice-1"`,
	`data-slice="slice-2"`,
	`data-approval="pending"`,
	`First slice`,
	`Second slice`,
	`design discussion prose`,
	`console.log("crispi interactive plan js")`,
	`.slice-card { color: red; }`,
}

func TestLinkErrorMessageInvalidPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	// Create a valid feature to use as target
	if err := testCreate("feature", "Target Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Find the feature ID
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	toID := featNode.ID

	// Try to add link from invalid ID (bad prefix like "bad-" with hex suffix)
	// resolveCollection checks prefix but won't recognize "bad-", triggering the error
	// We use a full ID format "bad-12345678" so resolveID passes but resolveCollection fails
	err := runLinkAdd("bad-12345678", toID, "relates_to")
	if err == nil {
		t.Fatal("expected error for invalid prefix, got nil")
	}

	errMsg := err.Error()

	// Check that error message lists valid prefixes
	validPrefixes := []string{"feat-", "bug-", "spk-", "trk-", "plan-", "spec-"}
	for _, prefix := range validPrefixes {
		if !stringContains(errMsg, prefix) {
			t.Errorf("error message should list %q prefix: %q", prefix, errMsg)
		}
	}
}

func TestLinkErrorMessageNoEdge(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	// Create two features
	if err := testCreate("feature", "Feature 1", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature 1: %v", err)
	}
	if err := testCreate("feature", "Feature 2", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature 2: %v", err)
	}

	// Find the feature IDs
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	feat1Node, _ := htmlparse.ParseFile(featFiles[0])
	feat2Node, _ := htmlparse.ParseFile(featFiles[1])
	fromID := feat1Node.ID
	toID := feat2Node.ID

	// Try to remove a link that doesn't exist
	err := runLinkRemove(fromID, toID, "blocks")
	if err == nil {
		t.Fatal("expected error for non-existent edge, got nil")
	}

	errMsg := err.Error()

	// Check that error message suggests 'link list'
	if !stringContains(errMsg, "link list") {
		t.Errorf("error message should suggest 'link list': %q", errMsg)
	}
	// Also check that it mentions the fromID in the suggestion
	if !stringContains(errMsg, fromID) {
		t.Errorf("error message should mention the fromID (%s): %q", fromID, errMsg)
	}
}

func TestWorkitemErrorMessageUnknownType(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Try to create a work item with invalid type
	opts := &wiCreateOpts{
		trackID:     "",
		priority:    "medium",
		description: "test",
		start:       false,
		noLink:      false,
	}
	err := runWiCreate("invalid_type", "Test Title", opts)

	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}

	errMsg := err.Error()

	// Check that error message lists all valid types
	validTypes := []string{"feature", "bug", "spike", "track", "plan", "spec"}
	for _, typ := range validTypes {
		if !stringContains(errMsg, typ) {
			t.Errorf("error message should list valid type %q: %q", typ, errMsg)
		}
	}
}

// setupRichPlanProject creates an isolated .wipnote project containing the
// richCRISPIPlanFixture and a target feature to link against. Returns the
// wipnote dir, the plan's path, the plan ID, and the target feature ID.
func setupRichPlanProject(t *testing.T) (hgDir, planPath, planID, toID string) {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir = filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	planID = "plan-deadbeef"
	planPath = filepath.Join(hgDir, "plans", planID+".html")
	if err := os.WriteFile(planPath, []byte(richCRISPIPlanFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = "" })

	trackID := testSetupTrack(t, hgDir)
	if err := testCreate("feature", "Target Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, err := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if err != nil || len(featFiles) == 0 {
		t.Fatalf("find target feature: %v", err)
	}
	featNode, err := htmlparse.ParseFile(featFiles[0])
	if err != nil {
		t.Fatalf("parse target feature: %v", err)
	}
	return hgDir, planPath, planID, featNode.ID
}

func assertRichPlanContentIntact(t *testing.T, planPath string) string {
	t.Helper()
	updated, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan after link op: %v", err)
	}
	content := string(updated)
	for _, marker := range richCRISPIPlanMarkers {
		if !strings.Contains(content, marker) {
			t.Errorf("plan content clobbered: marker %q missing after link op (before=%d bytes, after=%d bytes)",
				marker, len(richCRISPIPlanFixture), len(content))
		}
	}
	return content
}

// TestLinkAddPreservesRichPlanContent is a regression test for bug-38c2e0ad:
// resolveCollection used to return the embedded base *workitem.Collection for
// plan-prefixed IDs, so `link add` on a plan bypassed PlanCollection's
// CRISPI-preserving AddEdge override and silently destroyed the plan's
// rendered content (slice cards, design discussion, embedded JS/CSS) via the
// generic WriteNodeHTML re-render path. Confirmed against a real 207KB rich
// plan file, which collapsed to under 3KB before the fix.
func TestLinkAddPreservesRichPlanContent(t *testing.T) {
	_, planPath, planID, toID := setupRichPlanProject(t)

	if err := runLinkAdd(planID, toID, "relates_to"); err != nil {
		t.Fatalf("runLinkAdd: %v", err)
	}

	assertRichPlanContentIntact(t, planPath)

	node, err := htmlparse.ParseFile(planPath)
	if err != nil {
		t.Fatalf("parse patched plan: %v", err)
	}
	found := false
	for _, e := range node.Edges["relates_to"] {
		if e.TargetID == toID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected relates_to edge to %s not found after link add; edges=%v", toID, node.Edges)
	}
}

// TestLinkRemovePreservesRichPlanContent mirrors
// TestLinkAddPreservesRichPlanContent for `link remove`: PlanCollection had
// no RemoveEdge override at all (only AddEdge/Start/Complete/Get were
// overridden), so even after fixing resolveCollection, `link remove` on a
// plan would still fall through to the generic, destructive Collection.RemoveEdge.
func TestLinkRemovePreservesRichPlanContent(t *testing.T) {
	_, planPath, planID, toID := setupRichPlanProject(t)

	if err := runLinkAdd(planID, toID, "relates_to"); err != nil {
		t.Fatalf("runLinkAdd: %v", err)
	}
	if err := runLinkRemove(planID, toID, "relates_to"); err != nil {
		t.Fatalf("runLinkRemove: %v", err)
	}

	content := assertRichPlanContentIntact(t, planPath)
	if strings.Contains(content, `data-relationship="relates_to"`) {
		t.Errorf("expected relates_to edge to be removed from plan HTML, but it is still present")
	}

	node, err := htmlparse.ParseFile(planPath)
	if err != nil {
		t.Fatalf("parse patched plan: %v", err)
	}
	for _, e := range node.Edges["relates_to"] {
		if e.TargetID == toID {
			t.Errorf("expected relates_to edge to %s to be removed, but it is still present", toID)
		}
	}
}
