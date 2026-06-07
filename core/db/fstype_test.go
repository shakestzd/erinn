package db

import (
	"testing"
)

// TestIsUnsafeForMmap is a compile-coverage test. It calls isUnsafeForMmap on
// a real tmpdir and logs the result so the environment's backing filesystem is
// visible in test output. The test passes unconditionally — the purpose is to
// ensure the function compiles and does not panic, not to assert a specific
// value (which varies per environment/OS).
func TestIsUnsafeForMmap(t *testing.T) {
	dir := t.TempDir()
	unsafe := isUnsafeForMmap(dir)
	t.Logf("isUnsafeForMmap(%q) = %v", dir, unsafe)
}

// TestBuildPragmasJournalMode verifies that BuildPragmas returns a journal_mode
// that is either "WAL" or "DELETE" depending on the tmpdir's backing filesystem.
func TestBuildPragmasJournalMode(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	p := BuildPragmas(dbPath)

	jm, ok := p["journal_mode"]
	if !ok {
		t.Fatal("BuildPragmas: journal_mode key missing")
	}
	if jm != "WAL" && jm != "DELETE" {
		t.Errorf("BuildPragmas: unexpected journal_mode %q (want WAL or DELETE)", jm)
	}
	t.Logf("BuildPragmas journal_mode = %q for tmpdir-backed path", jm)

	// Ensure other required pragmas are present.
	for _, key := range []string{"synchronous", "foreign_keys", "mmap_size", "temp_store"} {
		if _, ok := p[key]; !ok {
			t.Errorf("BuildPragmas: missing pragma %q", key)
		}
	}
}
