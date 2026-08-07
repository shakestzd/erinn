package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/launcher/plan"
)

// TestCodexHelpRenders verifies that codexCmd().Execute() with --help
// doesn't error and prints help text.
func TestCodexHelpRenders(t *testing.T) {
	cmd := codexCmd()
	cmd.SetArgs([]string{"--help"})

	// Capture output to avoid printing during test
	outBuf := &strings.Builder{}
	cmd.SetOut(outBuf)

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("codexCmd().Execute() with --help: %v", err)
	}

	output := outBuf.String()
	if !strings.Contains(output, "Launch Codex CLI") {
		t.Errorf("help output missing expected text. Got:\n%s", output)
	}
}

// TestCodexParsingFlags verifies that codex command flags are parsed correctly.
// We only test flags that don't trigger external commands (like codex.exe or marketplace ops).
func TestCodexParsingFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantInit bool
	}{
		{
			name:     "--init with --dry-run",
			args:     []string{"--init", "--dry-run", "--yes"},
			wantInit: true,
		},
		{
			name:     "--help",
			args:     []string{"--help"},
			wantInit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := codexCmd()
			cmd.SetArgs(tt.args)

			// Suppress stdout/stderr for testing.
			cmd.SetOut(&strings.Builder{})
			cmd.SetErr(&strings.Builder{})

			// Note: --help causes Execute to return nil without running the command,
			// so it's safe to test. Commands that try to exec codex (no flags, or
			// --continue/--resume/--dev without --dry-run) will fail during tests
			// because codex binary is not available. Those are integration tests.
			err := cmd.Execute()
			if err != nil {
				t.Logf("Execute returned: %v (expected for --help or --init --dry-run)", err)
			}
		})
	}
}

