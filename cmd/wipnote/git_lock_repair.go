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
	"os/exec"
	"path/filepath"
	"strconv"
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

// sharedLockNames are repository-COMMON locks (live in the git common dir, not a
// per-worktree admin dir). They can be held by ANY linked worktree, so a local
// worktreeRoot liveness scan cannot prove they are stale — they are always
// report-only and never auto-removed by --fix, regardless of which dir they were
// scanned from (roborev #3711: in the main worktree --git-dir == --git-common-dir,
// so scan order alone would mis-mark them as per-worktree/removable).
var sharedLockNames = map[string]bool{
	"config.lock":      true,
	"packed-refs.lock": true,
}

func isSharedLockName(name string) bool { return sharedLockNames[name] }

// lockFileInfo describes one detected git lock file.
type lockFileInfo struct {
	Name    string
	Path    string
	ModTime time.Time
	// Shared is true when the lock lives in a git dir OTHER than the first
	// (primary per-worktree) dir passed to detectGitLocks — i.e. the git common
	// dir. Shared locks may be owned by the main worktree or another linked
	// worktree, whose liveness the local worktreeRoot scan cannot see, so --fix
	// must NOT delete them (roborev #3659). They are still reported.
	Shared bool
}

// detectGitLocks returns the known lock files found across gitDirs. It checks
// only known lock filenames and does not recurse. Multiple dirs are scanned
// because in a linked worktree the per-worktree admin dir (--git-dir) holds
// index.lock/HEAD.lock while shared locks like config.lock/packed-refs.lock live
// under the git common dir (--git-common-dir); both must be inspected (roborev
// #3641). The FIRST dir is treated as the primary per-worktree dir; locks found
// only in later dirs are marked Shared. Results are de-duplicated by absolute
// path so overlapping dirs (the main worktree, where the two dirs coincide)
// don't double-count.
func detectGitLocks(gitDirs ...string) []lockFileInfo {
	var found []lockFileInfo
	seen := make(map[string]bool)
	for i, gitDir := range gitDirs {
		for _, name := range knownLockNames {
			p := filepath.Join(gitDir, name)
			abs := p
			if a, err := filepath.Abs(p); err == nil {
				abs = a
			}
			if seen[abs] {
				continue
			}
			fi, err := os.Stat(p)
			if err != nil {
				continue // not present
			}
			seen[abs] = true
			found = append(found, lockFileInfo{
				Name:    name,
				Path:    p,
				ModTime: fi.ModTime(),
				// Shared when found in a non-primary (common) dir OR when it is a
				// repository-common lock name — both cases may be owned by another
				// worktree (roborev #3711).
				Shared: i > 0 || isSharedLockName(name),
			})
		}
	}
	return found
}

// hasLiveGitWriter returns true when a live `git` process appears to be
// operating on worktreeRoot. It inspects /proc/<pid>/cmdline and
// /proc/<pid>/cwd on Linux.
//
// A process counts only when its argv[0] basename is `git` (or a `git-*`
// helper) AND it references worktreeRoot via cwd or an argument. The current
// process is excluded: `wipnote launcher git-lock --fix` runs inside the repo
// and its cmdline contains the substring "git", so a naive substring match made
// the repair self-block — never removing the very lock it was asked to clean
// (roborev #3703). Matching by argv[0] basename + excluding self fixes that.
//
// False positives (reporting "live" when none exists) remain SAFE — the lock is
// left in place; false negatives are DANGEROUS, so any ambiguous parse that
// still looks like a real git process returns true.
func hasLiveGitWriter(worktreeRoot string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// /proc unavailable — conservatively assume a writer exists
		return true
	}
	selfPID := strconv.Itoa(os.Getpid())
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
		if !isNumeric || pid == selfPID {
			continue // skip non-PIDs and the repair process itself
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if err != nil {
			continue
		}
		argv := splitNulArgv(cmdline)
		if !isGitProcessArgv(argv) {
			continue
		}
		// Real git process: does it operate on THIS repo (cwd or an arg)?
		if cwd, err := os.Readlink(filepath.Join("/proc", pid, "cwd")); err == nil &&
			strings.HasPrefix(cwd, worktreeRoot) {
			return true
		}
		for _, a := range argv {
			if strings.Contains(a, worktreeRoot) {
				return true
			}
		}
	}
	return false
}

