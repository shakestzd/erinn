package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/launcher"
)

// TestAppendOrReplaceEnv_AppendsNew verifies that a key not present in env is appended.
func TestAppendOrReplaceEnv_AppendsNew(t *testing.T) {
	env := []string{"FOO=bar", "BAZ=qux"}
	got := appendOrReplaceEnv(env, "NEW_KEY=new_value")
	found := false
	for _, kv := range got {
		if kv == "NEW_KEY=new_value" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected NEW_KEY=new_value to be appended; got %v", got)
	}
	// Original keys must still be present.
	for _, orig := range env {
		present := false
		for _, kv := range got {
			if kv == orig {
				present = true
				break
			}
		}
		if !present {
			t.Errorf("original key %q was lost; got %v", orig, got)
		}
	}
}

// TestAppendOrReplaceEnv_OverridesExisting verifies that an existing key is replaced in-place.
func TestAppendOrReplaceEnv_OverridesExisting(t *testing.T) {
	env := []string{"FOO=old", "BAR=keep"}
	got := appendOrReplaceEnv(env, "FOO=new")

	// FOO must be overridden.
	fooCount := 0
	for _, kv := range got {
		if kv == "FOO=old" {
			t.Errorf("stale FOO=old still present in %v", got)
		}
		if kv == "FOO=new" {
			fooCount++
		}
	}
	if fooCount != 1 {
		t.Errorf("expected exactly one FOO=new entry, got %d in %v", fooCount, got)
	}

	// BAR must be unchanged.
	barFound := false
	for _, kv := range got {
		if kv == "BAR=keep" {
			barFound = true
		}
	}
	if !barFound {
		t.Errorf("BAR=keep was lost; got %v", got)
	}
}

// TestAppendOrReplaceEnv_Multiple verifies that multiple kv pairs are all applied.
func TestAppendOrReplaceEnv_Multiple(t *testing.T) {
	env := []string{"EXISTING=old"}
	got := appendOrReplaceEnv(env, "EXISTING=new", "BRAND=fresh")

	existingNew := false
	brandFresh := false
	for _, kv := range got {
		if kv == "EXISTING=new" {
			existingNew = true
		}
		if kv == "BRAND=fresh" {
			brandFresh = true
		}
		if kv == "EXISTING=old" {
			t.Errorf("stale EXISTING=old still present in %v", got)
		}
	}
	if !existingNew {
		t.Errorf("EXISTING=new not found in %v", got)
	}
	if !brandFresh {
		t.Errorf("BRAND=fresh not found in %v", got)
	}
}

// TestAppendOrReplaceEnv_Empty verifies that empty input env is handled correctly.
func TestAppendOrReplaceEnv_Empty(t *testing.T) {
	got := appendOrReplaceEnv(nil, "KEY=val")
	if len(got) != 1 || got[0] != "KEY=val" {
		t.Errorf("expected [KEY=val], got %v", got)
	}

	got2 := appendOrReplaceEnv([]string{}, "A=1", "B=2")
	if len(got2) != 2 {
		t.Errorf("expected 2 entries, got %v", got2)
	}
}

func TestBuildCodexAgentEnv_SetsCodexIdentity(t *testing.T) {
	got := buildCodexAgentEnv([]string{
		"WIPNOTE_AGENT_ID=previous",
		"WIPNOTE_AGENT_TYPE=previous",
		"KEEP=yes",
	})
	for _, want := range []string{
		"WIPNOTE_AGENT_ID=codex",
		"WIPNOTE_AGENT_TYPE=codex",
		"KEEP=yes",
	} {
		if indexOf(got, want) < 0 {
			t.Fatalf("expected %q in env %v", want, got)
		}
	}
	if indexOf(got, "WIPNOTE_AGENT_ID=previous") >= 0 || indexOf(got, "WIPNOTE_AGENT_TYPE=previous") >= 0 {
		t.Fatalf("stale Codex identity remained in env %v", got)
	}
}

