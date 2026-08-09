package workitem

import (
	"fmt"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// SpecOption configures a new spec during creation.
type SpecOption func(*specConfig)

type specConfig struct {
	priority string
	status   string
	trackID  string
	steps    []string
	content  string
}

// SpecWithPriority sets the spec's priority.
func SpecWithPriority(p string) SpecOption {
	return func(c *specConfig) { c.priority = p }
}

// SpecWithTrack links the spec to a track.
func SpecWithTrack(trackID string) SpecOption {
	return func(c *specConfig) { c.trackID = trackID }
}

// SpecWithSteps adds requirement items.
func SpecWithSteps(steps ...string) SpecOption {
	return func(c *specConfig) { c.steps = steps }
}

// SpecWithContent sets the specification body.
func SpecWithContent(content string) SpecOption {
	return func(c *specConfig) { c.content = content }
}

// SpecCollection provides CRUD operations for specs.
type SpecCollection struct {
	*Collection
}

// NewSpecCollection creates a SpecCollection bound to the given Base.
func NewSpecCollection(base *Base) *SpecCollection {
	return &SpecCollection{Collection: newCollection(base, "specs", "spec")}
}

// Create builds a new spec node and writes canonical HTML.
func (sc *SpecCollection) Create(title string, opts ...SpecOption) (*models.Node, error) {
	if title == "" {
		return nil, fmt.Errorf("spec title must not be empty")
	}

	cfg := &specConfig{priority: "medium", status: "todo"}
	for _, opt := range opts {
		opt(cfg)
	}

	now := time.Now().UTC()
	id := GenerateID("spec", title)

	var steps []models.Step
	for i, desc := range cfg.steps {
		steps = append(steps, models.Step{
			StepID:      fmt.Sprintf("step-%s-%d", id, i),
			Description: desc,
		})
	}

	node := &models.Node{
		ID:            id,
		Title:         title,
		Type:          "spec",
		Status:        models.NodeStatus(cfg.status),
		Priority:      models.Priority(cfg.priority),
		CreatedAt:     now,
		UpdatedAt:     now,
		AgentAssigned: sc.base.Agent,
		TrackID:       cfg.trackID,
		Steps:         steps,
		Content:       cfg.content,
	}

	if _, err := sc.writeNode(node); err != nil {
		return nil, fmt.Errorf("create spec: %w", err)
	}

	return node, nil
}
