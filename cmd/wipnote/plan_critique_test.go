package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/plantmpl"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

func TestCritique_ComplexityGateLow(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "plans"), 0o755)

	planID, err := createPlanFromTopic(dir, "Small Plan", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runPlanAddSliceYAML(dir, planID, "Single slice",
		"Do one thing", "", "", "", "", "S", "Low", ""); err != nil {
		t.Fatal(err)
	}

	out, err := extractCritiqueData(dir, planID)
	if err != nil {
		t.Fatalf("extractCritiqueData: %v", err)
	}
	if out.CritiqueWarranted {
		t.Error("expected critique_warranted=false for 1 slice")
	}
	if out.Complexity != "low" {
		t.Errorf("complexity = %q, want low", out.Complexity)
	}
}

func TestCritique_ComplexityGateMedium(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "plans"), 0o755)

	planID, err := createPlanFromTopic(dir, "Medium Plan", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"S1", "S2", "S3", "S4"} {
		if err := runPlanAddSliceYAML(dir, planID, s,
			"Do "+s, "", "", "", "", "S", "Low", ""); err != nil {
			t.Fatal(err)
		}
	}

	out, err := extractCritiqueData(dir, planID)
	if err != nil {
		t.Fatal(err)
	}
	if !out.CritiqueWarranted {
		t.Error("expected critique_warranted=true for 4 slices")
	}
	if out.Complexity != "medium" {
		t.Errorf("complexity = %q, want medium", out.Complexity)
	}
}

func TestCritique_TitleExtraction(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "plans"), 0o755)

	planID, err := createPlanFromTopic(dir, "Auth Rewrite", "compliance driven")
	if err != nil {
		t.Fatal(err)
	}

	out, err := extractCritiqueData(dir, planID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Auth Rewrite" {
		t.Errorf("title = %q, want Auth Rewrite", out.Title)
	}
	if out.Description != "compliance driven" {
		t.Errorf("description = %q, want 'compliance driven'", out.Description)
	}
}

func TestClassifyComplexity(t *testing.T) {
	tests := []struct {
		count      int
		complexity string
		warranted  bool
	}{
		{0, "low", false},
		{1, "low", false},
		{2, "low", false},
		{3, "medium", true},
		{5, "medium", true},
		{6, "high", true},
		{10, "high", true},
	}

	for _, tc := range tests {
		c, w := classifyComplexity(tc.count)
		if c != tc.complexity || w != tc.warranted {
			t.Errorf("classifyComplexity(%d) = (%q, %v), want (%q, %v)",
				tc.count, c, w, tc.complexity, tc.warranted)
		}
	}
}

// TestPlanCritique_YAMLDoesNotOpenDB verifies that extractCritiqueData for a
// v2 YAML plan never calls workitem.Open (and therefore never touches SQLite).
// The test installs an open-factory spy that fails the test if invoked.
func TestPlanCritique_YAMLDoesNotOpenDB(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Build a minimal YAML plan directly — no workitem.Open during setup.
	planID := "plan-spytest1"
	yamlPath := filepath.Join(dir, "plans", planID+".yaml")
	plan := planyaml.NewPlan(planID, "Spy Test Plan", "testing DB isolation")
	plan.Design.Problem = "test problem"
	plan.Design.Goals = []string{"goal1"}
	plan.Design.Constraints = []string{"constraint1"}
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, ID: "s1", Title: "Slice One", What: "do it", Why: "because",
			Files: []string{"x.go"}, DoneWhen: []string{"done"}, Tests: "unit",
			Effort: "S", Risk: "Low"},
		{Num: 2, ID: "s2", Title: "Slice Two", What: "do more", Why: "because",
			Files: []string{"y.go"}, DoneWhen: []string{"done"}, Tests: "unit",
			Effort: "S", Risk: "Low"},
		{Num: 3, ID: "s3", Title: "Slice Three", What: "finish", Why: "complete",
			Files: []string{"z.go"}, DoneWhen: []string{"done"}, Tests: "unit",
			Effort: "S", Risk: "Low"},
	}
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save YAML plan: %v", err)
	}

	// Install spy: fail the test if workitem.Open is ever called.
	orig := critiqueProjectOpener
	t.Cleanup(func() { critiqueProjectOpener = orig })
	critiqueProjectOpener = func(projectDir, agent string) (*workitem.Project, error) {
		t.Errorf("workitem.Open called for YAML plan (projectDir=%s) — DB path leaked", projectDir)
		return nil, errors.New("spy: DB must not be opened for YAML plans")
	}

	out, err := extractCritiqueData(dir, planID)
	if err != nil {
		t.Fatalf("extractCritiqueData: %v", err)
	}
	if out.Title != "Spy Test Plan" {
		t.Errorf("title = %q, want Spy Test Plan", out.Title)
	}
	if out.SliceCount != 3 {
		t.Errorf("slice_count = %d, want 3", out.SliceCount)
	}
	if out.Description != "testing DB isolation" {
		t.Errorf("description = %q, want testing DB isolation", out.Description)
	}
}

