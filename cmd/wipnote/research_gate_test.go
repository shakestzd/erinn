package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// --- wiIsResearchURL (pure, fast) -------------------------------------------

func TestWiIsResearchURL(t *testing.T) {
	valid := []string{
		"https://pkg.go.dev/github.com/x/y",
		"http://example.com/changelog",
		"https://code.claude.com/docs/en/hooks",
	}
	for _, u := range valid {
		if !wiIsResearchURL(u) {
			t.Errorf("expected %q to be a valid research URL", u)
		}
	}
	invalid := []string{
		"",
		"not a url",
		"ftp://example.com",
		"https://",       // no host
		"example.com",    // no scheme
		"/local/path.md", // local file
	}
	for _, u := range invalid {
		if wiIsResearchURL(u) {
			t.Errorf("expected %q to be rejected", u)
		}
	}
}

// --- integration: research gate at completion -------------------------------

// TestResearchGate_DepChangeNoEvidence_Blocked proves the R2 acceptance
// criterion: completing an item whose diff changes a dependency manifest without
// --research-url/--research-waiver fails. Provenance passes (a commit is seeded)
// so the research gate is actually reached.
func TestResearchGate_DepChangeNoEvidence_Blocked(t *testing.T) {
	_, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Dep Feature", trackID)
	seedFeatureFile(t, hgDir, id, "go.mod")
	seedProvCommit(t, hgDir, id, "deadbeefcafe2001") // provenance passes

	wiResearchURL = nil
	wiResearchWaiver = ""
	t.Cleanup(func() { wiResearchURL = nil; wiResearchWaiver = "" })

	err := runWiSetStatus("feature", id, "done")
	if err == nil {
		t.Fatal("expected completion blocked: dependency-manifest change with no research evidence")
	}
	if !strings.Contains(err.Error(), "research") {
		t.Errorf("error should mention research remediation, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status == models.StatusDone {
		t.Error("item must not be done when the research gate blocks")
	}
}

// TestResearchGate_InvalidURL_Blocked proves a non-http(s) --research-url does
// not satisfy the gate.
func TestResearchGate_InvalidURL_Blocked(t *testing.T) {
	_, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Dep Feature Bad URL", trackID)
	seedFeatureFile(t, hgDir, id, "go.mod")
	seedProvCommit(t, hgDir, id, "deadbeefcafe2002")

	wiResearchURL = []string{"not-a-url"}
	wiResearchWaiver = ""
	t.Cleanup(func() { wiResearchURL = nil; wiResearchWaiver = "" })

	err := runWiSetStatus("feature", id, "done")
	if err == nil {
		t.Fatal("expected completion blocked: invalid --research-url must not satisfy the gate")
	}
	if !strings.Contains(err.Error(), "http(s)") {
		t.Errorf("error should explain the URL shape requirement, got: %v", err)
	}
}

// TestResearchGate_MixedValidInvalidURL_Blocked proves the roborev #580 fix: an
// invalid --research-url is NOT silently dropped even when another valid URL is
// also supplied — every URL is shape-checked.
func TestResearchGate_MixedValidInvalidURL_Blocked(t *testing.T) {
	_, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Dep Feature Mixed URL", trackID)
	seedFeatureFile(t, hgDir, id, "go.mod")
	seedProvCommit(t, hgDir, id, "deadbeefcafe2006")

	wiResearchURL = []string{"https://pkg.go.dev/valid", "not-a-url"}
	wiResearchWaiver = ""
	t.Cleanup(func() { wiResearchURL = nil; wiResearchWaiver = "" })

	err := runWiSetStatus("feature", id, "done")
	if err == nil {
		t.Fatal("expected completion blocked: a mix with an invalid --research-url must not pass")
	}
	if !strings.Contains(err.Error(), "not-a-url") {
		t.Errorf("error should name the offending URL, got: %v", err)
	}
}

// TestResearchGate_NonDepItem_Exempt proves no regression: an item that touches
// no dependency manifest completes without research flags.
func TestResearchGate_NonDepItem_Exempt(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion gate (seedPassingGateRecord)")
	}
	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Plain Feature", trackID)
	seedFeatureFile(t, hgDir, id, "internal/foo/bar.go")
	seedProvCommit(t, hgDir, id, "deadbeefcafe2003")
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)

	wiResearchURL = nil
	wiResearchWaiver = ""
	t.Cleanup(func() { wiResearchURL = nil; wiResearchWaiver = "" })

	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("non-dependency item should complete without research flags, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("exempt item should be done, status=%s", node.Status)
	}
}

// TestResearchGate_DepChangeWithURL_Completes proves a cited https URL satisfies
// the gate and the evidence is persisted on the artifact.
func TestResearchGate_DepChangeWithURL_Completes(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion gate (seedPassingGateRecord)")
	}
	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Dep Feature OK", trackID)
	seedFeatureFile(t, hgDir, id, "go.mod")
	seedProvCommit(t, hgDir, id, "deadbeefcafe2004")
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)

	const cited = "https://pkg.go.dev/github.com/some/dep"
	wiResearchURL = []string{cited}
	wiResearchWaiver = ""
	t.Cleanup(func() { wiResearchURL = nil; wiResearchWaiver = "" })

	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("expected completion to succeed with --research-url, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("item should be done after citing research, status=%s", node.Status)
	}
	if !strings.Contains(node.Content, cited) {
		t.Errorf("cited research URL not persisted on artifact; content did not contain %q", cited)
	}
}

// TestResearchGate_DepChangeWithWaiver_Completes proves an explicit waiver
// satisfies the gate.
func TestResearchGate_DepChangeWithWaiver_Completes(t *testing.T) {
	if testing.Short() {
		t.Skip("drives real completion gate (seedPassingGateRecord)")
	}
	tmpDir, hgDir := prepProject(t)
	trackID := testSetupTrack(t, hgDir)
	id := createItem(t, hgDir, "feature", "Dep Feature Waiver", trackID)
	seedFeatureFile(t, hgDir, id, "go.mod")
	seedProvCommit(t, hgDir, id, "deadbeefcafe2005")
	seedPassingGateRecord(t, tmpDir, "test-session-prov", id)

	const reason = "version pin of an internal vendored fork; no upstream docs"
	wiResearchURL = nil
	wiResearchWaiver = reason
	t.Cleanup(func() { wiResearchURL = nil; wiResearchWaiver = "" })

	if err := runWiSetStatus("feature", id, "done"); err != nil {
		t.Fatalf("expected completion to succeed with --research-waiver, got: %v", err)
	}
	node, _ := htmlparse.ParseFile(filepath.Join(hgDir, "features", id+".html"))
	if node.Status != models.StatusDone {
		t.Errorf("item should be done after waiver, status=%s", node.Status)
	}
	if !strings.Contains(node.Content, reason) {
		t.Errorf("waiver reason not persisted on artifact")
	}
}
