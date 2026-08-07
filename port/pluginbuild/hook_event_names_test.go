package pluginbuild

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookEventNamesLiveManifest is the standing gate: every (event, target)
// pair in the shipped manifest must name an event the target harness actually
// dispatches. A registration that fails here would produce a hook that never
// fires, with no runtime signal from any harness.
func TestHookEventNamesLiveManifest(t *testing.T) {
	m := loadLiveManifest(t)
	for _, v := range checkHookEventNames(m) {
		t.Error(v)
	}
}

// TestHookEventNamesRejectsPhantomEvent is the regression test for
// bug-e39d408f: Codex 0.147.0 dispatches no TaskStarted, TaskComplete or
// TurnAborted, so binding stop/session-end to them left Codex sessions with no
// termination signal at all. Re-adding any of those names must fail the build.
func TestHookEventNamesRejectsPhantomEvent(t *testing.T) {
	for _, phantom := range []string{"TaskStarted", "TaskComplete", "TurnAborted"} {
		t.Run(phantom, func(t *testing.T) {
			bad := fixtureManifest()
			bad.Hooks.Events = append(bad.Hooks.Events, HookEvent{
				Name: phantom, Handler: "stop", Targets: []string{"codex"},
			})
			violations := checkHookEventNames(bad)
			if len(violations) == 0 {
				t.Fatalf("expected a violation for phantom codex event %q, got none", phantom)
			}
			joined := strings.Join(violations, "\n")
			if !strings.Contains(joined, phantom) {
				t.Errorf("violation should name the offending event %q:\n%s", phantom, joined)
			}
		})
	}
}

// TestHookEventNamesRejectsHarnessMismatch covers the subtler half of the
// defect: a name that is perfectly real for one harness but absent from
// another's vocabulary. TeammateIdle is a genuine Claude event and no part of
// Codex 0.147.0's 11-event set.
func TestHookEventNamesRejectsHarnessMismatch(t *testing.T) {
	bad := fixtureManifest()
	bad.Hooks.Events = append(bad.Hooks.Events, HookEvent{
		Name: "TeammateIdle", Handler: "teammate-idle", Targets: []string{"codex"},
	})
	violations := checkHookEventNames(bad)
	if len(violations) == 0 {
		t.Fatal("expected a violation for TeammateIdle registered against codex, got none")
	}
	joined := strings.Join(violations, "\n")
	// The message must list the harness's real vocabulary so the fix is obvious
	// without opening hook_event_names.go.
	for _, want := range []string{"TeammateIdle", "codex", "SessionStart"} {
		if !strings.Contains(joined, want) {
			t.Errorf("violation message missing %q:\n%s", want, joined)
		}
	}
}

// TestHookEventNamesAllowsPerHarnessScoping is the false-positive guard. An
// event valid for one harness and absent from another is entirely normal — the
// `targets` array scopes it, and that must never be an error. Only *registering*
// it against a harness that cannot dispatch it is.
func TestHookEventNamesAllowsPerHarnessScoping(t *testing.T) {
	ok := fixtureManifest()
	ok.Hooks.Events = append(ok.Hooks.Events,
		// Claude-only vocabulary, registered for claude only.
		HookEvent{Name: "WorktreeRemove", Handler: "worktree-remove", Targets: []string{"claude"}},
		// Antigravity-only vocabulary, registered for antigravity only.
		HookEvent{Name: "AfterAgent", Handler: "after-agent", Targets: []string{"antigravity"}},
		// Shared vocabulary, registered for all three.
		HookEvent{Name: "PostToolUse", Handler: "posttooluse", Targets: []string{"claude", "codex", "antigravity"}},
	)
	if violations := checkHookEventNames(ok); len(violations) > 0 {
		t.Errorf("legitimate per-harness scoping must not produce violations:\n%s",
			strings.Join(violations, "\n"))
	}
}

// TestHookEventNamesRejectsUnknownHarness ensures a target with no verified
// vocabulary cannot be registered against. Silently accepting it would be the
// same unvalidatable no-op in a new disguise.
func TestHookEventNamesRejectsUnknownHarness(t *testing.T) {
	bad := fixtureManifest()
	bad.Hooks.Events = append(bad.Hooks.Events, HookEvent{
		Name: "SessionStart", Handler: "session-start", Targets: []string{"cursor"},
	})
	violations := checkHookEventNames(bad)
	if len(violations) == 0 {
		t.Fatal("expected a violation for unknown harness \"cursor\", got none")
	}
	if !strings.Contains(strings.Join(violations, "\n"), "cursor") {
		t.Errorf("violation should name the unknown harness:\n%s", strings.Join(violations, "\n"))
	}
}

// TestLoadRejectsPhantomHookEvent proves the gate is a hard build failure, not
// merely an assertable helper: `wipnote plugin build-ports` and `check-ports`
// both reach it through Load -> validate, so no tree is emitted from a manifest
// carrying a dead registration.
func TestLoadRejectsPhantomHookEvent(t *testing.T) {
	m := fixtureManifest()
	m.Hooks.Events = append(m.Hooks.Events, HookEvent{
		Name: "TaskComplete", Handler: "session-end", Targets: []string{"codex"},
	})
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path)
	if err == nil {
		t.Fatal("Load must reject a manifest registering a phantom hook event")
	}
	if !strings.Contains(err.Error(), "TaskComplete") {
		t.Errorf("Load error should name the offending event:\n%v", err)
	}
}

