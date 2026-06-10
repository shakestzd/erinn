package pluginbuild

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func init() { Register(codexAdapter{}) }

// codexAdapter emits the Codex CLI marketplace tree. Layout:
//
//	<outDir>/.agents/plugins/marketplace.json
//	<outDir>/.agents/plugins/wipnote/.codex-plugin/plugin.json
//	<outDir>/.agents/plugins/wipnote/hooks.json
//	<outDir>/.agents/plugins/wipnote/.mcp.json
//	<outDir>/.agents/plugins/wipnote/{commands,agents,skills,templates,static,config}/
//
// Codex 0.121.0+ registers plugins exclusively via `codex marketplace add <path>`.
// Codex expects the marketplace root to contain `.agents/plugins/marketplace.json`
// and plugin content to live under `.agents/plugins/<plugin-name>/`.
//
// Codex hook event names differ from Claude in a few places (TaskStarted,
// TaskComplete, TurnAborted) — the manifest's `targets` field controls which
// events are emitted here. Business logic stays in `wipnote hook <handler>`
// so the Codex plugin is a thin wrapper just like the Claude one.
type codexAdapter struct{}

func (codexAdapter) Name() string { return "codex" }

// codexOwnedSubtrees lists paths relative to the marketplace outDir that
// build-ports fully regenerates. These are cleaned before each emit to prevent
// stale files accumulating. marketplace.json is regenerated separately.
// Hand-maintained files (README.md) outside these paths are never touched.
// The owned subtree is narrowed to the plugin's own directory to avoid
// deleting sibling plugins under .agents/plugins/.
var codexOwnedSubtrees = []string{".agents/plugins/wipnote"}

func (c codexAdapter) Emit(m *Manifest, repoRoot, outDir string) error {
	target, ok := m.Targets[c.Name()]
	if !ok {
		return fmt.Errorf("manifest has no target %q", c.Name())
	}

	// Determine where plugin content lives inside the marketplace tree.
	// Codex expects: <outDir>/.agents/plugins/<plugin-name>/
	pluginSubdir := target.PluginSubdir
	if pluginSubdir == "" {
		pluginSubdir = ".agents/plugins/wipnote"
	}
	pluginDir := filepath.Join(outDir, pluginSubdir)

	// Pre-clean owned subtrees so renamed/deleted source files don't leave
	// stale output files behind. marketplace.json is inside the owned subtree.
	if err := cleanOwnedSubtrees(outDir, codexOwnedSubtrees); err != nil {
		return fmt.Errorf("codex pre-clean: %w", err)
	}

	mktPath := filepath.Join(outDir, ".agents", "plugins", "marketplace.json")
	// source.path is relative to the directory containing marketplace.json.
	mktDir := filepath.Dir(mktPath)
	rel, err := filepath.Rel(mktDir, pluginDir)
	if err != nil {
		return fmt.Errorf("compute relative path for source.path: %w", err)
	}
	sourcePath := "./" + filepath.ToSlash(rel)
	if err := writeCodexMarketplace(m, target, mktPath, sourcePath); err != nil {
		return err
	}

	// Write per-plugin files under plugins/wipnote/.
	if err := writeCodexManifest(m, filepath.Join(pluginDir, target.ManifestPath)); err != nil {
		return err
	}
	if err := writeCodexHooks(m, filepath.Join(pluginDir, target.HooksPath)); err != nil {
		return err
	}
	if target.MCPPath != "" {
		if err := ensureCodexMCP(filepath.Join(pluginDir, target.MCPPath)); err != nil {
			return err
		}
	}
	knownRoles := codexKnownAgentRoles(m, repoRoot)
	if err := copyCodexAssets(m, repoRoot, pluginDir, knownRoles); err != nil {
		return err
	}
	return emitCodexAgents(m, repoRoot, pluginDir, knownRoles)
}

// codexMarketplaceJSON is the schema for marketplace.json at the root of a
// Codex marketplace directory. Codex reads this file on `codex marketplace add`.
type codexMarketplaceJSON struct {
	Name      string                `json:"name"`
	Interface codexMktInterfaceJSON `json:"interface"`
	Plugins   []codexMktPluginJSON  `json:"plugins"`
}

