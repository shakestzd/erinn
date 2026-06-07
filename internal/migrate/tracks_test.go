package migrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/models"
)

// fakeFiles makes a slice of FeatureFile with the given paths.
func fakeFiles(featureID string, paths ...string) []models.FeatureFile {
	out := make([]models.FeatureFile, len(paths))
	for i, p := range paths {
		out[i] = models.FeatureFile{
			ID:        "ff-" + p,
			FeatureID: featureID,
			FilePath:  p,
		}
	}
	return out
}

// testRules returns a small RuleSet covering the four value-aligned tracks.
func testRules() *RuleSet {
	return &RuleSet{Rules: []Rule{
		{Glob: "cmd/wipnote/yolo.go", TrackID: "trk-yolo", Priority: 110},
		{Glob: "cmd/wipnote/tmux.go", TrackID: "trk-yolo", Priority: 110},
		{Glob: "internal/worktree/**", TrackID: "trk-yolo", Priority: 100},
		{Glob: "cmd/wipnote/plan_*.go", TrackID: "trk-plan", Priority: 100},
		{Glob: "internal/plantmpl/**", TrackID: "trk-plan", Priority: 100},
		{Glob: "internal/blame/**", TrackID: "trk-lineage", Priority: 100},
		{Glob: "cmd/wipnote/blame.go", TrackID: "trk-lineage", Priority: 110},
		{Glob: "plugin/agents/*.md", TrackID: "trk-subagents", Priority: 100},
	}}
}

func TestClassify_SingleTrackDominant(t *testing.T) {
	rules := testRules()
	files := fakeFiles("feat-001",
		"cmd/wipnote/yolo.go",
		"cmd/wipnote/tmux.go",
		"internal/worktree/manager.go",
		"internal/worktree/registry.go",
	)
	d := ClassifyFeature(rules, files, "trk-cd61bbae", 0.6)
	if d.ProposedTrack != "trk-yolo" {
		t.Fatalf("proposed_track = %q, want trk-yolo", d.ProposedTrack)
	}
	if d.Ambiguous {
		t.Fatalf("expected confident, got ambiguous: %+v", d)
	}
	if d.DominantShare < 0.99 {
		t.Fatalf("dominant_share = %v, want ~1.0", d.DominantShare)
	}
	if d.Reason != "confident" {
		t.Fatalf("reason = %q, want confident", d.Reason)
	}
	if d.FileCount != 4 {
		t.Fatalf("file_count = %d, want 4", d.FileCount)
	}
}

func TestClassify_AmbiguousBelowThreshold(t *testing.T) {
	rules := testRules()
	// 3 plan files, 2 yolo files: dominant share = 3/5 = 0.6
	files := fakeFiles("feat-002",
		"cmd/wipnote/plan_create.go",
		"cmd/wipnote/plan_show.go",
		"cmd/wipnote/plan_finalize.go",
		"cmd/wipnote/yolo.go",
		"cmd/wipnote/tmux.go",
	)
	// threshold 0.7 → ambiguous (0.6 < 0.7)
	d := ClassifyFeature(rules, files, "trk-old", 0.7)
	if !d.Ambiguous {
		t.Fatalf("expected ambiguous (share=0.6, threshold=0.7), got %+v", d)
	}
	if d.ProposedTrack != "trk-plan" {
		t.Fatalf("proposed_track = %q, want trk-plan (still the leader)", d.ProposedTrack)
	}
	if d.Reason != "ambiguous" {
		t.Fatalf("reason = %q, want ambiguous", d.Reason)
	}
}

func TestClassify_NoFeatureFiles(t *testing.T) {
	rules := testRules()
	d := ClassifyFeature(rules, nil, "trk-old", 0.6)
	if d.Reason != "no-attribution" {
		t.Fatalf("reason = %q, want no-attribution", d.Reason)
	}
	if d.ProposedTrack != "" {
		t.Fatalf("proposed_track = %q, want empty", d.ProposedTrack)
	}
	if d.FileCount != 0 {
		t.Fatalf("file_count = %d, want 0", d.FileCount)
	}
}

func TestClassify_RulePriorityWins(t *testing.T) {
	// rule A glob=internal/blame/** (priority 100) → trk-lineage
	// rule B glob=cmd/wipnote/blame.go (priority 110) → trk-lineage
	// To test priority resolution we need two rules with overlapping globs but different track IDs.
	rules := &RuleSet{Rules: []Rule{
		{Glob: "cmd/wipnote/**", TrackID: "trk-broad", Priority: 50},
		{Glob: "cmd/wipnote/blame.go", TrackID: "trk-lineage", Priority: 200},
	}}
	files := fakeFiles("feat-prio", "cmd/wipnote/blame.go")
	d := ClassifyFeature(rules, files, "trk-old", 0.6)
	if d.ProposedTrack != "trk-lineage" {
		t.Fatalf("proposed_track = %q, want trk-lineage (higher priority should win)", d.ProposedTrack)
	}
}

