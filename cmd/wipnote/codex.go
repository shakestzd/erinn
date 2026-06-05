package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/shakestzd/wipnote/internal/storage"
	"github.com/spf13/cobra"
)

// codexMarketplaceRepo and codexMarketplaceSparse are retained for backward
// compatibility with tests and any external code; the production launchers
// no longer use them as of Phase B (bundled-tree migration) — the Codex
// marketplace is now resolved via resolveSharedTreePath("codex-marketplace").
const codexMarketplaceRepo = "shakestzd/wipnote"
const codexMarketplaceSparse = "packages/codex-marketplace"

// codexConfigPath returns the path to ~/.codex/config.toml.
func codexConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml")
}

// codexHooksPath returns the path Codex currently reads for user-level hooks.
func codexHooksPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "hooks.json")
}

// codexAgentsPath returns the documented user-level custom agent directory.
func codexAgentsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "agents")
}

func codexProjectAgentsPath(projectRoot string) string {
	if projectRoot == "" {
		return ""
	}
	return filepath.Join(projectRoot, ".codex", "agents")
}

// codexMarketplaceSection is the TOML key that indicates our marketplace is registered.
const codexMarketplaceSection = `[marketplaces.wipnote]`
const codexPluginID = "wipnote@wipnote"
const codexLocalPluginCacheVersion = "local"

// codexPluginCachePath returns Codex's cache location for the wipnote plugin.
func codexPluginCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "plugins", "cache", "wipnote", "wipnote")
}

// isCodexMarketplaceInstalledAt is the testable core that reads the given path.
func isCodexMarketplaceInstalledAt(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, "[marketplaces.wipnote]") ||
		strings.Contains(content, `[plugins."wipnote@wipnote"]`)
}

// isCodexHooksEnabledAt reports whether Codex hooks are enabled in config.toml.
// Returns true ONLY when the canonical [features].hooks key is enabled.
// The legacy codex_hooks key is recognized by ensureCodexHooksEnabled during
// migration, but does not suppress the migration check here.
func isCodexHooksEnabledAt(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}

	tree := make(map[string]any)
	if err := toml.Unmarshal(data, &tree); err != nil {
		return false
	}

	features, ok := tree["features"].(map[string]any)
	if !ok {
		return false
	}
	// Only return true for the canonical hooks key, not the legacy codex_hooks.
	// This ensures configs with only codex_hooks=true are treated as "not enabled"
	// and will trigger migration via ensureCodexHooksEnabled.
	if enabled, ok := features["hooks"].(bool); ok && enabled {
		return true
	}
	return false
}

// isCodexPluginEnabledAt returns true when the wipnote plugin itself is enabled.
// Marketplace registration only makes the plugin available; Codex loads skills
// and commands from enabled plugins.
func isCodexPluginEnabledAt(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}

	tree := make(map[string]any)
	if err := toml.Unmarshal(data, &tree); err != nil {
		return false
	}

	if plugins, ok := tree["plugins"].(map[string]any); ok {
		if plugin, ok := plugins[codexPluginID].(map[string]any); ok {
			if enabled, ok := plugin["enabled"].(bool); ok {
				return enabled
			}
		}
	}
	return false
}

// isCodexPluginInstalledAt returns true when Codex has a loadable plugin tree
// in its plugin cache. Codex expects one install-version/hash directory below
// ~/.codex/plugins/cache/<marketplace>/<plugin>.
func isCodexPluginInstalledAt(cachePath string) bool {
	return codexInstalledPluginDirAt(cachePath) != ""
}

func codexInstalledPluginDirAt(cachePath string) string {
	entries, err := os.ReadDir(cachePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if _, err := os.Stat(filepath.Join(cachePath, entry.Name(), ".codex-plugin", "plugin.json")); err == nil {
			return filepath.Join(cachePath, entry.Name())
		}
	}
	return ""
}

// codexManifestPath resolves the path to marketplace.json inside a
// codex-marketplace tree, transparently handling both the flat layout
// produced by the legacy GoReleaser archive (marketplace.json at the tree
// root) and the deep layout used by the dev source and `wipnote build`
// install (.agents/plugins/marketplace.json).
//
// This is used to LOCATE and parse the manifest (e.g. to derive the local
// plugin directory). It must NOT be passed to `codex plugin marketplace add`,
// which requires a marketplace ROOT DIRECTORY — see codexMarketplaceAddArg.
func codexManifestPath(treeRoot string) string {
	flat := filepath.Join(treeRoot, "marketplace.json")
	if _, err := os.Stat(flat); err == nil {
		return flat
	}
	return filepath.Join(treeRoot, ".agents", "plugins", "marketplace.json")
}

// codexMarketplaceAddArg resolves the directory to pass to
// `codex plugin marketplace add`. Codex CLI requires a local marketplace
// ROOT DIRECTORY whose layout is `<root>/.agents/plugins/marketplace.json`;
// passing the marketplace.json file directly fails with
// "local marketplace source must be a directory, not a file".
//
// The dev source, `wipnote build` install, and (after the v0.60.10 archive
// fix) the release tarball all keep the manifest nested under
// `<root>/.agents/plugins/marketplace.json`, so the bundled tree root IS the
// directory to register. For backward compatibility with an older flattened
// tarball that placed marketplace.json directly at the tree root, we fall
// back to the tree root itself (which is still a directory, satisfying
// Codex's directory requirement).
func codexMarketplaceAddArg(treeRoot string) string {
	nested := filepath.Join(treeRoot, ".agents", "plugins", "marketplace.json")
	if _, err := os.Stat(nested); err == nil {
		return treeRoot
	}
	// Legacy flat layout: marketplace.json at the tree root. The directory is
	// still treeRoot; Codex accepts the directory and resolves the manifest.
	return treeRoot
}

// getCodexMarketplacePathAt parses config.toml and returns the registered wipnote
// marketplace path, or empty string if not found.
func getCodexMarketplacePathAt(configPath string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	tree := make(map[string]any)
	if err := toml.Unmarshal(data, &tree); err != nil {
		return ""
	}

	// Check [marketplaces.wipnote]
	if mkts, ok := tree["marketplaces"].(map[string]any); ok {
		if hg, ok := mkts["wipnote"].(map[string]any); ok {
			if source, ok := hg["source"].(string); ok {
				return source
			}
			if path, ok := hg["path"].(string); ok {
				return path
			}
		}
	}

	// Check [plugins."wipnote@wipnote"]
	if plugins, ok := tree["plugins"].(map[string]any); ok {
		if hg, ok := plugins["wipnote@wipnote"].(map[string]any); ok {
			if source, ok := hg["source"].(string); ok {
				return source
			}
			if path, ok := hg["path"].(string); ok {
				return path
			}
		}
	}

	return ""
}