type codexMktInterfaceJSON struct {
	DisplayName string `json:"displayName"`
}

type codexMktPluginJSON struct {
	Name     string             `json:"name"`
	Source   codexMktSourceJSON `json:"source"`
	Policy   codexMktPolicyJSON `json:"policy"`
	Category string             `json:"category,omitempty"`
}

type codexMktSourceJSON struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

type codexMktPolicyJSON struct {
	Installation   string `json:"installation"`
	Authentication string `json:"authentication"`
}

// writeCodexMarketplace writes marketplace.json to path. sourcePath is the
// relative path from the marketplace.json directory to the plugin directory.
func writeCodexMarketplace(m *Manifest, target Target, path, sourcePath string) error {
	name := target.MarketplaceName
	if name == "" {
		name = m.Name
	}
	displayName := target.MarketplaceDisplayName
	if displayName == "" {
		displayName = m.Name
	}
	category := target.MarketplaceCategory
	if category == "" {
		category = m.Category
	}

	return writeJSON(path, codexMarketplaceJSON{
		Name:      name,
		Interface: codexMktInterfaceJSON{DisplayName: displayName},
		Plugins: []codexMktPluginJSON{
			{
				Name: m.Name,
				Source: codexMktSourceJSON{
					Source: "local",
					Path:   sourcePath,
				},
				Policy: codexMktPolicyJSON{
					Installation:   "AVAILABLE",
					Authentication: "ON_INSTALL",
				},
				Category: category,
			},
		},
	})
}

// codexPluginJSON mirrors the Codex plugin manifest schema. The top-level
// shape is similar to Claude's, plus an `interface` block Codex uses for
// install-surface metadata.
type codexPluginJSON struct {
	Name        string             `json:"name"`
	Version     string             `json:"version"`
	Description string             `json:"description"`
	Author      codexAuthorJSON    `json:"author"`
	Homepage    string             `json:"homepage,omitempty"`
	Repository  string             `json:"repository,omitempty"`
	License     string             `json:"license,omitempty"`
	Keywords    []string           `json:"keywords,omitempty"`
	Skills      string             `json:"skills,omitempty"`
	Hooks       string             `json:"hooks,omitempty"`
	MCPServers  string             `json:"mcpServers,omitempty"`
	Interface   codexInterfaceJSON `json:"interface"`
}

type codexAuthorJSON struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

type codexInterfaceJSON struct {
	DisplayName      string `json:"displayName"`
	ShortDescription string `json:"shortDescription"`
	LongDescription  string `json:"longDescription,omitempty"`
	DeveloperName    string `json:"developerName"`
	Category         string `json:"category,omitempty"`
}

func writeCodexManifest(m *Manifest, path string) error {
	return writeJSON(path, codexPluginJSON{
		Name:        m.Name,
		Version:     m.Version,
		Description: m.Description,
		Author: codexAuthorJSON{
			Name:  m.Author.Name,
			Email: m.Author.Email,
			URL:   m.Author.URL,
		},
		Homepage:   m.Homepage,
		Repository: m.Repository,
		License:    m.License,
		Keywords:   m.Keywords,
		Skills:     "./skills/",
		Hooks:      "./hooks.json",
		MCPServers: "./.mcp.json",
		Interface: codexInterfaceJSON{
			DisplayName:      "wipnote",
			ShortDescription: m.Description,
			DeveloperName:    m.Author.Name,
			Category:         m.Category,
		},
	})
}

// Codex hooks.json schema matches Claude's structure so shared matchers work.
// Different events are supported, not a different schema.
func writeCodexHooks(m *Manifest, path string) error {
	hooks := map[string][]claudeMatcherGroup{}
	order := []string{}

	for _, e := range m.Hooks.Events {
		if !e.AppliesTo("codex") {
			continue
		}
		cmd := e.Command
		if cmd == "" {
			cmd = "wipnote hook " + e.Handler
		}
		group := claudeMatcherGroup{
			Matcher: e.Matcher,
			Hooks: []claudeHookEntry{{
				Type:    "command",
				Command: cmd,
				Timeout: e.Timeout,
			}},
		}
		if _, seen := hooks[e.Name]; !seen {
			order = append(order, e.Name)
		}
		hooks[e.Name] = append(hooks[e.Name], group)
	}
	return writeJSON(path, orderedHookMap{keys: order, values: hooks})
}

