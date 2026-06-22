package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/provenance"
	"github.com/spf13/cobra"
)

// hookCmd returns the "wipnote hook" parent command with all subcommands.
func hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "hook",
		Short:         "Claude Code hook handlers (replaces Python hook scripts)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Hook subcommands read a CloudEvent JSON payload from stdin and write a
JSON result to stdout. They replace the Python hook scripts, eliminating the
~500ms uv cold-start cost per hook invocation.

Usage in hooks.json:
  "command": "wipnote hook session-start"
  "command": "wipnote hook pretooluse"
  etc.`,
		// Propagate the compiled version to the hooks package so session-start
		// can detect CLI/plugin version mismatches and so provenance attribution
		// records the binary that wrote each session/work item.
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			hooks.CLIVersion = version
			provenance.SetCLIVersion(version)
		},
	}

	// Shared fallback results used across commands.
	continueResult := &hooks.HookResult{Continue: true}
	allowResult := &hooks.HookResult{} // Empty object = allow (avoids Claude Code "hook error" label)
	emptyResult := &hooks.HookResult{}

	cmd.AddCommand(
		// Session lifecycle — need projectDir passed to the handler.
		hookSubcmdWithProject("session-start", "Handle SessionStart event", emptyResult,
			func(event *hooks.CloudEvent, database *sql.DB, projectDir string) (*hooks.HookResult, error) {
				hooks.ApplyTraceparent()
				return hooks.SessionStart(event, database, projectDir)
			}),
		hookSubcmdWithProject("session-end", "Handle SessionEnd event", continueResult, hooks.SessionEnd),
		hookSubcmdWithProject("session-resume", "Handle SessionResume event", continueResult, hooks.SessionResume),

		// Standard two-arg handlers (event + db only).
		// roborev-473 finding 1 (VERIFIED per-handler): user-prompt and pretooluse
		// route EVERY derived-index WRITE through the daemon and use their DB handle
		// ONLY for READS, so they dispatch with a READ-ONLY handle
		// (hookSubcmdReadOnly) — avoiding the writable open + cold-DB migration that
		// blocked the hot hooks under contention.
		//
		// The following hot hooks KEEP a writable handle because grep confirmed they
		// still issue DIRECT writes on the passed handle (correctness-first — forcing
		// read-only would cause "readonly database" errors):
		//   - subagent-start: finding-3 applied-ack pending fallback
		//     (db.UpsertPendingSubagentStart) + synthetic-sessions via-fallback.
		//   - subagent-stop: db.UpdateEventFields direct write.
		//   - stop: insertAssistantTextSignal, backfillMissedUserPrompts,
		//     runSessionExitReconcile, db.UpdateSessionHandoff — all direct writes on
		//     the passed handle that are not (yet) daemon-routable.
		hookSubcmdReadOnly("user-prompt", "Handle UserPromptSubmit event", emptyResult, hooks.UserPrompt),
		hookSubcmd("after-agent", "Handle Gemini AfterAgent event", continueResult, hooks.AfterAgent),
		hookSubcmd("after-model", "Handle Gemini AfterModel event", continueResult, hooks.AfterModel),
		hookSubcmdReadOnly("pretooluse", "Handle PreToolUse event", allowResult, hooks.PreToolUse),
		hookSubcmd("posttooluse", "Handle PostToolUse event", continueResult, hooks.PostToolUse),
		hookSubcmd("subagent-start", "Handle SubagentStart event", continueResult, hooks.SubagentStart),
		hookSubcmd("subagent-stop", "Handle SubagentStop event", continueResult, hooks.SubagentStop),
		hookSubcmd("stop", "Handle Stop event", continueResult, hooks.Stop),
		hookSubcmd("posttooluse-failure", "Handle PostToolUseFailure event", continueResult, hooks.PostToolUseFailure),
		hookSubcmd("pre-compact", "Handle PreCompact event", continueResult, hooks.PreCompact),
		hookSubcmd("post-compact", "Handle PostCompact event", continueResult, hooks.PostCompact),
		hookWorktreeCreateCmd(),
		hookSubcmd("worktree-remove", "Handle WorktreeRemove event", continueResult, hooks.WorktreeRemove),
		hookSubcmd("teammate-idle", "Handle TeammateIdle event", continueResult, hooks.TeammateIdle),
		hookSubcmd("task-completed", "Handle TaskCompleted event", continueResult, hooks.TaskCompleted),
		hookSubcmd("task-created", "Handle TaskCreated event", continueResult, hooks.TaskCreated),
		hookSubcmd("task-started", "Handle TaskStarted event", continueResult,
			func(event *hooks.CloudEvent, database *sql.DB) (*hooks.HookResult, error) {
				return hooks.TrackEvent("TaskStarted", event, database)
			}),
		hookSubcmd("task-aborted", "Handle TurnAborted event", continueResult,
			func(event *hooks.CloudEvent, database *sql.DB) (*hooks.HookResult, error) {
				return hooks.TrackEvent("TurnAborted", event, database)
			}),
		hookSubcmd("instructions-loaded", "Handle InstructionsLoaded event", continueResult, hooks.InstructionsLoaded),
		hookSubcmd("permission-request", "Handle PermissionRequest event", continueResult, hooks.PermissionRequest),
		hookSubcmd("config-change", "Handle ConfigChange event — persist permission_mode to session metadata", continueResult, hooks.ConfigChange),
		hookSubcmdWithProject("exit-plan-mode", "Handle ExitPlanMode event — convert markdown plan to CRISPI YAML", continueResult, handleExitPlanMode),

		// track-event accepts an optional tool-name argument.
		hookTrackEventCmd(continueResult),
	)
	return cmd
}

// hookWorktreeCreateCmd handles Claude Code's WorktreeCreate replacement hook.
// It must print exactly the created worktree path on stdout, bypassing the JSON
// HookResult response layer used by ordinary hooks.
func hookWorktreeCreateCmd() *cobra.Command {
	const use = "worktree-create"
	return &cobra.Command{
		Use:   use,
		Short: "Handle WorktreeCreate event",
		RunE: func(_ *cobra.Command, _ []string) error {
			rawPayload, err := hooks.ReadRawStdin()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return err
			}
			event, err := hooks.ParseClaudeWorktreeCreateEvent(rawPayload)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return err
			}
			hooks.TraceInvocation(use, rawPayload, event)

			projectDir := hooks.ResolveProjectDir(event.CWD, event.SessionID)
			if !hooks.IswipnoteProject(projectDir) {
				err := fmt.Errorf("not a wipnote project: %s", projectDir)
				fmt.Fprintln(os.Stderr, err)
				return err
			}
			dbPath, err := hooks.DBPath(projectDir)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return err
			}
			// Canonical-first fallback (feat-075c110d): OpenHookDB direct-opens
			// the writable handle. When it is unavailable (writer_unavailable —
			// e.g. the daemon holds the single writable handle, or a transient
			// lock), we do NOT abort: worktree creation does not require the DB,
			// only the best-effort checkpoint does. We proceed with a nil handle
			// so the worktree is still created and its bare path is still echoed
			// (#119 contract). The derived-index checkpoint is recovered by
			// reindex on the next serve cycle. A genuine failure (missing
			// worktree_name / worktree_base_path, or a worktree creation error)
			// still returns a non-zero error with no JSON on stdout.
			database, reason := hooks.OpenHookDB(use, event.SessionID, dbPath)
			if database != nil {
				defer database.Close()
			} else {
				_ = reason // already logged + counted inside OpenHookDB
			}

			path, err := hooks.WorktreeCreate(event, database)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return err
			}
			fmt.Fprintln(os.Stdout, path)
			return nil
		},
	}
}

// hookSubcmd creates a hook subcommand that resolves the project dir and opens
// the DB before calling handler. fallback is returned when the project is not
// an wipnote project or when the DB cannot be opened.
func hookSubcmd(
	use, short string,
	fallback *hooks.HookResult,
	handler func(*hooks.CloudEvent, *sql.DB) (*hooks.HookResult, error),
) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runHookNamed(use, func(event *hooks.CloudEvent) (*hooks.HookResult, error) {
				projectDir := hooks.ResolveProjectDir(event.CWD, event.SessionID)
				if !hooks.IswipnoteProject(projectDir) {
					return fallback, nil
				}
				dbPath, err := hooks.DBPath(projectDir)
				if err != nil {
					hooks.LogError(use, event.SessionID,
						fmt.Sprintf("DBPath failed: %v", err))
					return fallback, nil
				}
				// Canonical-first contract (plan-ae0c37b2 slice 7): the
				// writable open is best-effort. A failure is logged and
				// counted as writer_unavailable; we then return the
				// fallback HookResult so Claude Code sees SUCCESS. The
				// canonical NDJSON written elsewhere in the handler tree
				// (collector, session-start Rosetta) is the authoritative
				// copy; the dashboard indexer rebuilds the derived index
				// on its next cycle.
				database, reason := hooks.OpenHookDB(use, event.SessionID, dbPath)
				if database == nil {
					_ = reason
					return fallback, nil
				}
				defer database.Close()
				return handler(event, database)
			})
		},
	}
}

// hookSubcmdReadOnly is hookSubcmd for the hot hooks that route EVERY
// derived-index write through the daemon and use their DB handle ONLY for reads
// (roborev-473 finding 1: user-prompt, pretooluse, stop). It opens the project DB
// READ-ONLY (hooks.OpenHookDBReadOnly) instead of the writable OpenHookDB, so the
// hot path never pays a writable open + cold-DB migration under contention. On a
// genuine daemon miss the handlers' write routing (routeHookWriteVia /
// RouteHookWrite) transparently opens its own bounded writable handle, so the
// derived write is still persisted; canonical NDJSON + reindex remain the
// backstop. A read-only open failure is logged + counted as writer_unavailable
// and falls through to the fallback HookResult (Claude Code sees SUCCESS).
func hookSubcmdReadOnly(
	use, short string,
	fallback *hooks.HookResult,
	handler func(*hooks.CloudEvent, *sql.DB) (*hooks.HookResult, error),
) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runHookNamed(use, func(event *hooks.CloudEvent) (*hooks.HookResult, error) {
				projectDir := hooks.ResolveProjectDir(event.CWD, event.SessionID)
				if !hooks.IswipnoteProject(projectDir) {
					return fallback, nil
				}
				dbPath, err := hooks.DBPath(projectDir)
				if err != nil {
					hooks.LogError(use, event.SessionID,
						fmt.Sprintf("DBPath failed: %v", err))
					return fallback, nil
				}
				database, reason := hooks.OpenHookDBReadOnly(use, event.SessionID, dbPath)
				if database == nil {
					_ = reason
					return fallback, nil
				}
				defer database.Close()
				return handler(event, database)
			})
		},
	}
}

// hookSubcmdWithProject is like hookSubcmd but also passes projectDir to the
// handler (needed by session-start, session-end, session-resume).
func hookSubcmdWithProject(
	use, short string,
	fallback *hooks.HookResult,
	handler func(*hooks.CloudEvent, *sql.DB, string) (*hooks.HookResult, error),
) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(_ *cobra.Command, _ []string) error {
			// session-start gets a fresh trace file before anything else.
			if use == "session-start" {
				hooks.TruncateTraceFile()
			}
			return runHookNamed(use, func(event *hooks.CloudEvent) (*hooks.HookResult, error) {
				projectDir := hooks.ResolveProjectDir(event.CWD, event.SessionID)
				if !hooks.IswipnoteProject(projectDir) {
					return fallback, nil
				}
				dbPath, err := hooks.DBPath(projectDir)
				if err != nil {
					hooks.LogError(use, event.SessionID,
						fmt.Sprintf("DBPath failed: %v", err))
					return fallback, nil
				}
				// Canonical-first contract — see hookSubcmd above.
				//
				// bug-504095f2 history: session-start used to take a SHORT
				// busy_timeout writable handle (OpenHookDBWithBusyTimeout) because
				// it ran ALL its derived writes directly on this handle on the
				// launcher's post-selection critical path. As of plan-2390966a
				// slice-3 SessionStart routes every writable Exec through the
				// daemon (RouteHookWrite — daemon-first enqueue-only with a bounded
				// ~750ms direct fallback baked in), so the handle passed here is
				// used only for READS (lineage/family lookups), which do not
				// contend a held write lock. The session-start special-case is
				// therefore retired: all three session handlers take the standard
				// default-timeout open, identical to every other hook.
				database, reason := hooks.OpenHookDB(use, event.SessionID, dbPath)
				if database == nil {
					_ = reason
					return fallback, nil
				}
				defer database.Close()
				return handler(event, database, projectDir)
			})
		},
	}
}

// hookTrackEventCmd returns the track-event subcommand, which accepts an
// optional tool-name CLI argument.
func hookTrackEventCmd(fallback *hooks.HookResult) *cobra.Command {
	return &cobra.Command{
		Use:   "track-event [tool-name]",
		Short: "Record a generic hook event",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			toolName := "GenericEvent"
			if len(args) == 1 {
				toolName = args[0]
			}
			return runHookNamed("track-event", func(event *hooks.CloudEvent) (*hooks.HookResult, error) {
				projectDir := hooks.ResolveProjectDir(event.CWD, event.SessionID)
				if !hooks.IswipnoteProject(projectDir) {
					return fallback, nil
				}
				dbPath, err := hooks.DBPath(projectDir)
				if err != nil {
					hooks.LogError("track-event", event.SessionID,
						fmt.Sprintf("DBPath failed: %v", err))
					return fallback, nil
				}
				// Canonical-first contract — see hookSubcmd above.
				database, reason := hooks.OpenHookDB("track-event", event.SessionID, dbPath)
				if database == nil {
					_ = reason
					return fallback, nil
				}
				defer database.Close()
				return hooks.TrackEvent(toolName, event, database)
			})
		},
	}
}

// runHookNamed is like runHook but also records a trace entry for diagnostics.
// It performs harness detection from the raw stdin payload so that Codex and
// Gemini payloads are parsed with their own dialect adapters and responses are
// emitted in the harness-appropriate wire format. Claude is the default path and
// its behaviour is unchanged.
func runHookNamed(subcommand string, handler func(*hooks.CloudEvent) (*hooks.HookResult, error)) error {
	start := time.Now()

	// Read raw stdin bytes first so we can detect the harness before parsing.
	rawPayload, err := hooks.ReadRawStdin()
	if err != nil {
		hooks.LogError("runHook", "", fmt.Sprintf("read stdin: %v", err))
		// Detect harness fails gracefully to Claude when payload is unreadable.
		return hooks.WriteResultForHarness(hooks.HarnessClaude, hooks.AllowForHarness(hooks.HarnessClaude))
	}

	// Detect the harness from the raw payload shape.
	harness := hooks.DetectHarness(rawPayload)

	// Parse the event using the harness-specific input adapter.
	event, err := hooks.ParseEventForHarness(harness, rawPayload)
	if err != nil {
		hooks.LogError("runHook", "", fmt.Sprintf("parse event (%s): %v", harness, err))
		return hooks.WriteResultForHarness(harness, hooks.AllowForHarness(harness))
	}

	hooks.TraceInvocation(subcommand, rawPayload, event)

	result, err := handler(event)
	if err != nil {
		var blockErr *hooks.BlockExit2Error
		if errors.As(err, &blockErr) {
			fmt.Fprintln(os.Stderr, blockErr.Message)
			os.Exit(2)
		}
		hooks.LogError("runHook", event.SessionID, fmt.Sprintf("handler error: %v", err))
		return hooks.WriteResultForHarnessEvent(harness, hookEventNameForResponse(subcommand, event), hooks.AllowForHarness(harness))
	}
	if result == nil {
		hooks.LogError("runHook", event.SessionID, "handler returned nil result")
		return hooks.WriteResultForHarnessEvent(harness, hookEventNameForResponse(subcommand, event), hooks.AllowForHarness(harness))
	}

	projectDir := hooks.ResolveProjectDir(event.CWD, event.SessionID)
	hookName := subcommand
	hooks.LogTimed(projectDir, "runHook", map[string]string{
		"hook":    hookName,
		"session": event.SessionID[:hooks.MinSessionLen(event.SessionID)],
	}, start, "completed")

	// Emit the result in the harness-appropriate wire format.
	return hooks.WriteResultForHarnessEvent(harness, hookEventNameForResponse(subcommand, event), result)
}

// hookEventNameForResponse returns the hook event name that should be echoed in
// the response's hookSpecificOutput. It first checks the incoming CloudEvent's
// HookEventName field (populated by parseGeminiEvent, parseAntigravityEvent, and
// parseCodexEvent from the payload's hook_event_name). When that is present, it
// is echoed directly — ensuring Gemini/Antigravity receive their own native event
// names (BeforeAgent, BeforeTool, AfterTool) rather than Claude canonical names.
// When HookEventName is absent (Claude payloads lack hook_event_name), the
// subcommand-to-event-name mapping provides the Claude/Codex canonical name.
func hookEventNameForResponse(subcommand string, event *hooks.CloudEvent) string {
	// Prefer the harness-native event name echoed from the incoming payload.
	if event != nil && event.HookEventName != "" {
		return event.HookEventName
	}
	// Fall back to Claude/Codex canonical event names by subcommand.
	switch subcommand {
	case "session-start":
		return "SessionStart"
	case "session-end":
		return "SessionEnd"
	case "user-prompt":
		return "UserPromptSubmit"
	case "pretooluse":
		return "PreToolUse"
	case "posttooluse":
		return "PostToolUse"
	case "after-agent":
		return "AfterAgent"
	case "after-model":
		return "AfterModel"
	case "task-started":
		return "TaskStarted"
	case "task-aborted":
		return "TurnAborted"
	default:
		return ""
	}
}