func TestBuildCodexOtelConfigArgs_DisabledWithoutPort(t *testing.T) {
	if got := buildCodexOtelConfigArgs(0); len(got) != 0 {
		t.Fatalf("expected no config args without collector port, got %v", got)
	}
}

func TestBuildCodexOtelConfigArgs_ConfiguresLogsTracesAndMetrics(t *testing.T) {
	got := buildCodexOtelConfigArgs(43189)
	joined := ""
	for _, arg := range got {
		joined += arg + "\n"
	}
	for _, want := range []string{
		"otel.log_user_prompt=true",
		`otel.exporter={ otlp-http = { endpoint = "http://127.0.0.1:43189/v1/logs", protocol = "binary" } }`,
		`otel.trace_exporter={ otlp-http = { endpoint = "http://127.0.0.1:43189/v1/traces", protocol = "binary" } }`,
		`otel.metrics_exporter={ otlp-http = { endpoint = "http://127.0.0.1:43189/v1/metrics", protocol = "binary" } }`,
	} {
		if !containsLine(joined, want) {
			t.Errorf("Codex OTel config args missing %q in %v", want, got)
		}
	}
	for i := 0; i < len(got); i += 2 {
		if got[i] != "-c" {
			t.Fatalf("expected every config override to be prefixed by -c, got %v", got)
		}
	}
}

func TestApplyCodexLaunchIntent(t *testing.T) {
	got := applyCodexLaunchIntent("", "", "", false, launcher.LaunchIntent{
		Kind:            launcher.LaunchIntentContinue,
		Explicit:        true,
		WorkItemID:      "feat-cdx",
		SessionHarness:  "codex",
		ResumeSessionID: "sess-cdx",
		WorktreePath:    ".claude/worktrees/feat-cdx",
	})
	if got.mode != codexLaunchModeContinue {
		t.Fatalf("mode = %q, want %q", got.mode, codexLaunchModeContinue)
	}
	if got.resumeID != "sess-cdx" {
		t.Fatalf("resumeID = %q, want sess-cdx", got.resumeID)
	}
	if got.workItem != "feat-cdx" {
		t.Fatalf("workItem = %q, want feat-cdx", got.workItem)
	}
	if got.worktreePath != ".claude/worktrees/feat-cdx" {
		t.Fatalf("worktreePath = %q, want .claude/worktrees/feat-cdx", got.worktreePath)
	}

	yolo := applyCodexLaunchIntent("", "", "", true, launcher.ContinueWorkIntent("feat-yolo", "codex", "sess-yolo", ".claude/worktrees/feat-yolo", true))
	if yolo.mode != codexLaunchModeYoloCont {
		t.Fatalf("yolo mode = %q, want %q", yolo.mode, codexLaunchModeYoloCont)
	}

	cross := applyCodexLaunchIntent("/custom", "feat-existing", "keep-me", false, launcher.ContinueWorkIntent("feat-cross", "gemini", "", ".claude/worktrees/feat-cross", true))
	if cross.resumeID != "keep-me" {
		t.Fatalf("cross resumeID = %q, want keep-me", cross.resumeID)
	}
	if cross.worktreePath != "/custom" {
		t.Fatalf("explicit worktreePath overwritten: got %q", cross.worktreePath)
	}
	if cross.workItem != "feat-existing" {
		t.Fatalf("explicit workItem overwritten: got %q", cross.workItem)
	}
}

func TestBuildCodexArgs_PutsYoloBeforeResume(t *testing.T) {
	got := buildCodexArgs(codexLaunchOpts{
		ResumeLast: true,
		Yolo:       true,
	}, 0, nil)

	yoloIdx := indexOf(got, "--dangerously-bypass-approvals-and-sandbox")
	resumeIdx := indexOf(got, "resume")
	if yoloIdx < 0 {
		t.Fatalf("expected Codex bypass flag in %v", got)
	}
	if resumeIdx < 0 {
		t.Fatalf("expected resume subcommand in %v", got)
	}
	if yoloIdx > resumeIdx {
		t.Fatalf("expected bypass flag before resume subcommand, got %v", got)
	}
}

