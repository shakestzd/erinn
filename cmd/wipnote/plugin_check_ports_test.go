package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/port/pluginbuild"
)

// checkPortsManifest is a minimal self-contained manifest exercising all three
// targets. It mirrors the live packages/plugin-core/manifest.json target shape
// without depending on it, so the drift gate is tested hermetically.
func checkPortsManifest() *pluginbuild.Manifest {
	return &pluginbuild.Manifest{
		Name:        "wipnote",
		Version:     "0.0.0-test",
		Description: "test plugin",
		Author:      pluginbuild.Author{Name: "Tester"},
		Homepage:    "https://example.com",
		Repository:  "https://example.com/repo",
		License:     "MIT",
		Category:    "Dev",
		Keywords:    []string{"test"},
		Targets: map[string]pluginbuild.Target{
			"claude": {OutDir: "plugin", ManifestPath: ".claude-plugin/plugin.json", HooksPath: "hooks/hooks.json"},
			"codex": {
				OutDir:                 "port/packages/codex-marketplace",
				ManifestPath:           ".codex-plugin/plugin.json",
				HooksPath:              "hooks.json",
				MCPPath:                ".mcp.json",
				MarketplaceName:        "wipnote",
				MarketplaceDisplayName: "wipnote",
				MarketplaceCategory:    "Dev",
				PluginSubdir:           ".agents/plugins/wipnote",
			},
			"gemini": {OutDir: "port/packages/gemini-extension", ManifestPath: "gemini-extension.json", HooksPath: "hooks/hooks.json", ContextFile: "GEMINI.md", CommandNamespace: "wipnote"},
		},
		AssetSources: pluginbuild.AssetSources{
			Commands: "plugin/commands",
			Agents:   "plugin/agents",
		},
		Hooks: pluginbuild.HookMatrix{Events: []pluginbuild.HookEvent{
			{Name: "SessionStart", Handler: "session-start", Targets: []string{"claude", "codex", "gemini"}},
			{Name: "UserPromptSubmit", Handler: "user-prompt", Targets: []string{"claude", "codex"}},
			{Name: "Stop", Handler: "stop", Targets: []string{"claude"}},
		}},
	}
}

