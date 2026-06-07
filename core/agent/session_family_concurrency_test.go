package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// TestRegisterSessionFamily_ConcurrentGoroutines spawns N goroutines that each
// register a distinct session->family pair concurrently. Every entry must
// survive: the interprocess file lock (plus in-process mutex) must serialize
// the full read-modify-write so no registration is lost to a last-writer-wins
// race. Before the flock guard, concurrent read-modify-write would drop
// entries (each writer overwrote with its own stale-read copy).
func TestRegisterSessionFamily_ConcurrentGoroutines(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const n = 64
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sid := fmt.Sprintf("sess-%03d", i)
			fam := fmt.Sprintf("family-%03d", i)
			errs[i] = RegisterSessionFamily(dir, sid, fam)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d RegisterSessionFamily: %v", i, err)
		}
	}

	idx, err := ReadSessionFamilyIndex(dir)
	if err != nil {
		t.Fatalf("ReadSessionFamilyIndex: %v", err)
	}
	if len(idx) != n {
		t.Fatalf("expected %d entries after concurrent registration, got %d (lost-update race)", n, len(idx))
	}
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("sess-%03d", i)
		fam := fmt.Sprintf("family-%03d", i)
		if idx[sid] != fam {
			t.Errorf("entry %s = %q, want %q (lost or clobbered)", sid, idx[sid], fam)
		}
	}
}

// TestRegisterSessionFamily_ConcurrentProcesses is the true cross-process
// regression test for the slice-4 finding: the in-process mutex alone cannot
// prevent two independent launcher PROCESSES from clobbering each other's
// entries. It re-execs THIS test binary as N worker subprocesses, each of
// which registers a distinct session->family pair into a shared index, then
// asserts every entry survived. Without the OS advisory file lock guarding the
// full read-modify-write, concurrent processes lose entries (last-writer-wins).
func TestRegisterSessionFamily_ConcurrentProcesses(t *testing.T) {
	if dir := os.Getenv("WIPNOTE_TEST_SFW_DIR"); dir != "" {
		// Worker mode: perform a single registration and exit. This branch
		// runs in the re-exec'd subprocess, not the parent test.
		idxStr := os.Getenv("WIPNOTE_TEST_SFW_IDX")
		if err := RegisterSessionFamily(dir,
			"psess-"+idxStr, "pfamily-"+idxStr); err != nil {
			fmt.Fprintf(os.Stderr, "worker register: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(exe,
				"-test.run", "^TestRegisterSessionFamily_ConcurrentProcesses$")
			cmd.Env = append(os.Environ(),
				"WIPNOTE_TEST_SFW_DIR="+dir,
				"WIPNOTE_TEST_SFW_IDX="+strconv.Itoa(i),
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				errs[i] = fmt.Errorf("worker %d failed: %v\n%s", i, err, out)
			}
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	idx, err := ReadSessionFamilyIndex(dir)
	if err != nil {
		t.Fatalf("ReadSessionFamilyIndex: %v", err)
	}
	if len(idx) != n {
		t.Fatalf("expected %d entries after %d concurrent PROCESSES, got %d (cross-process lost-update race - flock guard not effective)", n, n, len(idx))
	}
	for i := 0; i < n; i++ {
		k := "psess-" + strconv.Itoa(i)
		want := "pfamily-" + strconv.Itoa(i)
		if idx[k] != want {
			t.Errorf("entry %s = %q, want %q (lost across processes)", k, idx[k], want)
		}
	}
}
