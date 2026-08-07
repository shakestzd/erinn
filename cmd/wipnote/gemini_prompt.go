package main

import "strings"

// gemini_prompt.go retains the Gemini-CLI orchestrator-prompt renderer after the
// Gemini CLI harness target was retired (feat-02f25a24). wipnote no longer
// launches `gemini`, but the Antigravity launcher (antigravity_launch.go) still
// calls renderGeminiSystemPrompt: agy is a Gemini-CLI descendant that speaks the
// same tool vocabulary (read_file/replace/run_shell_command/…), so the prompt is
// rendered with Gemini-CLI tool names and then agy-renamed (run_shell_command →
// run_command) by the caller. The name is kept to reflect that lineage.

// geminiLaunchMode is retained for renderGeminiSystemPrompt's signature. Only the
// default mode survives now that the `wipnote gemini` launcher (with its dev /
// continue / init modes) is gone.
type geminiLaunchMode string

const geminiLaunchModeDefault geminiLaunchMode = "default"

// renderGeminiSystemPrompt pre-processes the embedded orchestrator prompt by
// substituting tool-name placeholders with literal Gemini-CLI tool names, then
// appends the shared research-routing disposition exactly once. Source of truth
// for the disposition: cmd/wipnote/prompts/research-routing.md.
func renderGeminiSystemPrompt(content string, _ geminiLaunchMode) string {
	toolNameReplacements := map[string]string{
		"${read_file_ToolName}":         "read_file",
		"${replace_ToolName}":           "replace",
		"${write_file_ToolName}":        "write_file",
		"${grep_search_ToolName}":       "grep_search",
		"${glob_ToolName}":              "glob",
		"${run_shell_command_ToolName}": "run_shell_command",
		"${web_fetch_ToolName}":         "web_fetch",
		"${google_web_search_ToolName}": "google_web_search",
	}

	result := content
	for placeholder, literal := range toolNameReplacements {
		result = strings.ReplaceAll(result, placeholder, literal)
	}

	result = strings.TrimSpace(result) + "\n\n" + strings.TrimSpace(researchRoutingContent) + "\n"
	return result
}
