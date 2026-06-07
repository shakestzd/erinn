package workitem

import (
	"fmt"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// BugOption configures a new bug during creation.
type BugOption func(*bugConfig)

type bugConfig struct {
	priority   string
	status     string
	trackID    string
	steps      []string
	content    string
	severity   string
	reproSteps []string
}

// BugWithPriority sets the bug's priority.
func BugWithPriority(p string) BugOption {
	return func(c *bugConfig) { c.priority = p }
}

// BugWithStatus sets the bug's initial status.
func BugWithStatus(s string) BugOption {
	return func(c *bugConfig) { c.status = s }
}

// BugWithTrack links the bug to a track.
func BugWithTrack(trackID string) BugOption {
	return func(c *bugConfig) { c.trackID = trackID }
}

// BugWithSteps adds implementation/fix steps.
func BugWithSteps(steps ...string) BugOption {
	return func(c *bugConfig) { c.steps = steps }
}

// BugWithContent sets the description body.
func BugWithContent(content string) BugOption {
	return func(c *bugConfig) { c.content = content }
}

// BugWithSeverity sets the severity level.
func BugWithSeverity(s string) BugOption {
	return func(c *bugConfig) { c.severity = s }
}

// BugWithReproSteps documents how to reproduce the bug.
func BugWithReproSteps(steps ...string) BugOption {
	return func(c *bugConfig) { c.reproSteps = steps }
}

// BugCollection provides CRUD operations for bugs.
type BugCollection struct {
	*Collection
}

// NewBugCollection creates a BugCollection bound to the given Base.
func NewBugCollection(base *Base) *BugCollection {
	return &BugCollection{Collection: newCollection(base, "bugs", "bug")}
}

// Create builds a new bug, writes the HTML file, and optionally inserts
// a row into SQLite.
func (bc *BugCollection) Create(title string, opts ...BugOption) (*models.Node, error) {
	if title == "" {
		return nil, fmt.Errorf("bug title must not be empty")
	}

	cfg := &bugConfig{
		priority: "medium",
		status:   "todo",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	now := time.Now().UTC()
	id := GenerateID("bug", title)

	// Build steps from both explicit steps and repro steps
	var steps []models.Step
	for i, desc := range cfg.reproSteps {
		steps = append(steps, models.Step{
			StepID:      fmt.Sprintf("step-%s-repro-%d", id, i),
			Description: "[Repro] " + desc,
		})
	}
	for i, desc := range cfg.steps {
		steps = append(steps, models.Step{
			StepID:      fmt.Sprintf("step-%s-%d", id, i),
			Description: desc,
		})
	}

	// Build content including severity
	content := cfg.content
	if cfg.severity != "" && content == "" {
		content = fmt.Sprintf("<p>Severity: %s</p>", cfg.severity)
	}

	node := &models.Node{
		ID:            id,
		Title:         title,
		Type:          "bug",
		Status:        models.NodeStatus(cfg.status),
		Priority:      models.Priority(cfg.priority),
		CreatedAt:     now,
		UpdatedAt:     now,
		AgentAssigned: bc.base.Agent,
		TrackID:       cfg.trackID,
		Steps:         steps,
		Content:       content,
	}

	if _, err := bc.writeNode(node); err != nil {
		return nil, fmt.Errorf("create bug: %w", err)
	}

	// Dual-write to SQLite
	if bc.base.DB != nil {
		dbFeat := &dbpkg.Feature{
			ID:             id,
			Type:           "bug",
			Title:          title,
			Description:    content,
			Status:         cfg.status,
			Priority:       cfg.priority,
			AssignedTo:     bc.base.Agent,
			TrackID:        cfg.trackID,
			CreatedAt:      now,
			UpdatedAt:      now,
			StepsTotal:     len(steps),
			StepsCompleted: 0,
		}
		// UpsertFeature overwrites any placeholder row from ensureFeatureRow (bug-7f4a1a9c).
		_ = dbpkg.UpsertFeature(bc.base.DB, dbFeat)
	}

	return node, nil
}