// splitNulArgv splits a /proc/<pid>/cmdline blob (NUL-separated argv) into
// non-empty arguments.
func splitNulArgv(cmdline []byte) []string {
	var argv []string
	for _, part := range strings.Split(string(cmdline), "\x00") {
		if part != "" {
			argv = append(argv, part)
		}
	}
	return argv
}

// isGitProcessArgv reports whether argv[0] is the git binary (basename "git")
// or a git helper ("git-*"). This deliberately does NOT match `wipnote`, whose
// subcommand path "launcher git-lock" merely contains the substring "git".
func isGitProcessArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	base := filepath.Base(argv[0])
	return base == "git" || strings.HasPrefix(base, "git-")
}

// reportGitLockState writes the git lock diagnostics section to b.
// It is a pure read-only reporter — it NEVER deletes anything.
// Called from runDoctorReport as one section among several.
func reportGitLockState(b *bytes.Buffer, repoRoot string) {
	gitDirs := resolveGitLockDirs(repoRoot)
	now := time.Now()
	section := reportGitLockStateWith(repoRoot, gitDirs, now, hasLiveGitWriter, defaultMaxLockAge)
	fmt.Fprint(b, section)
}

// reportGitLockStateWith is the testable core of reportGitLockState.
// It accepts injectable seams for now(), a liveness check, and the age threshold.
// It NEVER deletes anything — it is a pure diagnostic function. gitDirs is the
// set of git directories to scan (per-worktree git dir plus the common dir).
func reportGitLockStateWith(
	repoRoot string, gitDirs []string,
	now time.Time,
	liveCheck func(string) bool,
	maxAge time.Duration,
) string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "--- git lock files ---")

	locks := detectGitLocks(gitDirs...)
	if len(locks) == 0 {
		fmt.Fprintln(&b, "  no lock files detected")
		fmt.Fprintln(&b)
		return b.String()
	}

	liveWriter := liveCheck(repoRoot)
	sharedSeen := false
	for _, lf := range locks {
		age := now.Sub(lf.ModTime)
		stale := age >= maxAge && !liveWriter
		verdict := "active"
		if stale {
			verdict = "stale"
		}
		scope := ""
		if lf.Shared {
			scope = "  scope=shared(common-dir)"
			sharedSeen = true
		}
		fmt.Fprintf(&b, "  %s  age=%s  verdict=%s%s\n", lf.Name, age.Round(time.Second), verdict, scope)
	}

	if liveWriter {
		fmt.Fprintln(&b, "  live git writer detected — locks are active (safe to leave)")
	} else {
		// Only NON-shared (per-worktree) locks are eligible for --fix; shared
		// common-dir locks are never auto-removed (roborev #3659).
		eligibleCount := 0
		for _, lf := range locks {
			if !lf.Shared && now.Sub(lf.ModTime) >= maxAge {
				eligibleCount++
			}
		}
		if eligibleCount > 0 {
			fmt.Fprintf(&b, "  %d stale lock(s) eligible for removal — run `wipnote launcher git-lock --fix` to remove\n", eligibleCount)
		} else {
			fmt.Fprintln(&b, "  lock(s) present but below age threshold — not yet eligible for removal")
		}
	}
	if sharedSeen {
		fmt.Fprintln(&b, "  shared (common-dir) locks are reported only — not auto-removed, as they may be")
		fmt.Fprintln(&b, "  held by the main worktree or another linked worktree; repair them from the")
		fmt.Fprintln(&b, "  main worktree after verifying no git process is running anywhere on the repo")
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
	gitDirs []string, worktreeRoot string,
	now time.Time,
	initialCheck func(string) bool,
	finalRecheck func(string) bool,
	maxAge time.Duration,
) (int, int, error) {
	locks := detectGitLocks(gitDirs...)
	repaired, skipped := 0, 0

	// worktreeRoot anchors the liveness scan to the working tree. It is passed
	// explicitly (not derived from gitDirs) because in a linked worktree the
	// per-worktree git dir is .git/worktrees/<name>, whose parent is NOT the
	// working tree root.

	for _, lf := range locks {
		age := now.Sub(lf.ModTime)

		// Gate 0: shared (common-dir) locks are NEVER auto-removed. They may be
		// owned by the main worktree or another linked worktree, whose live
		// writer the worktreeRoot-anchored liveness scan cannot observe; deleting
		// one could clobber an active operation in a worktree we can't see
		// (roborev #3659). They are reported by the dry-run path but left intact.
		if lf.Shared {
			skipped++
			continue
		}
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
		// Gate 4: re-stat immediately before unlink. Eligibility was decided from
		// the initial snapshot; if git replaced the lock since then, the file on
		// disk may now be FRESH (younger than maxAge) — deleting it would clobber
		// a live op. Re-confirm it still exists and is still old enough; skip
		// otherwise (roborev #3713 TOCTOU).
		if fi, statErr := os.Stat(lf.Path); statErr != nil || now.Sub(fi.ModTime()) < maxAge {
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

// resolveGitDir returns the absolute git directory for repoRoot, where the
// per-worktree lock files (index.lock, HEAD.lock) actually live. It uses
// `git rev-parse --absolute-git-dir`, which resolves every layout correctly:
// a standard `.git` directory, a linked-worktree `.git` FILE containing a
// `gitdir:` pointer (the lock then lives under `.git/worktrees/<name>/`), and
// submodules (where `.git` is also a file). If git is unavailable it falls back
// to parsing the `.git` file's `gitdir:` line directly, and finally to
// repoRoot/.git so detection degrades gracefully (detectGitLocks tolerates an
// absent directory).
func resolveGitDir(repoRoot string) string {
	if out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--absolute-git-dir").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
			return d
		}
	}
	candidate := filepath.Join(repoRoot, ".git")
	fi, err := os.Stat(candidate)
	if err != nil || fi.IsDir() {
		return candidate
	}
	// `.git` is a file (linked worktree / submodule): parse its gitdir pointer.
	data, err := os.ReadFile(candidate)
	if err != nil {
		return candidate
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:"); ok {
			gd := strings.TrimSpace(rest)
			if !filepath.IsAbs(gd) {
				gd = filepath.Join(repoRoot, gd)
			}
			return filepath.Clean(gd)
		}
	}
	return candidate
}

