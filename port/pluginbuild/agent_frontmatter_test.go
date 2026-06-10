package pluginbuild

import (
	"reflect"
	"testing"
)

func TestAgentFrontmatterFieldSpecsDriveDerivedTables(t *testing.T) {
	wantOrder := []string{
		"name",
		"description",
		"model",
		"color",
		"maxTurns",
		"tools",
		"disallowedTools",
		"skills",
		"initialPrompt",
		"memory",
		"timeout_mins",
	}
	if !reflect.DeepEqual(sharedAgentFrontmatterOrder, wantOrder) {
		t.Fatalf("shared order drifted from field specs:\ngot  %#v\nwant %#v", sharedAgentFrontmatterOrder, wantOrder)
	}

	for _, spec := range agentFrontmatterFieldSpecs {
		if _, ok := sharedAgentFrontmatterFields[spec.Name]; !ok {
			t.Fatalf("known field table missing %q from spec", spec.Name)
		}
		for harness := range spec.Harnesses {
			if _, ok := harnessAgentFrontmatterAllowlist[harness][spec.Name]; !ok {
				t.Fatalf("%s allowlist missing %q from spec", harness, spec.Name)
			}
		}
	}
}

func TestAgentFrontmatterFieldSpecsRequireProvenance(t *testing.T) {
	for _, spec := range agentFrontmatterFieldSpecs {
		if spec.DocURL == "" && spec.Provenance == "" {
			t.Fatalf("frontmatter field %q lacks doc URL or provenance note", spec.Name)
		}
	}
}

func TestAgentFrontmatterHarnessSupportAndTranslations(t *testing.T) {
	tests := []struct {
		field   string
		harness string
		want    bool
	}{
		{"color", "claude", true},
		{"color", "codex", false},
		{"timeout_mins", "gemini", true},
		{"timeout_mins", "claude", false},
		{"initialPrompt", "codex", true},
		{"initialPrompt", "gemini", false},
	}
	for _, tt := range tests {
		_, got := harnessAgentFrontmatterAllowlist[tt.harness][tt.field]
		if got != tt.want {
			t.Fatalf("%s support for %s = %t, want %t", tt.harness, tt.field, got, tt.want)
		}
	}

	if got := agentFrontmatterOutputName("maxTurns", "gemini"); got != "max_turns" {
		t.Fatalf("Gemini maxTurns output name = %q, want max_turns", got)
	}
	if got := agentFrontmatterOutputName("maxTurns", "claude"); got != "maxTurns" {
		t.Fatalf("Claude maxTurns output name = %q, want maxTurns", got)
	}
}
