package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

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
