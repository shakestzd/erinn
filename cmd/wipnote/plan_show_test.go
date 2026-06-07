package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/workitem"
)

const driftTestYAML = `meta:
    id: plan-drifttest
    title: Original Title
    description: test
    created_at: "2026-04-15"
    status: finalized
    priority: medium
    version: 1
design:
    problem: p
    goals: []
    constraints: []
    approved: false
    comment: ""
slices:
    - id: slice-1
      num: 1
      title: s1
      what: w
      why: y
      files: []
      deps: []
      done_when: []
      effort: S
      risk: Low
      tests: ""
      approved: false
      comment: ""
    - id: slice-2
      num: 2
      title: s2
      what: w
      why: y
      files: []
      deps: []
      done_when: []
      effort: S
      risk: Low
      tests: ""
      approved: false
      comment: ""
questions: []
`

func writeDriftTestPair(t *testing.T, yamlBody, htmlBody string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "plan-drifttest.yaml")
	htmlPath := filepath.Join(dir, "plan-drifttest.html")
	if err := os.WriteFile(yamlPath, []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(htmlPath, []byte(htmlBody), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	return yamlPath, htmlPath
}

func TestCheckPlanDrift_StatusMismatch(t *testing.T) {
	html := `<html><head><title>Plan: Original Title</title></head><body>
<article id="plan-drifttest" data-plan-id="plan-drifttest" data-status="draft">
<div data-slice="1"></div><div data-slice="2"></div>
</article></body></html>`
	yamlPath, htmlPath := writeDriftTestPair(t, driftTestYAML, html)

	var buf bytes.Buffer
	checkPlanDrift(yamlPath, htmlPath, &buf)

	out := buf.String()
	if !strings.Contains(out, "status: yaml=\"finalized\" html=\"draft\"") {
		t.Errorf("expected status drift warning, got:\n%s", out)
	}
	if strings.Contains(out, "title:") || strings.Contains(out, "slice count:") {
		t.Errorf("unexpected extra warnings:\n%s", out)
	}
}

func TestCheckPlanDrift_TitleMismatch(t *testing.T) {
	html := `<html><head><title>Plan: Stale Title</title></head><body>
<article id="plan-drifttest" data-status="finalized">
<div data-slice="1"></div><div data-slice="2"></div>
</article></body></html>`
	yamlPath, htmlPath := writeDriftTestPair(t, driftTestYAML, html)

	var buf bytes.Buffer
	checkPlanDrift(yamlPath, htmlPath, &buf)

	if !strings.Contains(buf.String(), `title: yaml="Original Title" html="Stale Title"`) {
		t.Errorf("expected title drift warning, got:\n%s", buf.String())
	}
}

func TestCheckPlanDrift_SliceCountMismatch(t *testing.T) {
	html := `<html><head><title>Plan: Original Title</title></head><body>
<article id="plan-drifttest" data-status="finalized">
<div data-slice="1"></div>
</article></body></html>`
	yamlPath, htmlPath := writeDriftTestPair(t, driftTestYAML, html)

	var buf bytes.Buffer
	checkPlanDrift(yamlPath, htmlPath, &buf)

	if !strings.Contains(buf.String(), "slice count: yaml=2 html=1") {
		t.Errorf("expected slice count drift warning, got:\n%s", buf.String())
	}
}

func TestCheckPlanDrift_NoDrift(t *testing.T) {
	html := `<html><head><title>Plan: Original Title</title></head><body>
<article id="plan-drifttest" data-status="finalized">
<div data-slice="1"></div><div data-slice="2"></div>
</article></body></html>`
	yamlPath, htmlPath := writeDriftTestPair(t, driftTestYAML, html)

	var buf bytes.Buffer
	checkPlanDrift(yamlPath, htmlPath, &buf)

	if buf.Len() != 0 {
		t.Errorf("expected no warnings, got:\n%s", buf.String())
	}
}

func TestRenderedYAMLPlanPriorityRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "plans"), 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	plan := `meta:
    id: plan-priority
    title: Priority Plan
    description: test
    created_at: "2026-05-07"
    status: active
    priority: high
    version: 1
design:
    problem: p
    goals: []
    constraints: []
    approved: false
    comment: ""
slices: []
questions: []
`
	if err := os.WriteFile(filepath.Join(wipnoteDir, "plans", "plan-priority.yaml"), []byte(plan), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := renderPlanToFile(wipnoteDir, "plan-priority"); err != nil {
		t.Fatalf("renderPlanToFile: %v", err)
	}
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	out := captureStdout(t, func() {
		if err := runPlanShowWithFormat("plan-priority", "text"); err != nil {
			t.Fatalf("runPlanShowWithFormat: %v", err)
		}
	})

	if !strings.Contains(out, "Priority  high") {
		t.Fatalf("rendered YAML plan did not round-trip high priority:\n%s", out)
	}
}

// TestPlanShow_PartialIDResolves verifies that a partial plan ID is resolved
// to its canonical form before building YAML/HTML paths, so drift-check runs
// against the real files (regression for roborev-50: partial IDs silently
// skipped the drift check).
func TestPlanShow_PartialIDResolves(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Also create sibling subdirs so resolveID's walker is happy.
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "specs"} {
		_ = os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	yamlBody := strings.ReplaceAll(driftTestYAML, "plan-drifttest", "plan-abcd1234")
	htmlBody := `<html><head><title>Plan: Original Title</title></head><body>
<article id="plan-abcd1234" data-status="draft">
<div data-slice="1"></div><div data-slice="2"></div>
</article></body></html>`
	if err := os.WriteFile(filepath.Join(plansDir, "plan-abcd1234.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plansDir, "plan-abcd1234.html"), []byte(htmlBody), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := workitem.ResolvePartialID(dir, "plan-abcd")
	if err != nil {
		t.Fatalf("ResolvePartialID: %v", err)
	}
	if resolved != "plan-abcd1234" {
		t.Fatalf("resolved = %q, want plan-abcd1234", resolved)
	}

	var buf bytes.Buffer
	checkPlanDrift(
		filepath.Join(plansDir, resolved+".yaml"),
		filepath.Join(plansDir, resolved+".html"),
		&buf,
	)
	if !strings.Contains(buf.String(), `status: yaml="finalized" html="draft"`) {
		t.Errorf("expected drift warning after partial-ID resolve, got:\n%s", buf.String())
	}
}

func TestCheckPlanDrift_MissingFilesSilent(t *testing.T) {
	var buf bytes.Buffer
	checkPlanDrift("/nonexistent/foo.yaml", "/nonexistent/foo.html", &buf)
	if buf.Len() != 0 {
		t.Errorf("expected silent on missing files, got:\n%s", buf.String())
	}
}
