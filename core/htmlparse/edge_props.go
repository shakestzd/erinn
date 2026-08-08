package htmlparse

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Edge-property wire format (bug-eb141e88).
//
// models.Edge.Properties is the only place edge-level metadata lives — the
// dedup heuristic's similarity_score, the origin stamps graph.FindBottlenecks
// filters on, and any future edge attribute. Until this format existed those
// values persisted solely via the SQLite dual-write in Collection.AddEdge and
// were lost on any rebuild from HTML, which contradicts the canonical-store
// promise that .wipnote/*.html is the source of truth.
//
// Two encodings, chosen per key:
//
//  1. Attribute-safe keys are emitted as their own data attribute:
//
//     <a href="…" data-relationship="blocked_by" data-origin="plan_slice_deps">
//
//     This is the 99% case (every property key wipnote writes today is a
//     lowercase identifier) and it is the reason to prefer per-key attributes
//     over one opaque blob: a human reading the committed HTML — or grepping
//     it — sees the metadata in place, and a diff of a changed property is a
//     one-attribute diff rather than a re-serialised payload.
//
//  2. Everything else goes into a single JSON escape hatch:
//
//     <a href="…" data-edge-props="{&#34;Weird Key&#34;:&#34;v&#34;}">
//
//     Attribute names cannot express every possible map key: the HTML parser
//     lowercases them, and space/quote/=/> are illegal outright. Silently
//     mangling such a key would reintroduce exactly the data loss this format
//     exists to prevent, so keys that cannot round-trip verbatim as an
//     attribute name are serialised as JSON instead. Values need no such
//     split — HTML escaping handles any value in either encoding.
//
// Reading tolerates both, in either combination, and tolerates neither being
// present: pre-existing HTML has no property markup at all and must keep
// parsing as an edge with no properties.

// EdgePropsAttr is the attribute holding the JSON escape hatch described above.
const EdgePropsAttr = "data-edge-props"

// reservedEdgeAttrs are data attributes on an edge anchor that carry edge
// fields rather than properties. They are excluded in both directions: the
// parser never harvests them as properties, and the writer never emits a
// property under one of these names (such a key takes the JSON escape hatch).
var reservedEdgeAttrs = map[string]struct{}{
	"data-relationship": {},
	"data-since":        {},
	EdgePropsAttr:       {},
}

// attrSafeEdgePropKey matches keys that survive a round-trip through an HTML
// attribute name byte-for-byte. Lowercase-only because the HTML parser
// lowercases attribute names; leading letter and [a-z0-9_-] thereafter because
// that is the intersection of "valid custom data attribute" and "unambiguous
// to a human reading the tag".
var attrSafeEdgePropKey = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// EdgePropKeyIsAttrSafe reports whether key can be written as a data-<key>
// attribute and read back unchanged. Keys that cannot take the JSON escape
// hatch instead. The writer half lives in core/workitem/htmlwriter.go
// (edgePropAttrs); this is the shared definition so the two halves cannot
// drift apart.
func EdgePropKeyIsAttrSafe(key string) bool {
	if !attrSafeEdgePropKey.MatchString(key) {
		return false
	}
	_, reserved := reservedEdgeAttrs["data-"+key]
	return !reserved
}

// parseEdgeProps reads an edge anchor's properties from both encodings.
// Returns nil (not an empty map) when the anchor carries none, so that an edge
// written without properties round-trips to a nil Properties map rather than a
// spurious empty one.
//
// Malformed JSON in the escape hatch is ignored rather than fatal: HTML is the
// canonical store and hand-edited files must never make a rebuild fail. The
// attribute-encoded properties on the same anchor still land.
func parseEdgeProps(link *goquery.Selection) map[string]string {
	node := link.Get(0)
	if node == nil {
		return nil
	}

	props := make(map[string]string)
	for _, attr := range node.Attr {
		if !strings.HasPrefix(attr.Key, "data-") {
			continue
		}
		if _, reserved := reservedEdgeAttrs[attr.Key]; reserved {
			continue
		}
		props[strings.TrimPrefix(attr.Key, "data-")] = attr.Val
	}

	// The escape hatch is authoritative for the keys it carries — it is the
	// lossless encoding, so it overlays the attribute harvest.
	if raw, ok := link.Attr(EdgePropsAttr); ok && raw != "" {
		var extra map[string]string
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
