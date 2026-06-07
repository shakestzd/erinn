package paths

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// NormalizeToRepoRelative converts an absolute filesystem path into a
// repository-relative, forward-slash path so that captured artifacts (hook
// payloads, CloudEvents, work-item lineage) remain stable across worktrees,
// machines, and users.
//
// Contract:
//
//  1. If absPath is already relative, return (absPath, true) unchanged.
//  2. If repoRoot is empty, the canonical relativization anchor is
//     discovered:
//     a. resolveAnchor(filepath.Dir(absPath)) — finds the local worktree
//     toplevel via `git rev-parse --show-toplevel`. Linked worktrees
//     mirror the main repo structure, so the local toplevel is the
//     correct anchor: relativizing against it gives a path that is
//     stable across worktrees of the same repo.
//     b. If that fails, walk up from filepath.Dir(absPath) looking for
//     the nearest .wipnote/ directory and use it as anchor.
//     c. If both fail and absPath matches HostPathPattern, return
//     ("unresolved:"+absPath, true). Otherwise return (absPath, true).
//  3. filepath.EvalSymlinks is applied to both anchor and absPath before
//     Rel. EvalSymlinks failures (path does not exist on disk yet — common
//     in tool_input) fall back to the raw strings; this is not an error.
//  4. If filepath.Rel yields a result starting with "..", the file lies
//     outside the anchor; return ("unresolved:"+absPath, true).
//  5. The relative result is returned via filepath.ToSlash.
//
// The function never returns ok=false in the current implementation; the
// boolean is reserved so call sites can compile against a future stricter
// contract without churn.
//
// A process-scoped cache keyed by filepath.Dir(absPath) ensures that
// `git rev-parse` is invoked at most once per unique containing directory
// per process lifetime. This is critical because the path is wired into
// PreToolUse/PostToolUse hooks which fire on every tool call.
func NormalizeToRepoRelative(absPath, repoRoot string) (string, bool) {
	return NormalizeWithResolver(absPath, repoRoot, resolveWipnoteAnchor)
}

// ResolveWipnoteAnchorForDir is the exported form of resolveWipnoteAnchor.
// It finds the relativization anchor (local worktree toplevel) for the given
// directory. Exported so that hook handlers can use it as the production
// resolver argument to NormalizeWithResolver, enabling tests to substitute a
// stub without shelling to git.
//
// Returns "" when dir is not inside any wipnote-aware git repo.
func ResolveWipnoteAnchorForDir(dir string) string {
	return resolveWipnoteAnchor(dir)
}

// resolveWipnoteAnchor finds the relativization anchor for paths inside
// dir. The anchor is the local worktree's toplevel (for both main and
// linked worktrees) — verified to belong to a wipnote project either
// locally or via `--git-common-dir`. Returns "" when dir is not inside any
// wipnote-aware git repo.
//
// Why the worktree toplevel and not the main repo root:
//
//	A linked worktree at /wt mirrors the main repo's tree exactly, so a
//	file at /wt/cmd/main.go corresponds to cmd/main.go in lineage terms
//	— the same logical file that the main repo would call cmd/main.go.
//	Relativizing against the main repo root would yield "../wt/cmd/main.go"
//	which is not portable across worktree layouts.
func resolveWipnoteAnchor(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse",
		"--show-toplevel", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return ""
	}
	toplevel := strings.TrimSpace(lines[0])
	gitCommonDir := strings.TrimSpace(lines[1])
	if toplevel == "" {
		return ""
	}
	// Normalise git-common-dir to absolute so we can stat .wipnote/.
	if gitCommonDir == ".git" {
		// Main worktree: .git is sibling to toplevel.
		gitCommonDir = filepath.Join(toplevel, ".git")
	} else if !filepath.IsAbs(gitCommonDir) {
		gitCommonDir = filepath.Join(dir, gitCommonDir)
	}
	mainRepoRoot := filepath.Dir(filepath.Clean(gitCommonDir))
	// Accept the toplevel as anchor when either the toplevel itself or the
	// main repo root contains .wipnote/. The toplevel check covers the
	// trivial main-worktree case; the mainRepoRoot check covers linked
	// worktrees where .wipnote/ only exists in the main repo.
	if hasWipnoteDir(toplevel) || hasWipnoteDir(mainRepoRoot) {
		return toplevel
	}
	return ""
}