// ensureCodexMCP writes a stub .mcp.json if none exists. wipnote doesn't
// currently expose an MCP server, but the file is part of the Codex plugin
// contract and future MCP integrations land here without schema churn.
func ensureCodexMCP(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return writeJSON(path, map[string]any{"mcpServers": map[string]any{}})
}

func copyCodexAssets(m *Manifest, repoRoot, outDir string, knownRoles map[string]struct{}) error {
	pairs := []struct{ src, dst string }{
		{m.AssetSources.Commands, "commands"},
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
		if err := copyAssetTreeCodex(src, dst, knownRoles); err != nil {
			return err
		}
	}
	return nil
}

// codexKnownAgentRoles returns the set of role names derived from the agents
// source directory. Each role is the bare name without prefix (e.g.
// "patch-coder", "feature-coder"). This set is used to build the colon-to-
// hyphen rewrite table so only declared agents are translated; unknown IDs are
// left untouched and surface a build warning.
func codexKnownAgentRoles(m *Manifest, repoRoot string) map[string]struct{} {
	roles := map[string]struct{}{}
	if m.AssetSources.Agents == "" {
		return roles
	}
	srcDir := filepath.Join(repoRoot, m.AssetSources.Agents)
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return roles // missing agents dir is tolerated
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		role := strings.TrimSuffix(e.Name(), ".md")
		roles[role] = struct{}{}
	}
	return roles
}

// copyAssetTreeCodex walks srcDir recursively and writes every file into dstDir,
// applying codexRewriteAgentIDs to text files. Binary files are copied verbatim.
func copyAssetTreeCodex(srcDir, dstDir string, knownRoles map[string]struct{}) error {
	info, err := os.Stat(srcDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("asset source %s is not a directory", srcDir)
	}
	same, err := samePath(srcDir, dstDir)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && isCodexOverrideFile(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileCodex(path, target, knownRoles)
	})
}

// copyFileCodex copies src to dst; for text files it applies the agent ID
// rewrite. Binary files (detected by a NUL byte in the first 512 bytes) are
// copied verbatim.
func copyFileCodex(src, dst string, knownRoles map[string]struct{}) error {
	resolvedSrc, err := codexAssetSource(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(resolvedSrc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Binary detection: if the first 512 bytes contain a NUL byte, copy verbatim.
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		return os.WriteFile(dst, data, 0o644)
	}
	translated := rewriteCodexDelegationSyntax(codexRewriteAgentIDs(string(data), knownRoles), knownRoles)
	info, err := os.Stat(resolvedSrc)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(translated), info.Mode().Perm())
}

