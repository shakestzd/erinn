package childproc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Shared test timing budget. SpawnTimeout is generous enough to absorb
// fork+exec+shell-startup jitter under full-suite parallel load (happy
// path still resolves in ~200ms); testCtxTimeout must exceed it with
// margin so the context isn't the limiting factor.
const (
	testSpawnTimeout = 10 * time.Second
	testCtxTimeout   = 15 * time.Second
)

// probeExec executes path as a subprocess to verify the filesystem is
// exec-capable. Returns nil on success, error when noexec.
func probeExec(path string) error {
	return exec.Command(path).Run()
}

// requireExecCapableTmpdir verifies that t.TempDir() returns an exec-capable
// path, and calls t.Skip when it does not. Call this at the start of every
// test that creates and executes shell scripts in the temp dir — not only in
// buildFakeChild — so tests that create scripts directly (TestInvalidHandshakeFails,
// TestHandshakeTimeout) also skip cleanly on noexec /tmp rather than passing
// for the wrong reason (fork/exec: permission denied instead of invalid-handshake
// or timeout behaviour).
func requireExecCapableTmpdir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("executable shell scripts are POSIX-only")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe-exec.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Skipf("requireExecCapableTmpdir: WriteFile: %v", err)
	}
	if err := probeExec(probe); err != nil {
		t.Skipf("requireExecCapableTmpdir: exec probe failed — noexec TMPDIR. "+
			"Set TMPDIR=/home/vscode/.gotest-tmp for exec-capable temp dir. "+
			"Current TMPDIR=%q, error: %v", os.Getenv("TMPDIR"), err)
	}
}

// buildFakeChild writes a shell script that ignores its args, prints the
// handshake line, then sleeps. The script is used as the BinPath for a
// Supervisor so the handshake/lifecycle/reap logic can be exercised
// without needing a real wipnote binary.
//
// PROFILE REQUIREMENT (slice-8, plan-1670cacd slice-10):
// This helper writes an executable shell script into t.TempDir(). It
// requires an exec-capable filesystem at TMPDIR. In the wipnote devcontainer
// /tmp is mounted noexec; set TMPDIR=/home/vscode/.gotest-tmp before
// running this package or the test will fail with "fork/exec: permission denied".
func buildFakeChild(t *testing.T, port int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake child shell script is POSIX-only")
	}
	dir := t.TempDir()
	// Probe: verify the temp dir is exec-capable before writing the real script.
	// If exec fails, skip with a clear profile requirement message so CI
	// without an exec-capable TMPDIR still passes (never fails).
	probeScript := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probeScript, []byte("#!/bin/sh\nexit 0\n"), 0o755); err == nil {
		if probeErr := probeExec(probeScript); probeErr != nil {
			t.Skipf("buildFakeChild requires exec-capable TMPDIR: "+
				"set TMPDIR=/home/vscode/.gotest-tmp (slice-8/slice-10 profile). "+
				"Current TMPDIR=%q, error: %v", dir, probeErr)
		}
	}
	path := filepath.Join(dir, "fake-wipnote")
	script := "#!/bin/sh\n" +
		"echo \"wipnote-serve-ready port=" + itoa(port) + " pid=$$\"\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestGetOrSpawnHandshake(t *testing.T) {
	bin := buildFakeChild(t, 12345)
	sup := NewSupervisor(Options{
		BinPath:      bin,
		SpawnTimeout: testSpawnTimeout,
	})
	defer sup.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), testCtxTimeout)
	defer cancel()

	c, err := sup.GetOrSpawn(ctx, "projA", t.TempDir())
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	if c.Port != 12345 {
		t.Errorf("port: got %d, want 12345", c.Port)
	}
	if c.PID == 0 {
		t.Errorf("pid: zero, want positive")
	}
	if c.Proxy == nil {
		t.Errorf("proxy: nil")
	}

	// Warm lookup: same child.
	c2, err := sup.GetOrSpawn(ctx, "projA", t.TempDir())
	if err != nil {
		t.Fatalf("GetOrSpawn warm: %v", err)
	}
	if c2 != c {
		t.Errorf("warm lookup returned different child")
	}
}

