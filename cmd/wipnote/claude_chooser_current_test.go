package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/agent"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/launcher"
)

// TestResolveCurrentSessionIDs_ExpandsFamily verifies the env+family expansion:
// the env session ID seeds the list, and every sibling sharing that session's
// family (including the long-running split-child) is included so the chooser can
// surface whichever member carries the live transcript.
func TestResolveCurrentSessionIDs_ExpandsFamily(t *testing.T) {
	root := t.TempDir()
	// Parent stub and a split-child share family "fam-1"; an unrelated session
	// belongs to a different family and must NOT be pulled in.
	if err := agent.RegisterSessionFamily(root, "sess-parent", "fam-1"); err != nil {
		t.Fatalf("register parent: %v", err)
	}
	if err := agent.RegisterSessionFamily(root, "sess-child", "fam-1"); err != nil {
		t.Fatalf("register child: %v", err)
	}
	if err := agent.RegisterSessionFamily(root, "sess-other", "fam-2"); err != nil {
		t.Fatalf("register other: %v", err)
	}

	// Only the parent stub is known from the harness env.
	t.Setenv("WIPNOTE_SESSION_ID", "sess-parent")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "")

	ids := resolveCurrentSessionIDs(root)
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["sess-parent"] {
		t.Errorf("missing env session sess-parent in %v", ids)
	}
	if !got["sess-child"] {
		t.Errorf("missing family sibling sess-child in %v", ids)
	}
	if got["sess-other"] {
		t.Errorf("unrelated-family session sess-other must not be included: %v", ids)
	}
}

