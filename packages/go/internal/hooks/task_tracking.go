package hooks

import (
	"database/sql"
	"os/exec"
	"strings"
	"time"
)

// addTaskStep shells out to the htmlgraph CLI to add a step to the active
// feature. This avoids importing the workitem package (architectural constraint:
// hooks must not import workitem to prevent spike creation policy violations).
func addTaskStep(database *sql.DB, sessionID, featureID, taskID, subject string) {
	if subject == "" {
		subject = "Task " + taskID
	}
	stepDesc := subject + " [task:" + taskID + "]"
	typeName := inferTypeName(featureID)

	// htmlgraph <type> add-step <id> "<description>"
	cmd := exec.Command("htmlgraph", typeName, "add-step", featureID, stepDesc)
	_ = cmd.Run()
}

// completeTaskStep marks a step as done by updating the step counters in SQLite.
// Full HTML step completion requires the workitem package, so we only update the
// database counters here. The HTML will be reconciled on next reindex.
func completeTaskStep(database *sql.DB, sessionID, featureID, taskID string) {
	if database == nil {
		return
	}
	// Increment steps_completed counter.
	_, _ = database.Exec(`
		UPDATE features
		SET steps_completed = MIN(steps_completed + 1, steps_total),
		    updated_at = ?
		WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), featureID)
}

// inferTypeName returns the CLI type name (feature, bug, spike) from an ID prefix.
func inferTypeName(id string) string {
	switch {
	case strings.HasPrefix(id, "bug-"):
		return "bug"
	case strings.HasPrefix(id, "spk-"):
		return "spike"
	default:
		return "feature"
	}
}

