package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- stagedDependencyManifests (pure) ---------------------------------------

func TestStagedDependencyManifests(t *testing.T) {
	cases := []struct {
		name   string
		staged []string
		want   []string
	}{
		{"go.mod", []string{"go.mod"}, []string{"go.mod"}},
		{"nested package.json", []string{"web/package.json"}, []string{"web/package.json"}},
		{"mixed", []string{"main.go", "go.mod", "README.md"}, []string{"go.mod"}},
		{"none", []string{"main.go", "core/hooks/x.go"}, nil},
		{"empty", nil, nil},
		{"multiple", []string{"go.mod", "go.sum"}, []string{"go.mod", "go.sum"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stagedDependencyManifests(tc.staged)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("stagedDependencyManifests(%v) = %v, want %v", tc.staged, got, tc.want)
			}
		})
	}
}

func TestCommitMessageHasResearchWaiver(t *testing.T) {
	matches := []string{
		`git commit -m "bump dep" -m "RESEARCH-WAIVER: internal pin"`,
		`git commit -m "research-waiver: vendored fork"`,
		`git commit --message="RESEARCH-WAIVER: justified"`,
		`git commit -m"RESEARCH-WAIVER: attached form"`, // -m with no space (attached)
		"git commit -m \"bump\" -m \"more notes\nRESEARCH-WAIVER: trailer reason\"",
	}
	for _, m := range matches {
		if !commitMessageHasResearchWaiver(m) {
			t.Errorf("expected waiver match for %q", m)
		}
	}
	nonMatches := []string{
		`git commit -m "add feature"`,
		`git commit -m "research the docs"`,                         // "research" alone is not a waiver
		`git commit -m "RESEARCH-WAIVER:"`,                          // colon but no reason
		`git commit -m "bump dep" && echo research-waiver`,          // token outside the message must NOT bypass
		`git commit -m "bump" # RESEARCH-WAIVER: in comment`,        // outside -m payload
		`git commit -m "bump" && echo -m "RESEARCH-WAIVER: fake"`,   // -m in a later segment must NOT bypass (roborev #572)
		`git commit -m "bump" ; echo -m "RESEARCH-WAIVER: fake"`,    // ; separator variant
		`echo -m "RESEARCH-WAIVER: fake" && git commit -m "bump"`,   // waiver before the commit segment
		`git commit -m "bump" # -m "RESEARCH-WAIVER: fake"`,         // -m inside a shell comment must NOT bypass (roborev #581)
		`git commit -m "bump" #-m "RESEARCH-WAIVER: fake"`,          // comment with no space
		`git commit -m "bump" -- go.mod -m "RESEARCH-WAIVER: fake"`, // -m after -- is a pathspec, not a message (roborev #582)
	}
	for _, m := range nonMatches {
		if commitMessageHasResearchWaiver(m) {
			t.Errorf("did not expect waiver match for %q", m)
		}
	}
}

// TestR5_WaiverOutsideMessage_DoesNotBypass proves the roborev #570 fix: a
// research-waiver token outside the -m payload (e.g. `&& echo research-waiver`)
// must NOT satisfy the gate — only a trailer in the commit message does.
func TestR5_WaiverOutsideMessage_DoesNotBypass(t *testing.T) {
	root := stageGoModRepo(t)
	const sid = "r5-waiver-bypass-sess"
	database := makeSessionDB(t, sid, root)
	insertAgentEventFull(t, &testDB{DB: database}, "evt-r5-read4", sid, "r5-agent", "tool_call", "Read", "go.mod")

	ev := &CloudEvent{
		ToolName:  "Bash",
		CWD:       root,
		ToolInput: map[string]any{"command": `git commit -m "bump dep" && echo research-waiver`},
	}
	if warn := checkDependencyResearchCommitGuard(ev, database, &toolUseContext{SessionID: sid}); warn == "" {
		t.Error("expected BLOCK: a research-waiver token outside the commit message must not bypass the gate")
	}
}

// --- checkDependencyResearchCommitGuard (no-op fast paths) -------------------

func TestCheckDependencyResearchCommitGuard_NonCommitAllowed(t *testing.T) {
	for _, tc := range []struct {
		tool, cmd string
	}{
		{"Bash", "git status"},
		{"Bash", "go test ./..."},
		{"Write", ""},
	} {
		ev := &CloudEvent{ToolName: tc.tool, CWD: "/tmp", ToolInput: map[string]any{"command": tc.cmd}}
		if warn := checkDependencyResearchCommitGuard(ev, nil, &toolUseContext{}); warn != "" {
			t.Errorf("tool=%q cmd=%q: expected no-op, got block %q", tc.tool, tc.cmd, warn)
		}
	}
}

