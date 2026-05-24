package paths_test

import (
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/paths"
)

// TestHostPathPattern_MatchesHomeDir verifies Linux home directories match.
func TestHostPathPattern_MatchesHomeDir(t *testing.T) {
	cases := []string{
		"/home/alice/project",
		"/home/bob/work/repo",
		"prefix /home/charlie/x suffix",
	}
	for _, s := range cases {
		if !paths.HostPathPattern.MatchString(s) {
			t.Errorf("expected HostPathPattern to match %q", s)
		}
	}
}

// TestHostPathPattern_MatchesUsersDir verifies macOS /Users/ home directories match.
func TestHostPathPattern_MatchesUsersDir(t *testing.T) {
	cases := []string{
		"/Users/alice/project",
		"/Users/fakeuser/Code",
		"prefix /Users/bob/x suffix",
	}
	for _, s := range cases {
		if !paths.HostPathPattern.MatchString(s) {
			t.Errorf("expected HostPathPattern to match %q", s)
		}
	}
}

// TestHostPathPattern_MatchesWorkspaces verifies Codespaces workspace paths match.
func TestHostPathPattern_MatchesWorkspaces(t *testing.T) {
	cases := []string{
		"/workspaces/wipnote/main.go",
		"/workspaces/foo/bar",
	}
	for _, s := range cases {
		if !paths.HostPathPattern.MatchString(s) {
			t.Errorf("expected HostPathPattern to match %q", s)
		}
	}
}

// TestHostPathPattern_MatchesMacTmp verifies macOS /private/var/folders/ matches.
func TestHostPathPattern_MatchesMacTmp(t *testing.T) {
	cases := []string{
		"/private/var/folders/abc/xyz",
		"/private/var/folders/",
	}
	for _, s := range cases {
		if !paths.HostPathPattern.MatchString(s) {
			t.Errorf("expected HostPathPattern to match %q", s)
		}
	}
}

// TestHostPathPattern_DoesNotMatchSafePaths verifies portable / generic paths
// are not flagged.
func TestHostPathPattern_DoesNotMatchSafePaths(t *testing.T) {
	cases := []string{
		"./relative/path",
		"foo/bar.go",
		"/usr/local/bin/tool",
		"/var/log/syslog",
		"/tmp/somefile",
		"plain text without paths",
	}
	for _, s := range cases {
		if paths.HostPathPattern.MatchString(s) {
			t.Errorf("expected HostPathPattern NOT to match %q", s)
		}
	}
}

// TestHostPathPattern_FindAllString returns the matched substring including the
// trailing slash. The precommit gate relies on this exact shape.
func TestHostPathPattern_FindAllString(t *testing.T) {
	matches := paths.HostPathPattern.FindAllString(
		"path=/Users/alice/x and /home/bob/y", -1)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(matches), matches)
	}
	if matches[0] != "/Users/alice/" {
		t.Errorf("match[0] = %q, want %q", matches[0], "/Users/alice/")
	}
	if matches[1] != "/home/bob/" {
		t.Errorf("match[1] = %q, want %q", matches[1], "/home/bob/")
	}
}

// TestSanitizeHostPaths verifies that absolute host-local path prefixes are
// replaced with a portable redaction token and the path remainder is preserved.
func TestSanitizeHostPaths(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// Codespaces workspace path — the primary trigger for bug-ff6a3286.
		{"/workspaces/wipnote/foo.go", "[host-path]/foo.go"},
		// Linux home directory.
		{"/home/vscode/project/bar.go", "[host-path]/project/bar.go"},
		// macOS home directory.
		{"/Users/alice/Code/baz.go", "[host-path]/Code/baz.go"},
		// macOS temp directory.
		{"/private/var/folders/abc/xyz", "[host-path]/abc/xyz"},
		// Mixed content: only the prefix is replaced, surrounding text is kept.
		{"see /workspaces/wipnote/foo.go for details", "see [host-path]/foo.go for details"},
		// Multiple matches in one string.
		{"/home/alice/a and /home/bob/b", "[host-path]/a and [host-path]/b"},
		// Safe paths must pass through unchanged.
		{"./relative/path", "./relative/path"},
		{"/usr/local/bin/tool", "/usr/local/bin/tool"},
		{"plain text", "plain text"},
	}
	for _, tc := range cases {
		got := paths.SanitizeHostPaths(tc.input)
		if got != tc.want {
			t.Errorf("SanitizeHostPaths(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestSanitizeHostPaths_WorkitemHTML proves the fix for bug-ff6a3286:
// a host path like /workspaces/wipnote/foo.go does NOT appear in the
// sanitized output.
func TestSanitizeHostPaths_WorkitemHTML(t *testing.T) {
	hostPath := "/workspaces/wipnote/foo.go"
	result := paths.SanitizeHostPaths("edited file " + hostPath)
	if strings.Contains(result, hostPath) {
		t.Errorf("SanitizeHostPaths output still contains %q: %q", hostPath, result)
	}
}
