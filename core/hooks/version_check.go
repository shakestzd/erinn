package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// CLIVersion holds the binary's compiled version string (set via ldflags).
// The main package sets this before any hook handler is invoked so that
// session-start can compare it against the installed plugin version.
var CLIVersion = "dev"

// pluginManifest is a minimal representation of plugin.json used only to
// extract the version field.
type pluginManifest struct {
	Version string `json:"version"`
}

// readPluginVersion reads the plugin version from
// ${CLAUDE_PLUGIN_ROOT}/.claude-plugin/plugin.json.
// Returns an empty string when CLAUDE_PLUGIN_ROOT is unset or the file is
// missing / unreadable.
func readPluginVersion() string {
	root := os.Getenv("CLAUDE_PLUGIN_ROOT")
	if root == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return ""
	}
	var manifest pluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ""
	}
	return manifest.Version
}

// stripGitDescribeSuffix strips the "-N-gHEX" suffix from a git describe version.
// If the version matches the pattern "-\d+-g[0-9a-f]+$", the suffix is removed.
// Otherwise, the version is returned unchanged.
// Examples:
//   - "0.55.6-34-ge86030cc" -> "0.55.6"
//   - "0.55.6" -> "0.55.6"
//   - "1.0.0-rc.1" -> "1.0.0-rc.1" (pre-release, not a git-describe suffix)
func stripGitDescribeSuffix(version string) string {
	// Match "-N-gHEX" at the end of the string.
	re := regexp.MustCompile(`^(.+)-\d+-g[0-9a-f]+$`)
	matches := re.FindStringSubmatch(version)
	if len(matches) == 2 {
		return matches[1]
	}
	return version
}

// versionMismatchWarning returns a warning string when the CLI version and
// plugin version differ (and neither is "dev"). Returns an empty string when
// the versions match or when the check is skipped.
// Dev builds (e.g., "0.55.6-34-ge86030cc") are compared against plugin versions
// after stripping the git-describe suffix.
func versionMismatchWarning() string {
	cli := CLIVersion
	plugin := readPluginVersion()

	// Skip check when running from a dev build or when the plugin version
	// cannot be determined.
	if cli == "dev" || plugin == "dev" || plugin == "" {
		return ""
	}

	// Strip git-describe suffix from both versions for comparison.
	cliCompare := stripGitDescribeSuffix(cli)
	pluginCompare := stripGitDescribeSuffix(plugin)

	if cliCompare == pluginCompare {
		return ""
	}

	return fmt.Sprintf(
		"wipnote version mismatch: CLI v%s != plugin v%s\nRun `wipnote build` to sync, or update the plugin.",
		cli, plugin,
	)
}
