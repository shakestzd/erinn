package models

import "time"

// FeatureFile records a file path touched by a feature, linking them in the
// feature_files relationship table.
type FeatureFile struct {
	ID        string    `json:"id"`
	FeatureID string    `json:"feature_id"`
	FilePath  string    `json:"file_path"`
	Operation string    `json:"operation"` // read, write, edit, glob, grep
	SessionID string    `json:"session_id,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	CreatedAt time.Time `json:"created_at"`
}
