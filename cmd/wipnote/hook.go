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
		// task-started / task-aborted are NOT registered for any harness in
		// packages/plugin-core/manifest.json and never were reachable: they were
		// bound to the Codex names TaskStarted and TurnAborted, neither of which
		// Codex dispatches (bug-e39d408f — TaskStarted appears zero times in the
		// 0.147.0 binary; TurnAborted is an internal ThreadEvent telemetry
		// variant, not a hook). Both bodies are generic TrackEvent checkpoints
		// with no side effect the registered handlers lack, so nothing was lost
		// when the registrations were dropped. Kept as manually-invocable
		// subcommands (a user's own hooks.json can call them, and a future
		// harness may supply a real turn-lifecycle event to repoint them at);
		// the build-time gate in port/pluginbuild/hook_event_names.go is what
		// prevents them being re-registered against a name nothing fires.
		hookSubcmd("task-started", "Record a turn-start checkpoint (not wired to any harness event)", continueResult,
			func(event *hooks.CloudEvent, database *sql.DB) (*hooks.HookResult, error) {
				return hooks.TrackEvent("TaskStarted", event, database)
			}),
		hookSubcmd("task-aborted", "Record a turn-abort checkpoint (not wired to any harness event)", continueResult,
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
				// Finding 1 (roborev-478 round-3): when the truly-read-only open
				// yields no usable handle (first run, deleted cache, DB not yet
				// created, or a transient lock), do NOT skip the handler — that
				// would silently bypass the DB-INDEPENDENT safety guards
				// (pretooluse's .wipnote/-write block, the cd/divergence guards,
				// the yolo work-item guard). Instead invoke the handler with a
				// nil DB so those guards still run in guard-only mode; the
				// read-only-dispatched handlers (user-prompt, pretooluse) are
				// nil-DB-safe — every DB-dependent read no-ops / returns empty
				// when database == nil — and route any write through the daemon
				// regardless of this handle.
				database, reason := hooks.OpenHookDBReadOnly(use, event.SessionID, dbPath)
				if database != nil {
					defer database.Close()
				} else {
					_ = reason // already logged + counted inside OpenHookDBReadOnly
				}
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
				// Handle selection is PER-HANDLER (roborev-476 finding 3):
				//   - session-start runs on the launcher's post-selection critical
				//     path. It routes every session/lineage/event/family write through
				//     the daemon (RouteHookWrite / routeSQLApplied) and uses this handle
				//     for READS plus ONE residual direct write — the bounded orphan sweep
				//     (SweepOrphanedEventsForProjectCapped → db.MarkEventAborted), whose
				//     atomic started→aborted transition needs RowsAffected and so cannot
				//     be routed enqueue-only. Because that write still touches this
				//     handle, session-start takes a BOUNDED handle
				//     (SessionStartBusyTimeout ~750ms): under a held external write lock
				//     the orphan-sweep UPDATE fail-fasts in well under a second (the
				//     residual orphan drains out-of-band via serve) instead of stalling
				//     the interactive launcher ~5s.
				//   - session-end / session-resume / exit-plan-mode keep the default 5s
				//     OpenHookDB: they are not on the post-selection critical path and
				//     their residual writes (FinalizeSessionHTML, runSessionExitReconcile)
				//     are tolerant of the default busy_timeout.
				database, reason := openSessionHookDB(use, event.SessionID, dbPath)
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

// openSessionHookDB selects the writable hook handle for the three session
// handlers dispatched by hookSubcmdWithProject (roborev-476 finding 3).
//
// session-start runs on the launcher's post-selection critical path and its only
// remaining direct write (the bounded orphan sweep db.MarkEventAborted, which
// needs RowsAffected and so cannot be routed enqueue-only) must fail-fast under a
// held external write lock — so it takes a SHORT bounded busy_timeout
// (SessionStartBusyTimeout ~750ms) rather than stalling the interactive launcher
// on the default 5s. Every other session-phase handler (session-end,
// session-resume, exit-plan-mode) keeps the default 5s OpenHookDB; they are not
// on the post-selection critical path. A nil handle is treated as
// canonical-success by the caller exactly as for OpenHookDB.
func openSessionHookDB(use, sessionID, dbPath string) (*sql.DB, hooks.FallbackReason) {
	if use == "session-start" {
		return hooks.OpenHookDBWithBusyTimeout(use, sessionID, dbPath, hooks.SessionStartBusyTimeout)
	}
	return hooks.OpenHookDB(use, sessionID, dbPath)
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
