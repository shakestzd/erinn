// Package main — compliance auto subcommand.
// Invoked as: wipnote compliance auto <feature-id> [flags]
//
// Pipeline:
//  1. Resolve git root; skip with "no git history" if not a repo.
//  2. Acquire per-feature lockfile; exit 1 if already running.
//  3. Load feature node; error if not found.
//  4. Extract spec; skip with "no spec" if absent.
//  5. Compute spec hash.
//  6. Build diff blob from the commits linked to the item (message convention
//     plus any committed_in edges); skip when none.
//  7. Truncate diff at --max-diff-chars at line boundary.
//  8. --dry-run: print prompt, exit 0.
//  9. Call headless claude -p; capture stdout/stderr.
//  10. Parse JSON result.
//  11. --preview: print findings to stdout, no HTML write.
//  12. Write <section class="compliance-findings"> atomically.
//  13. Print one-line summary.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// complianceAutoFlags holds all parsed flags for the auto subcommand.
type complianceAutoFlags struct {
	model        string
	effort       string
	dryRun       bool
	preview      bool
	maxDiffChars int
	maxTurns     int
	maxWallClock time.Duration
	batchSince   string
}

// autoComplianceFinding is the JSON structure returned by the LLM.
type autoComplianceFinding struct {
	Summary  string                    `json:"summary"`
	Criteria []autoComplianceCriterion `json:"criteria"`
	Score    int                       `json:"score"`
	Notes    string                    `json:"notes"`
}

// autoComplianceCriterion is one criterion in the LLM response.
type autoComplianceCriterion struct {
	Text     string `json:"text"`
	Status   string `json:"status"` // "pass" | "fail" | "unclear"
	Evidence string `json:"evidence"`
}

// headlessRequest holds the parameters for a headless claude invocation.
type headlessRequest struct {
	model        string
	effort       string
	maxTurns     int
	maxBudgetUSD float64
	maxWallClock time.Duration
	systemPrompt string
	userPrompt   string
}

// headlessResult holds the response from a headless claude invocation.
type headlessResult struct {
	text    string
	costUSD float64
}

// BudgetExceededError is returned when the LLM hits the --max-budget-usd cap.
type BudgetExceededError struct {
	msg string
}

func (e *BudgetExceededError) Error() string { return e.msg }

// headlessInvokerFn is the type for the headless invoker function.
// Package-level var for test stub injection.
// NOTE: Tests that swap this MUST NOT call t.Parallel() — the var is global.
var headlessInvoker = realHeadlessInvoker

// diffBuilderFn is the type for the diff builder function.
// Package-level var for test stub injection — allows tests to bypass git/DB diff
// collection and provide a deterministic diff blob directly.
// NOTE: Tests that swap this MUST NOT call t.Parallel() — the var is global.
var diffBuilderFn = realBuildDiffBlob

// realBuildDiffBlob is the production diff builder; wraps buildDiffBlob.
func realBuildDiffBlob(ctx context.Context, database *sql.DB, featureID, gitRoot string, maxChars int) (string, bool, error) {
	return buildDiffBlob(ctx, database, featureID, gitRoot, maxChars)
}

