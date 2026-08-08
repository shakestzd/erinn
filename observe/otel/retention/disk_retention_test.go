package retention

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mkSession creates .wipnote/sessions/<sid>/events.ndjson with the given
// content, an .index-offset of offsetBytes, and sets the events.ndjson mtime to
// modTime. offsetBytes == len(content) means "fully ingested".
func mkSession(t *testing.T, wipnoteDir, sid, content string, offsetBytes int, modTime time.Time) {
	t.Helper()
	dir := filepath.Join(wipnoteDir, "sessions", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(dir, "events.ndjson")
	if err := os.WriteFile(events, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".index-offset"), []byte(fmt.Sprintf("%d", offsetBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(events, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func dirExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// TestSweepNDJSON_PrunesOldIngested verifies that an OLD, inactive, fully
// ingested session is archived+pruned, while active / recent / un-ingested
// sessions are all left untouched in the same pass.
func TestSweepNDJSON_PrunesOldIngested(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	content := `{"event":"x"}` + "\n"

	old := now.Add(-40 * 24 * time.Hour)    // beyond retain window
	recent := now.Add(-2 * time.Minute)     // inside active grace
	withinRetain := now.Add(-3 * time.Hour) // inactive but young

	mkSession(t, wipnoteDir, "old-ingested", content, len(content), old)
	mkSession(t, wipnoteDir, "active", content, len(content), old)         // active session, must survive
	mkSession(t, wipnoteDir, "recent", content, len(content), recent)      // within grace
	mkSession(t, wipnoteDir, "young", content, len(content), withinRetain) // young, inactive
	mkSession(t, wipnoteDir, "old-unindexed", content, 0, old)             // old but not ingested

	cfg := Config{LogMaxBytes: DefaultLogMaxBytes, LogKeep: DefaultLogKeep, NDJSONRetainDays: 30}
	res, err := sweepNDJSON(wipnoteDir, cfg, false, sweepEnv{
		now:             now,
		activeSessionID: "active",
		grace:           time.Duration(activeGraceMinutes) * time.Minute,
	})
	if err != nil {
		t.Fatalf("sweepNDJSON: %v", err)
	}

	if res.Pruned != 1 {
		t.Errorf("expected 1 pruned, got %d", res.Pruned)
	}
	if res.BytesReclaimed != int64(len(content)) {
		t.Errorf("expected %d bytes reclaimed, got %d", len(content), res.BytesReclaimed)
	}

	sess := func(sid string) string { return filepath.Join(wipnoteDir, "sessions", sid) }
	if dirExists(sess("old-ingested")) {
		t.Error("old ingested session should have been pruned")
	}
	for _, sid := range []string{"active", "recent", "young", "old-unindexed"} {
		if !dirExists(sess(sid)) {
			t.Errorf("session %q must NOT be pruned", sid)
		}
	}

	// The pruned session's data must be recoverable from the archive.
	if err := ExtractArchive(wipnoteDir, "old-ingested"); err != nil {
		t.Errorf("pruned session not recoverable from archive: %v", err)
	}
}

// TestSweepNDJSON_MaxSessionsCap verifies the max-sessions cap force-prunes the
// stalest inactive+ingested sessions even when they are younger than the retain
// window.
func TestSweepNDJSON_MaxSessionsCap(t *testing.T) {
	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	content := `{"e":1}` + "\n"

	// 4 inactive, ingested, all young (within retain window).
	for i := 0; i < 4; i++ {
		mt := now.Add(-time.Duration(i+1) * time.Hour) // i=3 is oldest
		mkSession(t, wipnoteDir, fmt.Sprintf("s%d", i), content, len(content), mt)
	}

	cfg := Config{LogMaxBytes: DefaultLogMaxBytes, LogKeep: 2, NDJSONRetainDays: 30, NDJSONMaxSessions: 2}
	res, err := sweepNDJSON(wipnoteDir, cfg, false, sweepEnv{now: now, grace: 10 * time.Minute})
	if err != nil {
		t.Fatalf("sweepNDJSON: %v", err)
	}
	if res.Pruned != 2 {
		t.Fatalf("expected 2 pruned to satisfy cap of 2, got %d", res.Pruned)
	}
	// The two oldest (s3, s2) should be gone; the two newest (s1, s0) kept.
	sess := func(sid string) string { return filepath.Join(wipnoteDir, "sessions", sid) }
	if dirExists(sess("s3")) || dirExists(sess("s2")) {
		t.Error("stalest sessions should be pruned to satisfy cap")
	}
	if !dirExists(sess("s1")) || !dirExists(sess("s0")) {
		t.Error("newest sessions should survive under the cap")
	}
}

// TestRotateLog_CapsWithoutLosingWriter simulates an actively-writing process
// (long-lived O_APPEND fd) whose log exceeds the cap. After rotation the live
// file is truncated in place and the writer's subsequent appends must still
// land in the live file — never lost.
func TestRotateLog_CapsWithoutLosingWriter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "serve-auto.log")

	// Open a long-lived append fd, like spawnDetachedServe holds.
	w, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Write 100 bytes over a 50-byte cap.
	if _, err := w.WriteString(strings.Repeat("a", 100)); err != nil {
		t.Fatal(err)
	}

	reclaimed, err := RotateLog(logPath, 50, 2)
	if err != nil {
		t.Fatalf("RotateLog: %v", err)
	}
	if reclaimed != 100 {
		t.Errorf("expected 100 bytes reclaimed, got %d", reclaimed)
	}

	// Live file must be truncated.
	if info, _ := os.Stat(logPath); info.Size() != 0 {
		t.Errorf("expected live log truncated to 0, got %d", info.Size())
	}
	// Rotated copy .1 must hold only the bounded tail, not the whole old log.
	if data, err := os.ReadFile(logPath + ".1"); err != nil || len(data) != 50 || string(data) != strings.Repeat("a", 50) {
		t.Errorf("expected .1 rotation with 50-byte tail, got len=%d err=%v", len(data), err)
	}

	// The active writer's NEXT append must still reach the live file (O_APPEND
	// fd survived rotation — no inode was swapped, no fd closed).
	if _, err := w.WriteString("after-rotate\n"); err != nil {
		t.Fatalf("write after rotate: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after-rotate\n" {
		t.Errorf("post-rotation write lost or misplaced; live log = %q", string(data))
	}
}

// TestRotateLog_UnderCapNoOp verifies a log under the cap is untouched.
func TestRotateLog_UnderCapNoOp(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "debug.log")
	if err := os.WriteFile(logPath, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := RotateLog(logPath, 50, 2)
	if err != nil {
		t.Fatalf("RotateLog: %v", err)
	}
	if n != 0 {
		t.Errorf("expected no reclamation under cap, got %d", n)
	}
	if data, _ := os.ReadFile(logPath); string(data) != "small" {
		t.Error("log under cap must be left untouched")
	}
	if dirExists(logPath + ".1") {
		t.Error("no rotation file should be created under cap")
	}
}

func TestRotateProjectLogs_IncludesLegacyCacheServeLog(t *testing.T) {
	// Override the cache-dir resolution seam directly rather than setting
	// $XDG_CACHE_HOME: os.UserCacheDir() only consults that env var on
	// Linux — on Darwin it unconditionally returns $HOME/Library/Caches,
	// so an env-var-based redirect silently no-ops on macOS and the test
	// would spuriously fail to find its fixture file (bug-6882ecaa).
	cacheRoot := t.TempDir()
	origUserCacheDir := userCacheDir
	userCacheDir = func() (string, error) { return cacheRoot, nil }
	t.Cleanup(func() { userCacheDir = origUserCacheDir })

	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cacheLog := filepath.Join(cacheRoot, "wipnote", "serve.log")
	if err := os.MkdirAll(filepath.Dir(cacheLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheLog, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	reclaimed, err := rotateProjectLogs(wipnoteDir, Config{LogMaxBytes: 25, LogKeep: 1})
	if err != nil {
		t.Fatalf("rotateProjectLogs: %v", err)
	}
	if reclaimed != 100 {
		t.Fatalf("expected cache serve.log to be rotated, reclaimed %d", reclaimed)
	}
	info, err := os.Stat(cacheLog)
	if err != nil {
		t.Fatalf("stat live cache serve.log: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("expected live cache serve.log truncated, size=%d", info.Size())
	}
	info, err = os.Stat(cacheLog + ".1")
	if err != nil {
		t.Fatalf("stat rotated cache serve.log.1: %v", err)
	}
	if info.Size() != 25 {
		t.Fatalf("expected bounded cache serve.log.1, size=%d", info.Size())
	}
}

// TestOpenBoundedLog_RotatesBeforeOpen verifies that OpenBoundedLog rotates
// an oversized log file before returning the append fd, and that subsequent
// writes land in the freshly-truncated live file.
func TestOpenBoundedLog_RotatesBeforeOpen(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "writer.log")

	// Pre-seed the log with 100 bytes, over a 50-byte cap.
	if err := os.WriteFile(logPath, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := OpenBoundedLog(logPath, 50, 2)
	if err != nil {
		t.Fatalf("OpenBoundedLog: %v", err)
	}
	defer f.Close()

	// Live file must have been truncated before the open.
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat after open: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("expected live log truncated to 0 after bounded open, got %d bytes", info.Size())
	}

	// Rotated copy must contain the bounded tail.
	rot, err := os.ReadFile(logPath + ".1")
	if err != nil {
		t.Fatalf("read .1 rotation: %v", err)
	}
	if len(rot) != 50 {
		t.Errorf("expected 50-byte tail in .1, got %d", len(rot))
	}

	// Writes via the returned fd must land in the live file.
	if _, err := f.WriteString("new-entry\n"); err != nil {
		t.Fatalf("write via returned fd: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-entry\n" {
		t.Errorf("post-rotation write lost; live log = %q", string(data))
	}
}

// TestOpenBoundedLog_UnderCapNoRotation verifies that a log under the cap is
// opened as-is (no rotation, existing content preserved).
func TestOpenBoundedLog_UnderCapNoRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "serve-auto.log")
	if err := os.WriteFile(logPath, []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := OpenBoundedLog(logPath, 50, 2)
	if err != nil {
		t.Fatalf("OpenBoundedLog: %v", err)
	}
	defer f.Close()

	// No rotation file should exist.
	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Error("no rotation expected for log under cap")
	}

	// Existing content must still be there (file was opened for append, not truncated).
	if _, err := f.WriteString("appended\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "existing\n") || !strings.Contains(string(data), "appended\n") {
		t.Errorf("unexpected log contents: %q", string(data))
	}
}

// TestOpenBoundedLog_OldGenerationsPruned verifies that when multiple
// rotation generations already exist, shifting them drops the oldest on rotate.
func TestOpenBoundedLog_OldGenerationsPruned(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "debug.log")

	// Pre-create two existing rotations.
	for _, suf := range []string{".1", ".2"} {
		if err := os.WriteFile(logPath+suf, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Oversized live log.
	if err := os.WriteFile(logPath, []byte(strings.Repeat("y", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := OpenBoundedLog(logPath, 50, 2)
	if err != nil {
		t.Fatalf("OpenBoundedLog: %v", err)
	}
	f.Close()

	// After rotation with keep=2, .3 must not exist (pruned).
	if _, err := os.Stat(logPath + ".3"); !os.IsNotExist(err) {
		t.Error("generation .3 should not exist with keep=2")
	}
	// .1 and .2 must exist.
	for _, suf := range []string{".1", ".2"} {
		if _, err := os.Stat(logPath + suf); err != nil {
			t.Errorf("expected rotation %s to exist: %v", suf, err)
		}
	}
}

// TestLoadConfig_Defaults verifies defaults apply when config is missing and
// that present keys override them.
func TestLoadConfig_Defaults(t *testing.T) {
	// Missing config -> defaults.
	missing := LoadConfig(t.TempDir())
	if missing.LogMaxBytes != DefaultLogMaxBytes || missing.LogKeep != DefaultLogKeep ||
		missing.NDJSONRetainDays != DefaultNDJSONRetainDays {
		t.Errorf("missing config should yield defaults, got %+v", missing)
	}

	// Present config overrides.
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := `{"log_max_bytes": 1048576, "log_keep": 5, "ndjson_retain_days": 7, "ndjson_max_sessions": 50}`
	if err := os.WriteFile(filepath.Join(projectDir, ".wipnote", "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadConfig(projectDir)
	if got.LogMaxBytes != 1048576 || got.LogKeep != 5 || got.NDJSONRetainDays != 7 || got.NDJSONMaxSessions != 50 {
		t.Errorf("config override mismatch: %+v", got)
	}

	// Malformed JSON -> defaults (never fails).
	bad := t.TempDir()
	_ = os.MkdirAll(filepath.Join(bad, ".wipnote"), 0o755)
	_ = os.WriteFile(filepath.Join(bad, ".wipnote", "config.json"), []byte("{not json"), 0o644)
	if LoadConfig(bad).LogMaxBytes != DefaultLogMaxBytes {
		t.Error("malformed config must fall back to defaults")
	}
}
