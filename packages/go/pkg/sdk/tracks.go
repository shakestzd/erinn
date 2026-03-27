package sdk

import (
	"fmt"
	"time"

	"github.com/shakestzd/htmlgraph/internal/models"
)

// TrackOption configures a new track during creation.
type TrackOption func(*trackConfig)

type trackConfig struct {
	priority string
	status   string
	content  string
	spec     string
	phases   []string
}

// TrackWithPriority sets the track's priority.
func TrackWithPriority(p string) TrackOption {
	return func(c *trackConfig) { c.priority = p }
}

// TrackWithStatus sets the track's initial status.
func TrackWithStatus(s string) TrackOption {
	return func(c *trackConfig) { c.status = s }
}

// TrackWithContent sets the description body.
func TrackWithContent(content string) TrackOption {
	return func(c *trackConfig) { c.content = content }
}

// TrackWithSpec sets the track specification.
func TrackWithSpec(spec string) TrackOption {
	return func(c *trackConfig) { c.spec = spec }
}

// TrackWithPlanPhases adds planning phases.
func TrackWithPlanPhases(phases ...string) TrackOption {
	return func(c *trackConfig) { c.phases = phases }
}

// TrackCollection provides CRUD operations for tracks.
type TrackCollection struct {
	*Collection
}

// NewTrackCollection creates a TrackCollection bound to the SDK.
func NewTrackCollection(s *SDK) *TrackCollection {
	return &TrackCollection{Collection: newCollection(s, "tracks", "track")}
}

// Create builds a new track, writes the HTML file.
func (tc *TrackCollection) Create(title string, opts ...TrackOption) (*models.Node, error) {
	if title == "" {
		return nil, fmt.Errorf("track title must not be empty")
	}

	cfg := &trackConfig{
		priority: "medium",
		status:   "todo",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	now := time.Now().UTC()
	id := generateID("track", title)

	// Build steps from phases
	var steps []models.Step
	for i, phase := range cfg.phases {
		steps = append(steps, models.Step{
			StepID:      fmt.Sprintf("step-%s-%d", id, i),
			Description: phase,
		})
	}

	// Combine spec and content
	content := cfg.content
	if cfg.spec != "" {
		content = fmt.Sprintf("<p>%s</p>\n%s", cfg.spec, content)
	}

	node := &models.Node{
		ID:            id,
		Title:         title,
		Type:          "track",
		Status:        models.NodeStatus(cfg.status),
		Priority:      models.Priority(cfg.priority),
		CreatedAt:     now,
		UpdatedAt:     now,
		AgentAssigned: tc.base.Agent,
		Steps:         steps,
		Content:       content,
	}

	if _, err := tc.writeNode(node); err != nil {
		return nil, fmt.Errorf("create track: %w", err)
	}
	return node, nil
}
