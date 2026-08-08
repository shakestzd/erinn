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

// Node-property round-trip tests (bug-c65a5f4e).
//
// models.Node.Properties is a real map[string]any that production CLI code
// writes to via EditBuilder.SetProperty (standalone_reason,
// created_in_session, affected_files), but the writer never rendered it and
// the parser never read it back — a rewrite of any node silently dropped it.
// These tests pin the writer half of the fix; the parser half is pinned in
// core/htmlparse.

func nodePropsFixture(props map[string]any) *models.Node {
	return &models.Node{
		ID:         "feat-nodeprops",
		Title:      "Node property round-trip",
		Type:       "feature",
		Status:     models.StatusInProgress,
		Priority:   models.PriorityHigh,
		CreatedAt:  time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		Properties: props,
	}
}

// writeAndReparseNode writes the node and parses the file back, returning the
// raw HTML and the reparsed Node.
func writeAndReparseNode(t *testing.T, node *models.Node) (string, *models.Node) {
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
	return string(raw), parsed
}

// TestWriteNodeHTML_NodePropertiesRoundTrip covers the three live call sites
// (standalone_reason, created_in_session, affected_files) — all attribute-safe
// string values, which must appear as readable data attributes.
func TestWriteNodeHTML_NodePropertiesRoundTrip(t *testing.T) {
	props := map[string]any{
		"standalone_reason":  "pre-enforcement",
		"created_in_session": "019ebc63ba7ae905adb1f8db7504",
		"affected_files":     "cmd/foo.go,cmd/bar.go",
	}
	html, node := writeAndReparseNode(t, nodePropsFixture(props))

	for _, want := range []string{
		`data-standalone_reason="pre-enforcement"`,
		`data-created_in_session="019ebc63ba7ae905adb1f8db7504"`,
		`data-affected_files="cmd/foo.go,cmd/bar.go"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("output missing %s\n--- html ---\n%s", want, html)
		}
	}
	if strings.Contains(html, htmlparse.NodePropsAttr) {
		t.Errorf("attribute-safe string keys should not use the JSON escape hatch\n--- html ---\n%s", html)
	}
	if !reflect.DeepEqual(node.Properties, props) {
		t.Errorf("properties lost on round-trip:\n got %#v\nwant %#v", node.Properties, props)
	}
}

// TestWriteNodeHTML_NodePropertiesNonStringValues covers a non-string value
// (bool, float64, slice) — these cannot be flattened to an HTML attribute
// string without losing their Go type, so they must take the JSON escape
// hatch even though the key itself is attribute-safe.
func TestWriteNodeHTML_NodePropertiesNonStringValues(t *testing.T) {
	props := map[string]any{
		"retry_count": float64(3),
		"is_flaky":    true,
		"tags":        []any{"a", "b"},
	}
	html, node := writeAndReparseNode(t, nodePropsFixture(props))

	if !strings.Contains(html, htmlparse.NodePropsAttr+`="`) {
		t.Errorf("non-string values should use the JSON escape hatch\n--- html ---\n%s", html)
	}
	for _, unwanted := range []string{`data-retry_count=`, `data-is_flaky=`, `data-tags=`} {
		if strings.Contains(html, unwanted) {
			t.Errorf("non-string value wrongly rendered as a plain attribute (%s)\n--- html ---\n%s", unwanted, html)
		}
	}
	if !reflect.DeepEqual(node.Properties, props) {
		t.Errorf("properties lost or retyped on round-trip:\n got %#v\nwant %#v", node.Properties, props)
	}
}

// TestWriteNodeHTML_NodePropertiesAwkwardKeys pins the escape hatch for
// attr-unsafe keys, and confirms a property cannot clobber a reserved named
// attribute (e.g. "status") on <article>.
func TestWriteNodeHTML_NodePropertiesAwkwardKeys(t *testing.T) {
	props := map[string]any{
		"origin":      "batch_apply",
		"Mixed-Case":  "kept",
		"key with sp": "spaces are illegal in attribute names",
		// Reserved: writing data-status as a property would collide with the
		// node's own status attribute.
		"status": "not_the_real_status",
	}
	html, node := writeAndReparseNode(t, nodePropsFixture(props))

	if !strings.Contains(html, `data-origin="batch_apply"`) {
		t.Errorf("attribute-safe key should stay an attribute\n--- html ---\n%s", html)
	}
	if !strings.Contains(html, htmlparse.NodePropsAttr+`="`) {
		t.Errorf("awkward keys should use the JSON escape hatch\n--- html ---\n%s", html)
	}
	if !reflect.DeepEqual(node.Properties, props) {
		t.Errorf("properties lost on round-trip:\n got %#v\nwant %#v", node.Properties, props)
	}
	if string(node.Status) != "in-progress" {
		t.Errorf("reserved-name property overwrote the real status: %q", node.Status)
	}
}

// TestWriteNodeHTML_NoNodePropertiesEmitsNoMarkup keeps the common case
// clean: nodes with no properties (the overwhelming majority) must not gain
// empty markup, and must parse back as nil rather than an empty map.
func TestWriteNodeHTML_NoNodePropertiesEmitsNoMarkup(t *testing.T) {
	html, node := writeAndReparseNode(t, nodePropsFixture(nil))

	if strings.Contains(html, htmlparse.NodePropsAttr) {
		t.Errorf("empty properties emitted escape-hatch markup\n--- html ---\n%s", html)
	}
	if node.Properties != nil {
		t.Errorf("expected nil Properties for a propertyless node, got %#v", node.Properties)
	}
}

// TestWriteNodeHTML_NodePropertiesAreStable pins render determinism:
// rewriting a node parsed from its own output must produce byte-identical
// HTML.
func TestWriteNodeHTML_NodePropertiesAreStable(t *testing.T) {
	node := nodePropsFixture(map[string]any{
		"standalone_reason": "pre-enforcement",
		"Awkward":           "x",
		"another":           "y",
		"retry_count":       float64(2),
	})

	first, _ := writeAndReparseNode(t, node)
	for i := 0; i < 5; i++ {
		again, _ := writeAndReparseNode(t, node)
		if again != first {
			t.Fatalf("render is not deterministic on pass %d\n--- first ---\n%s\n--- again ---\n%s", i, first, again)
		}
	}
}
