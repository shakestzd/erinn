package hooks

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/paths"
)

// fakeResolver is a test-injectable anchor resolver: it returns repoRoot for
// any dir that is inside repoRoot, and "" otherwise.
func fakeResolverFor(repoRoot string) func(string) string {
	return func(dir string) string {
		if repoRoot != "" && strings.HasPrefix(dir, repoRoot) {
			return repoRoot
		}
		return ""
	}
}

// TestNormalizeToolInput_AbsoluteFilePath verifies that an absolute file_path
// is converted to a repo-relative path.
func TestNormalizeToolInput_AbsoluteFilePath(t *testing.T) {
	repoRoot := "/workspaces/myrepo"
	input := map[string]any{
		"file_path": "/workspaces/myrepo/cmd/main.go",
	}

	paths.ResetNormalizeCacheForTesting()
	got := normalizeToolInputPaths(input, "Read", repoRoot, fakeResolverFor(repoRoot))

	var result map[string]any
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := "cmd/main.go"
	if result["file_path"] != want {
		t.Errorf("file_path = %q, want %q", result["file_path"], want)
	}
}

// TestNormalizeToolInput_RelativeFilePath verifies that an already-relative
// file_path is returned unchanged.
func TestNormalizeToolInput_RelativeFilePath(t *testing.T) {
	repoRoot := "/workspaces/myrepo"
	input := map[string]any{
		"file_path": "cmd/main.go",
	}

	paths.ResetNormalizeCacheForTesting()
	got := normalizeToolInputPaths(input, "Read", repoRoot, fakeResolverFor(repoRoot))

	var result map[string]any
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["file_path"] != "cmd/main.go" {
		t.Errorf("file_path = %q, want %q", result["file_path"], "cmd/main.go")
	}
}

// TestNormalizeToolInput_AbsPathOutsideRepo verifies that an absolute path
// outside the repo that matches HostPathPattern gets an "unresolved:" prefix.
func TestNormalizeToolInput_AbsPathOutsideRepo(t *testing.T) {
	repoRoot := "/workspaces/myrepo"
	// /home/user/ matches HostPathPattern but is outside the repo.
	outsidePath := "/home/user/other-project/foo.go"
	input := map[string]any{
		"file_path": outsidePath,
	}

	paths.ResetNormalizeCacheForTesting()
	// Use a resolver that never matches (path is outside repo).
	got := normalizeToolInputPaths(input, "Read", repoRoot, func(string) string { return "" })

	var result map[string]any
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fp, _ := result["file_path"].(string)
	if !strings.HasPrefix(fp, "unresolved:") {
		t.Errorf("file_path = %q, want unresolved: prefix", fp)
	}
}

// TestNormalizeToolInput_BashCommandNotMutated verifies that the "command"
// field in Bash tool_input is not normalized.
func TestNormalizeToolInput_BashCommandNotMutated(t *testing.T) {
	repoRoot := "/workspaces/myrepo"
	cmd := "/workspaces/myrepo/scripts/build.sh && echo done"
	input := map[string]any{
		"command": cmd,
	}

	paths.ResetNormalizeCacheForTesting()
	got := normalizeToolInputPaths(input, "Bash", repoRoot, fakeResolverFor(repoRoot))

	var result map[string]any
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["command"] != cmd {
		t.Errorf("command = %q, want unchanged %q", result["command"], cmd)
	}
}

// TestNormalizeToolInput_MalformedInput verifies that a nil input returns the
// original empty string (no panic).
func TestNormalizeToolInput_MalformedInput(t *testing.T) {
	repoRoot := "/workspaces/myrepo"
	// nil input — no panic, return empty string.
	got := normalizeToolInputPaths(nil, "Read", repoRoot, fakeResolverFor(repoRoot))
	if got != "" {
		t.Errorf("nil input: got %q, want empty string", got)
	}
}

// TestNormalizeToolInput_MultiplePathKeys verifies that all known path keys
// are normalized in a single call.
func TestNormalizeToolInput_MultiplePathKeys(t *testing.T) {
	repoRoot := "/workspaces/myrepo"
	input := map[string]any{
		"file_path":     "/workspaces/myrepo/cmd/main.go",
		"notebook_path": "/workspaces/myrepo/notebooks/analysis.ipynb",
		"path":          "/workspaces/myrepo/internal/db/schema.sql",
	}

	paths.ResetNormalizeCacheForTesting()
	got := normalizeToolInputPaths(input, "NotebookEdit", repoRoot, fakeResolverFor(repoRoot))

	var result map[string]any
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	checks := map[string]string{
		"file_path":     "cmd/main.go",
		"notebook_path": "notebooks/analysis.ipynb",
		"path":          "internal/db/schema.sql",
	}
	for k, want := range checks {
		if result[k] != want {
			t.Errorf("%s = %q, want %q", k, result[k], want)
		}
	}
}

// TestNormalizeToolInput_GrepPatternNotMutated verifies that Grep's "pattern"
// field is left verbatim. The pattern is a search query, not a file path:
// normalising "/workspaces/myrepo/internal" to "internal" would silently
// corrupt the captured query and lose the user's intent.
func TestNormalizeToolInput_GrepPatternNotMutated(t *testing.T) {
	repoRoot := "/workspaces/myrepo"
	input := map[string]any{
		"pattern": "/workspaces/myrepo/internal",
	}

	paths.ResetNormalizeCacheForTesting()
	got := normalizeToolInputPaths(input, "Grep", repoRoot, fakeResolverFor(repoRoot))

	var result map[string]any
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["pattern"] != "/workspaces/myrepo/internal" {
		t.Errorf("pattern = %q, want unchanged %q",
			result["pattern"], "/workspaces/myrepo/internal")
	}
}
