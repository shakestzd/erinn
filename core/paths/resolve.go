// Package paths provides shared path-resolution utilities for the wipnote
// CLI and hook runner.
package paths

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ProjectDirOptions configures the unified project-directory resolver.
// Zero value is safe: all optional fields default to "not set".
type ProjectDirOptions struct {
	// ExplicitDir is the value of the --project-dir CLI flag.
	// When non-empty it is checked first and an error is returned if it
	// does not contain a .wipnote directory.
	ExplicitDir string

	// EventCWD is the "cwd" field extracted from a CloudEvent payload.
	// Checked after CLAUDE_PROJECT_DIR and git-common-dir resolution.
	EventCWD string

	// WalkLevels is the maximum number of parent directories to traverse
	// during the CWD walk-up phase.  0 means "no limit" (walk to root).
	WalkLevels int

	// SessionID enables session-scoped hint lookup when set (hook context).
	// CLI callers leave this empty and skip the hint step entirely.
	SessionID string
}

// ResolveProjectDir locates the project root (the directory that contains
// .wipnote/) using the following priority order:
//
//  1. opts.ExplicitDir (--project-dir flag) — hard error if set but invalid
//  2. WIPNOTE_PROJECT_DIR env var — written by yolo mode (subagent override);
//     takes precedence over ambient CLAUDE_PROJECT_DIR
//  3. CLAUDE_PROJECT_DIR env var — gated on WIPNOTE_SESSION_ID being set to
//     avoid picking up stale values from a parent shell (fix for bug-71fc095f)
//  4. Session-scoped hint file — written by SubagentStart for worktree
//     subagents whose CLAUDE_ENV_FILE is unset; read via ReadSessionHint()
//  5. ResolveViaGitCommonDir() — worktree → main repo root
//  6. opts.EventCWD — direct .wipnote check
//  7. os.Getwd() — direct .wipnote check
//  8. Walk-up from opts.EventCWD (limited by WalkLevels when > 0)
//  9. Walk-up from os.Getwd() (unlimited)
//
// Returns the project root directory (not the .wipnote subdirectory).
// The only hard-error case is when ExplicitDir is set but no .wipnote
// can be found there.  All other failures fall back gracefully.
func ResolveProjectDir(opts ProjectDirOptions) (string, error) {
	// 1. Explicit flag — highest priority, hard-fail on miss.
	if opts.ExplicitDir != "" {
		if _, err := os.Stat(filepath.Join(opts.ExplicitDir, ".wipnote")); err == nil {
			return opts.ExplicitDir, nil
		}
		return "", fmt.Errorf("--project-dir %q: no .wipnote directory found. cd into a repo with .wipnote/ or set $CLAUDE_PROJECT_DIR", opts.ExplicitDir)
	}

	// 2. WIPNOTE_PROJECT_DIR env var — written by yolo mode (subagent override);
	// takes precedence over ambient CLAUDE_PROJECT_DIR.
	if d := os.Getenv("WIPNOTE_PROJECT_DIR"); d != "" {
		if _, err := os.Stat(filepath.Join(d, ".wipnote")); err == nil {
			return d, nil
		}
	}

	// 3. CLAUDE_PROJECT_DIR env var — only trusted when WIPNOTE_SESSION_ID is
	// also set, confirming this env var was written by the current Claude Code
	// session's hook runner (not inherited as a stale value from a parent shell).
	// See: fix for bug-71fc095f (split-brain session HTML when user cd's between projects).
	if d := os.Getenv("CLAUDE_PROJECT_DIR"); d != "" && os.Getenv("WIPNOTE_SESSION_ID") != "" {
		if _, err := os.Stat(filepath.Join(d, ".wipnote")); err == nil {
			return d, nil
		}
	}

	// 4. Session-scoped hint file — written by SubagentStart for worktree
	// subagents whose CLAUDE_ENV_FILE is unset; read via ReadSessionHint().
	// Only consulted when SessionID is provided (hook context). CLI callers
	// don't set SessionID and skip this.
	if opts.SessionID != "" {
		if d := ReadSessionHint(opts.SessionID); d != "" {
			if _, err := os.Stat(filepath.Join(d, ".wipnote")); err == nil {
				return d, nil
			}
		}
	}

	// 5. Git worktree detection — resolve linked worktrees to main repo root.
	startDir := opts.EventCWD
	if startDir == "" {
		startDir, _ = os.Getwd()
	}
	if dir := ResolveViaGitCommonDir(startDir); dir != "" {
		return dir, nil
	}

	// 6. EventCWD direct check.
	if opts.EventCWD != "" {
		if _, err := os.Stat(filepath.Join(opts.EventCWD, ".wipnote")); err == nil {
			return opts.EventCWD, nil
		}
	}

	// 7. Process CWD direct check.
	if wd, err := os.Getwd(); err == nil {
		if _, err := os.Stat(filepath.Join(wd, ".wipnote")); err == nil {
			return wd, nil
		}
	}

	// 8. Walk-up from EventCWD (limited when WalkLevels > 0).
	if opts.EventCWD != "" {
		if found := walkUpForWipnote(opts.EventCWD, opts.WalkLevels); found != "" {
			return found, nil
		}
		return opts.EventCWD, nil
	}

	// 9. Walk-up from process CWD (unlimited).
	if wd, err := os.Getwd(); err == nil {
		if found := walkUpForWipnote(wd, 0); found != "" {
			return found, nil
		}
	}

	// Fallback: return best-effort directory without error (mirrors prior hook
	// behaviour where ResolveProjectDir never returned an empty string).
	if wd, err := os.Getwd(); err == nil {
		return wd, nil
	}
	return "", errors.New("no .wipnote directory found. cd into a repo with .wipnote/ or run `wipnote status` to confirm your project dir")
}

