package guardprofile

import (
	"os"
	"path/filepath"
	"testing"
)

// approvedProfileYAML returns a profile YAML whose approved.signature matches
// its content, so IsApproved is true.
func approvedProfileYAML(t *testing.T, p *Profile) string {
	t.Helper()
	sig := Signature(p)
	return "guards:\n" +
		"  quality:\n" +
		"    - name: build\n" +
		"      cmd: go build ./...\n" +
		"approved:\n" +
		"  signature: " + sig + "\n" +
		"  by: tester\n"
}

func TestResolveGuards_PrefersApprovedProfile(t *testing.T) {
	repo := t.TempDir()
	p := &Profile{Guards: map[string][]Guard{
		PhaseQuality: {{Name: "build", Cmd: "go build ./..."}},
	}}
	writeProfile(t, repo, approvedProfileYAML(t, p))

	guards, used, err := ResolveGuards(repo, PhaseQuality)
	if err != nil {
		t.Fatalf("ResolveGuards: %v", err)
	}
	if !used {
		t.Fatalf("expected usedProfile=true for an approved profile")
	}
	if len(guards) != 1 || guards[0].Cmd != "go build ./..." {
		t.Fatalf("unexpected guards: %#v", guards)
	}
}

func TestResolveGuards_FallsBackWhenAbsentOrUnapproved(t *testing.T) {
	// Absent profile.
	repo := t.TempDir()
	if guards, used, err := ResolveGuards(repo, PhaseQuality); err != nil || used || guards != nil {
		t.Fatalf("absent profile: got guards=%#v used=%v err=%v; want nil,false,nil", guards, used, err)
	}

	// Present but UNAPPROVED (no signature) -> fallback.
	repo2 := t.TempDir()
	writeProfile(t, repo2, "guards:\n  quality:\n    - name: build\n      cmd: go build ./...\n")
	if guards, used, err := ResolveGuards(repo2, PhaseQuality); err != nil || used || guards != nil {
		t.Fatalf("unapproved profile: got guards=%#v used=%v err=%v; want nil,false,nil", guards, used, err)
	}

	// Present but EDITED after approval (signature no longer matches) -> fallback.
	repo3 := t.TempDir()
	stale := &Profile{Guards: map[string][]Guard{PhaseQuality: {{Name: "build", Cmd: "go build ./..."}}}}
	staleSig := Signature(stale)
	// Approve with the stale signature, then change the cmd so content drifts.
	writeProfile(t, repo3, "guards:\n  quality:\n    - name: build\n      cmd: go build -tags drift ./...\n"+
		"approved:\n  signature: "+staleSig+"\n")
	if guards, used, err := ResolveGuards(repo3, PhaseQuality); err != nil || used || guards != nil {
		t.Fatalf("edited profile: got guards=%#v used=%v err=%v; want nil,false,nil", guards, used, err)
	}
}

func TestResolveGuards_AppliesWhenPathFilter(t *testing.T) {
	repo := t.TempDir()
	// Create a Go file so a guard scoped to **/*.go applies, and confirm a
	// guard scoped to **/*.rs does not.
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	p := &Profile{Guards: map[string][]Guard{
		PhaseQuality: {
			{Name: "go", Cmd: "go build ./...", AppliesWhen: &AppliesWhen{Paths: []string{"**/*.go"}}},
			{Name: "rust", Cmd: "cargo build", AppliesWhen: &AppliesWhen{Paths: []string{"**/*.rs"}}},
		},
	}}
	sig := Signature(p)
	yaml := "guards:\n" +
		"  quality:\n" +
		"    - name: go\n      cmd: go build ./...\n      applies_when:\n        paths:\n          - '**/*.go'\n" +
		"    - name: rust\n      cmd: cargo build\n      applies_when:\n        paths:\n          - '**/*.rs'\n" +
		"approved:\n  signature: " + sig + "\n"
	writeProfile(t, repo, yaml)

	guards, used, err := ResolveGuards(repo, PhaseQuality)
	if err != nil || !used {
		t.Fatalf("ResolveGuards: used=%v err=%v", used, err)
	}
	if len(guards) != 1 || guards[0].Name != "go" {
		t.Fatalf("expected only the go guard to apply, got %#v", guards)
	}
}

// TestMatchGlob_DoublestarNested is the regression for roborev #3684: "**" must
// match zero or more whole path segments, so recursive-scoped guards match
// nested files (the prior collapse-** approach dropped intermediate segments).
func TestMatchGlob_DoublestarNested(t *testing.T) {
	cases := []struct {
		glob, rel string
		want      bool
	}{
		{"internal/**/*.go", "internal/foo.go", true},
		{"internal/**/*.go", "internal/pkg/sub/foo.go", true},
		{"internal/**/*.go", "cmd/foo.go", false},
		{"**/*.go", "a/b/c.go", true},
		{"*.go", "a/b.go", false},
		{"web/**", "web/src/app.tsx", true},
		{"web/**", "web", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.glob, c.rel); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.glob, c.rel, got, c.want)
		}
	}
}
