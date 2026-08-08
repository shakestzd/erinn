package htmlparse

import (
	"reflect"
	"testing"
)

// Parser half of the edge-property round-trip (bug-eb141e88). These fixtures
// are hand-written rather than produced by core/workitem's writer so the read
// contract is pinned independently of the writer that emits it.

func parseSingleEdge(t *testing.T, anchor string) (string, map[string]string) {
	t.Helper()
	html := `<!DOCTYPE html><html><body>
<article id="feat-00000001" data-type="feature">
  <header><h1>T</h1></header>
  <nav data-graph-edges>
    <section data-edge-type="blocked_by"><ul><li>` + anchor + `</li></ul></section>
  </nav>
</article></body></html>`

	node, err := ParseString(html)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	edges := node.Edges["blocked_by"]
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	return string(edges[0].Relationship), edges[0].Properties
}

func TestParseEdgeProps_AttributeEncoded(t *testing.T) {
	rel, props := parseSingleEdge(t,
		`<a href="feat-00000002.html" data-relationship="blocked_by" data-since="2026-08-06T12:00:00" `+
			`data-origin="plan_slice_deps" data-slice_num="3">x</a>`)

	if rel != "blocked_by" {
		t.Errorf("relationship: got %q", rel)
	}
	want := map[string]string{"origin": "plan_slice_deps", "slice_num": "3"}
	if !reflect.DeepEqual(props, want) {
		t.Errorf("properties:\n got %#v\nwant %#v", props, want)
	}
}

func TestParseEdgeProps_JSONEscapeHatch(t *testing.T) {
	_, props := parseSingleEdge(t,
		`<a href="feat-00000002.html" data-relationship="blocked_by" data-origin="batch_apply" `+
			`data-edge-props="{&#34;Mixed-Case&#34;:&#34;kept&#34;,&#34;key with sp&#34;:&#34;v&#34;}">x</a>`)

	want := map[string]string{
		"origin":      "batch_apply",
		"Mixed-Case":  "kept",
		"key with sp": "v",
	}
	if !reflect.DeepEqual(props, want) {
		t.Errorf("properties:\n got %#v\nwant %#v", props, want)
	}
}

// TestParseEdgeProps_LegacyAndAbsent is the backward-compatibility case: every
// .wipnote/**/*.html written before this format carries no property markup at
// all, and some hand-written navs carry a bare data-tag. Both must parse, and
// an edge with no properties must come back nil rather than an empty map.
func TestParseEdgeProps_LegacyAndAbsent(t *testing.T) {
	if _, props := parseSingleEdge(t,
		`<a href="feat-00000002.html" data-relationship="blocked_by">x</a>`); props != nil {
		t.Errorf("propertyless edge should parse to nil Properties, got %#v", props)
	}

	_, props := parseSingleEdge(t,
		`<a href="feat-00000002.html" data-relationship="relates_to" data-tag="needs-triage-dup">x</a>`)
	if want := (map[string]string{"tag": "needs-triage-dup"}); !reflect.DeepEqual(props, want) {
		t.Errorf("legacy data-tag:\n got %#v\nwant %#v", props, want)
	}
}

// TestParseEdgeProps_MalformedJSONIsTolerated — HTML is the canonical store, so
// a hand-edited or truncated payload must not fail a rebuild or take the
// attribute-encoded properties down with it.
func TestParseEdgeProps_MalformedJSONIsTolerated(t *testing.T) {
	_, props := parseSingleEdge(t,
		`<a href="feat-00000002.html" data-relationship="blocked_by" data-origin="batch_apply" `+
			`data-edge-props="{not json">x</a>`)

	if want := (map[string]string{"origin": "batch_apply"}); !reflect.DeepEqual(props, want) {
		t.Errorf("properties:\n got %#v\nwant %#v", props, want)
	}
}

func TestEdgePropKeyIsAttrSafe(t *testing.T) {
	safe := []string{"origin", "plan_id", "slice-num", "similarity_score", "tag", "a1"}
	unsafe := []string{"", "Mixed", "key with sp", "1leading", "weird=key", "relationship", "since", "edge-props"}

	for _, k := range safe {
		if !EdgePropKeyIsAttrSafe(k) {
			t.Errorf("%q should be attribute-safe", k)
		}
	}
	for _, k := range unsafe {
		if EdgePropKeyIsAttrSafe(k) {
			t.Errorf("%q should NOT be attribute-safe", k)
		}
	}
}
