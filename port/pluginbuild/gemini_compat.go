package pluginbuild

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Gemini-CLI compatibility helpers.
//
// The Gemini CLI harness target was retired (feat-02f25a24) — wipnote no longer
// generates a gemini-extension tree or launches `gemini`. These helpers are
// RETAINED, however, because the Antigravity CLI adapter (antigravity.go) is a
// Gemini-CLI descendant that reuses the exact same translation vocabulary: agy
// speaks the Gemini-CLI tool names (read_file/replace/run_shell_command/…), the
// same command-TOML wrapping, the same "wipnote:role" delegation-ID rewriting,
// and the same model-shorthand → full-ID mapping. Renaming them would obscure
// that lineage, so they keep their Gemini names and live here as shared code.
//
// Nothing in this file emits a gemini target; the gemini adapter and its
// sub-emitters were removed with the target. See antigravity.go for the sole
// remaining caller set.

// claudeToGeminiTool maps Claude Code tool names to their Gemini-CLI equivalents
// (which agy inherits). Tools absent from this map are dropped with a warning by
// the caller — the Gemini-CLI tool namespace does not recognise Claude-specific
// tool names, so passing them through would cause extension load failures or
// silent no-ops.
//
// Browser MCP tools (mcp__claude-in-chrome__*) have no direct analogue and are
// intentionally omitted — they remain dropped with a warning.
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

// mapGeminiAgentModel translates Claude Code model shorthand to the full Gemini
// model identifiers required by Gemini-CLI-derived subagent frontmatter (agy).
// Shorthand aliases like "flash" or "flash-lite" are NOT documented as valid
// model values in the Gemini-CLI subagent schema
// (https://github.com/google-gemini/gemini-cli/blob/main/docs/core/subagents.md
// — "Supported frontmatter fields" table uses full IDs such as
// "gemini-3-flash-preview"). Full IDs are required.
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

// tripleTickForbidden is the sequence that cannot appear inside a TOML multiline
// literal string. If source markdown ever contains it, toGeminiCommandTOML returns
// an error instead of producing silently-broken TOML.
const tripleTickForbidden = "'''"

// toGeminiCommandTOML wraps a markdown body as a TOML `prompt` value using a
// multiline literal string (”'…”'). Literal strings pass all content through
// verbatim — backslashes, \n sequences, and \uXXXX escapes are NOT interpreted
// by the TOML parser, so the prompt round-trips byte-for-byte from source
// markdown to parsed TOML value. Consumed by the Antigravity command emitter.
//
// The only restriction of TOML literal strings is that they cannot contain the
// sequence ”'. If the source contains that sequence, this function returns an
// error — the caller should add an escape for that file or switch to a TOML
// writer library rather than silently producing unparseable output.
func toGeminiCommandTOML(mdBody string) (string, error) {
	if strings.Contains(mdBody, tripleTickForbidden) {
		return "", fmt.Errorf("command body contains %q which cannot appear inside a TOML multiline literal string", tripleTickForbidden)
	}
	return "prompt = '''\n" + mdBody + "\n'''\n", nil
}

// copyAssetTreeGemini copies srcDir into dstDir recursively, rewriting
// "wipnote:role" delegation IDs to bare role names in text files. Retained for
// the Antigravity asset emitter, which shares the Gemini-CLI command/agent-ID
// conventions. Codex-override files are skipped. Missing sources are no-ops.
func copyAssetTreeGemini(srcDir, dstDir string, knownRoles map[string]struct{}) error {
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
		return copyFileGemini(path, target, knownRoles)
	})
}

func copyFileGemini(src, dst string, knownRoles map[string]struct{}) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		return os.WriteFile(dst, data, 0o644)
	}
	translated := rewriteGeminiAgentIDs(rewriteGeminiDelegationSyntax(string(data), knownRoles), knownRoles)
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(translated), info.Mode().Perm())
}

// rewriteGeminiAgentIDs strips the "wipnote:" prefix from known agent-role IDs
// so delegation references resolve under the Gemini-CLI/agy agent namespace.
// Unknown roles keep the prefix untouched.
func rewriteGeminiAgentIDs(content string, knownRoles map[string]struct{}) string {
	const prefix = "wipnote:"
	if !strings.Contains(content, prefix) {
		return content
	}
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
		rest = rest[idx+len(prefix):]
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
			buf.WriteString(role)
		} else {
			buf.WriteString(prefix)
			buf.WriteString(role)
		}
	}
	return buf.String()
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
