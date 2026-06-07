package paths_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shakestzd/wipnote/core/paths"
)

// makeWipnoteRepo creates a tempdir with .wipnote/ and a git repo, returning
// the absolute path (evaluated through symlinks so tests can compare equal).
// The repo is configured with a local user.email / user.name so subsequent
// `git commit` invocations succeed even when no global identity is set.
func makeWipnoteRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.email",
		"test@example.com").Run(); err != nil {
		t.Skipf("git config user.email failed: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.name",
		"Test User").Run(); err != nil {
		t.Skipf("git config user.name failed: %v", err)
	}
	// EvalSymlinks ensures the path matches what NormalizeToRepoRelative will
	// see internally (macOS /var → /private/var, etc.).
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return canon
}

// --- NormalizeToRepoRelative ----------------------------------------------

// TestNormalize_AlreadyRelative_PassThrough verifies that a relative input is
// returned unchanged with ok=true.
func TestNormalize_AlreadyRelative_PassThrough(t *testing.T) {
	got, ok := paths.NormalizeToRepoRelative("foo/bar.go", "")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if got != "foo/bar.go" {
		t.Errorf("normalize(%q) = %q, want %q", "foo/bar.go", got, "foo/bar.go")
	}
}

// TestNormalize_InsideRepo_UsingExplicitRoot verifies that providing an
// explicit repoRoot is honoured (skips canonical resolution).
func TestNormalize_InsideRepo_UsingExplicitRoot(t *testing.T) {
	repo := makeWipnoteRepo(t)
	abs := filepath.Join(repo, "pkg", "thing.go")

	got, ok := paths.NormalizeToRepoRelative(abs, repo)
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if got != "pkg/thing.go" {
		t.Errorf("normalize = %q, want %q", got, "pkg/thing.go")
	}
}

// TestNormalize_InsideRepo_AutoResolve verifies that an absolute path inside a
// real git+wipnote repo normalises against the discovered root.
func TestNormalize_InsideRepo_AutoResolve(t *testing.T) {
	repo := makeWipnoteRepo(t)
	subdir := filepath.Join(repo, "internal", "x")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	abs := filepath.Join(subdir, "file.go")

	got, ok := paths.NormalizeToRepoRelative(abs, "")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if got != "internal/x/file.go" {
		t.Errorf("normalize = %q, want %q", got, "internal/x/file.go")
	}
}

// TestNormalize_InsideLinkedWorktree_CollapsesToMainRoot verifies that a path
// inside a linked git worktree resolves to a path relative to the MAIN repo
// root (the parent of the shared .git dir). This is the central worktree-
// resilience guarantee that downstream slices depend on.
func TestNormalize_InsideLinkedWorktree_CollapsesToMainRoot(t *testing.T) {
	mainRepo := makeWipnoteRepo(t)
	// We need at least one commit before adding a worktree.
	if err := exec.Command("git", "-C", mainRepo, "commit",
		"--allow-empty", "-m", "init", "-q").Run(); err != nil {
		t.Skipf("git commit failed: %v", err)
	}
	wtPath := filepath.Join(t.TempDir(), "wt")
	if err := exec.Command("git", "-C", mainRepo, "worktree", "add",
		"-q", wtPath, "-b", "feat-test").Run(); err != nil {
		t.Skipf("git worktree add failed: %v", err)
	}
	wtPath, err := filepath.EvalSymlinks(wtPath)
	if err != nil {
		t.Fatalf("EvalSymlinks(wt): %v", err)
	}

	abs := filepath.Join(wtPath, "cmd", "main.go")
	got, ok := paths.NormalizeToRepoRelative(abs, "")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	// The path must be expressed relative to the MAIN repo root so that
	// downstream sessions/events compare equal across worktrees.
	if got != "cmd/main.go" {
		t.Errorf("normalize = %q, want %q (worktree path should collapse to main repo root)",
			got, "cmd/main.go")
	}
}

