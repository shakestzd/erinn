package hooks

import (
	"database/sql"
	"os"
	"os/exec"
	"strings"
	"time"
)

// selfBinary returns the path to the wipnote binary for self-invocation.
//
// Resolution order:
//  1. os.Executable() — the binary currently running (which is whatever
//     resolved on PATH when Claude Code invoked the hook command). This is
//     the canonical answer: if `wipnote hook X` is running, self-invoking
//     `wipnote Y` should use the same binary.
//  2. "wipnote" on PATH (fallback when os.Executable() fails, rare).
//
// Note: previous versions checked `$CLAUDE_PLUGIN_ROOT/hooks/bin/wipnote`
// first. That fallback was removed because (a) hooks.json invokes the PATH
// `wipnote` directly, so the hook process already IS the PATH binary;
// (b) a stale binary lingering under plugin/hooks/bin/ could silently
// shadow the current install. Trust PATH — single source of truth.
func selfBinary() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "wipnote"
}

// CROSS-HARNESS STEP CONTRACT (feat-885ec940, Tier 4).
//
// Hook-driven step tracking (addTaskStep / completeTaskStep) is reachable ONLY
// from the Claude TaskCreated / TaskCompleted handlers in missing_events.go.
// This is intentional and load-bearing:
//
//   - Claude:  TaskCreated → addTaskStep, TaskCompleted → completeTaskStep.
//     A create/complete pair exists, so steps are honestly LIVE.
//   - Codex:   TaskStarted → TrackEvent (generic checkpoint agent_event only,
//     never addTaskStep); TaskComplete → stop/session-end (a SESSION
//     lifecycle event, NOT a per-task completion). There is no Codex
//     TaskCreate analog to pair with, so mapping TaskComplete to
//     completeTaskStep would tick steps that were never created as
//     steps — a dishonest "live steps" state. Deliberately NOT mapped.
//   - Gemini:  emits no task lifecycle hooks at all (manifest.json declares
//     none with targets:[gemini]). Nothing to map.
//
// The honest per-harness truth is centralized in resolveTaskTrackingInfo
// (cmd/wipnote/who.go): codex-cli / gemini-cli report Supported=false with an
// UNSUPPORTED detail string. Tier 4 surfaces that exact signal into
// /api/features as step_tracking_supported / step_tracking_detail so the
// Kanban board renders a visible "steps not live for this harness" state and
// NEVER implies live step tracking for a harness that cannot emit step events.
// Because no harness→step mapping changed, manifest.json is unchanged and no
// `wipnote plugin build-ports` regeneration is required.
//
// addTaskStep shells out to the wipnote CLI to add a task-associated step to
// the active feature. The CLI sets StepID="task-<taskID>" so completeTaskStep
// can find and tick it. Shells out rather than importing workitem directly
// (architectural constraint: hooks must not import workitem).
func addTaskStep(_ *sql.DB, _ string, featureID, taskID, subject, teammateName string) {
	if subject == "" {
		subject = "Task " + taskID
	}
	stepDesc := subject
	if teammateName != "" {
		stepDesc = "[" + teammateName + "] " + stepDesc
	}
	typeName := inferTypeName(featureID)

	// wipnote <type> add-task-step <id> <task-id> "<description>"
	cmd := exec.Command(selfBinary(), typeName, "add-task-step", featureID, taskID, stepDesc)
	_ = cmd.Run()
}

// completeTaskStep flips data-completed=true on the step with
// StepID="task-<taskID>" via the CLI. The CLI call (which uses
// workitem.Collection.CompleteTaskStep) is the canonical update — it mutates
// HTML and updates SQLite counters in one transaction.
func completeTaskStep(database *sql.DB, _ string, featureID, taskID, _ string) {
	typeName := inferTypeName(featureID)
	cmd := exec.Command(selfBinary(), typeName, "complete-task-step", featureID, taskID)
	_ = cmd.Run()

	if database == nil {
		return
	}
	// Bump updated_at so query consumers see freshness (CLI also updates this,
	// but the hook may run before/after the CLI completes — this is a no-op
	// when the CLI already touched the row).
	_, _ = database.Exec(`
		UPDATE features
		SET updated_at = ?
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
