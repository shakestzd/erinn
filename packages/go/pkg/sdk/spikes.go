package sdk

import (
	"fmt"
	"sort"
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// SpikeOption configures a new spike during creation.
type SpikeOption func(*spikeConfig)

type spikeConfig struct {
	priority  string
	status    string
	trackID   string
	steps     []string
	content   string
	spikeType string
	findings  string
	timebox   string // e.g. "4h"
}

// SpikeWithPriority sets the spike's priority.
func SpikeWithPriority(p string) SpikeOption {
	return func(c *spikeConfig) { c.priority = p }
}

// SpikeWithStatus sets the spike's initial status.
func SpikeWithStatus(s string) SpikeOption {
	return func(c *spikeConfig) { c.status = s }
}

// SpikeWithTrack links the spike to a track.
func SpikeWithTrack(trackID string) SpikeOption {
	return func(c *spikeConfig) { c.trackID = trackID }
}

// SpikeWithSteps adds investigation steps.
func SpikeWithSteps(steps ...string) SpikeOption {
	return func(c *spikeConfig) { c.steps = steps }
}

// SpikeWithType sets the spike investigation type.
func SpikeWithType(t string) SpikeOption {
	return func(c *spikeConfig) { c.spikeType = t }
}

// SpikeWithFindings sets the initial findings text.
func SpikeWithFindings(f string) SpikeOption {
	return func(c *spikeConfig) { c.findings = f }
}

// SpikeWithTimebox sets a timebox duration string.
func SpikeWithTimebox(tb string) SpikeOption {
	return func(c *spikeConfig) { c.timebox = tb }
}

// SpikeCollection provides CRUD operations for spikes.
type SpikeCollection struct {
	*Collection
}

// NewSpikeCollection creates a SpikeCollection bound to the SDK.
func NewSpikeCollection(s *SDK) *SpikeCollection {
	return &SpikeCollection{Collection: newCollection(s, "spikes", "spike")}
}

// Create builds a new spike, writes the HTML file, and optionally inserts
// a row into SQLite.
func (sc *SpikeCollection) Create(title string, opts ...SpikeOption) (*models.Node, error) {
	if title == "" {
		return nil, fmt.Errorf("spike title must not be empty")
	}

	cfg := &spikeConfig{
		priority: "medium",
		status:   "todo",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	now := time.Now().UTC()
	id := generateID("spike", title)

	var steps []models.Step
	for i, desc := range cfg.steps {
		steps = append(steps, models.Step{
			StepID:      fmt.Sprintf("step-%s-%d", id, i),
			Description: desc,
		})
	}

	// Build content from findings and description
	content := cfg.content
	if cfg.findings != "" {
		content = fmt.Sprintf("<p>%s</p>", cfg.findings)
	}

	node := &models.Node{
		ID:            id,
		Title:         title,
		Type:          "spike",
		Status:        models.NodeStatus(cfg.status),
		Priority:      models.Priority(cfg.priority),
		CreatedAt:     now,
		UpdatedAt:     now,
		AgentAssigned: sc.base.Agent,
		TrackID:       cfg.trackID,
		SpikeSubtype:  cfg.spikeType,
		Steps:         steps,
		Content:       content,
	}

	dbFeat := &dbpkg.Feature{
		ID:             id,
		Type:           "spike",
		Title:          title,
		Description:    content,
		Status:         cfg.status,
		Priority:       cfg.priority,
		AssignedTo:     sc.base.Agent,
		TrackID:        cfg.trackID,
		CreatedAt:      now,
		UpdatedAt:      now,
		StepsTotal:     len(steps),
		StepsCompleted: 0,
	}

	if _, err := sc.base.DualWriteNode(
		func() (string, error) { return sc.writeNode(node) },
		dbFeat,
	); err != nil {
		return nil, fmt.Errorf("create spike: %w", err)
	}

	return node, nil
}

// SetFindings updates the content of an existing spike with investigation findings.
func (sc *SpikeCollection) SetFindings(id, findings string) (*models.Node, error) {
	node, err := sc.Get(id)
	if err != nil {
		return nil, err
	}
	node.Content = fmt.Sprintf("<p>%s</p>", findings)
	node.UpdatedAt = time.Now().UTC()
	if _, err := sc.writeNode(node); err != nil {
		return nil, err
	}
	return node, nil
}

// GetLatest returns the most recent spike(s), optionally filtered by agent.
func (sc *SpikeCollection) GetLatest(agent string, limit int) ([]*models.Node, error) {
	if limit <= 0 {
		limit = 1
	}

	nodes, err := sc.List()
	if err != nil {
		return nil, err
	}

	if agent != "" {
		var filtered []*models.Node
		for _, n := range nodes {
			if n.AgentAssigned == agent {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
	}

	// Sort by created time descending
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].CreatedAt.After(nodes[j].CreatedAt)
	})

	if len(nodes) > limit {
		nodes = nodes[:limit]
	}
	return nodes, nil
}
