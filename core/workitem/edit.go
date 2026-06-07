package workitem

import (
	"fmt"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// EditBuilder provides a fluent API for modifying an existing work item.
// Changes are buffered until Save() is called.
//
// Usage:
//
//	err := project.Features.Edit("feat-abc").
//	    SetStatus("in-progress").
//	    AddNote("Started implementation").
//	    Save()
type EditBuilder struct {
	collection *Collection
	id         string
	err        error

	ops          []func(*models.Node)
	pendingNotes []string
}

// Edit returns an EditBuilder for modifying the node with the given ID.
// If the node cannot be loaded, the error is deferred until Save().
func (c *Collection) Edit(id string) *EditBuilder {
	return &EditBuilder{
		collection: c,
		id:         id,
	}
}

// SetStatus updates the node's status.
func (e *EditBuilder) SetStatus(status string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.ops = append(e.ops, func(node *models.Node) {
		node.Status = models.NodeStatus(status)
	})
	return e
}

// SetDescription replaces the node's content body.
func (e *EditBuilder) SetDescription(desc string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.ops = append(e.ops, func(node *models.Node) {
		node.Content = desc
	})
	return e
}

// SetFindings replaces the content with a findings section.
// Primarily useful for spikes.
func (e *EditBuilder) SetFindings(findings string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.ops = append(e.ops, func(node *models.Node) {
		node.Content = fmt.Sprintf("<p>%s</p>", findings)
	})
	return e
}

// AddNote appends a timestamped note to the node's content.
func (e *EditBuilder) AddNote(note string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.pendingNotes = append(e.pendingNotes, note)
	return e
}

// SetTitle updates the node's title.
func (e *EditBuilder) SetTitle(title string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.ops = append(e.ops, func(node *models.Node) {
		node.Title = title
	})
	return e
}

// SetPriority updates the node's priority.
func (e *EditBuilder) SetPriority(priority string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.ops = append(e.ops, func(node *models.Node) {
		node.Priority = models.Priority(priority)
	})
	return e
}

// SetAgent updates the agent assignment.
func (e *EditBuilder) SetAgent(agent string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.ops = append(e.ops, func(node *models.Node) {
		node.AgentAssigned = agent
	})
	return e
}

// SetTrack links the node to a track.
func (e *EditBuilder) SetTrack(trackID string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.ops = append(e.ops, func(node *models.Node) {
		node.TrackID = trackID
	})
	return e
}

// SetProvenance records who/what created this node. Empty fields are skipped
// so it can be called multiple times to layer fallbacks (env → session → flag).
// Mirrors the four data-created-by-* attributes rendered by the node template.
func (e *EditBuilder) SetProvenance(agent, model, role, cliVersion string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.ops = append(e.ops, func(node *models.Node) {
		if agent != "" {
			node.CreatedByAgent = agent
		}
		if model != "" {
			node.CreatedByModel = model
		}
		if role != "" {
			node.CreatedByRole = role
		}
		if cliVersion != "" {
			node.CreatedByCLIVersion = cliVersion
		}
	})
	return e
}

// SetProperty sets a key-value pair in the node's Properties map.
func (e *EditBuilder) SetProperty(key string, value any) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.ops = append(e.ops, func(node *models.Node) {
		if node.Properties == nil {
			node.Properties = make(map[string]any)
		}
		node.Properties[key] = value
	})
	return e
}

// AddStep appends an implementation step to the node. The harness identity
// (Collection.base.Agent) is recorded on Step.Agent so the rendered <li>
// carries data-created-by-agent matching the article's data-created-by-agent
// for items created in the same session.
func (e *EditBuilder) AddStep(description string) *EditBuilder {
	if e.err != nil {
		return e
	}
	agent := e.collection.base.Agent
	e.ops = append(e.ops, func(node *models.Node) {
		stepID := fmt.Sprintf("step-%s-%d", node.ID, len(node.Steps))
		node.Steps = append(node.Steps, models.Step{
			StepID:      stepID,
			Description: description,
			Agent:       agent,
		})
	})
	return e
}

// Save applies all buffered changes and writes the node to disk.
// Returns an error if the initial load or the write fails.
func (e *EditBuilder) Save() error {
	if e.err != nil {
		return fmt.Errorf("edit %s: %w", e.collection.collectionName, e.err)
	}

	_, err := e.collection.mutateNode(e.id, func(node *models.Node) error {
		for _, op := range e.ops {
			op(node)
		}
		for _, note := range e.pendingNotes {
			e.applyNote(node, note)
		}
		node.UpdatedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		return fmt.Errorf("edit save: %w", err)
	}
	return nil
}

// applyNote appends one note to the node's content.
func (e *EditBuilder) applyNote(node *models.Node, note string) {
	var b strings.Builder
	if node.Content != "" {
		// Wrap existing plain-text content in <p> so it survives
		// the HTML round-trip (parser only reads element children).
		content := node.Content
		if !strings.HasPrefix(strings.TrimSpace(content), "<") {
			content = "<p>" + content + "</p>"
		}
		b.WriteString(content)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04")
	agent := e.collection.base.Agent
	b.WriteString(fmt.Sprintf(
		"\n<p><strong>[%s %s]</strong> %s</p>", now, agent, note,
	))
	node.Content = b.String()
}

// --- Collection-level note and findings operations ---------------------------

// AddNote appends a timestamped agent note to any work item's content.
// This is a convenience method on Collection so all types (features,
// bugs, spikes, tracks) inherit it.
func (c *Collection) AddNote(id, note string) error {
	_, err := c.mutateNode(id, func(node *models.Node) error {
		now := time.Now().UTC().Format("2006-01-02 15:04")
		agent := c.base.Agent

		var b strings.Builder
		if node.Content != "" {
			// Wrap existing plain-text content in <p> so it survives
			// the HTML round-trip (parser only reads element children).
			content := node.Content
			if !strings.HasPrefix(strings.TrimSpace(content), "<") {
				content = "<p>" + content + "</p>"
			}
			b.WriteString(content)
		}
		b.WriteString(fmt.Sprintf(
			"\n<p><strong>[%s %s]</strong> %s</p>", now, agent, note,
		))
		node.Content = b.String()
		node.UpdatedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		return fmt.Errorf("add note %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// SetFindings replaces the content of a work item with findings text.
// Primarily intended for spikes, but available on all collections.
func (c *Collection) SetFindings(id, findings string) error {
	_, err := c.mutateNode(id, func(node *models.Node) error {
		node.Content = fmt.Sprintf("<p>%s</p>", findings)
		node.UpdatedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		return fmt.Errorf("set findings %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}
