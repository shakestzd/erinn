package main

// git_lock_repair.go — safe stale Git lock diagnostics and repair (feat-9704121b)
//
// SAFETY RULES (non-negotiable):
//  1. git index.lock is an EMPTY FILE with no PID inside. Staleness CANNOT be
//     read from lock contents. Infer staleness from (a) lock file age > threshold
//     AND (b) no live git process owning the repo. This detection is inherently racy.
//  2. DRY-RUN IS THE DEFAULT. reportGitLockState* never deletes anything.
//  3. --fix is opt-in; repairGitLocksWith is only called when the user passes --fix.
//  4. A FINAL LIVENESS RE-CHECK runs immediately before os.Remove — callers supply
//     two separate liveness funcs: initialCheck and finalRecheck.
//  5. NEVER delete when any live git writer is detected, regardless of lock age.
//  6. An age threshold (default 10 min) must be exceeded before a lock is eligible.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// defaultMaxLockAge is the minimum lock age before a lock is considered stale.
// Configurable via --max-age on the git-lock subcommand.
const defaultMaxLockAge = 10 * time.Minute

// knownLockNames lists the git lock file names to scan for.
var knownLockNames = []string{
	"index.lock",
	"HEAD.lock",
	"config.lock",
	"packed-refs.lock",
}

// lockFileInfo describes one detected git lock file.
type lockFileInfo struct {
	Name    string
	Path    string
	ModTime time.Time
}

// detectGitLocks returns lock files found in gitDir (the .git directory).
// It only checks known lock filenames — it does not recurse.
func detectGitLocks(gitDir string) []lockFileInfo {
	var found []lockFileInfo
	for _, name := range knownLockNames {
		p := filepath.Join(gitDir, name)
		fi, err := os.Stat(p)
		if err != nil {
			continue // not present
		}
		found = append(found, lockFileInfo{
			Name:    name,
			Path:    p,
			ModTime: fi.ModTime(),
		})
	}
	return found
}

// hasLiveGitWriter returns true when any process whose command-line arguments
// or working directory reference worktreeOrGitDir appears to be a live git
// operation. It inspects /proc/<pid>/cmdline and /proc/<pid>/cwd on Linux.
// False positives (reporting "live" when no writer exists) are SAFE — the
// lock will simply be left in place. False negatives are DANGEROUS, so we
// are deliberately conservative: any ambiguous parse returns true.
func hasLiveGitWriter(worktreeRoot string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// /proc unavailable — conservatively assume a writer exists
		return true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only numeric entries are PID directories
		pid := e.Name()
		isNumeric := true
		for _, ch := range pid {
			if ch < '0' || ch > '9' {
				isNumeric = false
				break
			}
		}
		if !isNumeric {
			continue
		}
		// Check cmdline for "git" invocations referencing the repo
		cmdline, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if err != nil {
			continue
		}
		args := strings.ReplaceAll(string(cmdline), "\x00", " ")
		if !strings.Contains(args, "git") {
			continue
		}
		// Check if the process cwd is inside the repo
		cwd, err := os.Readlink(filepath.Join("/proc", pid, "cwd"))
		if err == nil && strings.HasPrefix(cwd, worktreeRoot) {
			return true
		}
		// Also check if the repo root appears in the cmdline args
		if strings.Contains(args, worktreeRoot) {
			return true
		}
	}
	return false
}

// reportGitLockState writes the git lock diagnostics section to b.
// It is a pure read-only reporter — it NEVER deletes anything.
// Called from runDoctorReport as one section among several.
func reportGitLockState(b *bytes.Buffer, repoRoot string) {
	gitDir := resolveGitDir(repoRoot)
	now := time.Now()
	section := reportGitLockStateWith(repoRoot, gitDir, now, hasLiveGitWriter, defaultMaxLockAge)
	fmt.Fprint(b, section)
}

// reportGitLockStateWith is the testable core of reportGitLockState.
// It accepts injectable seams for now(), a liveness check, and the age threshold.
// It NEVER deletes anything — it is a pure diagnostic function.
func reportGitLockStateWith(
	repoRoot, gitDir string,
	now time.Time,
	liveCheck func(string) bool,
	maxAge time.Duration,
) string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "--- git lock files ---")

	locks := detectGitLocks(gitDir)
	if len(locks) == 0 {
		fmt.Fprintln(&b, "  no lock files detected")
		fmt.Fprintln(&b)
		return b.String()
	}

	liveWriter := liveCheck(repoRoot)
	for _, lf := range locks {
		age := now.Sub(lf.ModTime)
		stale := age >= maxAge && !liveWriter
		verdict := "active"
		if stale {
			verdict = "stale"
		}
		fmt.Fprintf(&b, "  %s  age=%s  verdict=%s\n", lf.Name, age.Round(time.Second), verdict)
	}

	if liveWriter {
		fmt.Fprintln(&b, "  live git writer detected — locks are active (safe to leave)")
	} else {
		eligibleCount := 0
		for _, lf := range locks {
			if now.Sub(lf.ModTime) >= maxAge {
				eligibleCount++
			}
		}
		if eligibleCount > 0 {
			fmt.Fprintf(&b, "  %d stale lock(s) eligible for removal — run `wipnote launcher git-lock --fix` to remove\n", eligibleCount)
		} else {
			fmt.Fprintln(&b, "  lock(s) present but below age threshold — not yet eligible for removal")
		}
	}
	fmt.Fprintln(&b)
	return b.String()
}

