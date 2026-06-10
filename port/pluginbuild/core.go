// Package pluginbuild loads the shared plugin manifest and emits target-specific
// plugin trees (Claude Code, Codex CLI) from it. The manifest at
// packages/plugin-core/manifest.json is the single source of truth — adapters
// translate it into the directory layouts each CLI expects.
package pluginbuild

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ManifestPath is the conventional location of the shared manifest relative to
// the repo root.
const ManifestPath = "packages/plugin-core/manifest.json"

// Manifest is the shared plugin metadata loaded from manifest.json.
type Manifest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Description  string            `json:"description"`
	Author       Author            `json:"author"`
	Homepage     string            `json:"homepage"`
	Repository   string            `json:"repository"`
	License      string            `json:"license"`
	Category     string            `json:"category"`
	Keywords     []string          `json:"keywords"`
	Targets      map[string]Target `json:"targets"`
	AssetSources AssetSources      `json:"assetSources"`
	Hooks        HookMatrix        `json:"hooks"`
}

// Author identifies the plugin publisher.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Target declares where a generated plugin tree should be written and what
// manifest/hooks paths it uses inside that tree.
type Target struct {
	OutDir       string `json:"outDir"`
	ManifestPath string `json:"manifestPath"`
	HooksPath    string `json:"hooksPath"`
	MCPPath      string `json:"mcpPath,omitempty"`
	// ContextFile, when set, names a repo-relative file copied into the
	// generated tree's root (e.g. Gemini's GEMINI.md). Empty means no copy.
	ContextFile string `json:"contextFile,omitempty"`
	// CommandNamespace, when set, wraps translated commands under
	// commands/<namespace>/ so the slash-command resolves to /namespace:name
	// (used by Gemini; Claude/Codex ignore this field).
	CommandNamespace string `json:"commandNamespace,omitempty"`
	// Marketplace metadata for targets that wrap the plugin in a marketplace
	// container (e.g. Codex 0.121.0+). Empty values mean no marketplace wrapping.
	MarketplaceName        string `json:"marketplaceName,omitempty"`
	MarketplaceDisplayName string `json:"marketplaceDisplayName,omitempty"`
	MarketplaceCategory    string `json:"marketplaceCategory,omitempty"`
	// PluginSubdir is the relative path under outDir where the actual plugin
	// content lives when marketplace wrapping is used (e.g. "plugins/wipnote").
	PluginSubdir string `json:"pluginSubdir,omitempty"`
}

// AssetSources names the repo-relative directories to copy verbatim into every
// generated plugin tree.
type AssetSources struct {
	Commands  string `json:"commands"`
	Agents    string `json:"agents"`
	Skills    string `json:"skills"`
	Templates string `json:"templates"`
	Static    string `json:"static"`
	Config    string `json:"config"`
}

// HookMatrix is the ordered list of hook events declared across all targets.
type HookMatrix struct {
	Events []HookEvent `json:"events"`
}

// HookEvent declares a single event entry. Handler is the `wipnote hook
// <handler>` subcommand (ignored when Command is set). Command is an escape
// hatch for shell-only hooks like the Claude timestamp injector. Targets is
// the list of target names for which this entry is emitted.
// GeminiEventName, when set, overrides the Claude event name in the Gemini
// output. When empty the Claude event name is used unchanged.
// GeminiHandler, when set, overrides the handler for Gemini targets. When empty
// the Handler field is used unchanged.
type HookEvent struct {
	Name            string   `json:"name"`
	Handler         string   `json:"handler"`
	Command         string   `json:"command,omitempty"`
	Matcher         string   `json:"matcher,omitempty"`
	Timeout         int      `json:"timeout,omitempty"`
	Targets         []string `json:"targets"`
	GeminiEventName string   `json:"geminiEventName,omitempty"`
	GeminiHandler   string   `json:"geminiHandler,omitempty"`
}

// AppliesTo reports whether the event should be emitted for the named target.
func (e HookEvent) AppliesTo(target string) bool {
	for _, t := range e.Targets {
		if t == target {
			return true
		}
	}
	return false
}

// Load reads and parses the shared manifest from manifestPath. Relative paths
// are resolved against the current working directory by the caller — Load
// itself does not assume a project root.
func Load(manifestPath string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read plugin-core manifest %s: %w", manifestPath, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse plugin-core manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// FindManifest walks up from startDir looking for packages/plugin-core/manifest.json.
// Returns the absolute path or an error when no manifest is found.
func FindManifest(startDir string) (string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for dir := abs; ; {
		candidate := filepath.Join(dir, ManifestPath)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("plugin-core manifest not found (looked for %s up from %s)", ManifestPath, startDir)
		}
		dir = parent
	}
}

func (m *Manifest) validate() error {
	if m.Name == "" {
		return fmt.Errorf("manifest.name is required")
	}
	if m.Version == "" {
		return fmt.Errorf("manifest.version is required")
	}
	if len(m.Targets) == 0 {
		return fmt.Errorf("manifest.targets must declare at least one target")
	}
	for name, t := range m.Targets {
		if t.OutDir == "" || t.ManifestPath == "" || t.HooksPath == "" {
			return fmt.Errorf("manifest.targets.%s must set outDir, manifestPath, hooksPath", name)
		}
	}
	for i, e := range m.Hooks.Events {
		if e.Name == "" {
			return fmt.Errorf("manifest.hooks.events[%d].name is required", i)
		}
		if e.Handler == "" && e.Command == "" {
			return fmt.Errorf("manifest.hooks.events[%d] must set handler or command", i)
		}
		if len(e.Targets) == 0 {
			return fmt.Errorf("manifest.hooks.events[%d].targets must list at least one target", i)
		}
	}
	return nil
}
