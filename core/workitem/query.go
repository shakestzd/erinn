package workitem

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// SortOrder specifies ascending or descending sort direction.
type SortOrder int

const (
	// Asc sorts in ascending order (oldest first, A-Z).
	Asc SortOrder = iota
	// Desc sorts in descending order (newest first, Z-A).
	Desc
)

// Query is a chainable query builder for wipnote nodes.
// Build a query with Find/FindAll, add Where/OrderBy/Limit, then Execute.
type Query struct {
	project    *Project
	collection string // empty means all collections
	predicates []Predicate
	orderField string
	orderDir   SortOrder
	limit      int
}

// Find begins a query scoped to a single collection (e.g. "features", "bugs").
func (p *Project) Find(collection string) *Query {
	return &Query{
		project:    p,
		collection: collection,
	}
}

// FindAll begins a query that spans all collections.
func (p *Project) FindAll() *Query {
	return &Query{
		project: p,
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
		return graph.LoadAll(q.project.ProjectDir)
	}

	dir := q.project.collectionDir(q.collection)
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
func (p *Project) collectionDir(name string) string {
	switch name {
	case "features":
		return p.FeaturesDir()
	case "bugs":
		return p.BugsDir()
	case "spikes":
		return p.SpikesDir()
	case "tracks":
		return p.TracksDir()
	case "plans":
		return p.PlansDir()
	case "specs":
		return p.SpecsDir()
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

// --- Predicates --------------------------------------------------------------

// Predicate is a composable filter function over graph nodes.
// Predicates can be combined with And, Or, and Not to build
// expressive queries without a custom DSL parser.
type Predicate func(*models.Node) bool

// StatusIs matches nodes whose status equals the given value.
func StatusIs(status string) Predicate {
	return func(n *models.Node) bool {
		return string(n.Status) == status
	}
}

// PriorityIs matches nodes whose priority equals the given value.
func PriorityIs(p string) Predicate {
	return func(n *models.Node) bool {
		return string(n.Priority) == p
	}
}

// TrackIs matches nodes linked to the given track ID.
func TrackIs(id string) Predicate {
	return func(n *models.Node) bool {
		return n.TrackID == id
	}
}

// AgentIs matches nodes assigned to the given agent.
func AgentIs(agent string) Predicate {
	return func(n *models.Node) bool {
		return n.AgentAssigned == agent
	}
}

// TypeIs matches nodes of the given type (e.g. "feature", "bug", "spike").
func TypeIs(t string) Predicate {
	return func(n *models.Node) bool {
		return n.Type == t
	}
}

// TitleContains matches nodes whose title contains sub (case-insensitive).
func TitleContains(sub string) Predicate {
	lower := strings.ToLower(sub)
	return func(n *models.Node) bool {
		return strings.Contains(strings.ToLower(n.Title), lower)
	}
}

// ContentContains matches nodes whose content contains sub (case-insensitive).
func ContentContains(sub string) Predicate {
	lower := strings.ToLower(sub)
	return func(n *models.Node) bool {
		return strings.Contains(strings.ToLower(n.Content), lower)
	}
}

// CreatedAfter matches nodes created strictly after the given time.
func CreatedAfter(t time.Time) Predicate {
	return func(n *models.Node) bool {
		return n.CreatedAt.After(t)
	}
}

// CreatedBefore matches nodes created strictly before the given time.
func CreatedBefore(t time.Time) Predicate {
	return func(n *models.Node) bool {
		return n.CreatedAt.Before(t)
	}
}

// UpdatedAfter matches nodes updated strictly after the given time.
func UpdatedAfter(t time.Time) Predicate {
	return func(n *models.Node) bool {
		return n.UpdatedAt.After(t)
	}
}

// HasSteps matches nodes that have at least one step defined.
func HasSteps() Predicate {
	return func(n *models.Node) bool {
		return len(n.Steps) > 0
	}
}

// CompletionAbove matches nodes whose completion percentage exceeds pct.
func CompletionAbove(pct int) Predicate {
	return func(n *models.Node) bool {
		return n.CompletionPercentage() > pct
	}
}

// CompletionBelow matches nodes whose completion percentage is below pct.
func CompletionBelow(pct int) Predicate {
	return func(n *models.Node) bool {
		return n.CompletionPercentage() < pct
	}
}

// And returns a predicate that is true when all given predicates are true.
func And(preds ...Predicate) Predicate {
	return func(n *models.Node) bool {
		for _, p := range preds {
			if !p(n) {
				return false
			}
		}
		return true
	}
}

// Or returns a predicate that is true when any given predicate is true.
func Or(preds ...Predicate) Predicate {
	return func(n *models.Node) bool {
		for _, p := range preds {
			if p(n) {
				return true
			}
		}
		return false
	}
}

// Not returns a predicate that negates the given predicate.
func Not(p Predicate) Predicate {
	return func(n *models.Node) bool {
		return !p(n)
	}
}

// --- Active work item lookup -------------------------------------------------

// WorkItem is a type-agnostic representation of an active work item.
// Returned by GetActiveWorkItem to avoid callers needing to know which
// collection the item belongs to.
type WorkItem struct {
	ID     string
	Type   string // "feature", "bug", or "spike"
	Title  string
	Status string
}

// GetActiveWorkItem scans features, bugs, and spikes for the first
// work item with status "in-progress" and returns it.
// Returns (nil, nil) if no active work item is found.
func GetActiveWorkItem(projectDir string) (*WorkItem, error) {
	dirs := []struct {
		path     string
		nodeType string
	}{
		{filepath.Join(projectDir, "features"), "feature"},
		{filepath.Join(projectDir, "bugs"), "bug"},
		{filepath.Join(projectDir, "spikes"), "spike"},
	}

	for _, d := range dirs {
		item, err := findActiveInDir(d.path, d.nodeType)
		if err != nil {
			return nil, err
		}
		if item != nil {
			return item, nil
		}
	}
	return nil, nil
}

// findActiveInDir scans a directory for the first in-progress node
// of the given type. Supports both flat format (id.html) and subdirectory
// format (id/index.html).
func findActiveInDir(dir, nodeType string) (*WorkItem, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		var path string
		if entry.IsDir() {
			// Try subdirectory format: id/index.html
			path = filepath.Join(dir, entry.Name(), "index.html")
			if _, err := os.Stat(path); err != nil {
				continue
			}
		} else if !strings.HasSuffix(entry.Name(), ".html") {
			// Skip non-HTML files
			continue
		} else {
			// Flat format: id.html
			path = filepath.Join(dir, entry.Name())
		}

		node, err := htmlparse.ParseFile(path)
		if err != nil {
			continue // skip unparseable files
		}
		if node.Status != models.StatusInProgress || node.Type != nodeType {
			continue
		}
		return &WorkItem{
			ID:     node.ID,
			Type:   node.Type,
			Title:  node.Title,
			Status: string(node.Status),
		}, nil
	}
	return nil, nil
}