func codexAssetSource(path string) (string, error) {
	override := codexOverridePath(path)
	if override == "" {
		return path, nil
	}
	if _, err := os.Stat(override); err == nil {
		return override, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

func codexOverridePath(path string) string {
	dir := filepath.Dir(path)
	file := filepath.Base(path)
	ext := filepath.Ext(file)
	if ext == "" {
		return ""
	}
	base := strings.TrimSuffix(file, ext)
	return filepath.Join(dir, base+".codex"+ext)
}

func isCodexOverrideFile(name string) bool {
	ext := filepath.Ext(name)
	if ext == "" {
		return false
	}
	base := strings.TrimSuffix(name, ext)
	return strings.HasSuffix(base, ".codex")
}

// codexRewriteAgentIDs rewrites every occurrence of wipnote:<role> → wipnote-<role>
// for roles that are in the knownRoles set. Occurrences of wipnote:<role> where
// <role> is NOT in knownRoles are left unchanged; a build warning is emitted once
// per unknown role so the team can detect stale references.
//
// Codex registers agents under the name "wipnote-<role>" (hyphen form) with
// nickname_candidates = ["<role>"]. Claude Code uses the colon form
// "wipnote:<role>". This translation is applied only to the Codex output tree;
// the shared source assets are never modified.
func codexRewriteAgentIDs(content string, knownRoles map[string]struct{}) string {
	const prefix = "wipnote:"
	if !strings.Contains(content, prefix) {
		return content
	}

	warned := map[string]bool{}
	var buf strings.Builder
	buf.Grow(len(content))
	rest := content
	for {
		idx := strings.Index(rest, prefix)
		if idx < 0 {
			buf.WriteString(rest)
			break
		}
		buf.WriteString(rest[:idx])
		rest = rest[idx+len(prefix):] // rest now starts after "wipnote:"

		// Extract the role name: runs of word chars and hyphens.
		roleEnd := 0
		for roleEnd < len(rest) {
			c := rest[roleEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_' {
				roleEnd++
			} else {
				break
			}
		}
		role := rest[:roleEnd]
		rest = rest[roleEnd:]

		if _, known := knownRoles[role]; known {
			buf.WriteString("wipnote-")
			buf.WriteString(role)
		} else {
			// Unknown role: leave the original colon-form intact.
			if role != "" && !warned[role] {
				log.Printf("codex_assets: unknown agent role %q in wipnote:%s — leaving unchanged", role, role)
				warned[role] = true
			}
			buf.WriteString(prefix)
			buf.WriteString(role)
		}
	}
	return buf.String()
}

func rewriteCodexDelegationSyntax(content string, knownRoles map[string]struct{}) string {
	return rewriteDelegationSyntax(content, delegationTargetCodex, knownRoles)
}

func rewriteGeminiDelegationSyntax(content string, knownRoles map[string]struct{}) string {
	return rewriteDelegationSyntax(content, delegationTargetGemini, knownRoles)
}

type delegationTarget string

const (
	delegationTargetCodex  delegationTarget = "codex"
	delegationTargetGemini delegationTarget = "gemini"
)

func rewriteDelegationSyntax(content string, target delegationTarget, knownRoles map[string]struct{}) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inDelegationBlock := false
	var block []string
	var blockIndent string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if isDelegationBlockStart(trimmed) {
			inDelegationBlock = true
			block = []string{line}
			blockIndent = indent
			out = append(out, indent+delegationBlockHeader(target))
			if target != delegationTargetCodex {
				if field := delegationFieldFromOpener(trimmed); field != "" {
					out = append(out, indent+"    "+rewriteDelegationField(field, target))
				}
			}
			continue
		}
		if inDelegationBlock && isDelegationBlockEnd(trimmed) {
			inDelegationBlock = false
			if target == delegationTargetCodex {
				block = append(block, line)
				out = append(out, blockIndent+codexDelegationBlockProse(strings.Join(block, "\n"), knownRoles))
				block = nil
			}
			continue
		}
		if inDelegationBlock {
			block = append(block, line)
			if target != delegationTargetCodex {
				out = append(out, rewriteDelegationField(line, target))
			}
			continue
		}
		out = append(out, rewriteInlineDelegationCall(line, target, knownRoles))
	}
	return strings.Join(out, "\n")
}

func isDelegationBlockStart(trimmed string) bool {
	if !(strings.HasPrefix(trimmed, "Task(") || strings.HasPrefix(trimmed, "Agent(")) {
		return false
	}
	return !strings.Contains(trimmed, ")")
}

func isDelegationBlockEnd(trimmed string) bool {
	return trimmed == ")" || trimmed == "),"
}

func delegationFieldFromOpener(trimmed string) string {
	idx := strings.Index(trimmed, "(")
	if idx < 0 {
		return ""
	}
	field := strings.TrimSpace(trimmed[idx+1:])
	if field == "" {
		return ""
	}
	field = strings.TrimSuffix(field, ")")
	field = strings.TrimSpace(field)
	return field
}

func delegationBlockHeader(target delegationTarget) string {
	switch target {
	case delegationTargetCodex:
		return "Call spawn_agent with:"
	case delegationTargetGemini:
		return "Use Gemini agent invocation with:"
	default:
		return "Delegate with:"
	}
}

