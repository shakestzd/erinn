package graph

import (
	"database/sql"
	"fmt"
	"strings"
)

// ExecuteDSL parses and executes a DSL query, returning matched nodes.
//
// Syntax:
//
//	type[field=value] -> rel_type -> type[field=value]
//
// Examples:
//
//	features -> contains -> features
//	features[status=todo] -> blocked_by -> features[status=done]
//	tracks -> contains -> features[status=todo]
//
// The first segment must be a type (optionally with filter). Subsequent
// segments alternate between relationship types and node types.
func ExecuteDSL(db *sql.DB, input string) ([]NodeResult, error) {
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

	currentIDs, err := resolveTypeSelector(db, first)
	if err != nil {
		return nil, err
	}

	for i := 1; i < len(tokens); i++ {
		if len(currentIDs) == 0 {
			break
		}
		switch v := tokens[i].(type) {
		case arrowToken:
			continue
		case relToken:
			currentIDs, err = followEdgesDSL(db, currentIDs, v.relType)
			if err != nil {
				return nil, err
			}
		case nodeSelector:
			currentIDs, err = filterBySelectorDSL(db, currentIDs, v)
			if err != nil {
				return nil, err
			}
		}
	}

	q := &QueryBuilder{db: db}
	return q.resolveNodes(currentIDs)
}

// Token types for the DSL parser.
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

// tokenize splits a DSL string into tokens.
func tokenize(input string) ([]dslToken, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	// Split on " -> " preserving the arrow as a separator.
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

		// Check if it's a node selector (has brackets or is a known type prefix).
		if strings.Contains(part, "[") {
			sel, err := parseNodeSelector(part)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, sel)
		} else if isNodeType(part) {
			tokens = append(tokens, nodeSelector{nodeType: part})
		} else {
			// It's a relationship type.
			tokens = append(tokens, relToken{relType: part})
		}
	}
	return tokens, nil
}

// parseNodeSelector parses "type[field=value]" syntax.
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

// isNodeType checks if a string is a known node type plural.
var knownNodeTypes = map[string]string{
	"features": "feature",
	"feature":  "feature",
	"bugs":     "bug",
	"bug":      "bug",
	"spikes":   "spike",
	"spike":    "spike",
	"tracks":   "track",
	"track":    "track",
	"plans":    "plan",
	"plan":     "plan",
	"specs":    "spec",
	"spec":     "spec",
	"commits":  "commit",
	"commit":   "commit",
	"files":    "file",
	"file":     "file",
	"sessions": "session",
	"session":  "session",
	"agents":   "agent",
	"agent":    "agent",
}

func isNodeType(s string) bool {
	_, ok := knownNodeTypes[strings.ToLower(s)]
	return ok
}

// IsNodeType is the exported version of isNodeType for use in tests.
func IsNodeType(s string) bool { return isNodeType(s) }

func normalizeNodeType(s string) string {
	if t, ok := knownNodeTypes[strings.ToLower(s)]; ok {
		return t
	}
	return strings.ToLower(s)
}

// NormalizeNodeType is the exported version of normalizeNodeType for use in tests.
func NormalizeNodeType(s string) string { return normalizeNodeType(s) }

