package workitem

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// Edge-property round-trip tests (bug-eb141e88).
//
// models.Edge.Properties used to persist only through the SQLite dual-write in
// Collection.AddEdge: the writer emitted no property markup at all, so a
// rebuild from canonical HTML silently dropped every edge attribute — the
// dedup heuristic's similarity_score, the origin stamps FindBottlenecks filters
// on, everything. These tests pin the writer half of the fix; the parser half
// is pinned in core/htmlparse.

func edgePropsFixture(t *testing.T, props map[string]string) *models.Node {
	t.Helper()
	return &models.Node{
		ID:        "feat-edgeprops",
		Title:     "Edge property round-trip",
		Type:      "feature",
		Status:    models.StatusInProgress,
		Priority:  models.PriorityHigh,
		CreatedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		Edges: map[string][]models.Edge{
			"blocked_by": {{
				TargetID:     "feat-blocker0",
				Relationship: models.RelationshipType("blocked_by"),
				Title:        "Blocker",
				Since:        time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
				Properties:   props,
			}},
		},
	}
}

// writeAndReparse writes the node and parses the file back, returning the raw
// HTML and the single edge that came back.
func writeAndReparse(t *testing.T, node *models.Node) (string, models.Edge) {
	t.Helper()
	dir := t.TempDir()
	path, err := WriteNodeHTML(dir, node)
	if err != nil {
		t.Fatalf("WriteNodeHTML: %v", err)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	parsed, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	edges := parsed.Edges["blocked_by"]
	if len(edges) != 1 {
		t.Fatalf("expected 1 blocked_by edge, got %d\n--- html ---\n%s", len(edges), raw)
	}
	return string(raw), edges[0]
}

// TestWriteNodeHTML_EdgePropertiesRoundTrip covers the two live cases: the
// origin stamp graph.FindBottlenecks filters on, and the dedup heuristic's
// similarity_score. Attribute-safe keys must appear as readable data
// attributes, not as an opaque payload.
func TestWriteNodeHTML_EdgePropertiesRoundTrip(t *testing.T) {
	props := map[string]string{
		"origin":           "plan_slice_deps",
		"plan_id":          "plan-1a2b3c4d",
		"slice_num":        "3",
		"tag":              "needs-triage-dup",
		"similarity_score": "0.842",
	}
	html, edge := writeAndReparse(t, edgePropsFixture(t, props))

	for _, want := range []string{
		`data-origin="plan_slice_deps"`,
		`data-plan_id="plan-1a2b3c4d"`,
		`data-similarity_score="0.842"`,
		`data-tag="needs-triage-dup"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("output missing %s\n--- html ---\n%s", want, html)
		}
	}
	if strings.Contains(html, htmlparse.EdgePropsAttr) {
		t.Errorf("attribute-safe keys should not use the JSON escape hatch\n--- html ---\n%s", html)
	}
	if !reflect.DeepEqual(edge.Properties, props) {
		t.Errorf("properties lost on round-trip:\n got %#v\nwant %#v", edge.Properties, props)
	}
	// The edge's own fields must survive alongside its properties.
	if edge.TargetID != "feat-blocker0" || string(edge.Relationship) != "blocked_by" {
		t.Errorf("edge identity corrupted: %+v", edge)
	}
	if edge.Since.IsZero() {
		t.Errorf("data-since lost: %+v", edge)
	}
}

// TestWriteNodeHTML_EdgePropertiesAwkwardKeys pins the escape hatch. An
// attribute name cannot express an uppercase, spaced, or punctuated key, so
// those keys must not be silently mangled into one that parses differently.
func TestWriteNodeHTML_EdgePropertiesAwkwardKeys(t *testing.T) {
	props := map[string]string{
		"origin":       "batch_apply",
		"Mixed-Case":   "kept",
		"key with sp":  "spaces are illegal in attribute names",
		"weird=key\"x": "punctuation",
		"":             "empty key",
		// Reserved: writing data-relationship as a property would collide with
		// the edge's own relationship attribute.
		"relationship": "not_the_real_relationship",
		"since":        "not_the_real_since",
	}
	html, edge := writeAndReparse(t, edgePropsFixture(t, props))

	if !strings.Contains(html, `data-origin="batch_apply"`) {
		t.Errorf("attribute-safe key should stay an attribute\n--- html ---\n%s", html)
	}
	if !strings.Contains(html, htmlparse.EdgePropsAttr+`="`) {
		t.Errorf("awkward keys should use the JSON escape hatch\n--- html ---\n%s", html)
	}
	if !reflect.DeepEqual(edge.Properties, props) {
		t.Errorf("properties lost on round-trip:\n got %#v\nwant %#v", edge.Properties, props)
	}
	if string(edge.Relationship) != "blocked_by" {
		t.Errorf("reserved-name property overwrote the relationship: %q", edge.Relationship)
	}
	if want := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC); !edge.Since.Equal(want) {
		t.Errorf("reserved-name property overwrote data-since: got %v want %v", edge.Since, want)
	}
}

