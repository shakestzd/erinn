package workitem

import (
	"fmt"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// FeatureOption configures a new feature during creation.
type FeatureOption func(*featureConfig)

type featureConfig struct {
	priority string
	status   string
	trackID  string
	steps    []string
	content  string
	edges    map[string][]models.Edge
}

// FeatWithPriority sets the feature's priority.
func FeatWithPriority(p string) FeatureOption {
	return func(c *featureConfig) { c.priority = p }
}

// FeatWithStatus sets the feature's initial status.
func FeatWithStatus(s string) FeatureOption {
	return func(c *featureConfig) { c.status = s }
}

// FeatWithTrack links the feature to a track.
func FeatWithTrack(trackID string) FeatureOption {
	return func(c *featureConfig) { c.trackID = trackID }
}

// FeatWithSteps adds implementation steps.
func FeatWithSteps(steps ...string) FeatureOption {
	return func(c *featureConfig) { c.steps = steps }
}

// FeatWithContent sets the description body.
func FeatWithContent(content string) FeatureOption {
	return func(c *featureConfig) { c.content = content }
}

// FeatWithEdge adds a typed edge to another node.
func FeatWithEdge(relType, targetID string) FeatureOption {
	return func(c *featureConfig) {
		if c.edges == nil {
			c.edges = make(map[string][]models.Edge)
		}
		c.edges[relType] = append(c.edges[relType], models.Edge{
			TargetID:     targetID,
			Relationship: models.RelationshipType(relType),
			Since:        time.Now().UTC(),
		})
	}
}

// FeatureCollection provides CRUD operations for features.
type FeatureCollection struct {
	*Collection
}

// NewFeatureCollection creates a FeatureCollection bound to the given Base.
func NewFeatureCollection(base *Base) *FeatureCollection {
	return &FeatureCollection{Collection: newCollection(base, "features", "feature")}
}

// Create builds a new feature and writes the canonical HTML file.
func (fc *FeatureCollection) Create(title string, opts ...FeatureOption) (*models.Node, error) {
	if title == "" {
		return nil, fmt.Errorf("feature title must not be empty")
	}

	cfg := &featureConfig{
		priority: "medium",
		status:   "todo",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	now := time.Now().UTC()
	id := GenerateID("feature", title)

	// Build steps
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
		Type:          "feature",
		Status:        models.NodeStatus(cfg.status),
		Priority:      models.Priority(cfg.priority),
		CreatedAt:     now,
		UpdatedAt:     now,
		AgentAssigned: fc.base.Agent,
		TrackID:       cfg.trackID,
		Steps:         steps,
		Content:       cfg.content,
		Edges:         cfg.edges,
	}

	if _, err := fc.writeNode(node); err != nil {
		return nil, fmt.Errorf("create feature: %w", err)
	}

	return node, nil
}