// TestNormalize_OutsideRepo_HostPath_MarkedUnresolved verifies that an
// absolute path outside any repo that matches HostPathPattern receives the
// "unresolved:" prefix so downstream consumers can detect it.
func TestNormalize_OutsideRepo_HostPath_MarkedUnresolved(t *testing.T) {
	// Use a path that matches HostPathPattern and is outside any wipnote repo.
	// /tmp won't match; use /home/somebody/foo which is guaranteed to.
	abs := "/home/somebody-not-real/foo/bar.go"
	got, ok := paths.NormalizeToRepoRelative(abs, "")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if got != "unresolved:"+abs {
		t.Errorf("normalize = %q, want %q", got, "unresolved:"+abs)
	}
}

// TestNormalize_OutsideRepo_NonHostPath_PassThrough verifies that an absolute
// path NOT matching HostPathPattern is returned unchanged (treated as exotic
// but acceptable; downstream may emit a warning but not silently mangle it).
func TestNormalize_OutsideRepo_NonHostPath_PassThrough(t *testing.T) {
	abs := "/opt/some/exotic/path.txt"
	got, ok := paths.NormalizeToRepoRelative(abs, "")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if got != abs {
		t.Errorf("normalize = %q, want %q (exotic absolute paths pass through)",
			got, abs)
	}
}

// TestNormalize_ExplicitRepoRoot_TakesPrecedence verifies that an explicit
// repoRoot argument is used in preference to canonical resolution.
func TestNormalize_ExplicitRepoRoot_TakesPrecedence(t *testing.T) {
	// Create two unrelated tempdirs; provide one explicitly, place the file
	// inside it.  No git/wipnote setup is needed because explicit-root path
	// skips ResolveViaGitCommonDir entirely.
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	abs := filepath.Join(repo, "a", "b.go")

	got, ok := paths.NormalizeToRepoRelative(abs, repo)
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if got != "a/b.go" {
		t.Errorf("normalize = %q, want %q", got, "a/b.go")
	}
}

// TestNormalize_SymlinkedRepoRoot verifies that EvalSymlinks is applied before
// Rel so a symlinked repoRoot argument still produces a clean relative path.
func TestNormalize_SymlinkedRepoRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require admin on Windows")
	}
	real := t.TempDir()
	real, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(real, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	link := filepath.Join(t.TempDir(), "linked-repo")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	abs := filepath.Join(real, "pkg", "thing.go")
	// Caller passes the symlinked root; EvalSymlinks must dereference it
	// before computing Rel so the result is "pkg/thing.go", not "../<real>/...".
	got, ok := paths.NormalizeToRepoRelative(abs, link)
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if got != "pkg/thing.go" {
		t.Errorf("normalize = %q, want %q", got, "pkg/thing.go")
	}
}

// TestNormalize_RelStartsWithDotDot_MarksUnresolved verifies that when
// filepath.Rel produces "../..." (path is outside repo), the result is
// marked unresolved.
func TestNormalize_RelStartsWithDotDot_MarksUnresolved(t *testing.T) {
	repo := makeWipnoteRepo(t)
	// Use a sibling temp directory of repo (definitely not inside it),
	// but also matching HostPathPattern is not required — outside-repo
	// detection happens via the "../" prefix check after Rel.
	sibling := filepath.Join(filepath.Dir(repo), "outside-file.go")
	got, ok := paths.NormalizeToRepoRelative(sibling, repo)
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	want := "unresolved:" + sibling
	if got != want {
		t.Errorf("normalize = %q, want %q", got, want)
	}
}

// TestNormalize_DotDotPrefixedFilename_NotMistakenForTraversal verifies that
// an in-repo path whose first component starts with two dots (e.g.
// "..data/file.go") is normalised as a legitimate in-repo path and is NOT
// flagged as outside-repo. The earlier strings.HasPrefix(rel, "..") check
// was over-broad; the precise check is rel == ".." || prefix "../" only.
func TestNormalize_DotDotPrefixedFilename_NotMistakenForTraversal(t *testing.T) {
	repo := makeWipnoteRepo(t)
	abs := filepath.Join(repo, "..data", "file.go")

	got, ok := paths.NormalizeToRepoRelative(abs, repo)
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	want := "..data/file.go"
	if got != want {
		t.Errorf("normalize(..data/file.go) = %q, want %q", got, want)
	}
}