// resolveTypeSelector queries for all node IDs matching a type+filter selector.
func resolveTypeSelector(db *sql.DB, sel nodeSelector) ([]string, error) {
	nodeType := normalizeNodeType(sel.nodeType)

	var query string
	var args []any

	switch nodeType {
	case "commit":
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT DISTINCT commit_hash FROM git_commits WHERE %s = ?`, col)
			args = append(args, sel.value)
		} else {
			query = `SELECT DISTINCT commit_hash FROM git_commits`
		}
	case "file":
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT DISTINCT file_path FROM feature_files WHERE %s = ?`, col)
			args = append(args, sel.value)
		} else {
			query = `SELECT DISTINCT file_path FROM feature_files`
		}
	case "session":
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT session_id FROM sessions WHERE %s = ?`, col)
			args = append(args, sel.value)
		} else {
			query = `SELECT session_id FROM sessions`
		}
	case "agent":
		// Agent nodes are the distinct agent_name across agent_lineage_trace
		// and sessions.agent_assigned. Filter support is limited to the
		// identity field (agent name itself) via the whitelist.
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`
				SELECT DISTINCT name FROM (
					SELECT agent_name AS name FROM agent_lineage_trace WHERE agent_name != ''
					UNION
					SELECT agent_assigned AS name FROM sessions WHERE agent_assigned != ''
				) WHERE %s = ?`, col)
			args = append(args, sel.value)
		} else {
			query = `
				SELECT DISTINCT name FROM (
					SELECT agent_name AS name FROM agent_lineage_trace WHERE agent_name != ''
					UNION
					SELECT agent_assigned AS name FROM sessions WHERE agent_assigned != ''
				)`
		}
	case "track":
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT id FROM tracks WHERE %s = ?`, col)
			args = append(args, sel.value)
		} else {
			query = `SELECT id FROM tracks`
		}
	default:
		// features, bugs, spikes, plans, specs — all stored in features table
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT id FROM features WHERE type = ? AND %s = ?`, col)
			args = append(args, nodeType, sel.value)
		} else {
			query = `SELECT id FROM features WHERE type = ?`
			args = append(args, nodeType)
		}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("dsl resolve type: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// followEdgesDSL is the DSL version of edge traversal.
func followEdgesDSL(db *sql.DB, sourceIDs []string, relType string) ([]string, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(sourceIDs))
	args := make([]any, len(sourceIDs)+1)
	for i, id := range sourceIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	args[len(sourceIDs)] = relType

	query := fmt.Sprintf(`
		SELECT DISTINCT to_node_id FROM graph_edges
		WHERE from_node_id IN (%s) AND relationship_type = ?`,
		strings.Join(placeholders, ","))

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("dsl follow: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// filterBySelectorDSL filters IDs by a node selector (type + optional field).
func filterBySelectorDSL(db *sql.DB, ids []string, sel nodeSelector) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	nodeType := normalizeNodeType(sel.nodeType)

	placeholders := make([]string, len(ids))
	baseArgs := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		baseArgs[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	var query string
	args := make([]any, len(baseArgs), len(baseArgs)+2)
	copy(args, baseArgs)

	switch nodeType {
	case "commit":
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT DISTINCT commit_hash FROM git_commits WHERE commit_hash IN (%s) AND %s = ?`, inClause, col)
			args = append(args, sel.value)
		} else {
			query = fmt.Sprintf(`SELECT DISTINCT commit_hash FROM git_commits WHERE commit_hash IN (%s)`, inClause)
		}
	case "file":
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT DISTINCT file_path FROM feature_files WHERE file_path IN (%s) AND %s = ?`, inClause, col)
			args = append(args, sel.value)
		} else {
			query = fmt.Sprintf(`SELECT DISTINCT file_path FROM feature_files WHERE file_path IN (%s)`, inClause)
		}
	case "session":
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT session_id FROM sessions WHERE session_id IN (%s) AND %s = ?`, inClause, col)
			args = append(args, sel.value)
		} else {
			query = fmt.Sprintf(`SELECT session_id FROM sessions WHERE session_id IN (%s)`, inClause)
		}
	case "agent":
		// Filter the candidate IDs down to names that actually appear as
		// agents in either source table. No schema-backed field filters beyond
		// identity are meaningful here, but we still honor the whitelist.
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`
				SELECT DISTINCT name FROM (
					SELECT agent_name AS name FROM agent_lineage_trace WHERE agent_name != ''
					UNION
					SELECT agent_assigned AS name FROM sessions WHERE agent_assigned != ''
				) WHERE name IN (%s) AND %s = ?`, inClause, col)
			args = append(args, sel.value)
		} else {
			query = fmt.Sprintf(`
				SELECT DISTINCT name FROM (
					SELECT agent_name AS name FROM agent_lineage_trace WHERE agent_name != ''
					UNION
					SELECT agent_assigned AS name FROM sessions WHERE agent_assigned != ''
				) WHERE name IN (%s)`, inClause)
		}
	case "track":
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT id FROM tracks WHERE id IN (%s) AND %s = ?`, inClause, col)
			args = append(args, sel.value)
		} else {
			query = fmt.Sprintf(`SELECT id FROM tracks WHERE id IN (%s)`, inClause)
		}
	default:
		// features, bugs, spikes, plans, specs
		if sel.field != "" {
			col, ok := allowedColumnFor(nodeType, sel.field)
			if !ok {
				return nil, fmt.Errorf("dsl: unsupported filter field %q for %s", sel.field, nodeType)
			}
			query = fmt.Sprintf(`SELECT id FROM features WHERE id IN (%s) AND type = ? AND %s = ?`, inClause, col)
			args = append(args, nodeType, sel.value)
		} else {
			query = fmt.Sprintf(`SELECT id FROM features WHERE id IN (%s) AND type = ?`, inClause)
			args = append(args, nodeType)
		}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("dsl filter: %w", err)
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
