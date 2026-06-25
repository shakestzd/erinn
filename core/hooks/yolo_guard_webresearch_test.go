package hooks

import (
	"strings"
	"testing"
)

// --- isExternalTechEdit -----------------------------------------------------

func TestIsExternalTechEdit(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"go.mod", true},
		{"go.sum", true},
		{"package.json", true},
		{"requirements.txt", true},
		{"pyproject.toml", true},
		{"Cargo.toml", true},
		{"Gemfile", true},
		{"internal/foo/go.mod", true}, // basename match regardless of dir
		{"/abs/path/package.json", true},
		{"main.go", false},
		{"README.md", false},
		{"cmd/wipnote/check.go", false},
		{"gomod", false}, // not exactly go.mod
		{"", false},
	}
	for _, tc := range cases {
		if got := isExternalTechEdit(tc.path); got != tc.want {
			t.Errorf("isExternalTechEdit(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// --- checkExternalTechResearchGuard (pure) ----------------------------------

func TestCheckExternalTechResearchGuard_BlocksManifestEditWithoutWebResearch(t *testing.T) {
	// A go.mod edit with NO web research must block.
	warn := checkExternalTechResearchGuard("Edit", false, "go.mod", "/proj")
	if warn == "" {
		t.Fatal("expected block: go.mod edit without web research must be blocked")
	}
	if !strings.Contains(warn, "go.mod") {
		t.Errorf("block message should name the manifest; got %q", warn)
	}
}

func TestCheckExternalTechResearchGuard_AllowsManifestEditWithWebResearch(t *testing.T) {
	// Same go.mod edit but web research IS present → allow.
	if warn := checkExternalTechResearchGuard("Edit", true, "go.mod", "/proj"); warn != "" {
		t.Errorf("expected allow when web research present; got block %q", warn)
	}
}

func TestCheckExternalTechResearchGuard_AllowsNonManifestEdit(t *testing.T) {
	// Regression guard: a normal source edit is NOT external-tech, so even with
	// no web research it must be allowed by this guard (the any-read guard still
	// applies elsewhere).
	if warn := checkExternalTechResearchGuard("Edit", false, "core/hooks/foo.go", "/proj"); warn != "" {
		t.Errorf("expected allow for non-manifest edit (no regression); got block %q", warn)
	}
}

func TestCheckExternalTechResearchGuard_SkipsOutsideProject(t *testing.T) {
	// A manifest outside the project root is skipped (mirrors checkYoloResearchGuard).
	if warn := checkExternalTechResearchGuard("Edit", false, "/etc/elsewhere/go.mod", "/home/user/proj"); warn != "" {
		t.Errorf("expected skip for manifest outside project root; got block %q", warn)
	}
}

func TestCheckExternalTechResearchGuard_SkipsNonWriteTool(t *testing.T) {
	for _, tool := range []string{"Read", "Grep", "Bash", "WebSearch"} {
		if warn := checkExternalTechResearchGuard(tool, false, "go.mod", "/proj"); warn != "" {
			t.Errorf("tool %q: expected skip (not a write tool); got block %q", tool, warn)
		}
	}
}

// --- hasRecentWebResearch (DB-backed) ---------------------------------------

func TestHasRecentWebResearch_WebToolsCount(t *testing.T) {
	cases := []struct {
		name string
		tool string
		summ string
	}{
		{"WebSearch", "WebSearch", "claude code hooks"},
		{"WebFetch", "WebFetch", "https://example.com"},
		{"web_search", "web_search", "q"},
		{"web_fetch", "web_fetch", "https://x"},
		{"google_web_search", "google_web_search", "q"},
		{"gh-bash", "Bash", "gh search issues research"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tdb := setupTestDB(t)
			defer tdb.DB.Close()
			const sid, aid = "web-sess-1", "web-agent-1"
			insertResearchTestSessionWithProject(t, tdb, sid, "", "")
			insertAgentEventFull(t, tdb, "evt-web-"+tc.name, sid, aid, "tool_call", tc.tool, tc.summ)
			if !hasRecentWebResearch(tdb.DB, sid, aid, "") {
				t.Errorf("expected hasRecentWebResearch=true for %s", tc.tool)
			}
		})
	}
}

func TestHasRecentWebResearch_LocalReadDoesNotCount(t *testing.T) {
	// The crux of R1: a plain local Read must NOT satisfy web research.
	for _, tool := range []string{"Read", "Grep", "Glob"} {
		t.Run(tool, func(t *testing.T) {
			tdb := setupTestDB(t)
			defer tdb.DB.Close()
			const sid, aid = "read-only-sess", "read-only-agent"
			insertResearchTestSessionWithProject(t, tdb, sid, "", "")
			insertAgentEventFull(t, tdb, "evt-local-"+tool, sid, aid, "tool_call", tool, "core/hooks/foo.go")
			if hasRecentWebResearch(tdb.DB, sid, aid, "") {
				t.Errorf("expected hasRecentWebResearch=false: %s is a local read, not web research", tool)
			}
		})
	}
}

func TestHasRecentWebResearch_CatBashDoesNotCount(t *testing.T) {
	// `cat go.mod` is local research that the any-read guard accepts, but it must
	// NOT satisfy the web-research gate (spk-0a982f70 root cause).
	tdb := setupTestDB(t)
	defer tdb.DB.Close()
	const sid, aid = "cat-sess", "cat-agent"
	insertResearchTestSessionWithProject(t, tdb, sid, "", "")
	insertAgentEventFull(t, tdb, "evt-cat", sid, aid, "tool_call", "Bash", "cat go.mod")
	if hasRecentWebResearch(tdb.DB, sid, aid, "") {
		t.Error("expected hasRecentWebResearch=false: `cat go.mod` is not web research")
	}
}

func TestHasRecentWebResearch_FailsOpenOnRecordingGap(t *testing.T) {
	// Only a SessionStart event (no tool_call) → recording gap → fail-open.
	tdb := setupTestDB(t)
	defer tdb.DB.Close()
	const sid, aid = "gap-web-sess", "gap-web-agent"
	insertResearchTestSessionWithProject(t, tdb, sid, "", "")
	insertAgentEventFull(t, tdb, "evt-gap-web-start", sid, aid, "start", "", "")
	if !hasRecentWebResearch(tdb.DB, sid, aid, "") {
		t.Error("expected hasRecentWebResearch=true (fail-open): no tool_call events recorded")
	}
}

// --- End-to-end verify scenarios (R1 acceptance criteria) -------------------

// TestR1_OnlyReadsPlusManifestEdit_Blocks proves: a session with only local
// Reads attempting an external-lib (go.mod) edit is BLOCKED.
func TestR1_OnlyReadsPlusManifestEdit_Blocks(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()
	const sid, aid = "r1-block-sess", "r1-block-agent"
	insertResearchTestSessionWithProject(t, tdb, sid, "", "")
	insertAgentEventFull(t, tdb, "evt-r1-read", sid, aid, "tool_call", "Read", "go.mod")

	hasWeb := hasRecentWebResearch(tdb.DB, sid, aid, "")
	if hasWeb {
		t.Fatal("precondition failed: only a Read happened, hasRecentWebResearch should be false")
	}
	if warn := checkExternalTechResearchGuard("Edit", hasWeb, "go.mod", ""); warn == "" {
		t.Error("expected BLOCK: only-Reads session editing go.mod must be blocked")
	}
}

// TestR1_PriorWebSearchPlusManifestEdit_Allows proves: the same go.mod edit
// after a prior WebSearch is ALLOWED.
func TestR1_PriorWebSearchPlusManifestEdit_Allows(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()
	const sid, aid = "r1-allow-sess", "r1-allow-agent"
	insertResearchTestSessionWithProject(t, tdb, sid, "", "")
	insertAgentEventFull(t, tdb, "evt-r1-read2", sid, aid, "tool_call", "Read", "go.mod")
	insertAgentEventFull(t, tdb, "evt-r1-web", sid, aid, "tool_call", "WebSearch", "go module version")

	hasWeb := hasRecentWebResearch(tdb.DB, sid, aid, "")
	if !hasWeb {
		t.Fatal("precondition failed: a WebSearch happened, hasRecentWebResearch should be true")
	}
	if warn := checkExternalTechResearchGuard("Edit", hasWeb, "go.mod", ""); warn != "" {
		t.Errorf("expected ALLOW: go.mod edit after WebSearch must be allowed; got %q", warn)
	}
}

// TestR1_NonManifestEditAfterRead_Allows proves: a non-external-tech edit after
// one Read is still ALLOWED (no regression on ordinary code edits).
func TestR1_NonManifestEditAfterRead_Allows(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()
	const sid, aid = "r1-noreg-sess", "r1-noreg-agent"
	insertResearchTestSessionWithProject(t, tdb, sid, "", "")
	insertAgentEventFull(t, tdb, "evt-r1-read3", sid, aid, "tool_call", "Read", "core/hooks/foo.go")

	// foo.go is not a dependency manifest → guard never fires regardless of web research.
	if warn := checkExternalTechResearchGuard("Edit", false, "core/hooks/foo.go", ""); warn != "" {
		t.Errorf("expected ALLOW: ordinary source edit after a Read must not be blocked; got %q", warn)
	}
}

// TestHasRecentWebResearch_CodexWebAndShellTools verifies the roborev
// #563/#566/#570 fix: the web-research detector recognizes Codex's web.* tools
// and counts `gh ...` run through Codex shell tool names, matching
// isOrchestratorResearchTool / isShellTool — so valid research is not falsely
// rejected.
func TestHasRecentWebResearch_CodexWebAndShellTools(t *testing.T) {
	cases := []struct {
		name string
		tool string
		summ string
	}{
		{"web.search_query", "web.search_query", "go module docs"},
		{"web.open", "web.open", "https://pkg.go.dev"},
		{"web.find", "web.find", "require"},
		{"web.click", "web.click", "link"},
		{"gh-exec_command", "exec_command", "gh search issues foo"},
		{"gh-functions.exec_command", "functions.exec_command", "gh api repos/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tdb := setupTestDB(t)
			defer tdb.DB.Close()
			sid, aid := "codexweb-"+tc.name, "codexweb-agent"
			insertResearchTestSessionWithProject(t, tdb, sid, "", "")
			insertAgentEventFull(t, tdb, "evt-cw-"+tc.name, sid, aid, "tool_call", tc.tool, tc.summ)
			if !hasRecentWebResearch(tdb.DB, sid, aid, "") {
				t.Errorf("expected hasRecentWebResearch=true for %s/%q", tc.tool, tc.summ)
			}
		})
	}
}

// --- apply_patch path extraction (roborev #563/#566) ------------------------

func TestApplyPatchTouchedPaths(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Update File: go.mod\n@@\n+require github.com/x/y v1.0.0\n" +
		"*** Add File: internal/new.go\n+package internal\n" +
		"*** Delete File: old/legacy.go\n" +
		"*** Update File: src/app.js\n*** Move to: src/app2.js\n" +
		"*** End Patch\n"
	got := applyPatchTouchedPaths(patch)
	want := map[string]bool{"go.mod": true, "internal/new.go": true, "old/legacy.go": true, "src/app.js": true, "src/app2.js": true}
	if len(got) != len(want) {
		t.Fatalf("applyPatchTouchedPaths returned %v, want %d paths", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path %q in %v", p, got)
		}
	}
	if applyPatchTouchedPaths("") != nil {
		t.Error("empty patch must yield nil")
	}
}

// TestEditTargetPaths_ApplyPatchManifest proves an apply_patch that touches a
// dependency manifest is detected (previously bypassed because extractFilePath
// returned "" for patch payloads).
func TestEditTargetPaths_ApplyPatchManifest(t *testing.T) {
	ev := &CloudEvent{
		ToolName: "apply_patch",
		ToolInput: map[string]any{
			"patch": "*** Begin Patch\n*** Update File: go.mod\n+require github.com/x/y v1.2.3\n*** End Patch\n",
		},
	}
	paths := editTargetPaths(ev)
	if got := firstPathMatching(paths, isExternalTechEdit); got != "go.mod" {
		t.Errorf("expected apply_patch go.mod edit to be detected as external-tech; paths=%v match=%q", paths, got)
	}

	// A Write still resolves via extractFilePath.
	wev := &CloudEvent{ToolName: "Write", ToolInput: map[string]any{"file_path": "package.json", "content": "{}"}}
	if got := firstPathMatching(editTargetPaths(wev), isExternalTechEdit); got != "package.json" {
		t.Errorf("expected Write package.json to be detected; got %q", got)
	}
}

// TestEditTargetPaths_ApplyPatchHarness proves an apply_patch touching a
// harness-contract file is detected.
func TestEditTargetPaths_ApplyPatchHarness(t *testing.T) {
	ev := &CloudEvent{
		ToolName: "apply_patch",
		ToolInput: map[string]any{
			"patch": "*** Begin Patch\n*** Update File: plugin/agents/patch-coder.md\n+tools\n*** End Patch\n",
		},
	}
	if got := firstPathMatching(editTargetPaths(ev), isHarnessContractEdit); got != "plugin/agents/patch-coder.md" {
		t.Errorf("expected apply_patch harness edit detected; got %q", got)
	}
}