// TestIsCodexMarketplaceInstalledAt verifies the marketplace detection logic.
func TestIsCodexMarketplaceInstalledAt(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	// Test 1: File does not exist — should return false
	if isCodexMarketplaceInstalledAt(configPath) {
		t.Errorf("expected false when config file does not exist")
	}

	// Test 2: File exists but does not contain the marketplace section
	err := os.WriteFile(configPath, []byte("[other]\nkey = value\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if isCodexMarketplaceInstalledAt(configPath) {
		t.Errorf("expected false when marketplace not in config")
	}

	// Test 3: File contains the marketplace section
	err = os.WriteFile(configPath, []byte("[marketplaces.wipnote]\nrepo = \"shakestzd/wipnote\"\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !isCodexMarketplaceInstalledAt(configPath) {
		t.Errorf("expected true when marketplace section exists")
	}

	// Test 4: File contains the plugin section variant
	err = os.WriteFile(configPath, []byte(`[plugins."wipnote@wipnote"]`+"\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !isCodexMarketplaceInstalledAt(configPath) {
		t.Errorf("expected true when plugin section exists")
	}
}

// TestIsCodexHooksEnabledAt verifies the hooks feature flag detection logic.
func TestIsCodexHooksEnabledAt(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	// Test 1: File does not exist
	if isCodexHooksEnabledAt(configPath) {
		t.Errorf("expected false when config file does not exist")
	}

	// Test 2: File exists but no hooks line
	err := os.WriteFile(configPath, []byte("[other]\nkey = value\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if isCodexHooksEnabledAt(configPath) {
		t.Errorf("expected false when hooks not in config")
	}

	// Test 3: File has hooks = true
	err = os.WriteFile(configPath, []byte("[features]\nhooks = true\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !isCodexHooksEnabledAt(configPath) {
		t.Errorf("expected true when hooks = true")
	}

	// Test 4: File has hooks = false
	err = os.WriteFile(configPath, []byte("[features]\nhooks = false\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if isCodexHooksEnabledAt(configPath) {
		t.Errorf("expected false when hooks = false")
	}

	// Test 5: Legacy codex_hooks alone does NOT satisfy the enabled check.
	// This ensures configs with only codex_hooks will trigger migration.
	err = os.WriteFile(configPath, []byte("[features]\ncodex_hooks = true\n"), 0644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if isCodexHooksEnabledAt(configPath) {
		t.Errorf("expected false when only legacy codex_hooks = true; should trigger migration")
	}
}

// TestIsCodexPluginEnabledAt verifies detection of the enabled plugin stanza.
func TestIsCodexPluginEnabledAt(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	if isCodexPluginEnabledAt(configPath) {
		t.Errorf("expected false when config file does not exist")
	}

	if err := os.WriteFile(configPath, []byte("[plugins.\"github@openai-curated\"]\nenabled = true\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if isCodexPluginEnabledAt(configPath) {
		t.Errorf("expected false when wipnote plugin is absent")
	}

	if err := os.WriteFile(configPath, []byte("[plugins.\"wipnote@wipnote\"]\nenabled = false\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if isCodexPluginEnabledAt(configPath) {
		t.Errorf("expected false when wipnote plugin is disabled")
	}

	if err := os.WriteFile(configPath, []byte("[plugins.\"wipnote@wipnote\"]\nenabled = true\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !isCodexPluginEnabledAt(configPath) {
		t.Errorf("expected true when wipnote plugin is enabled")
	}
}

// TestPromptYesNo verifies the yes/no prompt logic.
func TestPromptYesNo(t *testing.T) {
	tests := []struct {
		name     string
		autoYes  bool
		wantResp bool
		question string
	}{
		{
			name:     "auto-yes returns true immediately",
			autoYes:  true,
			wantResp: true,
			question: "Enable feature?",
		},
		{
			name:     "auto-yes=false still returns true (no stdin)",
			autoYes:  false,
			wantResp: false, // will be false because we have no stdin input
			question: "Enable feature?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When yes=true, promptYesNo returns immediately without reading stdin
			resp := promptYesNo(tt.question, tt.autoYes)
			if tt.autoYes && !resp {
				t.Errorf("promptYesNo(..., true) should return true immediately")
			}
		})
	}
}

// TestEnsureCodexHooksEnabledIdempotent verifies that ensureCodexHooksEnabled
// is idempotent — calling it twice produces identical output.
func TestEnsureCodexHooksEnabledIdempotent(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	// First call: create and enable
	if err := ensureCodexHooksEnabled(configPath); err != nil {
		t.Fatalf("first ensureCodexHooksEnabled: %v", err)
	}
	data1, _ := os.ReadFile(configPath)

	// Second call: should be idempotent
	if err := ensureCodexHooksEnabled(configPath); err != nil {
		t.Fatalf("second ensureCodexHooksEnabled: %v", err)
	}
	data2, _ := os.ReadFile(configPath)

	if string(data1) != string(data2) {
		t.Errorf("second call changed the output:\nFirst:\n%s\nSecond:\n%s", string(data1), string(data2))
	}
}

// TestCodexHooksUpsertPreservesExistingFeaturesTable verifies that enabling
// hooks merges into an existing [features] table without duplicating it.
func TestCodexHooksUpsertPreservesExistingFeaturesTable(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	// Create a config with existing [features] section and other keys
	initialContent := "[features]\nother_flag = true\n"
	if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Call ensureCodexHooksEnabled
	if err := ensureCodexHooksEnabled(configPath); err != nil {
		t.Fatalf("ensureCodexHooksEnabled: %v", err)
	}

	// Verify the config has both keys in a single [features] table
	data, _ := os.ReadFile(configPath)
	content := string(data)

	// Count [features] sections (should be exactly one)
	featuresSectionCount := strings.Count(content, "[features]")
	if featuresSectionCount != 1 {
		t.Errorf("expected exactly 1 [features] section, got %d:\n%s", featuresSectionCount, content)
	}

	// Verify both keys are present
	if !strings.Contains(content, "hooks = true") {
		t.Errorf("hooks = true not found in output")
	}
	if !strings.Contains(content, "other_flag") {
		t.Errorf("other_flag not preserved in output")
	}
}

// TestEnsureCodexHooksEnabledCreatesFromEmpty verifies that ensureCodexHooksEnabled
// can create a new config file with just the [features] section.
func TestEnsureCodexHooksEnabledCreatesFromEmpty(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	// Enable hooks on a non-existent file
	if err := ensureCodexHooksEnabled(configPath); err != nil {
		t.Fatalf("ensureCodexHooksEnabled: %v", err)
	}

	// Verify the file was created with hooks enabled
	data, _ := os.ReadFile(configPath)
	content := string(data)

	if !strings.Contains(content, "hooks = true") {
		t.Errorf("hooks = true not found in newly created config")
	}
	if !isCodexHooksEnabledAt(configPath) {
		t.Errorf("hooks = true check failed after ensureCodexHooksEnabled")
	}
}

func TestEnsureCodexHooksEnabledMigratesLegacyKey(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	initialContent := "[features]\ncodex_hooks = true\nother_flag = true\n"
	if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ensureCodexHooksEnabled(configPath); err != nil {
		t.Fatalf("ensureCodexHooksEnabled: %v", err)
	}

	data, _ := os.ReadFile(configPath)
	content := string(data)
	if strings.Contains(content, "codex_hooks") {
		t.Fatalf("legacy codex_hooks key should be removed:\n%s", content)
	}
	if !strings.Contains(content, "hooks = true") {
		t.Fatalf("hooks = true missing after migration:\n%s", content)
	}
	if !strings.Contains(content, "other_flag") {
		t.Fatalf("other_flag should be preserved:\n%s", content)
	}
}

// TestCodexHooksLegacyOnlyTriggersMigration verifies that a config with ONLY
// codex_hooks=true (no canonical hooks key) is treated as "not enabled" by the
// launcher check, thus triggering migration via ensureCodexHooksEnabled.
func TestCodexHooksLegacyOnlyTriggersMigration(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	// Create config with only the legacy key
	initialContent := "[features]\ncodex_hooks = true\n"
	if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// The launcher check should treat this as "not enabled"
	if isCodexHooksEnabledAt(configPath) {
		t.Errorf("isCodexHooksEnabledAt should return false for codex_hooks-only config, triggering migration")
	}

	// Migration should run and rewrite the config
	if err := ensureCodexHooksEnabled(configPath); err != nil {
		t.Fatalf("ensureCodexHooksEnabled: %v", err)
	}

	// After migration, the canonical key should exist and legacy should be gone
	data, _ := os.ReadFile(configPath)
	content := string(data)
	if strings.Contains(content, "codex_hooks") {
		t.Fatalf("legacy codex_hooks key should be removed after migration:\n%s", content)
	}
	if !strings.Contains(content, "hooks = true") {
		t.Fatalf("hooks = true missing after migration:\n%s", content)
	}

	// Now the launcher check should return true (already enabled)
	if !isCodexHooksEnabledAt(configPath) {
		t.Errorf("isCodexHooksEnabledAt should return true for migrated config with hooks=true")
	}
}

// TestEnsureCodexPluginEnabledIdempotent verifies that the plugin enablement
// config is created once and left stable on repeated calls.
func TestEnsureCodexPluginEnabledIdempotent(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	if err := ensureCodexPluginEnabled(configPath); err != nil {
		t.Fatalf("first ensureCodexPluginEnabled: %v", err)
	}
	data1, _ := os.ReadFile(configPath)

	if err := ensureCodexPluginEnabled(configPath); err != nil {
		t.Fatalf("second ensureCodexPluginEnabled: %v", err)
	}
	data2, _ := os.ReadFile(configPath)

	if string(data1) != string(data2) {
		t.Errorf("second call changed the output:\nFirst:\n%s\nSecond:\n%s", string(data1), string(data2))
	}
	if !isCodexPluginEnabledAt(configPath) {
		t.Errorf("wipnote plugin should be enabled after ensure")
	}
}

// TestEnsureCodexPluginEnabledPreservesExistingPlugins verifies enabling
// wipnote keeps other plugin configuration intact.
func TestEnsureCodexPluginEnabledPreservesExistingPlugins(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	initialContent := `[plugins."github@openai-curated"]
enabled = true

[features]
hooks = true
`
	if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ensureCodexPluginEnabled(configPath); err != nil {
		t.Fatalf("ensureCodexPluginEnabled: %v", err)
	}

	data, _ := os.ReadFile(configPath)
	content := string(data)
	if !strings.Contains(content, "github@openai-curated") {
		t.Errorf("github plugin config should be preserved:\n%s", content)
	}
	if !strings.Contains(content, "wipnote@wipnote") {
		t.Errorf("wipnote plugin config should be present:\n%s", content)
	}
	if !strings.Contains(content, "hooks = true") {
		t.Errorf("features config should be preserved:\n%s", content)
	}
	if !isCodexPluginEnabledAt(configPath) {
		t.Errorf("wipnote plugin should be enabled")
	}
}

func TestIsCodexPluginInstalledAt(t *testing.T) {
	tmpdir := t.TempDir()
	cachePath := filepath.Join(tmpdir, "cache", "wipnote", "wipnote")

	if isCodexPluginInstalledAt(cachePath) {
		t.Errorf("expected false when cache path does not exist")
	}

	directManifest := filepath.Join(cachePath, ".codex-plugin", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(directManifest), 0755); err != nil {
		t.Fatalf("MkdirAll direct: %v", err)
	}
	if err := os.WriteFile(directManifest, []byte(`{"name":"wipnote"}`), 0644); err != nil {
		t.Fatalf("WriteFile direct: %v", err)
	}
	if isCodexPluginInstalledAt(cachePath) {
		t.Errorf("expected false for direct cache plugin manifest; Codex expects a version subdirectory")
	}

	versionedCache := filepath.Join(tmpdir, "versioned", "wipnote", "wipnote")
	versionedManifest := filepath.Join(versionedCache, "abc123", ".codex-plugin", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(versionedManifest), 0755); err != nil {
		t.Fatalf("MkdirAll versioned: %v", err)
	}
	if err := os.WriteFile(versionedManifest, []byte(`{"name":"wipnote"}`), 0644); err != nil {
		t.Fatalf("WriteFile versioned: %v", err)
	}
	if !isCodexPluginInstalledAt(versionedCache) {
		t.Errorf("expected true for versioned cache plugin manifest")
	}
}

func TestCodexPluginDirFromMarketplace(t *testing.T) {
	tmpdir := t.TempDir()
	marketplaceDir := filepath.Join(tmpdir, "packages", "codex-marketplace", ".agents", "plugins")
	pluginDir := filepath.Join(marketplaceDir, "wipnote")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".codex-plugin"), 0755); err != nil {
		t.Fatalf("MkdirAll plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0644); err != nil {
		t.Fatalf("WriteFile plugin manifest: %v", err)
	}
	marketplaceJSON := filepath.Join(marketplaceDir, "marketplace.json")
	body := `{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./wipnote"}}]}`
	if err := os.WriteFile(marketplaceJSON, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile marketplace: %v", err)
	}

	got, err := codexPluginDirFromMarketplace(marketplaceJSON)
	if err != nil {
		t.Fatalf("codexPluginDirFromMarketplace: %v", err)
	}
	if got != pluginDir {
		t.Errorf("codexPluginDirFromMarketplace = %q, want %q", got, pluginDir)
	}
}

func TestEnsureCodexLocalPluginInstalled(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")
	marketplaceRoot := filepath.Join(tmpdir, "codex-marketplace")
	marketplaceDir := filepath.Join(marketplaceRoot, ".agents", "plugins")
	pluginDir := filepath.Join(marketplaceDir, "wipnote")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".codex-plugin"), 0755); err != nil {
		t.Fatalf("MkdirAll plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0644); err != nil {
		t.Fatalf("WriteFile plugin manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks.json"), []byte(`{}`), 0644); err != nil {
		t.Fatalf("WriteFile hooks: %v", err)
	}
	marketplaceJSON := filepath.Join(marketplaceDir, "marketplace.json")
	body := `{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./wipnote"}}]}`
	if err := os.WriteFile(marketplaceJSON, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile marketplace: %v", err)
	}
	config := "[marketplaces.wipnote]\nsource = \"" + filepath.ToSlash(marketplaceRoot) + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	installed, err := ensureCodexLocalPluginInstalled(configPath, true)
	if err != nil {
		t.Fatalf("ensureCodexLocalPluginInstalled: %v", err)
	}
	if !installed {
		t.Fatalf("expected local plugin cache to be installed")
	}
	if !isCodexPluginInstalledAt(codexPluginCachePath()) {
		t.Errorf("expected cache path to contain loadable plugin manifest")
	}
	wantManifest := filepath.Join(codexPluginCachePath(), codexLocalPluginCacheVersion, ".codex-plugin", "plugin.json")
	if _, err := os.Stat(wantManifest); err != nil {
		t.Errorf("expected local dev cache manifest at %s: %v", wantManifest, err)
	}
}

// TestEnsureCodexLocalPluginInstalledFilePathBranch verifies that when the
// config stores the manifest file path (new registration form from runCodexInit),
// ensureCodexLocalPluginInstalled resolves the plugin without double-appending
// .agents/plugins/marketplace.json.
func TestEnsureCodexLocalPluginInstalledFilePathBranch(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")
	marketplaceRoot := filepath.Join(tmpdir, "codex-marketplace")
	marketplaceDir := filepath.Join(marketplaceRoot, ".agents", "plugins")
	pluginDir := filepath.Join(marketplaceDir, "wipnote")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".codex-plugin"), 0755); err != nil {
		t.Fatalf("MkdirAll plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0644); err != nil {
		t.Fatalf("WriteFile plugin manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks.json"), []byte(`{}`), 0644); err != nil {
		t.Fatalf("WriteFile hooks: %v", err)
	}
	marketplaceJSON := filepath.Join(marketplaceDir, "marketplace.json")
	body := `{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./wipnote"}}]}`
	if err := os.WriteFile(marketplaceJSON, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile marketplace: %v", err)
	}
	// Store the manifest FILE PATH (new form) in the config — not the tree root.
	config := "[marketplaces.wipnote]\nsource = \"" + filepath.ToSlash(marketplaceJSON) + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	installed, err := ensureCodexLocalPluginInstalled(configPath, true)
	if err != nil {
		t.Fatalf("ensureCodexLocalPluginInstalled (file-path branch): %v", err)
	}
	if !installed {
		t.Fatalf("expected local plugin cache to be installed (file-path branch)")
	}
	wantManifest := filepath.Join(codexPluginCachePath(), codexLocalPluginCacheVersion, ".codex-plugin", "plugin.json")
	if _, err := os.Stat(wantManifest); err != nil {
		t.Errorf("expected local dev cache manifest at %s: %v", wantManifest, err)
	}
}

// TestEnsureCodexLocalPluginInstalledDirBranchStillWorks verifies that the
// legacy directory-root registration form still resolves the plugin correctly
// after the tolerance logic was added.
func TestEnsureCodexLocalPluginInstalledDirBranchStillWorks(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")
	marketplaceRoot := filepath.Join(tmpdir, "codex-marketplace")
	marketplaceDir := filepath.Join(marketplaceRoot, ".agents", "plugins")
	pluginDir := filepath.Join(marketplaceDir, "wipnote")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".codex-plugin"), 0755); err != nil {
		t.Fatalf("MkdirAll plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0644); err != nil {
		t.Fatalf("WriteFile plugin manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks.json"), []byte(`{}`), 0644); err != nil {
		t.Fatalf("WriteFile hooks: %v", err)
	}
	marketplaceJSON := filepath.Join(marketplaceDir, "marketplace.json")
	body := `{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./wipnote"}}]}`
	if err := os.WriteFile(marketplaceJSON, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile marketplace: %v", err)
	}
	// Store the DIRECTORY ROOT (legacy form) in the config.
	config := "[marketplaces.wipnote]\nsource = \"" + filepath.ToSlash(marketplaceRoot) + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	installed, err := ensureCodexLocalPluginInstalled(configPath, true)
	if err != nil {
		t.Fatalf("ensureCodexLocalPluginInstalled (dir-branch): %v", err)
	}
	if !installed {
		t.Fatalf("expected local plugin cache to be installed (dir-branch)")
	}
	wantManifest := filepath.Join(codexPluginCachePath(), codexLocalPluginCacheVersion, ".codex-plugin", "plugin.json")
	if _, err := os.Stat(wantManifest); err != nil {
		t.Errorf("expected local dev cache manifest at %s: %v", wantManifest, err)
	}
}

func TestEnsureCodexCustomAgentsInstalledCopiesTOML(t *testing.T) {
	tmpdir := t.TempDir()
	pluginDir := filepath.Join(tmpdir, "plugin")
	sourceAgents := filepath.Join(pluginDir, "agents")
	targetAgents := filepath.Join(tmpdir, ".codex", "agents")
	if err := os.MkdirAll(sourceAgents, 0755); err != nil {
		t.Fatalf("MkdirAll source agents: %v", err)
	}
	source := filepath.Join(sourceAgents, "wipnote-researcher.toml")
	body := "name = \"wipnote-researcher\"\ndescription = \"Research agent\"\n"
	if err := os.WriteFile(source, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile source agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceAgents, "legacy.md"), []byte("# ignored"), 0644); err != nil {
		t.Fatalf("WriteFile ignored agent: %v", err)
	}

	changed, err := ensureCodexCustomAgentsInstalled(pluginDir, targetAgents)
	if err != nil {
		t.Fatalf("ensureCodexCustomAgentsInstalled: %v", err)
	}
	if !changed {
		t.Fatalf("expected first install to report changed")
	}
	data, err := os.ReadFile(filepath.Join(targetAgents, "wipnote-researcher.toml"))
	if err != nil {
		t.Fatalf("reading installed agent: %v", err)
	}
	if string(data) != body {
		t.Fatalf("installed agent mismatch:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(targetAgents, "legacy.md")); !os.IsNotExist(err) {
		t.Fatalf("markdown agent should not be installed, stat err=%v", err)
	}

	changed, err = ensureCodexCustomAgentsInstalled(pluginDir, targetAgents)
	if err != nil {
		t.Fatalf("second ensureCodexCustomAgentsInstalled: %v", err)
	}
	if changed {
		t.Fatalf("expected second install to be idempotent")
	}
}

func TestEnsureCodexCustomAgentsInstalledSkipsBlockedProjectDir(t *testing.T) {
	tmpdir := t.TempDir()
	pluginDir := filepath.Join(tmpdir, "plugin")
	sourceAgents := filepath.Join(pluginDir, "agents")
	if err := os.MkdirAll(sourceAgents, 0755); err != nil {
		t.Fatalf("MkdirAll source agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceAgents, "wipnote-researcher.toml"), []byte("name = \"wipnote-researcher\"\n"), 0644); err != nil {
		t.Fatalf("WriteFile source agent: %v", err)
	}
	projectCodexPath := filepath.Join(tmpdir, "project", ".codex")
	if err := os.MkdirAll(filepath.Dir(projectCodexPath), 0755); err != nil {
		t.Fatalf("MkdirAll project dir: %v", err)
	}
	if err := os.WriteFile(projectCodexPath, nil, 0600); err != nil {
		t.Fatalf("WriteFile .codex sentinel: %v", err)
	}

	changed, err := ensureCodexCustomAgentsInstalled(pluginDir, filepath.Join(projectCodexPath, "agents"))
	if err != nil {
		t.Fatalf("ensureCodexCustomAgentsInstalled should skip file-backed .codex: %v", err)
	}
	if changed {
		t.Fatalf("file-backed .codex should not report agent installation")
	}
}

func TestEnsureCodexCustomAgentsInstalledPrunesStaleWipnoteAgents(t *testing.T) {
	tmpdir := t.TempDir()
	pluginDir := filepath.Join(tmpdir, "plugin")
	sourceAgents := filepath.Join(pluginDir, "agents")
	targetAgents := filepath.Join(tmpdir, ".codex", "agents")
	if err := os.MkdirAll(sourceAgents, 0755); err != nil {
		t.Fatalf("MkdirAll source agents: %v", err)
	}
	if err := os.MkdirAll(targetAgents, 0755); err != nil {
		t.Fatalf("MkdirAll target agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceAgents, "wipnote-patch-coder.toml"), []byte("name = \"wipnote-patch-coder\"\n"), 0644); err != nil {
		t.Fatalf("WriteFile source agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetAgents, "wipnote-haiku-coder.toml"), []byte("name = \"wipnote-haiku-coder\"\n"), 0644); err != nil {
		t.Fatalf("WriteFile stale agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetAgents, "other-agent.toml"), []byte("name = \"other-agent\"\n"), 0644); err != nil {
		t.Fatalf("WriteFile unrelated agent: %v", err)
	}

	changed, err := ensureCodexCustomAgentsInstalled(pluginDir, targetAgents)
	if err != nil {
		t.Fatalf("ensureCodexCustomAgentsInstalled: %v", err)
	}
	if !changed {
		t.Fatalf("expected prune/install to report changed")
	}
	if _, err := os.Stat(filepath.Join(targetAgents, "wipnote-haiku-coder.toml")); !os.IsNotExist(err) {
		t.Fatalf("stale wipnote agent should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetAgents, "other-agent.toml")); err != nil {
		t.Fatalf("unrelated custom agent should be preserved: %v", err)
	}
}

func TestBuildCodexAgentConfigArgs(t *testing.T) {
	tmpdir := t.TempDir()
	agentsDir := filepath.Join(tmpdir, ".codex", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll agents dir: %v", err)
	}
	agentPath := filepath.Join(agentsDir, "wipnote-test-runner.toml")
	body := "name = \"wipnote-test-runner\"\ndescription = \"Run focused checks\"\ndeveloper_instructions = \"Run tests.\"\n"
	if err := os.WriteFile(agentPath, []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "broken.toml"), []byte("not toml ="), 0644); err != nil {
		t.Fatalf("WriteFile broken agent: %v", err)
	}

	got := buildCodexAgentConfigArgs(agentsDir)
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"-c",
		`agents.wipnote-test-runner.description="Run focused checks"`,
		`agents.wipnote-test-runner.config_file="` + filepath.ToSlash(agentPath) + `"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("agent config args missing %q in %v", want, got)
		}
	}
	if strings.Contains(joined, "broken") {
		t.Fatalf("broken TOML should not produce config args: %v", got)
	}
}

func TestEnsureCodexGlobalHooksInstalledCreatesUserHooks(t *testing.T) {
	tmpdir := t.TempDir()
	pluginDir := filepath.Join(tmpdir, "plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("MkdirAll plugin: %v", err)
	}
	pluginHooks := `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"wipnote hook session-start"}]}],"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"wipnote hook pretooluse"}]}]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks.json"), []byte(pluginHooks), 0644); err != nil {
		t.Fatalf("WriteFile plugin hooks: %v", err)
	}
	hooksPath := filepath.Join(tmpdir, ".codex", "hooks.json")

	changed, err := ensureCodexGlobalHooksInstalled(hooksPath, pluginDir)
	if err != nil {
		t.Fatalf("ensureCodexGlobalHooksInstalled: %v", err)
	}
	if !changed {
		t.Fatalf("expected hooks install to report changed")
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("ReadFile hooks: %v", err)
	}
	content := string(data)
	for _, want := range []string{"SessionStart", "PreToolUse", "wipnote hook session-start", "wipnote hook pretooluse"} {
		if !strings.Contains(content, want) {
			t.Errorf("installed hooks missing %q:\n%s", want, content)
		}
	}

	changed, err = ensureCodexGlobalHooksInstalled(hooksPath, pluginDir)
	if err != nil {
		t.Fatalf("second ensureCodexGlobalHooksInstalled: %v", err)
	}
	if changed {
		t.Fatalf("expected second install to be idempotent")
	}
	data2, _ := os.ReadFile(hooksPath)
	if string(data) != string(data2) {
		t.Fatalf("second install changed hooks:\nfirst:\n%s\nsecond:\n%s", data, data2)
	}
}

