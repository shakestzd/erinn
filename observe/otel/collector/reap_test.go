package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// collectorPIDExists reports whether the .collector-pid file is present in
// sessDir.
func collectorPIDExists(sessDir string) bool {
	_, err := os.Stat(filepath.Join(sessDir, ".collector-pid"))
	return err == nil
}

// TestReapCollector_IdentityGuard proves ReapCollector is NOT the raw
// signalCollector: it writes a .collector-pid pointing at THIS live test
// process (os.Getpid()) but with a BOGUS recorded start time, so the
// start-time identity check in IsCollectorAlive rejects it as a reused pid.
// ReapCollector must therefore NOT signal anything — the fact this test
// process survives is the proof — and must clear the stale record.
func TestReapCollector_IdentityGuard(t *testing.T) {
	sessDir := t.TempDir()
	pid := os.Getpid()
	// pid is live, but starttime=1 will never match this process's real start.
	content := strconv.Itoa(pid) + "\n1\n"
	if err := os.WriteFile(filepath.Join(sessDir, ".collector-pid"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .collector-pid: %v", err)
	}

	gotPID, reaped := ReapCollector(sessDir, 2*time.Second)

	if reaped {
		t.Fatal("identity mismatch must NOT be reaped (would have signalled a non-collector pid)")
	}
	if gotPID != pid {
		t.Fatalf("returned pid=%d, want recorded pid %d", gotPID, pid)
	}
	// Prove we are still alive (ReapCollector did not signal us).
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("test process should be alive: %v", err)
	}
	if collectorPIDExists(sessDir) {
		t.Fatal(".collector-pid must be cleared even when not reaped")
	}
}

// TestReapCollector_DeadPid writes a record for a certainly-dead pid. No kill,
// reaped==false, record cleared.
func TestReapCollector_DeadPid(t *testing.T) {
	sessDir := t.TempDir()
	content := "2147483647\n1\n" // max int32 pid, never running
	if err := os.WriteFile(filepath.Join(sessDir, ".collector-pid"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .collector-pid: %v", err)
	}

	pid, reaped := ReapCollector(sessDir, 2*time.Second)
	if reaped {
		t.Fatal("dead pid must not be reaped")
	}
	if pid != 2147483647 {
		t.Fatalf("returned pid=%d, want 2147483647", pid)
	}
	if collectorPIDExists(sessDir) {
		t.Fatal(".collector-pid must be cleared for a dead pid")
	}
}

// TestReapCollector_LiveCollector spawns a real child, records its pid+real
// start time via WriteCollectorPID (so IsCollectorAlive verifies it), then
// reaps it. The child must be gone, reaped==true, and the record cleared.
func TestReapCollector_LiveCollector(t *testing.T) {
	projectDir := t.TempDir()
	const sid = "sess-live-collector"
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sid)

	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		// Defensive: ensure the child is gone even if the assertion path fails.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// WriteCollectorPID records the real pid + /proc start time → identity match.
	WriteCollectorPID(projectDir, sid, cmd.Process.Pid)
	if alive, _ := IsCollectorAlive(sessDir); !alive {
		t.Fatalf("precondition: collector should be alive before reap")
	}

	pid, reaped := ReapCollector(sessDir, 2*time.Second)
	if !reaped {
		t.Fatal("live verified collector must be reaped")
	}
	if pid != cmd.Process.Pid {
		t.Fatalf("returned pid=%d, want child pid %d", pid, cmd.Process.Pid)
	}

	// The child must be gone: reap the zombie and confirm kill(pid,0) → ESRCH.
	_, _ = cmd.Process.Wait()
	if err := syscall.Kill(cmd.Process.Pid, 0); err == nil {
		t.Fatalf("child pid %d should be dead after reap", cmd.Process.Pid)
	}
	if collectorPIDExists(sessDir) {
		t.Fatal(".collector-pid must be cleared after reaping a live collector")
	}
}
