package sdk

import (
	"fmt"
	"time"
)

// Claim marks a work item as claimed by the current agent.
// It sets AgentAssigned, ClaimedAt, and ClaimedBySession.
func (c *Collection) Claim(id, sessionID string) error {
	node, err := c.Get(id)
	if err != nil {
		return fmt.Errorf("claim %s/%s: %w", c.collectionName, id, err)
	}

	now := time.Now().UTC()
	node.AgentAssigned = c.base.Agent
	node.ClaimedAt = fmtTime(now)
	node.ClaimedBySession = sessionID
	node.UpdatedAt = now

	if _, err := c.writeNode(node); err != nil {
		return fmt.Errorf("claim %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// Release clears the claim on a work item, removing agent assignment
// and claim metadata.
func (c *Collection) Release(id string) error {
	node, err := c.Get(id)
	if err != nil {
		return fmt.Errorf("release %s/%s: %w", c.collectionName, id, err)
	}

	node.AgentAssigned = ""
	node.ClaimedAt = ""
	node.ClaimedBySession = ""
	node.UpdatedAt = time.Now().UTC()

	if _, err := c.writeNode(node); err != nil {
		return fmt.Errorf("release %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// AtomicClaim claims a work item only if it is not already claimed
// by another agent. Returns an error if already claimed.
func (c *Collection) AtomicClaim(id, sessionID string) error {
	node, err := c.Get(id)
	if err != nil {
		return fmt.Errorf("atomic claim %s/%s: %w", c.collectionName, id, err)
	}

	if node.ClaimedBySession != "" && node.ClaimedBySession != sessionID {
		return fmt.Errorf(
			"atomic claim %s/%s: already claimed by session %s",
			c.collectionName, id, node.ClaimedBySession,
		)
	}
	if node.AgentAssigned != "" && node.AgentAssigned != c.base.Agent {
		return fmt.Errorf(
			"atomic claim %s/%s: already claimed by agent %s",
			c.collectionName, id, node.AgentAssigned,
		)
	}

	now := time.Now().UTC()
	node.AgentAssigned = c.base.Agent
	node.ClaimedAt = fmtTime(now)
	node.ClaimedBySession = sessionID
	node.UpdatedAt = now

	if _, err := c.writeNode(node); err != nil {
		return fmt.Errorf("atomic claim %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}

// Unclaim removes the claim metadata without changing the node's status.
// Unlike Release, Unclaim only clears ClaimedAt and ClaimedBySession
// but preserves AgentAssigned.
func (c *Collection) Unclaim(id string) error {
	node, err := c.Get(id)
	if err != nil {
		return fmt.Errorf("unclaim %s/%s: %w", c.collectionName, id, err)
	}

	node.ClaimedAt = ""
	node.ClaimedBySession = ""
	node.UpdatedAt = time.Now().UTC()

	if _, err := c.writeNode(node); err != nil {
		return fmt.Errorf("unclaim %s/%s: %w", c.collectionName, id, err)
	}
	return nil
}
