package hooks

import (
	"fmt"
	"strings"
)

// PromptIntent captures the classification of a user prompt.
type PromptIntent struct {
	// Primary intent flags (from classify_prompt)
	IsImplementation bool
	IsInvestigation  bool
	IsBugReport      bool
	IsContinuation   bool

	// CIGS delegation flags (from classify_cigs_intent)
	InvolvesExploration bool
	InvolvesCodeChanges bool
	InvolvesGit         bool

	// Confidence score (0.0–1.0) for the strongest matched category.
	Confidence float64
}

// ---------- keyword lists ----------

// explorationKeywords signal search / read / review activity.
var explorationKeywords = []string{
	"search", "find", "what files", "which files", "where is",
	"locate", "analyze", "examine", "inspect", "review",
	"check", "look at", "show me", "list", "grep",
	"read", "scan", "explore",
}

// codeChangeKeywords signal implementation / modification activity.
var codeChangeKeywords = []string{
	"implement", "fix", "update", "refactor", "change",
	"modify", "edit", "write", "create file", "add code",
	"remove code", "replace", "rewrite", "patch", "add",
}

// gitKeywords signal git operations.
var gitKeywords = []string{
	"commit", "push", "pull", "merge", "branch", "checkout",
	"git add", "git commit", "git push", "git status", "git diff",
	"rebase", "cherry-pick", "stash",
}

// bugKeywords signal bug / error reports.
var bugKeywords = []string{
	"bug", "issue", "error", "problem", "broken",
	"not working", "fails", "crash", "something wrong",
	"doesn't work", "isn't working",
}

// implementationKeywords signal implementation requests.
var implementationKeywords = []string{
	"implement", "create", "build", "develop", "make",
	"add feature", "add function", "add method", "add endpoint",
	"write code", "fix bug", "resolve issue", "patch",
}

// investigationKeywords signal research / exploration intent.
var investigationKeywords = []string{
	"investigate", "research", "explore", "analyze",
	"understand", "find out", "look into",
	"why", "how come", "what causes",
}

// continuationKeywords signal "keep going" type prompts.
var continuationKeywords = []string{
	"continue", "resume", "proceed", "go on", "keep going",
	"next", "where we left off", "from before", "last time",
	"ok", "okay", "yes", "sure", "do it", "go ahead",
}

// ClassifyPrompt analyses a user prompt and returns a PromptIntent
// describing the user's likely intent. Uses fast keyword matching
// (no regex) for hook-level performance.
func ClassifyPrompt(prompt string) PromptIntent {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	intent := PromptIntent{}

	// Short prompts that are pure continuation signals.
	if matchesContinuation(lower) {
		intent.IsContinuation = true
		intent.Confidence = continuationConfidence
		return intent
	}

	// Primary intent classification.
	if countKeywordHits(lower, implementationKeywords) > 0 {
		intent.IsImplementation = true
		intent.Confidence = max(intent.Confidence, implementationConfidence)
	}
	if countKeywordHits(lower, investigationKeywords) > 0 {
		intent.IsInvestigation = true
		intent.Confidence = max(intent.Confidence, investigationConfidence)
	}
	if countKeywordHits(lower, bugKeywords) > 0 {
		intent.IsBugReport = true
		intent.Confidence = max(intent.Confidence, bugReportConfidence)
	}

	// CIGS delegation flags.
	if n := countKeywordHits(lower, explorationKeywords); n > 0 {
		intent.InvolvesExploration = true
		intent.Confidence = max(intent.Confidence, min(1.0, float64(n)*explorationConfidenceMultiplier))
	}
	if n := countKeywordHits(lower, codeChangeKeywords); n > 0 {
		intent.InvolvesCodeChanges = true
		intent.Confidence = max(intent.Confidence, min(1.0, float64(n)*codeChangeConfidenceMultiplier))
	}
	if n := countKeywordHits(lower, gitKeywords); n > 0 {
		intent.InvolvesGit = true
		intent.Confidence = max(intent.Confidence, min(1.0, float64(n)*gitConfidenceMultiplier))
	}

	return intent
}

// ---------- guidance generators ----------