// removeCodexWipnoteRegistrations removes any wipnote marketplace or plugin
// registrations from the given config.toml file. It is idempotent — if the file
// does not exist or contains no wipnote entries, it is a no-op.
// Returns (removed bool, error). removed=true indicates at least one entry was deleted.
func removeCodexWipnoteRegistrations(configPath string) (bool, error) {
	// Read existing config, if any
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // file doesn't exist; no-op
		}
		return false, fmt.Errorf("reading %s: %w", configPath, err)
	}

	// Parse the TOML tree
	tree := make(map[string]any)
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &tree); err != nil {
			return false, fmt.Errorf("parsing %s: %w", configPath, err)
		}
	}

	removed := false

	// Remove from [plugins]. The htmlgraph key is a legacy registration that
	// must be cleaned up so it cannot shadow the renamed wipnote plugin.
	if plugins, ok := tree["plugins"].(map[string]any); ok {
		for _, key := range []string{"wipnote@wipnote", "htmlgraph@htmlgraph"} {
			if _, exists := plugins[key]; exists {
				delete(plugins, key)
				removed = true
			}
		}
		// If [plugins] is now empty, remove the whole section
		if len(plugins) == 0 {
			delete(tree, "plugins")
		}
	}

	// Remove from [marketplaces]. Keep removing the legacy htmlgraph entry for
	// users who installed the plugin before the rename.
	if mkts, ok := tree["marketplaces"].(map[string]any); ok {
		for _, key := range []string{"wipnote", "htmlgraph"} {
			if _, exists := mkts[key]; exists {
				delete(mkts, key)
				removed = true
			}
		}
		// If [marketplaces] is now empty, remove the whole section
		if len(mkts) == 0 {
			delete(tree, "marketplaces")
		}
	}

	// If nothing was removed, no need to rewrite the file
	if !removed {
		return false, nil
	}

	// Marshal back to TOML and write
	newData, err := toml.Marshal(tree)
	if err != nil {
		return false, fmt.Errorf("marshaling TOML: %w", err)
	}

	if err := os.WriteFile(configPath, newData, 0644); err != nil {
		return false, fmt.Errorf("writing %s: %w", configPath, err)
	}

	return true, nil
}

// ensureCodexHooksEnabled parses the config.toml file, merges hooks = true into
// the [features] table (creating the section if absent), removes the deprecated
// codex_hooks key, and writes it back. This is idempotent.
func ensureCodexHooksEnabled(configPath string) error {
	// Read existing config, if any
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}

	// Parse or create the TOML tree
	tree := make(map[string]any)
	if err == nil && len(data) > 0 {
		if err := toml.Unmarshal(data, &tree); err != nil {
			return fmt.Errorf("parsing %s: %w", configPath, err)
		}
	}

	// Ensure [features] table exists and set hooks = true.
	features, ok := tree["features"].(map[string]any)
	if !ok {
		features = make(map[string]any)
		tree["features"] = features
	}
	features["hooks"] = true
	delete(features, "codex_hooks")

	// Marshal back to TOML and write
	newData, err := toml.Marshal(tree)
	if err != nil {
		return fmt.Errorf("marshaling TOML: %w", err)
	}

	if err := os.WriteFile(configPath, newData, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", configPath, err)
	}

	return nil
}

// ensureCodexPluginEnabled enables the installed marketplace plugin so Codex
// loads its skills, commands, hooks, and other plugin assets.
func ensureCodexPluginEnabled(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}

	tree := make(map[string]any)
	if err == nil && len(data) > 0 {
		if err := toml.Unmarshal(data, &tree); err != nil {
			return fmt.Errorf("parsing %s: %w", configPath, err)
		}
	}

	plugins, ok := tree["plugins"].(map[string]any)
	if !ok {
		plugins = make(map[string]any)
		tree["plugins"] = plugins
	}
	plugin, ok := plugins[codexPluginID].(map[string]any)
	if !ok {
		plugin = make(map[string]any)
		plugins[codexPluginID] = plugin
	}
	plugin["enabled"] = true

	newData, err := toml.Marshal(tree)
	if err != nil {
		return fmt.Errorf("marshaling TOML: %w", err)
	}

	if err := os.WriteFile(configPath, newData, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", configPath, err)
	}

	return nil
}

// ensureCodexLocalPluginInstalled materializes a local marketplace plugin into
// Codex's plugin cache. Codex currently loads enabled plugins from
// ~/.codex/plugins/cache/<marketplace>/<plugin>; registering a local marketplace
// alone leaves the enabled plugin with no installed tree to load.
func ensureCodexLocalPluginInstalled(configPath string, force bool) (bool, error) {
	if !force && isCodexPluginInstalledAt(codexPluginCachePath()) {
		return false, nil
	}

	marketplacePath := getCodexMarketplacePathAt(configPath)
	if marketplacePath == "" {
		return false, nil
	}

	// Support both: legacy directory-root registration (manifest under
	// .agents/plugins/marketplace.json) and new manifest-file registration.
	mktPath := marketplacePath
	if info, err := os.Stat(mktPath); err == nil && info.IsDir() {
		mktPath = codexManifestPath(marketplacePath)
	}
	pluginDir, err := codexPluginDirFromMarketplace(mktPath)
	if err != nil {
		return false, nil
	}
	if err := installCodexPluginCache(pluginDir, codexPluginCachePath()); err != nil {
		return false, err
	}
	return true, nil
}

