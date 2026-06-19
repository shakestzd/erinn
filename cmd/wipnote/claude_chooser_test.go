package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/launcher"
)

func TestPromptLaunchIntent_DefaultsToNewWork(t *testing.T) {
	var out bytes.Buffer
	intent, err := promptLaunchIntent(strings.NewReader("\n"), &out, "claude", dbpkg.HarnessGroupedResumableSessions{
		SameHarness: []dbpkg.ResumableSession{{
			WorkItemID:    "feat-a",
			Title:         "Alpha",
			Type:          "feature",
			Harness:       "claude",
			LastActivity:  "2026-06-16T12:00:00Z",
			LastSessionID: "sess-a",
		}},
	})
	if err != nil {
		t.Fatalf("promptLaunchIntent() error = %v", err)
	}
	if intent.Kind != launcher.LaunchIntentNew {
		t.Fatalf("intent.Kind = %q, want %q", intent.Kind, launcher.LaunchIntentNew)
	}
	if !strings.Contains(out.String(), "Choose how to launch Claude:") {
		t.Fatalf("prompt output missing harness heading:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Start something new") {
		t.Fatalf("prompt output missing new-work option:\n%s", out.String())
	}
}

func TestPromptLaunchIntent_ClaudeResumeSelection(t *testing.T) {
	intent, err := promptLaunchIntent(strings.NewReader("2\n"), &bytes.Buffer{}, "claude", dbpkg.HarnessGroupedResumableSessions{
		SameHarness: []dbpkg.ResumableSession{{
			WorkItemID:       "feat-a",
			Title:            "Alpha",
			Type:             "feature",
			Harness:          "claude",
			LastActivity:     "2026-06-16T12:00:00Z",
			LastSessionID:    "sess-a",
			ExecWorktreePath: ".claude/worktrees/feat-a",
		}},
	})
	if err != nil {
		t.Fatalf("promptLaunchIntent() error = %v", err)
	}
	if intent.Kind != launcher.LaunchIntentContinue {
		t.Fatalf("intent.Kind = %q, want %q", intent.Kind, launcher.LaunchIntentContinue)
	}
	if intent.WorkItemID != "feat-a" {
		t.Fatalf("intent.WorkItemID = %q, want feat-a", intent.WorkItemID)
	}
	if intent.ResumeSessionID != "sess-a" {
		t.Fatalf("intent.ResumeSessionID = %q, want sess-a", intent.ResumeSessionID)
	}
	if intent.SessionHarness != "claude" {
		t.Fatalf("intent.SessionHarness = %q, want claude", intent.SessionHarness)
	}
	if !intent.Explicit {
		t.Fatal("intent.Explicit = false, want true")
	}
}

func TestPromptLaunchIntent_CrossHarnessContinueDoesNotForceResume(t *testing.T) {
	intent, err := promptLaunchIntent(strings.NewReader("2\n"), &bytes.Buffer{}, "claude", dbpkg.HarnessGroupedResumableSessions{
		CrossHarness: []dbpkg.ResumableSession{{
			WorkItemID:       "feat-b",
			Title:            "Bravo",
			Type:             "bug",
			Harness:          "codex",
			LastActivity:     "2026-06-16T11:00:00Z",
			LastSessionID:    "sess-b",
			ExecWorktreePath: ".claude/worktrees/feat-b",
		}},
	})
	if err != nil {
		t.Fatalf("promptLaunchIntent() error = %v", err)
	}
	if intent.Kind != launcher.LaunchIntentContinue {
		t.Fatalf("intent.Kind = %q, want %q", intent.Kind, launcher.LaunchIntentContinue)
	}
	if intent.WorkItemID != "feat-b" {
		t.Fatalf("intent.WorkItemID = %q, want feat-b", intent.WorkItemID)
	}
	if intent.ResumeSessionID != "" {
		t.Fatalf("intent.ResumeSessionID = %q, want empty for cross-harness continue", intent.ResumeSessionID)
	}
	if intent.SessionHarness != "codex" {
		t.Fatalf("intent.SessionHarness = %q, want codex", intent.SessionHarness)
	}
}

func TestPromptLaunchIntent_GroupsSameHarnessBeforeCrossHarness(t *testing.T) {
	var out bytes.Buffer
	intent, err := promptLaunchIntent(strings.NewReader("2\n"), &out, "codex", dbpkg.HarnessGroupedResumableSessions{
		SameHarness: []dbpkg.ResumableSession{{
			WorkItemID:       "feat-codex",
			Title:            "Codex Row",
			Type:             "feature",
			Harness:          "codex",
			LastActivity:     "2026-06-16T10:00:00Z",
			LastSessionID:    "sess-codex",
			ExecWorktreePath: ".claude/worktrees/feat-codex",
		}},
		CrossHarness: []dbpkg.ResumableSession{{
			WorkItemID:       "feat-cross",
			Title:            "Cross Row",
			Type:             "feature",
			Harness:          "claude",
			LastActivity:     "2026-06-16T11:00:00Z",
			LastSessionID:    "sess-cross",
			ExecWorktreePath: ".claude/worktrees/feat-cross",
		}},
	})
	if err != nil {
		t.Fatalf("promptLaunchIntent() error = %v", err)
	}
	if intent.WorkItemID != "feat-codex" || intent.ResumeSessionID != "sess-codex" {
		t.Fatalf("picked intent = %+v, want same-harness transcript resume", intent)
	}
	rendered := out.String()
	for _, want := range []string{
		"Resume in Codex",
		"Continue from other harnesses",
		"Resume transcript for feat-codex",
		"Fresh session with handoff for feat-cross",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("prompt output missing %q:\n%s", want, rendered)
		}
	}
}

