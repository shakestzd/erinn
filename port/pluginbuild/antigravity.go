package pluginbuild

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func init() { Register(antigravityAdapter{}) }

// antigravityAdapter emits the Antigravity CLI plugin tree. Layout:
//
//	<outDir>/plugin.json
//	<outDir>/GEMINI.md                  (copied from repoRoot, if target.ContextFile is set)
//	<outDir>/commands/<namespace>/*.toml
//	<outDir>/agents/*.md
//	<outDir>/skills/<name>/SKILL.md
//	<outDir>/hooks.json
type antigravityAdapter struct{}

func (antigravityAdapter) Name() string { return "antigravity" }

// antigravityOwnedSubtrees lists the subdirectory names under the antigravity outDir that
// build-ports fully regenerates. Hand-maintained files (README.md, etc.) live
// outside these subtrees and are never touched by stale-file cleanup.
var antigravityOwnedSubtrees = []string{"commands", "agents", "skills", "templates", "static", "config"}

// geminiToAntigravityTool renames Gemini-CLI tool names that differ in the
// Antigravity CLI. Verified live against agy v1.0.8 (feat-c08b20a6): agy exposes
// "run_command" and has no "run_shell_command". Tools not listed here are
// assumed identical (agy is a Gemini-CLI descendant).
var geminiToAntigravityTool = map[string]string{
	"run_shell_command": "run_command",
}

func (a antigravityAdapter) Emit(m *Manifest, repoRoot, outDir string) error {
	target, ok := m.Targets[a.Name()]
	if !ok {
		return fmt.Errorf("manifest has no target %q", a.Name())
	}

	// Pre-clean owned subtrees so renamed/deleted source files don't leave
	// stale output files behind. Non-owned files (README, plugin.json,
	// GEMINI.md, etc.) at the outDir root are untouched.
	if err := cleanOwnedSubtrees(outDir, antigravityOwnedSubtrees); err != nil {
		return fmt.Errorf("antigravity pre-clean: %w", err)
	}

	if err := writeAntigravityManifest(m, target, filepath.Join(outDir, target.ManifestPath)); err != nil {
		return err
	}
	if err := ensureAntigravitySkeletonDirs(outDir); err != nil {
		return err
	}

	// Sub-emitters populate the skeleton (assets, commands).
	for _, emit := range antigravitySubEmitters {
		if err := emit(m, repoRoot, outDir, target); err != nil {
			return err
		}
	}

	// Write hooks.json
	if err := writeAntigravityHooks(m, filepath.Join(outDir, target.HooksPath)); err != nil {
		return err
	}

	// Scaffold the plugin-scoped MCP config. agy reads plugin MCP servers from
	// mcp_config.json at the extension root (verified live against agy v1.0.8:
	// `agy plugin validate` reports "mcpServers: processed" only for
	// mcp_config.json — not .mcp.json or an mcpServers key in plugin.json).
	if target.MCPPath != "" {
		if err := ensureMCPServersScaffold(filepath.Join(outDir, target.MCPPath)); err != nil {
			return err
		}
	}

	// Mark explicit-only skills with disable-model-invocation so agy never
	// auto-activates destructive/high-cost workflows from description matching.
	if err := applyAntigravitySkillInvocationFlags(filepath.Join(outDir, "skills")); err != nil {
		return err
	}

	return nil
}

// antigravityDisableModelInvocationSkills lists wipnote skills that must only
// run on explicit user invocation — never via autonomous model activation.
// agy honors disable-model-invocation in SKILL.md frontmatter (verified live
// against agy v1.0.8, spk-0698d585); it is the only wipnote target harness that
// does, so the flag is injected here rather than in the shared skill source.
// The set is deliberately conservative: destructive (deploy ships releases),
// high-cost (execute fans out many agents), and externally-visible
// (report-issue transmits data) workflows.
var antigravityDisableModelInvocationSkills = map[string]bool{
	"deploy":       true,
	"execute":      true,
	"report-issue": true,
}