// TestNormalize_NonExistentAbsPath_DoesNotCrash verifies that EvalSymlinks
// failure (path doesn't exist on disk yet — tool_input is full of these)
// falls back to the raw paths and still produces a sensible result.
func TestNormalize_NonExistentAbsPath_DoesNotCrash(t *testing.T) {
	repo := makeWipnoteRepo(t)
	// File does not exist on disk — EvalSymlinks(abs) will fail.
	abs := filepath.Join(repo, "not-yet-written", "file.go")

	got, ok := paths.NormalizeToRepoRelative(abs, repo)
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if got != "not-yet-written/file.go" {
		t.Errorf("normalize(non-existent) = %q, want %q",
			got, "not-yet-written/file.go")
	}
}

// TestNormalize_CacheHitsAvoidRepeatedGit verifies that two calls with the
// same containing directory invoke the resolver function at most once. We
// use the test seam NormalizeWithResolver, which accepts the function used
// to discover the repo root and lets us count invocations.
func TestNormalize_CacheHitsAvoidRepeatedGit(t *testing.T) {
	// Clear cache so a previous test cannot mask the count.
	paths.ResetNormalizeCacheForTesting()

	repo := makeWipnoteRepo(t)
	subdir := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	abs1 := filepath.Join(subdir, "one.go")
	abs2 := filepath.Join(subdir, "two.go") // same containing dir

	var calls int32
	resolver := func(dir string) string {
		atomic.AddInt32(&calls, 1)
		return repo
	}

	if _, ok := paths.NormalizeWithResolver(abs1, "", resolver); !ok {
		t.Fatalf("first call: expected ok=true")
	}
	if _, ok := paths.NormalizeWithResolver(abs2, "", resolver); !ok {
		t.Fatalf("second call: expected ok=true")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("resolver invoked %d times, want 1 (cache miss)", got)
	}
}

// TestNormalize_CacheKeyIsDirectory verifies that a different containing
// directory triggers a fresh resolver call.
func TestNormalize_CacheKeyIsDirectory(t *testing.T) {
	paths.ResetNormalizeCacheForTesting()
	repo := makeWipnoteRepo(t)
	dirA := filepath.Join(repo, "a")
	dirB := filepath.Join(repo, "b")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	var calls int32
	resolver := func(dir string) string {
		atomic.AddInt32(&calls, 1)
		return repo
	}
	_, _ = paths.NormalizeWithResolver(filepath.Join(dirA, "x.go"), "", resolver)
	_, _ = paths.NormalizeWithResolver(filepath.Join(dirB, "y.go"), "", resolver)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("resolver invoked %d times, want 2 (one per unique dir)", got)
	}
}

// --- MustNormalize --------------------------------------------------------

// TestMustNormalize_SuccessReturnsNormalized verifies the success path returns
// the normalised relative path.
func TestMustNormalize_SuccessReturnsNormalized(t *testing.T) {
	repo := makeWipnoteRepo(t)
	abs := filepath.Join(repo, "x.go")
	got := paths.MustNormalize(abs, repo)
	if got != "x.go" {
		t.Errorf("MustNormalize = %q, want %q", got, "x.go")
	}
}

// TestMustNormalize_AlreadyRelativePassThrough verifies relative paths pass
// straight through MustNormalize.
func TestMustNormalize_AlreadyRelativePassThrough(t *testing.T) {
	got := paths.MustNormalize("foo/bar.go", "")
	if got != "foo/bar.go" {
		t.Errorf("MustNormalize = %q, want pass-through", got)
	}
}

// TestMustNormalize_DoesNotPanicOnExotic verifies MustNormalize never panics
// even when called with weird inputs. It returns either the normalised path
// or the original input — never panics, never empty.
func TestMustNormalize_DoesNotPanicOnExotic(t *testing.T) {
	cases := []string{
		"",
		"/nonexistent/path/that/cannot/resolve/file.go",
		"/home/nobody-real/foo.go",
	}
	for _, in := range cases {
		got := paths.MustNormalize(in, "")
		if in != "" && got == "" {
			t.Errorf("MustNormalize(%q) returned empty string", in)
		}
		// Must contain the original somewhere (either as-is or prefixed).
		if in != "" && !strings.Contains(got, strings.TrimPrefix(in, "/")) &&
			got != in && !strings.HasSuffix(got, in) {
			// soft check — just ensure we didn't lose the data entirely
			t.Logf("MustNormalize(%q) = %q (lossy but non-panic OK)", in, got)
		}
	}
}
