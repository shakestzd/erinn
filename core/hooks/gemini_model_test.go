package hooks

import (
	"testing"
)

// TestAfterModel_SkipsIntermediateStreamingChunks verifies the guard at
// gemini_model.go:82: AfterModel hooks that lack finishReason AND totalTokens
// are intermediate streaming chunks (per geminicli.com docs) and must not
// produce agent_events rows. Only the final chunk (carrying finishReason+
// usageMetadata.totalTokenCount) is recorded. bug-55a17fc2.
func TestAfterModel_SkipsIntermediateStreamingChunks(t *testing.T) {
	td := setupTestDB(t)

	// Intermediate chunk: no finishReason, no usageMetadata.
	intermediate := &CloudEvent{
		SessionID: "test-sess",
		Model:     "gemini-3-flash",
		LLMResponse: map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{map[string]any{"text": "partial chunk text"}},
					},
				},
			},
		},
	}
	if _, err := AfterModel(intermediate, td.DB); err != nil {
		t.Fatalf("AfterModel intermediate: %v", err)
	}

	// Final chunk: carries finishReason and totalTokenCount.
	final := &CloudEvent{
		SessionID: "test-sess",
		Model:     "gemini-3-flash",
		LLMResponse: map[string]any{
			"candidates": []any{
				map[string]any{
					"finishReason": "STOP",
					"content": map[string]any{
						"parts": []any{map[string]any{"text": "final chunk text"}},
					},
				},
			},
			"usageMetadata": map[string]any{
				"totalTokenCount": float64(1234),
			},
		},
	}
	if _, err := AfterModel(final, td.DB); err != nil {
		t.Fatalf("AfterModel final: %v", err)
	}

	var count int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND tool_name = 'AfterModel'`,
		"test-sess",
	).Scan(&count); err != nil {
		t.Fatalf("count AfterModel events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 AfterModel row (final chunk only); got %d", count)
	}
}