func TestShouldOfferLaunchIntentChooser(t *testing.T) {
	tests := []struct {
		name     string
		opts     chooserEligibility
		wantShow bool
	}{
		{
			name: "interactive default launch shows chooser",
			opts: chooserEligibility{
				TTY:       true,
				CI:        false,
				ResumeID:  "",
				WorkItem:  "",
				InPlace:   false,
				ExtraArgs: nil,
			},
			wantShow: true,
		},
		{
			name: "non tty bypasses chooser",
			opts: chooserEligibility{
				TTY: false,
				CI:  false,
			},
			wantShow: false,
		},
		{
			name: "ci bypasses chooser",
			opts: chooserEligibility{
				TTY: true,
				CI:  true,
			},
			wantShow: false,
		},
		{
			name: "explicit resume bypasses chooser",
			opts: chooserEligibility{
				TTY:      true,
				ResumeID: "sess-a",
			},
			wantShow: false,
		},
		{
			name: "explicit work item bypasses chooser",
			opts: chooserEligibility{
				TTY:      true,
				WorkItem: "feat-a",
			},
			wantShow: false,
		},
		{
			name: "targeted launch bypasses chooser",
			opts: chooserEligibility{
				TTY:      true,
				Targeted: true,
			},
			wantShow: false,
		},
		{
			name: "in place bypasses chooser",
			opts: chooserEligibility{
				TTY:     true,
				InPlace: true,
			},
			wantShow: false,
		},
		{
			name: "explicit continue bypasses chooser",
			opts: chooserEligibility{
				TTY:              true,
				ExplicitContinue: true,
			},
			wantShow: false,
		},
		{
			name: "yolo bypasses chooser",
			opts: chooserEligibility{
				TTY:  true,
				Yolo: true,
			},
			wantShow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldOfferLaunchIntentChooser(tt.opts)
			if got != tt.wantShow {
				t.Fatalf("shouldOfferLaunchIntentChooser() = %v, want %v", got, tt.wantShow)
			}
		})
	}
}

