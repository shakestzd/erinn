package childproc

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// TestChildSysProcAttrSetpgid verifies that childSysProcAttr always sets
// Setpgid — regardless of platform — so child processes are placed in their
// own process group and a SIGKILL to the parent's pgroup does not propagate.
func TestChildSysProcAttrSetpgid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SysProcAttr.Setpgid is a POSIX concept")
	}
	attr := childSysProcAttr()
	if attr == nil {
		t.Fatal("childSysProcAttr() returned nil; want non-nil SysProcAttr")
	}
	if !attr.Setpgid {
		t.Error("SysProcAttr.Setpgid = false; want true")
	}
}

// TestChildSysProcAttrPdeathsigLinux verifies that on Linux Pdeathsig is set
// to SIGTERM so the kernel delivers the signal to the child the moment the
// parent dies.
func TestChildSysProcAttrPdeathsigLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Pdeathsig is Linux-only")
	}
	attr := childSysProcAttr()
	if attr == nil {
		t.Fatal("childSysProcAttr() returned nil")
	}
	if attr.Pdeathsig != syscall.SIGTERM {
		t.Errorf("Pdeathsig = %v; want SIGTERM (%v)", attr.Pdeathsig, syscall.SIGTERM)
	}
}

// TestPIDFilePath_ verifies that the PID file path helper produces the
// expected path relative to the project directory.
func TestPIDFilePath_(t *testing.T) {
	got := pidFilePath_("/home/user/myproject")
	want := filepath.Join("/home/user/myproject", ".wipnote", ".serve-children.pid")
	if got != want {
		t.Errorf("pidFilePath_ = %q; want %q", got, want)
	}
}

// TestRecordForgetChildPID verifies that recordChildPID writes PIDs and
// forgetChildPID removes them from the PID file.
func TestRecordForgetChildPID(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sup := NewSupervisor(Options{PIDFileDir: dir})

	// Record two PIDs.
	sup.recordChildPID(111)
	sup.recordChildPID(222)

	// Read them back.
	sup.mu.Lock()
	pids := sup.readPIDsLocked()
	sup.mu.Unlock()

	if len(pids) != 2 {
		t.Fatalf("readPIDsLocked: got %v, want [111 222]", pids)
	}

	// Forget one.
	sup.forgetChildPID(111)

	sup.mu.Lock()
	pids = sup.readPIDsLocked()
	sup.mu.Unlock()

	if len(pids) != 1 || pids[0] != 222 {
		t.Errorf("after forgetChildPID(111): got %v, want [222]", pids)
	}
}

// TestReapStaleChildrenTruncatesFile verifies that ReapStaleChildren resets
// the PID file to empty so the next generation starts clean, even when the
// recorded PIDs are not alive.
func TestReapStaleChildrenTruncatesFile(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatal(err)
	}

	pidPath := filepath.Join(wipnoteDir, serveChildrenPIDFile)
	// Write two definitely-dead PIDs (very large numbers unlikely to be
	// real processes; also kill -0 on missing PIDs returns an error).
	if err := os.WriteFile(pidPath, []byte("99999997\n99999998\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sup := NewSupervisor(Options{PIDFileDir: dir})
	sup.ReapStaleChildren()

	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("PID file missing after ReapStaleChildren: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("PID file not truncated: %q", string(data))
	}
}

// TestReapStaleChildrenSkipsUnrelatedPID verifies that on Linux, a PID whose
// /proc/<pid>/cmdline does not contain "_serve-child" is not SIGTERMed.
// On non-Linux this test is skipped because isServeChildPID always returns true.
func TestReapStaleChildrenSkipsUnrelatedPID(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc check is Linux-only")
	}

	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatal(err)
	}

	pidPath := filepath.Join(wipnoteDir, serveChildrenPIDFile)
	// Record our own PID — this process is alive but is NOT a _serve-child.
	myPID := os.Getpid()
	pidStr := itoa(myPID) + "\n"
	if err := os.WriteFile(pidPath, []byte(pidStr), 0o644); err != nil {
		t.Fatal(err)
	}

	sup := NewSupervisor(Options{PIDFileDir: dir})
	// ReapStaleChildren should skip us because our cmdline lacks "_serve-child".
	// If it does NOT skip us, we get SIGTERMed — the test will die. A passing
	// test therefore implicitly validates the guard.
	sup.ReapStaleChildren()

	// We're still alive here — guard worked.
}

// TestIsServeChildPIDFalseForOtherProcess verifies that isServeChildPID
// returns false for our own PID (which does not have "_serve-child" in its
// cmdline).
func TestIsServeChildPIDFalseForOtherProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc check is Linux-only")
	}
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	pidFile := filepath.Join(wipnoteDir, serveChildrenPIDFile)
	got := isServeChildPID(os.Getpid(), pidFile)
	if got {
		t.Errorf("isServeChildPID returned true for test process; want false")
	}
}
