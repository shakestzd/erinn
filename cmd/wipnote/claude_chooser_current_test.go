package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

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

func TestShortSessionID(t *testing.T) {
	cases := map[string]string{
		"6ef24b5a-c9e2-4501-901e-b15de323782f": "6ef24b5a",
		"019ede12-3456":                        "019ede12",
		"abcdefghij":                           "abcdefgh", // no dash, first 8
		"short":                                "short",
		"":                                     "",
	}
	for in, want := range cases {
		if got := shortSessionID(in); got != want {
			t.Errorf("shortSessionID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateTitle(t *testing.T) {
	if got := truncateTitle("short title", 44); got != "short title" {
		t.Errorf("short unchanged: got %q", got)
	}
	long := "Plan renderer strips literal angle-bracket placeholders and derives status"
	got := truncateTitle(long, 44)
	if n := len([]rune(got)); n > 44 {
		t.Errorf("truncated len = %d, want <= 44: %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated should end with ellipsis: %q", got)
	}
}

func TestRelativeTime(t *testing.T) {
	if got := relativeTime(""); got != "" {
		t.Errorf("empty: got %q, want empty", got)
	}
	if got := relativeTime("not-a-timestamp"); got == "" {
		t.Errorf("unparseable should fall back to raw, got empty")
	}
	// Far past → calendar date fallback.
	if got := relativeTime("2000-01-02T03:04:05Z"); got != "2000-01-02" {
		t.Errorf("old date: got %q, want 2000-01-02", got)
	}
	// Fractional-second RFC3339 (as messages.timestamp stores) must parse.
	old := time.Now().UTC().Add(-3 * time.Hour).Format("2006-01-02T15:04:05.000Z07:00")
	if got := relativeTime(old); got != "3h ago" {
		t.Errorf("3h fractional: got %q, want 3h ago", got)
	}
	// SQLite CURRENT_TIMESTAMP form ("2006-01-02 15:04:05", UTC, no zone) must
	// also render as a relative delta, not a bare date.
	sqliteNow := time.Now().UTC().Add(-5 * time.Minute).Format("2006-01-02 15:04:05")
	if got := relativeTime(sqliteNow); got != "5m ago" {
		t.Errorf("sqlite datetime: got %q, want 5m ago", got)
	}
}

// TestDescribeCurrentSession_ShowsSessionID is the direct fix for the "looks
// hardwired" feedback: the current-session slot must surface its short session
// ID so the user can see which session it resolves to.
func TestDescribeCurrentSession_ShowsSessionID(t *testing.T) {
	row := dbpkg.ResumableSession{
		Harness:       "claude",
		LastSessionID: "6ef24b5a-c9e2-4501-901e-b15de323782f",
		LastActivity:  "2000-01-02T03:04:05Z",
	}
	got := describeCurrentSession(row)
	if !strings.Contains(got, "6ef24b5a") {
		t.Fatalf("current-session description missing short session id: %q", got)
	}
	if strings.Contains(got, "6ef24b5a-c9e2-4501") {
		t.Fatalf("should show SHORT id, not the full UUID: %q", got)
	}
}

// TestIsActionableCurrentSession guards the slot from producing a degenerate
// continue intent. With no work item, only a same-harness session on a harness
// that resumes natively by session ID (claude/codex) is actionable. Any row with
// a work item is actionable. Cross-harness or non-native-resume rows without a
// work item are not — their continuation context would bail and launch fresh.
func TestIsActionableCurrentSession(t *testing.T) {
	cases := []struct {
		name    string
		row     dbpkg.ResumableSession
		harness string
		want    bool
	}{
		{
			name:    "same harness with resume id",
			row:     dbpkg.ResumableSession{Harness: "claude", LastSessionID: "sess-cur"},
			harness: "claude",
			want:    true,
		},
		{
			name:    "same harness without resume id",
			row:     dbpkg.ResumableSession{Harness: "claude", LastSessionID: ""},
			harness: "claude",
			want:    false,
		},
		{
			name:    "codex same harness with resume id (native resume)",
			row:     dbpkg.ResumableSession{Harness: "codex", LastSessionID: "sess-cur"},
			harness: "codex",
			want:    true,
		},
		{
			// Gemini resumes by numeric index, not the stored session ID — a
			// resume-ID-only slot is not actionable without a work item.
			name:    "gemini same harness resume-id-only not actionable",
			row:     dbpkg.ResumableSession{Harness: "gemini", LastSessionID: "sess-cur"},
			harness: "gemini",
			want:    false,
		},
		{
			name:    "gemini same harness with work item is actionable",
			row:     dbpkg.ResumableSession{Harness: "gemini", LastSessionID: "sess-cur", WorkItemID: "feat-a"},
			harness: "gemini",
			want:    true,
		},
		{
			name:    "antigravity same harness resume-id-only not actionable",
			row:     dbpkg.ResumableSession{Harness: "antigravity", LastSessionID: "sess-cur"},
			harness: "antigravity",
			want:    false,
		},
		{
			name:    "cross harness with no context",
			row:     dbpkg.ResumableSession{Harness: "codex", LastSessionID: "sess-x"},
			harness: "claude",
			want:    false,
		},
		{
			name:    "cross harness with work item",
			row:     dbpkg.ResumableSession{Harness: "codex", WorkItemID: "feat-a", LastSessionID: "sess-x"},
			harness: "claude",
			want:    true,
		},
		{
			// Worktree without a work item is NOT actionable: the continue-context
			// path drops the worktree when WorkItemID is empty.
			name:    "cross harness with worktree but no work item",
			row:     dbpkg.ResumableSession{Harness: "codex", ExecWorktreePath: ".claude/worktrees/feat-a"},
			harness: "claude",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isActionableCurrentSession(tc.row, tc.harness); got != tc.want {
				t.Fatalf("isActionableCurrentSession() = %v, want %v", got, tc.want)
			}
		})
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