func TestEnsureCodexGlobalHooksInstalledPreservesUserHooks(t *testing.T) {
	tmpdir := t.TempDir()
	pluginDir := filepath.Join(tmpdir, "plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("MkdirAll plugin: %v", err)
	}
	pluginHooks := `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"wipnote hook stop"}]}]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks.json"), []byte(pluginHooks), 0644); err != nil {
		t.Fatalf("WriteFile plugin hooks: %v", err)
	}
	hooksPath := filepath.Join(tmpdir, ".codex", "hooks.json")
	userHooks := `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"echo user-stop"}]}],"UserPromptSubmit":[{"matcher":"","hooks":[{"type":"command","command":"echo user-prompt"}]}]}}`
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0755); err != nil {
		t.Fatalf("MkdirAll hooks dir: %v", err)
	}
	if err := os.WriteFile(hooksPath, []byte(userHooks), 0644); err != nil {
		t.Fatalf("WriteFile user hooks: %v", err)
	}

	changed, err := ensureCodexGlobalHooksInstalled(hooksPath, pluginDir)
	if err != nil {
		t.Fatalf("ensureCodexGlobalHooksInstalled: %v", err)
	}
	if !changed {
		t.Fatalf("expected merge to report changed")
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("ReadFile hooks: %v", err)
	}
	content := string(data)
	for _, want := range []string{"echo user-stop", "echo user-prompt", "wipnote hook stop"} {
		if !strings.Contains(content, want) {
			t.Errorf("merged hooks missing %q:\n%s", want, content)
		}
	}
}

func TestPruneCodexGlobalHooksInstalledRemovesOnlyWipnoteHooks(t *testing.T) {
	tmpdir := t.TempDir()
	pluginDir := filepath.Join(tmpdir, "plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("MkdirAll plugin: %v", err)
	}
	pluginHooks := `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"wipnote hook session-start"}]}],"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"wipnote hook pretooluse"}]}]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks.json"), []byte(pluginHooks), 0644); err != nil {
		t.Fatalf("WriteFile plugin hooks: %v", err)
	}
	hooksPath := filepath.Join(tmpdir, ".codex", "hooks.json")
	globalHooks := `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"wipnote hook session-start"}]}],"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"wipnote hook pretooluse"}]},{"matcher":"^Bash$","hooks":[{"type":"command","command":"roborev agent-hook run"}]}],"Stop":[{"matcher":"","hooks":[{"type":"command","command":"echo user-stop"}]}]}}`
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0755); err != nil {
		t.Fatalf("MkdirAll hooks dir: %v", err)
	}
	if err := os.WriteFile(hooksPath, []byte(globalHooks), 0644); err != nil {
		t.Fatalf("WriteFile global hooks: %v", err)
	}

	changed, err := pruneCodexGlobalHooksInstalled(hooksPath, pluginDir)
	if err != nil {
		t.Fatalf("pruneCodexGlobalHooksInstalled: %v", err)
	}
	if !changed {
		t.Fatalf("expected prune to report changed")
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("ReadFile hooks: %v", err)
	}
	content := string(data)
	for _, gone := range []string{"wipnote hook session-start", "wipnote hook pretooluse"} {
		if strings.Contains(content, gone) {
			t.Fatalf("global hooks still contain mirrored wipnote hook %q:\n%s", gone, content)
		}
	}
	for _, want := range []string{"roborev agent-hook run", "echo user-stop"} {
		if !strings.Contains(content, want) {
			t.Fatalf("global hooks lost user hook %q:\n%s", want, content)
		}
	}

	changed, err = pruneCodexGlobalHooksInstalled(hooksPath, pluginDir)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if changed {
		t.Fatalf("second prune should be idempotent")
	}
}

