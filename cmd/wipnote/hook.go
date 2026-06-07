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
		hookSubcmd("user-prompt", "Handle UserPromptSubmit event", emptyResult, hooks.UserPrompt),
		hookSubcmd("after-agent", "Handle Gemini AfterAgent event", continueResult, hooks.AfterAgent),
		hookSubcmd("after-model", "Handle Gemini AfterModel event", continueResult, hooks.AfterModel),
		hookSubcmd("pretooluse", "Handle PreToolUse event", allowResult, hooks.PreToolUse),
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
		return hooks.WriteResultForHarness(harness, hooks.AllowForHarness(harness))
	}
	if result == nil {
		hooks.LogError("runHook", event.SessionID, "handler returned nil result")
		return hooks.WriteResultForHarness(harness, hooks.AllowForHarness(harness))
	}

	projectDir := hooks.ResolveProjectDir(event.CWD, event.SessionID)
	hookName := subcommand
	hooks.LogTimed(projectDir, "runHook", map[string]string{
		"hook":    hookName,
		"session": event.SessionID[:hooks.MinSessionLen(event.SessionID)],
	}, start, "completed")

	// Emit the result in the harness-appropriate wire format.
	return hooks.WriteResultForHarness(harness, result)
}
