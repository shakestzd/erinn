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

// TestMarketplaceWipnotePresent_FalseWhenNothingInstalled verifies that
// marketplaceWipnotePresent returns false when HOME contains no plugin files.
// This guards the Fix 1 fast-path: removeMarketplaceWipnote must skip all
// subprocess calls (~4s) when the marketplace plugin was never installed.
func TestMarketplaceWipnotePresent_FalseWhenNothingInstalled(t *testing.T) {
	// Point HOME at an empty temp dir so os.UserHomeDir() finds no plugin files.
	t.Setenv("HOME", t.TempDir())

	got := marketplaceWipnotePresent()
	if got {
		t.Error("marketplaceWipnotePresent() = true, want false when no plugin files exist")
	}
}

// TestMarketplaceWipnotePresent_TrueWhenMarketplaceDirExists verifies that
// marketplaceWipnotePresent returns true when the marketplaces/wipnote dir is present,
// even when installed_plugins.json has no entry.
func TestMarketplaceWipnotePresent_TrueWhenMarketplaceDirExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create the marketplace dir that indicates a cloned plugin.
	mktDir := filepath.Join(home, ".claude", "plugins", "marketplaces", "wipnote")
	if err := os.MkdirAll(mktDir, 0755); err != nil {
		t.Fatalf("mkdir marketplaces/wipnote: %v", err)
	}

	got := marketplaceWipnotePresent()
	if !got {
		t.Error("marketplaceWipnotePresent() = false, want true when marketplaces/wipnote dir exists")
	}
}

// TestMarketplaceWipnotePresent_TrueWhenPluginInstalled verifies that
// marketplaceWipnotePresent returns true when installed_plugins.json records the plugin.
func TestMarketplaceWipnotePresent_TrueWhenPluginInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Write a valid installed_plugins.json with wipnote entry.
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	data := map[string]any{
		"version": 1,
		"plugins": map[string]any{
			"wipnote@wipnote": []map[string]string{
				{"scope": "wipnote", "installPath": "/some/path", "version": "0.39.0"},
			},
		},
	}
	b, _ := json.Marshal(data)
	if err := os.WriteFile(filepath.Join(pluginsDir, "installed_plugins.json"), b, 0644); err != nil {
		t.Fatalf("write installed_plugins.json: %v", err)
	}

	got := marketplaceWipnotePresent()
	if !got {
		t.Error("marketplaceWipnotePresent() = false, want true when plugin entry exists in installed_plugins.json")
	}
}

// TestMarketplaceWipnotePresent_TrueWhenLocalMarketplaceDirExists verifies that
// marketplaceWipnotePresent returns true when the cache/local-marketplace/wipnote dir is present.
// This detects a local-marketplace-only install that would otherwise be missed.
func TestMarketplaceWipnotePresent_TrueWhenLocalMarketplaceDirExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create the local-marketplace dir that indicates a cached local plugin.
	mktDir := filepath.Join(home, ".claude", "plugins", "cache", "local-marketplace", "wipnote")
	if err := os.MkdirAll(mktDir, 0755); err != nil {
		t.Fatalf("mkdir cache/local-marketplace/wipnote: %v", err)
	}

	got := marketplaceWipnotePresent()
	if !got {
		t.Error("marketplaceWipnotePresent() = false, want true when cache/local-marketplace/wipnote dir exists")
	}
}

// TestMarketplaceWipnotePresent_TrueWhenHtmlgraphDirExists verifies that
// marketplaceWipnotePresent returns true when legacy htmlgraph artifact dirs exist.
// This detects old htmlgraph installs that need cleanup.
func TestMarketplaceWipnotePresent_TrueWhenHtmlgraphDirExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a legacy htmlgraph cache dir.
	mktDir := filepath.Join(home, ".claude", "plugins", "cache", "htmlgraph")
	if err := os.MkdirAll(mktDir, 0755); err != nil {
		t.Fatalf("mkdir cache/htmlgraph: %v", err)
	}

	got := marketplaceWipnotePresent()
	if !got {
		t.Error("marketplaceWipnotePresent() = false, want true when cache/htmlgraph dir exists")
	}
}
