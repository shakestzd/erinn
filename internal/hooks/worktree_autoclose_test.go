package hooks

import "testing"

func TestExtractWorktreeItemID(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "feature worktree",
			path: "/Users/dev/project/.claude/worktrees/feat-abc12345",
			want: "feat-abc12345",
		},
		{
			name: "bug worktree",
			path: "/Users/dev/project/.claude/worktrees/bug-def67890",
			want: "bug-def67890",
		},
		{
			name: "spike worktree",
			path: "/Users/dev/project/.claude/worktrees/spk-11223344",
			want: "spk-11223344",
		},
		{
			name: "track worktree — not a closable item",
			path: "/Users/dev/project/.claude/worktrees/trk-aabbccdd",
			want: "",
		},
		{
			name: "agent worktree nested under track",
			path: "/Users/dev/project/.claude/worktrees/trk-aabbccdd/agent-task1",
			want: "",
		},
		{
			name: "empty path",
			path: "",
			want: "",
		},
		{
			name: "non-worktree path",
			path: "/Users/dev/project/src/main.go",
			want: "",
		},
		{
			name: "feature ID in middle of path — extracts from basename",
			path: "/tmp/.claude/worktrees/feat-99887766",
			want: "feat-99887766",
		},
		{
			name: "feature ID with trailing slash",
			path: "/Users/dev/project/.claude/worktrees/feat-abc12345/",
			want: "feat-abc12345",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWorktreeItemID(tt.path)
			if got != tt.want {
				t.Errorf("extractWorktreeItemID(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