// repairGitLocksWith is the testable core of the --fix repair path.
// It accepts injectable seams for now(), an initial liveness check, a final
// pre-unlink re-check, and the age threshold.
//
// SAFETY: for each eligible lock:
//  1. initialCheck is called; if live writer found → skip (safe, no delete)
//  2. age < maxAge → skip
//  3. finalRecheck is called immediately before os.Remove; if live writer
//     appeared since step 1 → abort (safe, no delete)
//
// Returns: (repaired count, skipped count, first error encountered).
func repairGitLocksWith(
	gitDir string,
	now time.Time,
	initialCheck func(string) bool,
	finalRecheck func(string) bool,
	maxAge time.Duration,
) (int, int, error) {
	locks := detectGitLocks(gitDir)
	repaired, skipped := 0, 0

	// worktreeRoot for liveness checks — parent of .git
	worktreeRoot := filepath.Dir(gitDir)

	for _, lf := range locks {
		age := now.Sub(lf.ModTime)

		// Gate 1: initial liveness check
		if initialCheck(worktreeRoot) {
			skipped++
			continue
		}
		// Gate 2: age threshold
		if age < maxAge {
			skipped++
			continue
		}
		// Gate 3: FINAL liveness re-check immediately before unlink (SAFETY RULE 4)
		if finalRecheck(worktreeRoot) {
			skipped++
			continue
		}
		// All gates passed — safe to remove
		if err := os.Remove(lf.Path); err != nil {
			return repaired, skipped, fmt.Errorf("remove %s: %w", lf.Path, err)
		}
		repaired++
	}
	return repaired, skipped, nil
}

// resolveGitDir returns the .git directory for repoRoot.
// Falls back to repoRoot/.git when git is not available.
func resolveGitDir(repoRoot string) string {
	// Fast path: standard single-worktree layout
	candidate := filepath.Join(repoRoot, ".git")
	if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
		return candidate
	}
	// Fallback: return the candidate even if missing (detectGitLocks handles absent dirs gracefully)
	return candidate
}

// launcherGitLockCmd returns the "wipnote launcher git-lock" subcommand.
// Default (no --fix): dry-run report of what would be removed.
// With --fix: performs the repair with mandatory age-threshold and final re-check.
func launcherGitLockCmd() *cobra.Command {
	var fixMode bool
	var maxAgeMinutes int

	cmd := &cobra.Command{
		Use:   "git-lock",
		Short: "Diagnose (and optionally repair) stale git lock files",
		Long: `Scans for stale git lock files (index.lock, config.lock, HEAD.lock, packed-refs.lock)
and reports their age and whether a live git writer was detected.

Default (dry-run): prints what it WOULD remove, modifies nothing.
--fix: removes stale locks only when age > threshold AND no live git writer is found.
       A mandatory final liveness re-check runs immediately before each unlink.

SAFETY: git lock files have no owner PID inside. Staleness is inferred from
lock age plus the absence of live git processes — this is inherently racy.
--fix is never the default. False "alive" detections are always safe.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := doctorFindRepoRoot()
			if err != nil {
				return fmt.Errorf("could not locate git repository: %w", err)
			}
			gitDir := resolveGitDir(repoRoot)
			maxAge := time.Duration(maxAgeMinutes) * time.Minute
			now := time.Now()

			if !fixMode {
				// Dry-run: report only
				report := reportGitLockStateWith(repoRoot, gitDir, now, hasLiveGitWriter, maxAge)
				fmt.Fprint(cmd.OutOrStdout(), report)
				return nil
			}

			// --fix path: repair with dual liveness check
			repaired, skipped, err := repairGitLocksWith(gitDir, now, hasLiveGitWriter, hasLiveGitWriter, maxAge)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "git-lock repair: %d removed, %d skipped\n", repaired, skipped)
			return nil
		},
	}

	cmd.Flags().BoolVar(&fixMode, "fix", false, "Remove eligible stale locks (requires age > threshold AND no live git writer)")
	cmd.Flags().IntVar(&maxAgeMinutes, "max-age", int(defaultMaxLockAge/time.Minute), "Minimum lock age in minutes before a lock is eligible for removal")
	return cmd
}