// resolveGitLockDirs returns the set of directories that can hold git lock files
// for repoRoot, de-duplicated. In the main worktree the per-worktree git dir and
// the git common dir coincide (one entry). In a LINKED worktree they differ:
// --git-dir (from resolveGitDir) holds index.lock/HEAD.lock while --git-common-dir
// holds shared locks like config.lock/packed-refs.lock — both must be scanned
// (roborev #3641). The common dir is best-effort: if git can't report it we just
// return the primary git dir.
func resolveGitLockDirs(repoRoot string) []string {
	dirs := []string{resolveGitDir(repoRoot)}
	if out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-common-dir").Output(); err == nil {
		common := strings.TrimSpace(string(out))
		if common != "" {
			if !filepath.IsAbs(common) {
				common = filepath.Join(repoRoot, common)
			}
			common = filepath.Clean(common)
			if common != dirs[0] {
				dirs = append(dirs, common)
			}
		}
	}
	return dirs
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
			// The safety contract requires a positive age threshold; a zero or
			// negative --max-age would make every non-shared lock instantly
			// eligible for removal (roborev #3711).
			if maxAgeMinutes <= 0 {
				return fmt.Errorf("--max-age must be a positive number of minutes (got %d)", maxAgeMinutes)
			}
			repoRoot, err := doctorFindRepoRoot()
			if err != nil {
				return fmt.Errorf("could not locate git repository: %w", err)
			}
			gitDirs := resolveGitLockDirs(repoRoot)
			maxAge := time.Duration(maxAgeMinutes) * time.Minute
			now := time.Now()

			if !fixMode {
				// Dry-run: report only
				report := reportGitLockStateWith(repoRoot, gitDirs, now, hasLiveGitWriter, maxAge)
				fmt.Fprint(cmd.OutOrStdout(), report)
				return nil
			}

			// --fix path: repair with dual liveness check
			repaired, skipped, err := repairGitLocksWith(gitDirs, repoRoot, now, hasLiveGitWriter, hasLiveGitWriter, maxAge)
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
