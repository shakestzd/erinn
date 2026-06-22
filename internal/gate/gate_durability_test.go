package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/guardprofile"
	"github.com/shakestzd/wipnote/core/paths"
)

// gate_durability_test.go — plan-2390966a slice-8 (feat-bbb80917).
//
// These tests pin the CI durability gate wiring: the Go quality-gate plan MUST
// run a module-wide `go test -short ./...`, which exercises the always-on
// durability contention fixture (TestSQLiteContentionStress_MigratedHotHooksUnderHeldLock,
// which does NOT skip under -short). Removing the -short flag, narrowing the
// ./... scope, or renaming/deleting the fixture reference all fail here, so a
// regression in the migrated hot hooks cannot silently stop being gated.

// TestGoGatePlan_RunsDurabilityFixtureUnderShort asserts the Go plan DetectPlan
// produces includes a `go test` command that runs under -short over the whole
// module, so the durability contention fixture is part of the standard gate.
func TestGoGatePlan_RunsDurabilityFixtureUnderShort(t *testing.T) {
	root := t.TempDir()
	// Make this look like a Go project so DetectPlan picks the Go branch.
	mustWriteGoMod(t, root)

	plan, err := DetectPlan(root, root, guardprofile.PhaseQuality)
	if err != nil {
		t.Fatalf("DetectPlan: %v", err)
	}
	if plan.ProjectType != paths.ProjectTypeGo {
		t.Fatalf("project type = %q, want go", plan.ProjectType)
	}
	if !GoGateRunsDurabilityFixtureUnderShort(plan.Commands) {
		t.Fatalf("Go gate plan does not run `go test -short ./...` — the always-on "+
			"durability contention fixture %s (%s) would no longer be gated by the "+
			"standard quality gate. Restore the -short + ./... go test step in DetectPlan.\n"+
			"commands: %+v", DurabilityContentionFixtureTest, DurabilityContentionFixturePkg, plan.Commands)
	}
}

// TestDurabilityFixtureReference_NonEmpty guards the load-bearing identifiers so
// a rename of the fixture or its package is a deliberate, reviewed change that
// updates this reference (and the gate documentation) in lock-step.
func TestDurabilityFixtureReference_NonEmpty(t *testing.T) {
	if strings.TrimSpace(DurabilityContentionFixtureTest) == "" {
		t.Fatal("DurabilityContentionFixtureTest is empty — the durability gate reference is broken")
	}
	if strings.TrimSpace(DurabilityContentionFixturePkg) == "" {
		t.Fatal("DurabilityContentionFixturePkg is empty — the durability gate reference is broken")
	}
	if !strings.HasPrefix(DurabilityContentionFixtureTest, "TestSQLiteContentionStress") {
		t.Fatalf("DurabilityContentionFixtureTest = %q; expected the TestSQLiteContentionStress family "+
			"so `go test -run TestSQLiteContentionStress` covers it", DurabilityContentionFixtureTest)
	}
}

// TestGoGateHelper_RejectsNonGatingCommands is a negative control: command sets
// that DON'T run -short over ./... must not satisfy the durability-coverage
// predicate — in EITHER the argv form or the approved-profile `sh -c` form.
func TestGoGateHelper_RejectsNonGatingCommands(t *testing.T) {
	cases := [][]Command{
		nil,
		{{Name: "go build", Args: []string{"go", "build", "./..."}}},
		{{Name: "go test pkg", Args: []string{"go", "test", "-short", "./cmd/wipnote/"}}}, // not ./...
		{{Name: "go test all", Args: []string{"go", "test", "./..."}}},                    // no -short
		{{Name: "npm test", Args: []string{"npm", "test"}}},
		// Approved-profile shell forms that do NOT gate the fixture:
		{{Name: "go-build", Args: []string{"sh", "-c", "go build ./..."}}},                 // no go test
		{{Name: "go-vet", Args: []string{"sh", "-c", "go vet ./..."}}},                     // no go test
		{{Name: "selective", Args: []string{"sh", "-c", "go test -short ./cmd/wipnote/"}}}, // not ./...
		{{Name: "no-short", Args: []string{"sh", "-c", "go test ./..."}}},                  // no -short
	}
	for i, cmds := range cases {
		if GoGateRunsDurabilityFixtureUnderShort(cmds) {
			t.Fatalf("case %d: predicate accepted a non-gating command set: %+v", i, cmds)
		}
	}
}

