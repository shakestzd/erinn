package sdk

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/graph"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// SortOrder specifies ascending or descending sort direction.
type SortOrder int

const (
	// Asc sorts in ascending order (oldest first, A-Z).
	Asc SortOrder = iota
	// Desc sorts in descending order (newest first, Z-A).
	Desc
)

// Query is a chainable query builder for HtmlGraph nodes.
// Build a query with Find/FindAll, add Where/OrderBy/Limit, then Execute.
type Query struct {
	sdk        *SDK
	collection string // empty means all collections
	predicates []Predicate
	orderField string
	orderDir   SortOrder
	limit      int
}

// Find begins a query scoped to a single collection (e.g. "features", "bugs").
func (s *SDK) Find(collection string) *Query {
	return &Query{
		sdk:        s,
		collection: collection,
	}
}

// FindAll begins a query that spans all collections.
func (s *SDK) FindAll() *Query {
	return &Query{
		sdk: s,
	}
}

// Where adds a predicate filter to the query. Multiple Where calls
// are combined with AND semantics.
func (q *Query) Where(p Predicate) *Query {
	q.predicates = append(q.predicates, p)
	return q
}

// OrderBy sets the sort field and direction. Supported fields:
// "created", "updated", "title", "status", "priority", "id".
func (q *Query) OrderBy(field string, order SortOrder) *Query {
	q.orderField = field
	q.orderDir = order
	return q
}

// Limit caps the number of results returned.
func (q *Query) Limit(n int) *Query {
	q.limit = n
	return q
}

// Execute runs the query and returns matching nodes.
func (q *Query) Execute() ([]*models.Node, error) {
	nodes, err := q.loadNodes()
	if err != nil {
		return nil, err
	}

	nodes = q.applyPredicates(nodes)
	q.applySort(nodes)

	if q.limit > 0 && len(nodes) > q.limit {
		nodes = nodes[:q.limit]
	}

	return nodes, nil
}

// First returns the first matching node, or an error if none found.
func (q *Query) First() (*models.Node, error) {
	q.limit = 1
	nodes, err := q.Execute()
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no matching nodes found")
	}
	return nodes[0], nil
}

// Count returns the number of matching nodes without allocating
// the full result slice beyond filtering.
func (q *Query) Count() (int, error) {
	nodes, err := q.loadNodes()
	if err != nil {
		return 0, err
	}
	return len(q.applyPredicates(nodes)), nil
}

// loadNodes loads raw nodes from the appropriate collection(s).
func (q *Query) loadNodes() ([]*models.Node, error) {
	if q.collection == "" {
		return graph.LoadAll(q.sdk.ProjectDir)
	}

	// Map collection name to directory.
	dir := q.sdk.collectionDir(q.collection)
	if dir == "" {
		return nil, fmt.Errorf("unknown collection %q", q.collection)
	}

	nodes, err := graph.LoadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", q.collection, err)
	}
	return nodes, nil
}

// applyPredicates filters nodes through all registered predicates.
func (q *Query) applyPredicates(nodes []*models.Node) []*models.Node {
	if len(q.predicates) == 0 {
		return nodes
	}
	var out []*models.Node
	for _, n := range nodes {
		match := true
		for _, p := range q.predicates {
			if !p(n) {
				match = false
				break
			}
		}
		if match {
			out = append(out, n)
		}
	}
	return out
}

// applySort sorts nodes in place according to orderField and orderDir.
func (q *Query) applySort(nodes []*models.Node) {
	if q.orderField == "" {
		return
	}

	sort.SliceStable(nodes, func(i, j int) bool {
		cmp := q.compareNodes(nodes[i], nodes[j])
		if q.orderDir == Desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

// compareNodes compares two nodes by the configured sort field.
// Returns negative if a < b, positive if a > b, zero if equal.
func (q *Query) compareNodes(a, b *models.Node) int {
	switch strings.ToLower(q.orderField) {
	case "created", "created_at":
		return compareTime(a.CreatedAt, b.CreatedAt)
	case "updated", "updated_at":
		return compareTime(a.UpdatedAt, b.UpdatedAt)
	case "title":
		return strings.Compare(
			strings.ToLower(a.Title),
			strings.ToLower(b.Title),
		)
	case "status":
		return strings.Compare(string(a.Status), string(b.Status))
	case "priority":
		return comparePriority(a.Priority, b.Priority)
	case "id":
		return strings.Compare(a.ID, b.ID)
	default:
		return 0
	}
}

// collectionDir maps a collection name to its directory path.
func (s *SDK) collectionDir(name string) string {
	switch name {
	case "features":
		return s.FeaturesDir()
	case "bugs":
		return s.BugsDir()
	case "spikes":
		return s.SpikesDir()
	case "tracks":
		return s.TracksDir()
	default:
		return ""
	}
}

// compareTime compares two times, returning -1, 0, or 1.
func compareTime(a, b time.Time) int {
	if a.Before(b) {
		return -1
	}
	if a.After(b) {
		return 1
	}
	return 0
}

// priorityRank maps priority to a sortable int (higher = more urgent).
var priorityRank = map[models.Priority]int{
	models.PriorityLow:      0,
	models.PriorityMedium:   1,
	models.PriorityHigh:     2,
	models.PriorityCritical: 3,
}

// comparePriority compares two priorities by rank.
func comparePriority(a, b models.Priority) int {
	ra, rb := priorityRank[a], priorityRank[b]
	if ra < rb {
		return -1
	}
	if ra > rb {
		return 1
	}
	return 0
}