// walkUpForWipnote traverses parent directories looking for .wipnote/.
// maxLevels == 0 means walk all the way to the filesystem root.
func walkUpForWipnote(start string, maxLevels int) string {
	dir := start
	for i := 0; maxLevels == 0 || i < maxLevels; i++ {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		if _, err := os.Stat(filepath.Join(dir, ".wipnote")); err == nil {
			return dir
		}
	}
	return ""
}

// ResolveViaGitCommonDir detects when dir is inside a git linked worktree and
// returns the main repository root (i.e. the parent of the shared .git dir).
//
// When running in a linked worktree, `git rev-parse --git-common-dir` returns
// something like `../../.git` or an absolute path — its parent directory is the
// main repo root.  When running in the main worktree the command returns the
// literal string `.git`, which means we are NOT in a linked worktree; in that
// case the function returns "" so the caller falls through to its normal
// walk-up logic.
//
// The function also verifies that the resolved main repo root contains a
// `.wipnote/` directory before returning it, so callers can use the return
// value directly as a project root without a second stat.
//
// If dir is empty the function uses os.Getwd().
// All errors are silently ignored; on any failure the function returns "".
func ResolveViaGitCommonDir(dir string) string {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil || dir == "" {
			return ""
		}
	}

	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		return "" // not a git repo, or git not installed
	}

	gitCommonDir := strings.TrimSpace(string(out))
	if gitCommonDir == "" || gitCommonDir == ".git" {
		// ".git" means we are in the main worktree, not a linked worktree.
		// Let the caller's normal walk-up handle it.
		return ""
	}

	// For linked worktrees the path may be relative (e.g. "../../.git").
	if !filepath.IsAbs(gitCommonDir) {
		gitCommonDir = filepath.Join(dir, gitCommonDir)
	}
	gitCommonDir = filepath.Clean(gitCommonDir)

	mainRepoRoot := filepath.Dir(gitCommonDir)

	candidate := filepath.Join(mainRepoRoot, ".wipnote")
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return mainRepoRoot
	}
	return ""
}

