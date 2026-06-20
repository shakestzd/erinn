package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIsPluginInstalled_TrueWhenPresent(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsFile := filepath.Join(tmpDir, "installed_plugins.json")

	data := map[string]any{
		"version": 1,
		"plugins": map[string]any{
			"wipnote@wipnote": []map[string]string{
				{"scope": "wipnote", "installPath": "/some/path", "version": "0.39.0"},
			},
		},
	}
	b, _ := json.Marshal(data)
	os.WriteFile(pluginsFile, b, 0644)

	got := isPluginInstalledAt(pluginsFile)
	if !got {
		t.Error("expected plugin to be detected as installed")
	}
}

func TestIsPluginInstalled_FalseWhenAbsent(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsFile := filepath.Join(tmpDir, "installed_plugins.json")

	data := map[string]any{"version": 1, "plugins": map[string]any{}}
	b, _ := json.Marshal(data)
	os.WriteFile(pluginsFile, b, 0644)

	got := isPluginInstalledAt(pluginsFile)
	if got {
		t.Error("expected plugin to NOT be detected as installed")
	}
}

func TestIsPluginInstalled_FalseWhenFileNotFound(t *testing.T) {
	got := isPluginInstalledAt("/nonexistent/path/installed_plugins.json")
	if got {
		t.Error("expected false when file does not exist")
	}
}

func TestInstalledPluginVersion_ReturnsVersion(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsFile := filepath.Join(tmpDir, "installed_plugins.json")

	data := map[string]any{
		"version": 1,
		"plugins": map[string]any{
			"wipnote@wipnote": []map[string]string{
				{"scope": "wipnote", "installPath": "/some/path", "version": "0.38.0"},
			},
		},
	}
	b, _ := json.Marshal(data)
	os.WriteFile(pluginsFile, b, 0644)

	got := installedPluginVersionAt(pluginsFile)
	if got != "0.38.0" {
		t.Errorf("got %q, want %q", got, "0.38.0")
	}
}

func TestInstalledPluginVersion_EmptyWhenNotInstalled(t *testing.T) {
	tmpDir := t.TempDir()
	pluginsFile := filepath.Join(tmpDir, "installed_plugins.json")

	data := map[string]any{"version": 1, "plugins": map[string]any{}}
	b, _ := json.Marshal(data)
	os.WriteFile(pluginsFile, b, 0644)

	got := installedPluginVersionAt(pluginsFile)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestCheckVersionNotice_NoNoticeWhenCurrent(t *testing.T) {
	notice := versionNotice("0.39.0", "0.39.0")
	if notice != "" {
		t.Errorf("expected no notice, got %q", notice)
	}
}

func TestCheckVersionNotice_NoticeWhenOutdated(t *testing.T) {
	notice := versionNotice("0.38.0", "0.39.0")
	if notice == "" {
		t.Error("expected a version notice for outdated plugin")
	}
}

func TestCheckVersionNotice_NoNoticeWhenDevBuild(t *testing.T) {
	notice := versionNotice("dev", "0.39.0")
	if notice != "" {
		t.Errorf("expected no notice for dev build, got %q", notice)
	}
}
