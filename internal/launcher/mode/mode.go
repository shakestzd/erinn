// Package mode provides a shared launcher-mode helper that computes execution,
// runtime, and plugin modes for wipnote launchers (claude, codex, antigravity, yolo).
// It is a read-only observer: it never mutates any repository state.
package mode

import "os"

// RuntimeMode describes where wipnote is running.
type RuntimeMode string

const (
	// RuntimeHost is a standard host installation (bare metal or VM).
	RuntimeHost RuntimeMode = "host"
	// RuntimeDevcontainer is a VS Code devcontainer / GitHub Codespace.
	RuntimeDevcontainer RuntimeMode = "devcontainer"
	// RuntimeCI is a continuous-integration environment.
	RuntimeCI RuntimeMode = "ci"
)

// ExecutionMode describes how the launcher manages source-code isolation.
type ExecutionMode string

const (
	// ExecIsolatedWorktree runs in a git worktree (strongest isolation).
	ExecIsolatedWorktree ExecutionMode = "isolated-worktree"
	// ExecInPlace runs directly in the project root.
	ExecInPlace ExecutionMode = "in-place"
	// ExecReadOnly is a read-only view (no mutations permitted).
	ExecReadOnly ExecutionMode = "read-only"
)

// PluginMode describes which plugin tree is active.
type PluginMode string

const (
	// PluginInstalled uses the bundled / marketplace-installed plugin tree.
	PluginInstalled PluginMode = "installed"
	// PluginLocalDev uses the in-tree plugin/ source via --plugin-dir.
	PluginLocalDev PluginMode = "local-dev"
	// PluginGeneratedPort uses a generated harness-specific port tree.
	PluginGeneratedPort PluginMode = "generated-port"
)

// LauncherMode is the composed mode object returned to callers.
type LauncherMode struct {
	Runtime       RuntimeMode
	Execution     ExecutionMode
	Plugin        PluginMode
	DashboardHost string
	DashboardPort int
}

// ComputeInput bundles the inputs for ComputeWith, letting callers inject stubs.
type ComputeInput struct {
	// IsDevcontainer detects /.dockerenv, CODESPACES, REMOTE_CONTAINERS.
	// Callers should pass defaultDevcontainerDetector from claude_serve_autostart.go,
	// or a test stub.
	IsDevcontainer func() bool
	// IsCI detects CI / GITHUB_ACTIONS.
	IsCI func() bool
	// WorktreePath is the active worktree path, or empty if in-place.
	WorktreePath string
	// ReadOnly marks the session as read-only.
	ReadOnly bool
	// DevPlugin is true when the launcher uses --plugin-dir (dev mode).
	DevPlugin bool
	// GeneratedPort is true when a harness-generated port tree is active.
	GeneratedPort bool
}

// Compute returns the LauncherMode using live environment signals.
// It reuses the same detection signals as defaultDevcontainerDetector in
// cmd/wipnote/claude_serve_autostart.go (/.dockerenv, CODESPACES, REMOTE_CONTAINERS).
func Compute(worktreePath string, readOnly, devPlugin, generatedPort bool) LauncherMode {
	return ComputeWith(ComputeInput{
		IsDevcontainer: defaultDevcontainerFn,
		IsCI:           defaultCIFn,
		WorktreePath:   worktreePath,
		ReadOnly:       readOnly,
		DevPlugin:      devPlugin,
		GeneratedPort:  generatedPort,
	})
}

// ComputeWith returns the LauncherMode using injected detector functions.
// This is the testable core — production code calls Compute.
func ComputeWith(in ComputeInput) LauncherMode {
	runtime := DetectRuntimeModeWith(in.IsDevcontainer, in.IsCI)
	execution := DetectExecutionModeWith(in.WorktreePath, in.ReadOnly)
	plugin := DetectPluginMode(in.DevPlugin, in.GeneratedPort)
	host, port := DashboardBindDefaults(runtime)
	return LauncherMode{
		Runtime:       runtime,
		Execution:     execution,
		Plugin:        plugin,
		DashboardHost: host,
		DashboardPort: port,
	}
}

// DetectRuntimeModeWith returns the RuntimeMode using injected detector functions.
// CI takes priority over devcontainer so that CI pipelines inside containers
// are correctly identified as CI.
func DetectRuntimeModeWith(isDevcontainer, isCI func() bool) RuntimeMode {
	if isCI() {
		return RuntimeCI
	}
	if isDevcontainer() {
		return RuntimeDevcontainer
	}
	return RuntimeHost
}

// DetectExecutionModeWith returns the ExecutionMode from the worktree path and
// read-only flag.
func DetectExecutionModeWith(worktreePath string, readOnly bool) ExecutionMode {
	if readOnly {
		return ExecReadOnly
	}
	if worktreePath != "" {
		return ExecIsolatedWorktree
	}
	return ExecInPlace
}

// DetectPluginMode returns the PluginMode from boolean launcher flags.
func DetectPluginMode(devPlugin, generatedPort bool) PluginMode {
	switch {
	case devPlugin:
		return PluginLocalDev
	case generatedPort:
		return PluginGeneratedPort
	default:
		return PluginInstalled
	}
}

// DashboardBindDefaults returns the default host and port for the wipnote
// dashboard based on the runtime mode.
//
//   - RuntimeDevcontainer: 0.0.0.0:8088 — the port must be forwarded to the
//     host, so binding all interfaces is required.
//   - RuntimeHost / RuntimeCI: 127.0.0.1:8080 — localhost only.
func DashboardBindDefaults(runtime RuntimeMode) (host string, port int) {
	if runtime == RuntimeDevcontainer {
		return "0.0.0.0", 8088
	}
	return "127.0.0.1", 8080
}

// defaultDevcontainerFn replicates the detection logic of
// defaultDevcontainerDetector in cmd/wipnote/claude_serve_autostart.go.
// The signals are /.dockerenv, CODESPACES=true, REMOTE_CONTAINERS=true.
func defaultDevcontainerFn() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if os.Getenv("CODESPACES") == "true" {
		return true
	}
	if os.Getenv("REMOTE_CONTAINERS") == "true" {
		return true
	}
	return false
}

// defaultCIFn detects common CI environment markers.
func defaultCIFn() bool {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		return true
	}
	if os.Getenv("CI") == "true" {
		return true
	}
	return false
}
