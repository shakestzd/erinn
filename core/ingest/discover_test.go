package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSubagents(t *testing.T) {
	t.Run("returns nil when no subagents dir", func(t *testing.T) {
		dir := t.TempDir()
		got, err := DiscoverSubagents(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d files, want 0", len(got))
		}
	})

	t.Run("discovers agent JSONL files", func(t *testing.T) {
		dir := t.TempDir()
		subDir := filepath.Join(dir, "subagents")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}

		// Create two agent files and one non-matching file.
		for _, name := range []string{"agent-abc123.jsonl", "agent-def456.jsonl", "other.jsonl"} {
			if err := os.WriteFile(filepath.Join(subDir, name), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		got, err := DiscoverSubagents(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d files, want 2", len(got))
		}
		ids := map[string]bool{}
		for _, sf := range got {
			ids[sf.SessionID] = true
			if sf.Size == 0 {
				t.Errorf("file %s has zero size", sf.Path)
			}
		}
		if !ids["abc123"] {
			t.Error("expected agent ID abc123")
		}
		if !ids["def456"] {
			t.Error("expected agent ID def456")
		}
	})

	t.Run("empty subagents dir returns no files", func(t *testing.T) {
		dir := t.TempDir()
		subDir := filepath.Join(dir, "subagents")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := DiscoverSubagents(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d files, want 0", len(got))
		}
	})
}

func TestDecodeProjectPath(t *testing.T) {
	tests := []struct {
		encoded  string
		expected string
	}{
		{
			encoded:  "-Users-testuser-DevProjects-wipnote",
			expected: "/Users/testuser/DevProjects/wipnote",
		},
		{
			encoded:  "-Users-alice-code-myapp",
			expected: "/Users/alice/code/myapp",
		},
		{
			encoded:  "-home-bob-projects-foo",
			expected: "/home/bob/projects/foo",
		},
		{
			encoded:  "",
			expected: "",
		},
		// Dashes in directory names are indistinguishable from path separators
		// in Claude's encoding — "foo-bar" encodes as "-foo-bar" just like
		// "/foo/bar" would, so the decode is a best-effort reconstruction.
		{
			encoded:  "-Users-testuser-DevProjects-my-project",
			expected: "/Users/testuser/DevProjects/my/project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.encoded, func(t *testing.T) {
			got := decodeProjectPath(tt.encoded)
			if got != tt.expected {
				t.Errorf("decodeProjectPath(%q) = %q, want %q", tt.encoded, got, tt.expected)
			}
		})
	}
}

func TestFilterByGitRemote(t *testing.T) {
	files := []SessionFile{
		{SessionID: "sess-1", Project: "wipnote", GitRemoteURL: "https://github.com/owner/wipnote.git"},
		{SessionID: "sess-2", Project: "wipnote", GitRemoteURL: "https://github.com/owner/wipnote.git"},
		{SessionID: "sess-3", Project: "other-project", GitRemoteURL: "https://github.com/owner/other.git"},
		{SessionID: "sess-4", Project: "no-remote", GitRemoteURL: ""},
	}

	t.Run("filters to matching remote only", func(t *testing.T) {
		got := FilterByGitRemote(files, "https://github.com/owner/wipnote.git")
		if len(got) != 2 {
			t.Fatalf("got %d files, want 2", len(got))
		}
		for _, sf := range got {
			if sf.GitRemoteURL != "https://github.com/owner/wipnote.git" {
				t.Errorf("unexpected remote %q in result", sf.GitRemoteURL)
			}
		}
	})

	t.Run("returns all when targetRemote is empty", func(t *testing.T) {
		got := FilterByGitRemote(files, "")
		if len(got) != len(files) {
			t.Fatalf("got %d files, want %d", len(got), len(files))
		}
	})

	t.Run("excludes sessions with empty remote URL", func(t *testing.T) {
		got := FilterByGitRemote(files, "https://github.com/owner/other.git")
		if len(got) != 1 {
			t.Fatalf("got %d files, want 1", len(got))
		}
		if got[0].SessionID != "sess-3" {
			t.Errorf("got session %q, want sess-3", got[0].SessionID)
		}
	})

	t.Run("returns empty when no match", func(t *testing.T) {
		got := FilterByGitRemote(files, "https://github.com/owner/nonexistent.git")
		if len(got) != 0 {
			t.Fatalf("got %d files, want 0", len(got))
		}
	})

	t.Run("handles empty input slice", func(t *testing.T) {
		got := FilterByGitRemote(nil, "https://github.com/owner/wipnote.git")
		if len(got) != 0 {
			t.Fatalf("got %d files, want 0", len(got))
		}
	})
}