func codexPluginDirFromMarketplace(marketplaceJSONPath string) (string, error) {
	data, err := os.ReadFile(marketplaceJSONPath)
	if err != nil {
		return "", err
	}
	var marketplace struct {
		Plugins []struct {
			Name   string `json:"name"`
			Source struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &marketplace); err != nil {
		return "", err
	}
	for _, plugin := range marketplace.Plugins {
		if plugin.Name != "wipnote" || plugin.Source.Source != "local" || plugin.Source.Path == "" {
			continue
		}
		pluginDir := filepath.Clean(filepath.Join(filepath.Dir(marketplaceJSONPath), plugin.Source.Path))
		if _, err := os.Stat(filepath.Join(pluginDir, ".codex-plugin", "plugin.json")); err == nil {
			return pluginDir, nil
		}
	}
	return "", os.ErrNotExist
}

func installCodexPluginCache(pluginDir, cachePath string) error {
	if err := os.RemoveAll(cachePath); err != nil {
		return err
	}
	if err := os.MkdirAll(cachePath, 0755); err != nil {
		return err
	}
	installPath := filepath.Join(cachePath, codexLocalPluginCacheVersion)
	return copyDir(pluginDir, installPath)
}

type codexHooksFile struct {
	Hooks map[string][]codexHookGroup `json:"hooks"`
}

type codexHookGroup struct {
	Matcher string           `json:"matcher,omitempty"`
	Hooks   []codexHookEntry `json:"hooks"`
}

type codexHookEntry struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func ensureCodexGlobalHooksInstalled(hooksPath, pluginDir string) (bool, error) {
	if pluginDir == "" {
		return false, nil
	}
	sourcePath := filepath.Join(pluginDir, "hooks.json")
	sourceData, err := os.ReadFile(sourcePath)
	if err != nil {
		return false, fmt.Errorf("reading plugin hooks %s: %w", sourcePath, err)
	}
	var source codexHooksFile
	if err := json.Unmarshal(sourceData, &source); err != nil {
		return false, fmt.Errorf("parsing plugin hooks %s: %w", sourcePath, err)
	}
	if len(source.Hooks) == 0 {
		return false, nil
	}

	var target codexHooksFile
	target.Hooks = map[string][]codexHookGroup{}
	targetData, err := os.ReadFile(hooksPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading %s: %w", hooksPath, err)
	}
	if err == nil && len(targetData) > 0 {
		if err := json.Unmarshal(targetData, &target); err != nil {
			return false, fmt.Errorf("parsing %s: %w", hooksPath, err)
		}
		if target.Hooks == nil {
			target.Hooks = map[string][]codexHookGroup{}
		}
	}

	changed := false
	for eventName, groups := range source.Hooks {
		for _, group := range groups {
			if codexHookGroupInstalled(target.Hooks[eventName], group) {
				continue
			}
			target.Hooks[eventName] = append(target.Hooks[eventName], group)
			changed = true
		}
	}
	if !changed {
		return false, nil
	}

	out, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshaling %s: %w", hooksPath, err)
	}
	out = append(out, '\n')
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0755); err != nil {
		return false, fmt.Errorf("creating %s: %w", filepath.Dir(hooksPath), err)
	}
	if err := os.WriteFile(hooksPath, out, 0644); err != nil {
		return false, fmt.Errorf("writing %s: %w", hooksPath, err)
	}
	return true, nil
}

func codexHookGroupInstalled(existing []codexHookGroup, want codexHookGroup) bool {
	for _, wantHook := range want.Hooks {
		if strings.TrimSpace(wantHook.Command) == "" {
			continue
		}
		found := false
		for _, group := range existing {
			for _, hook := range group.Hooks {
				if strings.TrimSpace(hook.Command) == strings.TrimSpace(wantHook.Command) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func ensureCodexGlobalHooksFromCache() (bool, error) {
	return ensureCodexGlobalHooksInstalled(codexHooksPath(), codexInstalledPluginDirAt(codexPluginCachePath()))
}

type codexCustomAgentHeader struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
}

func ensureCodexAgentsFromCache() (bool, error) {
	return ensureCodexCustomAgentsInstalled(codexInstalledPluginDirAt(codexPluginCachePath()), codexAgentsPath())
}

// ensureCodexCustomAgentsInstalled mirrors wipnote-*.toml agent definitions from the plugin
// cache into ~/.codex/agents/ and cleans up stale files. Files are intentionally not
// deleted on exit — users running bare codex (outside wipnote) should still see the agents.
// Stale wipnote-*.toml files from old versions are cleaned up here on the next launch.
func ensureCodexCustomAgentsInstalled(pluginDir, agentsDir string) (bool, error) {
	if pluginDir == "" || agentsDir == "" {
		return false, nil
	}
	parentDir := filepath.Dir(agentsDir)
	if info, err := os.Stat(parentDir); err == nil && !info.IsDir() {
		return false, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("checking Codex agents parent %s: %w", parentDir, err)
	}
	sourceDir := filepath.Join(pluginDir, "agents")
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading Codex agent source %s: %w", sourceDir, err)
	}

	changed := false
	sourceNames := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		sourceNames[entry.Name()] = true
		sourcePath := filepath.Join(sourceDir, entry.Name())
		targetPath := filepath.Join(agentsDir, entry.Name())
		if sameFileContent(sourcePath, targetPath) {
			continue
		}
		if err := os.MkdirAll(agentsDir, 0755); err != nil {
			return false, fmt.Errorf("creating Codex agents dir %s: %w", agentsDir, err)
		}
		if err := copyFile(sourcePath, targetPath); err != nil {
			return false, fmt.Errorf("installing Codex agent %s: %w", targetPath, err)
		}
		changed = true
	}
	targetEntries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return changed, nil
		}
		return false, fmt.Errorf("reading Codex agents target %s: %w", agentsDir, err)
	}
	for _, entry := range targetEntries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "wipnote-") || !strings.HasSuffix(name, ".toml") || sourceNames[name] {
			continue
		}
		if err := os.Remove(filepath.Join(agentsDir, name)); err != nil {
			return false, fmt.Errorf("removing stale Codex agent %s: %w", name, err)
		}
		changed = true
	}
	return changed, nil
}