// complianceAutoCmd returns the "auto" subcommand for "compliance".
// It should be added to the compliance command via:
//
//	compliance.AddCommand(complianceAutoCmd())
func complianceAutoCmd() *cobra.Command {
	var flags complianceAutoFlags

	cmd := &cobra.Command{
		Use:   "auto <feature-id>",
		Short: "Auto-grade a feature's compliance with its spec using claude -p (headless)",
		Long: `Compare a feature's spec against its implementation diff via claude -p and write
structured findings to a <section class="compliance-findings"> in the feature HTML.

Flags:
  --dry-run     Print the prompt that would be sent; skip LLM call.
  --preview     Run the full pipeline but print findings to stdout; do not write HTML.
  --batch-since Run against all done features completed since a date (format: 2006-01-02).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.batchSince != "" {
				return runComplianceAutoBatch(cmd.Context(), flags)
			}
			if len(args) == 0 {
				return fmt.Errorf("feature-id required (or use --batch-since <date>)")
			}
			return runComplianceAuto(cmd.Context(), args[0], flags)
		},
	}

	cmd.Flags().StringVar(&flags.model, "model", "claude-sonnet-4-6", "Model to use for headless claude invocation")
	cmd.Flags().StringVar(&flags.effort, "effort", "medium", "Effort level for headless claude (low|medium|high)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Print the prompt; skip LLM call; exit 0")
	cmd.Flags().BoolVar(&flags.preview, "preview", false, "Run full pipeline; print findings to stdout; do not write HTML")
	cmd.Flags().IntVar(&flags.maxDiffChars, "max-diff-chars", 50000, "Max characters of diff to include in prompt (truncated at line boundary)")
	cmd.Flags().IntVar(&flags.maxTurns, "max-turns", 5, "Max tool-loop turns for claude -p")
	cmd.Flags().DurationVar(&flags.maxWallClock, "max-wall-clock", 5*time.Minute, "Self-enforced wall-clock timeout for the LLM call")
	cmd.Flags().StringVar(&flags.batchSince, "batch-since", "", "Iterate all done features completed since date (format: 2006-01-02); rate-limited to 1 call per 30s")

	return cmd
}

// runComplianceAuto runs the full compliance auto pipeline for a single feature.
func runComplianceAuto(ctx context.Context, featureID string, flags complianceAutoFlags) error {
	// Step 1: Resolve wipnote dir and project root.
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(wipnoteDir)

	gitRoot, err := resolveGitRoot(projectDir)
	if err != nil {
		// Not a git repo — write "no git history" finding, exit 0.
		featurePath := filepath.Join(wipnoteDir, "features", featureID+".html")
		if statErr := writeSkipFinding(featurePath, "compliance skipped: no git history"); statErr != nil {
			return statErr
		}
		fmt.Printf("compliance %s: skipped — no git history\n", featureID)
		return nil
	}

	// Step 2: Acquire per-feature lockfile.
	lockPath := filepath.Join(wipnoteDir, "locks", "compliance-"+featureID+".lock")
	unlock, err := acquireComplianceLock(lockPath)
	if err != nil {
		return err
	}
	defer unlock()

	// Step 3: Load feature path; error if not found.
	featurePath := filepath.Join(wipnoteDir, "features", featureID+".html")
	if _, err := os.Stat(featurePath); err != nil {
		return fmt.Errorf("no feature: %s", featureID)
	}

	featureHTML, err := os.ReadFile(featurePath)
	if err != nil {
		return fmt.Errorf("read feature file: %w", err)
	}

	// Step 4: Extract spec.
	specContent := extractSpecSection(string(featureHTML))
	if specContent == "" {
		if err := writeSkipFinding(featurePath, "compliance skipped: no spec"); err != nil {
			return err
		}
		fmt.Printf("compliance %s: skipped — no spec\n", featureID)
		return nil
	}

	// Step 5: Compute spec hash.
	specHash := computeSpecHash(specContent)

	// Check if existing section has a different spec hash (for stale-spec reporting).
	prevSpecHash := extractPrevSpecHash(string(featureHTML))

	// Step 6: Build diff blob.
	database, err := openDB(wipnoteDir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	diffBlob, diffTruncated, err := diffBuilderFn(ctx, database, featureID, gitRoot, flags.maxDiffChars)
	if err != nil {
		return fmt.Errorf("build diff: %w", err)
	}

	if diffBlob == "" {
		if err := writeSkipFinding(featurePath, "compliance skipped: no diff available"); err != nil {
			return err
		}
		fmt.Printf("compliance %s: skipped — no diff available\n", featureID)
		return nil
	}

	// Step 6.5: Gather outcome evidence (feat-f9118b9c). Up to this point
	// compliance auto has only ever compared the diff against the spec — it
	// has never asked whether the result actually worked. rollupSig reads
	// the data-rollup-* attributes feat-7ee73444 persists directly onto this
	// same feature HTML (re-parsed from the featureHTML bytes already read
	// in step 3, no extra disk I/O); gatePass/gateFail come from this item's
	// own quality-gate history. Both sources degrade to "no evidence" rather
	// than aborting the run — a feature file that predates the <article>
	// convention, or a gate query error, must not block grading — and
	// buildOutcomeEvidence says so explicitly in the prompt text rather than
	// letting an absence read as a clean result.
	var rollupSig workitem.ItemRollupSignal
	if node, parseErr := htmlparse.ParseString(string(featureHTML)); parseErr == nil {
		rollupSig = workitem.RollupSignalFor(node)
	}
	gatePass, gateFail, gateErr := dbpkg.GateRecordCountsForWorkItem(database, featureID)
	if gateErr != nil {
		fmt.Fprintf(os.Stderr, "wipnote: compliance %s: gate history unavailable: %v\n", featureID, gateErr)
		gatePass, gateFail = 0, 0
	}
	outcomeEvidence := buildOutcomeEvidence(rollupSig, gatePass, gateFail)

	// Step 7: Build prompt.
	systemPrompt := buildComplianceSystemPrompt()
	userPrompt := buildComplianceUserPrompt(specContent, diffBlob, outcomeEvidence)

	// Step 8: --dry-run: print prompt, exit 0.
	if flags.dryRun {
		fmt.Println("=== SYSTEM PROMPT ===")
		fmt.Println(systemPrompt)
		fmt.Println("=== USER PROMPT ===")
		fmt.Println(userPrompt)
		return nil
	}

	// Step 9: Call headless claude.
	req := headlessRequest{
		model:        flags.model,
		effort:       flags.effort,
		maxTurns:     flags.maxTurns,
		maxBudgetUSD: 0.50,
		maxWallClock: flags.maxWallClock,
		systemPrompt: systemPrompt,
		userPrompt:   userPrompt,
	}

	result, err := headlessInvoker(ctx, req)
	if err != nil {
		if budgetErr, ok := err.(*BudgetExceededError); ok {
			attrs := map[string]string{
				"score":          "0",
				"cost-usd":       "0",
				"model":          flags.model,
				"spec-hash":      specHash,
				"timestamp":      time.Now().UTC().Format(time.RFC3339),
				"diff-truncated": strconv.FormatBool(diffTruncated),
				"gate-pass":      strconv.Itoa(gatePass),
				"gate-fail":      strconv.Itoa(gateFail),
			}
			body := renderFindingsHTML(&autoComplianceFinding{
				Summary: "compliance error: budget exceeded",
				Notes:   budgetErr.Error(),
			})
			if !flags.preview {
				_ = writeComplianceSection(featurePath, attrs, body)
			} else {
				fmt.Println(body)
			}
			return fmt.Errorf("compliance %s: budget exceeded: %w", featureID, budgetErr)
		}
		if strings.Contains(err.Error(), "timeout") {
			attrs := map[string]string{
				"score":          "0",
				"cost-usd":       "0",
				"model":          flags.model,
				"spec-hash":      specHash,
				"timestamp":      time.Now().UTC().Format(time.RFC3339),
				"diff-truncated": strconv.FormatBool(diffTruncated),
				"gate-pass":      strconv.Itoa(gatePass),
				"gate-fail":      strconv.Itoa(gateFail),
			}
			body := renderFindingsHTML(&autoComplianceFinding{
				Summary: "compliance error: wall-clock timeout",
				Notes:   err.Error(),
			})
			if !flags.preview {
				_ = writeComplianceSection(featurePath, attrs, body)
			} else {
				fmt.Println(body)
			}
			return fmt.Errorf("compliance %s: timeout: %w", featureID, err)
		}
		return fmt.Errorf("compliance %s: headless invoke: %w", featureID, err)
	}

	// Step 10: Parse JSON result.
	finding, err := parseComplianceFinding(result.text)
	if err != nil {
		// Write parse-failure finding, exit 1.
		attrs := map[string]string{
			"score":          "0",
			"cost-usd":       fmt.Sprintf("%.6f", result.costUSD),
			"model":          flags.model,
			"spec-hash":      specHash,
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
			"diff-truncated": strconv.FormatBool(diffTruncated),
			"gate-pass":      strconv.Itoa(gatePass),
			"gate-fail":      strconv.Itoa(gateFail),
		}
		failFinding := &autoComplianceFinding{
			Summary: "compliance error: parse failure",
			Notes:   fmt.Sprintf("raw response: %s", truncateStr(result.text, 500)),
		}
		body := renderFindingsHTML(failFinding)
		if !flags.preview {
			_ = writeComplianceSection(featurePath, attrs, body)
		} else {
			fmt.Println(body)
		}
		return fmt.Errorf("compliance %s: JSON parse failure: %w", featureID, err)
	}

	// Step 11: --preview: print findings to stdout, no HTML write.
	attrs := map[string]string{
		"score":          strconv.Itoa(finding.Score),
		"cost-usd":       fmt.Sprintf("%.6f", result.costUSD),
		"model":          flags.model,
		"spec-hash":      specHash,
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"diff-truncated": strconv.FormatBool(diffTruncated),
		"gate-pass":      strconv.Itoa(gatePass),
		"gate-fail":      strconv.Itoa(gateFail),
	}
	body := renderFindingsHTML(finding)

	if flags.preview {
		fmt.Println(body)
		return nil
	}

	// Step 12: Write compliance findings section atomically.
	if err := writeComplianceSection(featurePath, attrs, body); err != nil {
		return fmt.Errorf("write compliance section: %w", err)
	}

	// Step 13: Print one-line summary.
	staleNote := ""
	if prevSpecHash != "" && prevSpecHash != specHash {
		staleNote = " [stale-spec]"
	}
	fmt.Printf("compliance %s score=%d cost=$%.6f%s\n", featureID, finding.Score, result.costUSD, staleNote)

	return nil
}

// runComplianceAutoBatch iterates done features completed since batchSince and
// runs compliance auto on each, rate-limited to 1 call per 30 seconds.
func runComplianceAutoBatch(ctx context.Context, flags complianceAutoFlags) error {
	since, err := time.Parse("2006-01-02", flags.batchSince)
	if err != nil {
		return fmt.Errorf("invalid --batch-since date %q: use format 2006-01-02", flags.batchSince)
	}

	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	database, err := openDB(wipnoteDir)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	features, err := listDoneFeaturesSince(database, since)
	if err != nil {
		return fmt.Errorf("list done features: %w", err)
	}

	fmt.Printf("batch compliance: %d done features since %s\n", len(features), flags.batchSince)

	for i, feat := range features {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(30 * time.Second):
			}
		}
		fmt.Printf("[%d/%d] processing %s: %s\n", i+1, len(features), feat.ID, feat.Title)
		if err := runComplianceAuto(ctx, feat.ID, flags); err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
		}
	}
	return nil
}

// listDoneFeaturesSince queries done features updated on or after since.
func listDoneFeaturesSince(database *sql.DB, since time.Time) ([]dbpkg.Feature, error) {
	rows, err := database.Query(`
		SELECT id, type, title, description, status, priority,
			assigned_to, track_id, created_at, updated_at, steps_total, steps_completed
		FROM features
		WHERE status = 'done' AND updated_at >= ?
		ORDER BY updated_at DESC`,
		since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("query done features: %w", err)
	}
	defer rows.Close()

	var features []dbpkg.Feature
	for rows.Next() {
		var f dbpkg.Feature
		var trackID, assignedTo, description sql.NullString
		var createdStr, updatedStr string
		if err := rows.Scan(
			&f.ID, &f.Type, &f.Title, &description, &f.Status, &f.Priority,
			&assignedTo, &trackID, &createdStr, &updatedStr,
			&f.StepsTotal, &f.StepsCompleted,
		); err != nil {
			return nil, err
		}
		f.TrackID = trackID.String
		f.Description = description.String
		f.AssignedTo = assignedTo.String
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		features = append(features, f)
	}
	return features, rows.Err()
}

// resolveGitRoot runs `git -C <dir> rev-parse --show-toplevel` and returns the
// project's git root. Returns an error if <dir> is not a git repository.
func resolveGitRoot(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// acquireComplianceLock creates a lockfile at lockPath containing the current PID.
// Returns a cleanup function that removes the lockfile, or an error if another
// process already holds the lock (determined by a live PID in the lockfile).
func acquireComplianceLock(lockPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create locks dir: %w", err)
	}

	// Check if lockfile already exists with a live PID.
	if data, err := os.ReadFile(lockPath); err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, err := strconv.Atoi(pidStr); err == nil {
			// Check if the process is alive by sending signal 0.
			proc, err := os.FindProcess(pid)
			if err == nil {
				if proc.Signal(syscall.Signal(0)) == nil {
					// Process is alive — lock is held.
					return nil, fmt.Errorf("compliance already running for %s (pid %d)", filepath.Base(lockPath), pid)
				}
			}
		}
		// Stale lockfile — remove it.
		os.Remove(lockPath)
	}

	// Write our PID to the lockfile.
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return nil, fmt.Errorf("write lockfile: %w", err)
	}

	cleanup := func() { os.Remove(lockPath) }
	return cleanup, nil
}

// buildDiffBlob builds the diff text to embed in the compliance prompt.
//
// Commits are Git-derived: canonicalLinkedCommits finds the commits whose
// messages name featureID under wipnote's commit convention — the same source
// the completion gate uses (workitem_provenance_canonical.go). It replaces two
// reads of the derived index that can no longer return anything:
// GetCommitsByFeature (git_commits) and, as a fallback, ListFilesByFeature
// (feature_files). Neither table is in the hydrate set, so both returned empty
// on every call and the compliance prompt silently shipped with no diff
// (feat-fc3cc9e0).
//
// The database parameter is retained because diffBuilderFn is a stubbing seam
// with several test doubles bound to this signature; nothing here reads it.
//
// Returns (diffText, truncated, error). An item whose commits do not name it
// yields an empty diff, which the caller already handles.
func buildDiffBlob(ctx context.Context, database *sql.DB, featureID, gitRoot string, maxChars int) (string, bool, error) {
	_ = database

	// The work item's node is passed so a commit linked by hand via
	// `wipnote <type> link-commit` counts here exactly as it does for the
	// completion gate. Best-effort: a node that cannot be read yields the
	// message-linkage commits alone, which is the common case anyway.
	var node *models.Node
	if wipnoteDir, err := findWipnoteDir(); err == nil {
		if path := resolveNodePath(wipnoteDir, featureID); path != "" {
			node, _ = htmlparse.ParseFile(path)
		}
	}

	var diffParts []string
	for _, hash := range canonicalLinkedCommits(gitRoot, featureID, node) {
		args := []string{"-C", gitRoot, "show", "--no-color", "--first-parent", hash}
		out, err := exec.CommandContext(ctx, "git", args...).Output()
		if err != nil {
			// Skip commits not present locally (shallow clone safety).
			continue
		}
		diffParts = append(diffParts, string(out))
	}

	if len(diffParts) == 0 {
		return "", false, nil
	}

	combined := strings.Join(diffParts, "\n---\n")
	return truncateDiff(combined, maxChars)
}

// truncateDiff truncates a diff string at the nearest line boundary at or before
// maxChars. Returns (truncated text, wasTruncated). If diff is shorter than
// maxChars, it is returned as-is with wasTruncated=false.
func truncateDiff(diff string, maxChars int) (string, bool, error) {
	if len(diff) <= maxChars {
		return diff, false, nil
	}

	// Find the last newline at or before maxChars.
	cutAt := maxChars
	for cutAt > 0 && diff[cutAt] != '\n' {
		cutAt--
	}
	if cutAt == 0 {
		// No newline found — hard cut at maxChars.
		cutAt = maxChars
	}

	truncated := diff[:cutAt]
	footer := fmt.Sprintf("\n... [truncated %d chars]", len(diff)-cutAt)
	return truncated + footer, true, nil
}

// computeSpecHash returns the hex SHA-256 of spec content (first 8 chars).
func computeSpecHash(spec string) string {
	h := sha256.Sum256([]byte(spec))
	return fmt.Sprintf("%x", h[:4])
}

// extractPrevSpecHash reads the data-spec-hash attribute from an existing
// <section class="compliance-findings"> section, or returns "".
func extractPrevSpecHash(featureHTML string) string {
	const openTag = `<section class="compliance-findings"`
	start := strings.Index(featureHTML, openTag)
	if start == -1 {
		return ""
	}
	// Find the end of the opening tag.
	tagEnd := strings.Index(featureHTML[start:], ">")
	if tagEnd == -1 {
		return ""
	}
	tagContent := featureHTML[start : start+tagEnd]

	// Extract data-spec-hash="..." attribute.
	prefix := `data-spec-hash="`
	hashIdx := strings.Index(tagContent, prefix)
	if hashIdx == -1 {
		return ""
	}
	rest := tagContent[hashIdx+len(prefix):]
	quoteEnd := strings.Index(rest, `"`)
	if quoteEnd == -1 {
		return ""
	}
	return rest[:quoteEnd]
}

// buildComplianceSystemPrompt returns the system prompt for the LLM.
func buildComplianceSystemPrompt() string {
	return `Return ONLY valid JSON. No prose, no markdown fences.
Schema: {"summary": string, "criteria": [{"text": string, "status": "pass"|"fail"|"unclear", "evidence": string}], "score": 0-100, "notes": string}`
}

// buildOutcomeEvidence renders the per-item outcome facts available at
// grading time into a plain-text block for the compliance prompt
// (feat-f9118b9c): this item's own gate pass/fail history, plus — when a
// feat-7ee73444 rollup exists — its measured failure rate, retries, and edit
// churn. Absence is written out explicitly rather than omitted, because an
// LLM grader that sees only "matched spec" text for an unmeasured item has no
// way to distinguish that from "measured and clean" unless told so — that
// conflation is exactly what this evidence block exists to prevent. Cost is
// deliberately never included: it ships degraded
// (workitem.SourceOtelCost) and a grading signal cannot represent that
// caveat, so it is left out rather than silently promoted.
func buildOutcomeEvidence(sig workitem.ItemRollupSignal, gatePass, gateFail int) string {
	var b strings.Builder

	if gatePass+gateFail == 0 {
		b.WriteString("Gate history: no gate runs recorded for this item (unmeasured — do not treat as passing).\n")
	} else {
		b.WriteString(fmt.Sprintf("Gate history: %d passed / %d failed (%d runs recorded).\n",
			gatePass, gateFail, gatePass+gateFail))
	}

	if !sig.Measured {
		b.WriteString("Runtime outcome: unmeasured — no telemetry rollup exists for this item (do not treat as healthy).\n")
		return b.String()
	}

	if sig.HasFailureRate {
		b.WriteString(fmt.Sprintf("Measured failure rate: %.2f%% (%s, source %s).\n",
			sig.FailureRate*100, sig.FailureRateCoverage, sig.FailureRateSource))
	} else {
		b.WriteString("Measured failure rate: unmeasured for this item.\n")
	}
	if sig.HasRetries {
		b.WriteString(fmt.Sprintf("Retries observed: %d (%s).\n", sig.Retries, sig.RetriesCoverage))
	}
	if sig.HasChurnFiles {
		b.WriteString(fmt.Sprintf("Files edited more than once: %d (%s).\n", sig.ChurnFiles, sig.ChurnFilesCoverage))
	}
	if sig.TelemetryUnavailable {
		b.WriteString("Telemetry: unavailable for this item's harness (do not treat as clean).\n")
	}

	return b.String()
}

// buildComplianceUserPrompt builds the user prompt embedding spec, diff, and
// outcome evidence (feat-f9118b9c) — the join that lets a compliance verdict
// reflect whether the result worked, not only whether the diff matched the
// spec.
func buildComplianceUserPrompt(specContent, diffBlob, outcomeEvidence string) string {
	return fmt.Sprintf(`You are a code compliance reviewer. Compare the feature spec against the implementation diff.

## Feature Spec

%s

## Implementation Diff

%s

## Outcome Evidence

%s
Absence of evidence above means the outcome is UNMEASURED, not passing. Do not treat missing runtime or gate data as a clean result. A high measured failure rate or a majority of failed gate runs should weigh against the score even when the diff otherwise looks complete.

## Instructions

For each acceptance criterion in the spec, determine if the diff shows it was implemented (pass), explicitly not implemented (fail), or uncertain (unclear). Weigh the outcome evidence above alongside the diff: a criterion whose code looks right but whose gate or runtime evidence shows failure should not be scored as a clean pass. Return your assessment as JSON matching the schema.`, specContent, diffBlob, outcomeEvidence)
}

// parseComplianceFinding parses the LLM's JSON response into an autoComplianceFinding.
// The LLM may return a JSON object with a "result" wrapper or directly.
func parseComplianceFinding(raw string) (*autoComplianceFinding, error) {
	raw = strings.TrimSpace(raw)

	// Try direct parse first.
	var finding autoComplianceFinding
	if err := json.Unmarshal([]byte(raw), &finding); err == nil {
		return &finding, nil
	}

	// Try wrapped in {"result": ...}.
	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err == nil && wrapper.Result != nil {
		if err2 := json.Unmarshal(wrapper.Result, &finding); err2 == nil {
			return &finding, nil
		}
	}

	return nil, fmt.Errorf("cannot parse LLM response as compliance JSON: %q", truncateStr(raw, 200))
}

// renderFindingsHTML renders a finding into the inner HTML for the compliance section.
func renderFindingsHTML(f *autoComplianceFinding) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<h3>Compliance Score: %d/100</h3>\n", f.Score))
	sb.WriteString(fmt.Sprintf("<p><strong>Summary:</strong> %s</p>\n", html.EscapeString(f.Summary)))

	if len(f.Criteria) > 0 {
		sb.WriteString("<ul>\n")
		for _, c := range f.Criteria {
			icon := "?"
			switch c.Status {
			case "pass":
				icon = "&#x2705;"
			case "fail":
				icon = "&#x274C;"
			}
			sb.WriteString(fmt.Sprintf("  <li>%s <strong>%s</strong>: %s",
				icon, html.EscapeString(c.Text), html.EscapeString(c.Status)))
			if c.Evidence != "" {
				sb.WriteString(fmt.Sprintf(" — <em>%s</em>", html.EscapeString(c.Evidence)))
			}
			sb.WriteString("</li>\n")
		}
		sb.WriteString("</ul>\n")
	}

	if f.Notes != "" {
		sb.WriteString(fmt.Sprintf("<p><em>Notes: %s</em></p>\n", html.EscapeString(f.Notes)))
	}

	return sb.String()
}

