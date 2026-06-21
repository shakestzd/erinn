package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// isPluginInstalled checks whether the wipnote plugin is in Claude Code's
// installed_plugins.json.
func isPluginInstalled() bool {
	return isPluginInstalledAt(installedPluginsJSONPath())
}

// isPluginInstalledAt is the testable core — reads the given file.
func isPluginInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var outer struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(data, &outer) != nil {
		return false
	}
	raw, ok := outer.Plugins["wipnote@wipnote"]
	if !ok {
		return false
	}
	// Verify the entry is a non-empty array.
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || len(entries) == 0 {
		return false
	}
	return true
}

// marketplacePluginRegistryScopes lists all registry keys that
// removeMarketplaceWipnote handles. Detection scope must match removal scope
// so marketplaceWipnotePresent() doesn't miss registry-only installs that
// lack artifact directories (e.g. wipnote@local-marketplace or legacy
// htmlgraph scopes).
var marketplacePluginRegistryScopes = []string{
	"wipnote@wipnote",
	"wipnote@local-marketplace",
	"htmlgraph@htmlgraph",
	"htmlgraph@local-marketplace",
}

// isAnyMarketplacePluginInstalledAt returns true when installed_plugins.json
// at path contains a non-empty entry for ANY of the registry scopes that
// removeMarketplaceWipnote handles. Testable core used by
// marketplaceWipnotePresent.
func isAnyMarketplacePluginInstalledAt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var outer struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(data, &outer) != nil {
		return false
	}
	for _, scope := range marketplacePluginRegistryScopes {
		raw, ok := outer.Plugins[scope]
		if !ok {
			continue
		}
		var entries []json.RawMessage
		if json.Unmarshal(raw, &entries) == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

// installedPluginVersionAt is the testable core.
func installedPluginVersionAt(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var outer struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(data, &outer) != nil {
		return ""
	}
	raw, ok := outer.Plugins["wipnote@wipnote"]
	if !ok {
		return ""
	}
	var entries []struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &entries) != nil || len(entries) == 0 {
		return ""
	}
	return entries[0].Version
}

// versionNotice returns a user-facing notice if the installed plugin version
// differs from the binary version. Returns empty string if versions match or
// if the binary is a dev build.
func versionNotice(binaryVersion, latestVersion string) string {
	if binaryVersion == "dev" || binaryVersion == "" {
		return ""
	}
	if latestVersion == "" || binaryVersion == latestVersion {
		return ""
	}
	return fmt.Sprintf("Update available: v%s → v%s. Run: wipnote plugin install", binaryVersion, latestVersion)
}

// fetchLatestVersion queries the GitHub API for the latest release version.
// Returns empty string on any error (network, parse, timeout).
func fetchLatestVersion() string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/shakestzd/wipnote/releases/latest")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(resp.Body).Decode(&release) != nil {
		return ""
	}
	// Strip "v" prefix: "v0.39.0" → "0.39.0"
	return strings.TrimPrefix(release.TagName, "v")
}

// interactivePluginInstall prompts the user to choose a plugin install method.
// Only called when no plugin is detected (first launch).
func interactivePluginInstall() {
	fmt.Println()
	fmt.Println("wipnote plugin is not installed for Claude Code.")
	fmt.Println("The plugin adds hooks, agents, skills, and slash commands.")
	fmt.Println()
	fmt.Println("  1. Install from marketplace (recommended)")
	fmt.Println("  2. Continue without plugin (CLI-only)")
	fmt.Println()
	fmt.Print("Select [1/2]: ")

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	choice := strings.TrimSpace(line)

	switch choice {
	case "1", "":
		fmt.Println()
		if err := ensureWipnotePlugin(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: plugin installation failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "  Run manually: claude plugin marketplace add shakestzd/wipnote && claude plugin install wipnote@wipnote\n")
		} else {
			fmt.Println("Plugin installed successfully.")
		}
	case "2":
		fmt.Println("Continuing without plugin. Run 'wipnote plugin install' later to add it.")
	default:
		fmt.Println("Invalid choice. Continuing without plugin.")
	}
	fmt.Println()
}

// ensurePluginOnLaunch is called by all non-dev launchers. It handles:
// 1. First launch: interactive plugin install prompt
// 2. Subsequent launches: version update check
func ensurePluginOnLaunch() {
	if !isPluginInstalled() {
		interactivePluginInstall()
		// Post-install verification: confirm the install actually took effect.
		if !isPluginInstalled() {
			fmt.Fprintln(os.Stderr, "warning: plugin installation did not complete — launching without plugin")
			fmt.Fprintln(os.Stderr, "  Run manually: claude plugin marketplace add shakestzd/wipnote && claude plugin install wipnote@wipnote")
		}
		return
	}

	// Plugin is installed — check for version updates.
	latest := fetchLatestVersion()
	if notice := versionNotice(version, latest); notice != "" {
		fmt.Printf("  %s\n", notice)
	}
}
