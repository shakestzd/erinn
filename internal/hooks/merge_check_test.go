package hooks

import "testing"

func TestLooksLikeGitMerge(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{`git merge feat-branch`, true},
		{`git merge --no-ff yolo-feat-abc12345`, true},
		{`git merge --no-ff yolo-feat-abc12345 -m "merge"`, true},
		{`git log`, false},
		{`git commit -m "merge branch"`, false},
		{`echo "git merge"`, false}, // inside quotes, not a real merge
	}
	for _, tt := range tests {
		if got := looksLikeGitMerge(tt.cmd); got != tt.want {
			t.Errorf("looksLikeGitMerge(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

func TestExtractMergeBranch(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{
			name: "simple merge",
			cmd:  "git merge feat-branch",
			want: "feat-branch",
		},
		{
			name: "merge with --no-ff",
			cmd:  "git merge --no-ff yolo-feat-abc12345",
			want: "yolo-feat-abc12345",
		},
		{
			name: "merge with -m message",
			cmd:  `git merge --no-ff yolo-feat-abc12345 -m "merge feature"`,
			want: "yolo-feat-abc12345",
		},
		{
			name: "merge track branch",
			cmd:  "git merge --no-ff trk-aabbccdd",
			want: "trk-aabbccdd",
		},
		{
			name: "merge agent branch",
			cmd:  "git merge --no-ff agent-trk-aabbccdd-task1",
			want: "agent-trk-aabbccdd-task1",
		},
		{
			name: "not a merge",
			cmd:  "git log",
			want: "",
		},
		{
			name: "merge with no branch",
			cmd:  "git merge",
			want: "",
		},
		{
			name: "merge with --abort",
			cmd:  "git merge --abort",
			want: "",
		},
		{
			name: "merge with --continue",
			cmd:  "git merge --continue",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMergeBranch(tt.cmd)
			if got != tt.want {
				t.Errorf("extractMergeBranch(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestExtractBranchItemIDs(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		want   []string
	}{
		{
			name:   "yolo feature branch",
			branch: "yolo-feat-abc12345",
			want:   []string{"feat-abc12345"},
		},
		{
			name:   "yolo bug branch",
			branch: "yolo-bug-def67890",
			want:   []string{"bug-def67890"},
		},
		{
			name:   "yolo spike branch",
			branch: "yolo-spk-11223344",
			want:   []string{"spk-11223344"},
		},
		{
			name:   "bare feature branch",
			branch: "feat-abc12345",
			want:   []string{"feat-abc12345"},
		},
		{
			name:   "track branch — returns track ID",
			branch: "trk-aabbccdd",
			want:   []string{"trk-aabbccdd"},
		},
		{
			name:   "agent branch with track ID",
			branch: "agent-trk-aabbccdd-task1",
			want:   []string{"trk-aabbccdd"},
		},
		{
			name:   "unrelated branch",
			branch: "main",
			want:   nil,
		},
		{
			name:   "empty branch",
			branch: "",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBranchItemIDs(tt.branch)
			if len(got) != len(tt.want) {
				t.Fatalf("extractBranchItemIDs(%q) = %v, want %v", tt.branch, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractBranchItemIDs(%q)[%d] = %q, want %q", tt.branch, i, got[i], tt.want[i])
				}
			}
		})
	}
}
