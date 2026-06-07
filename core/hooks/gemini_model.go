package hooks

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/shakestzd/wipnote/core/models"
)

// AfterModel handles the Gemini CLI AfterModel hook event. This event fires
// after each LLM turn and carries per-turn token counts, finish reason, and
// (when GEMINI_TELEMETRY_TRACES=true) the model's response text.
//
// The payload has Gemini-specific nested fields (llm_request, llm_response)
// that are parsed by parseGeminiEvent into CloudEvent.LLMRequest / LLMResponse.
func AfterModel(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		sessionID = EnvSessionID(event.SessionID)
	}
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	// Extract model from llm_request.model (overrides the top-level model field).
	model := event.Model
	if req := event.LLMRequest; req != nil {
		if m, ok := req["model"].(string); ok && m != "" {
			model = m
		}
	}

	// Extract finish reason and token count from llm_response.
	var finishReason string
	var totalTokens int64
	var responseText string

	if resp := event.LLMResponse; resp != nil {
		// llm_response.candidates[0].finishReason
		if candidates, ok := resp["candidates"].([]any); ok && len(candidates) > 0 {
			if cand, ok := candidates[0].(map[string]any); ok {
				finishReason, _ = cand["finishReason"].(string)
				// llm_response.candidates[0].content.parts joined as response text
				if content, ok := cand["content"].(map[string]any); ok {
					if parts, ok := content["parts"].([]any); ok {
						var sb strings.Builder
						for _, p := range parts {
							switch v := p.(type) {
							case string:
								sb.WriteString(v)
							case map[string]any:
								if text, ok := v["text"].(string); ok {
									sb.WriteString(text)
								}
							}
						}
						responseText = sb.String()
					}
				}
			}
		}
		// llm_response.usageMetadata.totalTokenCount
		if usage, ok := resp["usageMetadata"].(map[string]any); ok {
			switch v := usage["totalTokenCount"].(type) {
			case float64:
				totalTokens = int64(v)
			case int64:
				totalTokens = v
			case int:
				totalTokens = int64(v)
			}
		}
	}

	// Store response text as an assistant_text otel_signals row.
	if responseText != "" {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		insertAssistantTextSignalFromHookPayload(database, projectDir, sessionID, event, responseText, "after_model")
	}

	// Skip intermediate streaming chunks. Per geminicli.com docs, AfterModel
	// fires "for every chunk" — only the final chunk carries finishReason and
	// usageMetadata.totalTokenCount. Recording every chunk produces ~65% noise
	// in agent_events (bug-55a17fc2). The durable fix is to migrate hooks.json
	// from AfterModel to AfterAgent (the docs-canonical turn-level hook) once
	// gemini-cli issue #15468 (AfterAgent premature-fire) is confirmed fixed
	// in shipped versions; until then this guard keeps the timeline clean.
	if finishReason == "" && totalTokens == 0 {
		return &HookResult{Continue: true}, nil
	}

	// Record a lightweight agent_event so model+token info appears in the timeline.
	summary := buildAfterModelSummary(model, totalTokens, finishReason)
	return recordSimpleEvent(models.EventCheckPoint, "AfterModel", summary, "recorded", event, database)
}

// buildAfterModelSummary constructs a human-readable summary for the AfterModel event.
func buildAfterModelSummary(model string, totalTokens int64, finishReason string) string {
	parts := []string{"AfterModel"}
	if model != "" {
		parts = append(parts, model)
	}
	if totalTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d tokens", totalTokens))
	}
	if finishReason != "" {
		parts = append(parts, finishReason)
	}
	return strings.Join(parts, ", ")
}
