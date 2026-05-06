package hooks

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// selfBinary returns the path to the erinn binary for self-invocation.
// Resolution order:
//  1. CLAUDE_PLUGIN_ROOT env var (always correct in hook context)
//  2. os.Executable() (correct when called from the binary itself)
//  3. "erinn" on PATH (fallback)
func selfBinary() string {
	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
		candidate := filepath.Join(root, "hooks", "bin", "erinn")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		legacyCandidate := filepath.Join(root, "hooks", "bin", "htmlgraph")
		if _, err := os.Stat(legacyCandidate); err == nil {
			return legacyCandidate
		}
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "erinn"
}

// addTaskStep shells out to the htmlgraph CLI to add a step to the active
// feature. This avoids importing the workitem package (architectural constraint:
// hooks must not import workitem to prevent spike creation policy violations).
func addTaskStep(database *sql.DB, sessionID, featureID, taskID, subject, teammateName string) {
	if subject == "" {
		subject = "Task " + taskID
	}
	stepDesc := subject + " [task:" + taskID + "]"
	if teammateName != "" {
		stepDesc = "[" + teammateName + "] " + stepDesc
	}
	typeName := inferTypeName(featureID)

	// htmlgraph <type> add-step <id> "<description>"
	cmd := exec.Command(selfBinary(), typeName, "add-step", featureID, stepDesc)
	_ = cmd.Run()
}

// completeTaskStep marks a step as done by updating the step counters in SQLite.
// Full HTML step completion requires the workitem package, so we only update the
// database counters here. The HTML will be reconciled on next reindex.
func completeTaskStep(database *sql.DB, sessionID, featureID, taskID, teammateName string) {
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
