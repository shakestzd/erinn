package pluginbuild

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// tripleTickForbidden is the sequence that cannot appear inside a TOML multiline
// literal string. If source markdown ever contains it, toGeminiCommandTOML returns
// an error instead of producing silently-broken TOML.
const tripleTickForbidden = "'''"

// init registers this phase's sub-emitter. Order within geminiSubEmitters is
// deterministic by filename collation across gemini_*.go files, so assets/
// commands/hooks never race even though each lives in its own file.
func init() {
	geminiSubEmitters = append(geminiSubEmitters, emitGeminiCommands)
}

// emitGeminiCommands translates every plugin/commands/*.md file into a Gemini
// TOML command under <outDir>/commands/<namespace>/<name>.toml. Gemini loads
// .toml commands where the `prompt` key is the full markdown body; the
// namespace segment means the slash-command resolves to /<namespace>:<name>
// (e.g. /wipnote:feature-start). When the target declares no namespace the
// files land directly under commands/ — a degenerate case kept for symmetry.
func emitGeminiCommands(m *Manifest, repoRoot, outDir string, t Target) error {
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
			return fmt.Errorf("encode gemini command %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".md") + ".toml"
		dst := filepath.Join(dstDir, name)
		if err := os.WriteFile(dst, []byte(toml), 0o644); err != nil {
			return fmt.Errorf("write gemini command %s: %w", dst, err)
		}
	}
	return nil
}

// toGeminiCommandTOML wraps a markdown body as a TOML `prompt` value using a
// multiline literal string (”'…”'). Literal strings pass all content through
// verbatim — backslashes, \n sequences, and \uXXXX escapes are NOT interpreted
// by the TOML parser, so the prompt round-trips byte-for-byte from source
// markdown to parsed TOML value.
//
// The only restriction of TOML literal strings is that they cannot contain the
// sequence ”'  If the source contains that sequence, this function returns an
// error — the caller should add an escape for that file or switch to a TOML
// writer library rather than silently producing unparseable output.
func toGeminiCommandTOML(mdBody string) (string, error) {
	if strings.Contains(mdBody, tripleTickForbidden) {
		return "", fmt.Errorf("command body contains %q which cannot appear inside a TOML multiline literal string", tripleTickForbidden)
	}
	return "prompt = '''\n" + mdBody + "\n'''\n", nil
}
