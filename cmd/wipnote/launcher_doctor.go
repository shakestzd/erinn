package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// launcherCmd returns the "wipnote launcher" parent command.
func launcherCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "launcher",
		Short: "Launcher diagnostics and migration tooling",
	}
	cmd.AddCommand(launcherDoctorCmd())
	cmd.AddCommand(launcherGitLockCmd())
	return cmd
}

// launcherDoctorCmd returns "wipnote launcher doctor".
func launcherDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose launcher/worktree health and print non-destructive remediation steps",
		Long: `Checks git state, managed worktrees, and session-family metadata.
Prints non-destructive remediation guidance. Does NOT auto-mutate anything.

Delegates to:
  wipnote cleanup orphan-sessions  (orphan-session reporting)
  wipnote reconcile                (session-exit reconciliation)

Rollout gate: host-production stays warn-only until doctor checks pass
and the operator opts in via WIPNOTE_ENFORCE_ISOLATION=true.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := doctorFindRepoRoot()
			if err != nil {
				return fmt.Errorf("could not locate git repository: %w", err)
			}
			report := runDoctorReport(repoRoot)
			fmt.Fprint(cmd.OutOrStdout(), report)
			return nil
		},
	}
}

// findRepoRoot returns the git repository root from the current directory.
func doctorFindRepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runDoctorReport is the pure diagnostic core — takes a repoRoot and
// returns the full doctor report as a string. Exported for tests.
func runDoctorReport(repoRoot string) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "wipnote launcher doctor — %s\n\n", repoRoot)
	reportGitState(&b, repoRoot)
	reportWorktrees(&b, repoRoot)
	reportGitLockState(&b, repoRoot)
	reportSessionDivergence(&b, repoRoot)
	reportRolloutGate(&b)
	var sb strings.Builder
	reportMainWorktreeIsolation(&sb, repoRoot)
	b.WriteString(sb.String())
	fmt.Fprintln(&b, "--- delegated checks ---")
	fmt.Fprintln(&b, "  orphan sessions: run `wipnote cleanup orphan-sessions` to list/remove")
	fmt.Fprintln(&b, "  session reconcile: run `wipnote reconcile` to auto-commit artifacts and report drift")
	return b.String()
}

// reportGitState writes main branch health (dirty, ahead/behind) to b.
func reportGitState(b *bytes.Buffer, repoRoot string) {
	fmt.Fprintln(b, "--- git state ---")
	branch := doctorCurrentBranch(repoRoot)
	fmt.Fprintf(b, "  branch: %s\n", branch)
	porcelain, _ := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output()
	if strings.TrimSpace(string(porcelain)) != "" {
		fmt.Fprintln(b, "  status: DIRTY — uncommitted changes detected")
		fmt.Fprintln(b, "  remediation: commit or stash changes, or use a managed worktree to isolate mutations")
	} else {
		fmt.Fprintln(b, "  status: clean")
	}
	if ab := doctorAheadBehind(repoRoot, branch); ab != "" {
		fmt.Fprintf(b, "  origin: %s\n", ab)
	}
	fmt.Fprintln(b)
}

// reportWorktrees lists managed worktrees and flags stale ones.
func reportWorktrees(b *bytes.Buffer, repoRoot string) {
	fmt.Fprintln(b, "--- managed worktrees ---")
	worktrees := doctorListWorktrees(repoRoot)
	if len(worktrees) == 0 {
		fmt.Fprintln(b, "  none")
		fmt.Fprintln(b)
		return
	}
	staleCount := 0
	for _, wt := range worktrees {
		if wt.Stale {
			staleCount++
			fmt.Fprintf(b, "  STALE  %s (directory removed)\n", wt.Path)
		} else {
			fmt.Fprintf(b, "  OK     %s (branch: %s)\n", wt.Path, wt.Branch)
		}
	}
	if staleCount > 0 {
		fmt.Fprintf(b, "  remediation: run `git -C %s worktree prune` to remove %d stale admin entries\n",
			repoRoot, staleCount)
	}
	fmt.Fprintln(b)
}

// reportSessionDivergence reports session-family and canonical-root health.
func reportSessionDivergence(b *bytes.Buffer, repoRoot string) {
	fmt.Fprintln(b, "--- session / canonical-root health ---")
	familyMap := readDoctorFamilyMap(repoRoot)
	if len(familyMap) == 0 {
		fmt.Fprintln(b, "  no session-family data found (new install or pre-slice-4 sessions)")
	} else {
		legacy := 0
		for _, fid := range familyMap {
			if fid == "" {
				legacy++
			}
		}
		fmt.Fprintf(b, "  %d sessions registered; %d legacy (no family-id)\n", len(familyMap), legacy)
		if legacy > 0 {
			fmt.Fprintln(b, "  legacy sessions remain VISIBLE in the dashboard — no action required")
		}
	}
	fmt.Fprintln(b)
}

// reportRolloutGate prints the current rollout gate status.
func reportRolloutGate(b *bytes.Buffer) {
	fmt.Fprintln(b, "--- rollout gate ---")
	if os.Getenv("WIPNOTE_ENFORCE_ISOLATION") == "true" {
		fmt.Fprintln(b, "  WIPNOTE_ENFORCE_ISOLATION=true — host enforcement ACTIVE")
		fmt.Fprintln(b, "  isolation: managed-worktree is required for all launchers on this host")
	} else {
		fmt.Fprintln(b, "  host profile: warn-only (default)")
		fmt.Fprintln(b, "  to advance to enforced mode, verify all checks above pass, then set:")
		fmt.Fprintln(b, "    export WIPNOTE_ENFORCE_ISOLATION=true")
		fmt.Fprintln(b, "  see docs/runbook/launcher-isolation.md for the full migration guide")
	}
	fmt.Fprintln(b)
}

// doctorSessionLabel returns the display label for a session given the family map.
// Returns "legacy" for sessions without a family-id entry.
func doctorSessionLabel(sessionID string, familyMap map[string]string) string {
	fid, ok := familyMap[sessionID]
	if !ok || fid == "" {
		return "legacy"
	}
	return fid
}

// worktreeEntry describes one git worktree entry for doctor reporting.
type worktreeEntry struct {
	Path   string
	Branch string
	Stale  bool
}

// doctorListWorktrees returns managed wipnote worktrees, flagging stale ones.
func doctorListWorktrees(repoRoot string) []worktreeEntry {
	managedDir := filepath.Join(repoRoot, ".claude", "worktrees")
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var entries []worktreeEntry
	var cur worktreeEntry
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur = worktreeEntry{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "":
			if cur.Path != "" && strings.HasPrefix(cur.Path, managedDir) {
				if _, statErr := os.Stat(cur.Path); os.IsNotExist(statErr) {
					cur.Stale = true
				}
				entries = append(entries, cur)
			}
			cur = worktreeEntry{}
		}
	}
	return entries
}

// doctorCurrentBranch returns the current branch name.
func doctorCurrentBranch(repoRoot string) string {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "(unknown)"
	}
	return strings.TrimSpace(string(out))
}

// doctorAheadBehind returns an ahead/behind summary vs origin. Returns "" when
// no remote tracking branch is configured.
func doctorAheadBehind(repoRoot, branch string) string {
	upstream := "origin/" + branch
	out, err := exec.Command("git", "-C", repoRoot,
		"rev-list", "--left-right", "--count", "HEAD..."+upstream).Output()
	if err != nil {
		return ""
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		return ""
	}
	if parts[0] == "0" && parts[1] == "0" {
		return "up to date"
	}
	return fmt.Sprintf("ahead %s, behind %s vs %s", parts[0], parts[1], upstream)
}

// readDoctorFamilyMap reads session-families.json from .wipnote/.
// Returns nil on any error (file not found is normal for new installs).
func readDoctorFamilyMap(repoRoot string) map[string]string {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".wipnote", "session-families.json"))
	if err != nil {
		return nil
	}
	type familyFile struct {
		Families map[string]string `json:"families"`
	}
	var ff familyFile
	if err := json.Unmarshal(data, &ff); err != nil {
		return nil
	}
	return ff.Families
}