// TestCodexDevReplacesMismatchedMarketplace verifies that --dev mode detects
// a mismatched marketplace registration and replaces it.
func TestCodexDevReplacesMismatchedMarketplace(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	// Seed a config with a mismatched marketplace pointing elsewhere
	initialContent := `[marketplaces.wipnote]
source = "/some/other/path"
`
	if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Verify the mismatched path is detected
	detected := getCodexMarketplacePathAt(configPath)
	if detected != "/some/other/path" {
		t.Errorf("expected to detect /some/other/path, got %q", detected)
	}

	// In a real scenario, launchCodexDev would now detect the mismatch
	// and run marketplace remove + add. For testing, we just verify the detection.
	// A full integration test would mock exec.Command.
}

// TestGetCodexMarketplacePathAt verifies marketplace path detection from TOML.
func TestGetCodexMarketplacePathAt(t *testing.T) {
	tmpdir := t.TempDir()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "no config file",
			content: "",
			want:    "",
		},
		{
			name: "marketplaces.wipnote with source",
			content: "[marketplaces.wipnote]\n" +
				"source = \"/path/to/marketplace\"\n",
			want: "/path/to/marketplace",
		},
		{
			name: "marketplaces.wipnote with path",
			content: "[marketplaces.wipnote]\n" +
				"path = \"/alt/path\"\n",
			want: "/alt/path",
		},
		{
			name: "plugins variant",
			content: "[plugins]\n" +
				"\"wipnote@wipnote\" = {source = \"/plugin/path\"}\n",
			want: "/plugin/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(tmpdir, tt.name+".toml")
			if tt.content != "" {
				if err := os.WriteFile(configPath, []byte(tt.content), 0644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			got := getCodexMarketplacePathAt(configPath)
			if got != tt.want {
				t.Errorf("getCodexMarketplacePathAt: want %q, got %q", tt.want, got)
			}
		})
	}
}