// --- integration: staged go.mod + session web-research state -----------------

// stageGoModRepo creates a temp git repo with a staged go.mod and returns the root.
func stageGoModRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := initTestGitRepoOnBranch(t, root, "main"); err != nil {
		t.Skipf("cannot init git repo: %v", err)
	}
	gomod := filepath.Join(root, "go.mod")
	if err := os.WriteFile(gomod, []byte("module example.com/x\n\ngo 1.22\n\nrequire github.com/some/dep v1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGitAdd(t, root, gomod)
	return root
}

// TestR5_StagedGoModNoWebResearch_Blocks proves: staging a go.mod change with no
// web research in the session blocks the commit.
func TestR5_StagedGoModNoWebResearch_Blocks(t *testing.T) {
	root := stageGoModRepo(t)
	const sid = "r5-block-sess"
	database := makeSessionDB(t, sid, root)
	// A non-web tool_call so the recording-gap fail-open does NOT trigger.
	insertAgentEventFull(t, &testDB{DB: database}, "evt-r5-read", sid, "r5-agent", "tool_call", "Read", "go.mod")

	ev := &CloudEvent{
		ToolName:  "Bash",
		CWD:       root,
		ToolInput: map[string]any{"command": `git commit -m "bump dep"`},
	}
	warn := checkDependencyResearchCommitGuard(ev, database, &toolUseContext{SessionID: sid})
	if warn == "" {
		t.Fatal("expected BLOCK: staged go.mod with no web research must block the commit")
	}
	if !strings.Contains(warn, "go.mod") || !strings.Contains(warn, "RESEARCH-WAIVER") {
		t.Errorf("block message should name go.mod and the waiver override; got %q", warn)
	}
}

// TestR5_StagedGoModWithWebResearch_Allows proves: with prior web research the
// same commit is allowed.
func TestR5_StagedGoModWithWebResearch_Allows(t *testing.T) {
	root := stageGoModRepo(t)
	const sid = "r5-allow-sess"
	database := makeSessionDB(t, sid, root)
	insertAgentEventFull(t, &testDB{DB: database}, "evt-r5-web", sid, "r5-agent", "tool_call", "WebSearch", "github.com/some/dep changelog")

	ev := &CloudEvent{
		ToolName:  "Bash",
		CWD:       root,
		ToolInput: map[string]any{"command": `git commit -m "bump dep"`},
	}
	if warn := checkDependencyResearchCommitGuard(ev, database, &toolUseContext{SessionID: sid}); warn != "" {
		t.Errorf("expected ALLOW: staged go.mod after WebSearch must be allowed; got %q", warn)
	}
}

// TestR5_WaiverAllows proves: an explicit RESEARCH-WAIVER trailer allows the
// commit even with no web research.
func TestR5_WaiverAllows(t *testing.T) {
	root := stageGoModRepo(t)
	const sid = "r5-waiver-sess"
	database := makeSessionDB(t, sid, root)
	insertAgentEventFull(t, &testDB{DB: database}, "evt-r5-read2", sid, "r5-agent", "tool_call", "Read", "go.mod")

	ev := &CloudEvent{
		ToolName:  "Bash",
		CWD:       root,
		ToolInput: map[string]any{"command": `git commit -m "pin internal fork" -m "RESEARCH-WAIVER: vendored, no upstream"`},
	}
	if warn := checkDependencyResearchCommitGuard(ev, database, &toolUseContext{SessionID: sid}); warn != "" {
		t.Errorf("expected ALLOW with explicit RESEARCH-WAIVER trailer; got %q", warn)
	}
}

// TestR5_NoManifestStaged_Allows proves no regression: a commit with only a
// regular source file staged is allowed regardless of web research.
func TestR5_NoManifestStaged_Allows(t *testing.T) {
	root := t.TempDir()
	if err := initTestGitRepoOnBranch(t, root, "main"); err != nil {
		t.Skipf("cannot init git repo: %v", err)
	}
	src := filepath.Join(root, "main.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGitAdd(t, root, src)
	const sid = "r5-noreg-sess"
	database := makeSessionDB(t, sid, root)
	insertAgentEventFull(t, &testDB{DB: database}, "evt-r5-read3", sid, "r5-agent", "tool_call", "Read", "main.go")

	ev := &CloudEvent{
		ToolName:  "Bash",
		CWD:       root,
		ToolInput: map[string]any{"command": `git commit -m "edit main"`},
	}
	if warn := checkDependencyResearchCommitGuard(ev, database, &toolUseContext{SessionID: sid}); warn != "" {
		t.Errorf("expected ALLOW: no dependency manifest staged (no regression); got %q", warn)
	}
}
