package launcher

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestAppendOrReplaceEnv_AppendsAndReplaces(t *testing.T) {
	env := []string{"FOO=old", "BAR=keep"}
	got := AppendOrReplaceEnv(env, "FOO=new", "BAZ=fresh")

	if got[0] != "FOO=new" {
		t.Fatalf("FOO entry = %q, want replacement", got[0])
	}
	if got[1] != "BAR=keep" {
		t.Fatalf("BAR entry = %q, want existing value preserved", got[1])
	}
	if got[2] != "BAZ=fresh" {
		t.Fatalf("BAZ entry = %q, want appended value", got[2])
	}
}

func TestBuildHarnessOtelEnv_ZeroPortReturnsBase(t *testing.T) {
	base := []string{"EXISTING=1"}
	got := BuildHarnessOtelEnv(base, "gemini_cli", 0, "sess-123")

	if len(got) != len(base) {
		t.Fatalf("len = %d, want %d", len(got), len(base))
	}
	if got[0] != base[0] {
		t.Fatalf("entry = %q, want %q", got[0], base[0])
	}
}

func TestBuildHarnessOtelEnv_AddsHarnessVars(t *testing.T) {
	got := BuildHarnessOtelEnv(nil, "codex", 4317, "sess-abc")

	expect := map[string]bool{
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317": false,
		"OTEL_SERVICE_NAME=codex-cli":                       false,
		"WIPNOTE_OTEL_SESSION=sess-abc":                     false,
	}
	for _, entry := range got {
		if _, ok := expect[entry]; ok {
			expect[entry] = true
		}
	}
	for entry, seen := range expect {
		if !seen {
			t.Fatalf("missing env entry %q in %#v", entry, got)
		}
	}
}

func TestBuildHarnessAgentEnv_AddsHarnessAgentVars(t *testing.T) {
	base := []string{"PATH=/bin", "WIPNOTE_AGENT_ID=old"}
	got := BuildHarnessAgentEnv(base, "antigravity_cli")

	expect := map[string]bool{
		"PATH=/bin":                      false,
		"WIPNOTE_AGENT_ID=antigravity":   false,
		"WIPNOTE_AGENT_TYPE=antigravity": false,
	}
	for _, entry := range got {
		if _, ok := expect[entry]; ok {
			expect[entry] = true
		}
	}
	for entry, seen := range expect {
		if !seen {
			t.Fatalf("missing env entry %q in %#v", entry, got)
		}
	}
}

func TestRunHarnessWithCleanupCore_NormalExit(t *testing.T) {
	c := exec.Command("/bin/sh", "-c", "exit 0")
	res := RunHarnessWithCleanupCore(c, nil)
	if res.Err != nil {
		t.Fatalf("Err = %v, want nil", res.Err)
	}
	if res.ReraiseSig != 0 {
		t.Fatalf("ReraiseSig = %v, want 0", res.ReraiseSig)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestRunHarnessWithCleanupCore_ChildSignaled(t *testing.T) {
	cleanupCalled := false
	cleanup := func() { cleanupCalled = true }

	c := exec.Command("/bin/sh", "-c", "kill -TERM $$")
	res := RunHarnessWithCleanupCore(c, cleanup)

	if res.Err != nil {
		t.Fatalf("Err = %v, want nil", res.Err)
	}
	if res.ReraiseSig != syscall.SIGTERM {
		t.Fatalf("ReraiseSig = %v, want SIGTERM", res.ReraiseSig)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !cleanupCalled {
		t.Fatal("cleanup was not invoked")
	}
}

func TestRunHarnessWithCleanup_StartFailure(t *testing.T) {
	cleanupCalled := false
	err := RunHarnessWithCleanup(exec.Command("/this/binary/does/not/exist"), func() {
		cleanupCalled = true
	})
	if err == nil {
		t.Fatal("expected start failure")
	}
	if !cleanupCalled {
		t.Fatal("cleanup was not invoked")
	}
}

func TestNoexecTempRoot_ProfileCheck(t *testing.T) {
	dir := t.TempDir()
	scriptPath := dir + "/probe.sh"
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Skipf("noexec profile check: cannot write probe script: %v", err)
	}
	cmd := exec.Command(scriptPath)
	if err := cmd.Run(); err != nil {
		t.Skipf("noexec profile check: TMPDIR=%q is not exec-capable: %v", dir, err)
	}
}
