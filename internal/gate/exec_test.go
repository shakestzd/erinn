package gate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLookupAndUpsertEnv(t *testing.T) {
	env := []string{"FOO=1", "BAR=2"}
	if got := LookupEnv(env, "BAR"); got != "2" {
		t.Errorf("LookupEnv BAR: got %q want 2", got)
	}
	if got := LookupEnv(env, "MISSING"); got != "" {
		t.Errorf("LookupEnv MISSING: got %q want empty", got)
	}
	out := UpsertEnv(env, "BAR", "9")
	if LookupEnv(out, "BAR") != "9" || len(out) != 2 {
		t.Errorf("UpsertEnv replace failed: %v", out)
	}
	out = UpsertEnv(env, "BAZ", "3")
	if LookupEnv(out, "BAZ") != "3" || len(out) != 3 {
		t.Errorf("UpsertEnv insert failed: %v", out)
	}
}

func TestResolveCodeRoot(t *testing.T) {
	const proj = "/repo/main"
	const wt = "/repo/.worktrees/feat"
	const common = "/repo/main/.git"
	cases := []struct {
		name                          string
		cwdTop, cwdCommon, projCommon string
		want                          string
	}{
		{"cwd-not-in-git", "", "", common, proj},
		{"project-not-a-repo", wt, "/repo/.worktrees/feat/.git", "", proj},
		{"cwd-is-project", proj, common, common, proj},
		{"linked-worktree-same-repo", wt, common, common, wt},
		{"unrelated-repo", "/other/repo", "/other/repo/.git", common, proj},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveCodeRoot(proj, tc.cwdTop, tc.cwdCommon, tc.projCommon); got != tc.want {
				t.Fatalf("ResolveCodeRoot() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodeRoot_LinkedWorktreeOverride(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	mainRepo := filepath.Join(root, "main")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := exec.Command("git", "init", mainRepo).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	git(mainRepo, "config", "user.email", "t@example.com")
	git(mainRepo, "config", "user.name", "t")
	git(mainRepo, "commit", "--allow-empty", "-m", "init")
	wt := filepath.Join(root, "wt-feat")
	git(mainRepo, "worktree", "add", "-b", "feat-x", wt)
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	t.Chdir(wt)
	if got := resolve(CodeRoot(mainRepo)); got != resolve(wt) {
		t.Fatalf("CodeRoot from worktree = %q, want %q", got, resolve(wt))
	}
}

func TestGateExecEnvRedirectsWhenTmpUnusable(t *testing.T) {
	codeRoot := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(codeRoot, "nope-noexec"))
	t.Setenv("GOTMPDIR", "")
	env, redirected, dir := GateExecEnv(codeRoot)
	if !redirected {
		t.Skipf("scratch dir %q not exec-capable", filepath.Join(codeRoot, gateTmpDirName))
	}
	wantDir := filepath.Join(codeRoot, gateTmpDirName)
	if dir != wantDir || LookupEnv(env, "GOTMPDIR") != wantDir {
		t.Fatalf("unexpected redirect: dir=%q env=%q want=%q", dir, LookupEnv(env, "GOTMPDIR"), wantDir)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Fatalf("scratch dir not created: %v", err)
	}
}

func TestRunManagedGateCancelKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var sout, serr strings.Builder
	done := make(chan error, 1)
	go func() {
		_, err := RunManagedGate(ctx, "sleep", "", nil, &sout, &serr, "sh", "-c", "sleep 60")
		done <- err
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunManagedGate did not return after context cancellation")
	}
}