// writeSkipFinding writes a "compliance skipped: <reason>" finding to the feature HTML.
// It creates the feature file if it doesn't exist (no-op if feature file is missing).
func writeSkipFinding(featurePath, reason string) error {
	if _, err := os.Stat(featurePath); err != nil {
		// Feature file doesn't exist — nothing to write to.
		return nil
	}

	attrs := map[string]string{
		"score":          "0",
		"cost-usd":       "0",
		"model":          "none",
		"spec-hash":      "none",
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"diff-truncated": "false",
	}
	body := fmt.Sprintf("<p><em>%s</em></p>", html.EscapeString(reason))
	return writeComplianceSection(featurePath, attrs, body)
}

// truncateStr truncates a string to at most maxLen chars, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// buildHeadlessArgs constructs the `claude` CLI argv for a headless
// compliance-grading invocation (see realHeadlessInvoker). It is split out
// from realHeadlessInvoker so the exact flag set and order can be asserted by
// tests without spawning a process — see compliance_auto_test.go's
// TestBuildHeadlessArgs_GoldenFlags (always-on) and
// TestBuildHeadlessArgs_ValidatedAgainstInstalledCLI (live, skipped when
// `claude` isn't on PATH).
//
// req.userPrompt is intentionally NOT included here: it is piped via stdin
// by the caller, not passed as an argument.
//
// NOTE: this used to pass "--system" (not a recognized flag — `claude -p
// --system ...` fails immediately with "error: unknown option '--system'")
// until bug-33348600's predecessor fix; every real (non-stubbed) invocation
// of this code path was failing before that correction. No existing test
// caught it because all compliance_auto_test.go coverage stubbed
// headlessInvoker rather than exercising realHeadlessInvoker's constructed
// argv against the actual claude binary. The tests added for bug-33348600
// close that hole.
func buildHeadlessArgs(req headlessRequest) []string {
	return []string{
		"-p",
		"--output-format", "json",
		"--model", req.model,
		"--effort", req.effort,
		"--max-budget-usd", fmt.Sprintf("%.2f", req.maxBudgetUSD),
		"--max-turns", strconv.Itoa(req.maxTurns),
		"--permission-mode", "dontAsk",
		"--allowedTools", "Read",
		"--system-prompt", req.systemPrompt,
	}
}

