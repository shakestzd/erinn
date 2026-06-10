package pluginbuild

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// AgentAsset is the canonical markdown/frontmatter agent asset used by the
// shared plugin source tree. The source format follows Claude conventions; each
// target adapter maps only fields its host clearly supports.
type AgentAsset struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	Model         string   `yaml:"model"`
	Color         string   `yaml:"color"`
	Tools         []string `yaml:"tools"`
	Disallowed    []string `yaml:"disallowedTools"`
	MaxTurns      int      `yaml:"maxTurns"`
	Skills        []string `yaml:"skills"`
	InitialPrompt string   `yaml:"initialPrompt"`
}

type codexAgentTOML struct {
	Name                  string   `toml:"name"`
	Description           string   `toml:"description"`
	Model                 string   `toml:"model,omitempty"`
	ModelReasoningEffort  string   `toml:"model_reasoning_effort,omitempty"`
	SandboxMode           string   `toml:"sandbox_mode,omitempty"`
	NicknameCandidates    []string `toml:"nickname_candidates,omitempty"`
	DeveloperInstructions string   `toml:"developer_instructions"`
}

func emitCodexAgents(m *Manifest, repoRoot, outDir string, knownRoles map[string]struct{}) error {
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

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read agents source %s: %w", srcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return fmt.Errorf("read agent %s: %w", e.Name(), err)
		}
		agent, err := translateCodexAgent(e.Name(), raw)
		if err != nil {
			return fmt.Errorf("translate agent %s: %w", e.Name(), err)
		}
		// Translate colon-form agent IDs in developer_instructions so agent
		// bodies that reference wipnote:<role> work correctly in Codex.
		agent.DeveloperInstructions = codexRewriteAgentIDs(agent.DeveloperInstructions, knownRoles)
		out, err := toml.Marshal(agent)
		if err != nil {
			return fmt.Errorf("marshal codex agent %s: %w", e.Name(), err)
		}
		dst := filepath.Join(dstDir, agent.Name+".toml")
		if err := os.WriteFile(dst, out, 0o644); err != nil {
			return fmt.Errorf("write codex agent %s: %w", dst, err)
		}
	}
	return nil
}

func translateCodexAgent(filename string, raw []byte) (codexAgentTOML, error) {
	fm, body, hasFM, err := parseAgentFrontmatter(raw)
	if err != nil {
		return codexAgentTOML{}, err
	}
	if !hasFM {
		name := strings.TrimSuffix(filename, filepath.Ext(filename))
		return codexAgentTOML{
			Name:                  codexAgentName(name),
			Description:           "wipnote agent",
			SandboxMode:           "read-only",
			NicknameCandidates:    nicknameCandidates(name),
			DeveloperInstructions: strings.TrimSpace(string(raw)),
		}, nil
	}

	filtered := filterAgentFrontmatter(filename, "codex", fm)
	filteredYAML, err := marshalAgentFrontmatter(filtered)
	if err != nil {
		return codexAgentTOML{}, err
	}
	var asset AgentAsset
	if err := yaml.Unmarshal(filteredYAML, &asset); err != nil {
		return codexAgentTOML{}, fmt.Errorf("parse frontmatter YAML: %w", err)
	}
	if asset.Name == "" {
		asset.Name = strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	if asset.Description == "" {
		return codexAgentTOML{}, fmt.Errorf("description is required")
	}

	model, effort := mapCodexAgentModel(asset.Model)
	return codexAgentTOML{
		Name:                  codexAgentName(asset.Name),
		Description:           asset.Description,
		Model:                 model,
		ModelReasoningEffort:  effort,
		SandboxMode:           codexSandboxMode(asset),
		NicknameCandidates:    nicknameCandidates(asset.Name),
		DeveloperInstructions: codexDeveloperInstructions(body, asset.InitialPrompt),
	}, nil
}

func codexAgentName(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "wipnote-") {
		return name
	}
	return "wipnote-" + name
}

func nicknameCandidates(name string) []string {
	if name == "" {
		return nil
	}
	return []string{name}
}

func codexDeveloperInstructions(body []byte, initialPrompt string) string {
	parts := []string{}
	if s := strings.TrimSpace(string(body)); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(initialPrompt); s != "" {
		parts = append(parts, "## Initial Prompt\n\n"+s)
	}
	return strings.Join(parts, "\n\n")
}

func codexSandboxMode(asset AgentAsset) string {
	for _, tool := range asset.Tools {
		switch tool {
		case "Edit", "Write", "Bash":
			return "workspace-write"
		}
	}
	return "read-only"
}

func mapCodexAgentModel(model string) (string, string) {
	model = strings.TrimSpace(model)
	switch model {
	case "haiku":
		return "gpt-5.4-mini", "low"
	case "sonnet":
		return "gpt-5.4", "medium"
	case "opus":
		return "gpt-5.5", "high"
	}
	if strings.HasPrefix(model, "gpt-") {
		return model, ""
	}
	return "", ""
}
