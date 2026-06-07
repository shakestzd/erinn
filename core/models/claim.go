package models

import (
	"encoding/json"
	"time"
)

// ClaimStatus represents the lifecycle state of a claim.
type ClaimStatus string

const (
	ClaimProposed       ClaimStatus = "proposed"
	ClaimClaimed        ClaimStatus = "claimed"
	ClaimInProgress     ClaimStatus = "in_progress"
	ClaimBlocked        ClaimStatus = "blocked"
	ClaimHandoffPending ClaimStatus = "handoff_pending"
	ClaimCompleted      ClaimStatus = "completed"
	ClaimAbandoned      ClaimStatus = "abandoned"
	ClaimExpired        ClaimStatus = "expired"
	ClaimRejected       ClaimStatus = "rejected"
)

// ActiveClaimStatuses lists statuses that represent an active (non-terminal) claim.
var ActiveClaimStatuses = []ClaimStatus{
	ClaimProposed, ClaimClaimed, ClaimInProgress, ClaimBlocked, ClaimHandoffPending,
}

// ValidClaimTransitions maps each status to the set of statuses it can transition to.
var ValidClaimTransitions = map[ClaimStatus][]ClaimStatus{
	ClaimProposed:       {ClaimClaimed, ClaimRejected, ClaimAbandoned},
	ClaimClaimed:        {ClaimInProgress, ClaimAbandoned, ClaimExpired, ClaimRejected},
	ClaimInProgress:     {ClaimCompleted, ClaimBlocked, ClaimHandoffPending, ClaimAbandoned, ClaimExpired},
	ClaimBlocked:        {ClaimInProgress, ClaimAbandoned, ClaimExpired, ClaimHandoffPending},
	ClaimHandoffPending: {ClaimClaimed, ClaimAbandoned, ClaimExpired},
	// Terminal states: no transitions out of completed, abandoned, expired, rejected
}

// WriteScope declares the set of artifacts a claim is allowed to modify.
type WriteScope struct {
	Paths       []string `json:"paths,omitempty"`
	Directories []string `json:"directories,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	Worktree    string   `json:"worktree,omitempty"`
}

// Claim represents a time-bounded assignment of a work item to a session.
type Claim struct {
	ClaimID          string          `json:"claim_id"`
	WorkItemID       string          `json:"work_item_id"`
	TrackID          string          `json:"track_id,omitempty"`
	OwnerSessionID   string          `json:"owner_session_id"`
	OwnerAgent       string          `json:"owner_agent"`
	ClaimedByAgentID string          `json:"claimed_by_agent_id"` // subagent ID or "" for orchestrator
	Status           ClaimStatus     `json:"status"`
	IntendedOutput   string          `json:"intended_output,omitempty"`
	WriteScope       json.RawMessage `json:"write_scope,omitempty"`
	LeasedAt         time.Time       `json:"leased_at"`
	LeaseExpiresAt   time.Time       `json:"lease_expires_at"`
	LastHeartbeatAt  time.Time       `json:"last_heartbeat_at"`
	Dependencies     json.RawMessage `json:"dependencies,omitempty"`
	ProgressNotes    string          `json:"progress_notes,omitempty"`
	BlockerReason    string          `json:"blocker_reason,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// IsActive reports whether the claim is in a non-terminal state.
func (c *Claim) IsActive() bool {
	for _, s := range ActiveClaimStatuses {
		if c.Status == s {
			return true
		}
	}
	return false
}

// CanTransitionTo reports whether transitioning to the target status is valid.
func (c *Claim) CanTransitionTo(target ClaimStatus) bool {
	allowed, ok := ValidClaimTransitions[c.Status]
	if !ok {
		return false // terminal state
	}
	for _, s := range allowed {
		if s == target {
			return true
		}
	}
	return false
}