// TestWriteNodeHTML_EdgePropertyValuesAreEscaped verifies that a value which
// could close the attribute or inject markup cannot: it must round-trip
// verbatim and must not appear raw in the file.
func TestWriteNodeHTML_EdgePropertyValuesAreEscaped(t *testing.T) {
	hostile := `" onmouseover="alert(1)` + " <b>&</b> 'single'"
	props := map[string]string{
		"note":         hostile,
		"Awkward Note": hostile,
	}
	html, edge := writeAndReparse(t, edgePropsFixture(t, props))

	// Breaking out would require a real (unescaped) quote closing the value
	// and opening a new attribute. Escaped, the same bytes appear only as
	// entity-encoded text inside the value, which is inert.
	if strings.Contains(html, `onmouseover="alert`) {
		t.Errorf("hostile value broke out of its attribute\n--- html ---\n%s", html)
	}
	if strings.Contains(html, `<b>`) {
		t.Errorf("hostile value injected markup\n--- html ---\n%s", html)
	}
	if got := edge.Properties["note"]; got != hostile {
		t.Errorf("attribute-encoded value mangled:\n got %q\nwant %q", got, hostile)
	}
	if got := edge.Properties["Awkward Note"]; got != hostile {
		t.Errorf("JSON-encoded value mangled:\n got %q\nwant %q", got, hostile)
	}
}

// TestWriteNodeHTML_NoPropertiesEmitsNoMarkup keeps the common case clean:
// pre-existing edges have no properties and must not gain empty markup, and
// must parse back as nil rather than an empty map.
func TestWriteNodeHTML_NoPropertiesEmitsNoMarkup(t *testing.T) {
	html, edge := writeAndReparse(t, edgePropsFixture(t, nil))

	if strings.Contains(html, htmlparse.EdgePropsAttr) {
		t.Errorf("empty properties emitted escape-hatch markup\n--- html ---\n%s", html)
	}
	if edge.Properties != nil {
		t.Errorf("expected nil Properties for a propertyless edge, got %#v", edge.Properties)
	}
}

// TestWriteNodeHTML_EdgePropertiesAreStable pins render determinism: rewriting
// a node parsed from its own output must produce byte-identical HTML. Without
// sorted keys (and sorted relationship groups) Go's map iteration would churn
// the committed artifact on every write.
func TestWriteNodeHTML_EdgePropertiesAreStable(t *testing.T) {
	node := edgePropsFixture(t, map[string]string{
		"origin":    "plan_slice_deps",
		"plan_id":   "plan-1a2b3c4d",
		"slice_num": "3",
		"Awkward":   "x",
		"another":   "y",
	})
	// A second relationship group so group ordering is exercised too.
	node.Edges["relates_to"] = []models.Edge{{
		TargetID:     "bug-0badc0de",
		Relationship: models.RelationshipType("relates_to"),
		Properties:   map[string]string{"tag": "needs-triage-dup", "similarity_score": "0.900"},
	}}

	first, _ := writeAndReparse(t, node)
	for i := 0; i < 5; i++ {
		again, _ := writeAndReparse(t, node)
		if again != first {
			t.Fatalf("render is not deterministic on pass %d\n--- first ---\n%s\n--- again ---\n%s", i, first, again)
		}
	}

	// Round-trip through the parser and back must also be byte-stable.
	dir := t.TempDir()
	path, err := WriteNodeHTML(dir, node)
	if err != nil {
		t.Fatalf("WriteNodeHTML: %v", err)
	}
	reparsed, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	reparsed.CreatedAt = node.CreatedAt
	reparsed.UpdatedAt = node.UpdatedAt
	rewritten, err := WriteNodeHTML(t.TempDir(), reparsed)
	if err != nil {
		t.Fatalf("WriteNodeHTML (rewrite): %v", err)
	}
	got, err := os.ReadFile(rewritten) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("read rewritten: %v", err)
	}
	if string(got) != first {
		t.Errorf("write→parse→write is not a fixed point\n--- want ---\n%s\n--- got ---\n%s", first, got)
	}
}