// seedCheckPortsRepo builds a synthetic repo root containing go.mod, the
// manifest at its canonical path, source assets, and a freshly generated (and
// therefore in-sync) set of committed plugin trees. It returns the repo root
// and the resolved manifest.
func seedCheckPortsRepo(t *testing.T) (string, *pluginbuild.Manifest) {
	t.Helper()
	repoRoot := t.TempDir()

	// go.mod so findRepoRoot terminates at repoRoot.
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Source assets under plugin/.
	cmdDir := filepath.Join(repoRoot, "plugin", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "hello.md"), []byte("# hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agDir := filepath.Join(repoRoot, "plugin", "agents")
	if err := os.MkdirAll(agDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agDir, "x.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Gemini requires GEMINI.md at repo root when ContextFile is set.
	if err := os.WriteFile(filepath.Join(repoRoot, "GEMINI.md"), []byte("# ctx\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Manifest at the canonical path so FindManifest locates it.
	manifestDir := filepath.Join(repoRoot, "packages", "plugin-core")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(checkPortsManifest(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(manifestDir, "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := pluginbuild.Load(manifestPath)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}

	// Generate the committed trees so the starting point is in sync.
	for _, name := range []string{"claude", "codex", "gemini"} {
		adapter, err := pluginbuild.Get(name)
		if err != nil {
			t.Fatalf("get adapter %s: %v", name, err)
		}
		outDir := filepath.Join(repoRoot, m.Targets[name].OutDir)
		if err := adapter.Emit(m, repoRoot, outDir); err != nil {
			t.Fatalf("seed emit %s: %v", name, err)
		}
	}
	return repoRoot, m
}

// runCheckPortsCmd invokes `plugin check-ports` with cwd set to repoRoot and
// returns combined output plus the RunE error.
func runCheckPortsCmd(t *testing.T, repoRoot string) (string, error) {
	t.Helper()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })

	cmd := pluginCheckPortsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(nil)
	runErr := cmd.Execute()
	return buf.String(), runErr
}

// TestCheckPortsCleanTreeExitsZero: a freshly generated tree is in sync, so
// check-ports must succeed.
func TestCheckPortsCleanTreeExitsZero(t *testing.T) {
	repoRoot, _ := seedCheckPortsRepo(t)
	out, err := runCheckPortsCmd(t, repoRoot)
	if err != nil {
		t.Fatalf("expected clean tree to pass, got error: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "in sync") {
		t.Errorf("expected 'in sync' confirmation, got:\n%s", out)
	}
}

// TestCheckPortsDetectsMutatedGeneratedFile: hand-editing a committed
// generated artifact must be reported as drift naming that path.
func TestCheckPortsDetectsMutatedGeneratedFile(t *testing.T) {
	repoRoot, m := seedCheckPortsRepo(t)

	// Mutate the committed Claude manifest (a generated artifact).
	claudeManifest := filepath.Join(repoRoot, m.Targets["claude"].OutDir, m.Targets["claude"].ManifestPath)
	if err := os.WriteFile(claudeManifest, []byte(`{"name":"tampered"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCheckPortsCmd(t, repoRoot)
	if err == nil {
		t.Fatalf("expected drift error, got nil\noutput:\n%s", out)
	}
	rel := filepath.Join(m.Targets["claude"].OutDir, m.Targets["claude"].ManifestPath)
	if !strings.Contains(out, rel) {
		t.Errorf("expected drift output to name %q, got:\n%s", rel, out)
	}
	if !strings.Contains(out, "out of sync") {
		t.Errorf("expected 'out of sync' summary, got:\n%s", out)
	}
}

// TestCheckPortsDetectsManifestInputChange: changing a generator input
// (manifest.json) without regenerating must surface as drift.
func TestCheckPortsDetectsManifestInputChange(t *testing.T) {
	repoRoot, _ := seedCheckPortsRepo(t)

	manifestPath := filepath.Join(repoRoot, "packages", "plugin-core", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	// Bump the version: the regenerated manifests embed this, so the committed
	// (un-regenerated) trees now differ from a fresh build.
	generic["version"] = "9.9.9-changed"
	bumped, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, bumped, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCheckPortsCmd(t, repoRoot)
	if err == nil {
		t.Fatalf("expected drift after manifest input change, got nil\noutput:\n%s", out)
	}
	if !strings.Contains(out, "out of sync") {
		t.Errorf("expected 'out of sync' summary, got:\n%s", out)
	}
}

// TestCheckPortsDetectsStaleExtraFile: a committed file under an owned subtree
// that the fresh regen no longer produces is reported as "extra" drift.
func TestCheckPortsDetectsStaleExtraFile(t *testing.T) {
	repoRoot, m := seedCheckPortsRepo(t)

	stale := filepath.Join(repoRoot, m.Targets["codex"].OutDir, ".agents", "plugins", "wipnote", "commands", "stale-removed.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCheckPortsCmd(t, repoRoot)
	if err == nil {
		t.Fatalf("expected drift for stale extra file, got nil\noutput:\n%s", out)
	}
	if !strings.Contains(out, "stale-removed.md") {
		t.Errorf("expected stale file to be named in drift output, got:\n%s", out)
	}
}

// repoRootDir walks up from this test file to the repo root (the dir holding
// go.mod) so the smoke tests can load the real .githooks/pre-commit.
func repoRootDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root from test file")
		}
		dir = parent
	}
}

// execTempDir returns a fresh temp dir on an exec-capable filesystem. The
// default $TMPDIR is mounted noexec in this devcontainer, which makes git
// refuse the pre-commit hook and prevents the stub scripts from running. We
// root throwaway dirs under <repo>/.gotmp-exec (on the exec-capable workspace
// volume) and fall back to t.TempDir() elsewhere.
func execTempDir(t *testing.T, repoSrc string) string {
	t.Helper()
	base := filepath.Join(repoSrc, ".gotmp-exec")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp(base, "checkports-smoke-")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// installPreCommitSmokeRepo creates a throwaway git repo with the real
// .githooks/pre-commit installed and a stub `wipnote` on PATH. The stub writes
// a marker file when invoked and exits non-zero, so the test can distinguish
// "check-ports ran and aborted the commit" from "check-ports was skipped".
// Returns the repo dir and the marker path.
func installPreCommitSmokeRepo(t *testing.T) (repoDir, marker string, env []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoSrc := repoRootDir(t)
	hookSrc := filepath.Join(repoSrc, ".githooks", "pre-commit")
	hookBytes, err := os.ReadFile(hookSrc)
	if err != nil {
		t.Fatalf("read real pre-commit hook: %v", err)
	}

	repoDir = execTempDir(t, repoSrc)
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = repoDir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "commit.gpgsign", "false")

	// Install the real hook.
	hooksDir := filepath.Join(repoDir, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, hookBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode is umask-masked; force the exec bits or git ignores it.
	if err := os.Chmod(hookPath, 0o755); err != nil {
		t.Fatal(err)
	}
	run("config", "core.hooksPath", ".githooks")

	// Stub `wipnote`: record the call, then fail (simulating detected drift).
	binDir := execTempDir(t, repoSrc)
	marker = filepath.Join(binDir, "wipnote-was-called")
	stub := "#!/bin/sh\necho called > " + shellQuote(marker) + "\nexit 1\n"
	wipnoteStub := filepath.Join(binDir, "wipnote")
	if err := os.WriteFile(wipnoteStub, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wipnoteStub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Stub `go` too: the hook's Go gate would otherwise run a real toolchain
	// against an empty repo. A no-op success keeps the smoke test focused on
	// the check-ports branch.
	goStub := filepath.Join(binDir, "go")
	if err := os.WriteFile(goStub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(goStub, 0o755); err != nil {
		t.Fatal(err)
	}

	env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	return repoDir, marker, env
}

func gitCommit(t *testing.T, repoDir string, env []string, stagePath, content string) (string, error) {
	t.Helper()
	full := filepath.Join(repoDir, stagePath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", stagePath)
	add.Dir = repoDir
	add.Env = env
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v\n%s", stagePath, err, out)
	}
	c := exec.Command("git", "commit", "-m", "smoke")
	c.Dir = repoDir
	c.Env = env
	out, err := c.CombinedOutput()
	return string(out), err
}

// TestPreCommitInvokesCheckPortsOnScopedPath: staging a plugin/ path must
// trigger the hook's check-ports gate; the failing stub aborts the commit.
func TestPreCommitInvokesCheckPortsOnScopedPath(t *testing.T) {
	repoDir, marker, env := installPreCommitSmokeRepo(t)

	out, err := gitCommit(t, repoDir, env, "plugin/commands/new.md", "# new\n")
	if err == nil {
		t.Fatalf("expected commit to be aborted by failing check-ports, but it succeeded\n%s", out)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("expected check-ports (stub wipnote) to have been invoked; marker missing: %v\ncommit output:\n%s", statErr, out)
	}
	if !strings.Contains(out, "check-ports") {
		t.Errorf("expected hook output to mention check-ports, got:\n%s", out)
	}
}

// TestPreCommitSkipsCheckPortsOnUnrelatedPath: staging only an unrelated path
// must NOT invoke check-ports, so the commit succeeds despite the failing stub.
func TestPreCommitSkipsCheckPortsOnUnrelatedPath(t *testing.T) {
	repoDir, marker, env := installPreCommitSmokeRepo(t)

	out, err := gitCommit(t, repoDir, env, "docs/readme.txt", "hello\n")
	if err != nil {
		t.Fatalf("expected commit to succeed (check-ports skipped), got error: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Errorf("expected check-ports to be SKIPPED for unrelated path, but stub wipnote was invoked\ncommit output:\n%s", out)
	}
}
