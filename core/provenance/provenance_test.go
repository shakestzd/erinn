package provenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectReadsEnvVars(t *testing.T) {
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_MODEL", "claude-opus-4-7")
	t.Setenv("CLAUDE_MODEL", "")
	t.Setenv("WIPNOTE_AGENT_TYPE", "architect-coder")
	SetCLIVersion("1.2.3")

	p := Detect()
	if p.Agent != "claude-code" {
		t.Errorf("Agent = %q, want claude-code", p.Agent)
	}
	if p.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q, want claude-opus-4-7", p.Model)
	}
	if p.Role != "architect-coder" {
		t.Errorf("Role = %q, want architect-coder", p.Role)
	}
	if p.CLIVersion != "1.2.3" {
		t.Errorf("CLIVersion = %q, want 1.2.3", p.CLIVersion)
	}
}

func TestDetectFallsBackToClaudeModel(t *testing.T) {
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_MODEL", "")
	t.Setenv("CLAUDE_MODEL", "claude-sonnet-4-5")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	SetCLIVersion("1.2.3")

	p := Detect()
	if p.Model != "claude-sonnet-4-5" {
		t.Errorf("Model = %q, want claude-sonnet-4-5", p.Model)
	}
}

func TestDetectEmptyWhenNoEnv(t *testing.T) {
	t.Setenv("WIPNOTE_AGENT_ID", "")
	t.Setenv("WIPNOTE_MODEL", "")
	t.Setenv("CLAUDE_MODEL", "")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")

	p := Detect()
	if p.Agent != "" || p.Model != "" || p.Role != "" {
		t.Errorf("expected empty agent/model/role, got %+v", p)
	}
}

func TestMergeFillsEmptyFromBase(t *testing.T) {
	base := Provenance{
		Agent:      "claude-code",
		Model:      "claude-opus-4-7",
		Role:       "architect-coder",
		CLIVersion: "1.0.0",
	}
	override := Provenance{
		Model: "claude-sonnet-4-5",
	}
	merged := override.Merge(base)
	if merged.Agent != "claude-code" {
		t.Errorf("Agent = %q, want inherited claude-code", merged.Agent)
	}
	if merged.Model != "claude-sonnet-4-5" {
		t.Errorf("Model = %q, want override claude-sonnet-4-5", merged.Model)
	}
	if merged.Role != "architect-coder" {
		t.Errorf("Role = %q, want inherited architect-coder", merged.Role)
	}
	if merged.CLIVersion != "1.0.0" {
		t.Errorf("CLIVersion = %q, want inherited 1.0.0", merged.CLIVersion)
	}
}

func TestHumanStringRendersUnknownForMissing(t *testing.T) {
	p := Provenance{Agent: "claude-code"}
	got := p.HumanString()
	want := "claude-code / unknown / unknown / unknown"
	if got != want {
		t.Errorf("HumanString() = %q, want %q", got, want)
	}
}

func TestHTMLAttrsOmitsEmptyFields(t *testing.T) {
	p := Provenance{Agent: "claude-code", CLIVersion: "1.2.3"}
	got := p.HTMLAttrs()
	if !strings.Contains(got, `data-created-by-agent="claude-code"`) {
		t.Errorf("missing agent attr in %q", got)
	}
	if !strings.Contains(got, `data-created-by-cli-version="1.2.3"`) {
		t.Errorf("missing version attr in %q", got)
	}
	if strings.Contains(got, "data-created-by-model") {
		t.Errorf("empty model should be omitted, got %q", got)
	}
	if strings.Contains(got, "data-created-by-role") {
		t.Errorf("empty role should be omitted, got %q", got)
	}
}

func TestHTMLAttrsEscapesValue(t *testing.T) {
	p := Provenance{Agent: `evil"<script>`}
	got := p.HTMLAttrs()
	if strings.Contains(got, `<script>`) {
		t.Errorf("should escape unsafe chars, got %q", got)
	}
}

func TestParseHTMLRoundTripsHTMLAttrs(t *testing.T) {
	original := Provenance{
		Agent:      "codex",
		Model:      "gpt-5-mini",
		Role:       "feature-coder",
		CLIVersion: "0.99.0",
	}
	htmlText := `<article id="x"` + original.HTMLAttrs() + `>body</article>`
	parsed := ParseHTML(htmlText)
	if parsed != original {
		t.Errorf("round-trip mismatch:\nwant %+v\ngot  %+v", original, parsed)
	}
}

func TestFromActiveSessionReadsHTMLAttrs(t *testing.T) {
	projectDir := t.TempDir()
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := `<!DOCTYPE html>
<article id="sess-abc" data-type="session"
         data-created-by-agent="claude-code"
         data-created-by-model="claude-opus-4-7"
         data-created-by-role="architect-coder"
         data-created-by-cli-version="1.2.3">
</article>`
	if err := os.WriteFile(filepath.Join(sessDir, "sess-abc.html"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	p := FromActiveSession(projectDir, "sess-abc")
	if p.Agent != "claude-code" || p.Model != "claude-opus-4-7" ||
		p.Role != "architect-coder" || p.CLIVersion != "1.2.3" {
		t.Errorf("FromActiveSession = %+v", p)
	}
}

func TestFromActiveSessionEmptyWhenMissing(t *testing.T) {
	projectDir := t.TempDir()
	p := FromActiveSession(projectDir, "no-such-session")
	if !p.IsEmpty() {
		t.Errorf("expected empty provenance for missing file, got %+v", p)
	}
}

func TestFromActiveSessionEmptyWhenNoAttrs(t *testing.T) {
	projectDir := t.TempDir()
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := `<article id="sess-old" data-type="session"></article>`
	if err := os.WriteFile(filepath.Join(sessDir, "sess-old.html"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	p := FromActiveSession(projectDir, "sess-old")
	if !p.IsEmpty() {
		t.Errorf("expected empty provenance for legacy file, got %+v", p)
	}
}