// GetGitRemoteURL returns the remote origin URL for the given directory by
// running `git -C <dir> remote get-url origin`.  It returns an empty string
// on any error (not a git repo, no origin remote, git not installed, etc.).
// If dir is empty, the function returns "" immediately.
func GetGitRemoteURL(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SessionHintPath returns the path to the session-scoped project dir hint.
func SessionHintPath(sessionID string) string {
	return filepath.Join(os.TempDir(), "wipnote-session-"+sessionID+".projectdir")
}

// ReadSessionHint reads the project directory from a session-scoped hint file.
// Returns "" when the file does not exist or cannot be read.
func ReadSessionHint(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	b, err := os.ReadFile(SessionHintPath(sessionID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// WriteSessionHint writes the project directory to a session-scoped hint file.
func WriteSessionHint(sessionID, projectDir string) {
	if sessionID == "" || projectDir == "" {
		return
	}
	_ = os.WriteFile(SessionHintPath(sessionID), []byte(projectDir), 0o644)
}

// CleanupSessionHint removes the session-scoped hint file.
func CleanupSessionHint(sessionID string) {
	if sessionID == "" {
		return
	}
	_ = os.Remove(SessionHintPath(sessionID))
}

// CleanupGlobalHint removes the legacy global hint file if it exists.
// Called once at startup to clean up stale state from older versions.
func CleanupGlobalHint() {
	_ = os.Remove(filepath.Join(os.TempDir(), "wipnote-project-dir.hint"))
}

// SubagentContext holds the identity of a subagent written at SubagentStart
// so that later PreToolUse/PostToolUse hook subprocesses (which cannot read
// CLAUDE_ENV_FILE) can still resolve their agent_id and parent_event_id.
type SubagentContext struct {
	AgentID       string    `json:"agent_id"`
	ParentEventID string    `json:"parent_event_id"`
	StartedAt     time.Time `json:"started_at"`
}

// subagentHintDir returns the directory used for per-subagent hint files.
func subagentHintDir(sessionID string) string {
	return filepath.Join(os.TempDir(), "wipnote-subagents", sessionID)
}

// WriteSubagentHint persists the subagent context to a per-subagent file so
// that PreToolUse/PostToolUse hook processes can resolve it via
// ReadSubagentHint when CLAUDE_ENV_FILE is unset.
func WriteSubagentHint(sessionID, agentID, parentEventID string) {
	if sessionID == "" || agentID == "" {
		return
	}
	dir := subagentHintDir(sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	ctx := SubagentContext{
		AgentID:       agentID,
		ParentEventID: parentEventID,
		StartedAt:     time.Now().UTC(),
	}
	data, err := json.Marshal(ctx)
	if err != nil {
		return
	}
	path := filepath.Join(dir, agentID+".json")
	_ = os.WriteFile(path, data, 0o644)
}

// ReadSubagentHint reads the most recently started subagent context for a
// session. When multiple subagents are active concurrently, it returns the
// one with the latest StartedAt timestamp.
// Returns a zero SubagentContext when no hint files exist.
func ReadSubagentHint(sessionID string) SubagentContext {
	if sessionID == "" {
		return SubagentContext{}
	}
	dir := subagentHintDir(sessionID)
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(entries) == 0 {
		return SubagentContext{}
	}
	var best SubagentContext
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ctx SubagentContext
		if err := json.Unmarshal(data, &ctx); err != nil {
			continue
		}
		if best.AgentID == "" || ctx.StartedAt.After(best.StartedAt) {
			best = ctx
		}
	}
	return best
}

// CleanupSubagentHint removes the hint file for a specific subagent.
// Called from SubagentStop so stale files do not linger between sessions.
func CleanupSubagentHint(sessionID, agentID string) {
	if sessionID == "" || agentID == "" {
		return
	}
	path := filepath.Join(subagentHintDir(sessionID), agentID+".json")
	_ = os.Remove(path)
}
