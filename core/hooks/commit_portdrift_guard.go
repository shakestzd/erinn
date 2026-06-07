package hooks

import (
	"os/exec"
	"strings"
)

// generatorInputDirs are the source-of-truth directories whose contents, when
// staged, trigger a CheckPorts verification. They map to the asset source
// directories that pluginbuild.Emit reads from repoRoot.
var generatorInputDirs = []string{
	"plugin/commands",
	"plugin/agents",
	"plugin/skills",
	"plugin/templates",
	"plugin/static",
	"plugin/config",
}

// generatorInputManifest is the manifest file that, when staged, also triggers
// a CheckPorts verification.
const generatorInputManifest = "packages/plugin-core/manifest.json"

// checkPortDriftCommitGuard is a PreToolUse guard that fires ONLY when a git
// commit is being executed AND at least one staged file is a generator-input
// (packages/plugin-core/manifest.json or under plugin/{commands,agents,…}).
//
// Fast path: if no generator-input files are staged, the guard returns "" with
// only the cost of one `git diff --cached --name-only` call.
//
// Slow path: if a generator-input IS staged, calls pluginbuild.CheckPorts. If
// it reports drift (stale generated trees), blocks the commit with a
// remediation message. If ports are in sync, allows.
//
// Always-on (not YOLO-gated) — this is a correctness gate, consistent with
// other always-on PreToolUse guards. Works on all three harnesses via the same
// Decision:"block" / HookResult mechanism as other PreToolUse guards.
func checkPortDriftCommitGuard(event *CloudEvent) string {
	if !isShellTool(event.ToolName) {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
	if !gitCommitPattern.MatchString(cmd) {
		return ""
	}

	// Resolve the repo root from the event CWD.
	dir := event.CWD
	if dir == "" {
		return ""
	}
	repoRoot := resolveGitRepoRoot(dir)
	if repoRoot == "" {
		return ""
	}

	// Cheap gate: list staged files.
	staged, err := stagedFiles(repoRoot)
	if err != nil || len(staged) == 0 {
		return ""
	}

	// Fast pass: if none of the staged files are generator inputs, allow.
	if !hasGeneratorInput(staged) {
		return ""
	}

	// Slow path: a generator-input is staged — check for drift via the injected
	// checker (feat-331927fb) so this core guard does not import pluginbuild. A
	// nil checker means plugin tooling isn't wired (e.g. a downstream project
	// dogfooding wipnote), so there is nothing to verify — allow.
	if PortDriftPathsFn == nil {
		return ""
	}
	paths := PortDriftPathsFn(repoRoot)
	if len(paths) == 0 {
		return ""
	}
	return "Generated plugin trees are stale — run `wipnote plugin build-ports`, " +
		"stage the result, and re-commit.\n" +
		"Drifted paths: " + strings.Join(paths, ", ")
}

// stagedFiles returns the list of staged file paths (relative to repoRoot)
// using `git diff --cached --name-only`.
func stagedFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "diff", "--cached", "--name-only").Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// hasGeneratorInput reports whether any staged path is a generator input:
// either the manifest file or a file under one of the generatorInputDirs.
func hasGeneratorInput(staged []string) bool {
	for _, f := range staged {
		if f == generatorInputManifest {
			return true
		}
		for _, dir := range generatorInputDirs {
			// Use HasPrefix with a trailing slash to avoid partial dir name matches.
			if strings.HasPrefix(f, dir+"/") || f == dir {
				return true
			}
		}
	}
	return false
}

// resolveGitRepoRoot returns the top-level git repository root for dir, or ""
// when dir is not inside a git repository.
func resolveGitRepoRoot(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
