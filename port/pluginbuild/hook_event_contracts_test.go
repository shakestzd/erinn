package pluginbuild

import (
	"os"
	"strings"
	"testing"
)

// TestHookEventContracts is the build-time guard that prevents the
// "JSON-emitting handler registered for a replacement hook" class of bugs
// (canonical case: bug-80b27913 / WorktreeCreate).
//
// It exercises three properties:
//
//  1. COMPLETENESS — every Claude-registered event in the live manifest has an
//     entry in hookEventContractSpecs. A new unclassified registration fails the
//     build, forcing the author to choose a classification before shipping.
//
//  2. REPLACEMENT GUARD — any ReplacementOnRegistration event in the manifest
//     must be an explicitly allowlisted raw-output handler.
//
//  3. NEGATIVE UNIT TEST — a synthetic manifest that registers an unknown
//     replacement event triggers a violation, proving the guard fires when
//     needed.
func TestHookEventContracts(t *testing.T) {
	// --- locate the live manifest ---
	// The plugin-core manifest stays at the repository root
	// (packages/plugin-core/manifest.json); it did not move under port/ when the
	// pluginbuild generator was lifted into this module (trk-1ea27426). Walk up to
	// find it instead of assuming a fixed depth from this test package.
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

	// --- 1. COMPLETENESS ---
	// Every Claude-registered event name must have an entry in the spec table.
	for _, name := range claudeHookEventNames(m) {
		if _, ok := hookEventContractSpecs[name]; !ok {
			t.Errorf("hook event %q is registered for claude but has no entry in hookEventContractSpecs; "+
				"add it with an appropriate classification (Observational / AdditiveControlling / ReplacementOnRegistration) "+
				"in port/pluginbuild/hook_event_contracts.go before shipping",
				name)
		}
	}

	// --- 2. REPLACEMENT GUARD ---
	// ReplacementOnRegistration events need explicit raw-output support.
	violations := checkHookEventContracts(m)
	for _, v := range violations {
		t.Error(v)
	}
}

// TestHookEventContractsNegative proves that checkHookEventContracts fires on a
// synthetic manifest that registers an unallowlisted ReplacementOnRegistration
// event. This guards the guard itself.
func TestHookEventContractsNegative(t *testing.T) {
	hookEventContractSpecs["SyntheticReplacement"] = hookEventContractSpec{
		Classification: ReplacementOnRegistration,
		Note:           "synthetic replacement hook for guard testing",
	}
	t.Cleanup(func() { delete(hookEventContractSpecs, "SyntheticReplacement") })

	// Build a minimal manifest with an unallowlisted replacement hook.
	bad := fixtureManifest()
	bad.Hooks.Events = append(bad.Hooks.Events, HookEvent{
		Name:    "SyntheticReplacement",
		Handler: "synthetic-replacement",
		Targets: []string{"claude"},
	})

	violations := checkHookEventContracts(bad)
	if len(violations) == 0 {
		t.Fatal("expected checkHookEventContracts to report a violation for SyntheticReplacement, got none")
	}

	// The violation message must name the event and reference the bug.
	joined := strings.Join(violations, "\n")
	for _, want := range []string{"SyntheticReplacement", "ReplacementOnRegistration", "bug-80b27913"} {
		if !strings.Contains(joined, want) {
			t.Errorf("violation message missing %q:\n%s", want, joined)
		}
	}
}

// TestHookEventContractSpecsRequireNote ensures every spec has a non-empty
// contract note (mirrors TestAgentFrontmatterFieldSpecsRequireProvenance).
func TestHookEventContractSpecsRequireNote(t *testing.T) {
	for name, spec := range hookEventContractSpecs {
		if spec.Note == "" {
			t.Errorf("hookEventContractSpecs[%q] has an empty Note; add a short contract description", name)
		}
	}
}
