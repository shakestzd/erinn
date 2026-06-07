package hooks

import "testing"

func TestClassifyPrompt_Implementation(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		{"implement a new REST endpoint for users", true},
		{"create a function that validates email", true},
		{"build the dashboard component", true},
		{"fix bug in login flow", true},
		{"add feature for export", true},
		{"what is the weather today", false},
	}
	for _, tt := range tests {
		intent := ClassifyPrompt(tt.prompt)
		if intent.IsImplementation != tt.want {
			t.Errorf("ClassifyPrompt(%q).IsImplementation = %v, want %v",
				tt.prompt, intent.IsImplementation, tt.want)
		}
	}
}

func TestClassifyPrompt_Investigation(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		{"investigate why the tests are failing", true},
		{"research best practices for caching", true},
		{"explore the codebase structure", true},
		{"look into the performance issue", true},
		{"deploy the app", false},
	}
	for _, tt := range tests {
		intent := ClassifyPrompt(tt.prompt)
		if intent.IsInvestigation != tt.want {
			t.Errorf("ClassifyPrompt(%q).IsInvestigation = %v, want %v",
				tt.prompt, intent.IsInvestigation, tt.want)
		}
	}
}

func TestClassifyPrompt_BugReport(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		{"there's a bug in the parser", true},
		{"the login is broken", true},
		{"error when submitting the form", true},
		{"the build fails on CI", true},
		{"add a new button to the UI", false},
	}
	for _, tt := range tests {
		intent := ClassifyPrompt(tt.prompt)
		if intent.IsBugReport != tt.want {
			t.Errorf("ClassifyPrompt(%q).IsBugReport = %v, want %v",
				tt.prompt, intent.IsBugReport, tt.want)
		}
	}
}

func TestClassifyPrompt_Continuation(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		{"continue", true},
		{"ok", true},
		{"yes", true},
		{"go ahead", true},
		{"proceed with the implementation", true},
		{"implement the feature now", false},
	}
	for _, tt := range tests {
		intent := ClassifyPrompt(tt.prompt)
		if intent.IsContinuation != tt.want {
			t.Errorf("ClassifyPrompt(%q).IsContinuation = %v, want %v",
				tt.prompt, intent.IsContinuation, tt.want)
		}
	}
}

func TestClassifyPrompt_CIGS_Exploration(t *testing.T) {
	intent := ClassifyPrompt("search for all error handling code and review it")
	if !intent.InvolvesExploration {
		t.Error("expected InvolvesExploration = true for search+review prompt")
	}
}

func TestClassifyPrompt_CIGS_CodeChanges(t *testing.T) {
	intent := ClassifyPrompt("refactor the database layer and rewrite the queries")
	if !intent.InvolvesCodeChanges {
		t.Error("expected InvolvesCodeChanges = true for refactor+rewrite prompt")
	}
}

func TestClassifyPrompt_CIGS_Git(t *testing.T) {
	intent := ClassifyPrompt("commit the changes and push to main")
	if !intent.InvolvesGit {
		t.Error("expected InvolvesGit = true for commit+push prompt")
	}
}

func TestClassifyPrompt_Confidence(t *testing.T) {
	// A clear implementation request should have high confidence.
	intent := ClassifyPrompt("implement a new feature for user authentication")
	if intent.Confidence < 0.7 {
		t.Errorf("expected Confidence >= 0.7, got %f", intent.Confidence)
	}

	// A continuation should have 0.9 confidence.
	intent = ClassifyPrompt("ok")
	if intent.Confidence != 0.9 {
		t.Errorf("expected Confidence = 0.9 for continuation, got %f", intent.Confidence)
	}
}

func TestGenerateGuidance_ImplementationNoActive(t *testing.T) {
	intent := PromptIntent{IsImplementation: true, Confidence: 0.8}
	guidance := GenerateGuidance(intent, "", "", "")
	if guidance == "" {
		t.Fatal("expected non-empty guidance for implementation without active work item")
	}
	if !containsStr(guidance, "no active work item") {
		t.Errorf("guidance should mention no active work item, got: %s", guidance)
	}
}

func TestGenerateGuidance_ImplementationDuringSpike(t *testing.T) {
	intent := PromptIntent{IsImplementation: true, Confidence: 0.8}
	guidance := GenerateGuidance(intent, "spk-001", "spike", "")
	if !containsStr(guidance, "spike") {
		t.Errorf("guidance should warn about spike, got: %s", guidance)
	}
	if !containsStr(guidance, "NOT implementation") {
		t.Errorf("guidance should say spikes are not for implementation, got: %s", guidance)
	}
}

func TestGenerateGuidance_ImplementationWithFeature(t *testing.T) {
	intent := PromptIntent{IsImplementation: true, Confidence: 0.8}
	guidance := GenerateGuidance(intent, "feat-001", "feature", "")
	if !containsStr(guidance, "Delegate") {
		t.Errorf("guidance should mention delegation, got: %s", guidance)
	}
}

func TestGenerateGuidance_ContinuationWithActive(t *testing.T) {
	intent := PromptIntent{IsContinuation: true, Confidence: 0.9}
	guidance := GenerateGuidance(intent, "feat-001", "feature", "ACTIVE: feat-001")
	// Should only contain the attribution block, no extra directive.
	if containsStr(guidance, "ORCHESTRATOR DIRECTIVE") {
		t.Errorf("continuation with active work should not have directive, got: %s", guidance)
	}
}

func TestGenerateGuidance_WithAttribution(t *testing.T) {
	intent := PromptIntent{IsImplementation: true, Confidence: 0.8}
	attribution := "ACTIVE: feat-001\nOPEN: feat feat-001(ip)"
	guidance := GenerateGuidance(intent, "feat-001", "feature", attribution)
	if !containsStr(guidance, "ACTIVE: feat-001") {
		t.Errorf("guidance should include attribution block, got: %s", guidance)
	}
}

func TestGenerateGuidance_ExplorationCIGS(t *testing.T) {
	intent := PromptIntent{InvolvesExploration: true, Confidence: 0.6}
	guidance := GenerateGuidance(intent, "feat-001", "feature", "")
	if !containsStr(guidance, "Exploration detected") {
		t.Errorf("guidance should include CIGS exploration directive, got: %s", guidance)
	}
}

func TestGenerateGuidance_EmptyForNoSignals(t *testing.T) {
	intent := PromptIntent{}
	guidance := GenerateGuidance(intent, "feat-001", "feature", "")
	if guidance != "" {
		t.Errorf("expected empty guidance for no signals, got: %s", guidance)
	}
}

// containsStr is a test helper (avoids import cycle with strings).
func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
