package projection

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shakestzd/wipnote/core/graph"
)

type dslToken interface{ dslToken() }

type nodeSelector struct {
	nodeType string
	field    string
	value    string
}

type arrowToken struct{}
type relToken struct{ relType string }

func (nodeSelector) dslToken() {}
func (arrowToken) dslToken()   {}
func (relToken) dslToken()     {}

// ExecuteDSL evaluates the graph query DSL against the canonical projection.
func (s *Snapshot) ExecuteDSL(input string) ([]graph.NodeResult, error) {
	tokens, err := tokenize(input)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("dsl: empty query")
	}
	first, ok := tokens[0].(nodeSelector)
	if !ok {
		return nil, fmt.Errorf("dsl: query must start with a node type, got %q", tokens[0])
	}
	currentIDs, err := s.resolveTypeSelector(first)
	if err != nil {
		return nil, err
	}
	for _, token := range tokens[1:] {
		if len(currentIDs) == 0 {
			break
		}
		switch v := token.(type) {
		case arrowToken:
		case relToken:
			currentIDs = s.Follow(currentIDs, v.relType)
		case nodeSelector:
			currentIDs, err = s.filterBySelector(currentIDs, v)
			if err != nil {
				return nil, err
			}
		}
	}
	return orderedResults(s.ResolveToMap(currentIDs), currentIDs), nil
}

func tokenize(input string) ([]dslToken, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}
	parts := strings.Split(input, "->")
	var tokens []dslToken
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i > 0 {
			tokens = append(tokens, arrowToken{})
		}
		if strings.Contains(part, "[") {
			sel, err := parseNodeSelector(part)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, sel)
		} else if isNodeType(part) {
			tokens = append(tokens, nodeSelector{nodeType: part})
		} else {
			tokens = append(tokens, relToken{relType: part})
		}
	}
	return tokens, nil
}

func parseNodeSelector(s string) (nodeSelector, error) {
	bracketIdx := strings.Index(s, "[")
	if bracketIdx < 0 {
		return nodeSelector{nodeType: s}, nil
	}
	nodeType := strings.TrimSpace(s[:bracketIdx])
	rest := s[bracketIdx+1:]
	endBracket := strings.Index(rest, "]")
	if endBracket < 0 {
		return nodeSelector{}, fmt.Errorf("dsl: unclosed bracket in %q", s)
	}
	filter := rest[:endBracket]
	eqIdx := strings.Index(filter, "=")
	if eqIdx < 0 {
		return nodeSelector{}, fmt.Errorf("dsl: expected field=value in brackets, got %q", filter)
	}
	return nodeSelector{
		nodeType: nodeType,
		field:    strings.TrimSpace(filter[:eqIdx]),
		value:    strings.TrimSpace(filter[eqIdx+1:]),
	}, nil
}

func isNodeType(s string) bool {
	return normalizeNodeType(s) != ""
}

func normalizeNodeType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "features", "feature":
		return "feature"
	case "bugs", "bug":
		return "bug"
	case "spikes", "spike":
		return "spike"
	case "tracks", "track":
		return "track"
	case "plans", "plan":
		return "plan"
	case "specs", "spec":
		return "spec"
	case "sessions", "session":
		return "session"
	case "arch", "architecture", "architectures":
		return "arch"
	default:
		return ""
	}
}

func (s *Snapshot) resolveTypeSelector(sel nodeSelector) ([]string, error) {
	nodeType := normalizeNodeType(sel.nodeType)
	if nodeType == "" {
		return nil, fmt.Errorf("dsl: unsupported node type %q", sel.nodeType)
	}
	if err := validateField(nodeType, sel.field); err != nil {
		return nil, err
	}
	var ids []string
	for _, id := range s.NodeOrder {
		if n := s.Nodes[id]; n.Type == nodeType && nodeMatches(n, sel) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *Snapshot) filterBySelector(ids []string, sel nodeSelector) ([]string, error) {
	nodeType := normalizeNodeType(sel.nodeType)
	if nodeType == "" {
		return nil, fmt.Errorf("dsl: unsupported node type %q", sel.nodeType)
	}
	if err := validateField(nodeType, sel.field); err != nil {
		return nil, err
	}
	var out []string
	for _, id := range ids {
		if n := s.Nodes[id]; n.Type == nodeType && nodeMatches(n, sel) {
			out = append(out, id)
		}
	}
	return out, nil
}

func validateField(nodeType, field string) error {
	if field == "" {
		return nil
	}
	if _, ok := allowedFields(nodeType)[field]; ok {
		return nil
	}
	return fmt.Errorf("dsl: unsupported filter field %q for %s", field, nodeType)
}

func allowedFields(nodeType string) map[string]bool {
	fields := map[string]bool{"type": true, "status": true}
	switch nodeType {
	case "feature", "bug", "spike", "plan", "spec", "track":
		fields["priority"] = true
		fields["track_id"] = true
	case "session":
		fields["harness"] = true
	case "arch":
		fields["kind"] = true
		fields["created_by"] = true
	}
	return fields
}

func nodeMatches(n Node, sel nodeSelector) bool {
	switch sel.field {
	case "":
		return true
	case "type":
		return n.Type == sel.value
	case "status":
		return n.Status == sel.value
	case "priority":
		return n.Priority == sel.value
	case "track_id":
		return n.TrackID == sel.value
	case "harness":
		return n.Agent == sel.value
	case "kind":
		return n.Kind == sel.value
	case "created_by":
		return n.CreatedBy == sel.value
	default:
		return false
	}
}

func orderedResults(resolved map[string]graph.NodeResult, ids []string) []graph.NodeResult {
	var out []graph.NodeResult
	for _, id := range ids {
		out = append(out, resolved[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