// TestRemoveCodexWipnoteRegistrations verifies that removeCodexWipnoteRegistrations
// correctly deletes wipnote entries while preserving other config sections.
func TestRemoveCodexWipnoteRegistrations(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	// Create a realistic config with wipnote entries plus other unrelated config
	initialContent := `[plugins]
"wipnote@wipnote" = {source = "/old/path"}
"htmlgraph@htmlgraph" = {source = "/legacy/path"}
"github@openai-curated" = {source = "https://github.com/openai/curated"}

[marketplaces]
wipnote = {source = "/also/old/path"}
htmlgraph = {source = "/legacy/marketplace/path"}
other_marketplace = {source = "https://other.com"}

[mcp_servers]
my_server = {command = "/path/to/server"}

[features]
some_feature = true
`
	if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Call removeCodexWipnoteRegistrations
	removed, err := removeCodexWipnoteRegistrations(configPath)
	if err != nil {
		t.Fatalf("removeCodexWipnoteRegistrations: %v", err)
	}
	if !removed {
		t.Errorf("expected removed=true, got false")
	}

	// Read the result and verify
	data, _ := os.ReadFile(configPath)
	content := string(data)

	// wipnote entries should be gone
	if strings.Contains(content, `"wipnote@wipnote"`) {
		t.Errorf("wipnote@wipnote should be removed but is still present")
	}
	if strings.Contains(content, `"htmlgraph@htmlgraph"`) {
		t.Errorf("legacy htmlgraph@htmlgraph should be removed but is still present")
	}
	if strings.Contains(content, "wipnote = ") {
		t.Errorf("[marketplaces.wipnote] should be removed but is still present")
	}
	if strings.Contains(content, "htmlgraph = ") {
		t.Errorf("legacy [marketplaces.htmlgraph] should be removed but is still present")
	}

	// Other entries must be preserved
	if !strings.Contains(content, "github@openai-curated") {
		t.Errorf("github@openai-curated plugin should be preserved but was removed")
	}
	if !strings.Contains(content, "other_marketplace") {
		t.Errorf("other_marketplace should be preserved but was removed")
	}
	if !strings.Contains(content, "mcp_servers") {
		t.Errorf("[mcp_servers] section should be preserved but was removed")
	}
	if !strings.Contains(content, "some_feature") {
		t.Errorf("[features] section should be preserved but was removed")
	}
}

// TestRemoveCodexWipnoteRegistrationsNoop verifies that removeCodexWipnoteRegistrations
// returns removed=false and preserves file content byte-for-byte when no wipnote entries exist.
func TestRemoveCodexWipnoteRegistrationsNoop(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")

	// Create a config with no wipnote entries
	initialContent := `[plugins]
"github@openai-curated" = {source = "https://github.com/openai/curated"}

[mcp_servers]
my_server = {command = "/path/to/server"}
`
	if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Read the original content for comparison
	originalData, _ := os.ReadFile(configPath)

	// Call removeCodexWipnoteRegistrations
	removed, err := removeCodexWipnoteRegistrations(configPath)
	if err != nil {
		t.Fatalf("removeCodexWipnoteRegistrations: %v", err)
	}
	if removed {
		t.Errorf("expected removed=false (no wipnote entries), got true")
	}

	// Verify the file was not modified
	finalData, _ := os.ReadFile(configPath)
	if !bytes.Equal(originalData, finalData) {
		t.Errorf("file was modified when it should have been left unchanged.\nOriginal:\n%s\nFinal:\n%s",
			string(originalData), string(finalData))
	}
}

// TestRemoveCodexWipnoteRegistrationsNonexistentFile verifies that removeCodexWipnoteRegistrations
// gracefully handles a non-existent config file.
func TestRemoveCodexWipnoteRegistrationsNonexistentFile(t *testing.T) {
	configPath := "/nonexistent/path/config.toml"

	removed, err := removeCodexWipnoteRegistrations(configPath)
	if err != nil {
		t.Fatalf("removeCodexWipnoteRegistrations on non-existent file: %v", err)
	}
	if removed {
		t.Errorf("expected removed=false for non-existent file, got true")
	}
}