// applyAntigravitySkillInvocationFlags injects "disable-model-invocation: true"
// into the YAML frontmatter of each explicit-only skill's SKILL.md. It is a
// no-op for skills not in the set and idempotent for skills that already carry
// the flag. Skills without frontmatter or that are absent from the tree are
// skipped silently.
func applyAntigravitySkillInvocationFlags(skillsDir string) error {
	for name := range antigravityDisableModelInvocationSkills {
		path := filepath.Join(skillsDir, name, "SKILL.md")
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			// Fail loud, not silent: a listed explicit-only skill that no longer
			// exists (renamed/removed in source) would otherwise lose its
			// disable-model-invocation flag and become model-invocable again —
			// the unsafe direction for destructive/high-cost workflows.
			log.Printf("antigravity_skills: explicit-only skill %q not found at %s; disable-model-invocation NOT applied — update antigravityDisableModelInvocationSkills if it was renamed or removed", name, path)
			continue
		}
		if err != nil {
			return fmt.Errorf("read skill %s: %w", name, err)
		}
		patched, changed := injectDisableModelInvocation(raw)
		if !changed {
			continue
		}
		if err := os.WriteFile(path, patched, 0o644); err != nil {
			return fmt.Errorf("write skill %s: %w", name, err)
		}
	}
	return nil
}

// injectDisableModelInvocation inserts a "disable-model-invocation: true" line
// at the top of the leading YAML frontmatter block. Returns (input, false) when
// the file has no leading frontmatter delimiter or already contains the key.
func injectDisableModelInvocation(raw []byte) ([]byte, bool) {
	const key = "disable-model-invocation"
	if !bytes.HasPrefix(raw, []byte("---\n")) {
		return raw, false
	}
	if bytes.Contains(raw, []byte(key+":")) {
		return raw, false
	}
	// Insert the key immediately after the opening delimiter line.
	rest := raw[len("---\n"):]
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.WriteString(key + ": true\n")
	buf.Write(rest)
	return buf.Bytes(), true
}

type AntigravitySubEmitter func(m *Manifest, repoRoot, outDir string, target Target) error

var antigravitySubEmitters = []AntigravitySubEmitter{
	emitAntigravityAssets,
	emitAntigravityCommands,
	emitAntigravityAgents,
}

// Antigravity plugin manifest schema.
type antigravityPluginJSON struct {
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Description string           `json:"description"`
	Author      claudeAuthorJSON `json:"author"`
	Homepage    string           `json:"homepage,omitempty"`
	Repository  string           `json:"repository,omitempty"`
	License     string           `json:"license,omitempty"`
}

func writeAntigravityManifest(m *Manifest, t Target, path string) error {
	return writeJSON(path, antigravityPluginJSON{
		Name:        m.Name,
		Version:     m.Version,
		Description: m.Description,
		Author:      claudeAuthorJSON{Name: m.Author.Name},
		Homepage:    m.Homepage,
		Repository:  m.Repository,
		License:     m.License,
	})
}