func rewriteDelegationField(line string, target delegationTarget) string {
	line = strings.ReplaceAll(line, "prompt=", "message=")
	for _, key := range []string{"subagent_type=", "agent="} {
		idx := strings.Index(line, key+"\"")
		if idx < 0 {
			continue
		}
		valueStart := idx + len(key) + 1
		valueEnd := strings.Index(line[valueStart:], "\"")
		if valueEnd < 0 {
			return line
		}
		valueEnd += valueStart
		role := line[valueStart:valueEnd]
		var replacement string
		switch target {
		case delegationTargetCodex:
			if isWipnoteRole(role) {
				replacement = "agent_type=\"" + codexAgentType(role) + "\""
			} else {
				replacement = "workflow=\"" + role + "\""
			}
		case delegationTargetGemini:
			if isWipnoteRole(role) {
				replacement = "agent=\"" + geminiAgentInvocation(role) + "\""
			} else {
				replacement = "workflow=\"" + role + "\""
			}
		default:
			replacement = "agent=\"" + role + "\""
		}
		return line[:idx] + replacement + line[valueEnd+1:]
	}
	switch target {
	case delegationTargetCodex:
		line = strings.ReplaceAll(line, "subagent_type=", "agent_type=")
	case delegationTargetGemini:
		line = strings.ReplaceAll(line, "subagent_type=", "agent=")
	}
	return line
}

func rewriteInlineDelegationCall(line string, target delegationTarget, knownRoles map[string]struct{}) string {
	for {
		taskIdx := strings.Index(line, "Task(")
		agentIdx := strings.Index(line, "Agent(")
		idx := firstNonNegative(taskIdx, agentIdx)
		if idx < 0 {
			return line
		}
		endRel := strings.Index(line[idx:], ")")
		if endRel < 0 {
			return line
		}
		end := idx + endRel + 1
		replacement := delegationProse(line[idx:end], target, knownRoles)
		line = line[:idx] + replacement + line[end:]
	}
}

func delegationProse(call string, target delegationTarget, knownRoles map[string]struct{}) string {
	role := delegationRole(call)
	switch target {
	case delegationTargetCodex:
		if role == "" {
			return "call spawn_agent with the appropriate agent_type"
		}
		if !isKnownWipnoteRole(role, knownRoles) {
			return "use the " + delegationWorkflowName(role) + " workflow described here"
		}
		return "call spawn_agent with agent_type \"" + codexAgentType(role) + "\""
	case delegationTargetGemini:
		if role == "" {
			return "use the appropriate Gemini agent invocation"
		}
		if !isKnownWipnoteRole(role, knownRoles) {
			return "use the " + delegationWorkflowName(role) + " workflow described here"
		}
		return "use " + geminiAgentInvocation(role)
	default:
		if role == "" {
			return "delegate to the appropriate agent"
		}
		return "delegate to " + role
	}
}

func codexDelegationBlockProse(call string, knownRoles map[string]struct{}) string {
	role := delegationRole(call)
	details := delegationDetails(call)
	if !isKnownWipnoteRole(role, knownRoles) {
		if role == "" {
			if details != "" {
				return "Call spawn_agent with: " + ensureTerminalPeriod(details)
			}
			return "Use the appropriate workflow described here."
		}
		if details != "" {
			return "Use the " + delegationWorkflowName(role) + " workflow described here with: " + ensureTerminalPeriod(details)
		}
		return "Use the " + delegationWorkflowName(role) + " workflow described here."
	}
	if details == "" {
		return "Call spawn_agent with agent_type \"" + codexAgentType(role) + "\"."
	}
	return "Call spawn_agent with agent_type \"" + codexAgentType(role) + "\" and message containing: " + ensureTerminalPeriod(details)
}