// TestHasResumableOptions_CurrentOnlyCountsAsResumable is the regression guard
// for the chooseLaunchIntent gate: when the current-session slot is the ONLY
// resumable option (both harness groups empty — a split-child session with no or
// a completed work item), the chooser must NOT short-circuit to new work.
func TestHasResumableOptions_CurrentOnlyCountsAsResumable(t *testing.T) {
	cases := []struct {
		name    string
		grouped dbpkg.HarnessGroupedResumableSessions
		want    bool
	}{
		{
			name:    "nothing resumable",
			grouped: dbpkg.HarnessGroupedResumableSessions{},
			want:    false,
		},
		{
			name: "current slot only",
			grouped: dbpkg.HarnessGroupedResumableSessions{
				Current: &dbpkg.ResumableSession{Harness: "claude", LastSessionID: "sess-current"},
			},
			want: true,
		},
		{
			name: "same-harness only",
			grouped: dbpkg.HarnessGroupedResumableSessions{
				SameHarness: []dbpkg.ResumableSession{{WorkItemID: "feat-a", Harness: "claude", LastSessionID: "sess-a"}},
			},
			want: true,
		},
		{
			name: "cross-harness only",
			grouped: dbpkg.HarnessGroupedResumableSessions{
				CrossHarness: []dbpkg.ResumableSession{{WorkItemID: "feat-b", Harness: "codex", LastSessionID: "sess-b"}},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasResumableOptions(tc.grouped); got != tc.want {
				t.Fatalf("hasResumableOptions() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPromptLaunchIntent_CurrentSessionSlotAtTop verifies the first-class
// "Resume this session" entry: it renders above the same/cross-harness groups
// (numeric option 2) and resolves to a continue intent that resumes the current
// session ID, even when that session carries no work item.
func TestPromptLaunchIntent_CurrentSessionSlotAtTop(t *testing.T) {
	var out bytes.Buffer
	grouped := dbpkg.HarnessGroupedResumableSessions{
		Current: &dbpkg.ResumableSession{
			WorkItemID:    "",
			Harness:       "claude",
			LastSessionID: "sess-current",
			LastActivity:  "2026-06-19T01:00:00Z",
		},
		SameHarness: []dbpkg.ResumableSession{{
			WorkItemID:    "feat-a",
			Title:         "Alpha",
			Type:          "feature",
			Harness:       "claude",
			LastSessionID: "sess-a",
			LastActivity:  "2026-06-16T12:00:00Z",
		}},
	}

	intent, err := promptLaunchIntent(strings.NewReader("2\n"), &out, "claude", grouped)
	if err != nil {
		t.Fatalf("promptLaunchIntent() error = %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "Resume this session") {
		t.Fatalf("output missing current-session slot:\n%s", rendered)
	}
	if intent.Kind != launcher.LaunchIntentContinue {
		t.Fatalf("intent.Kind = %q, want continue", intent.Kind)
	}
	if intent.ResumeSessionID != "sess-current" {
		t.Fatalf("intent.ResumeSessionID = %q, want sess-current", intent.ResumeSessionID)
	}
}

// TestPromptLaunchIntent_CurrentSlotOrdersBeforeGroups verifies the option
// ordering: current=2, then same-harness=3. Selecting 3 must resolve to the
// same-harness row, proving the current slot did not displace the groups.
func TestPromptLaunchIntent_CurrentSlotOrdersBeforeGroups(t *testing.T) {
	var out bytes.Buffer
	grouped := dbpkg.HarnessGroupedResumableSessions{
		Current: &dbpkg.ResumableSession{
			Harness: "claude", LastSessionID: "sess-current",
		},
		SameHarness: []dbpkg.ResumableSession{{
			WorkItemID: "feat-a", Harness: "claude", LastSessionID: "sess-a",
		}},
	}

	intent, err := promptLaunchIntent(strings.NewReader("3\n"), &out, "claude", grouped)
	if err != nil {
		t.Fatalf("promptLaunchIntent() error = %v", err)
	}
	if intent.WorkItemID != "feat-a" || intent.ResumeSessionID != "sess-a" {
		t.Fatalf("option 3 resolved to %+v, want same-harness feat-a/sess-a", intent)
	}
}

// TestPromptLaunchIntent_NoCurrentSlotWhenNil verifies clean degradation: with
// Current nil the menu is unchanged — option 2 is the first same-harness row and
// no "Resume this session" header is printed.
func TestPromptLaunchIntent_NoCurrentSlotWhenNil(t *testing.T) {
	var out bytes.Buffer
	grouped := dbpkg.HarnessGroupedResumableSessions{
		SameHarness: []dbpkg.ResumableSession{{
			WorkItemID: "feat-a", Harness: "claude", LastSessionID: "sess-a",
		}},
	}

	intent, err := promptLaunchIntent(strings.NewReader("2\n"), &out, "claude", grouped)
	if err != nil {
		t.Fatalf("promptLaunchIntent() error = %v", err)
	}
	if strings.Contains(out.String(), "Resume this session") {
		t.Fatalf("unexpected current-session slot when Current is nil:\n%s", out.String())
	}
	if intent.WorkItemID != "feat-a" {
		t.Fatalf("option 2 resolved to %q, want feat-a", intent.WorkItemID)
	}
}

// TestBuildSelectOptions_IncludesCurrentSlot verifies the TUI option list puts
// the current-session entry at index 1 (right after "Start something new"),
// ahead of the same-harness rows.
func TestBuildSelectOptions_IncludesCurrentSlot(t *testing.T) {
	grouped := dbpkg.HarnessGroupedResumableSessions{
		Current: &dbpkg.ResumableSession{
			Harness: "claude", LastSessionID: "sess-current",
		},
		SameHarness: []dbpkg.ResumableSession{{
			WorkItemID: "feat-a", Harness: "claude", LastSessionID: "sess-a",
		}},
	}
	opts := buildSelectOptions("claude", grouped)
	if len(opts) != 3 {
		t.Fatalf("len(opts) = %d, want 3 (new + current + same)", len(opts))
	}
	if !strings.Contains(opts[1].Key, "Resume this session") {
		t.Fatalf("opts[1] = %q, want current-session slot", opts[1].Key)
	}
	// Index value of the current slot must be 1 so mapIndexToIntent picks
	// orderedRows[0] (the current session).
	if opts[1].Value != 1 {
		t.Fatalf("opts[1].Value = %d, want 1", opts[1].Value)
	}
}
