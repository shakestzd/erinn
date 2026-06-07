// Package provenance captures and renders attribution metadata for sessions
// and work items so downstream consumers can tell which AI harness, model, and
// role created each artifact.
//
// Four canonical attributes are tracked everywhere:
//
//	created-by-agent        the harness (claude-code, codex, gemini)
//	created-by-model        the model identity (e.g. claude-opus-4-7)
//	created-by-role         the agent role (e.g. architect-coder)
//	created-by-cli-version  the wipnote binary version string
//
// The package is intentionally lightweight (no internal imports) so both
// CLI commands and hook handlers can use it without import cycles.
package provenance

import (
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Provenance records who/what produced an artifact (work item, session,
// step). Empty fields render as the literal string "unknown" in human output;
// in HTML they are simply omitted.
type Provenance struct {
	Agent      string // harness: claude-code, codex, gemini, ...
	Model      string // model identity: claude-opus-4-7, gpt-5-mini, ...
	Role       string // agent role: architect-coder, feature-coder, ...
	CLIVersion string // wipnote binary version string
}

// cliVersion is set once at startup by SetCLIVersion. It is a plain package
// var (no mutex) because writes happen exactly once before any reads.
var cliVersion = "dev"

// SetCLIVersion records the wipnote binary's compiled version. The main
// package calls this in PersistentPreRun so every Detect() call sees it.
func SetCLIVersion(v string) {
	if v == "" {
		return
	}
	cliVersion = v
}

// CLIVersion returns the recorded wipnote binary version (default: "dev").
func CLIVersion() string {
	return cliVersion
}

// Detect reads provenance from the process environment using the standard
// wipnote env-var contract:
//
//	Agent  ← WIPNOTE_AGENT_ID
//	Model  ← WIPNOTE_MODEL, then CLAUDE_MODEL
//	Role   ← WIPNOTE_AGENT_TYPE
//
// CLIVersion is always taken from SetCLIVersion. Empty env vars yield empty
// fields — callers decide how to render absence.
func Detect() Provenance {
	return Provenance{
		Agent:      os.Getenv("WIPNOTE_AGENT_ID"),
		Model:      firstNonEmpty(os.Getenv("WIPNOTE_MODEL"), os.Getenv("CLAUDE_MODEL")),
		Role:       os.Getenv("WIPNOTE_AGENT_TYPE"),
		CLIVersion: cliVersion,
	}
}

// Merge returns a new Provenance where each field of override falls back to
// the corresponding field of base when override's field is empty. Use it to
// inherit unspecified fields from a session/parent record.
func (p Provenance) Merge(base Provenance) Provenance {
	return Provenance{
		Agent:      firstNonEmpty(p.Agent, base.Agent),
		Model:      firstNonEmpty(p.Model, base.Model),
		Role:       firstNonEmpty(p.Role, base.Role),
		CLIVersion: firstNonEmpty(p.CLIVersion, base.CLIVersion),
	}
}

// IsEmpty returns true when no provenance fields are populated.
func (p Provenance) IsEmpty() bool {
	return p.Agent == "" && p.Model == "" && p.Role == "" && p.CLIVersion == ""
}

// HumanString returns a "/"-joined string suitable for `wipnote show`.
// Missing fields render as "unknown" so the format stays positional.
//
// Example: "claude-code / claude-opus-4-7 / architect-coder / v1.2.3"
func (p Provenance) HumanString() string {
	parts := []string{
		fallback(p.Agent),
		fallback(p.Model),
		fallback(p.Role),
		fallback(p.CLIVersion),
	}
	return strings.Join(parts, " / ")
}

// HTMLAttrs returns the four data-* attributes (with leading space) ready to
// inline into an HTML opening tag. Empty fields are omitted entirely so old
// items round-trip cleanly through htmlparse.
//
// Example output:
//
//	` data-created-by-agent="claude-code" data-created-by-model="claude-opus-4-7"`
func (p Provenance) HTMLAttrs() string {
	var b strings.Builder
	writeAttr(&b, "data-created-by-agent", p.Agent)
	writeAttr(&b, "data-created-by-model", p.Model)
	writeAttr(&b, "data-created-by-role", p.Role)
	writeAttr(&b, "data-created-by-cli-version", p.CLIVersion)
	return b.String()
}

// FromActiveSession reads provenance from the session HTML in projectDir
// belonging to sessionID. Returns an empty Provenance when the file does
// not exist or has no provenance attributes recorded — never an error.
//
// This is the inheritance source used by `wipnote feature create` so that
// a feature created mid-session picks up the session's identity by default.
func FromActiveSession(projectDir, sessionID string) Provenance {
	if projectDir == "" || sessionID == "" {
		return Provenance{}
	}
	path := filepath.Join(projectDir, ".wipnote", "sessions", sessionID+".html")
	data, err := os.ReadFile(path)
	if err != nil {
		return Provenance{}
	}
	return parseAttrs(string(data))
}

// articleAttrRe matches the four provenance attributes inside any open tag.
// The patterns are deliberately permissive (non-greedy, single-line) because
// the session/work-item articles each render attributes on multiple lines.
var (
	agentAttrRe   = regexp.MustCompile(`data-created-by-agent="([^"]*)"`)
	modelAttrRe   = regexp.MustCompile(`data-created-by-model="([^"]*)"`)
	roleAttrRe    = regexp.MustCompile(`data-created-by-role="([^"]*)"`)
	versionAttrRe = regexp.MustCompile(`data-created-by-cli-version="([^"]*)"`)
)

// parseAttrs extracts provenance from an HTML fragment. Used both by
// FromActiveSession and (indirectly) by tests asserting round-tripping.
func parseAttrs(htmlText string) Provenance {
	return Provenance{
		Agent:      firstSubmatch(agentAttrRe, htmlText),
		Model:      firstSubmatch(modelAttrRe, htmlText),
		Role:       firstSubmatch(roleAttrRe, htmlText),
		CLIVersion: firstSubmatch(versionAttrRe, htmlText),
	}
}

// ParseHTML is the exported parser companion of HTMLAttrs, kept around so
// htmlparse and other callers can read provenance without re-deriving the
// regex set.
func ParseHTML(htmlText string) Provenance {
	return parseAttrs(htmlText)
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func writeAttr(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	b.WriteByte(' ')
	b.WriteString(name)
	b.WriteString(`="`)
	b.WriteString(html.EscapeString(value))
	b.WriteByte('"')
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func fallback(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
