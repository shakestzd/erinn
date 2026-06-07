package ingest

import "strings"

// IsSystemMessage reports whether content is an internal/runtime-generated
// message that should not be classified as an end-user prompt.
func IsSystemMessage(content string) bool {
	s := strings.TrimSpace(content)
	if s == "" {
		return false
	}

	lower := strings.ToLower(s)

	for _, prefix := range []string{
		"This session is being continued",
		"[Request interrupted",
		"[Session resumed",
		"<task-notification>",
		"↩ resumed: background task",
	} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}

	if strings.HasPrefix(lower, "you are a code reviewer.") &&
		(strings.Contains(lower, "review the code changes shown below") ||
			strings.Contains(lower, "return only the findings") ||
			strings.Contains(lower, "return only the final")) {
		return true
	}

	return false
}
