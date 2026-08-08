package htmlparse

import (
	"reflect"
	"testing"
)

// Parser half of the node-property round-trip (bug-c65a5f4e). These fixtures
// are hand-written rather than produced by core/workitem's writer so the read
// contract is pinned independently of the writer that emits it.

func parseArticleProps(t *testing.T, articleAttrs string) map[string]any {
	t.Helper()
	html := `<!DOCTYPE html><html><body>
<article id="feat-00000001" data-type="feature"` + articleAttrs + `>
  <header><h1>T</h1></header>
</article></body></html>`

	node, err := ParseString(html)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	return node.Properties
}

func TestParseNodeProps_AttributeEncoded(t *testing.T) {
	props := parseArticleProps(t, ` data-standalone_reason="pre-enforcement" data-created_in_session="sess-1"`)

	want := map[string]any{
		"standalone_reason":  "pre-enforcement",
		"created_in_session": "sess-1",
	}
	if !reflect.DeepEqual(props, want) {
		t.Errorf("properties:\n got %#v\nwant %#v", props, want)
	}
}

func TestParseNodeProps_JSONEscapeHatch(t *testing.T) {
	props := parseArticleProps(t,
		` data-origin="batch_apply" data-node-props="{&#34;Mixed-Case&#34;:&#34;kept&#34;,&#34;retry_count&#34;:3}"`)

	want := map[string]any{
		"origin":      "batch_apply",
		"Mixed-Case":  "kept",
		"retry_count": float64(3), // encoding/json decodes numbers into float64
	}
	if !reflect.DeepEqual(props, want) {
		t.Errorf("properties:\n got %#v\nwant %#v", props, want)
	}
}

// TestParseNodeProps_LegacyAndAbsent is the backward-compatibility case: every
// .wipnote/**/*.html written before this format carries no property markup at
// all. A node with no properties must come back nil rather than an empty map,
// and named fields (data-status, etc.) must never leak into Properties.
func TestParseNodeProps_LegacyAndAbsent(t *testing.T) {
	if props := parseArticleProps(t, ` data-status="todo" data-priority="high"`); props != nil {
		t.Errorf("propertyless node should parse to nil Properties, got %#v", props)
	}

	props := parseArticleProps(t, ` data-status="todo" data-tag="needs-triage"`)
	if want := (map[string]any{"tag": "needs-triage"}); !reflect.DeepEqual(props, want) {
		t.Errorf("legacy data-tag:\n got %#v\nwant %#v", props, want)
	}
}

// TestParseNodeProps_MalformedJSONIsTolerated — HTML is the canonical store,
// so a hand-edited or truncated payload must not fail a rebuild or take the
// attribute-encoded properties down with it.
func TestParseNodeProps_MalformedJSONIsTolerated(t *testing.T) {
	props := parseArticleProps(t, ` data-origin="batch_apply" data-node-props="{not json"`)

	if want := (map[string]any{"origin": "batch_apply"}); !reflect.DeepEqual(props, want) {
		t.Errorf("properties:\n got %#v\nwant %#v", props, want)
	}
}

func TestNodePropKeyIsAttrSafe(t *testing.T) {
	safe := []string{"standalone_reason", "created_in_session", "affected_files", "origin", "a1"}
	unsafe := []string{
		"", "Mixed", "key with sp", "1leading", "weird=key",
		// Reserved: these collide with named Node fields rendered on <article>.
		"status", "priority", "type", "created", "updated",
		"agent-assigned", "track-id", "plan-task-id", "spike-subtype",
		"claimed-at", "claimed-by-session",
		"created-by-agent", "created-by-model", "created-by-role", "created-by-cli-version",
		"node-props",
	}

	for _, k := range safe {
		if !NodePropKeyIsAttrSafe(k) {
			t.Errorf("%q should be attribute-safe", k)
		}
	}
	for _, k := range unsafe {
		if NodePropKeyIsAttrSafe(k) {
			t.Errorf("%q should NOT be attribute-safe", k)
		}
	}
}
