package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

// hostPathPattern matches absolute paths that are specific to a developer's machine:
//   - /Users/<name>/        — macOS home directories
//   - /home/<name>/         — Linux home directories
//   - /workspaces/<name>/   — GitHub Codespaces per-user workspace paths
//   - /private/var/folders/ — macOS temp directory (always machine-specific)
//
// /home/runner/ (GitHub Actions CI user) is filtered out after matching via ciAllowPattern.
var hostPathPattern = regexp.MustCompile(
	`/Users/[^/\s]+/` +
		`|/home/[^/\s]+/` +
		`|/workspaces/[^/\s]+/` +
		`|/private/var/folders/`,
)

// ciAllowPattern matches paths that should be excluded from violations even though
// they match hostPathPattern — specifically /home/runner/ used by GitHub Actions CI.
var ciAllowPattern = regexp.MustCompile(`^/home/runner/`)

// hostPathViolation records a single match found during scanning.
type hostPathViolation struct {
	file    string
	line    int
	matched string
}

func (v hostPathViolation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.file, v.line, v.matched)
}

// validateDescriptionForHostPaths checks description text for host-local absolute
// path patterns before writing it to a work-item file. Call this at creation time
// and on set-description so that violations are caught immediately rather than at
// the pre-commit gate (which runs the full test suite first — ~4.5 min wasted).
//
// When allowHostPaths is true the check is skipped entirely (--allow-host-paths
// bypass flag). Returns a descriptive error on violation.
func validateDescriptionForHostPaths(description string, allowHostPaths bool) error {
	if allowHostPaths || description == "" {
		return nil
	}
	matches := hostPathPattern.FindAllString(description, -1)
	var violations []string
	for _, m := range matches {
		if ciAllowPattern.MatchString(m) {
			continue
		}
		violations = append(violations, m)
	}
	if len(violations) == 0 {
		return nil
	}
	return fmt.Errorf(
		"description contains host-local absolute path(s): %s\n"+
			"  Replace with a relative path or basename.\n"+
			"  To bypass this check, re-run with --allow-host-paths.",
		strings.Join(violations, ", "),
	)
}

// checkHostPathsCmd returns the cobra sub-command wired into `htmlgraph check`.
func checkHostPathsCmd() *cobra.Command {
	var stagedOnly bool

	cmd := &cobra.Command{
		Use:   "host-paths",
		Short: "Scan committed artifacts for host-local absolute paths",
		Long: `Scan .htmlgraph/ and .claude/ for host-local absolute paths that must
not be committed (e.g. /Users/alice/, /home/bob/, /workspaces/charlie/).

Files listed in scripts/host-paths-allowlist.txt are skipped.
The binary .htmlgraph/htmlgraph.db and .claude/settings.local.json are always skipped.

Exit code 0 — no violations; exit code 1 — violations found.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := projectRoot()
			if err != nil {
				return fmt.Errorf("resolve git root: %w", err)
			}

			allowlist, err := loadHostPathAllowlist(filepath.Join(repoRoot, "scripts", "host-paths-allowlist.txt"))
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("load allowlist: %w", err)
			}

			var files []string
			if stagedOnly {
				files, err = stagedScopeFiles(repoRoot)
			} else {
				files, err = fullScopeFiles(repoRoot)
			}
			if err != nil {
				return fmt.Errorf("collect files: %w", err)
			}

			violations, scanned, err := scanHostPathFiles(repoRoot, files, allowlist)
			if err != nil {
				return err
			}

			if len(violations) > 0 {
				for _, v := range violations {
					fmt.Println(v)
				}
				fmt.Printf("\nFAIL: %d host-local path violation(s) in %d file(s) scanned.\n", len(violations), scanned)
				fmt.Println("      Commit only project-relative or portable paths.")
				fmt.Println("      To allowlist a file, add its repo-relative path to scripts/host-paths-allowlist.txt")
				return fmt.Errorf("host-local path violations found")
			}

			fmt.Printf("OK — %d file(s) scanned, no host-local path violations found.\n", scanned)
			return nil
		},
	}

	cmd.Flags().BoolVar(&stagedOnly, "staged", false, "Scan only git-staged files (for pre-commit use)")
	return cmd
}

// loadHostPathAllowlist reads an allowlist file and returns a set of repo-relative paths.
// Lines starting with # and blank lines are ignored.
func loadHostPathAllowlist(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	allowlist := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		allowlist[line] = true
	}
	return allowlist, scanner.Err()
}

// fullScopeFiles collects all scannable files under .htmlgraph/ and .claude/.
// Skips:
//   - .htmlgraph/htmlgraph.db (binary)
//   - .claude/settings.local.json (ephemeral)
//   - .claude/worktrees/ entirely — linked-worktree .git files legitimately
//     carry absolute gitdir: paths by design (git requires them). Scanning
//     them produces noisy false positives on every developer's machine.
func fullScopeFiles(repoRoot string) ([]string, error) {
	var files []string
	for _, dir := range []string{".htmlgraph", ".claude"} {
		dirPath := filepath.Join(repoRoot, dir)
		if _, err := os.Stat(dirPath); os.IsNotExist(err) {
			continue
		}
		err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			rel, relErr := filepath.Rel(repoRoot, path)
			if relErr == nil && strings.HasPrefix(rel, filepath.Join(".claude", "worktrees")+string(filepath.Separator)) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			base := filepath.Base(path)
			if base == storage.DBFileName || base == "settings.local.json" {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// stagedScopeFiles returns the subset of git-staged files that fall within scan scope.
func stagedScopeFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "diff", "--cached", "--name-only", "--diff-filter=ACMR").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	}

	var files []string
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel == "" {
			continue
		}
		if !strings.HasPrefix(rel, ".htmlgraph/") && !strings.HasPrefix(rel, ".claude/") {
			continue
		}
		base := filepath.Base(rel)
		if base == storage.DBFileName || base == "settings.local.json" {
			continue
		}
		abs := filepath.Join(repoRoot, rel)
		if _, err := os.Stat(abs); err == nil {
			files = append(files, abs)
		}
	}
	return files, nil
}

// scanHostPathFiles scans each file for host-local path violations, skipping allowlisted entries.
// Returns the list of violations and the number of files actually scanned.
func scanHostPathFiles(repoRoot string, files []string, allowlist map[string]bool) ([]hostPathViolation, int, error) {
	var violations []hostPathViolation
	scanned := 0

	for _, absPath := range files {
		relPath, err := filepath.Rel(repoRoot, absPath)
		if err != nil {
			relPath = absPath
		}

		if allowlist[relPath] {
			continue
		}

		scanned++
		vs, err := scanFileForHostPaths(absPath, relPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", relPath, err)
			continue
		}
		violations = append(violations, vs...)
	}

	return violations, scanned, nil
}

// scanFileForHostPaths scans a single file for host-local path patterns.
func scanFileForHostPaths(absPath, displayPath string) ([]hostPathViolation, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var violations []hostPathViolation
	scanner := bufio.NewScanner(f)
	// Increase buffer for long HTML lines
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		matches := hostPathPattern.FindAllString(line, -1)
		for _, m := range matches {
			// /home/runner/ is the GitHub Actions CI user — not a developer path.
			if ciAllowPattern.MatchString(m) {
				continue
			}
			violations = append(violations, hostPathViolation{
				file:    displayPath,
				line:    lineNum,
				matched: m,
			})
		}
	}
	return violations, scanner.Err()
}
