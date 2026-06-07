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
	TrackID          string   `json:"track_id,omitempty"`
	PlanTaskID       string   `json:"plan_task_id,omitempty"`
	SpecRequirements []string `json:"spec_requirements,omitempty"`

	// Handoff context
	HandoffRequired  bool   `json:"handoff_required,omitempty"`
	PreviousAgent    string `json:"previous_agent,omitempty"`
	HandoffReason    string `json:"handoff_reason,omitempty"`
	HandoffNotes     string `json:"handoff_notes,omitempty"`
	HandoffTimestamp string `json:"handoff_timestamp,omitempty"`

	// Capability-based routing
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	CapabilityTags       []string `json:"capability_tags,omitempty"`

	// Context tracking
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