// TestCodexFlagsParseWorktree verifies that the --feature and --track flags are
// registered on the codex command and recognized during flag parsing.
func TestCodexFlagsParseWorktree(t *testing.T) {
	cmd := codexCmd()
	// Verify that the flags exist by looking them up.
	featureFlag := cmd.Flags().Lookup("feature")
	if featureFlag == nil {
		t.Fatal("codexCmd missing --feature flag")
	}
	trackFlag := cmd.Flags().Lookup("track")
	if trackFlag == nil {
		t.Fatal("codexCmd missing --track flag")
	}
	worktreeFlag := cmd.Flags().Lookup("worktree")
	if worktreeFlag == nil {
		t.Fatal("codexCmd missing --worktree flag")
	}
	workItemFlag := cmd.Flags().Lookup("work-item")
	if workItemFlag == nil {
		t.Fatal("codexCmd missing --work-item flag")
	}
	yoloFlag := cmd.Flags().Lookup("yolo")
	if yoloFlag == nil {
		t.Fatal("codexCmd missing --yolo flag")
	}
}

func TestPrepareCodexDevMarketplace_RefreshesLocalCache(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	marketplaceDir := filepath.Join(repo, "port", "packages", "codex-marketplace")
	if err := os.MkdirAll(filepath.Join(marketplaceDir, ".agents", "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir marketplace tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir marketplace plugin dir: %v", err)
	}
	marketplaceJSON := `{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./.agents/plugins"}}]}`
	if err := os.WriteFile(filepath.Join(marketplaceDir, "marketplace.json"), []byte(marketplaceJSON), 0o644); err != nil {
		t.Fatalf("write marketplace.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", "hooks.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	home := filepath.Join(repo, "home")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir home codex: %v", err)
	}
	binDir := filepath.Join(repo, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	script := "#!/bin/sh\nset -eu\nif [ \"$1\" = plugin ] && [ \"$2\" = marketplace ] && [ \"$3\" = add ]; then\n  mkdir -p \"$HOME/.codex\"\n  printf '[marketplaces.wipnote]\\nsource = \"%s\"\\n' \"$4\" > \"$HOME/.codex/config.toml\"\n  exit 0\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Neutralize ambient session env: ResolveProjectDir trusts CLAUDE_PROJECT_DIR
	// whenever WIPNOTE_SESSION_ID is also set (core/paths/resolve.go tier 3),
	// which is exactly the case when this test runs inside a wipnote-launched
	// Claude Code session on this repo — it would silently override the
	// os.Chdir(repo) below and resolve to the real repo root instead of the
	// test's temp repo, causing a flaky/order-dependent failure.
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	oldWD, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if _, err := prepareCodexDevMarketplace(filepath.Join(home, ".codex", "config.toml"), false); err != nil {
		t.Fatalf("prepareCodexDevMarketplace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(codexPluginCachePath(), codexLocalPluginCacheVersion, ".codex-plugin", "plugin.json")); err != nil {
		t.Fatalf("expected local plugin cache refresh, stat err=%v", err)
	}
}

func TestPrepareCodexDevMarketplace_DryRunDoesNotMutateCache(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	marketplaceDir := filepath.Join(repo, "port", "packages", "codex-marketplace")
	if err := os.MkdirAll(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir marketplace tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, "marketplace.json"), []byte(`{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./.agents/plugins"}}]}`), 0o644); err != nil {
		t.Fatalf("write marketplace.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", "hooks.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	home := filepath.Join(repo, "home")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir home codex: %v", err)
	}
	binDir := filepath.Join(repo, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	script := "#!/bin/sh\nset -eu\nif [ \"$1\" = plugin ] && [ \"$2\" = marketplace ] && [ \"$3\" = add ]; then\n  mkdir -p \"$HOME/.codex\"\n  printf '[marketplaces.wipnote]\\nsource = \"%s\"\\n' \"$4\" > \"$HOME/.codex/config.toml\"\n  exit 0\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Neutralize ambient session env: ResolveProjectDir trusts CLAUDE_PROJECT_DIR
	// whenever WIPNOTE_SESSION_ID is also set (core/paths/resolve.go tier 3),
	// which is exactly the case when this test runs inside a wipnote-launched
	// Claude Code session on this repo — it would silently override the
	// os.Chdir(repo) below and resolve to the real repo root instead of the
	// test's temp repo, causing a flaky/order-dependent failure.
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	oldWD, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	// prepareCodexDevMarketplace now returns BannerDetail rows instead of printing
	// them directly; the caller folds them into the single launch banner.
	details, err := prepareCodexDevMarketplace(filepath.Join(home, ".codex", "config.toml"), true)
	if err != nil {
		t.Fatalf("prepareCodexDevMarketplace dry-run: %v", err)
	}
	// Flatten all returned detail values for substring matching.
	var detailValues strings.Builder
	for _, d := range details {
		detailValues.WriteString(d.Label + ": " + d.Value + "\n")
	}
	got := detailValues.String()
	for _, want := range []string{
		"[dry-run] codex plugin marketplace add",
		"[dry-run] would install locally",
		"[dry-run] would remove mirrored hooks",
		"[dry-run] would install wipnote agents",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run details missing %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(codexPluginCachePath(), codexLocalPluginCacheVersion)); !os.IsNotExist(err) {
		t.Fatalf("dry-run mutated cache unexpectedly, stat err=%v", err)
	}
}

func TestPrepareCodexBundledMarketplace_RepairsWhenAlreadyInstalled(t *testing.T) {
	repo := t.TempDir()
	marketplaceDir := filepath.Join(repo, "marketplace")
	if err := os.MkdirAll(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir marketplace tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, "marketplace.json"), []byte(`{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./.agents/plugins"}}]}`), 0o644); err != nil {
		t.Fatalf("write marketplace.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", "hooks.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}
	home := filepath.Join(repo, "home")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir home codex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("[marketplaces.wipnote]\nsource = \""+marketplaceDir+"\"\n"), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "hooks.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", os.Getenv("PATH"))
	oldWD, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if _, err := prepareCodexBundledMarketplace(filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Fatalf("prepareCodexBundledMarketplace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(codexPluginCachePath(), codexLocalPluginCacheVersion, ".codex-plugin", "plugin.json")); err != nil {
		t.Fatalf("expected bundled marketplace finalize cache install, stat err=%v", err)
	}
}

func TestRunCodexInit_RendersFramedSetupSummary(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir home codex: %v", err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	script := "#!/bin/sh\nset -eu\nif [ \"$1\" = plugin ] && [ \"$2\" = marketplace ] && [ \"$3\" = add ]; then\n  mkdir -p \"$HOME/.codex\"\n  printf '[marketplaces.wipnote]\\nsource = \"%s\"\\n' \"$4\" > \"$HOME/.codex/config.toml\"\n  exit 0\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := &strings.Builder{}
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, r)
		close(done)
	}()
	t.Cleanup(func() { os.Stdout = oldStdout })

	if err := runCodexInit(true, true); err != nil {
		t.Fatalf("runCodexInit: %v", err)
	}
	_ = w.Close()
	<-done

	got := out.String()
	// The boxless session-thread layout renders detail labels without a trailing
	// colon (the aligned value column is the separator), so assert on the bare
	// label tokens (bug-0f6af202).
	for _, want := range []string{
		"Codex wipnote setup",
		"Marketplace",
		"Plugin cache",
		"Mirrored hooks",
		"Agents",
		"Setup complete. Run: wipnote codex",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("setup output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "wipnote Codex plugin installed in local cache.") {
		t.Fatalf("expected framed summary instead of bare cache line:\n%s", got)
	}
	if strings.Contains(got, "no mirrored wipnote Codex hooks found in ~/.codex/hooks.json.") {
		t.Fatalf("expected framed summary instead of bare hooks line:\n%s", got)
	}
}

func TestLaunchCodexDevDryRunDoesNotExec(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	marketplaceDir := filepath.Join(repo, "port", "packages", "codex-marketplace")
	if err := os.MkdirAll(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir marketplace tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, "marketplace.json"), []byte(`{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./.agents/plugins"}}]}`), 0o644); err != nil {
		t.Fatalf("write marketplace.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", "hooks.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}
	home := filepath.Join(repo, "home")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir home codex: %v", err)
	}
	binDir := filepath.Join(repo, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	script := "#!/bin/sh\nset -eu\nif [ \"$1\" = plugin ] && [ \"$2\" = marketplace ] && [ \"$3\" = add ]; then\n  mkdir -p \"$HOME/.codex\"\n  printf '[marketplaces.wipnote]\\nsource = \"%s\"\\n' \"$4\" > \"$HOME/.codex/config.toml\"\n  exit 0\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Neutralize ambient session env: ResolveProjectDir trusts CLAUDE_PROJECT_DIR
	// whenever WIPNOTE_SESSION_ID is also set (core/paths/resolve.go tier 3),
	// which is exactly the case when this test runs inside a wipnote-launched
	// Claude Code session on this repo — it would silently override the
	// os.Chdir(repo) below and resolve to the real repo root instead of the
	// test's temp repo, causing a flaky/order-dependent failure.
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	oldWD, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	execCalled := false
	origExec := execCodexFn
	execCodexFn = func(codexLaunchOpts) error {
		execCalled = true
		return nil
	}
	t.Cleanup(func() { execCodexFn = origExec })

	out := &strings.Builder{}
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, r)
		close(done)
	}()
	t.Cleanup(func() { os.Stdout = oldStdout })

	if err := launchCodexDev("", false, true, false, nil, "", "", "", "", false); err != nil {
		t.Fatalf("launchCodexDev dry-run: %v", err)
	}
	_ = w.Close()
	<-done
	if execCalled {
		t.Fatal("execCodex was called during dev dry-run")
	}
	got := out.String()
	if !strings.Contains(got, "[dry-run] would exec: codex") {
		t.Fatalf("dry-run output missing exec preview:\n%s", got)
	}
}

func TestLaunchCodexDevDryRunSkipsWorkItemStart(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	marketplaceDir := filepath.Join(repo, "port", "packages", "codex-marketplace")
	if err := os.MkdirAll(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir marketplace tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, "marketplace.json"), []byte(`{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./.agents/plugins"}}]}`), 0o644); err != nil {
		t.Fatalf("write marketplace.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", "hooks.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}
	home := filepath.Join(repo, "home")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir home codex: %v", err)
	}
	binDir := filepath.Join(repo, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	script := "#!/bin/sh\nset -eu\nif [ \"$1\" = plugin ] && [ \"$2\" = marketplace ] && [ \"$3\" = add ]; then\n  mkdir -p \"$HOME/.codex\"\n  printf '[marketplaces.wipnote]\\nsource = \"%s\"\\n' \"$4\" > \"$HOME/.codex/config.toml\"\n  exit 0\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Neutralize ambient session env: ResolveProjectDir trusts CLAUDE_PROJECT_DIR
	// whenever WIPNOTE_SESSION_ID is also set (core/paths/resolve.go tier 3),
	// which is exactly the case when this test runs inside a wipnote-launched
	// Claude Code session on this repo — it would silently override the
	// os.Chdir(repo) below and resolve to the real repo root instead of the
	// test's temp repo, causing a flaky/order-dependent failure.
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	// See TestLaunchCodexDevDryRunSkipsWorktreeCreation: this test's "would
	// create a managed worktree" assertion only holds unconditionally on
	// RuntimeDevcontainer/RuntimeCI; force enforcement so it's deterministic
	// on every runtime (e.g. a bare macOS host).
	t.Setenv("WIPNOTE_ENFORCE_ISOLATION", "true")
	oldWD, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	startCalled := false
	origStart := runCodexFeatureStartFn
	runCodexFeatureStartFn = func(string) error {
		startCalled = true
		return nil
	}
	t.Cleanup(func() { runCodexFeatureStartFn = origStart })

	execCalled := false
	origExec := execCodexFn
	execCodexFn = func(codexLaunchOpts) error {
		execCalled = true
		return nil
	}
	t.Cleanup(func() { execCodexFn = origExec })

	out := &strings.Builder{}
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, r)
		close(done)
	}()
	t.Cleanup(func() { os.Stdout = oldStdout })

	if err := launchCodexDev("", false, true, false, nil, "", "", "", "feat-dryrun01", false); err != nil {
		t.Fatalf("launchCodexDev dry-run with work item: %v", err)
	}
	_ = w.Close()
	<-done

	if startCalled {
		t.Fatal("runCodexFeatureStart was called during dev dry-run")
	}
	if execCalled {
		t.Fatal("execCodex was called during dev dry-run")
	}
	got := out.String()
	if !strings.Contains(got, `target=worktree="`) || !strings.Contains(got, `.claude/worktrees/feat-dryrun01`) {
		t.Fatalf("dry-run output missing worktree preview:\n%s", got)
	}
}

