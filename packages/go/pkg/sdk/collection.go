package sdk

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shakestzd/htmlgraph/internal/graph"
	"github.com/shakestzd/htmlgraph/internal/htmlparse"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// FilterFunc is a predicate applied to nodes during queries.
type FilterFunc func(*models.Node) bool

// FilterOption configures listing/query behaviour.
type FilterOption func(*filterConfig)

type filterConfig struct {
	status   string
	priority string
	trackID  string
	agent    string
}

// WithStatus filters by node status.
func WithStatus(s string) FilterOption {
	return func(c *filterConfig) { c.status = s }
}

// WithPriority filters by node priority.
func WithPriority(p string) FilterOption {
	return func(c *filterConfig) { c.priority = p }
}

// WithTrackID filters by track ID.
func WithTrackID(id string) FilterOption {
	return func(c *filterConfig) { c.trackID = id }
}

// WithAgent filters by agent assignment.
func WithAgent(a string) FilterOption {
	return func(c *filterConfig) { c.agent = a }
}

// Collection is a generic, type-aware collection of work item nodes.
// It manages a single subdirectory of .htmlgraph/ (features, bugs, spikes,
// tracks, or sessions) and provides CRUD, filtering, and lifecycle methods.
type Collection struct {
	base           *Base
	collectionName string // e.g. "features"
	nodeType       string // e.g. "feature"
}

func newCollection(s *SDK, name, nodeType string) *Collection {
	return &Collection{base: s.Base, collectionName: name, nodeType: nodeType}
}

// Dir returns the absolute path to this collection's directory.
func (c *Collection) Dir() string {
	return filepath.Join(c.base.ProjectDir, c.collectionName)
}

// Get retrieves a single node by ID from the HTML file on disk.
func (c *Collection) Get(id string) (*models.Node, error) {
	path := filepath.Join(c.Dir(), id+".html")
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", c.collectionName, id, err)
	}
	return node, nil
}

// List returns all nodes in this collection, optionally filtered.
func (c *Collection) List(opts ...FilterOption) ([]*models.Node, error) {
	cfg := &filterConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	nodes, err := graph.LoadDir(c.Dir())
	if err != nil {
		// Directory might not exist yet — return empty list.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list %s: %w", c.collectionName, err)
	}

	var filtered []*models.Node
	for _, n := range nodes {
		if n.Type != c.nodeType {
			continue
		}
		if cfg.status != "" && string(n.Status) != cfg.status {
			continue
		}
		if cfg.priority != "" && string(n.Priority) != cfg.priority {
			continue
		}
		if cfg.trackID != "" && n.TrackID != cfg.trackID {
			continue
		}
		if cfg.agent != "" && n.AgentAssigned != cfg.agent {
			continue
		}
		filtered = append(filtered, n)
	}
	return filtered, nil
}

// Filter returns nodes matching a custom predicate.
func (c *Collection) Filter(fn FilterFunc) ([]*models.Node, error) {
	nodes, err := graph.LoadDir(c.Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("filter %s: %w", c.collectionName, err)
	}

	var out []*models.Node
	for _, n := range nodes {
		if n.Type != c.nodeType {
			continue
		}
		if fn(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// Delete removes a node's HTML file from disk.
func (c *Collection) Delete(id string) error {
	path := filepath.Join(c.Dir(), id+".html")
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// writeNode writes a node to disk and returns its path.
func (c *Collection) writeNode(node *models.Node) (string, error) {
	return WriteNodeHTML(c.Dir(), node)
}

// Start marks a node as in-progress.
func (c *Collection) Start(id string) (*models.Node, error) {
	node, err := c.Get(id)
	if err != nil {
		return nil, err
	}
	node.Status = models.StatusInProgress
	node.AgentAssigned = c.base.Agent
	node.UpdatedAt = time.Now().UTC()
	if _, err := c.writeNode(node); err != nil {
		return nil, err
	}
	return node, nil
}

// Complete marks a node as done and auto-completes all steps.
func (c *Collection) Complete(id string) (*models.Node, error) {
	node, err := c.Get(id)
	if err != nil {
		return nil, err
	}
	for i := range node.Steps {
		if !node.Steps[i].Completed {
			node.Steps[i].Completed = true
			node.Steps[i].Agent = c.base.Agent
			node.Steps[i].Timestamp = time.Now().UTC()
		}
	}
	node.Status = models.StatusDone
	node.UpdatedAt = time.Now().UTC()
	if _, err := c.writeNode(node); err != nil {
		return nil, err
	}
	return node, nil
}