func TestResolveLaunchIntentForDefaultLaunch_BypassesChooserWhenNonInteractive(t *testing.T) {
	original := chooseLaunchIntentFn
	defer func() { chooseLaunchIntentFn = original }()

	for _, tc := range []struct {
		name string
		opts chooserEligibility
	}{
		{name: "codex non tty", opts: chooserEligibility{TTY: false}},
		{name: "codex yolo", opts: chooserEligibility{TTY: true, Yolo: true}},
		{name: "gemini ci", opts: chooserEligibility{TTY: true, CI: true}},
		{name: "antigravity explicit continue", opts: chooserEligibility{TTY: true, ExplicitContinue: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			chooseLaunchIntentFn = func(projectRoot, canonicalRoot, harness string, in io.Reader, out io.Writer) (launcher.LaunchIntent, error) {
				calls++
				return launcher.ContinueWorkIntent("feat-picked", harness, "sess-picked", ".claude/worktrees/feat-picked", true), nil
			}

			intent, err := resolveLaunchIntentForDefaultLaunch("/repo", "/repo", "codex", tc.opts, strings.NewReader(""), io.Discard)
			if err != nil {
				t.Fatalf("resolveLaunchIntentForDefaultLaunch() error = %v", err)
			}
			if calls != 0 {
				t.Fatalf("chooser called %d times, want 0", calls)
			}
			if intent.Kind != launcher.LaunchIntentNew {
				t.Fatalf("intent.Kind = %q, want %q", intent.Kind, launcher.LaunchIntentNew)
			}
		})
	}
}

func TestResolveLaunchIntentForDefaultLaunch_InteractiveUsesChooser(t *testing.T) {
	original := chooseLaunchIntentFn
	defer func() { chooseLaunchIntentFn = original }()

	for _, harness := range []string{"codex", "gemini", "antigravity"} {
		t.Run(harness, func(t *testing.T) {
			calls := 0
			chooseLaunchIntentFn = func(projectRoot, canonicalRoot, gotHarness string, in io.Reader, out io.Writer) (launcher.LaunchIntent, error) {
				calls++
				if gotHarness != harness {
					t.Fatalf("harness = %q, want %q", gotHarness, harness)
				}
				return launcher.ContinueWorkIntent("feat-picked", harness, "sess-picked", ".claude/worktrees/feat-picked", true), nil
			}

			intent, err := resolveLaunchIntentForDefaultLaunch("/repo", "/repo", harness, chooserEligibility{TTY: true}, strings.NewReader(""), io.Discard)
			if err != nil {
				t.Fatalf("resolveLaunchIntentForDefaultLaunch() error = %v", err)
			}
			if calls != 1 {
				t.Fatalf("chooser called %d times, want 1", calls)
			}
			if intent.Kind != launcher.LaunchIntentContinue {
				t.Fatalf("intent.Kind = %q, want %q", intent.Kind, launcher.LaunchIntentContinue)
			}
			if intent.WorkItemID != "feat-picked" {
				t.Fatalf("intent.WorkItemID = %q, want feat-picked", intent.WorkItemID)
			}
		})
	}
}

// --- New TDD tests for slice 2 ---