// TestCritique_RewritesInPlace is the slice-4 regression: after two critique
// rounds a slice holds exactly one `what` value and no accumulated
// critic_revisions. Appending was measured as the dominant driver of
// per-slice word growth (77% words-per-slice increase across 45 plans / 282
// slices while slices-per-plan fell 22%); reviseSliceInPlace replaces the
// field instead of stacking onto it.
func TestCritique_RewritesInPlace(t *testing.T) {
	plan := planyaml.NewPlan("plan-revise01", "Revise Test", "test in-place revision")
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, ID: "s1", Title: "Slice One", What: "original text", Why: "original why",
			Files: []string{"x.go"}, DoneWhen: []string{"done"}, Tests: "unit", Effort: "S", Risk: "Low"},
	}

	if err := reviseSliceInPlace(plan, 1, "round one text", "", ""); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if err := reviseSliceInPlace(plan, 1, "round two text", "", ""); err != nil {
		t.Fatalf("round 2: %v", err)
	}

	if got := plan.Slices[0].What; got != "round two text" {
		t.Errorf("what = %q, want %q (only the latest round should survive)", got, "round two text")
	}
	if n := len(plan.Slices[0].CriticRevisions); n != 0 {
		t.Errorf("critic_revisions = %d entries after two revise rounds, want 0 (no accumulation)", n)
	}
	// why was never passed a value by either round — it must be left
	// untouched (not blanked), proving the write is field-scoped.
	if got := plan.Slices[0].Why; got != "original why" {
		t.Errorf("why = %q, want unchanged %q", got, "original why")
	}
}

// TestCritique_RewritesInPlace_ThroughDisk exercises the same two-round
// scenario through the actual CLI write path (runPlanCritiqueRevise),
// round-tripping through planyaml.Save/Load rather than mutating an
// in-memory struct, so the on-disk YAML is verified too.
func TestCritique_RewritesInPlace_ThroughDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}

	planID, err := createPlanFromTopic(dir, "Disk Revise Plan", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runPlanAddSliceYAML(dir, planID, "Slice One",
		"original text", "original why", "", "", "unit", "S", "Low", ""); err != nil {
		t.Fatal(err)
	}

	if err := runPlanCritiqueRevise(dir, planID, 1, "round one text", "", ""); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if err := runPlanCritiqueRevise(dir, planID, 1, "round two text", "", ""); err != nil {
		t.Fatalf("round 2: %v", err)
	}

	yamlPath := filepath.Join(dir, "plans", planID+".yaml")
	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := plan.Slices[0].What; got != "round two text" {
		t.Errorf("what = %q, want %q", got, "round two text")
	}
	if n := len(plan.Slices[0].CriticRevisions); n != 0 {
		t.Errorf("critic_revisions accumulated on disk: %d entries", n)
	}
}

// TestLoad_LegacyCriticRevisions is the slice-4 backward-compat test: a plan
// that already carries populated critic_revisions (written before this
// change stopped appending to it) must still load and render without error.
// plan-f8c02547 — this feature's own planning document — carries
// critic_revisions from its own critique pass, making it a live fixture
// rather than a synthetic one.
func TestLoad_LegacyCriticRevisions(t *testing.T) {
	yamlPath := filepath.Join("..", "..", ".wipnote", "plans", "plan-f8c02547.yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		t.Skipf("legacy fixture not found at %s: %v", yamlPath, err)
	}

	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		t.Fatalf("load legacy plan carrying critic_revisions: %v", err)
	}

	foundRevisions := false
	for _, s := range plan.Slices {
		for _, cr := range s.CriticRevisions {
			foundRevisions = true
			if cr.Source == "" || cr.Severity == "" || cr.Summary == "" {
				t.Errorf("slice %d critic_revision missing a field: %+v", s.Num, cr)
			}
		}
		// Rendering must not error either. SliceCardFromPlanSlice is a pure
		// function (no file I/O) so it's safe to call directly against the
		// live fixture without touching its committed HTML.
		_ = plantmpl.SliceCardFromPlanSlice(s)
	}
	if !foundRevisions {
		t.Fatal("expected at least one slice with critic_revisions in the legacy fixture — has it been migrated already?")
	}
}