// hasWipnoteDir reports whether dir contains a .wipnote/ subdirectory.
func hasWipnoteDir(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, ".wipnote"))
	return err == nil && info.IsDir()
}

// MustNormalize calls NormalizeToRepoRelative and returns the normalised path
// on success or the original absPath on failure / edge cases. It never
// panics. Suitable for hot-path hook code where a normalisation glitch must
// not be allowed to abort capture.
func MustNormalize(absPath, repoRoot string) string {
	got, ok := NormalizeToRepoRelative(absPath, repoRoot)
	if !ok || got == "" {
		debugLog("MustNormalize: falling back to original %q", absPath)
		return absPath
	}
	return got
}

// NormalizeProjectDir normalizes an absolute project-root directory to a
// repo-relative path, applying the same outside-repo policy as
// NormalizeToRepoRelative but treating the argument as a directory rather
// than a file. This is necessary because NormalizeToRepoRelative resolves the
// anchor from filepath.Dir(absPath), which for a directory gives its PARENT —
// causing the git lookup to run in the wrong directory.
//
// Policy (mirrors NormalizeToRepoRelative):
//   - Already-relative input → returned unchanged with ok=true.
//   - Empty input → returned unchanged with ok=true.
//   - Local session (dir resolves to a wipnote-aware git repo) → returned as
//     the relative path from the repo root (typically "." for the root itself).
//   - Foreign-machine session (canonical root differs from any local repo) →
//     returned as "unresolved:"+dir so the origin is queryable.
func NormalizeProjectDir(dir string) string {
	if dir == "" || !filepath.IsAbs(dir) {
		return dir
	}
	// Use a sentinel child path so filepath.Dir(sentinel) == dir, allowing
	// discoverRepoRoot to invoke the resolver with dir itself rather than
	// its parent. The sentinel file need not exist on disk — EvalSymlinks
	// failures are tolerated by NormalizeWithResolver.
	sentinel := filepath.Join(dir, ".wipnote")
	got, _ := NormalizeWithResolver(sentinel, "", resolveWipnoteAnchor)
	if got == "" {
		return dir
	}
	// Strip the ".wipnote" suffix that was appended as the sentinel.
	if got == ".wipnote" {
		return "."
	}
	if strings.HasSuffix(got, "/.wipnote") {
		return strings.TrimSuffix(got, "/.wipnote")
	}
	// Handle "unresolved:" prefix — strip the sentinel suffix from the original path.
	if strings.HasPrefix(got, "unresolved:") {
		return "unresolved:" + dir
	}
	return got
}

// --- Internal -------------------------------------------------------------

// normalizeCache stores resolved repo roots keyed by containing directory so
// repeated normalisation of files in the same directory avoids re-running
// `git rev-parse`. An empty-string value is also cached to remember
// "not in a worktree" answers (negative caching prevents pathological hot
// paths where every Edit on /tmp invokes git).
var normalizeCache sync.Map // map[string]string

// ResetNormalizeCacheForTesting clears the process-wide cache. Test-only seam.
func ResetNormalizeCacheForTesting() {
	normalizeCache = sync.Map{}
}

