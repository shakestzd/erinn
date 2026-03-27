package sdk

import (
	"fmt"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/models"
)

// EditBuilder provides a fluent API for modifying an existing work item.
// Changes are buffered until Save() is called.
//
// Usage:
//
//	err := sdk.Features.Edit("feat-abc").
//	    SetStatus("in-progress").
//	    AddNote("Started implementation").
//	    Save()
type EditBuilder struct {
	collection *Collection
	node       *models.Node
	err        error

	pendingNotes []string
}

// Edit returns an EditBuilder for modifying the node with the given ID.
// If the node cannot be loaded, the error is deferred until Save().
func (c *Collection) Edit(id string) *EditBuilder {
	node, err := c.Get(id)
	return &EditBuilder{
		collection: c,
		node:       node,
		err:        err,
	}
}

// SetStatus updates the node's status.
func (e *EditBuilder) SetStatus(status string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.node.Status = models.NodeStatus(status)
	return e
}

// SetDescription replaces the node's content body.
func (e *EditBuilder) SetDescription(desc string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.node.Content = desc
	return e
}

// SetFindings replaces the content with a findings section.
// Primarily useful for spikes.
func (e *EditBuilder) SetFindings(findings string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.node.Content = fmt.Sprintf("<p>%s</p>", findings)
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

// SetPriority updates the node's priority.
func (e *EditBuilder) SetPriority(priority string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.node.Priority = models.Priority(priority)
	return e
}

// SetAgent updates the agent assignment.
func (e *EditBuilder) SetAgent(agent string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.node.AgentAssigned = agent
	return e
}

// SetTrack links the node to a track.
func (e *EditBuilder) SetTrack(trackID string) *EditBuilder {
	if e.err != nil {
		return e
	}
	e.node.TrackID = trackID
	return e
}

// Save applies all buffered changes and writes the node to disk.
// Returns an error if the initial load or the write fails.
func (e *EditBuilder) Save() error {
	if e.err != nil {
		return fmt.Errorf("edit %s: %w", e.collection.collectionName, e.err)
	}

	// Append any pending notes to the content
	if len(e.pendingNotes) > 0 {
		e.applyNotes()
	}

	e.node.UpdatedAt = time.Now().UTC()

	if _, err := e.collection.writeNode(e.node); err != nil {
		return fmt.Errorf("edit save: %w", err)
	}
	return nil
}

// applyNotes appends all pending notes to the node's content.
func (e *EditBuilder) applyNotes() {
	var b strings.Builder
	if e.node.Content != "" {
		// Wrap existing plain-text content in <p> so it survives
		// the HTML round-trip (parser only reads element children).
		content := e.node.Content
		if !strings.HasPrefix(strings.TrimSpace(content), "<") {
			content = "<p>" + content + "</p>"
		}
		b.WriteString(content)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04")
	agent := e.collection.base.Agent
	for _, note := range e.pendingNotes {
		b.WriteString(fmt.Sprintf(
			"\n<p><strong>[%s %s]</strong> %s</p>", now, agent, note,
		))
	}
	e.node.Content = b.String()
}
