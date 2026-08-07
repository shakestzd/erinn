package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LaunchIsolationMode is the value of the .wipnote/config.json "launch_isolation"
// key. It controls how the launcher (claude/codex/antigravity/yolo)
// treats launches on a dirty protected branch.
type LaunchIsolationMode string

const (
	// LaunchIsolationWarnOnly is the default (also used when the config file or
	// key is absent). Launching on a dirty protected branch only warns; no
	// worktree is forced and no launch is refused. BACKWARD COMPATIBLE: this is
	// today's behavior for every existing repo.
	LaunchIsolationWarnOnly LaunchIsolationMode = "warn-only"
	// LaunchIsolationEnforce refuses a launch on a dirty protected branch unless
	// the caller passes a work item (managed worktree) or --in-place.
	LaunchIsolationEnforce LaunchIsolationMode = "enforce"
	// LaunchIsolationAuto does everything enforce does AND isolates even with no
	// work item: a bare `wipnote claude` plans an ad-hoc managed worktree.
	LaunchIsolationAuto LaunchIsolationMode = "auto"
)

// launchIsolationConfig represents the single field this reader cares about in
// .wipnote/config.json. Per the per-reader decode convention (see
// core/hooks.readTaskCompletionConfig) it intentionally decodes ONLY its own key
// and ignores everything else in the flat JSON document.
type launchIsolationConfig struct {
	LaunchIsolation string `json:"launch_isolation"`
}

// readLaunchIsolationMode returns the launch_isolation mode declared in
// <canonicalRoot>/.wipnote/config.json. It returns LaunchIsolationWarnOnly (the
// backward-compatible default) when the file is missing, unreadable, the key is
// absent/empty, or the value is unrecognized.
//
// Always read from the canonical project root, never from a linked worktree.
// This ensures the project-level launch_isolation config is honored even when
// called from within an isolated worktree where .wipnote/ may be absent or
// git-excluded.
func readLaunchIsolationMode(canonicalRoot string) LaunchIsolationMode {
	data, err := os.ReadFile(filepath.Join(canonicalRoot, ".wipnote", "config.json"))
	if err != nil {
		return LaunchIsolationWarnOnly
	}
	var cfg launchIsolationConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return LaunchIsolationWarnOnly
	}
	switch LaunchIsolationMode(cfg.LaunchIsolation) {
	case LaunchIsolationEnforce:
		return LaunchIsolationEnforce
	case LaunchIsolationAuto:
		return LaunchIsolationAuto
	default:
		return LaunchIsolationWarnOnly
	}
}

// resolveIsolationFlags combines the .wipnote/config.json launch_isolation mode
// with the WIPNOTE_ENFORCE_ISOLATION env var to produce the two effective plan
// inputs. The env var is preserved for backward compatibility and ORs with the
// config: setting it implies at least enforce, never weakens an "auto" config.
//
// canonicalRoot must be the canonical main repository root, never a linked
// worktree. This ensures the project-level launch_isolation config is read
// from the canonical location.
//
//   - enforce = (mode == enforce || mode == auto) || envEnforce
//   - autoWorktree = (mode == auto)
func resolveIsolationFlags(canonicalRoot string) (enforce, autoWorktree bool) {
	m := readLaunchIsolationMode(canonicalRoot)
	envEnforce := os.Getenv("WIPNOTE_ENFORCE_ISOLATION") == "true"
	enforce = m == LaunchIsolationEnforce || m == LaunchIsolationAuto || envEnforce
	autoWorktree = m == LaunchIsolationAuto
	return enforce, autoWorktree
}

// adhocBranchName returns a deterministic-per-call slug for an auto-mode worktree
// when no work item is available. PlanLaunch must stay pure, so the clock is read
// here in the caller, not inside PlanLaunch. Format: "adhoc-<UTC YYYYMMDD-HHMMSS>".
func adhocBranchName(now time.Time) string {
	return fmt.Sprintf("adhoc-%s", now.UTC().Format("20060102-150405"))
}