func TestBuildCodexArgs_YoloPassesBothBypassFlags(t *testing.T) {
	got := buildCodexArgs(codexLaunchOpts{
		Yolo: true,
	}, 0, nil)

	approvalsIdx := indexOf(got, "--dangerously-bypass-approvals-and-sandbox")
	trustIdx := indexOf(got, "--dangerously-bypass-hook-trust")

	if approvalsIdx < 0 {
		t.Fatalf("expected --dangerously-bypass-approvals-and-sandbox flag in %v", got)
	}
	if trustIdx < 0 {
		t.Fatalf("expected --dangerously-bypass-hook-trust flag in %v", got)
	}
	// Both flags should be present before any subcommand
	for i, arg := range got {
		if arg == "resume" || arg == "exec" {
			if i < approvalsIdx || i < trustIdx {
				t.Fatalf("expected both bypass flags before subcommands, got %v", got)
			}
		}
	}
}

func TestBuildCodexArgs_NonYoloOmitsBypassFlags(t *testing.T) {
	got := buildCodexArgs(codexLaunchOpts{
		ResumeLast: true,
	}, 0, nil)

	approvalsIdx := indexOf(got, "--dangerously-bypass-approvals-and-sandbox")
	trustIdx := indexOf(got, "--dangerously-bypass-hook-trust")

	if approvalsIdx >= 0 {
		t.Fatalf("expected no --dangerously-bypass-approvals-and-sandbox flag in non-yolo mode, got %v", got)
	}
	if trustIdx >= 0 {
		t.Fatalf("expected no --dangerously-bypass-hook-trust flag in non-yolo mode, got %v", got)
	}
}

func TestBuildCodexArgs_PutsSandboxOverrideBeforeResume(t *testing.T) {
	got := buildCodexArgs(codexLaunchOpts{
		ResumeLast:  true,
		SandboxMode: "danger-full-access",
	}, 0, nil)

	sandboxIdx := indexOf(got, "--sandbox")
	resumeIdx := indexOf(got, "resume")
	if sandboxIdx < 0 {
		t.Fatalf("expected --sandbox in %v", got)
	}
	if sandboxIdx+1 >= len(got) || got[sandboxIdx+1] != "danger-full-access" {
		t.Fatalf("expected sandbox override value after --sandbox, got %v", got)
	}
	if resumeIdx < 0 {
		t.Fatalf("expected resume subcommand in %v", got)
	}
	if sandboxIdx > resumeIdx {
		t.Fatalf("expected sandbox override before resume subcommand, got %v", got)
	}
}

func TestBuildCodexArgs_PutsOtelConfigBeforeResume(t *testing.T) {
	got := buildCodexArgs(codexLaunchOpts{
		ResumeLast: true,
		ExtraArgs:  []string{"--sandbox", "workspace-write"},
	}, 43189, nil)

	resumeIdx := indexOf(got, "resume")
	if resumeIdx < 0 {
		t.Fatalf("expected resume subcommand in %v", got)
	}
	for i := 0; i < resumeIdx; i += 2 {
		if got[i] != "-c" {
			t.Fatalf("expected config overrides before resume, got %v", got)
		}
	}
	if !containsLine(strings.Join(got[:resumeIdx], "\n"), "otel.log_user_prompt=true") {
		t.Fatalf("expected OTel config before resume, got %v", got)
	}
	if got[resumeIdx+1] != "--last" {
		t.Fatalf("expected resume --last after config overrides, got %v", got)
	}
	if got[len(got)-2] != "--sandbox" || got[len(got)-1] != "workspace-write" {
		t.Fatalf("expected forwarded extra args after resume args, got %v", got)
	}
}