func ensureAntigravitySkeletonDirs(outDir string) error {
	for _, dir := range []string{"commands", "agents", "skills"} {
		if err := os.MkdirAll(filepath.Join(outDir, dir), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func emitAntigravityAssets(m *Manifest, repoRoot, outDir string, t Target) error {
	knownRoles := codexKnownAgentRoles(m, repoRoot)
	pairs := []struct{ src, dst string }{
		{m.AssetSources.Skills, "skills"},
		{m.AssetSources.Templates, "templates"},
		{m.AssetSources.Static, "static"},
		{m.AssetSources.Config, "config"},
	}
	for _, p := range pairs {
		if p.src == "" {
			continue
		}
		src := filepath.Join(repoRoot, p.src)
		dst := filepath.Join(outDir, p.dst)
		if err := copyAssetTreeGemini(src, dst, knownRoles); err != nil {
			return fmt.Errorf("antigravity copy %s -> %s: %w", p.src, p.dst, err)
		}
	}

	if t.ContextFile != "" {
		src := filepath.Join(repoRoot, t.ContextFile)
		// Antigravity reads GEMINI.md-style context files, but the source is
		// Antigravity-specific so we do not copy the Gemini instructions verbatim.
		dst := filepath.Join(outDir, "GEMINI.md")
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("antigravity copy contextFile %s: %w", t.ContextFile, err)
		}
	}
	return nil
}

func emitAntigravityCommands(m *Manifest, repoRoot, outDir string, t Target) error {
	if m.AssetSources.Commands == "" {
		return nil
	}
	knownRoles := codexKnownAgentRoles(m, repoRoot)
	srcDir := filepath.Join(repoRoot, m.AssetSources.Commands)
	info, err := os.Stat(srcDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat commands source %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("commands source %s is not a directory", srcDir)
	}

	dstDir := filepath.Join(outDir, "commands")
	if t.CommandNamespace != "" {
		dstDir = filepath.Join(dstDir, t.CommandNamespace)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read commands source %s: %w", srcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return fmt.Errorf("read command %s: %w", e.Name(), err)
		}
		toml, err := toGeminiCommandTOML(rewriteGeminiAgentIDs(rewriteGeminiDelegationSyntax(string(body), knownRoles), knownRoles))
		if err != nil {
			return fmt.Errorf("encode antigravity command %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".md") + ".toml"
		dst := filepath.Join(dstDir, name)
		if err := os.WriteFile(dst, []byte(toml), 0o644); err != nil {
			return fmt.Errorf("write antigravity command %s: %w", dst, err)
		}
	}
	return nil
}

func emitAntigravityAgents(m *Manifest, repoRoot, outDir string, t Target) error {
	if m.AssetSources.Agents == "" {
		return nil
	}
	srcDir := filepath.Join(repoRoot, m.AssetSources.Agents)
	info, err := os.Stat(srcDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat agents source %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("agents source %s is not a directory", srcDir)
	}

	dstDir := filepath.Join(outDir, "agents")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	knownRoles := codexKnownAgentRoles(m, repoRoot)

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read agents source %s: %w", srcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		raw, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read agent %s: %w", e.Name(), err)
		}
		translated, err := translateAntigravityAgentFrontmatter(e.Name(), raw)
		if err != nil {
			return fmt.Errorf("translate agent %s: %w", e.Name(), err)
		}
		dst := filepath.Join(dstDir, e.Name())
		body := rewriteGeminiAgentIDs(rewriteGeminiDelegationSyntax(string(translated), knownRoles), knownRoles)
		if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write translated agent %s: %w", dst, err)
		}
	}
	return nil
}

