package hooks

import (
	"strings"
	"testing"
)

// --- isHarnessContractEdit --------------------------------------------------

func TestIsHarnessContractEdit(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"plugin/agents/patch-coder.md", true},
		{"plugin/hooks/hooks.json", true},
		{"packages/plugin-core/manifest.json", true},
		{"port/pluginbuild/agent_frontmatter.go", true},
		{"cmd/wipnote/prompts/system-prompt.md", true},
		{"/abs/repo/plugin/agents/researcher.md", true},
		{"core/hooks/yolo_guard.go", false},
		{"cmd/wipnote/check.go", false},
		{"README.md", false},
		{"go.mod", false}, // dependency manifest is R1's domain, not harness-contract
		{"", false},
	}
	for _, tc := range cases {
		if got := isHarnessContractEdit(tc.path); got != tc.want {
			t.Errorf("isHarnessContractEdit(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// --- checkHarnessContractResearchGuard (pure) -------------------------------

func TestCheckHarnessContractResearchGuard_BlocksHarnessEditWithoutWebResearch(t *testing.T) {
	warn := checkHarnessContractResearchGuard("Edit", false, "plugin/agents/feature-coder.md", "/proj")
	if warn == "" {
		t.Fatal("expected block: harness-contract edit without web research must be blocked")
	}
	if !strings.Contains(warn, "feature-coder.md") {
		t.Errorf("block message should name the harness file; got %q", warn)
	}
}

func TestCheckHarnessContractResearchGuard_AllowsHarnessEditWithWebResearch(t *testing.T) {
	if warn := checkHarnessContractResearchGuard("Write", true, "packages/plugin-core/manifest.json", "/proj"); warn != "" {
		t.Errorf("expected allow when web research present; got block %q", warn)
	}
}

func TestCheckHarnessContractResearchGuard_AllowsNonHarnessEdit(t *testing.T) {
	// Ordinary source edit is not harness-contract → never blocked here (no regression).
	if warn := checkHarnessContractResearchGuard("Edit", false, "core/hooks/foo.go", "/proj"); warn != "" {
		t.Errorf("expected allow for non-harness edit (no regression); got block %q", warn)
	}
}

func TestCheckHarnessContractResearchGuard_SkipsOutsideProject(t *testing.T) {
	if warn := checkHarnessContractResearchGuard("Edit", false, "/elsewhere/plugin/agents/x.md", "/home/user/proj"); warn != "" {
		t.Errorf("expected skip for harness file outside project root; got block %q", warn)
	}
}

func TestCheckHarnessContractResearchGuard_SkipsNonWriteTool(t *testing.T) {
	for _, tool := range []string{"Read", "Grep", "Bash", "WebSearch"} {
		if warn := checkHarnessContractResearchGuard(tool, false, "plugin/agents/x.md", "/proj"); warn != "" {
			t.Errorf("tool %q: expected skip (not a write tool); got block %q", tool, warn)
		}
	}
}

// --- End-to-end verify scenarios (R4 acceptance criteria) -------------------

// TestR4_HarnessEditNoWebResearch_Blocks proves: a harness-integration edit with
// no web research in the session is BLOCKED (only local reads do not satisfy it).
func TestR4_HarnessEditNoWebResearch_Blocks(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()
	const sid, aid = "r4-block-sess", "r4-block-agent"
	insertResearchTestSessionWithProject(t, tdb, sid, "", "")
	insertAgentEventFull(t, tdb, "evt-r4-read", sid, aid, "tool_call", "Read", "plugin/agents/patch-coder.md")

	hasWeb := hasRecentWebResearch(tdb.DB, sid, aid, "")
	if hasWeb {
		t.Fatal("precondition failed: only a Read happened, hasRecentWebResearch should be false")
	}
	if warn := checkHarnessContractResearchGuard("Edit", hasWeb, "plugin/agents/patch-coder.md", ""); warn == "" {
		t.Error("expected BLOCK: harness-contract edit with no web research must be blocked")
	}
}

// TestR4_HarnessEditWithWebResearch_Allows proves: the same harness edit after a
// WebSearch is ALLOWED.
func TestR4_HarnessEditWithWebResearch_Allows(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()
	const sid, aid = "r4-allow-sess", "r4-allow-agent"
	insertResearchTestSessionWithProject(t, tdb, sid, "", "")
	insertAgentEventFull(t, tdb, "evt-r4-web", sid, aid, "tool_call", "WebFetch", "https://code.claude.com/docs/en/sub-agents")

	hasWeb := hasRecentWebResearch(tdb.DB, sid, aid, "")
	if !hasWeb {
		t.Fatal("precondition failed: a WebFetch happened, hasRecentWebResearch should be true")
	}
	if warn := checkHarnessContractResearchGuard("Edit", hasWeb, "plugin/agents/patch-coder.md", ""); warn != "" {
		t.Errorf("expected ALLOW: harness edit after WebFetch must be allowed; got %q", warn)
	}
}