func TestClassify_NoMatchingRules(t *testing.T) {
	rules := testRules()
	files := fakeFiles("feat-noop", "some/random/path.txt", "another/file.md")
	d := ClassifyFeature(rules, files, "trk-old", 0.6)
	if d.Reason != "no-match" {
		t.Fatalf("reason = %q, want no-match", d.Reason)
	}
	if d.Ambiguous {
		t.Fatalf("no-match must not be flagged ambiguous: %+v", d)
	}
	if d.ProposedTrack != "" {
		t.Fatalf("proposed_track = %q, want empty for no-match", d.ProposedTrack)
	}
	if d.FileCount != 2 {
		t.Fatalf("file_count = %d, want 2 (files exist, just no rule matched)", d.FileCount)
	}
}

func TestClassify_LowConfidenceOnSameTrackStillAmbiguous(t *testing.T) {
	// Regression: roborev finding — when a feature's current_track happens to
	// be the dominant track but the dominance is below threshold, the result
	// must be ambiguous, not no-change. A 1/4 share on the current track is
	// still split attribution.
	rules := testRules()
	files := fakeFiles("feat-low",
		"cmd/wipnote/yolo.go",        // 1 yolo
		"cmd/wipnote/plan_create.go", // 1 plan
		"plugin/agents/x.md",         // 1 subagents
		"internal/blame/blame.go",    // 1 lineage
	)
	d := ClassifyFeature(rules, files, "trk-yolo", 0.6)
	if !d.Ambiguous {
		t.Fatalf("expected ambiguous (1/4=0.25 < 0.6) even when current==dominant, got %+v", d)
	}
	if d.Reason != "ambiguous" {
		t.Fatalf("reason = %q, want ambiguous", d.Reason)
	}
}

func TestClassify_AlreadyOnDominantTrack(t *testing.T) {
	rules := testRules()
	files := fakeFiles("feat-stay", "cmd/wipnote/yolo.go", "cmd/wipnote/tmux.go")
	d := ClassifyFeature(rules, files, "trk-yolo", 0.6) // current == dominant
	if d.Reason != "no-change" {
		t.Fatalf("reason = %q, want no-change", d.Reason)
	}
	if d.ProposedTrack != "trk-yolo" {
		t.Fatalf("proposed_track = %q, want trk-yolo", d.ProposedTrack)
	}
}

func TestLoadRules_ParsesYAML(t *testing.T) {
	yaml := `rules:
  - { glob: "cmd/wipnote/yolo.go", track_id: "trk-yolo", priority: 110 }
  - { glob: "internal/blame/**", track_id: "trk-lineage", priority: 100 }
`
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rs, err := LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rs.Rules))
	}
	if rs.Rules[0].Glob != "cmd/wipnote/yolo.go" || rs.Rules[0].TrackID != "trk-yolo" || rs.Rules[0].Priority != 110 {
		t.Fatalf("rule[0] = %+v", rs.Rules[0])
	}
}

func TestClassify_HandlesAbsolutePaths(t *testing.T) {
	rules := testRules()
	// File paths captured by hooks running in a worktree are absolute.
	files := fakeFiles("feat-abs",
		"/workspaces/wipnote/.claude/worktrees/wt-foo/cmd/wipnote/yolo.go",
		"/workspaces/wipnote/cmd/wipnote/tmux.go",
	)
	d := ClassifyFeature(rules, files, "trk-old", 0.6)
	if d.ProposedTrack != "trk-yolo" {
		t.Fatalf("absolute paths not normalized: proposed=%q reason=%q", d.ProposedTrack, d.Reason)
	}
	if d.Reason != "confident" {
		t.Fatalf("reason = %q, want confident", d.Reason)
	}
}

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"cmd/wipnote/yolo.go", "cmd/wipnote/yolo.go"},
		{"/workspaces/wipnote/cmd/wipnote/yolo.go", "cmd/wipnote/yolo.go"},
		{"/repo/.claude/worktrees/x/internal/blame/blame.go", "internal/blame/blame.go"},
		{"/random/path/with/no/anchor.txt", "/random/path/with/no/anchor.txt"},
		{"/foo/plugin/agents/x.md", "plugin/agents/x.md"},
		// Worktree prefix with cmd/ underneath
		{"/workspaces/wipnote/.claude/worktrees/wt-foo/cmd/wipnote/plan_show.go",
			"cmd/wipnote/plan_show.go"},
		// .githooks anchor
		{"/workspaces/wipnote/.githooks/pre-commit", ".githooks/pre-commit"},
	}
	for _, c := range cases {
		got := normalizePath(c.in)
		if got != c.want {
			t.Errorf("normalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatchGlob_DoubleStar(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"internal/blame/**", "internal/blame/blame.go", true},
		{"internal/blame/**", "internal/blame/sub/areas.go", true},
		{"internal/blame/**", "internal/other/blame.go", false},
		{"cmd/wipnote/plan_*.go", "cmd/wipnote/plan_create.go", true},
		{"cmd/wipnote/plan_*.go", "cmd/wipnote/plan_sub/file.go", false},
		{"cmd/wipnote/yolo.go", "cmd/wipnote/yolo.go", true},
		{"cmd/wipnote/yolo.go", "cmd/wipnote/yolo_test.go", false},
		{"plugin/agents/*.md", "plugin/agents/feature-coder.md", true},
		{"plugin/agents/*.md", "plugin/agents/sub/x.md", false},
	}
	for _, c := range cases {
		got := matchGlob(c.glob, c.path)
		if got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}