// NormalizeWithResolver is the test-injectable form of NormalizeToRepoRelative.
// Production callers should use NormalizeToRepoRelative.
//
// The resolver argument is invoked at most once per unique containing
// directory per process. It must accept a directory and return the
// relativization anchor (typically the local worktree toplevel for that dir)
// or "" when no wipnote-aware anchor can be found.
func NormalizeWithResolver(absPath, repoRoot string, resolver func(dir string) string) (string, bool) {
	if absPath == "" {
		return absPath, true
	}
	if !filepath.IsAbs(absPath) {
		// Already-relative paths pass through unchanged. Hook payloads
		// frequently contain a mix; preserving relatives avoids
		// double-normalisation.
		return absPath, true
	}

	canonicalRoot := repoRoot
	if canonicalRoot == "" {
		canonicalRoot = discoverRepoRoot(filepath.Dir(absPath), resolver)
	}

	if canonicalRoot == "" {
		// Nothing to anchor against. Mark host-local paths so the
		// downstream migration rewriter can recognise and repair them;
		// leave exotic paths (/opt/..., /usr/local/...) untouched.
		if HostPathPattern.MatchString(absPath) {
			return "unresolved:" + absPath, true
		}
		return absPath, true
	}

	// Apply EvalSymlinks to both sides. Failures are tolerated — tool_input
	// regularly references files that have not yet been created on disk.
	evaledRoot := evalSymlinksOrRaw(canonicalRoot)
	evaledAbs := evalSymlinksOrRaw(absPath)

	rel, err := filepath.Rel(evaledRoot, evaledAbs)
	if err != nil {
		// Rel only errors on Windows when the volumes differ; treat as
		// outside-repo.
		return "unresolved:" + absPath, true
	}
	// Exact traversal check: paths whose first component starts with two dots
	// (e.g. "..data/foo.go") are legitimate in-repo paths and must not be
	// flagged as outside-repo. Only treat ".." or "../..." as traversal.
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "unresolved:" + absPath, true
	}
	return filepath.ToSlash(rel), true
}

// discoverRepoRoot resolves the canonical main repo root for the directory
// containing the file being normalised. Results are cached in normalizeCache
// keyed by dir so repeated lookups in hot paths never re-shell to git.
//
// When dir does not exist on disk (tool_input frequently references not-
// yet-created files inside not-yet-created subdirectories), the function
// walks up to the nearest existing ancestor before invoking the resolver.
// The cache is still keyed by the original dir so hot-path callers benefit
// from cache hits even for imminent-but-not-yet-created files.
func discoverRepoRoot(dir string, resolver func(string) string) string {
	if dir == "" {
		return ""
	}
	if cached, ok := normalizeCache.Load(dir); ok {
		return cached.(string)
	}

	existingDir := nearestExistingDir(dir)
	root := ""
	if existingDir != "" {
		root = resolver(existingDir)
		if root == "" {
			// Resolver returned "" — either dir is not inside any
			// git repo, or it is inside a git repo with no
			// .wipnote/ ancestor. Try a plain walk-up looking for
			// .wipnote/ to cover the rare case of a wipnote
			// project that is not itself a git repo (e.g. a notes
			// directory under a parent vault).
			root = walkUpForWipnote(existingDir, 0)
		}
	}
	normalizeCache.Store(dir, root)
	return root
}

// nearestExistingDir returns dir if it exists, else the closest existing
// ancestor, else "". Needed so `git -C <dir> rev-parse` never sees a
// non-existent directory — git would just fail and we'd lose worktree
// resolution for hot-path Edits on files inside not-yet-created dirs.
func nearestExistingDir(dir string) string {
	for dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// evalSymlinksOrRaw returns EvalSymlinks(p) when it succeeds, otherwise p
// unchanged. Used because tool_input often points at not-yet-created files
// and an ENOENT must not abort normalisation.
func evalSymlinksOrRaw(p string) string {
	if e, err := filepath.EvalSymlinks(p); err == nil {
		return e
	}
	return p
}

// debugLog writes a message to stderr only when WIPNOTE_DEBUG is set.
// Kept package-local so internal/paths has no external logging dep.
func debugLog(format string, args ...interface{}) {
	if os.Getenv("WIPNOTE_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "[wipnote/paths] "+format+"\n", args...)
}
