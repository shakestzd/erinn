package workitem

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/models"
)

// TestWriteNodeHTML_RendersProvenanceAttrs verifies the four data-created-by-*
// attributes appear on <article> when the Node carries provenance fields,
// and that empty fields are omitted entirely (so legacy items round-trip
// without spurious attributes).
func TestWriteNodeHTML_RendersProvenanceAttrs(t *testing.T) {
	dir := t.TempDir()
	node := &models.Node{
		ID:                  "feat-prov0001",
		Title:               "Provenance render check",
		Type:                "feature",
		Status:              models.StatusTodo,
		Priority:            models.PriorityMedium,
		CreatedAt:           time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		UpdatedAt:           time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		CreatedByAgent:      "claude-code",
		CreatedByModel:      "claude-opus-4-7",
		CreatedByRole:       "architect-coder",
		CreatedByCLIVersion: "1.2.3",
	}

	path, err := WriteNodeHTML(dir, node)
	if err != nil {
		t.Fatalf("WriteNodeHTML: %v", err)
	}

	html, err := readFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}

	wantAttrs := []string{
		`data-created-by-agent="claude-code"`,
		`data-created-by-model="claude-opus-4-7"`,
		`data-created-by-role="architect-coder"`,
		`data-created-by-cli-version="1.2.3"`,
	}
	for _, attr := range wantAttrs {
		if !strings.Contains(html, attr) {
			t.Errorf("output missing %s\n--- html ---\n%s", attr, html)
		}
	}
}

func TestWriteNodeHTML_OmitsEmptyProvenanceAttrs(t *testing.T) {
	dir := t.TempDir()
	node := &models.Node{
		ID:        "feat-prov0002",
		Title:     "No provenance",
		Type:      "feature",
		Status:    models.StatusTodo,
		Priority:  models.PriorityMedium,
		CreatedAt: time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
	}
	path, err := WriteNodeHTML(dir, node)
	if err != nil {
		t.Fatalf("WriteNodeHTML: %v", err)
	}
	html, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, attr := range []string{"data-created-by-agent", "data-created-by-model", "data-created-by-role", "data-created-by-cli-version"} {
		if strings.Contains(html, attr) {
			t.Errorf("legacy node should omit %s; got:\n%s", attr, html)
		}
	}
}

// TestWriteNodeHTML_StepProvenance asserts that a step with Agent and the
// new CreatedBy* fields renders the four data-created-by-* attributes on <li>.
func TestWriteNodeHTML_StepProvenance(t *testing.T) {
	dir := t.TempDir()
	node := &models.Node{
		ID:        "feat-prov0003",
		Title:     "Step provenance",
		Type:      "feature",
		Status:    models.StatusTodo,
		Priority:  models.PriorityMedium,
		CreatedAt: time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
		Steps: []models.Step{
			{
				StepID:              "step-feat-prov0003-0",
				Description:         "first step",
				Agent:               "claude-code",
				CreatedByModel:      "claude-opus-4-7",
				CreatedByRole:       "architect-coder",
				CreatedByCLIVersion: "1.2.3",
			},
		},
	}
	path, err := WriteNodeHTML(dir, node)
	if err != nil {
		t.Fatalf("WriteNodeHTML: %v", err)
	}
	html, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wantAttrs := []string{
		`data-created-by-agent="claude-code"`,
		`data-created-by-model="claude-opus-4-7"`,
		`data-created-by-role="architect-coder"`,
		`data-created-by-cli-version="1.2.3"`,
	}
	for _, attr := range wantAttrs {
		if !strings.Contains(html, attr) {
			t.Errorf("step <li> missing %s\nhtml:\n%s", attr, html)
		}
	}
}

// TestWriteNodeHTML_SanitizesHostPaths proves the render-layer fix for
// bug-ff6a3286: absolute host-local path prefixes in Title, Content, and Step
// descriptions must NOT appear verbatim in the rendered HTML file.
func TestWriteNodeHTML_SanitizesHostPaths(t *testing.T) {
	dir := t.TempDir()
	hostPath := "/workspaces/wipnote/foo.go"
	node := &models.Node{
		ID:        "bug-hostpath01",
		Title:     "Fix " + hostPath,
		Type:      "bug",
		Status:    models.StatusTodo,
		Priority:  models.PriorityMedium,
		CreatedAt: time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC),
		Content:   "Edited " + hostPath + " to fix the bug.",
		Steps: []models.Step{
			{
				StepID:      "step-1",
				Description: "Apply patch to " + hostPath,
			},
		},
	}

	path, err := WriteNodeHTML(dir, node)
	if err != nil {
		t.Fatalf("WriteNodeHTML: %v", err)
	}
	html, err := readFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}

	if strings.Contains(html, hostPath) {
		t.Errorf("rendered HTML still contains host path %q\n--- excerpt ---\n%s", hostPath, html)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