// realHeadlessInvoker spawns `claude -p` with the given request parameters,
// captures stdout/stderr into buffers, honors context cancellation, and
// self-enforces the wall-clock timeout via time.AfterFunc.
//
// Stderr is never leaked to the caller's stderr — it is captured into a buffer.
// WIPNOTE_AUTO_COMPLIANCE_RUNNING=1 is set in the spawned process env
// (forward-compat fork-bomb guard for a future hook plan).
//
// Bare-mode exposure (bug-48b023e5): Claude Code's `--bare` flag (skips
// auto-discovery of hooks/skills/plugins/MCP servers/CLAUDE.md, and restricts
// auth to ANTHROPIC_API_KEY/apiKeyHelper only — OAuth and keychain are never
// read in bare mode) is documented as "the recommended mode for scripted and
// SDK calls, and will become the default for -p in a future release"
// (code.claude.com/docs/en/headless, verified against Claude Code v2.1.224).
// This call is intentionally NOT passing --bare today, and is safe either way
// with respect to *discovery*: the system prompt is fully replaced via
// --system-prompt below, --allowedTools is pinned to "Read" only, and the
// prompt never references skills or relies on CLAUDE.md/plugin/hook state —
// so losing auto-discovery when the default flips changes nothing about the
// grading result. The unresolved risk is auth, not discovery: bare mode's
// OAuth/keychain lockout is bundled into the same flag, and there is currently
// no documented --no-bare opt-out to keep today's ambient-login behavior once
// -p defaults to bare. If that flip also flips the default auth mode, this
// call starts failing for any environment without ANTHROPIC_API_KEY set —
// watch Claude Code release notes for the -p default change and set
// ANTHROPIC_API_KEY (or wire --bare explicitly) before it lands.
func realHeadlessInvoker(ctx context.Context, req headlessRequest) (*headlessResult, error) {
	args := buildHeadlessArgs(req)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(req.userPrompt)

	// Capture stdout and stderr; never leak to caller.
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Set env with fork-bomb guard.
	cmd.Env = append(os.Environ(), "WIPNOTE_AUTO_COMPLIANCE_RUNNING=1")

	// Self-enforced wall-clock timeout.
	var timedOut bool
	var timedOutMu sync.Mutex
	timer := time.AfterFunc(req.maxWallClock, func() {
		timedOutMu.Lock()
		timedOut = true
		timedOutMu.Unlock()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})
	defer timer.Stop()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	waitErr := cmd.Wait()

	timedOutMu.Lock()
	wasTimedOut := timedOut
	timedOutMu.Unlock()

	if wasTimedOut {
		return nil, fmt.Errorf("timeout: wall-clock limit %s exceeded", req.maxWallClock)
	}

	if waitErr != nil {
		// Check stderr for budget exceeded error.
		stderrContent := stderrBuf.String()
		if strings.Contains(stderrContent, "error_max_budget_usd") ||
			strings.Contains(stdoutBuf.String(), "error_max_budget_usd") {
			return nil, &BudgetExceededError{msg: "max_budget_usd exceeded"}
		}
		return nil, fmt.Errorf("claude exited with error: %w; stderr: %s", waitErr, truncateStr(stderrContent, 500))
	}

	// Parse the JSON response from claude.
	return parseClaudeOutput(stdoutBuf.Bytes())
}

// claudeOutputJSON is the structure of claude's --output-format json response.
type claudeOutputJSON struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Result  string          `json:"result"`
	CostUSD float64         `json:"total_cost_usd"`
	Error   json.RawMessage `json:"error"`
}

// parseClaudeOutput parses the JSON output from `claude -p --output-format json`.
func parseClaudeOutput(data []byte) (*headlessResult, error) {
	var out claudeOutputJSON
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse claude output JSON: %w; raw: %s", err, truncateStr(string(data), 300))
	}

	if out.Subtype == "error_max_budget_usd" {
		return nil, &BudgetExceededError{msg: "max_budget_usd exceeded"}
	}

	return &headlessResult{
		text:    out.Result,
		costUSD: out.CostUSD,
	}, nil
}
