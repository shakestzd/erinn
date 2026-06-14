package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// --- helpers -----------------------------------------------------------

// minimalFeatureHTMLForSpec returns a minimal feature HTML document. If
// existingSpec is non-empty, a <section class="spec"> wrapping it is included.
func minimalFeatureHTMLForSpec(featureID, existingSpec string) string {
	spec := ""
	if existingSpec != "" {
		spec = `<section class="spec">` + "\n" + existingSpec + "\n" + `</section>`
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>%s</title></head>
<body>
<article id="%s"></article>
%s
</body>
</html>`, featureID, featureID, spec)
}

// writeFeatureFile creates a feature HTML file and returns its path.
func writeFeatureFile(t *testing.T, dir, featureID, body string) string {
	t.Helper()
	featuresDir := filepath.Join(dir, "features")
	if err := os.MkdirAll(featuresDir, 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}
	path := filepath.Join(featuresDir, featureID+".html")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	return path
}

// fixtureSpec returns a specTemplate populated with new-format Requirements
// for renderer tests.
func fixtureSpec(featureID, title string) *specTemplate {
	return &specTemplate{
		FeatureID: featureID,
		Title:     title,
		Problem:   "Old auth flow leaks tokens.",
		Files:     []string{"NEW: cmd/auth/login.go"},
		Notes:     []string{"Risks: token rotation timing"},
		Requirements: []specRequirement{
			{
				Name:  "Requirement 1",
				SHALL: "Users authenticate via OAuth2.",
				Scenarios: []specScenario{
					{
						Name: "valid token",
						When: "the token signature verifies",
						Then: "the user is logged in",
					},
				},
			},
		},
	}
}

// --- Unit tests --------------------------------------------------------

// TestSpecGenerate_OpenSpecFormat — fixture slice produces output containing
// the OpenSpec structural keywords.
func TestSpecGenerate_OpenSpecFormat(t *testing.T) {
	out := renderSpecMarkdown(fixtureSpec("feat-x", "Auth"))
	for _, want := range []string{
		"### Requirement:",
		"#### Scenario:",
		"- **WHEN**",
		"- **THEN**",
		"## ADDED Requirements",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderSpecMarkdown missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestSpecGenerate_Insert_NewSection — feature without spec section gets one
// created by insertSpecIntoFeature.
func TestSpecGenerate_Insert_NewSection(t *testing.T) {
	dir := t.TempDir()
	featureID := "feat-insertnew"
	path := writeFeatureFile(t, dir, featureID, minimalFeatureHTMLForSpec(featureID, ""))

	if err := insertSpecIntoFeature(path, fixtureSpec(featureID, "T"), false); err != nil {
		t.Fatalf("insert: %v", err)
	}

	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), `<section class="spec">`) {
		t.Errorf("expected spec section, got:\n%s", body)
	}
	if !strings.Contains(string(body), "### Requirement:") {
		t.Errorf("expected Requirement marker, got:\n%s", body)
	}
}

// TestSpecGenerate_Insert_NonClobber — existing non-empty spec section without
// --force refuses with error containing the remediation message.
func TestSpecGenerate_Insert_NonClobber(t *testing.T) {
	dir := t.TempDir()
	featureID := "feat-clobberguard"
	existing := "manual content the user typed"
	path := writeFeatureFile(t, dir, featureID, minimalFeatureHTMLForSpec(featureID, existing))

	err := insertSpecIntoFeature(path, fixtureSpec(featureID, "T"), false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "spec section already has content") {
		t.Errorf("error missing remediation message: %v", err)
	}

	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), existing) {
		t.Error("original content should not have been clobbered")
	}
}

// TestSpecGenerate_Insert_Force — --force replaces existing content; idempotent
// under repeated --force runs.
func TestSpecGenerate_Insert_Force(t *testing.T) {
	dir := t.TempDir()
	featureID := "feat-forcerepl"
	path := writeFeatureFile(t, dir, featureID, minimalFeatureHTMLForSpec(featureID, "old content"))

	if err := insertSpecIntoFeature(path, fixtureSpec(featureID, "T"), true); err != nil {
		t.Fatalf("first force insert: %v", err)
	}
	first, _ := os.ReadFile(path)

	if err := insertSpecIntoFeature(path, fixtureSpec(featureID, "T"), true); err != nil {
		t.Fatalf("second force insert: %v", err)
	}
	second, _ := os.ReadFile(path)

	if string(first) != string(second) {
		t.Error("--force re-run should be idempotent")
	}
	if strings.Contains(string(first), "old content") {
		t.Error("--force should have replaced the old content")
	}
}

// TestSpecGenerate_Insert_AtomicAndLocked — concurrent inserts on the same
// feature serialise via LockFeatureForWrite and produce a single coherent
// final HTML.
func TestSpecGenerate_Insert_AtomicAndLocked(t *testing.T) {
	dir := t.TempDir()
	featureID := "feat-concurrent"
	path := writeFeatureFile(t, dir, featureID, minimalFeatureHTMLForSpec(featureID, ""))

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			spec := fixtureSpec(featureID, fmt.Sprintf("T-%d", i))
			_ = insertSpecIntoFeature(path, spec, true)
		}(i)
	}
	wg.Wait()

	body, _ := os.ReadFile(path)
	openCount := strings.Count(string(body), `<section class="spec">`)
	closeCount := strings.Count(string(body), `</section>`)
	if openCount != 1 {
		t.Errorf("expected exactly 1 spec opening tag, got %d\n%s", openCount, body)
	}
	if closeCount < 1 {
		t.Errorf("expected at least 1 closing section tag, got %d", closeCount)
	}
}

// TestSpecGenerate_DecisionsRendered — DecisionsNotes set renders ## Decisions
// between ## Problem and ## ADDED Requirements.
func TestSpecGenerate_DecisionsRendered(t *testing.T) {
	spec := fixtureSpec("feat-d", "T")
	spec.DecisionsNotes = "### Scope\nA, B, C\n\n### Decisions\nChose option X."

	out := renderSpecMarkdown(spec)

	if !strings.Contains(out, "## Decisions") {
		t.Errorf("missing ## Decisions heading\n%s", out)
	}
	probIdx := strings.Index(out, "## Problem")
	decIdx := strings.Index(out, "## Decisions")
	reqIdx := strings.Index(out, "## ADDED Requirements")
	if !(probIdx >= 0 && decIdx > probIdx && reqIdx > decIdx) {
		t.Errorf("section order wrong: Problem=%d Decisions=%d Requirements=%d", probIdx, decIdx, reqIdx)
	}
	if !strings.Contains(out, "Chose option X.") {
		t.Errorf("decisions prose not rendered\n%s", out)
	}
}

// TestSpecGenerate_DecisionsAbsent — empty DecisionsNotes renders no heading.
func TestSpecGenerate_DecisionsAbsent(t *testing.T) {
	spec := fixtureSpec("feat-d", "T")
	spec.DecisionsNotes = ""

	out := renderSpecMarkdown(spec)

	if strings.Contains(out, "## Decisions") {
		t.Errorf("Decisions section should not appear when notes empty\n%s", out)
	}
}

// TestSpecGenerate_FlagsMutex — --insert + --output is rejected.
func TestSpecGenerate_FlagsMutex(t *testing.T) {
	err := runSpecGenerate("feat-x", "markdown", "/tmp/out.md", true, false)
	if err == nil {
		t.Fatal("expected error for --insert + --output, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion: %v", err)
	}
}

// TestSpecGenerate_FlagsInsertJSON — --insert + --format=json is rejected.
func TestSpecGenerate_FlagsInsertJSON(t *testing.T) {
	err := runSpecGenerate("feat-x", "json", "", true, false)
	if err == nil {
		t.Fatal("expected error for --insert + --format=json, got nil")
	}
}

// TestThirdPartyNotices_HasOpenSpec — the notices file at repo root contains
// the OpenSpec entry.
func TestThirdPartyNotices_HasOpenSpec(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..")
	noticesPath := filepath.Join(repoRoot, "THIRD-PARTY-NOTICES.md")

	body, err := os.ReadFile(noticesPath)
	if err != nil {
		t.Fatalf("read notices: %v", err)
	}
	if !thirdPartyNoticesPattern.Match(body) {
		t.Errorf("notices file missing OpenSpec/MIT entry\n%s", body)
	}
}

// TestSpecSectionSurvivesStatusWrite — the spec section inserted by
// `spec generate --insert` must NOT be deleted when the feature is later
// rewritten by a status transition (col.Start / col.Complete). This is the
// regression test for the HIGH finding in roborev job 236.
func TestSpecSectionSurvivesStatusWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item status lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "tracks", "plans", "specs", "spikes", "bugs"} {
		_ = os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	// Create a feature, then insert a spec, then mark it in-progress (which
	// re-renders the HTML via WriteNodeHTML). The spec section must persist.
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	if err := testCreate("track", "T", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) == 0 {
		t.Fatal("no track")
	}
	trackNode, _ := htmlparseParseFileForTest(t, trackFiles[0])

	if err := testCreate("feature", "Feature With Spec", trackNode.ID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) == 0 {
		t.Fatal("no feature")
	}
	featNode, _ := htmlparseParseFileForTest(t, featFiles[0])

	if err := insertSpecIntoFeature(featFiles[0], fixtureSpec(featNode.ID, "T"), false); err != nil {
		t.Fatalf("insert spec: %v", err)
	}

	beforeBody, _ := os.ReadFile(featFiles[0])
	if !strings.Contains(string(beforeBody), `<section class="spec">`) {
		t.Fatal("setup: spec section should exist before status transition")
	}

	if err := runWiSetStatus("feature", featNode.ID, "in-progress"); err != nil {
		t.Fatalf("set in-progress: %v", err)
	}

	afterBody, _ := os.ReadFile(featFiles[0])
	if !strings.Contains(string(afterBody), `<section class="spec">`) {
		t.Errorf("spec section was lost during status transition; content:\n%s", afterBody)
	}
	if !strings.Contains(string(afterBody), "### Requirement:") {
		t.Errorf("spec body content was lost during status transition")
	}
}

// htmlparseParseFileForTest is a thin wrapper that fatals the test rather
// than returning errors.
func htmlparseParseFileForTest(t *testing.T, path string) (*modelsNode, error) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Pull the article id="..." attribute.
	const idMarker = `<article id="`
	i := strings.Index(string(body), idMarker)
	if i == -1 {
		t.Fatalf("no article id in %s", path)
	}
	rest := string(body)[i+len(idMarker):]
	q := strings.Index(rest, `"`)
	if q == -1 {
		t.Fatalf("malformed article id")
	}
	return &modelsNode{ID: rest[:q]}, nil
}

// modelsNode is a tiny stand-in so we don't pull the full models.Node into
// test imports; we only need ID for these tests.
type modelsNode struct {
	ID string
}

// TestSpecInsertAndComplianceAutoConcurrent — insertSpecIntoFeature and
// writeComplianceSection racing on the same feature both succeed; both
// sections appear in the final HTML and no update is lost.
func TestSpecInsertAndComplianceAutoConcurrent(t *testing.T) {
	dir := t.TempDir()
	featureID := "feat-race"
	path := writeFeatureFile(t, dir, featureID, minimalFeatureHTMLForSpec(featureID, ""))

	const N = 8
	var wg sync.WaitGroup
	wg.Add(N * 2)

	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = insertSpecIntoFeature(path, fixtureSpec(featureID, fmt.Sprintf("T-%d", i)), true)
		}(i)
		go func(i int) {
			defer wg.Done()
			attrs := map[string]string{
				"score":     "85",
				"timestamp": fmt.Sprintf("2026-05-04T00:00:%02d", i),
			}
			body := fmt.Sprintf("<p>finding %d</p>", i)
			_ = writeComplianceSection(path, attrs, body)
		}(i)
	}
	wg.Wait()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	bodyStr := string(body)
	specOpens := strings.Count(bodyStr, `<section class="spec">`)
	complianceOpens := strings.Count(bodyStr, `<section class="compliance-findings"`)

	if specOpens != 1 {
		t.Errorf("expected exactly 1 spec section, got %d", specOpens)
	}
	if complianceOpens != 1 {
		t.Errorf("expected exactly 1 compliance-findings section, got %d", complianceOpens)
	}
	if !strings.Contains(bodyStr, "### Requirement:") {
		t.Errorf("spec section content missing Requirement marker:\n%s", bodyStr)
	}
}