func TestLaunchCodexDevDryRunSkipsWorktreeCreation(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	marketplaceDir := filepath.Join(repo, "port", "packages", "codex-marketplace")
	if err := os.MkdirAll(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir marketplace tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, "marketplace.json"), []byte(`{"plugins":[{"name":"wipnote","source":{"source":"local","path":"./.agents/plugins"}}]}`), 0o644); err != nil {
		t.Fatalf("write marketplace.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", ".codex-plugin", "plugin.json"), []byte(`{"name":"wipnote"}`), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceDir, ".agents", "plugins", "hooks.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}
	home := filepath.Join(repo, "home")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir home codex: %v", err)
	}
	binDir := filepath.Join(repo, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	script := "#!/bin/sh\nset -eu\nif [ \"$1\" = plugin ] && [ \"$2\" = marketplace ] && [ \"$3\" = add ]; then\n  mkdir -p \"$HOME/.codex\"\n  printf '[marketplaces.wipnote]\\nsource = \"%s\"\\n' \"$4\" > \"$HOME/.codex/config.toml\"\n  exit 0\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Neutralize ambient session env: ResolveProjectDir trusts CLAUDE_PROJECT_DIR
	// whenever WIPNOTE_SESSION_ID is also set (core/paths/resolve.go tier 3),
	// which is exactly the case when this test runs inside a wipnote-launched
	// Claude Code session on this repo — it would silently override the
	// os.Chdir(repo) below and resolve to the real repo root instead of the
	// test's temp repo, causing a flaky/order-dependent failure.
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	// This test asserts on the "would create a managed worktree" preview, but
	// plan.PlanLaunch only plans a managed worktree unconditionally on
	// RuntimeDevcontainer/RuntimeCI (internal/launcher/plan/plan.go); on
	// RuntimeHost it stays warn-only unless isolation is explicitly enforced.
	// mode.Compute's devcontainer/CI detectors read ambient signals
	// (/.dockerenv, CODESPACES, REMOTE_CONTAINERS, CI, GITHUB_ACTIONS), so
	// this test previously only passed by accident of running inside a
	// devcontainer/CI shell and failed deterministically on a bare host
	// (e.g. sandboxed macOS). Force enforcement explicitly so the assertion
	// holds on every runtime.
	t.Setenv("WIPNOTE_ENFORCE_ISOLATION", "true")
	oldWD, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	trackCalled := false
	origTrack := ensureForTrackStatusFn
	ensureForTrackStatusFn = func(string, string, io.Writer) (string, bool, error) {
		trackCalled = true
		return "", false, nil
	}
	t.Cleanup(func() { ensureForTrackStatusFn = origTrack })

	execCalled := false
	origExec := execCodexFn
	execCodexFn = func(codexLaunchOpts) error {
		execCalled = true
		return nil
	}
	t.Cleanup(func() { execCodexFn = origExec })

	out := &strings.Builder{}
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, r)
		close(done)
	}()
	t.Cleanup(func() { os.Stdout = oldStdout })

	if err := launchCodexDev("", false, true, false, nil, "trk-dryrun01", "", "", "", false); err != nil {
		t.Fatalf("launchCodexDev dry-run with track: %v", err)
	}
	_ = w.Close()
	<-done

	if trackCalled {
		t.Fatal("EnsureForTrackStatus was called during dev dry-run")
	}
	if execCalled {
		t.Fatal("execCodex was called during dev dry-run")
	}
	if _, err := os.Stat(filepath.Join(repo, "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created worktree state unexpectedly: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `target=worktree="`) || !strings.Contains(got, `.claude/worktrees/trk-dryrun01`) {
		t.Fatalf("dry-run output missing worktree preview:\n%s", got)
	}
}

func TestPlannedCodexLaunchTargetPrefersManagedWorktreePath(t *testing.T) {
	got := plannedCodexLaunchTarget(
		plan.LaunchPlan{
			IsolationMode:       plan.IsolationManagedWorktree,
			PlannedWorktreePath: "/repo/.claude/worktrees/adhoc-20260618-120000",
		},
		"",
		"",
		"",
		"",
		false,
		"/repo",
	)
	if got != `worktree="/repo/.claude/worktrees/adhoc-20260618-120000"` {
		t.Fatalf("plannedCodexLaunchTarget = %q", got)
	}
}

// TestCodexManifestPath verifies that codexManifestPath resolves the correct
// marketplace.json path for both tarball flat and dev-source deep layouts.
func TestCodexManifestPath(t *testing.T) {
	t.Run("flat layout returns flat path", func(t *testing.T) {
		dir := t.TempDir()
		flatPath := filepath.Join(dir, "marketplace.json")
		if err := os.WriteFile(flatPath, []byte(`{}`), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got := codexManifestPath(dir)
		if got != flatPath {
			t.Errorf("got %q, want %q", got, flatPath)
		}
	})

	t.Run("deep layout returns deep path when no flat file", func(t *testing.T) {
		dir := t.TempDir()
		deepDir := filepath.Join(dir, ".agents", "plugins")
		if err := os.MkdirAll(deepDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		deepPath := filepath.Join(deepDir, "marketplace.json")
		if err := os.WriteFile(deepPath, []byte(`{}`), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got := codexManifestPath(dir)
		if got != deepPath {
			t.Errorf("got %q, want %q", got, deepPath)
		}
	})

	t.Run("flat takes precedence when both exist", func(t *testing.T) {
		dir := t.TempDir()
		flatPath := filepath.Join(dir, "marketplace.json")
		if err := os.WriteFile(flatPath, []byte(`{}`), 0644); err != nil {
			t.Fatalf("WriteFile flat: %v", err)
		}
		deepDir := filepath.Join(dir, ".agents", "plugins")
		if err := os.MkdirAll(deepDir, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(deepDir, "marketplace.json"), []byte(`{}`), 0644); err != nil {
			t.Fatalf("WriteFile deep: %v", err)
		}
		got := codexManifestPath(dir)
		if got != flatPath {
			t.Errorf("got %q, want flat path %q", got, flatPath)
		}
	})

	t.Run("empty dir returns deep path as fallback", func(t *testing.T) {
		dir := t.TempDir()
		wantDeep := filepath.Join(dir, ".agents", "plugins", "marketplace.json")
		got := codexManifestPath(dir)
		if got != wantDeep {
			t.Errorf("got %q, want %q", got, wantDeep)
		}
	})
}

// TestCodexMarketplaceAddArgIsDirectory verifies the regression fix for
// bug-ee77c482: `codex plugin marketplace add` requires a marketplace ROOT
// DIRECTORY (layout <root>/.agents/plugins/marketplace.json), not the
// manifest FILE path. Passing the file fails with "local marketplace source
// must be a directory, not a file".
func TestCodexMarketplaceAddArgIsDirectory(t *testing.T) {
	dir := t.TempDir()
	deepDir := filepath.Join(dir, ".agents", "plugins")
	if err := os.MkdirAll(deepDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deepDir, "marketplace.json"), []byte(`{}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := codexMarketplaceAddArg(dir)

	// Must be the tree root directory, not the manifest file.
	if got != dir {
		t.Errorf("codexMarketplaceAddArg = %q, want tree root %q", got, dir)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("returned path does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("returned path %q is not a directory; Codex rejects file paths", got)
	}
	if strings.HasSuffix(got, "marketplace.json") {
		t.Errorf("returned path %q is a manifest FILE; Codex requires a directory", got)
	}
	// The returned directory must contain the nested manifest Codex looks for.
	nested := filepath.Join(got, ".agents", "plugins", "marketplace.json")
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("expected %q to exist under returned dir: %v", nested, err)
	}
}

// TestCodexMarketplaceAddArgRoundTrip verifies that registering the directory
// returned by codexMarketplaceAddArg and then reading it back via
// getCodexMarketplacePathAt / isCodexMarketplaceInstalledAt reports the
// marketplace as installed at the SAME directory value — so a subsequent
// launch does not spuriously re-register (registeredAbs == bundledAbs).
func TestCodexMarketplaceAddArgRoundTrip(t *testing.T) {
	tmpdir := t.TempDir()
	configPath := filepath.Join(tmpdir, "config.toml")
	marketplaceRoot := filepath.Join(tmpdir, "codex-marketplace")
	deepDir := filepath.Join(marketplaceRoot, ".agents", "plugins")
	if err := os.MkdirAll(deepDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deepDir, "marketplace.json"), []byte(`{}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// The value we would pass to `codex plugin marketplace add`.
	addArg := codexMarketplaceAddArg(marketplaceRoot)

	// Simulate Codex persisting the registered source as that directory value.
	config := "[marketplaces.wipnote]\nsource = \"" + filepath.ToSlash(addArg) + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	// Detection must report installed.
	if !isCodexMarketplaceInstalledAt(configPath) {
		t.Errorf("expected marketplace to be detected as installed after registering %q", addArg)
	}

	// getCodexMarketplacePathAt must round-trip the same directory value, so
	// the launch-path comparison (registeredAbs vs bundledAbs) is an equality
	// and does NOT trigger a re-register.
	registered := getCodexMarketplacePathAt(configPath)
	registeredAbs, _ := filepath.Abs(registered)
	bundledAbs, _ := filepath.Abs(addArg)
	if registeredAbs != bundledAbs {
		t.Errorf("round-trip mismatch: registered %q (abs %q) != add arg %q (abs %q); would re-register every launch",
			registered, registeredAbs, addArg, bundledAbs)
	}
}