// GenerateGuidance produces the additionalContext string for CIGS injection.
// It combines intent-specific orchestrator directives with an optional
// terse active-item hint (one-liner, not full attribution block).
//
// Parameters:
//   - intent: classification result from ClassifyPrompt
//   - activeFeatureID: currently active work item (may be "")
//   - activeWorkType: type of the active work item ("feature", "spike", "bug", or "")
//   - activeItemHint: terse one-liner "ACTIVE: <id> — <title>" or "" (not full block)
//
// Returns the combined guidance string (may be empty).
func GenerateGuidance(intent PromptIntent, activeFeatureID, activeWorkType, activeItemHint string) string {
	var parts []string

	directive := intentDirective(intent, activeFeatureID, activeWorkType)
	if directive != "" {
		parts = append(parts, directive)
	}

	cigsBlock := cigsImperatives(intent)
	if cigsBlock != "" {
		parts = append(parts, cigsBlock)
	}

	if activeItemHint != "" {
		parts = append(parts, activeItemHint)
	}

	return strings.Join(parts, "\n\n")
}

// intentDirective returns orchestrator workflow directives based on the prompt
// intent and the currently active work item type.
func intentDirective(intent PromptIntent, activeFeatureID, activeWorkType string) string {
	// Continuation with active work — no extra directive needed.
	if intent.IsContinuation && activeFeatureID != "" {
		return ""
	}

	hasActive := activeFeatureID != ""

	// Implementation during a spike — warn to transition to a feature.
	if intent.IsImplementation && hasActive && activeWorkType == "spike" {
		return fmt.Sprintf(
			"ORCHESTRATOR DIRECTIVE: Implementation requested during spike.\n"+
				"Active work: %s — Type: spike\n\n"+
				"Spikes are for investigation, NOT implementation.\n"+
				"REQUIRED: Complete or pause the spike, then create a feature for implementation.\n"+
				"Delegate to a coder subagent — orchestrators coordinate, subagents implement.",
			activeFeatureID,
		)
	}

	// Implementation with a feature active — remind to delegate.
	if intent.IsImplementation && hasActive && activeWorkType == "feature" {
		return fmt.Sprintf(
			"ORCHESTRATOR DIRECTIVE: Implementation work detected.\n"+
				"Active work: %s — Type: feature\n\n"+
				"REQUIRED: Delegate to a coder subagent.\n"+
				"DO NOT execute code directly in orchestrator context.",
			activeFeatureID,
		)
	}

	// Bug report when feature is active — suggest creating a bug.
	if intent.IsBugReport && hasActive && activeWorkType == "feature" {
		return fmt.Sprintf(
			"WORKFLOW GUIDANCE: Bug report detected.\n"+
				"Active work: %s — Type: feature\n\n"+
				"If this bug is part of the current feature, continue.\n"+
				"If separate, create a bug: wipnote bug create \"Title\" --track <trk-id>",
			activeFeatureID,
		)
	}

	// No active work item — nudge toward creating one.
	// Prioritize: Implementation > Bug > Investigation
	if !hasActive {
		if intent.IsImplementation {
			return "ORCHESTRATOR DIRECTIVE: Implementation work detected but no active work item.\n" +
				"REQUIRED: Create a feature, start it, then delegate to a coder subagent."
		}
		if intent.IsBugReport {
			return "WORKFLOW GUIDANCE: Bug report detected but no active work item.\n" +
				"Create a bug: wipnote bug create \"Title\" --track <trk-id> then wipnote bug start <id>"
		}
		if intent.IsInvestigation {
			return "WORKFLOW GUIDANCE: Investigation detected but no active work item.\n" +
				"Create a spike: wipnote spike create \"Title\" --track <trk-id> then wipnote spike start <id>"
		}
	}

	return ""
}

// cigsImperatives returns delegation imperative lines for exploration,
// code changes, or git operations.
func cigsImperatives(intent PromptIntent) string {
	var lines []string

	if intent.InvolvesExploration {
		lines = append(lines,
			"[CIGS] Exploration detected — consider delegating to researcher subagent.")
	}
	if intent.InvolvesCodeChanges {
		lines = append(lines,
			"[CIGS] Code changes detected — consider delegating to coder subagent.")
	}
	if intent.InvolvesGit {
		lines = append(lines,
			"[CIGS] Git operations detected — consider using Bash(\"copilot ...\") or delegating to patch-coder.")
	}

	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// ---------- helpers ----------

// countKeywordHits returns how many keywords from the list appear in text.
func countKeywordHits(text string, keywords []string) int {
	n := 0
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			n++
		}
	}
	return n
}

// matchesContinuation checks whether the prompt is a short continuation signal.
// We only match when the keyword appears at or near the start of the prompt.
func matchesContinuation(lower string) bool {
	for _, kw := range continuationKeywords {
		if strings.HasPrefix(lower, kw) {
			return true
		}
		// Also match if the entire prompt equals the keyword.
		if lower == kw {
			return true
		}
	}
	return false
}