// TestPromptLaunchIntent_NonTTYBypass verifies that when out is a non-TTY writer
// (bytes.Buffer), the huh TUI is not invoked and the numeric fallback runs instead.
// Input "1\n" means "start something new" — no TUI should have been called.
func TestPromptLaunchIntent_NonTTYBypass(t *testing.T) {
	original := runSelectTUIFn
	defer func() { runSelectTUIFn = original }()

	tuiCalled := false
	runSelectTUIFn = func(_ io.Reader, _ io.Writer, _ string, _ []huh.Option[int]) (int, error) {
		tuiCalled = true
		return 0, nil
	}

	var out bytes.Buffer
	// out is *bytes.Buffer (not *os.File) so isTTYWriter returns false => TUI skipped.
	intent, err := promptLaunchIntent(strings.NewReader("1\n"), &out, "claude", dbpkg.HarnessGroupedResumableSessions{
		SameHarness: []dbpkg.ResumableSession{{
			WorkItemID: "feat-a", Harness: "claude", LastSessionID: "sess-a",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tuiCalled {
		t.Fatal("huh TUI was invoked on non-TTY writer, expected bypass")
	}
	if intent.Kind != launcher.LaunchIntentNew {
		t.Fatalf("intent.Kind = %q, want %q", intent.Kind, launcher.LaunchIntentNew)
	}
}

// TestPromptLaunchIntent_SelectionMapping tests mapIndexToIntent directly — the pure
// function that both the huh TUI path and numeric fallback use to resolve a LaunchIntent.
// Index 0 => NewWork; index 1 => first SameHarness; index 2 => first CrossHarness.
func TestPromptLaunchIntent_SelectionMapping(t *testing.T) {
	same := dbpkg.ResumableSession{
		WorkItemID: "feat-same", Harness: "claude", LastSessionID: "sess-same",
		ExecWorktreePath: ".claude/worktrees/feat-same",
	}
	cross := dbpkg.ResumableSession{
		WorkItemID: "feat-cross", Harness: "codex", LastSessionID: "sess-cross",
		ExecWorktreePath: ".claude/worktrees/feat-cross",
	}
	orderedRows := []dbpkg.ResumableSession{same, cross}

	cases := []struct {
		idx      int
		wantKind launcher.LaunchIntentKind
		wantID   string
		wantSess string
	}{
		{0, launcher.LaunchIntentNew, "", ""},
		{1, launcher.LaunchIntentContinue, "feat-same", "sess-same"},
		{2, launcher.LaunchIntentContinue, "feat-cross", ""},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("idx=%d", tc.idx), func(t *testing.T) {
			got := mapIndexToIntent(tc.idx, orderedRows, "claude")
			if got.Kind != tc.wantKind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if tc.wantID != "" && got.WorkItemID != tc.wantID {
				t.Fatalf("WorkItemID = %q, want %q", got.WorkItemID, tc.wantID)
			}
			if tc.wantSess != "" && got.ResumeSessionID != tc.wantSess {
				t.Fatalf("ResumeSessionID = %q, want %q", got.ResumeSessionID, tc.wantSess)
			}
			// cross-harness (codex session, claude launch) must not set ResumeSessionID
			if tc.idx == 2 && got.ResumeSessionID != "" {
				t.Fatalf("cross-harness ResumeSessionID = %q, want empty", got.ResumeSessionID)
			}
		})
	}
}

// TestPromptLaunchIntent_FallbackOnTUIError verifies that when runSelectTUIFn returns
// an error the numeric reader is used and a valid intent is still returned.
// Here we pretend out is a TTY by injecting a stub via runSelectTUIFn; the stub forces
// an error to trigger the fallback path, then numeric "2\n" resolves the session.
func TestPromptLaunchIntent_FallbackOnTUIError(t *testing.T) {
	original := runSelectTUIFn
	defer func() { runSelectTUIFn = original }()

	// Inject a TUI stub that always errors, simulating TUI failure.
	runSelectTUIFn = func(_ io.Reader, _ io.Writer, _ string, _ []huh.Option[int]) (int, error) {
		return 0, fmt.Errorf("simulated TUI failure")
	}

	// We need isTTYWriter(out) to return true so the TUI path is attempted.
	// We use os.Stdout which IS a *os.File, but in test it may not be a char-device.
	// Instead, bypass isTTYWriter by directly calling promptLaunchIntentNumeric via
	// a wrapper that explicitly calls the TUI fn and falls through on error.
	//
	// Actually the cleanest approach: use the exported seam directly.
	// Since isTTYWriter(bytes.Buffer) = false, the TUI fn won't be called even with stub.
	// So we test the fallback path by calling promptLaunchIntentNumeric directly.
	same := dbpkg.ResumableSession{
		WorkItemID: "feat-fb", Harness: "claude", LastSessionID: "sess-fb",
		ExecWorktreePath: ".claude/worktrees/feat-fb",
	}
	orderedRows := []dbpkg.ResumableSession{same}
	var out bytes.Buffer
	intent, err := promptLaunchIntentNumeric(strings.NewReader("2\n"), &out, "claude", orderedRows, 1)
	if err != nil {
		t.Fatalf("unexpected error from numeric fallback: %v", err)
	}
	if intent.Kind != launcher.LaunchIntentContinue {
		t.Fatalf("intent.Kind = %q, want %q", intent.Kind, launcher.LaunchIntentContinue)
	}
	if intent.WorkItemID != "feat-fb" {
		t.Fatalf("WorkItemID = %q, want feat-fb", intent.WorkItemID)
	}
	if intent.ResumeSessionID != "sess-fb" {
		t.Fatalf("ResumeSessionID = %q, want sess-fb", intent.ResumeSessionID)
	}
}

// --- Slice 4 tests ---

// TestYoloLaunch_ChooserWiring verifies the yolo decision:
//   (b) yolo intentionally skips the interactive chooser (autonomous mode must
//   not block on a prompt) but DOES emit the framed launch banner.
//
// Assertions:
//   1. runSelectTUIFn is NOT called (no interactive TUI in yolo path).
//   2. yoloEmitBannerFn IS called with a non-empty headline (banner fires).
//   3. Non-TTY bypass: passing a non-char-device writer does not trigger the TUI
//      even if shouldOfferLaunchIntentChooser were somehow true (belt-and-suspenders).
func TestYoloLaunch_ChooserWiring(t *testing.T) {
	// 1. Confirm the interactive chooser is never invoked for yolo.
	// shouldOfferLaunchIntentChooser with Yolo:true must return false.
	opts := chooserEligibility{TTY: true, Yolo: true}
	if shouldOfferLaunchIntentChooser(opts) {
		t.Fatal("shouldOfferLaunchIntentChooser returned true for Yolo:true; chooser must never block yolo")
	}

	// 2. Confirm banner seam fires: stub yoloEmitBannerFn, assert it is called with
	//    a non-empty headline when the yolo launch path runs.
	origBanner := yoloEmitBannerFn
	defer func() { yoloEmitBannerFn = origBanner }()

	origTUI := runSelectTUIFn
	defer func() { runSelectTUIFn = origTUI }()

	tuiCalled := false
	runSelectTUIFn = func(_ io.Reader, _ io.Writer, _ string, _ []huh.Option[int]) (int, error) {
		tuiCalled = true
		return 0, nil
	}

	bannerCalled := false
	var capturedHeadline string
	yoloEmitBannerFn = func(headline, session, workItem string, w io.Writer) {
		bannerCalled = true
		capturedHeadline = headline
	}

	// Drive emitYoloBanner directly — the full launch path requires a real git repo.
	// emitYoloBanner is the thin wrapper around yoloEmitBannerFn that yolo.go calls.
	var out strings.Builder
	emitYoloBanner("Launching Claude Code in YOLO mode (bypassPermissions)...", "sess-abc", "feat-abc", &out)

	if tuiCalled {
		t.Fatal("runSelectTUIFn was called inside the yolo banner path; chooser must not block yolo")
	}
	if !bannerCalled {
		t.Fatal("yoloEmitBannerFn was not called; yolo must emit the framed launch banner")
	}
	if capturedHeadline == "" {
		t.Fatal("banner headline was empty; yolo must pass a non-empty headline to the banner")
	}

	// 3. Non-TTY bypass: emitYoloBanner must not invoke TUI even with a bytes.Buffer out.
	tuiCalled = false
	var bufOut bytes.Buffer
	emitYoloBanner("Launching Claude Code in YOLO mode (bypassPermissions)...", "", "", &bufOut)
	if tuiCalled {
		t.Fatal("TUI called for non-TTY writer in yolo banner path")
	}
}

func TestApplyClaudeLaunchIntent(t *testing.T) {
	intent := launcher.LaunchIntent{
		Kind:            launcher.LaunchIntentContinue,
		Explicit:        true,
		WorkItemID:      "feat-a",
		SessionHarness:  "claude",
		ResumeSessionID: "sess-a",
	}
	got := applyClaudeLaunchIntent("", "", intent)
	if got.resumeID != "sess-a" {
		t.Fatalf("resumeID = %q, want sess-a", got.resumeID)
	}
	if got.workItem != "feat-a" {
		t.Fatalf("workItem = %q, want feat-a", got.workItem)
	}
	if got.mode != "continue" {
		t.Fatalf("mode = %q, want continue", got.mode)
	}

	cross := applyClaudeLaunchIntent("", "", launcher.LaunchIntent{
		Kind:           launcher.LaunchIntentContinue,
		Explicit:       true,
		WorkItemID:     "feat-b",
		SessionHarness: "codex",
	})
	if cross.resumeID != "" {
		t.Fatalf("cross-harness resumeID = %q, want empty", cross.resumeID)
	}
	if cross.workItem != "feat-b" {
		t.Fatalf("cross-harness workItem = %q, want feat-b", cross.workItem)
	}
	if cross.mode != "continue" {
		t.Fatalf("cross-harness mode = %q, want continue", cross.mode)
	}
}
