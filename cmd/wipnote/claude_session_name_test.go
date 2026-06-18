package main

import (
	"strings"
	"testing"
)

// TestResolveSessionName covers the four decision branches of resolveSessionName.
// Each case mirrors a real launch scenario that previously had inline guard logic.
func TestResolveSessionName(t *testing.T) {
	const fakeRoot = "/fake/project"

	tests := []struct {
		name        string
		sessionName string
		resumeID    string
		continue_   bool
		// wantEmpty signals we expect an empty string (no synthesized name).
		// wantPrefix signals the result must start with that prefix (synthesized name).
		wantEmpty  bool
		wantPrefix string
	}{
		{
			name:       "continue=true suppresses synthesis (the bug we fixed)",
			sessionName: "",
			resumeID:   "",
			continue_:  true,
			wantEmpty:  true,
		},
		{
			name:       "explicit resumeID suppresses synthesis",
			sessionName: "",
			resumeID:   "sess-abc123",
			continue_:  false,
			wantEmpty:  true,
		},
		{
			name:        "explicit sessionName kept as-is, not overwritten",
			sessionName: "my-custom-name",
			resumeID:    "",
			continue_:   false,
			wantPrefix:  "my-custom-name",
		},
		{
			name:       "fresh launch synthesizes a name",
			sessionName: "",
			resumeID:   "",
			continue_:  false,
			// defaultSessionName(fakeRoot) = "project-<timestamp>"; slug of "project" is "project"
			wantPrefix: "project-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSessionName(tt.sessionName, tt.resumeID, tt.continue_, fakeRoot)
			if tt.wantEmpty {
				if got != "" {
					t.Fatalf("resolveSessionName() = %q, want empty string", got)
				}
				return
			}
			if tt.wantPrefix != "" {
				if !strings.HasPrefix(got, tt.wantPrefix) {
					t.Fatalf("resolveSessionName() = %q, want prefix %q", got, tt.wantPrefix)
				}
			}
		})
	}
}