// TestFixtureManifestPassesHookEventGate keeps the shared test fixture honest.
// Before bug-e39d408f the fixture modelled phantom Codex events as if they were
// normal, which is how an invented vocabulary survives review.
func TestFixtureManifestPassesHookEventGate(t *testing.T) {
	if violations := checkHookEventNames(fixtureManifest()); len(violations) > 0 {
		t.Errorf("fixtureManifest must use real per-harness vocabulary:\n%s",
			strings.Join(violations, "\n"))
	}
}

// TestCodexHookEventVocabulary locks the verified Codex 0.147.0 event set.
// Ground truth (trk-1db7bf72) was established three independent ways: the
// HookEventNameWire enum plus 11 embedded JSON schemas extracted from the Rust
// binary, a live black-box ~/.codex/hooks.json dispatch test, and
// learn.chatgpt.com/docs/hooks. Codex silently drops anything else, so growing
// this set requires re-verification, not a plausible guess.
func TestCodexHookEventVocabulary(t *testing.T) {
	want := map[string]struct{}{
		"SessionStart": {}, "SessionEnd": {}, "UserPromptSubmit": {},
		"PreToolUse": {}, "PostToolUse": {}, "PermissionRequest": {},
		"PreCompact": {}, "PostCompact": {}, "SubagentStart": {},
		"SubagentStop": {}, "Stop": {},
	}
	got := harnessHookEventNames["codex"]
	if len(got) != len(want) {
		t.Errorf("codex vocabulary has %d events, want exactly %d: %v",
			len(got), len(want), hookEventNamesForHarness("codex"))
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("codex vocabulary missing verified event %q", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("codex vocabulary has unverified event %q; Codex 0.147.0 dispatches exactly 11", name)
		}
	}
}

// TestAntigravityEventNamesDerivedFromSpecs asserts the agy translation map is
// the one derived from hookEventNameSpecs. A second hand-maintained copy would
// be free to disagree with the gate, which is how the antigravity wrong-name
// bug (arch card gemini-cli-sunset-antigravity-hook-bugs) went unnoticed.
func TestAntigravityEventNamesDerivedFromSpecs(t *testing.T) {
	want := map[string]string{
		"UserPromptSubmit": "PreInvocation",
		"AfterAgent":       "PostInvocation",
		"PreToolUse":       "PreToolUse",
		"PostToolUse":      "PostToolUse",
		"Stop":             "Stop",
	}
	if len(antigravityEventNames) != len(want) {
		t.Fatalf("antigravity map has %d entries, want %d: %v",
			len(antigravityEventNames), len(want), antigravityEventNames)
	}
	for canonical, native := range want {
		if got := antigravityEventNames[canonical]; got != native {
			t.Errorf("antigravity %q -> %q, want %q", canonical, got, native)
		}
	}
}

// TestHookEventNameSpecsRequireProvenance mirrors
// TestAgentFrontmatterFieldSpecsRequireProvenance and
// TestHookEventContractSpecsRequireNote: an entry without a recorded source is
// indistinguishable from an invented one.
func TestHookEventNameSpecsRequireProvenance(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range hookEventNameSpecs {
		if spec.Name == "" {
			t.Error("hookEventNameSpecs contains an entry with an empty Name")
			continue
		}
		if seen[spec.Name] {
			t.Errorf("hookEventNameSpecs has a duplicate entry for %q", spec.Name)
		}
		seen[spec.Name] = true
		if spec.Provenance == "" {
			t.Errorf("hookEventNameSpecs[%q] has no Provenance; record how the vocabulary was verified", spec.Name)
		}
		for harness, native := range spec.Harnesses {
			if _, known := knownHookHarnesses[harness]; !known {
				t.Errorf("hookEventNameSpecs[%q] names unknown harness %q", spec.Name, harness)
			}
			if native == "" {
				t.Errorf("hookEventNameSpecs[%q].Harnesses[%q] is empty; give the dispatched event name", spec.Name, harness)
			}
		}
	}
}

// TestClaudeHookEventNamesHaveContractSpecs keeps the two Claude-facing tables
// in agreement. hookEventNameSpecs says whether Claude dispatches an event;
// hookEventContractSpecs says how Claude treats its stdout. A name in the first
// without an entry in the second could be registered without ever choosing a
// stdout contract — the bug-80b27913 failure mode.
func TestClaudeHookEventNamesHaveContractSpecs(t *testing.T) {
	for _, name := range hookEventNamesForHarness("claude") {
		if _, ok := hookEventContractSpecs[name]; !ok {
			t.Errorf("event %q is claude vocabulary in hookEventNameSpecs but has no hookEventContractSpecs entry; "+
				"classify its stdout contract in hook_event_contracts.go", name)
		}
	}
}

func loadLiveManifest(t *testing.T) *Manifest {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	manifestPath, err := FindManifest(wd)
	if err != nil {
		t.Fatalf("manifest not found: %v", err)
	}
	m, err := Load(manifestPath)
	if err != nil {
		t.Fatalf("Load manifest: %v", err)
	}
	return m
}
