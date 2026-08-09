package workitem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
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
// It manages a single subdirectory of .wipnote/ (features, bugs, spikes,
// tracks, or sessions) and provides CRUD, filtering, and lifecycle methods.
type Collection struct {
	base           *Base
	collectionName string // e.g. "features"
	nodeType       string // e.g. "feature"
}

func newCollection(base *Base, name, nodeType string) *Collection {
	return &Collection{base: base, collectionName: name, nodeType: nodeType}
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

func (c *Collection) nodePath(id string) string {
	return filepath.Join(c.Dir(), id+".html")
}

func (c *Collection) writeNodeUnlocked(node *models.Node) (string, error) {
	return writeNodeHTMLUnlocked(c.Dir(), node)
}

// mutateNode serialises the full canonical read-modify-write window for a
// single work item. The HTML file is read and written while holding the
// per-item sidecar lock.
func (c *Collection) mutateNode(id string, mutate func(*models.Node) error, afterWrite ...func(*models.Node)) (*models.Node, error) {
	release := LockFeatureForWrite(c.nodePath(id))
	defer release()

	node, err := c.Get(id)
	if err != nil {
		return nil, err
	}
	if err := mutate(node); err != nil {
		return nil, err
	}
	if _, err := c.writeNodeUnlocked(node); err != nil {
		return nil, err
	}
	for _, fn := range afterWrite {
		fn(node)
	}
	return node, nil
}

// Start marks a node as in-progress in the canonical HTML artifact.
func (c *Collection) Start(id string) (*models.Node, error) {
	node, err := c.mutateNode(id, func(node *models.Node) error {
		node.Status = models.StatusInProgress
		node.AgentAssigned = c.base.Agent
		node.UpdatedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return node, nil
}

// Complete marks a node as done and auto-completes all steps.
func (c *Collection) Complete(id string) (*models.Node, error) {
	node, err := c.mutateNode(id, func(node *models.Node) error {
		for i := range node.Steps {
			if !node.Steps[i].Completed {
				node.Steps[i].Completed = true
				node.Steps[i].Agent = c.base.Agent
				node.Steps[i].Timestamp = time.Now().UTC()
			}
		}
		node.Status = models.StatusDone
		node.UpdatedAt = time.Now().UTC()
		// feat-7ee73444: collapse the item's telemetry and git history into
		// rollup properties inside this same locked read-modify-write, so the
		// numbers land in the canonical HTML with the status transition rather
		// than through a second write. ApplyRollup never fails — a rollup it
		// cannot compute degrades to a marked absence, never a blocked
		// completion.
		ApplyRollup(node, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return node, nil
}

// --- Edge operations ---------------------------------------------------------

// AddEdge reads a node, appends an edge, and writes it back to disk.
func (c *Collection) AddEdge(id string, e models.Edge) (*models.Node, error) {
	node, err := c.mutateNode(id, func(node *models.Node) error {
		node.AddEdge(e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("add edge %s: %w", id, err)
	}

	return node, nil
}

// RemoveEdge reads a node, removes the matching edge, and writes it back.
// Returns the updated node and whether an edge was actually removed.
func (c *Collection) RemoveEdge(id, targetID string, relType models.RelationshipType) (*models.Node, bool, error) {
	removed := false
	node, err := c.mutateNode(id, func(node *models.Node) error {
		removed = node.RemoveEdge(targetID, relType)
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("remove edge %s: %w", id, err)
	}
	if !removed {
		return node, false, nil
	}

	return node, true, nil
}

// inferNodeType derives the node type string from an ID prefix.
// feat-* → "feature", bug-* → "bug", spk-* → "spike",
// trk-* → "track", plan-* → "plan", spec-* → "spec".
// Falls back to "unknown" for unrecognised prefixes.
func inferNodeType(id string) string {
	switch {
	case strings.HasPrefix(id, "feat-"):
		return "feature"
	case strings.HasPrefix(id, "bug-"):
		return "bug"
	case strings.HasPrefix(id, "spk-"):
		return "spike"
	case strings.HasPrefix(id, "trk-"):
		return "track"
	case strings.HasPrefix(id, "plan-"):
		return "plan"
	case strings.HasPrefix(id, "spec-"):
		return "spec"
	default:
		return "unknown"
	}
}

// --- Claim / release operations ----------------------------------------------

// Claim marks a work item as claimed by the current agent.
// It sets AgentAssigned, ClaimedAt, and ClaimedBySession.
func (c *Collection) Claim(id, sessionID string) error {
	_, err := c.mutateNode(id, func(node *models.Node) error {
		now := time.Now().UTC()
		node.AgentAssigned = c.base.Agent
		node.ClaimedAt = fmtTime(now)
		node.ClaimedBySession = sessionID
		node.UpdatedAt = now
		return nil
	})
	if err != nil {
		return fmt.Errorf("claim %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// Release clears the claim on a work item, removing agent assignment
// and claim metadata.
func (c *Collection) Release(id string) error {
	_, err := c.mutateNode(id, func(node *models.Node) error {
		node.AgentAssigned = ""
		node.ClaimedAt = ""
		node.ClaimedBySession = ""
		node.UpdatedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		return fmt.Errorf("release %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// AtomicClaim claims a work item only if it is not already claimed
// by another agent. Returns an error if already claimed.
func (c *Collection) AtomicClaim(id, sessionID string) error {
	release := LockFeatureForWrite(c.nodePath(id))
	defer release()

	node, err := c.Get(id)
	if err != nil {
		return fmt.Errorf("atomic claim %s/%s: %w", c.collectionName, id, err)
	}
	if node.ClaimedBySession != "" {
		return fmt.Errorf("atomic claim %s/%s: already claimed by session %s",
			c.collectionName, id, node.ClaimedBySession)
	}
	if node.AgentAssigned != "" && node.AgentAssigned != c.base.Agent {
		return fmt.Errorf("atomic claim %s/%s: already claimed by agent %s",
			c.collectionName, id, node.AgentAssigned)
	}

	// Update HTML metadata (non-authoritative, for display).
	now := time.Now().UTC()
	node.AgentAssigned = c.base.Agent
	node.ClaimedAt = fmtTime(now)
	node.ClaimedBySession = sessionID
	node.UpdatedAt = now

	if _, err := c.writeNodeUnlocked(node); err != nil {
		return fmt.Errorf("atomic claim %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// AddTaskStep appends a step to the node identified by id, using taskID as the
// step ID so CompleteTaskStep can find and mark it done later.
func (c *Collection) AddTaskStep(id, taskID, subject string) error {
	_, err := c.mutateNode(id, func(node *models.Node) error {
		stepDesc := subject
		if stepDesc == "" {
			stepDesc = "Task " + taskID
		}

		node.Steps = append(node.Steps, models.Step{
			StepID:      "task-" + taskID,
			Description: stepDesc,
			Completed:   false,
			Agent:       c.base.Agent,
			Timestamp:   time.Now().UTC(),
		})
		node.UpdatedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		return fmt.Errorf("add task step %s: %w", id, err)
	}
	return nil
}

// CompleteTaskStep marks the step matching taskID as completed on the node.
// No-op if the step is already complete or not found.
func (c *Collection) CompleteTaskStep(id, taskID string) error {
	modified := false
	_, err := c.mutateNode(id, func(node *models.Node) error {
		stepID := "task-" + taskID
		for i := range node.Steps {
			if node.Steps[i].StepID == stepID && !node.Steps[i].Completed {
				node.Steps[i].Completed = true
				node.Steps[i].Agent = c.base.Agent
				node.Steps[i].Timestamp = time.Now().UTC()
				modified = true
				break
			}
		}
		if modified {
			node.UpdatedAt = time.Now().UTC()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("complete task step %s: %w", id, err)
	}
	return nil
}

// CompleteStep marks a manual step as completed by 1-based index, or completes the
// next incomplete step if stepNum is 0. No-op if the step is already complete.
// Returns a clean error if stepNum is out of range.
func (c *Collection) CompleteStep(id string, stepNum int) error {
	_, err := c.mutateNode(id, func(node *models.Node) error {
		if len(node.Steps) == 0 {
			return fmt.Errorf("no steps defined")
		}

		// If stepNum is 0, find the next incomplete step
		targetIdx := stepNum
		if stepNum == 0 {
			targetIdx = -1
			for i := range node.Steps {
				if !node.Steps[i].Completed {
					targetIdx = i + 1 // Convert to 1-based
					break
				}
			}
			if targetIdx == -1 {
				return fmt.Errorf("all steps already complete")
			}
		}

		// Validate step index (1-based)
		if targetIdx < 1 || targetIdx > len(node.Steps) {
			return fmt.Errorf("step %d out of range (1-%d)", targetIdx, len(node.Steps))
		}

		// Convert to 0-based index
		idx := targetIdx - 1
		if !node.Steps[idx].Completed {
			node.Steps[idx].Completed = true
			node.Steps[idx].Agent = c.base.Agent
			node.Steps[idx].Timestamp = time.Now().UTC()
			node.UpdatedAt = time.Now().UTC()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("complete step %s: %w", id, err)
	}
	return nil
}

// Unclaim removes the claim metadata without changing the node's status.
// Unlike Release, Unclaim only clears ClaimedAt and ClaimedBySession
// but preserves AgentAssigned.
func (c *Collection) Unclaim(id string) error {
	_, err := c.mutateNode(id, func(node *models.Node) error {
		node.ClaimedAt = ""
		node.ClaimedBySession = ""
		node.UpdatedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		return fmt.Errorf("unclaim %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}
