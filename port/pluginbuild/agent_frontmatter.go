package pluginbuild

import (
	"bytes"
	"fmt"
	"log"
	"sort"

	"gopkg.in/yaml.v3"
)

type agentFrontmatterFieldSpec struct {
	Name       string
	Harnesses  map[string]string
	DocURL     string
	Provenance string
}

var agentFrontmatterFieldSpecs = []agentFrontmatterFieldSpec{
	{
		Name:   "name",
		DocURL: "https://code.claude.com/docs/en/sub-agents",
		Harnesses: map[string]string{
			"claude": "name",
			"codex":  "name",
			"gemini": "name",
		},
	},
	{
		Name:   "description",
		DocURL: "https://code.claude.com/docs/en/sub-agents",
		Harnesses: map[string]string{
			"claude": "description",
			"codex":  "description",
			"gemini": "description",
		},
	},
	{
		Name:   "model",
		DocURL: "https://code.claude.com/docs/en/sub-agents",
		Harnesses: map[string]string{
			"claude": "model",
			"codex":  "model",
			"gemini": "model",
		},
	},
	{
		Name:   "color",
		DocURL: "https://code.claude.com/docs/en/sub-agents",
		Harnesses: map[string]string{
			"claude": "color",
		},
	},
	{
		Name:   "maxTurns",
		DocURL: "https://code.claude.com/docs/en/sub-agents",
		Harnesses: map[string]string{
			"claude": "maxTurns",
			"gemini": "max_turns",
		},
	},
	{
		Name:   "tools",
		DocURL: "https://code.claude.com/docs/en/sub-agents",
		Harnesses: map[string]string{
			"claude": "tools",
			"codex":  "tools",
			"gemini": "tools",
		},
	},
	{
		Name:       "disallowedTools",
		DocURL:     "https://code.claude.com/docs/en/sub-agents",
		Provenance: "Recognized as a shared source field so unsupported target output is stripped with a specific warning.",
	},
	{
		Name:       "skills",
		DocURL:     "https://code.claude.com/docs/en/skills",
		Provenance: "Recognized as plugin source metadata and stripped from generated agent frontmatter unless a target explicitly supports it.",
	},
	{
		Name:   "initialPrompt",
		DocURL: "https://github.com/openai/codex",
		Harnesses: map[string]string{
			"codex": "initialPrompt",
		},
	},
	{
		Name:   "memory",
		DocURL: "https://code.claude.com/docs/en/sub-agents",
		Harnesses: map[string]string{
			"claude": "memory",
		},
	},
	{
		Name:   "timeout_mins",
		DocURL: "https://github.com/google-gemini/gemini-cli/blob/main/docs/core/subagents.md",
		Harnesses: map[string]string{
			"gemini": "timeout_mins",
		},
	},
}

var sharedAgentFrontmatterOrder = agentFrontmatterOrder(agentFrontmatterFieldSpecs)
var sharedAgentFrontmatterFields = agentFrontmatterKnownFields(agentFrontmatterFieldSpecs)
var harnessAgentFrontmatterAllowlist = agentFrontmatterAllowlists(agentFrontmatterFieldSpecs)

func parseAgentFrontmatter(raw []byte) (fm map[string]any, body []byte, hasFM bool, err error) {
	fmRaw, body, hasFM := splitFrontmatter(raw)
	if !hasFM {
		return nil, raw, false, nil
	}
	if err := yaml.Unmarshal([]byte(fmRaw), &fm); err != nil {
		return nil, nil, false, fmt.Errorf("parse frontmatter YAML: %w", err)
	}
	if fm == nil {
		fm = map[string]any{}
	}
	return fm, body, true, nil
}

func filterAgentFrontmatter(filename, harness string, fm map[string]any) map[string]any {
	allow, ok := harnessAgentFrontmatterAllowlist[harness]
	if !ok {
		return fm
	}
	filtered := make(map[string]any, len(fm))
	for _, key := range sortedKeys(fm) {
		value := fm[key]
		if _, known := sharedAgentFrontmatterFields[key]; !known {
			log.Printf("pluginbuild: agent %s: frontmatter field %q is not recognized in shared source; omitting from %s output", filename, key, harness)
			continue
		}
		if _, allowed := allow[key]; !allowed {
			log.Printf("pluginbuild: agent %s: frontmatter field %q is unsupported for %s output; omitting", filename, key, harness)
			continue
		}
		filtered[key] = value
	}
	return filtered
}

func marshalAgentFrontmatter(fm map[string]any) ([]byte, error) {
	return marshalAgentFrontmatterForHarness(fm, "")
}

func marshalAgentFrontmatterForHarness(fm map[string]any, harness string) ([]byte, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, key := range sharedAgentFrontmatterOrder {
		value, ok := fm[key]
		if !ok {
			continue
		}
		outputKey := key
		if harness != "" {
			outputKey = agentFrontmatterOutputName(key, harness)
		}
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: outputKey}
		valueNode := &yaml.Node{}
		if err := valueNode.Encode(value); err != nil {
			return nil, fmt.Errorf("encode frontmatter field %q: %w", key, err)
		}
		node.Content = append(node.Content, keyNode, valueNode)
	}
	return yaml.Marshal(node)
}

func renderAgentMarkdown(fm map[string]any, body []byte) ([]byte, error) {
	if len(fm) == 0 {
		return body, nil
	}
	fmBytes, err := marshalAgentFrontmatter(fm)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(fmBytes)
	buf.WriteString("---\n")
	buf.Write(body)
	return buf.Bytes(), nil
}

func agentFrontmatterOrder(specs []agentFrontmatterFieldSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Name)
	}
	return out
}

func agentFrontmatterKnownFields(specs []agentFrontmatterFieldSpec) map[string]struct{} {
	out := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		out[spec.Name] = struct{}{}
	}
	return out
}

func agentFrontmatterAllowlists(specs []agentFrontmatterFieldSpec) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{
		"claude": {},
		"codex":  {},
		"gemini": {},
	}
	for _, spec := range specs {
		for harness := range spec.Harnesses {
			if _, ok := out[harness]; !ok {
				out[harness] = map[string]struct{}{}
			}
			out[harness][spec.Name] = struct{}{}
		}
	}
	return out
}

func agentFrontmatterOutputName(field, harness string) string {
	for _, spec := range agentFrontmatterFieldSpecs {
		if spec.Name != field {
			continue
		}
		if outputName, ok := spec.Harnesses[harness]; ok && outputName != "" {
			return outputName
		}
		return field
	}
	return field
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
