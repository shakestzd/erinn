package worktree_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/worktree"
)

// setupGitRepoWithWipnote creates a fresh git repo with a .wipnote/ directory.
func setupGitRepoWithWipnote(t *testing.T) string {
	t.Helper()
	dir := setupGitRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	return dir
}

// TestEnsureWipnoteGitignore_CreatesFileWhenMissing verifies that calling
// EnsureWipnoteGitignore on a project without a .wipnote/.gitignore creates
// the file with the expected runtime-artifact patterns.
func TestEnsureWipnoteGitignore_CreatesFileWhenMissing(t *testing.T) {
	dir := setupGitRepoWithWipnote(t)
	target := filepath.Join(dir, ".wipnote", ".gitignore")

	// File should not exist yet.
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected .wipnote/.gitignore to be absent before installer; stat err: %v", err)
	}

	worktree.EnsureWipnoteGitignore(dir)

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected .wipnote/.gitignore to exist after installer: %v", err)
	}

	// Spot-check a representative set of patterns.
	for _, want := range []string{
		"sessions/",
		"events/",
		"logs/",
		"*.db",
		"*.bloom",
		"archive-index/",
		"**/*.jsonl",
		"**/*.log",
		"**/*.lock",
		".active-session",
		".launch-mode",
		".otel-notice-shown",
		".session-warning-state.json",
		".session-families.lock",
		"session-families.json",
		"parent-activity.json",
		".error-spikes.json",
		"active-auto-spikes.json",
		"drift-queue.json",
		".otlp-port",
		".serve.lock",
		"migrations/*.done",
		"**/*.pid",
		"**/*.sock",
		"**/.index-offset",
		"**/.collector-pid",
		"**/state.json",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf(".wipnote/.gitignore missing pattern %q", want)
		}
	}
}

// TestEnsureWipnoteGitignore_Idempotent verifies that calling EnsureWipnoteGitignore
// twice does not overwrite a user's existing .wipnote/.gitignore.
func TestEnsureWipnoteGitignore_Idempotent(t *testing.T) {
	dir := setupGitRepoWithWipnote(t)
	target := filepath.Join(dir, ".wipnote", ".gitignore")

	// Pre-populate with custom content.
	custom := "# user-managed content\nsessions/\n"
	if err := os.WriteFile(target, []byte(custom), 0o644); err != nil {
		t.Fatalf("write custom .gitignore: %v", err)
	}

	worktree.EnsureWipnoteGitignore(dir)

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read .wipnote/.gitignore: %v", err)
	}
	if string(got) != custom {
		t.Errorf("EnsureWipnoteGitignore must not overwrite existing file\ngot:  %q\nwant: %q", string(got), custom)
	}

	// Second call: also a no-op.
	worktree.EnsureWipnoteGitignore(dir)
	got2, _ := os.ReadFile(target)
	if string(got2) != custom {
		t.Errorf("second call overwrote existing file: %q", string(got2))
	}
}

// TestEnsureWipnoteGitignore_EmptyProjectDir verifies that an empty projectDir
// does not panic or error.
func TestEnsureWipnoteGitignore_EmptyProjectDir(t *testing.T) {
	// Must not panic.
	worktree.EnsureWipnoteGitignore("")
}

// TestEnsureWipnoteGitignore_GitHonorsPolicy verifies that after the installer
// runs, git does NOT list wipnote runtime files as untracked, but DOES still
// list (track) canonical work-item HTML files.
func TestEnsureWipnoteGitignore_GitHonorsPolicy(t *testing.T) {
	dir := setupGitRepoWithWipnote(t)

	worktree.EnsureWipnoteGitignore(dir)

	// Create runtime artifacts that should be ignored.
	runtimeFiles := map[string]string{
		".wipnote/sessions/":                "",
		".wipnote/.active-session":          `{"session_id":"x"}`,
		".wipnote/session-families.json":    `{}`,
		".wipnote/.session-families.lock":   "",
		".wipnote/drift-queue.json":         `[]`,
		".wipnote/.serve.lock":              "pid:123",
		".wipnote/events/":                  "",
		".wipnote/logs/":                    "",
		".wipnote/migrations/":              "",
		".wipnote/migrations/001_init.done": "",
	}
	for path, content := range runtimeFiles {
		full := filepath.Join(dir, path)
		if strings.HasSuffix(path, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", path, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir parent of %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	// Create a canonical work-item HTML that should NOT be ignored.
	featDir := filepath.Join(dir, ".wipnote", "features")
	if err := os.MkdirAll(featDir, 0o755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}
	canonicalHTML := filepath.Join(featDir, "feat-x.html")
	if err := os.WriteFile(canonicalHTML, []byte("<article></article>"), 0o644); err != nil {
		t.Fatalf("write feat-x.html: %v", err)
	}

	// git ls-files --others --exclude-standard to get what git sees as untracked.
	out, err := exec.Command("git", "-C", dir, "ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	listed := string(out)

	// Runtime artifacts must NOT appear.
	for path := range runtimeFiles {
		if strings.HasSuffix(path, "/") {
			continue // directories not listed by ls-files directly
		}
		clean := strings.TrimPrefix(path, ".wipnote/")
		_ = clean
		if strings.Contains(listed, path) {
			t.Errorf("git lists runtime file %q as untracked — should be ignored by .wipnote/.gitignore; full output:\n%s", path, listed)
		}
	}

	// Canonical HTML must still be visible.
	if !strings.Contains(listed, "feat-x.html") {
		t.Errorf("canonical feat-x.html should be untracked (not ignored); git output:\n%s", listed)
	}
}
