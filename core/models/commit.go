package models

import "time"

// GitCommit represents a git commit captured during a session,
// linked to the active feature at the time of the commit.
type GitCommit struct {
	CommitHash  string
	SessionID   string
	FeatureID   string
	ToolEventID string
	Message     string
	Timestamp   time.Time
}