func sameFileContent(a, b string) bool {
	left, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	right, err := os.ReadFile(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

func buildCodexAgentConfigArgs(agentsDir string) []string {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil
	}
	var args []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		path := filepath.Join(agentsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var header codexCustomAgentHeader
		if err := toml.Unmarshal(data, &header); err != nil || header.Name == "" {
			continue
		}
		if header.Description != "" {
			args = append(args, "-c", fmt.Sprintf("agents.%s.description=%q", header.Name, header.Description))
		}
		args = append(args, "-c", fmt.Sprintf("agents.%s.config_file=%q", header.Name, filepath.ToSlash(path)))
	}
	return args
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// promptYesNo asks the user a yes/no question and returns true if they answer y/Y/yes.
// If yes is true (--yes flag), the function returns true without prompting.
func promptYesNo(question string, yes bool) bool {
	if yes {
		return true
	}
	fmt.Print(question + " [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.TrimSpace(strings.ToLower(line))
	return answer == "y" || answer == "yes"
}

// codexCmd returns the cobra command for `wipnote codex`.
func codexCmd() *cobra.Command {
	var init_, continue_, dev, cleanup, dryRun, yes, noWorktree, inPlace, yolo bool
	var resumeID, trackID, featureID, worktreePath, workItem, baseBranch string

	cmd := &cobra.Command{
		Use:   "codex",
		Short: "Launch Codex CLI with wipnote context",
		Long: `Launch Codex CLI with wipnote observability context.

Modes:
  wipnote codex                   Launch Codex interactively with wipnote env.
  wipnote codex --init            Install the wipnote Codex marketplace (idempotent).
  wipnote codex --continue        Resume the last Codex session (codex resume --last).
  wipnote codex --resume <id>     Resume a specific Codex session by ID.
  wipnote codex --dev             Register local packages/codex-marketplace/ and launch.
  wipnote codex --feature <id>    Launch in the feature's git worktree.
  wipnote codex --track <id>      Launch in the track's git worktree.
  wipnote codex --yolo            Launch without Codex approvals/sandbox prompts.

Session IDs come from ~/.codex/session_index.jsonl.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case init_:
				return runCodexInit(yes, dryRun)
			case dev:
				return launchCodexDev(resumeID, cleanup, dryRun, yolo, args)
			case continue_:
				return launchCodexContinue(resumeID, yolo, args)
			default:
				effectiveInPlace := inPlace || noWorktree
				_ = baseBranch // reserved for slice-3+
				return launchCodexDefault(resumeID, trackID, featureID, worktreePath, workItem, effectiveInPlace, yolo, args)
			}
		},
	}

	cmd.Flags().BoolVar(&init_, "init", false, "Install the wipnote Codex marketplace plugin (idempotent)")
	cmd.Flags().BoolVar(&continue_, "continue", false, "Resume the last Codex session")
	cmd.Flags().BoolVar(&dev, "dev", false, "Register local packages/codex-marketplace/ and launch Codex")
	cmd.Flags().BoolVar(&cleanup, "cleanup", false, "With --dev: unregister the local marketplace on exit")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would happen without executing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Answer yes to all prompts (non-interactive)")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "Skip worktree creation; run in project root (alias for --in-place)")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "Intentional in-place mutation; records opt-out of isolation")
	cmd.Flags().BoolVar(&yolo, "yolo", false, "Pass Codex --dangerously-bypass-approvals-and-sandbox")
	cmd.Flags().StringVar(&resumeID, "resume", "", "Resume a specific Codex session by ID")
	cmd.Flags().StringVar(&trackID, "track", "", "Track ID to work on (e.g., trk-3719d8f3)")
	cmd.Flags().StringVar(&featureID, "feature", "", "Feature ID to work on (e.g., feat-15c458aa)")
	cmd.Flags().StringVar(&worktreePath, "worktree", "", "Explicit worktree path (overrides --track/--feature resolution)")
	cmd.Flags().StringVar(&workItem, "work-item", "", "Work item ID for attribution prefix (e.g., feat-15c458aa)")
	cmd.Flags().StringVar(&baseBranch, "base", "", "Base branch for managed worktree (advanced; default: current HEAD)")

	return cmd
}

// runCodexInit installs the wipnote Codex marketplace plugin, idempotently.
// Corresponds to: wipnote codex --init
// Phase 1: Install / verify marketplace (idempotent).
// Phase 2: Check hooks feature flag — prompt user if not set.
//
// Phase B of the marketplace-to-bundled-plugin migration: --init now points
// Codex at the bundled local marketplace tree resolved via
// resolveSharedTreePath("codex-marketplace") instead of cloning the marketplace
// from GitHub. The Codex plugin tree is shipped alongside the wipnote binary
// (in the release tarball or via brew install) and mirrored by `wipnote build`.
func runCodexInit(yes, dryRun bool) error {
	configPath := codexConfigPath()

	bundledMarketplace, bundleErr := resolveSharedTreePath("codex-marketplace")
	if bundleErr != nil {
		return fmt.Errorf("resolving bundled Codex marketplace: %w", bundleErr)
	}
	// Codex CLI requires a marketplace ROOT DIRECTORY (layout
	// <root>/.agents/plugins/marketplace.json), NOT the manifest file path —
	// passing the file fails with "local marketplace source must be a
	// directory, not a file". Register the directory and compare detection
	// against the SAME directory value to avoid re-registering every launch.
	bundledDir := codexMarketplaceAddArg(bundledMarketplace)

	// Phase 1: Install or verify marketplace. If a different marketplace is
	// already registered (e.g. from a previous GitHub-clone-based --init), we
	// rewrite the registration to point at the bundled local path.
	registeredPath := getCodexMarketplacePathAt(configPath)
	registeredAbs, _ := filepath.Abs(registeredPath)
	bundledAbs, _ := filepath.Abs(bundledDir)

	if registeredAbs != "" && registeredAbs != bundledAbs {
		fmt.Printf("Replacing existing Codex marketplace registration (%s)\n", registeredPath)
		if dryRun {
			fmt.Printf("[dry-run] would remove wipnote registrations from %s\n", configPath)
		} else if _, rmErr := removeCodexWipnoteRegistrations(configPath); rmErr != nil {
			return fmt.Errorf("removing stale Codex marketplace registration: %w", rmErr)
		}
		registeredPath = ""
		registeredAbs = ""
	}

	if registeredAbs != bundledAbs {
		addArgs := []string{"plugin", "marketplace", "add", bundledDir}
		fmt.Printf("Installing wipnote Codex marketplace (bundled)...\n")
		fmt.Printf("  path: %s\n", bundledDir)
		if dryRun {
			fmt.Printf("[dry-run] codex %s\n", strings.Join(addArgs, " "))
		} else {
			if out, err := exec.Command("codex", addArgs...).CombinedOutput(); err != nil {
				return fmt.Errorf("codex marketplace add failed: %w\n%s", err, strings.TrimSpace(string(out)))
			}
			fmt.Println("wipnote Codex marketplace installed.")
		}
	} else {
		fmt.Println("wipnote Codex marketplace is already installed (bundled).")
	}

	// Phase 2: Check and optionally enable the hooks feature flag.
	// This runs on every --init so partial setups can be repaired.
	if !isCodexHooksEnabledAt(configPath) {
		if promptYesNo("Enable the hooks feature flag in ~/.codex/config.toml?", yes) {
			if dryRun {
				fmt.Println("[dry-run] would enable hooks = true in ~/.codex/config.toml")
			} else {
				if err := ensureCodexHooksEnabled(configPath); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not enable hooks feature flag: %v\n", err)
				} else {
					fmt.Println("hooks feature flag enabled.")
				}
			}
		}
	} else {
		fmt.Println("hooks feature flag is already enabled.")
	}

	// Phase 3: enable the actual plugin. Without this, the marketplace is
	// registered but skills/commands are not loaded in Codex sessions.
	if !isCodexPluginEnabledAt(configPath) {
		if dryRun {
			fmt.Println("[dry-run] would enable plugin wipnote@wipnote in ~/.codex/config.toml")
		} else {
			if err := ensureCodexPluginEnabled(configPath); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not enable wipnote plugin: %v\n", err)
			} else {
				fmt.Println("wipnote Codex plugin enabled.")
			}
		}
	} else {
		fmt.Println("wipnote Codex plugin is already enabled.")
	}

	// Phase 4: ensure Codex has an installed plugin tree behind the enabled
	// stanza. Git marketplaces are installed via Codex's upgrade command; local
	// dev marketplaces are materialized directly into Codex's cache.
	if !isCodexPluginInstalledAt(codexPluginCachePath()) {
		if dryRun {
			fmt.Println("[dry-run] would install plugin wipnote@wipnote into Codex plugin cache")
		} else if installed, err := ensureCodexLocalPluginInstalled(configPath, false); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not install local wipnote plugin cache: %v\n", err)
		} else if installed {
			fmt.Println("wipnote Codex plugin installed in local cache.")
		} else if out, err := exec.Command("codex", "plugin", "marketplace", "upgrade", "wipnote").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not install wipnote plugin cache from marketplace: %v\n%s\n", err, strings.TrimSpace(string(out)))
		} else {
			fmt.Println("wipnote Codex plugin cache installed.")
		}
	}
	if dryRun {
		fmt.Println("[dry-run] would install wipnote hooks into ~/.codex/hooks.json")
	} else if changed, err := ensureCodexGlobalHooksFromCache(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install wipnote Codex hooks: %v\n", err)
	} else if changed {
		fmt.Println("wipnote Codex hooks installed in ~/.codex/hooks.json.")
	} else {
		fmt.Println("wipnote Codex hooks are already installed.")
	}
	if dryRun {
		fmt.Println("[dry-run] would install wipnote Codex agents into ~/.codex/agents")
	} else if changed, err := ensureCodexAgentsFromCache(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install wipnote Codex agents: %v\n", err)
	} else if changed {
		fmt.Println("wipnote Codex agents installed in ~/.codex/agents.")
	} else {
		fmt.Println("wipnote Codex agents are already installed.")
	}

	fmt.Println()
	fmt.Println("Setup complete. Run: wipnote codex")
	return nil
}

// launchCodexDefault launches Codex interactively with wipnote env injection.
// Corresponds to: wipnote codex
//
// Phase B: if no marketplace is registered, auto-register the bundled local
// marketplace tree resolved via resolveSharedTreePath("codex-marketplace").
// This replaces the historical "user must run --init first" path with the
// brew/curl-install bundled tree.
func launchCodexDefault(resumeID, trackID, featureID, worktreePath, workItem string, noWorktree, yolo bool, extraArgs []string) error {
	projectRoot, _ := resolveProjectRoot()
	// Apply isolation plan and HONOR it (slice-9): a RefuseLaunch plan aborts
	// before Codex starts. noWorktree here is effectiveInPlace (--in-place || --no-worktree).
	// When the plan will create a managed worktree, suppress the generic dirty-main
	// advisory — we emit an accurate message after carryover instead (bug-938e56ae).
	willCreateWorktree := !noWorktree && (trackID != "" || featureID != "" || workItem != "")
	launchPlan := applyLaunchPlanOpts(projectRoot, workItem, noWorktree, willCreateWorktree, os.Stderr)
	if err := enforceLaunchPlan(launchPlan, os.Stderr); err != nil {
		return err
	}
	configPath := codexConfigPath()

	// Auto-register the bundled marketplace if not registered.
	if !isCodexMarketplaceInstalledAt(configPath) {
		bundled, err := resolveSharedTreePath("codex-marketplace")
		if err != nil {
			return fmt.Errorf("resolving bundled Codex marketplace: %w", err)
		}
		bundledDir := codexMarketplaceAddArg(bundled)
		if out, addErr := exec.Command("codex", "plugin", "marketplace", "add", bundledDir).CombinedOutput(); addErr != nil {
			outStr := strings.TrimSpace(string(out))
			return fmt.Errorf("WIPNOTE AGENTS NOT LOADED\n─────────────────────────\nFailed to register the wipnote marketplace with Codex CLI:\n  %v\n\nThe Codex session will run WITHOUT wipnote agents (researcher, feature-coder, etc.).\n\nTry:\n  - Run `wipnote codex --init` manually to retry the setup\n  - Check ~/.codex/config.toml for a stale marketplace entry under [plugins.\"wipnote@wipnote\"]\n  - Report this at https://github.com/shakestzd/wipnote/issues\n\nOutput:\n%s", addErr, outStr)
		}
		fmt.Printf("wipnote Codex marketplace registered (bundled): %s\n", bundledDir)
	}

	if isCodexMarketplaceInstalledAt(configPath) && !isCodexHooksEnabledAt(configPath) {
		if err := ensureCodexHooksEnabled(configPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not enable hooks feature flag: %v\n", err)
		} else {
			fmt.Println("hooks feature flag enabled.")
		}
	}
	if isCodexMarketplaceInstalledAt(configPath) && !isCodexPluginEnabledAt(configPath) {
		if err := ensureCodexPluginEnabled(configPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not enable wipnote Codex plugin: %v\n", err)
		} else {
			fmt.Println("wipnote Codex plugin enabled.")
		}
	}
	if isCodexMarketplaceInstalledAt(configPath) && !isCodexPluginInstalledAt(codexPluginCachePath()) {
		if installed, err := ensureCodexLocalPluginInstalled(configPath, false); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not install local wipnote Codex plugin cache: %v\n", err)
		} else if installed {
			fmt.Println("wipnote Codex plugin installed in local cache.")
		}
	}
	if isCodexPluginInstalledAt(codexPluginCachePath()) {
		if changed, err := ensureCodexGlobalHooksFromCache(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not install wipnote Codex hooks: %v\n", err)
		} else if changed {
			fmt.Println("wipnote Codex hooks installed in ~/.codex/hooks.json.")
		}
		if changed, err := ensureCodexAgentsFromCache(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not install wipnote Codex agents: %v\n", err)
		} else if changed {
			fmt.Println("wipnote Codex agents installed in ~/.codex/agents.")
		}
	}

	// Work item attribution: emit `wipnote feature start <id>` before launching.
	if workItem != "" {
		if err := runCodexFeatureStart(workItem); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not start work item %s: %v\n", workItem, err)
		}
	}

	// Resolve worktree path.
	// canonicalProjectRoot detects when CWD is already a linked worktree (slice-3):
	// returns the canonical main repo root, or "" when in the main worktree.
	// canonicalRoot is the value injected as WIPNOTE_PROJECT_DIR AND used as the
	// base for worktree creation — it must always be the canonical main root,
	// never the linked worktree copy (slice-3 contract).
	canonicalRoot := projectRoot
	if c := canonicalProjectRoot(projectRoot); c != "" {
		canonicalRoot = c
	}
	workDir := projectRoot
	wipnoteRoot := canonicalProjectRoot(projectRoot)
	resolved := false
	worktreeCreated := false
	switch {
	case worktreePath != "":
		// Explicit path — use as-is; set WIPNOTE_PROJECT_DIR to canonical root.
		workDir = worktreePath
		wipnoteRoot = canonicalRoot
		resolved = true
	case !noWorktree && trackID != "":
		wt, created, err := EnsureForTrackStatus(trackID, canonicalRoot, os.Stdout)
		if err != nil {
			return err
		}
		workDir = wt
		wipnoteRoot = canonicalRoot
		worktreeCreated = created
		resolved = true
	case !noWorktree && featureID != "":
		wt, created, err := EnsureForFeatureStatus(featureID, canonicalRoot, os.Stdout)
		if err != nil {
			return err
		}
		workDir = wt
		wipnoteRoot = canonicalRoot
		worktreeCreated = created
		resolved = true
	}

	// Honor a managed-worktree plan (slice-9) when no explicit/track/feature
	// worktree was resolved above. WIPNOTE_PROJECT_DIR stays the canonical root.
	if wt, created, werr := resolveManagedWorktreeStatus(launchPlan, canonicalRoot, trackID, featureID, workItem, workDir, resolved, os.Stdout); werr != nil {
		return werr
	} else if wt != "" && wt != workDir {
		workDir = wt
		wipnoteRoot = canonicalRoot
		worktreeCreated = created
	}

	// Carry the canonical main repo's uncommitted tracked changes into the
	// freshly-created worktree so the session builds on the user's latest
	// working state (bug-938e56ae). Only for newly-created worktrees: a reused
	// worktree may already contain prior work and re-applying would double-apply
	// or fail. Main is never mutated. Carryover failure is non-fatal.
	if workDir != projectRoot {
		effectiveRoot := canonicalRoot
		emitWorktreeCarryoverMessage(launchPlan, effectiveRoot, workDir, worktreeCreated, os.Stdout)
	}

	fmt.Println("Launching Codex CLI with wipnote context...")
	return execCodex(codexLaunchOpts{
		ResumeID:     resumeID,
		ExtraArgs:    extraArgs,
		ProjectRoot:  workDir,
		WorktreeRoot: workDir,
		WipnoteRoot:  wipnoteRoot,
		Mode:         codexLaunchModeDefault,
		Yolo:         yolo,
	})
}

// runFeatureStart runs `wipnote feature start <id>` for work item attribution.
func runFeatureStart(id string) error {
	return runFeatureStartWithEnv(id, nil)
}

func runCodexFeatureStart(id string) error {
	return runFeatureStartWithEnv(id, buildCodexAgentEnv(nil))
}

func runFeatureStartWithEnv(id string, extraEnv []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not determine executable: %w", err)
	}
	cmd := exec.Command(exe, "feature", "start", id)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	return cmd.Run()
}

// launchCodexContinue resumes the last Codex session.
// Corresponds to: wipnote codex --continue
func launchCodexContinue(resumeID string, yolo bool, extraArgs []string) error {
	projectRoot, _ := resolveProjectRoot()
	fmt.Println("Resuming last Codex session...")
	return execCodex(codexLaunchOpts{
		ResumeLast:  resumeID == "", // only pass --last when no specific ID
		ResumeID:    resumeID,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		Mode:        codexLaunchModeContinue,
		Yolo:        yolo,
	})
}

// launchCodexDev registers the local packages/codex-marketplace/ and launches Codex.
// Corresponds to: wipnote codex --dev [--cleanup]
// If a mismatched marketplace is already registered (e.g., from a prior --init),
// it is removed and replaced with the local path.
func launchCodexDev(resumeID string, cleanup, dryRun, yolo bool, extraArgs []string) error {
	// Compute launcher mode for preflight logging/inspection (no behavior change).
	_ = computeLauncherMode("", true, false)

	// Resolve the local marketplace path relative to the project root.
	localMarketplace, err := resolveLocalCodexMarketplace()
	if err != nil {
		return err
	}
	projectRoot, _ := resolveProjectRoot()

	fmt.Printf("Launching Codex CLI in dev mode...\n")
	fmt.Printf("  Local marketplace: %s\n", localMarketplace)

	// Ensure the local marketplace is registered (replace mismatched registrations).
	configPath := codexConfigPath()
	registeredPath := getCodexMarketplacePathAt(configPath)

	// Convert to absolute paths for comparison
	localAbs, _ := filepath.Abs(localMarketplace)
	registeredAbs, _ := filepath.Abs(registeredPath)

	if registeredAbs != "" && registeredAbs != localAbs {
		// Mismatched registration: remove the old one via direct TOML editing
		oldPathDisplay := registeredPath
		if oldPathDisplay == "" {
			oldPathDisplay = "(unknown previous path)"
		}
		fmt.Printf("Replacing mismatched marketplace registration (%s)\n", oldPathDisplay)
		if dryRun {
			fmt.Printf("[dry-run] would remove wipnote registrations from %s\n", configPath)
		} else {
			removed, rmErr := removeCodexWipnoteRegistrations(configPath)
			if rmErr != nil {
				return fmt.Errorf("removing mismatched marketplace from %s: %w", configPath, rmErr)
			}
			if removed {
				fmt.Println("Mismatched registration removed from config.toml.")
			}
		}
		registeredPath = "" // Force re-add
	}

	// Add the local marketplace if not already registered at the correct path.
	// Codex requires the marketplace ROOT DIRECTORY, not the manifest file.
	localDir := codexMarketplaceAddArg(localMarketplace)
	localDirAbs, _ := filepath.Abs(localDir)
	if registeredAbs != localDirAbs {
		addArgs := []string{"plugin", "marketplace", "add", localDir}
		if dryRun {
			fmt.Printf("[dry-run] codex %s\n", strings.Join(addArgs, " "))
		} else {
			if out, err := exec.Command("codex", addArgs...).CombinedOutput(); err != nil {
				return fmt.Errorf("registering local marketplace failed: %w\n%s", err, strings.TrimSpace(string(out)))
			}
			fmt.Println("Local marketplace registered.")
		}
	} else {
		fmt.Println("Local marketplace already registered — proceeding.")
	}

	if dryRun {
		if !isCodexHooksEnabledAt(configPath) {
			fmt.Println("[dry-run] would enable hooks = true in ~/.codex/config.toml")
		}
	} else if !isCodexHooksEnabledAt(configPath) {
		if err := ensureCodexHooksEnabled(configPath); err != nil {
			return fmt.Errorf("enabling hooks feature flag in %s: %w", configPath, err)
		}
		fmt.Println("hooks feature flag enabled.")
	}

	if dryRun {
		if !isCodexPluginEnabledAt(configPath) {
			fmt.Println("[dry-run] would enable plugin wipnote@wipnote in ~/.codex/config.toml")
		}
	} else if !isCodexPluginEnabledAt(configPath) {
		if err := ensureCodexPluginEnabled(configPath); err != nil {
			return fmt.Errorf("enabling local wipnote plugin in %s: %w", configPath, err)
		}
		fmt.Println("Local wipnote plugin enabled.")
	}
	if !dryRun {
		if installed, err := ensureCodexLocalPluginInstalled(configPath, true); err != nil {
			return fmt.Errorf("installing local wipnote plugin cache: %w", err)
		} else if installed {
			fmt.Println("Local wipnote plugin installed in Codex cache.")
		}
		if changed, err := ensureCodexGlobalHooksFromCache(); err != nil {
			return fmt.Errorf("installing wipnote Codex hooks: %w", err)
		} else if changed {
			fmt.Println("wipnote Codex hooks installed in ~/.codex/hooks.json.")
		} else {
			fmt.Println("wipnote Codex hooks are already installed.")
		}
		pluginDir := codexInstalledPluginDirAt(codexPluginCachePath())
		if changed, err := ensureCodexCustomAgentsInstalled(pluginDir, codexAgentsPath()); err != nil {
			return fmt.Errorf("installing wipnote Codex agents: %w", err)
		} else if changed {
			fmt.Println("wipnote Codex agents installed in ~/.codex/agents.")
		}
		if changed, err := ensureCodexCustomAgentsInstalled(pluginDir, codexProjectAgentsPath(projectRoot)); err != nil {
			return fmt.Errorf("installing project wipnote Codex agents: %w", err)
		} else if changed {
			fmt.Println("wipnote Codex agents installed in .codex/agents.")
		}
	} else {
		fmt.Println("[dry-run] would install wipnote hooks into ~/.codex/hooks.json")
		fmt.Println("[dry-run] would install wipnote Codex agents into ~/.codex/agents and .codex/agents")
	}

	if dryRun {
		fmt.Printf("[dry-run] would exec: codex (resume=%q) in %s\n", resumeID, projectRoot)
		return nil
	}

	err = execCodex(codexLaunchOpts{
		ResumeID:    resumeID,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		Mode:        codexLaunchModeDev,
		Yolo:        yolo,
	})

	// --cleanup: unregister the local marketplace after session ends.
	if cleanup && !dryRun {
		fmt.Println("Cleaning up local marketplace registration...")
		removed, rmErr := removeCodexWipnoteRegistrations(configPath)
		if rmErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove marketplace registration: %v\n", rmErr)
		} else if !removed {
			fmt.Println("No wipnote registrations found to clean up.")
		}
	}

	return err
}

// resolveLocalCodexMarketplace returns the absolute path to packages/codex-marketplace/
// by walking up from CWD to find the project root (directory containing .wipnote/).
// Returns an error if no project root is found or the marketplace directory is missing.
func resolveLocalCodexMarketplace() (string, error) {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return "", fmt.Errorf("could not find project root (.wipnote/ directory not found)\n" +
			"Run from the wipnote project directory, or use wipnote codex --init for the marketplace version")
	}
	projectRoot := filepath.Dir(wipnoteDir)
	marketplacePath := filepath.Join(projectRoot, "packages", "codex-marketplace")
	if _, statErr := os.Stat(marketplacePath); os.IsNotExist(statErr) {
		return "", fmt.Errorf("packages/codex-marketplace/ not found at %s\n"+
			"Run from the wipnote repo root, or use wipnote codex --init for the marketplace version",
			marketplacePath)
	}
	abs, err := filepath.Abs(marketplacePath)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path for %s: %w", marketplacePath, err)
	}
	return abs, nil
}

// codexLaunchOpts controls how Codex is launched.
type codexLaunchOpts struct {
	// ResumeLast, when true, passes "resume --last" to codex.
	ResumeLast bool
	// ResumeID, if non-empty, passes "resume <id>" to codex.
	// Takes precedence over ResumeLast.
	ResumeID string
	// ExtraArgs are forwarded to the codex process.
	ExtraArgs []string
	// ProjectRoot is the absolute path to the project root (or worktree path).
	// When set, Codex is started with this as the working directory, and
	// WIPNOTE_PROJECT_DIR env var is injected.
	ProjectRoot string
	// WorktreeRoot, when non-empty, overrides the working directory for the
	// Codex process. The process runs in WorktreeRoot but WIPNOTE_PROJECT_DIR
	// is set to WipnoteRoot (the canonical project root with .wipnote/).
	WorktreeRoot string
	// WipnoteRoot is the canonical project root containing .wipnote/.
	// Used to set WIPNOTE_PROJECT_DIR when running in a worktree.
	WipnoteRoot string
	// Mode selects the wipnote instruction addendum composed into
	// model_instructions_file.
	Mode codexLaunchMode
	// Yolo passes Codex's explicit approvals/sandbox bypass flag before any
	// subcommand, matching Claude's bypassPermissions launcher behavior.
	Yolo bool
	// WritableRoots are passed to Codex before any subcommand so resumed
	// sessions and spawned subagents inherit required writable directories.
	WritableRoots []string
}

// execCodex builds the codex argv and execs it, replacing the current process.
// Returns only on exec error.
func execCodex(opts codexLaunchOpts) error {
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		return fmt.Errorf("codex not found in PATH: %w\nInstall Codex CLI first: https://github.com/openai/codex", err)
	}

	// Resolve the effective project dir for OTel collector spawning.
	effectiveProjDir := opts.ProjectRoot
	if opts.WipnoteRoot != "" {
		effectiveProjDir = opts.WipnoteRoot
	}

	// Auto-start a detached `wipnote serve` for the dashboard.
	ensureServeForDashboard(effectiveProjDir)

	// Launch-time guard-profile initialization (peer to the OTel collector
	// bootstrap). No-op when already approved or non-interactive; never blocks.
	ensureGuardProfile(effectiveProjDir)

	// Spawn a per-session OTel collector when a project dir is known and OTel
	// is not explicitly disabled. Non-fatal: falls back gracefully on failure.
	var otelPort int
	var otelSessionID string
	var otelCleanup func()
	if effectiveProjDir != "" && !isExplicitlyDisabled(os.Getenv("WIPNOTE_OTEL_ENABLED")) {
		otelPort, otelSessionID, otelCleanup = spawnCodexOtelCollector(effectiveProjDir)
		if otelCleanup != nil {
			defer otelCleanup()
		}
	}

	var dbPath string
	if effectiveProjDir != "" {
		var dbDir string
		dbPath, dbDir, err = prepareCodexWritableDB(effectiveProjDir)
		if err != nil {
			return err
		}
		opts.WritableRoots = appendUniqueCodexWritableRoot(opts.WritableRoots, dbDir)
	}

	instructionArgs, instructionErr := buildCodexInstructionConfigArgs(codexPath, opts.ExtraArgs, opts.effectiveMode())
	if instructionErr != nil {
		fmt.Fprintf(os.Stderr, "wipnote: warning: codex orchestrator instructions skipped: %v\n", instructionErr)
	}
	configArgs := append([]string{}, instructionArgs...)
	configArgs = append(configArgs, buildCodexAgentConfigArgs(codexAgentsPath())...)
	if opts.ProjectRoot != "" {
		configArgs = append(configArgs, buildCodexAgentConfigArgs(codexProjectAgentsPath(opts.ProjectRoot))...)
	}
	codexArgs := buildCodexArgs(opts, otelPort, configArgs)
	c := exec.Command(codexPath, codexArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	// Build the child env: start from os.Environ, inject WIPNOTE_PROJECT_DIR,
	// and layer OTel exporter vars when a collector was spawned.
	env := os.Environ()
	workDir := ""

	switch {
	case opts.WorktreeRoot != "":
		projectDir := opts.WipnoteRoot
		if projectDir == "" {
			projectDir = opts.ProjectRoot
		}
		env = setOrReplaceEnv(env, "WIPNOTE_PROJECT_DIR", projectDir)
		workDir = opts.WorktreeRoot
	case opts.ProjectRoot != "":
		env = setOrReplaceEnv(env, "WIPNOTE_PROJECT_DIR", opts.ProjectRoot)
		workDir = opts.ProjectRoot
	}

	if dbPath != "" {
		env = setOrReplaceEnv(env, "WIPNOTE_DB_PATH", dbPath)
	}
	env = buildCodexOtelEnv(env, otelPort, otelSessionID)
	env = buildCodexAgentEnv(env)

	// Session-family continuity (slice-4, feat-a225ce7c):
	// Resolve which family this Codex session belongs to, then inject
	// WIPNOTE_SESSION_FAMILY_ID so the SessionStart hook can write the DB column.
	// Also persist the launcher-side state file immediately (concrete write path
	// that survives even when hooks are not configured).
	if otelSessionID != "" && effectiveProjDir != "" {
		isResume := opts.ResumeID != "" || opts.ResumeLast
		// opts.ResumeID is the Codex-native session ID. It only matches a
		// wipnote family-index key in the rare case the two coincide; when it
		// does not, resolveSessionFamilyID falls through to the ordered
		// most-recent-session family (correct for "resume last").
		familyID := resolveSessionFamilyID(effectiveProjDir, otelSessionID, opts.ResumeID, isResume)
		env = setOrReplaceEnv(env, "WIPNOTE_SESSION_FAMILY_ID", familyID)
		persistLauncherSessionFamily(effectiveProjDir, otelSessionID, "codex", familyID)
	}

	// Signal YOLO mode to hook subprocesses. WIPNOTE_YOLO=1 is set only on an
	// explicit --yolo launch so the worktree source-isolation guard fires under
	// Codex just as it does under Claude Code's bypassPermissions path.
	if opts.Yolo {
		env = setOrReplaceEnv(env, "WIPNOTE_YOLO", "1")
	}

	c.Env = env
	if workDir != "" {
		c.Dir = workDir
	}

	return runHarnessWithCleanup(c, otelCleanup)
}

func prepareCodexWritableDB(projectDir string) (dbPath string, dbDir string, err error) {
	dbPath, err = storage.CanonicalDBPath(projectDir)
	if err != nil {
		return "", "", fmt.Errorf("resolving wipnote SQLite cache path for Codex: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return "", "", fmt.Errorf("creating wipnote SQLite cache directory for Codex: %w", err)
	}
	return dbPath, filepath.Dir(dbPath), nil
}

func appendUniqueCodexWritableRoot(roots []string, root string) []string {
	if root == "" {
		return roots
	}
	clean := filepath.Clean(root)
	for _, existing := range roots {
		if filepath.Clean(existing) == clean {
			return roots
		}
	}
	return append(roots, root)
}

func buildCodexArgs(opts codexLaunchOpts, otelPort int, instructionArgs []string) []string {
	var args []string
	args = append(args, buildCodexOtelConfigArgs(otelPort)...)
	args = append(args, instructionArgs...)
	if opts.Yolo {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	for _, root := range opts.WritableRoots {
		if root != "" {
			args = append(args, "--add-dir", root)
		}
	}

	if opts.ResumeID != "" {
		args = append(args, "resume", opts.ResumeID)
	} else if opts.ResumeLast {
		args = append(args, "resume", "--last")
	}

	args = append(args, opts.ExtraArgs...)
	return args
}
