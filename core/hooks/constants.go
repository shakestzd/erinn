package hooks

const (
	// --- Project Directory Resolution ---
	// Default walk-up levels when resolving .wipnote/ directory
	defaultProjectDirWalkLevels = 10

	// --- Text Truncation Limits ---
	// Maximum characters for prompt summaries in UserPrompt hook
	promptSummaryMaxLen = 300
	// Maximum characters for output summaries in SubagentStop hook
	outputSummaryMaxLen = 500
	// Maximum characters for session last_user_query field
	sessionQueryMaxLen = 200
	// Maximum characters for debug log messages in missing_events
	debugMsgMaxLen = 200

	// --- Code Quality Limits ---
	// Module size warning threshold (lines of code)
	moduleWarnLines = 300
	// Module size hard limit for new code (lines of code)
	moduleLimitLines = 500
	// Function length warning threshold (lines of code)
	funcWarnLines = 30
	// Function length hard limit for new code (lines of code)
	funcLimitLines = 50
	// Minimum lines per duplicate block for duplication detection
	dupBlockSize = 5

	// --- YOLO Mode Hard Limits ---
	// Maximum file count in a single YOLO commit
	yoloBudgetMaxFiles = 20
	// Maximum lines added in a single YOLO commit
	yoloBudgetMaxLines = 600
	// Code health threshold for YOLO mode (file size limit)
	yoloCodeHealthMaxLines = 500

	// --- Query Limits ---
	// Maximum number of open work items to display in attribution guidance
	maxOpenWorkItemsDisplay = 10
	// Maximum characters for active feature description in CIGS injection
	activeDescMaxLen = 200

	// --- Session ID Formatting ---
	// Length of session ID preview in debug logs (first N chars)
	sessionIDPreviewLen = 8

	// --- Confidence Thresholds (Prompt Classification) ---
	// Continuation prompt confidence score
	continuationConfidence = 0.9
	// Implementation classification confidence
	implementationConfidence = 0.8
	// Bug report classification confidence
	bugReportConfidence = 0.75
	// Investigation classification confidence
	investigationConfidence = 0.7
	// Base confidence multiplier for exploration keywords (0.3 per keyword)
	explorationConfidenceMultiplier = 0.3
	// Base confidence multiplier for code change keywords (0.35 per keyword)
	codeChangeConfidenceMultiplier = 0.35
	// Base confidence multiplier for git operation keywords (0.4 per keyword)
	gitConfidenceMultiplier = 0.4
)
