package htmlparse

import (
	"encoding/json"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Node-property wire format (bug-c65a5f4e).
//
// models.Node.Properties is a real map[string]any that production CLI code
// writes to via EditBuilder.SetProperty (standalone_reason,
// created_in_session, affected_files), but it was never rendered to HTML or
// parsed back — a rewrite of any node silently dropped it. This mirrors the
// edge-property format above (see edge_props.go) at the <article> level, with
// one difference: Node.Properties is map[string]any, not map[string]string,
// so a value's Go type — not just its key — decides which encoding it takes:
//
//  1. An attribute-safe key holding a string value is emitted as its own data
//     attribute, exactly like an edge property:
//
//     <article id="feat-…" data-standalone_reason="pre-enforcement" …>
//
//  2. Everything else — an attr-unsafe key, or any non-string value (bool,
//     number, slice, map, nil) that would lose its type if flattened to an
//     HTML attribute string — goes into the JSON escape hatch:
//
//     <article id="feat-…" data-node-props="{&#34;retry_count&#34;:3}" …>
//
// Reading tolerates both, in either combination, and tolerates neither being
// present: pre-existing HTML has no property markup at all and must keep
// parsing as a node with no properties.

// NodePropsAttr is the attribute holding the JSON escape hatch described above.
const NodePropsAttr = "data-node-props"

// reservedNodeAttrs are data attributes on <article> that carry named Node
// fields rather than properties. They are excluded in both directions: the
// parser never harvests them as properties (they're already parsed by name),
// and the writer never emits a property under one of these names (such a key
// takes the JSON escape hatch instead).
var reservedNodeAttrs = map[string]struct{}{
	"data-type":                   {},
	"data-status":                 {},
	"data-priority":               {},
	"data-created":                {},
	"data-updated":                {},
	"data-agent-assigned":         {},
	"data-track-id":               {},
	"data-plan-task-id":           {},
	"data-spike-subtype":          {},
	"data-claimed-at":             {},
	"data-claimed-by-session":     {},
	"data-created-by-agent":       {},
	"data-created-by-model":       {},
	"data-created-by-role":        {},
	"data-created-by-cli-version": {},
	NodePropsAttr:                 {},
}

// NodePropKeyIsAttrSafe reports whether key can be written as a data-<key>
// attribute on <article> and read back unchanged, without colliding with one
// of the node's own named fields. Keys that fail this take the JSON escape
// hatch instead. The writer half lives in core/workitem/htmlwriter.go.
func NodePropKeyIsAttrSafe(key string) bool {
	if !attrSafeEdgePropKey.MatchString(key) {
		return false
	}
	_, reserved := reservedNodeAttrs["data-"+key]
	return !reserved
}

// parseNodeProps reads an <article>'s properties from both encodings. Returns
// nil (not an empty map) when the article carries none, so that a node
// written without properties round-trips to a nil Properties map rather than
// a spurious empty one.
//
// Malformed JSON in the escape hatch is ignored rather than fatal: HTML is the
// canonical store and hand-edited files must never make a rebuild fail. The
// attribute-encoded properties on the same article still land.
func parseNodeProps(article *goquery.Selection) map[string]any {
	el := article.Get(0)
	if el == nil {
		return nil
	}

	props := make(map[string]any)
	for _, attr := range el.Attr {
		if !strings.HasPrefix(attr.Key, "data-") {
			continue
		}
		if _, reserved := reservedNodeAttrs[attr.Key]; reserved {
			continue
		}
		props[strings.TrimPrefix(attr.Key, "data-")] = attr.Val
	}

	// The escape hatch is authoritative for the keys it carries — it is the
	// lossless encoding, so it overlays the attribute harvest.
	if raw, ok := article.Attr(NodePropsAttr); ok && raw != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(raw), &extra); err == nil {
			for k, v := range extra {
				props[k] = v
			}
		}
	}

	if len(props) == 0 {
		return nil
	}
	return props
}
