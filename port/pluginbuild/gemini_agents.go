package pluginbuild

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// claudeToGeminiTool maps Claude Code tool names to their Gemini CLI equivalents.
// Tools absent from this map are dropped with a warning logged — Gemini does not
// recognise Claude-specific tool names, so passing them through would cause
// extension load failures or silent no-ops.
//
// Browser MCP tools (mcp__claude-in-chrome__*) have no direct Gemini analogue
// and are intentionally omitted — they remain dropped with a warning.
var claudeToGeminiTool = map[string]string{
	"Read":      "read_file",
	"Edit":      "replace",
	"Write":     "write_file",
	"Grep":      "grep_search",
	"Glob":      "glob",
	"Bash":      "run_shell_command",
	"WebSearch": "google_web_search",
	"WebFetch":  "web_fetch",
}

func init() {
	geminiSubEmitters = append(geminiSubEmitters, emitGeminiAgents)
}

// emitGeminiAgents translates every plugin/agents/*.md file from Claude
// frontmatter to Gemini frontmatter and writes the result into
// <outDir>/agents/. Verbatim copy of agents is deliberately skipped in
// emitGeminiAssets — this emitter owns the agents subtree for Gemini.
//
// Translation rules applied per agent:
//   - DROP:    color, skills, initialPrompt
//   - RENAME:  maxTurns → max_turns
//   - KEEP:    name, description, model, timeout_mins
//   - TRANSLATE: tools (see claudeToGeminiTool map; unknowns dropped with warning)
//   - FALLBACK: empty tools after translation → ["*"] (Gemini wildcard for all tools)
func emitGeminiAgents(m *Manifest, repoRoot, outDir string, t Target) error {
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
		translated, err := translateAgentFrontmatter(e.Name(), raw)
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

// translateAgentFrontmatter parses the YAML frontmatter in raw, applies the
// Claude-to-Gemini translation rules, and returns the file with translated
// frontmatter. The markdown body after the closing --- delimiter is preserved
// verbatim.
func translateAgentFrontmatter(filename string, raw []byte) ([]byte, error) {
	claudeFM, body, hasFM, err := parseAgentFrontmatter(raw)
	if !hasFM {
		// No frontmatter — pass through unchanged. Gemini will read the body as
		// a bare markdown agent without additional metadata.
		return raw, nil
	}
	if err != nil {
		return nil, err
	}
	claudeFM = filterAgentFrontmatter(filename, "gemini", claudeFM)

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

	// Translate tools list: map known Claude tools to Gemini equivalents;
	// drop unknowns with a warning.
	if toolsRaw, ok := claudeFM["tools"]; ok {
		claudeTools := toStringSlice(toolsRaw)
		geminiTools := make([]string, 0, len(claudeTools))
		for _, ct := range claudeTools {
			if gt, known := claudeToGeminiTool[ct]; known {
				geminiTools = append(geminiTools, gt)
			} else {
				log.Printf("gemini_agents: agent %s: unknown tool %q dropped (not in claudeToGeminiTool map)", filename, ct)
			}
		}
		if len(geminiTools) == 0 {
			// Fallback to wildcard: after translation the tools list is empty,
			// which would leave the agent with no capabilities. Gemini's "*"
			// wildcard grants access to all tools, preserving the agent's intent
			// rather than silently restricting it.
			geminiTools = []string{"*"}
		}
		gFM["tools"] = geminiTools
	}

	// Marshal the translated frontmatter back to YAML, applying any output
	// name translations from the shared field matrix (for example maxTurns
	// becomes max_turns for Gemini).
	fmBytes, err := marshalAgentFrontmatterForHarness(gFM, "gemini")
	if err != nil {
		return nil, fmt.Errorf("marshal gemini frontmatter: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fmBytes)
	buf.WriteString("---\n")
	buf.Write(body)
	return buf.Bytes(), nil
}

// mapGeminiAgentModel translates Claude Code model shorthand to the full Gemini
// model identifiers required by Gemini CLI's subagent frontmatter. Shorthand
// aliases like "flash" or "flash-lite" are NOT documented as valid model values
// in Gemini CLI's subagent schema (https://github.com/google-gemini/gemini-cli/
// blob/main/docs/core/subagents.md — "Supported frontmatter fields" table uses
// full IDs such as "gemini-3-flash-preview"). Full IDs are required.
func mapGeminiAgentModel(model string) string {
	switch strings.TrimSpace(model) {
	case "haiku":
		// Fast/cheap tier → gemini-2.5-flash-lite (https://ai.google.dev/gemini-api/docs/models/gemini-2.5-flash-lite)
		return "gemini-2.5-flash-lite"
	case "sonnet":
		// Balanced tier → gemini-3-flash-preview (https://ai.google.dev/gemini-api/docs/models/gemini-3-flash-preview)
		return "gemini-3-flash-preview"
	case "opus":
		// Deep-reasoning tier → gemini-3.1-pro-preview (https://ai.google.dev/gemini-api/docs/models/gemini-3.1-pro-preview)
		return "gemini-3.1-pro-preview"
	default:
		return model
	}
}

// splitFrontmatter splits a markdown file at the YAML frontmatter delimiters.
// Returns (frontmatter, body, true) when delimiters are found, or ("", raw, false)
// when the file has no frontmatter.
func splitFrontmatter(raw []byte) (fm string, body []byte, ok bool) {
	if !bytes.HasPrefix(raw, []byte("---\n")) {
		return "", raw, false
	}
	rest := raw[4:] // skip opening ---\n
	idx := bytes.Index(rest, []byte("\n---\n"))
	if idx < 0 {
		return "", raw, false
	}
	fm = string(rest[:idx])
	body = rest[idx+5:] // skip \n---\n
	return fm, body, true
}

// toStringSlice converts an interface{} that may be []interface{} or []string
// into a plain []string. Other types return nil.
func toStringSlice(v interface{}) []string {
	switch x := v.(type) {
	case []interface{}:
		result := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case []string:
		return x
	}
	return nil
}