// TestHistory_SurfacesSuperseded is the slice-4 integration test for
// done_when #2: after a critique-revise round supersedes a slice's wording,
// `wipnote history <plan-id>` — which resolves plan- ids to the .yaml file,
// unlike every other work-item kind (see history.go's subDirAndExt) — still
// lists the commit carrying the prior wording, and git itself (the actual
// history mechanism) can recover the superseded text from that commit.
func TestHistory_SurfacesSuperseded(t *testing.T) {
	dir := t.TempDir()
	// Resolve symlinks up front: macOS temp dirs live under /var, a symlink
	// to /private/var, and `git rev-parse --show-toplevel` returns the
	// resolved form. Without this, repoRoot and path disagree on prefix and
	// filepath.Rel produces a bogus path escaping the repo.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	initGitRepo(t, dir)
	t.Chdir(dir)
	t.Setenv("WIPNOTE_PROJECT_DIR", dir)

	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}

	planID, err := createPlanFromTopic(wipnoteDir, "History Test Plan", "")
	if err != nil {
		t.Fatalf("createPlanFromTopic: %v", err)
	}
	if err := runPlanAddSliceYAML(wipnoteDir, planID, "Slice One",
		"round one text", "why", "", "", "unit", "S", "Low", ""); err != nil {
		t.Fatalf("add slice: %v", err)
	}
	if err := runPlanCritiqueRevise(wipnoteDir, planID, 1, "round two text", "", ""); err != nil {
		t.Fatalf("critique revise: %v", err)
	}

	// Resolve exactly the way `wipnote history` does.
	path, err := resolveHistoryPath(wipnoteDir, planID)
	if err != nil {
		t.Fatalf("resolveHistoryPath: %v", err)
	}
	if !strings.HasSuffix(path, ".yaml") {
		t.Fatalf("resolveHistoryPath(%q) = %q, want a .yaml path (plan- ids resolve to YAML, not HTML)", planID, path)
	}

	repoRoot, err := resolveHistoryRoot(filepath.Dir(wipnoteDir))
	if err != nil {
		t.Fatalf("resolveHistoryRoot: %v", err)
	}

	entries, err := runHistoryLog(repoRoot, path)
	if err != nil {
		t.Fatalf("runHistoryLog: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 commits (add-slice, critique-revise), got %d: %+v", len(entries), entries)
	}

	// Entries are newest-first: the critique-revise commit, then add-slice.
	newest, oldest := entries[0], entries[len(entries)-1]
	if !strings.Contains(newest.Subject, "critique revise") {
		t.Errorf("newest commit subject = %q, want it to mention the critique revise", newest.Subject)
	}
	if !strings.Contains(oldest.Subject, "add slice") {
		t.Errorf("oldest commit subject = %q, want it to mention add slice", oldest.Subject)
	}

	// git itself — the actual history mechanism — must be able to recover
	// the superseded wording from the older commit.
	relPath, err := filepath.Rel(repoRoot, path)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	out, err := exec.Command("git", "-C", repoRoot, "show", oldest.SHA+":"+relPath).Output()
	if err != nil {
		t.Fatalf("git show %s:%s: %v", oldest.SHA, relPath, err)
	}
	if !strings.Contains(string(out), "round one text") {
		t.Error("git show at the oldest commit does not contain the superseded wording")
	}
	if strings.Contains(string(out), "round two text") {
		t.Error("git show at the oldest commit unexpectedly contains the newer wording")
	}

	// The current file on disk holds only the latest wording — no
	// accumulation of prior rounds.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current plan file: %v", err)
	}
	if strings.Contains(string(current), "round one text") {
		t.Error("current plan file still contains the superseded wording — it should have been replaced, not accumulated")
	}
	if !strings.Contains(string(current), "round two text") {
		t.Error("current plan file is missing the latest wording")
	}
}