func translateAntigravityAgentFrontmatter(filename string, raw []byte) ([]byte, error) {
	claudeFM, body, hasFM, err := parseAgentFrontmatter(raw)
	if !hasFM {
		return raw, nil
	}
	if err != nil {
		return nil, err
	}
	claudeFM = filterAgentFrontmatter(filename, "antigravity", claudeFM)

	gFM := map[string]any{}

	if v, ok := claudeFM["name"].(string); ok {
		gFM["name"] = v
	}
	if v, ok := claudeFM["description"].(string); ok {
		gFM["description"] = v
	}
	if v, ok := claudeFM["model"].(string); ok {
		gFM["model"] = mapGeminiAgentModel(v)
	}
	if v, ok := claudeFM["maxTurns"].(int); ok {
		gFM["maxTurns"] = v
	}
	if v, ok := claudeFM["timeout_mins"].(int); ok {
		gFM["timeout_mins"] = v
	}

	if toolsRaw, ok := claudeFM["tools"]; ok {
		claudeTools := toStringSlice(toolsRaw)
		agyTools := make([]string, 0, len(claudeTools))
		for _, ct := range claudeTools {
			gt, known := claudeToGeminiTool[ct]
			if !known {
				log.Printf("antigravity_agents: agent %s: unknown tool %q dropped (not in claudeToGeminiTool map)", filename, ct)
				continue
			}
			// agy renamed some Gemini-CLI tools (verified live vs agy v1.0.8:
			// run_shell_command -> run_command). Apply the rename so agent
			// frontmatter references tools agy actually exposes.
			if at, renamed := geminiToAntigravityTool[gt]; renamed {
				gt = at
			}
			agyTools = append(agyTools, gt)
		}
		if len(agyTools) == 0 {
			agyTools = []string{"*"}
		}
		gFM["tools"] = agyTools
	}

	fmBytes, err := marshalAgentFrontmatterForHarness(gFM, "antigravity")
	if err != nil {
		return nil, fmt.Errorf("marshal antigravity frontmatter: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fmBytes)
	buf.WriteString("---\n")
	buf.Write(body)
	return buf.Bytes(), nil
}

// antigravityEventNames maps wipnote's canonical hook event names to the event
// names the Antigravity CLI (agy) understands. Verified live against agy v1.0.8
// (feat-c08b20a6): only these five register command handlers; AfterModel has no
// agy equivalent and is dropped. agy does NOT understand the Gemini-CLI names
// (BeforeTool/AfterTool/BeforeAgent/AfterAgent/SessionStart) — emitting those
// produces zero handlers.
//
//	SessionStart  -> (no working command-hook in agy v1.0.8; dropped)
//	UserPromptSubmit -> PreInvocation   (start of each agent invocation)
//	AfterAgent    -> PostInvocation
//	PreToolUse    -> PreToolUse
//	PostToolUse   -> PostToolUse
//	Stop          -> Stop
//
// Derived from hookEventNameSpecs rather than hand-maintained: that table is the
// single source of truth for which names each harness dispatches, and a second
// copy here could drift out of agreement with the build-time gate.
var antigravityEventNames = harnessHookEventNames["antigravity"]

// writeAntigravityHooks emits hooks.json in the schema agy's jsonhook parser
// requires (verified live against agy v1.0.8): a top-level map of named hooks,
// each an object with an "enabled" flag and per-event arrays of matcher groups:
//
//	{ "wipnote": { "enabled": true,
//	    "PreToolUse": [ { "matcher": "*", "hooks": [ {"type":"command","command":"..."} ] } ],
//	    ... } }
//
// This differs from the Claude-Code schema ({ "hooks": { "<Event>": [...] } }),
// which agy rejects with "cannot unmarshal array into JSONHookSpec". agy also
// strict-decodes the spec, so only "enabled" + known event keys may appear.
func writeAntigravityHooks(m *Manifest, path string) error {
	events := map[string][]claudeMatcherGroup{}
	order := []string{}

	for _, e := range m.Hooks.Events {
		if !e.AppliesTo("antigravity") {
			continue
		}
		agyEvent, ok := antigravityEventNames[e.Name]
		if !ok {
			// No agy equivalent (e.g. SessionStart/AfterModel) — skip rather
			// than emit a dead or parse-breaking key.
			continue
		}

		cmd := e.Command
		if cmd == "" {
			cmd = "WIPNOTE_AGENT_ID=antigravity WIPNOTE_AGENT_TYPE=antigravity wipnote hook " + e.Handler
		}
		cmd = strings.ReplaceAll(cmd, "$GEMINI_EXTENSION_DIR", "${extensionPath}")

		matcher := e.Matcher
		if matcher == "" {
			matcher = "*"
		}

		group := claudeMatcherGroup{
			Matcher: matcher,
			Hooks: []claudeHookEntry{{
				Type:    "command",
				Command: cmd,
				Timeout: e.Timeout,
			}},
		}
		if _, seen := events[agyEvent]; !seen {
			order = append(order, agyEvent)
		}
		events[agyEvent] = append(events[agyEvent], group)
	}

	if len(order) == 0 {
		return nil
	}
	return writeJSON(path, antigravityHooksJSON{name: "wipnote", enabled: true, keys: order, values: events})
}

// antigravityHooksJSON renders the agy hooks.json shape:
//
//	{ "<name>": { "enabled": <bool>, "<Event>": [matcher groups], ... } }
//
// with the inner event keys serialized in the supplied order for stable diffs.
type antigravityHooksJSON struct {
	name    string
	enabled bool
	keys    []string
	values  map[string][]claudeMatcherGroup
}

func (a antigravityHooksJSON) MarshalJSON() ([]byte, error) {
	var buf []byte
	buf = append(buf, '{')
	nameB, err := jsonMarshal(a.name)
	if err != nil {
		return nil, err
	}
	buf = append(buf, nameB...)
	buf = append(buf, ':', '{')
	enabledB, err := jsonMarshal(a.enabled)
	if err != nil {
		return nil, err
	}
	buf = append(buf, `"enabled":`...)
	buf = append(buf, enabledB...)
	for _, k := range a.keys {
		buf = append(buf, ',')
		kb, err := jsonMarshal(k)
		if err != nil {
			return nil, err
		}
		vb, err := jsonMarshal(a.values[k])
		if err != nil {
			return nil, err
		}
		buf = append(buf, kb...)
		buf = append(buf, ':')
		buf = append(buf, vb...)
	}
	buf = append(buf, '}', '}')
	return buf, nil
}
