package models

import (
	"encoding/json"
	"errors"
	"time"
)

// Step represents an implementation step within a node (task checklist item).
type Step struct {
	StepID      string    `json:"step_id,omitempty"`
	Description string    `json:"description"`
	Completed   bool      `json:"completed"`
	Agent       string    `json:"agent,omitempty"`
	Timestamp   time.Time `json:"timestamp,omitempty"`
	DependsOn   []string  `json:"depends_on,omitempty"`

	// Provenance — captured at step creation/completion so we can tell which
	// model + role + CLI version produced each step. Agent (above) doubles as
	// the harness identity (claude-code, codex, gemini) and is rendered as
	// the data-created-by-agent attribute in HTML.
	CreatedByModel      string `json:"created_by_model,omitempty"`
	CreatedByRole       string `json:"created_by_role,omitempty"`
	CreatedByCLIVersion string `json:"created_by_cli_version,omitempty"`
}

// Edge represents a graph edge (relationship between nodes).
type Edge struct {
	TargetID     string            `json:"target_id"`
	Relationship RelationshipType  `json:"relationship"`
	Title        string            `json:"title,omitempty"`
	Since        time.Time         `json:"since,omitempty"`
	Properties   map[string]string `json:"properties,omitempty"`
}

// Node represents a graph node — an HTML file tracking a work item.
type Node struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Type     string     `json:"type"`
	Status   NodeStatus `json:"status"`
	Priority Priority   `json:"priority"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Properties map[string]any    `json:"properties,omitempty"`
	Edges      map[string][]Edge `json:"edges,omitempty"`
	Steps      []Step            `json:"steps,omitempty"`
	Content    string            `json:"content,omitempty"`

	AgentAssigned    string `json:"agent_assigned,omitempty"`
	ClaimedAt        string `json:"claimed_at,omitempty"`
	ClaimedBySession string `json:"claimed_by_session,omitempty"`

	// Vertical integration
	TrackID string `json:"track_id,omitempty"`
	// PlanTaskID mirrors TrackID's round-trip (rendered as data-plan-task-id
	// on <article>, parsed back by name). No CLI path sets it yet, but it
	// carries the same shape as TrackID and had already grown a parser half
	// with no writer half — the exact asymmetry bug-c65a5f4e fixed for
	// Properties — so it was completed here (bug-e5c04997) rather than left
	// as a dangling read of an attribute nothing ever emits.
	PlanTaskID string `json:"plan_task_id,omitempty"`

	// SpecRequirements has no writer or reader anywhere in the codebase
	// (audited bug-e5c04997). The live spec-generation path
	// (cmd/wipnote/spec.go) computes its own requirements list from the
	// plan slice's DoneWhen/Tests at generation time and stores it on the
	// spec document, not on the feature Node — so this field currently
	// duplicates nothing and nothing depends on it. Not wired to HTML;
	// wiring persistence for a field nothing sets would be speculative.
	// Revisit only alongside an actual design for snapshotting requirements
	// onto the Node itself.
	SpecRequirements []string `json:"spec_requirements,omitempty"`

	// Handoff context has no writer or reader anywhere in the codebase
	// (audited bug-e5c04997). The handoff mechanism that IS implemented and
	// used end-to-end lives on models.Session (core/db/session_repo.go,
	// core/hooks/session_end.go, cmd/wipnote/launcher_continue.go) — these
	// Node-level fields appear to be an earlier, superseded design. Not
	// wired to HTML; resurrecting a second, unused handoff surface would
	// duplicate a mechanism that already works at the session level.
	HandoffRequired  bool   `json:"handoff_required,omitempty"`
	PreviousAgent    string `json:"previous_agent,omitempty"`
	HandoffReason    string `json:"handoff_reason,omitempty"`
	HandoffNotes     string `json:"handoff_notes,omitempty"`
	HandoffTimestamp string `json:"handoff_timestamp,omitempty"`

	// Capability-based routing fields have no writer or reader anywhere in
	// the codebase (audited bug-e5c04997) — no CLI flag sets them and no
	// routing logic consults them. Not wired to HTML: this would be
	// speculative plumbing for a feature that doesn't exist yet, the same
	// YAGNI call bug-c65a5f4e made for a SQL properties column. Wire these
	// only alongside the routing logic that would actually read them.
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	CapabilityTags       []string `json:"capability_tags,omitempty"`

	// Context tracking fields have no writer or reader anywhere in the
	// codebase (audited bug-e5c04997). Token/cost tracking is now owned by
	// the observe/otel/* pipeline (core/db/otel_schema.go,
	// observe/otel/materialize) at per-signal granularity in SQLite — a
	// coarser Node-level cache would be a second, easily-stale source of
	// truth for the same numbers. Not wired to HTML; a per-Node rollup, if
	// ever wanted, should be computed on demand from OTel data rather than
	// hand-maintained here.
	ContextTokensUsed int      `json:"context_tokens_used,omitempty"`
	ContextPeakTokens int      `json:"context_peak_tokens,omitempty"`
	ContextCostUSD    float64  `json:"context_cost_usd,omitempty"`
	ContextSessions   []string `json:"context_sessions,omitempty"`

	// Spike classification (set by CLI spike create --type)
	SpikeSubtype string `json:"spike_subtype,omitempty"`

	// Provenance — captured at creation so consumers can tell which model,
	// role, and wipnote CLI version produced this work item. Items written
	// before this feature was added leave these empty (rendered as "unknown").
	CreatedByAgent      string `json:"created_by_agent,omitempty"`
	CreatedByModel      string `json:"created_by_model,omitempty"`
	CreatedByRole       string `json:"created_by_role,omitempty"`
	CreatedByCLIVersion string `json:"created_by_cli_version,omitempty"`
}

// Validate checks required fields and business rules.
func (n *Node) Validate() error {
	if n.ID == "" {
		return errors.New("node ID must be non-empty")
	}
	if n.Title == "" {
		return errors.New("node title must be non-empty")
	}
	if n.SpikeSubtype != "" && n.Type != "spike" {
		return errors.New("spike_subtype can only be set on spike nodes")
	}
	return nil
}

// CompletionPercentage returns the percentage of steps completed (0-100).
func (n *Node) CompletionPercentage() int {
	if len(n.Steps) == 0 {
		if n.Status == StatusDone {
			return 100
		}
		return 0
	}
	completed := 0
	for _, s := range n.Steps {
		if s.Completed {
			completed++
		}
	}
	return (completed * 100) / len(n.Steps)
}

// NextStep returns the first incomplete step whose dependencies are all met.
func (n *Node) NextStep() *Step {
	completedIDs := make(map[string]bool)
	for _, s := range n.Steps {
		if s.Completed && s.StepID != "" {
			completedIDs[s.StepID] = true
		}
	}
	for i := range n.Steps {
		s := &n.Steps[i]
		if s.Completed {
			continue
		}
		ready := true
		for _, dep := range s.DependsOn {
			if !completedIDs[dep] {
				ready = false
				break
			}
		}
		if ready {
			return s
		}
	}
	return nil
}

// AddEdge appends an edge under the given relationship type.
func (n *Node) AddEdge(e Edge) {
	if n.Edges == nil {
		n.Edges = make(map[string][]Edge)
	}
	rel := string(e.Relationship)
	n.Edges[rel] = append(n.Edges[rel], e)
	n.UpdatedAt = time.Now().UTC()
}

// RemoveEdge removes the first edge matching targetID and relType.
// Returns true if an edge was removed, false if not found.
func (n *Node) RemoveEdge(targetID string, relType RelationshipType) bool {
	if n.Edges == nil {
		return false
	}
	rel := string(relType)
	edges, ok := n.Edges[rel]
	if !ok {
		return false
	}
	for i, e := range edges {
		if e.TargetID == targetID {
			n.Edges[rel] = append(edges[:i], edges[i+1:]...)
			if len(n.Edges[rel]) == 0 {
				delete(n.Edges, rel)
			}
			n.UpdatedAt = time.Now().UTC()
			return true
		}
	}
	return false
}

// MarshalJSON produces JSON compatible with the Python serialization.
func (n *Node) MarshalJSON() ([]byte, error) {
	type Alias Node
	return json.Marshal((*Alias)(n))
}
