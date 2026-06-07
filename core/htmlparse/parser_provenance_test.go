package htmlparse

import "testing"

// TestParseFile_Provenance verifies the parser reads back the four
// data-created-by-* attributes on <article> and on each <li> step.
func TestParseFile_Provenance(t *testing.T) {
	html := `<!DOCTYPE html>
<html lang="en">
<head><title>Provenance test</title></head>
<body>
<article id="feat-prov0001"
         data-type="feature"
         data-status="todo"
         data-priority="medium"
         data-created="2026-05-07T12:00:00Z"
         data-updated="2026-05-07T12:00:00Z"
         data-created-by-agent="claude-code"
         data-created-by-model="claude-opus-4-7"
         data-created-by-role="architect-coder"
         data-created-by-cli-version="1.2.3">
  <header><h1>Provenance test</h1></header>
  <section data-steps>
    <h3>Implementation Steps</h3>
    <ol>
      <li data-completed="false"
          data-step-id="step-feat-prov0001-0"
          data-agent="claude-code"
          data-created-by-agent="claude-code"
          data-created-by-model="claude-opus-4-7"
          data-created-by-role="architect-coder"
          data-created-by-cli-version="1.2.3">first step</li>
    </ol>
  </section>
</article>
</body>
</html>`
	node, err := ParseString(html)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	if node.CreatedByAgent != "claude-code" {
		t.Errorf("CreatedByAgent = %q, want claude-code", node.CreatedByAgent)
	}
	if node.CreatedByModel != "claude-opus-4-7" {
		t.Errorf("CreatedByModel = %q, want claude-opus-4-7", node.CreatedByModel)
	}
	if node.CreatedByRole != "architect-coder" {
		t.Errorf("CreatedByRole = %q, want architect-coder", node.CreatedByRole)
	}
	if node.CreatedByCLIVersion != "1.2.3" {
		t.Errorf("CreatedByCLIVersion = %q, want 1.2.3", node.CreatedByCLIVersion)
	}

	if len(node.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(node.Steps))
	}
	step := node.Steps[0]
	if step.Agent != "claude-code" {
		t.Errorf("step.Agent = %q, want claude-code", step.Agent)
	}
	if step.CreatedByModel != "claude-opus-4-7" {
		t.Errorf("step.CreatedByModel = %q, want claude-opus-4-7", step.CreatedByModel)
	}
	if step.CreatedByRole != "architect-coder" {
		t.Errorf("step.CreatedByRole = %q, want architect-coder", step.CreatedByRole)
	}
	if step.CreatedByCLIVersion != "1.2.3" {
		t.Errorf("step.CreatedByCLIVersion = %q, want 1.2.3", step.CreatedByCLIVersion)
	}
}

// TestParseFile_LegacyNoProvenance verifies items without provenance
// attributes parse to empty strings (rendered as "unknown" by show output).
func TestParseFile_LegacyNoProvenance(t *testing.T) {
	html := `<!DOCTYPE html>
<html lang="en">
<head><title>Legacy</title></head>
<body>
<article id="feat-legacy0001"
         data-type="feature"
         data-status="todo"
         data-priority="medium">
  <header><h1>Legacy</h1></header>
</article>
</body>
</html>`
	node, err := ParseString(html)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	if node.CreatedByAgent != "" || node.CreatedByModel != "" ||
		node.CreatedByRole != "" || node.CreatedByCLIVersion != "" {
		t.Errorf("expected empty provenance for legacy node, got %+v", node)
	}
}