func TestBuildCodexArgs_IncludesInstructionOverrideBeforeResume(t *testing.T) {
	got := buildCodexArgs(codexLaunchOpts{
		ResumeLast: true,
	}, 0, []string{"-c", `model_instructions_file="/tmp/wipnote-codex.md"`})

	resumeIdx := indexOf(got, "resume")
	if resumeIdx < 0 {
		t.Fatalf("expected resume subcommand in %v", got)
	}
	if got[0] != "-c" || got[1] != `model_instructions_file="/tmp/wipnote-codex.md"` {
		t.Fatalf("expected instruction override before resume, got %v", got)
	}
	if resumeIdx < 2 {
		t.Fatalf("expected resume after instruction override, got %v", got)
	}
}

func TestBuildCodexArgs_PutsWritableRootsBeforeResume(t *testing.T) {
	got := buildCodexArgs(codexLaunchOpts{
		ResumeLast:    true,
		WritableRoots: []string{"/tmp/wipnote-cache"},
		ExtraArgs:     []string{"--sandbox", "workspace-write"},
	}, 0, nil)

	addDirIdx := indexOf(got, "--add-dir")
	resumeIdx := indexOf(got, "resume")
	if addDirIdx < 0 {
		t.Fatalf("expected --add-dir in %v", got)
	}
	if addDirIdx+1 >= len(got) || got[addDirIdx+1] != "/tmp/wipnote-cache" {
		t.Fatalf("expected writable root after --add-dir, got %v", got)
	}
	if resumeIdx < 0 {
		t.Fatalf("expected resume subcommand in %v", got)
	}
	if addDirIdx > resumeIdx {
		t.Fatalf("expected --add-dir before resume subcommand, got %v", got)
	}
	if got[len(got)-2] != "--sandbox" || got[len(got)-1] != "workspace-write" {
		t.Fatalf("expected forwarded extra args after resume args, got %v", got)
	}
}

func TestPrepareCodexWritableDBCreatesParent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cache", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	gotPath, gotDir, err := prepareCodexWritableDB(t.TempDir())
	if err != nil {
		t.Fatalf("prepareCodexWritableDB: %v", err)
	}
	if gotPath != dbPath {
		t.Fatalf("db path = %q, want %q", gotPath, dbPath)
	}
	if gotDir != filepath.Dir(dbPath) {
		t.Fatalf("db dir = %q, want %q", gotDir, filepath.Dir(dbPath))
	}
	if info, err := os.Stat(gotDir); err != nil {
		t.Fatalf("expected db parent to exist: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("expected db parent to be a directory")
	}
}

func TestAppendUniqueCodexWritableRootDedupesCleanPaths(t *testing.T) {
	got := appendUniqueCodexWritableRoot([]string{"/tmp/wipnote-cache"}, "/tmp/wipnote-cache/.")
	if len(got) != 1 {
		t.Fatalf("expected duplicate clean path to be ignored, got %v", got)
	}
	got = appendUniqueCodexWritableRoot(got, "/tmp/other-cache")
	if len(got) != 2 || got[1] != "/tmp/other-cache" {
		t.Fatalf("expected distinct root to be appended, got %v", got)
	}
}

