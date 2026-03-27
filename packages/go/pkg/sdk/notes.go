package sdk

import (
	"fmt"
	"strings"
	"time"
)

// AddNote appends a timestamped agent note to any work item's content.
// This is a convenience method on Collection so all types (features,
// bugs, spikes, tracks) inherit it.
func (c *Collection) AddNote(id, note string) error {
	node, err := c.Get(id)
	if err != nil {
		return fmt.Errorf("add note %s/%s: %w", c.collectionName, id, err)
	}

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

	if _, err := c.writeNode(node); err != nil {
		return fmt.Errorf("add note %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// SetFindings replaces the content of a work item with findings text.
// Primarily intended for spikes, but available on all collections.
func (c *Collection) SetFindings(id, findings string) error {
	node, err := c.Get(id)
	if err != nil {
		return fmt.Errorf("set findings %s/%s: %w", c.collectionName, id, err)
	}

	node.Content = fmt.Sprintf("<p>%s</p>", findings)
	node.UpdatedAt = time.Now().UTC()

	if _, err := c.writeNode(node); err != nil {
		return fmt.Errorf("set findings %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}