// TestGoGateHelper_AcceptsApprovedProfileShellForm is a positive control for the
// approved-profile `sh -c "<cmd>"` shape DetectPlan renders for each guard
// (roborev-476 finding 4).
func TestGoGateHelper_AcceptsApprovedProfileShellForm(t *testing.T) {
	cmds := []Command{
		{Name: "go-build", Args: []string{"sh", "-c", "go build ./..."}},
		{Name: "go-vet", Args: []string{"sh", "-c", "go vet ./..."}},
		{Name: "go-test-durability-short", Args: []string{"sh", "-c", "go test -buildvcs=false -short ./..."}},
	}
	if !GoGateRunsDurabilityFixtureUnderShort(cmds) {
		t.Fatalf("predicate rejected an approved-profile command set that DOES run "+
			"`go test -short ./...` in shell form: %+v", cmds)
	}
}

// TestRealRepoProfile_GatesDurabilityFixture is the roborev-476 finding-4 gate:
// it asserts against the ACTUAL approved guard profile this repo ships
// (.wipnote/guard-profile.yaml), not a synthetic temp-dir plan. The earlier
// TestGoGatePlan_RunsDurabilityFixtureUnderShort only ever exercises the
// autodetection branch (fresh t.TempDir, no profile), so it could pass while the
// REAL approved profile's quality phase ran only build+vet — silently bypassing
// the always-on durability contention fixture.
//
// This test resolves the repo root, requires the profile to be present + approved
// (UsedProfile == true), and asserts the resolved quality plan covers the
// durability fixture under -short. A future edit to the profile that drops the
// module-wide -short test step (or breaks its signature so it falls back to
// build+vet-only) fails HERE.
func TestRealRepoProfile_GatesDurabilityFixture(t *testing.T) {
	root := findRepoRootWithGuardProfile(t)

	prof, err := guardprofile.Load(root)
	if err != nil {
		t.Fatalf("load real guard profile: %v", err)
	}
	if prof == nil {
		t.Fatalf("no guard profile at %s/.wipnote/guard-profile.yaml — finding-4 expects this repo "+
			"to ship an approved profile", root)
	}
	if !guardprofile.IsApproved(prof) {
		t.Fatalf("the repo guard profile is NOT approved (signature mismatch). After editing "+
			".wipnote/guard-profile.yaml you must re-sign it (guardprofile.Signature) or the gate "+
			"falls back to autodetection and this finding-4 assertion is meaningless")
	}

	plan, err := DetectPlan(root, root, guardprofile.PhaseQuality)
	if err != nil {
		t.Fatalf("DetectPlan against real repo profile: %v", err)
	}
	if !plan.UsedProfile {
		t.Fatalf("DetectPlan did not use the approved profile for the quality phase "+
			"(UsedProfile=false) — it fell back to autodetection, so the real gate this repo "+
			"runs is NOT what this test validates. Re-sign .wipnote/guard-profile.yaml.")
	}
	if !GoGateRunsDurabilityFixtureUnderShort(plan.Commands) {
		t.Fatalf("the REAL approved guard profile's quality phase does NOT run "+
			"`go test -short ./...`, so the always-on durability contention fixture %s (%s) is "+
			"BYPASSED by the actual gate this repo uses (roborev-476 finding 4). Add a module-wide "+
			"-short go test guard to the quality phase of .wipnote/guard-profile.yaml and re-sign it.\n"+
			"resolved quality commands: %+v", DurabilityContentionFixtureTest, DurabilityContentionFixturePkg, plan.Commands)
	}
}

// findRepoRootWithGuardProfile walks up from the test's working directory until
// it finds a directory containing .wipnote/guard-profile.yaml, and returns it.
// The gate package is part of the root module, so the test runs with a CWD
// inside the repo tree; the walk is bounded by the filesystem root.
func findRepoRootWithGuardProfile(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, ".wipnote", "guard-profile.yaml")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skipf("no .wipnote/guard-profile.yaml found walking up from %s — finding-4 "+
				"assertion requires the repo profile to be reachable from the test CWD", dir)
		}
		dir = parent
	}
}

func mustWriteGoMod(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/durabilitygate\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}
