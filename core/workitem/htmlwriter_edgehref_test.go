package workitem

import (
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// TestWriteNodeHTML_CrossCollectionEdgeHrefs verifies that edge links from a
// bug to items in other collections emit collection-aware relative hrefs
// (../tracks/…, ../sessions/…) rather than bare "<id>.html" filenames, which
// would 404 across collection directories (bug-fddf5820, finding 1).
func TestWriteNodeHTML_CrossCollectionEdgeHrefs(t *testing.T) {
	dir := t.TempDir()
	node := &models.Node{
		ID:        "bug-fddf5820",
		Title:     "Edge href check",
		Type:      "bug",
		Status:    models.StatusInProgress,
		Priority:  models.PriorityHigh,
		CreatedAt: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		Edges: map[string][]models.Edge{
			"part_of": {{
				TargetID:     "trk-13e39042",
				Relationship: models.RelationshipType("part_of"),
				Title:        "Some track",
			}},
			"implemented_in": {{
				TargetID:     "127926be-6a1c-4045-a347-e42785ec5839",
				Relationship: models.RelationshipType("implemented_in"),
				Title:        "A session",
			}},
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

	wantHrefs := []string{
		`href="../tracks/trk-13e39042.html"`,
		`href="../sessions/127926be-6a1c-4045-a347-e42785ec5839.html"`,
	}
	for _, want := range wantHrefs {
		if !strings.Contains(html, want) {
			t.Errorf("output missing %s\n--- html ---\n%s", want, html)
		}
	}
	// The old bare-filename form must not survive for cross-collection edges.
	if strings.Contains(html, `href="trk-13e39042.html"`) {
		t.Errorf("bare cross-collection href leaked into output\n%s", html)
	}
}

// TestEdgeHref_PrefixMapping is a table test for the ID-prefix → collection
// directory mapping used by edge hrefs (bug-fddf5820, finding 1).
func TestEdgeHref_PrefixMapping(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"feat-12345678", "../features/feat-12345678.html"},
		{"bug-12345678", "../bugs/bug-12345678.html"},
		{"spk-12345678", "../spikes/spk-12345678.html"},
		{"trk-12345678", "../tracks/trk-12345678.html"},
		{"pln-12345678", "../plans/pln-12345678.html"},
		// plan- is the prefix actually used by plan IDs (roborev job 126);
		// pln- is retained above for defensive coverage.
		{"plan-edeb2163", "../plans/plan-edeb2163.html"},
		{"spc-12345678", "../specs/spc-12345678.html"},
		{"127926be-6a1c-4045-a347-e42785ec5839", "../sessions/127926be-6a1c-4045-a347-e42785ec5839.html"},
		// Unrecognized prefix falls back to bare same-directory href.
		{"weird-id", "weird-id.html"},
	}
	for _, c := range cases {
		if got := edgeHref(c.id); got != c.want {
			t.Errorf("edgeHref(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

// TestWriteNodeHTML_EscapesAngleBracketsInDescription verifies that plain-text
// descriptions containing angle-bracket placeholders are HTML entity-escaped
// at render time, so they survive the HTML round-trip instead of being parsed
// as tags and silently dropped (bug-fddf5820, finding 2).
func TestWriteNodeHTML_EscapesAngleBracketsInDescription(t *testing.T) {
	dir := t.TempDir()
	node := &models.Node{
		ID:        "feat-escape01",
		Title:     "Escape check",
		Type:      "feature",
		Status:    models.StatusTodo,
		Priority:  models.PriorityMedium,
		CreatedAt: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		Content:   "Run wipnote bug start <id> then edit <path/to/file> & save.",
	}

	path, err := WriteNodeHTML(dir, node)
	if err != nil {
		t.Fatalf("WriteNodeHTML: %v", err)
	}
	html, err := readFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}

	// Angle brackets and ampersand must be entity-escaped.
	wantEntities := []string{"&lt;id&gt;", "&lt;path/to/file&gt;", "&amp;"}
	for _, want := range wantEntities {
		if !strings.Contains(html, want) {
			t.Errorf("output missing escaped %q\n--- html ---\n%s", want, html)
		}
	}
	// The raw placeholder must not survive as a literal tag.
	if strings.Contains(html, "<id>") {
		t.Errorf("raw <id> placeholder leaked into output (would be dropped on re-ingest)\n%s", html)
	}
}