func TestCodexRequestedModel(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "short", args: []string{"-m", "gpt-5.4"}, want: "gpt-5.4"},
		{name: "long", args: []string{"--model", "gpt-5.5"}, want: "gpt-5.5"},
		{name: "equals", args: []string{"--model=gpt-5.3-codex"}, want: "gpt-5.3-codex"},
		{name: "absent", args: []string{"--sandbox", "workspace-write"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexRequestedModel(tt.args); got != tt.want {
				t.Fatalf("codexRequestedModel(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestCodexRequestedSandbox(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "long", args: []string{"--sandbox", "workspace-write"}, want: "workspace-write"},
		{name: "equals", args: []string{"--sandbox=danger-full-access"}, want: "danger-full-access"},
		{name: "absent", args: []string{"--model", "gpt-5"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexRequestedSandbox(tt.args); got != tt.want {
				t.Fatalf("codexRequestedSandbox(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestResolveCodexSandboxMode_UsesDangerFullAccessOnBubblewrapFailure(t *testing.T) {
	orig := codexSandboxProbe
	codexSandboxProbe = func(string) ([]byte, error) {
		return []byte("bwrap: Failed to make / slave: Permission denied"), errors.New("exit status 1")
	}
	t.Cleanup(func() { codexSandboxProbe = orig })

	mode, notice := resolveCodexSandboxMode("/tmp/codex", codexLaunchOpts{}, true)
	if mode != "danger-full-access" {
		t.Fatalf("mode = %q, want danger-full-access", mode)
	}
	for _, want := range []string{
		"bubblewrap sandbox is unavailable",
		"`--sandbox danger-full-access`",
		"Approvals remain enabled",
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice missing %q in %q", want, notice)
		}
	}
}

func TestRenderCodexWarningBanner_FormatsSandboxNotice(t *testing.T) {
	notice := "wipnote: Codex bubblewrap sandbox is unavailable in this devcontainer/Codespace; launching with `--sandbox danger-full-access` so tools do not fail one by one. Approvals remain enabled."
	got := renderCodexWarningBanner(notice)

	for _, want := range []string{
		"Codex launch notice",
		"⚠ wipnote: Codex bubblewrap sandbox is unavailable",
		"`--sandbox danger-full-access`",
		"Approvals remain enabled",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("warning banner missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Warning: Warning:") {
		t.Fatalf("warning banner doubled the label:\n%s", got)
	}
}

func TestResolveCodexSandboxMode_SkipsProbeOutsideDevcontainer(t *testing.T) {
	orig := codexSandboxProbe
	called := false
	codexSandboxProbe = func(string) ([]byte, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { codexSandboxProbe = orig })

	mode, notice := resolveCodexSandboxMode("/tmp/codex", codexLaunchOpts{}, false)
	if mode != "" || notice != "" {
		t.Fatalf("expected no override outside devcontainer, got mode=%q notice=%q", mode, notice)
	}
	if called {
		t.Fatal("probe should not run outside devcontainer")
	}
}

func TestResolveCodexSandboxMode_RespectsExplicitSandboxArg(t *testing.T) {
	orig := codexSandboxProbe
	called := false
	codexSandboxProbe = func(string) ([]byte, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { codexSandboxProbe = orig })

	mode, notice := resolveCodexSandboxMode("/tmp/codex", codexLaunchOpts{
		ExtraArgs: []string{"--sandbox", "workspace-write"},
	}, true)
	if mode != "" || notice != "" {
		t.Fatalf("expected explicit sandbox arg to win, got mode=%q notice=%q", mode, notice)
	}
	if called {
		t.Fatal("probe should not run when sandbox is explicit")
	}
}

func TestResolveCodexSandboxMode_IgnoresNonBubblewrapFailures(t *testing.T) {
	orig := codexSandboxProbe
	codexSandboxProbe = func(string) ([]byte, error) {
		return []byte("config parse error"), errors.New("exit status 1")
	}
	t.Cleanup(func() { codexSandboxProbe = orig })

	mode, notice := resolveCodexSandboxMode("/tmp/codex", codexLaunchOpts{}, true)
	if mode != "" || notice != "" {
		t.Fatalf("expected unrelated probe failure to avoid override, got mode=%q notice=%q", mode, notice)
	}
}

func TestSelectCodexBaseInstructions(t *testing.T) {
	data := []byte(`{"models":[{"slug":"gpt-a","base_instructions":"base a"},{"slug":"gpt-b","base_instructions":"base b"}]}`)

	if got, err := selectCodexBaseInstructions(data, "gpt-b"); err != nil || got != "base b" {
		t.Fatalf("select specific = %q, %v; want base b, nil", got, err)
	}
	if got, err := selectCodexBaseInstructions(data, ""); err != nil || got != "base a" {
		t.Fatalf("select default = %q, %v; want base a, nil", got, err)
	}
	if _, err := selectCodexBaseInstructions(data, "missing"); err == nil {
		t.Fatalf("expected missing model error")
	}
}

func TestWriteCodexInstructionsFileComposesBaseAndWipnotePrompt(t *testing.T) {
	path, err := writeCodexInstructionsFile("base instructions", "extra instructions", codexLaunchModeDefault)
	if err != nil {
		t.Fatalf("writeCodexInstructionsFile: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated instructions: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"base instructions",
		"# wipnote Orchestrator Addendum",
		"extra instructions",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("generated instructions missing %q:\n%s", want, content)
		}
	}
}

func TestCodexLaunchOptsEffectiveMode(t *testing.T) {
	tests := []struct {
		name string
		opts codexLaunchOpts
		want codexLaunchMode
	}{
		{name: "default", opts: codexLaunchOpts{}, want: codexLaunchModeDefault},
		{name: "continue", opts: codexLaunchOpts{ResumeLast: true}, want: codexLaunchModeContinue},
		{name: "dev", opts: codexLaunchOpts{Mode: codexLaunchModeDev}, want: codexLaunchModeDev},
		{name: "yolo", opts: codexLaunchOpts{Yolo: true}, want: codexLaunchModeYolo},
		{name: "yolo dev", opts: codexLaunchOpts{Mode: codexLaunchModeDev, Yolo: true}, want: codexLaunchModeYoloDev},
		{name: "yolo continue", opts: codexLaunchOpts{Mode: codexLaunchModeContinue, Yolo: true}, want: codexLaunchModeYoloCont},
		{name: "yolo resume", opts: codexLaunchOpts{ResumeLast: true, Yolo: true}, want: codexLaunchModeYoloCont},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.opts.effectiveMode(); got != tt.want {
				t.Fatalf("effectiveMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCodexInstructionAddendumByMode(t *testing.T) {
	tests := []struct {
		name string
		mode codexLaunchMode
		want []string
	}{
		{name: "default", mode: codexLaunchModeDefault, want: []string{"# wipnote Orchestrator"}},
		{name: "dev", mode: codexLaunchModeDev, want: []string{"# wipnote Orchestrator", "## Codex Dev Mode"}},
		{name: "continue", mode: codexLaunchModeContinue, want: []string{"# wipnote Orchestrator", "## Codex Continue Mode"}},
		{name: "yolo", mode: codexLaunchModeYolo, want: []string{"# YOLO Autonomous Development Mode", "## Codex YOLO Mode"}},
		{name: "yolo dev", mode: codexLaunchModeYoloDev, want: []string{"# YOLO Autonomous Development Mode", "## Codex Dev Mode", "## Codex YOLO Mode"}},
		{name: "yolo continue", mode: codexLaunchModeYoloCont, want: []string{"# YOLO Autonomous Development Mode", "## Codex Continue Mode", "## Codex YOLO Mode"}},
		{name: "init", mode: codexLaunchModeInit, want: []string{"## Codex Init Mode"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codexInstructionAddendum(tt.mode)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("addendum for %s missing %q", tt.name, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Gap 1: real bwrap probe — probe-mocked tests
// ---------------------------------------------------------------------------

// TestResolveCodexSandboxMode_BwrapAbsent verifies that when the bwrap binary
// is not found on PATH (simulated via exec.LookPath failure path by returning
// "bwrap not found" output with a non-nil error) the resolver degrades.
func TestResolveCodexSandboxMode_BwrapAbsent(t *testing.T) {
	orig := codexSandboxProbe
	codexSandboxProbe = func(string) ([]byte, error) {
		// Simulate bwrap binary absent from PATH — same output shape that
		// the real probe emits via exec.LookPath failure.
		return []byte("bwrap not found on PATH"), errors.New("exec: \"bwrap\": executable file not found in $PATH")
	}
	t.Cleanup(func() { codexSandboxProbe = orig })

	mode, notice := resolveCodexSandboxMode("/tmp/codex", codexLaunchOpts{}, true)
	// bwrap absent is not a bwrap failure message → probe error alone is not
	// sufficient; only errors that match isCodexBubblewrapFailure degrade.
	// The output "bwrap not found on PATH" DOES contain "bwrap" → degraded.
	if mode != "danger-full-access" {
		t.Fatalf("mode = %q, want danger-full-access when bwrap binary absent", mode)
	}
	if !strings.Contains(notice, "bubblewrap sandbox is unavailable") {
		t.Fatalf("notice missing expected text, got %q", notice)
	}
}

// TestResolveCodexSandboxMode_CleanProbeNoDegrade verifies that when the probe
// succeeds (exit 0) the resolver does not apply any override.
func TestResolveCodexSandboxMode_CleanProbeNoDegrade(t *testing.T) {
	orig := codexSandboxProbe
	codexSandboxProbe = func(string) ([]byte, error) {
		return []byte(""), nil // probe succeeds → bwrap available
	}
	t.Cleanup(func() { codexSandboxProbe = orig })

	mode, notice := resolveCodexSandboxMode("/tmp/codex", codexLaunchOpts{}, true)
	if mode != "" || notice != "" {
		t.Fatalf("expected no override when probe succeeds, got mode=%q notice=%q", mode, notice)
	}
}

// TestResolveCodexSandboxMode_UnsharePermissionDenied verifies that a
// realistic bwrap namespace failure ("operation not permitted" + "unshare")
// correctly degrades.
func TestResolveCodexSandboxMode_UnsharePermissionDenied(t *testing.T) {
	orig := codexSandboxProbe
	codexSandboxProbe = func(string) ([]byte, error) {
		return []byte("bwrap: unshare(CLONE_NEWUSER): Operation not permitted"), errors.New("exit status 1")
	}
	t.Cleanup(func() { codexSandboxProbe = orig })

	mode, notice := resolveCodexSandboxMode("/tmp/codex", codexLaunchOpts{}, true)
	if mode != "danger-full-access" {
		t.Fatalf("mode = %q, want danger-full-access for unshare EPERM", mode)
	}
	if !strings.Contains(notice, "Approvals remain enabled") {
		t.Fatalf("notice missing Approvals remain enabled, got %q", notice)
	}
}

// ---------------------------------------------------------------------------
// Gap 2: env var WIPNOTE_CODEX_SANDBOX=degraded
// ---------------------------------------------------------------------------

// TestApplySandboxDegradedEnv_SetsDegradedWhenTrue verifies the env var is
// injected when degraded=true.
func TestApplySandboxDegradedEnv_SetsDegradedWhenTrue(t *testing.T) {
	env := []string{"FOO=bar"}
	got := applySandboxDegradedEnv(env, true)
	found := false
	for _, kv := range got {
		if kv == "WIPNOTE_CODEX_SANDBOX=degraded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected WIPNOTE_CODEX_SANDBOX=degraded in env %v", got)
	}
}

// TestApplySandboxDegradedEnv_NoOpWhenFalse verifies the env is unchanged when
// degraded=false (normal and user-overridden launches must not be polluted).
func TestApplySandboxDegradedEnv_NoOpWhenFalse(t *testing.T) {
	env := []string{"FOO=bar", "WIPNOTE_CODEX_SANDBOX=something-else"}
	got := applySandboxDegradedEnv(env, false)
	for _, kv := range got {
		if kv == "WIPNOTE_CODEX_SANDBOX=degraded" {
			t.Fatalf("unexpected WIPNOTE_CODEX_SANDBOX=degraded set when degraded=false in %v", got)
		}
	}
	// Original env must be intact.
	if len(got) != len(env) {
		t.Fatalf("env length changed: got %d, want %d", len(got), len(env))
	}
}

// TestApplySandboxDegradedEnv_ReplacesExisting verifies that if
// WIPNOTE_CODEX_SANDBOX is already set it is replaced, not duplicated.
func TestApplySandboxDegradedEnv_ReplacesExisting(t *testing.T) {
	env := []string{"WIPNOTE_CODEX_SANDBOX=other", "BAR=baz"}
	got := applySandboxDegradedEnv(env, true)
	count := 0
	for _, kv := range got {
		if strings.HasPrefix(kv, "WIPNOTE_CODEX_SANDBOX=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one WIPNOTE_CODEX_SANDBOX entry, got %d in %v", count, got)
	}
}

// ---------------------------------------------------------------------------
// Gap 3: isCodexBubblewrapFailure — "operation not permitted" gating
// ---------------------------------------------------------------------------

// TestIsCodexBubblewrapFailure_EPERMAloneDoesNotTrigger ensures a bare
// "operation not permitted" without namespace/bwrap context is not treated
// as a bwrap failure, avoiding false positives on unrelated EPERM errors.
func TestIsCodexBubblewrapFailure_EPERMAloneDoesNotTrigger(t *testing.T) {
	out := []byte("open /etc/passwd: operation not permitted")
	err := errors.New("exit status 1")
	if isCodexBubblewrapFailure(out, err) {
		t.Fatal("bare 'operation not permitted' should NOT trigger bwrap detection")
	}
}

// TestIsCodexBubblewrapFailure_EPERMWithNamespaceContext confirms that
// "operation not permitted" paired with a namespace-context word IS treated
// as a bwrap failure.
func TestIsCodexBubblewrapFailure_EPERMWithNamespaceContext(t *testing.T) {
	cases := []struct {
		name string
		out  string
	}{
		{"unshare", "bwrap: unshare(CLONE_NEWUSER): Operation not permitted"},
		{"namespace", "failed to create namespace: operation not permitted"},
		{"bwrap direct", "bwrap: operation not permitted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isCodexBubblewrapFailure([]byte(tc.out), errors.New("exit status 1")) {
				t.Fatalf("expected bwrap failure for output %q", tc.out)
			}
		})
	}
}

// TestIsCodexBubblewrapFailure_BwrapDirectMatch ensures existing bwrap/bubblewrap
// keyword matching still works without needing "operation not permitted".
func TestIsCodexBubblewrapFailure_BwrapDirectMatch(t *testing.T) {
	cases := []struct {
		name string
		out  string
	}{
		{"bwrap keyword", "bwrap: some error"},
		{"bubblewrap", "bubblewrap failed"},
		{"failed to make slave", "failed to make / slave: operation not permitted"},
		{"cannot create namespace", "Cannot create namespace: permission denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isCodexBubblewrapFailure([]byte(tc.out), errors.New("exit status 1")) {
				t.Fatalf("expected bwrap failure for output %q", tc.out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// User override guards — no auto-degrade regardless of probe
// ---------------------------------------------------------------------------

// TestResolveCodexSandboxMode_YoloSkipsProbe verifies --yolo bypasses the probe.
func TestResolveCodexSandboxMode_YoloSkipsProbe(t *testing.T) {
	orig := codexSandboxProbe
	called := false
	codexSandboxProbe = func(string) ([]byte, error) {
		called = true
		return []byte("bwrap: unshare: Operation not permitted"), errors.New("exit status 1")
	}
	t.Cleanup(func() { codexSandboxProbe = orig })

	mode, notice := resolveCodexSandboxMode("/tmp/codex", codexLaunchOpts{Yolo: true}, true)
	if mode != "" || notice != "" {
		t.Fatalf("expected yolo to skip auto-degrade, got mode=%q notice=%q", mode, notice)
	}
	if called {
		t.Fatal("probe should not run in yolo mode")
	}
}

// TestResolveCodexSandboxMode_BypassFlagSkipsProbe verifies that
// --dangerously-bypass-approvals-and-sandbox skips the probe.
func TestResolveCodexSandboxMode_BypassFlagSkipsProbe(t *testing.T) {
	orig := codexSandboxProbe
	called := false
	codexSandboxProbe = func(string) ([]byte, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { codexSandboxProbe = orig })

	mode, notice := resolveCodexSandboxMode("/tmp/codex", codexLaunchOpts{
		ExtraArgs: []string{"--dangerously-bypass-approvals-and-sandbox"},
	}, true)
	if mode != "" || notice != "" {
		t.Fatalf("expected bypass flag to skip auto-degrade, got mode=%q notice=%q", mode, notice)
	}
	if called {
		t.Fatal("probe should not run when bypass flag is set")
	}
}

func containsLine(s, want string) bool {
	for _, line := range strings.Split(s, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}
