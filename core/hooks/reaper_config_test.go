package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReaperConfig(t *testing.T) {
	t.Run("empty projectDir returns defaults", func(t *testing.T) {
		if got := ReaperSessionTTL(""); got != 1800*time.Second {
			t.Errorf("ReaperSessionTTL(\"\") = %v, want 1800s", got)
		}
		if got := ReaperCollectorGrace(""); got != 10*time.Second {
			t.Errorf("ReaperCollectorGrace(\"\") = %v, want 10s", got)
		}
		if got := ReaperDaemonReportOnly(""); got != false {
			t.Errorf("ReaperDaemonReportOnly(\"\") = %v, want false", got)
		}
	})

	t.Run("missing config.json returns defaults", func(t *testing.T) {
		dir := t.TempDir()
		if got := ReaperSessionTTL(dir); got != 1800*time.Second {
			t.Errorf("ReaperSessionTTL(no-config) = %v, want 1800s", got)
		}
		if got := ReaperCollectorGrace(dir); got != 10*time.Second {
			t.Errorf("ReaperCollectorGrace(no-config) = %v, want 10s", got)
		}
		if got := ReaperDaemonReportOnly(dir); got != false {
			t.Errorf("ReaperDaemonReportOnly(no-config) = %v, want false", got)
		}
	})

	t.Run("valid config overrides are honored", func(t *testing.T) {
		dir := t.TempDir()
		configDir := filepath.Join(dir, ".wipnote")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatalf("mkdir .wipnote: %v", err)
		}
		configJSON := `{
			"reaper_session_ttl_seconds": 60,
			"reaper_collector_grace_seconds": 3,
			"reaper_daemon_report_only": true
		}`
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}

		if got := ReaperSessionTTL(dir); got != 60*time.Second {
			t.Errorf("ReaperSessionTTL = %v, want 60s", got)
		}
		if got := ReaperCollectorGrace(dir); got != 3*time.Second {
			t.Errorf("ReaperCollectorGrace = %v, want 3s", got)
		}
		if got := ReaperDaemonReportOnly(dir); got != true {
			t.Errorf("ReaperDaemonReportOnly = %v, want true", got)
		}
	})

	t.Run("zero and negative overrides fall back to defaults", func(t *testing.T) {
		dir := t.TempDir()
		configDir := filepath.Join(dir, ".wipnote")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatalf("mkdir .wipnote: %v", err)
		}
		configJSON := `{
			"reaper_session_ttl_seconds": 0,
			"reaper_collector_grace_seconds": -5
		}`
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}

		if got := ReaperSessionTTL(dir); got != 1800*time.Second {
			t.Errorf("ReaperSessionTTL(zero) = %v, want default 1800s", got)
		}
		if got := ReaperCollectorGrace(dir); got != 10*time.Second {
			t.Errorf("ReaperCollectorGrace(negative) = %v, want default 10s", got)
		}
	})
}

func TestReaperReportOnly(t *testing.T) {
	t.Run("true config returns true", func(t *testing.T) {
		dir := t.TempDir()
		configDir := filepath.Join(dir, ".wipnote")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatalf("mkdir .wipnote: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"reaper_daemon_report_only": true}`), 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
		if got := ReaperDaemonReportOnly(dir); got != true {
			t.Errorf("ReaperDaemonReportOnly = %v, want true", got)
		}
	})

	t.Run("absent key returns false", func(t *testing.T) {
		dir := t.TempDir()
		configDir := filepath.Join(dir, ".wipnote")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatalf("mkdir .wipnote: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
		if got := ReaperDaemonReportOnly(dir); got != false {
			t.Errorf("ReaperDaemonReportOnly(absent) = %v, want false", got)
		}
	})

	t.Run("explicit false returns false", func(t *testing.T) {
		dir := t.TempDir()
		configDir := filepath.Join(dir, ".wipnote")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatalf("mkdir .wipnote: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"reaper_daemon_report_only": false}`), 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
		if got := ReaperDaemonReportOnly(dir); got != false {
			t.Errorf("ReaperDaemonReportOnly(false) = %v, want false", got)
		}
	})
}
