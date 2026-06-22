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
// predicate.
func TestGoGateHelper_RejectsNonGatingCommands(t *testing.T) {
	cases := [][]Command{
		nil,
		{{Name: "go build", Args: []string{"go", "build", "./..."}}},
		{{Name: "go test pkg", Args: []string{"go", "test", "-short", "./cmd/wipnote/"}}}, // not ./...
		{{Name: "go test all", Args: []string{"go", "test", "./..."}}},                    // no -short
		{{Name: "npm test", Args: []string{"npm", "test"}}},
	}
	for i, cmds := range cases {
		if GoGateRunsDurabilityFixtureUnderShort(cmds) {
			t.Fatalf("case %d: predicate accepted a non-gating command set: %+v", i, cmds)
		}
	}
}

func mustWriteGoMod(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/durabilitygate\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}
