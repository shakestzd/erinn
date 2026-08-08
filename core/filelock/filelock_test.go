package filelock

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestGuardSerialisesWriters proves the guard actually excludes concurrent
// writers rather than merely returning a release func. The counter is
// deliberately unsynchronised apart from the guard: if Guard did not exclude,
// the interleaved read-modify-write would lose increments.
func TestGuardSerialisesWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.html")
	const writers = 50

	counter := 0
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := Guard(path)
			defer release()
			v := counter
			// Yield inside the critical section so an unguarded implementation
			// reliably interleaves instead of accidentally serialising.
			for j := 0; j < 100; j++ {
				_ = j
			}
			counter = v + 1
		}()
	}
	wg.Wait()

	if counter != writers {
		t.Errorf("counter = %d, want %d — the guard did not serialise writers", counter, writers)
	}
}

// TestGuardCreatesSidecarBesideTarget pins where the lock lives. It must be a
// SEPARATE file: canonical writers replace the data file via temp-then-rename,
// and a lock on the data file's inode would not survive the swap.
func TestGuardCreatesSidecarBesideTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.html")

	release := Guard(path)
	release()

	if !CrossProcess {
		t.Skip("no cross-process lock on this platform (bug-68f3593b); no sidecar is created")
	}
	if _, err := os.Stat(path + LockSuffix); err != nil {
		t.Fatalf("sidecar %s not created: %v", path+LockSuffix, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Guard must not create the data file itself: %v", err)
	}
}

// TestGuardCreatesMissingParentDirectory covers the first write to a brand-new
// artifact. Without the MkdirAll the sidecar open fails and the guard silently
// degrades to in-process only — exactly the kind of silent downgrade the
// cross-process lock exists to prevent.
func TestGuardCreatesMissingParentDirectory(t *testing.T) {
	if !CrossProcess {
		t.Skip("no cross-process lock on this platform (bug-68f3593b)")
	}
	path := filepath.Join(t.TempDir(), "nested", "deeper", "ledger.html")

	release := Guard(path)
	release()

	if _, err := os.Stat(path + LockSuffix); err != nil {
		t.Fatalf("sidecar not created in a missing parent directory: %v", err)
	}
}

// TestGuardIsReentrantAcrossSequentialAcquisitions guards against a release
// that fails to unlock — the next Guard on the same path would deadlock.
func TestGuardIsReentrantAcrossSequentialAcquisitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.html")
	for i := 0; i < 3; i++ {
		release := Guard(path)
		release()
	}
}

// TestDistinctPathsDoNotBlockEachOther confirms the in-process mutex is keyed
// by path, not global — two different canonical artifacts must not serialise
// against one another.
func TestDistinctPathsDoNotBlockEachOther(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.html")
	b := filepath.Join(dir, "b.html")

	releaseA := Guard(a)
	defer releaseA()

	done := make(chan struct{})
	go func() {
		releaseB := Guard(b)
		releaseB()
		close(done)
	}()

	<-done // would block forever if the guard were global
}