func TestGetOrSpawnConcurrent(t *testing.T) {
	bin := buildFakeChild(t, 12346)
	sup := NewSupervisor(Options{
		BinPath:      bin,
		SpawnTimeout: testSpawnTimeout,
	})
	defer sup.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), testCtxTimeout)
	defer cancel()

	// Fire N concurrent GetOrSpawn calls for the same projectID. The
	// sync.Once gate must cause them to share a single spawned child.
	const N = 10
	herdDir := t.TempDir()
	results := make(chan *Child, N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			c, err := sup.GetOrSpawn(ctx, "herd", herdDir)
			if err != nil {
				errs <- err
				return
			}
			results <- c
		}()
	}

	var first *Child
	for i := 0; i < N; i++ {
		select {
		case err := <-errs:
			t.Fatalf("spawn err: %v", err)
		case c := <-results:
			if first == nil {
				first = c
			} else if c != first {
				t.Errorf("herd produced different children: %p vs %p", first, c)
			}
		case <-time.After(testCtxTimeout):
			t.Fatal("timeout waiting for spawn results")
		}
	}
}

func TestInvalidHandshakeFails(t *testing.T) {
	requireExecCapableTmpdir(t)
	// Fake binary that prints the wrong line.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-wipnote")
	bad := "#!/bin/sh\necho \"wrong line format\"\nexec sleep 30\n"
	if err := os.WriteFile(path, []byte(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	sup := NewSupervisor(Options{
		BinPath:      path,
		SpawnTimeout: testSpawnTimeout,
	})
	defer sup.Shutdown(context.Background())

	_, err := sup.GetOrSpawn(context.Background(), "bad", t.TempDir())
	if err == nil {
		t.Fatal("expected error on invalid handshake")
	}
}

func TestHandshakeTimeout(t *testing.T) {
	requireExecCapableTmpdir(t)
	// Fake binary that never prints — sleeps forever with no output.
	dir := t.TempDir()
	path := filepath.Join(dir, "silent-wipnote")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sup := NewSupervisor(Options{
		BinPath:      path,
		SpawnTimeout: 200 * time.Millisecond,
	})
	defer sup.Shutdown(context.Background())

	start := time.Now()
	_, err := sup.GetOrSpawn(context.Background(), "silent", t.TempDir())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected handshake timeout error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("took too long: %s", elapsed)
	}
}

func TestIdleReaperKillsStaleChild(t *testing.T) {
	bin := buildFakeChild(t, 12347)
	sup := NewSupervisor(Options{
		BinPath:      bin,
		SpawnTimeout: testSpawnTimeout,
		IdleTimeout:  100 * time.Millisecond,
	})
	defer sup.Shutdown(context.Background())

	c, err := sup.GetOrSpawn(context.Background(), "idle", t.TempDir())
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Force LastRequest into the past.
	c.LastRequest.Store(time.Now().Add(-1 * time.Second).Unix())

	// Re-issue reapIdleOnce each poll iteration: a just-spawned child can
	// miss a single SIGTERM under scheduler delay (signal arrives before the
	// exec'd sleep process is scheduled). Production's RunIdleReaper re-issues
	// every tick anyway, so retrying here is faithful to production behaviour,
	// not a test hack. The fake child uses "exec sleep 30" — sleep honours
	// SIGTERM immediately once scheduled, so any retry that hits it will work.
	// reapIdleOnce also escalates to SIGKILL after 100ms, so even a tardy
	// scheduler cannot stall the test indefinitely.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		sup.reapIdleOnce()
		if len(sup.Children()) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child was not reaped after idle; children=%d", len(sup.Children()))
}
