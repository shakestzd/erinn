package workitem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/daemon/apply"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"

	dbpkg "github.com/shakestzd/wipnote/core/db"
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
// single work item. SQLite remains a derived read index; the HTML file is read
// and written while holding the per-item sidecar lock.
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

// Start marks a node as in-progress and dual-writes status to SQLite.
func (c *Collection) Start(id string) (*models.Node, error) {
	node, err := c.mutateNode(id, func(node *models.Node) error {
		node.Status = models.StatusInProgress
		node.AgentAssigned = c.base.Agent
		node.UpdatedAt = time.Now().UTC()
		return nil
	}, func(*models.Node) {
		c.routeFeatureStatus(id, "in-progress")
	})
	if err != nil {
		return nil, err
	}
	return node, nil
}

// Complete marks a node as done and auto-completes all steps.
// Also releases any active SQLite claim with completed status.
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
		return nil
	}, func(*models.Node) {
		// bug-74a7bda7: the completion status transition is THE
		// user-visible contended write. feat-075c110d MVP-4 routes it
		// through the per-project writer daemon first (bounded, fallback-
		// safe); on any daemon miss it falls back to the direct write with
		// bounded SQLITE_BUSY backoff so a transient lock resolves
		// transparently instead of silently dropping the derived-index
		// completion (canonical HTML is still authoritative either way).
		c.routeFeatureStatus(id, "done")
		if c.base.DB != nil {
			if activeClaim, err := dbpkg.GetActiveClaim(c.base.DB, id); err == nil && activeClaim != nil {
				completeWithBusyRetry(func() error {
					return dbpkg.ReleaseClaim(c.base.DB, activeClaim.ClaimID, activeClaim.OwnerSessionID, models.ClaimCompleted)
				})
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return node, nil
}

// completeWithBusyRetry runs a derived-index write for the work-item
// status-transition path, retrying on SQLITE_BUSY with bounded exponential
// backoff (bug-74a7bda7). It records any terminal BUSY under the
// cli_mutation subsystem so the launch stress gate stays observable, then
// swallows the error: the canonical HTML on disk remains the source of
// truth and `wipnote reindex` recovers any row the derived index missed.
// This is the universal-resilience layer — it helps WAL hosts too, it just
// almost never has to retry there.
func completeWithBusyRetry(fn func() error) {
	err := dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, fn)
	dbpkg.Record(dbpkg.SubsystemCLIMutation, err)
}

// routeFeatureStatus persists a work-item status transition to the derived
// SQLite index. feat-075c110d MVP-4: it FIRST attempts the per-project writer
// daemon (apply.RouteFeatureStatus — bounded ~2s, auto-spawn) so concurrent
// start/complete + dashboard reads no longer contend the writer lock. On ANY
// daemon miss (unavailable / forbidden / timeout / error-ack) it falls back to
// the existing direct write via completeWithBusyRetry — exactly today's
// behaviour. The canonical HTML written upstream is authoritative either way,
// so a missed derived write is recovered by `wipnote reindex`.
//
// SAFETY: this is on the feature/bug/spike start+complete path used by core
// work tracking. It must never hang and never surface a daemon error to the
// caller — both the daemon attempt and the fallback are strictly bounded.
func (c *Collection) routeFeatureStatus(id, status string) {
	// Reads stay direct; only the derived write is delegated. base.ProjectDir
	// is the .wipnote/ dir, so the project root is its parent.
	projectRoot := filepath.Dir(c.base.ProjectDir)
	if apply.RouteFeatureStatus(projectRoot, id, status) {
		return // applied (or deduped) by the writer daemon
	}
	if c.base.DB == nil {
		return // no daemon and no direct handle — canonical HTML is authoritative
	}
	completeWithBusyRetry(func() error {
		return dbpkg.UpdateFeatureStatus(c.base.DB, id, status)
	})
}

// --- Edge operations ---------------------------------------------------------

// AddEdge reads a node, appends an edge, and writes it back to disk.
// It also dual-writes to graph_edges in SQLite when a DB connection is available.
// HTML is canonical; SQLite errors are non-fatal.
func (c *Collection) AddEdge(id string, e models.Edge) (*models.Node, error) {
	node, err := c.mutateNode(id, func(node *models.Node) error {
		node.AddEdge(e)
		return nil
	}, func(*models.Node) {
		// Dual-write to SQLite read index.
		if c.base.DB != nil {
			edgeID := fmt.Sprintf("%s-%s-%s", id, string(e.Relationship), e.TargetID)
			_ = dbpkg.InsertEdge(
				c.base.DB,
				edgeID, id, c.nodeType,
				e.TargetID, inferNodeType(e.TargetID),
				string(e.Relationship),
				e.Properties,
			)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("add edge %s: %w", id, err)
	}

	return node, nil
}

// RemoveEdge reads a node, removes the matching edge, and writes it back.
// Returns the updated node and whether an edge was actually removed.
// It also removes the corresponding row from graph_edges in SQLite.
func (c *Collection) RemoveEdge(id, targetID string, relType models.RelationshipType) (*models.Node, bool, error) {
	removed := false
	node, err := c.mutateNode(id, func(node *models.Node) error {
		removed = node.RemoveEdge(targetID, relType)
		return nil
	}, func(*models.Node) {
		// Dual-write: remove from SQLite read index.
		if removed && c.base.DB != nil {
			_ = dbpkg.DeleteEdge(c.base.DB, id, targetID, string(relType))
		}
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
// and claim metadata. Also releases the SQLite claim if DB is available.
func (c *Collection) Release(id string) error {
	_, err := c.mutateNode(id, func(node *models.Node) error {
		node.AgentAssigned = ""
		node.ClaimedAt = ""
		node.ClaimedBySession = ""
		node.UpdatedAt = time.Now().UTC()
		return nil
	}, func(*models.Node) {
		// Release SQLite claim if DB is available.
		if c.base.DB != nil {
			if activeClaim, err := dbpkg.GetActiveClaim(c.base.DB, id); err == nil && activeClaim != nil {
				_ = dbpkg.ReleaseClaim(c.base.DB, activeClaim.ClaimID, activeClaim.OwnerSessionID, models.ClaimAbandoned)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("release %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// AtomicClaim claims a work item only if it is not already claimed
// by another agent. Returns an error if already claimed.
// When a DB connection is available, claiming is atomic at the SQLite level
// (no race condition). Falls back to HTML-only check when DB is nil.
func (c *Collection) AtomicClaim(id, sessionID string) error {
	release := LockFeatureForWrite(c.nodePath(id))
	defer release()

	node, err := c.Get(id)
	if err != nil {
		return fmt.Errorf("atomic claim %s/%s: %w", c.collectionName, id, err)
	}
	// SQLite-first: use atomic claim if DB is available.
	if c.base.DB != nil {
		claim := &models.Claim{
			ClaimID:          "clm-" + uuid.NewString()[:8],
			WorkItemID:       id,
			OwnerSessionID:   sessionID,
			OwnerAgent:       c.base.Agent,
			ClaimedByAgentID: c.base.AgentID,
			Status:           models.ClaimInProgress,
		}
		if err := dbpkg.ClaimItem(c.base.DB, claim, 30*time.Minute); err != nil {
			return fmt.Errorf("atomic claim %s/%s: %w", c.collectionName, id, err)
		}
	} else {
		// Fallback: HTML-only check (legacy path, race-prone).
		if node.ClaimedBySession != "" && node.ClaimedBySession != sessionID {
			return fmt.Errorf("atomic claim %s/%s: already claimed by session %s",
				c.collectionName, id, node.ClaimedBySession)
		}
		if node.AgentAssigned != "" && node.AgentAssigned != c.base.Agent {
			return fmt.Errorf("atomic claim %s/%s: already claimed by agent %s",
				c.collectionName, id, node.AgentAssigned)
		}
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
// Also updates SQLite step counters (best-effort, HTML is canonical).
func (c *Collection) AddTaskStep(id, taskID, subject string) error {
	var total, completed int
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
		total, completed = countSteps(node.Steps)
		return nil
	}, func(*models.Node) {
		if c.base.DB != nil {
			_ = dbpkg.UpdateFeatureSteps(c.base.DB, id, total, completed)
		}
	})
	if err != nil {
		return fmt.Errorf("add task step %s: %w", id, err)
	}
	return nil
}

// CompleteTaskStep marks the step matching taskID as completed on the node.
// No-op if the step is already complete or not found.
// Also updates SQLite step counters (best-effort, HTML is canonical).
func (c *Collection) CompleteTaskStep(id, taskID string) error {
	modified := false
	var total, completed int
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
		total, completed = countSteps(node.Steps)
		return nil
	}, func(*models.Node) {
		if modified && c.base.DB != nil {
			_ = dbpkg.UpdateFeatureSteps(c.base.DB, id, total, completed)
		}
	})
	if err != nil {
		return fmt.Errorf("complete task step %s: %w", id, err)
	}
	return nil
}

// countSteps returns the total and completed step counts for a step slice.
func countSteps(steps []models.Step) (total, completed int) {
	total = len(steps)
	for _, s := range steps {
		if s.Completed {
			completed++
		}
	}
	return total, completed
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
