package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestCheckServeLock_NoLockfile verifies that checkServeLock returns
// (false, false) when no lock file exists.
func TestCheckServeLock_NoLockfile(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755)

	skip, stale := checkServeLock(dir)
	if skip {
		t.Error("checkServeLock: skipSpawn = true, want false (no lockfile)")
	}
	if stale {
		t.Error("checkServeLock: stale = true, want false (no lockfile)")
	}
}

// TestEnsureServeForOtel_SkipsWhenLockfileAlive verifies that
// ensureServeForOtel does not spawn a serve process when the lock file
// contains the PID of a live process (os.Getpid()).
//
// We test the checkServeLock helper directly: a lock pointing at
// os.Getpid() must return (skipSpawn=true, stale=false) because this
// process is alive.
func TestEnsureServeForOtel_SkipsWhenLockfileAlive(t *testing.T) {
	dir := t.TempDir()
	hgDir := filepath.Join(dir, ".wipnote")
	_ = os.MkdirAll(hgDir, 0o755)

	// Write a lock file pointing at the current (live) process.
	lockPath := serveLockPath(dir)
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	skip, stale := checkServeLock(dir)
	if !skip {
		t.Error("checkServeLock: skipSpawn = false, want true (current PID is alive)")
	}
	if stale {
		t.Error("checkServeLock: stale = true, want false (current PID is alive)")
	}
}

// TestEnsureServeForOtel_CleansStaleLockfile verifies that checkServeLock
// returns (false, stale=true) when the lock file contains a non-existent PID,
// allowing the caller to clean up and proceed with a fresh spawn.
func TestEnsureServeForOtel_CleansStaleLockfile(t *testing.T) {
	dir := t.TempDir()
	hgDir := filepath.Join(dir, ".wipnote")
	_ = os.MkdirAll(hgDir, 0o755)

	// Use a PID that cannot exist: 99999999 (far beyond OS limit on any platform).
	lockPath := serveLockPath(dir)
	if err := os.WriteFile(lockPath, []byte("99999999\n"), 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	skip, stale := checkServeLock(dir)
	if skip {
		t.Error("checkServeLock: skipSpawn = true, want false (PID 99999999 is not alive)")
	}
	if !stale {
		t.Error("checkServeLock: stale = false, want true (PID 99999999 is not alive)")
	}

	// Caller should remove the stale lockfile; verify our test assumption that
	// the file still exists (the helper does not remove it — that's the caller's job).
	if _, err := os.Stat(lockPath); err != nil {
		t.Error("lockfile unexpectedly removed by checkServeLock; removal is caller's responsibility")
	}
}

// withDevcontainer is a test helper that overrides the devcontainer detector
// for the duration of the test, then restores it on cleanup.
func withDevcontainer(t *testing.T, isContainer bool) {
	t.Helper()
	orig := devcontainerDetector
	devcontainerDetector = func() bool { return isContainer }
	t.Cleanup(func() { devcontainerDetector = orig })
}

// TestResolveDashboardAddress verifies address resolution logic via env vars.
// The devcontainer state is controlled via withDevcontainer() helper.
func TestResolveDashboardAddress(t *testing.T) {
	cases := []struct {
		name           string
		isDevcontainer bool
		envBind        string
		envPort        string
		wantHost       string
		wantPort       int
	}{
		{
			name:           "default no env no devcontainer",
			isDevcontainer: false,
			wantHost:       "127.0.0.1",
			wantPort:       8080,
		},
		{
			name:           "devcontainer triggers defaults",
			isDevcontainer: true,
			wantHost:       "0.0.0.0",
			wantPort:       8088,
		},
		{
			name:           "WIPNOTE_SERVE_BIND overrides host",
			isDevcontainer: false,
			envBind:        "1.2.3.4",
			wantHost:       "1.2.3.4",
			wantPort:       8080,
		},
		{
			name:           "WIPNOTE_SERVE_PORT overrides port",
			isDevcontainer: false,
			envPort:        "9999",
			wantHost:       "127.0.0.1",
			wantPort:       9999,
		},
		{
			name:           "env vars override devcontainer defaults",
			isDevcontainer: true,
			envBind:        "1.2.3.4",
			envPort:        "9999",
			wantHost:       "1.2.3.4",
			wantPort:       9999,
		},
		{
			name:           "WIPNOTE_SERVE_PORT invalid string falls back gracefully",
			isDevcontainer: false,
			envPort:        "notanumber",
			wantHost:       "127.0.0.1",
			wantPort:       8080,
		},
		{
			name:           "WIPNOTE_SERVE_PORT zero falls back gracefully",
			isDevcontainer: false,
			envPort:        "0",
			wantHost:       "127.0.0.1",
			wantPort:       8080,
		},
		{
			name:           "WIPNOTE_SERVE_PORT negative falls back gracefully",
			isDevcontainer: false,
			envPort:        "-1",
			wantHost:       "127.0.0.1",
			wantPort:       8080,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDevcontainer(t, tc.isDevcontainer)
			// Ensure a clean slate for each sub-test.
			t.Setenv("WIPNOTE_SERVE_BIND", tc.envBind)
			t.Setenv("WIPNOTE_SERVE_PORT", tc.envPort)
			t.Setenv("REMOTE_CONTAINERS", "") // always clear to avoid cross-contamination

			gotHost, gotPort := resolveDashboardAddress()
			if gotHost != tc.wantHost {
				t.Errorf("host = %q, want %q", gotHost, tc.wantHost)
			}
			if gotPort != tc.wantPort {
				t.Errorf("port = %d, want %d", gotPort, tc.wantPort)
			}
		})
	}
}

// TestWriteRemoveServeLock verifies the write/remove lifecycle.
func TestWriteRemoveServeLock(t *testing.T) {
	dir := t.TempDir()
	hgDir := filepath.Join(dir, ".wipnote")
	_ = os.MkdirAll(hgDir, 0o755)

	writeServeLock(dir)

	data, err := os.ReadFile(serveLockPath(dir))
	if err != nil {
		t.Fatalf("lockfile not written: %v", err)
	}
	pidStr := string(data)
	pid, err := strconv.Atoi(pidStr[:len(pidStr)-1]) // strip trailing newline
	if err != nil || pid != os.Getpid() {
		t.Errorf("lockfile PID = %q, want %d", pidStr, os.Getpid())
	}

	removeServeLock(dir)
	if _, err := os.Stat(serveLockPath(dir)); !os.IsNotExist(err) {
		t.Error("lockfile still exists after removeServeLock")
	}
}