func ensureTerminalPeriod(s string) string {
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

func delegationDetails(call string) string {
	lines := strings.Split(call, "\n")
	parts := []string{}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if i == 0 {
			trimmed = delegationFieldFromOpener(trimmed)
		}
		if trimmed == "" || isDelegationBlockEnd(trimmed) {
			continue
		}
		if strings.HasPrefix(trimmed, "prompt=") || strings.HasPrefix(trimmed, "message=") {
			detail, next := delegationMultilineFieldDetail(lines, i, trimmed)
			if detail != "" {
				parts = append(parts, detail)
			}
			i = next
			continue
		}
		if strings.HasPrefix(trimmed, "subagent_type=") || strings.HasPrefix(trimmed, "agent=") {
			if detail := delegationAgentFieldDetail(trimmed); detail != "" {
				parts = append(parts, detail)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "description=") ||
			strings.HasPrefix(trimmed, "isolation=") ||
			strings.HasPrefix(trimmed, "run_in_background=") {
			parts = append(parts, strings.TrimRight(trimmed, ","))
		}
	}
	return strings.Join(parts, "; ")
}

func delegationAgentFieldDetail(trimmed string) string {
	trimmed = strings.TrimRight(trimmed, ",")
	for _, prefix := range []string{"subagent_type=", "agent="} {
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			return ""
		}
		return "agent_type=" + value
	}
	return ""
}

func delegationMultilineFieldDetail(lines []string, start int, first string) (string, int) {
	keyEnd := strings.Index(first, "=")
	if keyEnd < 0 {
		return strings.TrimRight(first, ","), start
	}
	key := first[:keyEnd]
	value := strings.TrimSpace(first[keyEnd+1:])
	if !strings.HasPrefix(value, `"""`) {
		return strings.TrimRight(first, ","), start
	}
	value = strings.TrimPrefix(value, `"""`)
	parts := []string{}
	if done, cleaned := trimTripleQuoteEnd(value); done {
		if cleaned != "" {
			parts = append(parts, cleaned)
		}
		return key + "=" + strings.Join(parts, "\n"), start
	}
	if value != "" {
		parts = append(parts, value)
	}
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if done, cleaned := trimTripleQuoteEnd(trimmed); done {
			if cleaned != "" {
				parts = append(parts, cleaned)
			}
			return key + "=" + strings.Join(parts, "\n"), i
		}
		parts = append(parts, strings.TrimRight(trimmed, ","))
	}
	return key + "=" + strings.Join(parts, "\n"), len(lines) - 1
}

func trimTripleQuoteEnd(value string) (bool, string) {
	trimmed := strings.TrimSpace(value)
	for _, suffix := range []string{`""",`, `"""`} {
		if strings.HasSuffix(trimmed, suffix) {
			cleaned := strings.TrimSpace(strings.TrimSuffix(trimmed, suffix))
			return true, strings.TrimRight(cleaned, ",")
		}
	}
	return false, value
}

func isWipnoteRole(role string) bool {
	return strings.HasPrefix(role, "wipnote:") || strings.HasPrefix(role, "wipnote-")
}

func isKnownWipnoteRole(role string, knownRoles map[string]struct{}) bool {
	if !isWipnoteRole(role) {
		return false
	}
	role = strings.TrimPrefix(role, "wipnote:")
	role = strings.TrimPrefix(role, "wipnote-")
	_, ok := knownRoles[role]
	return ok
}

func delegationWorkflowName(role string) string {
	role = strings.TrimPrefix(role, "wipnote:")
	role = strings.TrimPrefix(role, "wipnote-")
	return role
}

func codexAgentType(role string) string {
	role = strings.TrimPrefix(role, "wipnote:")
	if strings.HasPrefix(role, "wipnote-") {
		return role
	}
	return "wipnote-" + role
}

func geminiAgentInvocation(role string) string {
	role = strings.TrimPrefix(role, "wipnote:")
	role = strings.TrimPrefix(role, "wipnote-")
	return "@" + role
}

func firstNonNegative(a, b int) int {
	switch {
	case a < 0:
		return b
	case b < 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

func delegationRole(call string) string {
	for _, key := range []string{`subagent_type="`, `agent="`} {
		idx := strings.Index(call, key)
		if idx < 0 {
			continue
		}
		rest := call[idx+len(key):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			return ""
		}
		return rest[:end]
	}
	return ""
}
