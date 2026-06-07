package hooks

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TrackEvent handles generic Claude Code hook events that should be recorded
// as agent_events without blocking (e.g. InstructionsLoaded, PreCompact).
func TrackEvent(toolName string, event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	featureID := cachedGetActiveFeatureID(database, sessionID)

	ev := &models.AgentEvent{
		EventID:      uuid.New().String(),
		AgentID:      resolveEventAgentID(event),
		EventType:    models.EventCheckPoint,
		Timestamp:    time.Now().UTC(),
		ToolName:     toolName,
		InputSummary: fmt.Sprintf("%s event recorded", toolName),
		SessionID:    sessionID,
		FeatureID:    featureID,
		Status:       "recorded",
		Source:       "hook",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	_ = db.InsertEvent(database, ev) // Non-fatal

	return &HookResult{Continue: true}, nil
}
