package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireRipgrep skips the caller when `rg` is not on PATH. runRelevantSearch
// shells out to ripgrep for keyword queries, so CI runners without rg must
// skip these tests instead of hard-failing. Keeps the quality gate green on
// minimal environments (containers, fresh VMs) while still catching real
// regressions when rg is present.
func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) not found in PATH — skipping test that requires it")
	}
}

// sampleFeatureHTML is a minimal valid wipnote feature HTML fixture.
const sampleFeatureHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Test Feature</title></head>
<body>
<article id="feat-aabbccdd"
         data-type="feature"
         data-status="in-progress"
         data-priority="high"
         data-created="2026-01-01T00:00:00Z"
         data-updated="2026-04-01T00:00:00Z">
  <header><h1>Retrieval-first discovery</h1></header>
  <section data-content>
    <p>This feature adds a retrieval-first workflow using ripgrep and git log.</p>
    <p>It is very useful for finding relevant work items by path keyword or sha.</p>
  </section>
</article>
</body>
</html>`

// makeRelevantFixture creates a temp .wipnote directory with one feature HTML file.
// Returns the .wipnote dir path and a cleanup function.
func makeRelevantFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	hgDir := filepath.Join(dir, ".wipnote")
	featDir := filepath.Join(hgDir, "features")
	if err := os.MkdirAll(featDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	featPath := filepath.Join(featDir, "feat-aabbccdd.html")
	if err := os.WriteFile(featPath, []byte(sampleFeatureHTML), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return hgDir
}

// --- Query auto-detection unit tests ---

func TestDetectQueryType_FilePath(t *testing.T) {
	cases := []struct {
		query string
		want  queryType
	}{
		{"cmd/wipnote/relevant.go", queryTypeFile},
		{"internal/models/node.go", queryTypeFile},
		{"./foo/bar.html", queryTypeFile},
		{"/absolute/path/to/file.md", queryTypeFile},
		{"some/dir/", queryTypeFile},
	}
	for _, tc := range cases {
		got := detectQueryType(tc.query)
		if got != tc.want {
			t.Errorf("detectQueryType(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestDetectQueryType_GitSHA(t *testing.T) {
	cases := []string{
		"abc1234",         // 7 hex chars
		"abc1234def56789", // longer hex
		"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0", // 40 chars
	}
	for _, sha := range cases {
		got := detectQueryType(sha)
		if got != queryTypeSHA {
			t.Errorf("detectQueryType(%q) = %v, want %v", sha, got, queryTypeSHA)
		}
	}
}

func TestDetectQueryType_Keyword(t *testing.T) {
	cases := []string{
		"retrieval",
		"auth middleware",
		"plan finalize",
		"xyz123", // too short/mixed to be SHA
		"hello world",
	}
	for _, kw := range cases {
		got := detectQueryType(kw)
		if got != queryTypeKeyword {
			t.Errorf("detectQueryType(%q) = %v, want %v", kw, got, queryTypeKeyword)
		}
	}
}

func TestDetectQueryType_ExistingFile(t *testing.T) {
	// A real file that exists should be detected as file-path
	f, err := os.CreateTemp(t.TempDir(), "testfile*.go")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	got := detectQueryType(f.Name())
	if got != queryTypeFile {
		t.Errorf("detectQueryType(existing file %q) = %v, want %v", f.Name(), got, queryTypeFile)
	}
}

// --- Keyword ripgrep search ---

func TestRunRelevantKeyword_ReturnsMatch(t *testing.T) {
	requireRipgrep(t)
	hgDir := makeRelevantFixture(t)

	results, err := runRelevantSearch(hgDir, "retrieval", queryTypeKeyword)
	if err != nil {
		t.Fatalf("runRelevantSearch: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result for keyword 'retrieval', got 0")
	}

	// Check that the result has the required fields populated.
	r := results[0]
	if r.ID == "" {
		t.Error("result.ID is empty")
	}
	if r.Title == "" {
		t.Error("result.Title is empty")
	}
	if r.Type == "" {
		t.Error("result.Type is empty")
	}
	if r.Status == "" {
		t.Error("result.Status is empty")
	}
}

// TestRunRelevantKeyword_MultiWordTokenized is the regression for bug-72b52aa4:
// multi-word queries used to be passed to ripgrep as a literal phrase, so
// "retrieval sha" matched nothing even though each word individually appears
// in the fixture. Tokenizing the query per whitespace and scoring each token
// independently now surfaces the match.
func TestRunRelevantKeyword_MultiWordTokenized(t *testing.T) {
	requireRipgrep(t)
	hgDir := makeRelevantFixture(t)

	results, err := runRelevantSearch(hgDir, "retrieval sha", queryTypeKeyword)
	if err != nil {
		t.Fatalf("runRelevantSearch: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected multi-word 'retrieval sha' to match via per-token scoring, got 0")
	}
	// The fixture contains both tokens, so the item should accumulate scores
	// from both — higher than a single-token match.
	if results[0].Score < 2*weightFileMention {
		t.Errorf("expected score >= 2*weightFileMention for two-token match, got %v", results[0].Score)
	}
}

func TestTokenizeQuery(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"lineage review", []string{"lineage", "review"}},
		{"  retrieval   sha  ", []string{"retrieval", "sha"}},
		{"PR 38 lineage review", []string{"PR", "38", "lineage", "review"}},
		{"a lineage a review", []string{"lineage", "review"}}, // "a" < 2 chars dropped
		{"lineage LINEAGE Lineage", []string{"lineage"}},      // case-insensitive dedup
		{"", nil},
	}
	for _, tc := range cases {
		got := tokenizeQuery(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("tokenizeQuery(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("tokenizeQuery(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// --- File-path search ---

func TestRunRelevantFilePath_ReturnsMatchingItems(t *testing.T) {
	requireRipgrep(t)
	hgDir := makeRelevantFixture(t)

	// The fixture content mentions "ripgrep and git log" — use the HTML file itself as path.
	// Since we don't have a real git repo here, file-path mode falls back to keyword.
	// Instead, verify the call path resolves without error.
	results, err := runRelevantSearch(hgDir, "git log", queryTypeKeyword)
	if err != nil {
		t.Fatalf("runRelevantSearch (file path fallback): %v", err)
	}
	// Should find the fixture because it contains "git log".
	if len(results) == 0 {
		t.Fatal("expected match for 'git log' in content, got 0")
	}
}

// --- JSON output shape ---

func TestRelevantResult_JSONShape(t *testing.T) {
	r := relevantResult{
		ID:     "feat-aabbccdd",
		Type:   "feature",
		Title:  "Retrieval-first discovery",
		Status: "in-progress",
		Score:  3.0,
		Citations: []citation{
			{File: ".wipnote/features/feat-aabbccdd.html", Line: 10, Snippet: "retrieval"},
		},
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{`"id"`, `"type"`, `"title"`, `"status"`, `"score"`, `"citations"`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing field %s: %s", want, s)
		}
	}
}

// TestRunRelevantSearch_DegradesGracefullyWithoutRipgrep is the regression
// for the rg-missing rabbit hole flagged in GH#151: without ripgrep on PATH,
// runRelevantSearch used to hard-fail the whole command. It now degrades —
// keyword queries return zero matches (no hard error) instead of erroring out.
func TestRunRelevantSearch_DegradesGracefullyWithoutRipgrep(t *testing.T) {
	hgDir := makeRelevantFixture(t)

	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	if err := os.Setenv("PATH", t.TempDir()); err != nil {
		t.Fatalf("setenv PATH: %v", err)
	}

	results, err := runRelevantSearch(hgDir, "retrieval", queryTypeKeyword)
	if err != nil {
		t.Fatalf("runRelevantSearch should degrade gracefully without rg, got error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results in degraded mode (no rg, keyword query), got %d", len(results))
	}
}

// --- No results ---

func TestRunRelevantKeyword_NoMatch(t *testing.T) {
	requireRipgrep(t)
	hgDir := makeRelevantFixture(t)

	results, err := runRelevantSearch(hgDir, "zzz_nomatch_xyz_unlikely", queryTypeKeyword)
	if err != nil {
		t.Fatalf("runRelevantSearch: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for non-matching keyword, got %d", len(results))
	}
}

// --- Score ordering ---

func TestRankResults_OrderByScore(t *testing.T) {
	items := []relevantResult{
		{ID: "feat-aaa", Score: 1.0},
		{ID: "feat-bbb", Score: 5.0},
		{ID: "feat-ccc", Score: 3.0},
	}
	ranked := rankResults(items)
	if ranked[0].ID != "feat-bbb" {
		t.Errorf("first ranked item should be feat-bbb (score 5), got %s", ranked[0].ID)
	}
	if ranked[1].ID != "feat-ccc" {
		t.Errorf("second ranked item should be feat-ccc (score 3), got %s", ranked[1].ID)
	}
}

// --- Bounded/scoped output (GH#151) ---

func sampleRelevantResults() []relevantResult {
	return []relevantResult{
		{ID: "feat-aaa", Type: "feature", Status: "in-progress", Score: 5.0, Title: "First"},
		{ID: "bug-bbb", Type: "bug", Status: "todo", Score: 4.0, Title: "Second"},
		{ID: "spk-ccc", Type: "spike", Status: "done", Score: 3.0, Title: "Third"},
		{ID: "feat-ddd", Type: "feature", Status: "todo", Score: 2.0, Title: "Fourth"},
		{ID: "trk-eee", Type: "track", Status: "in-progress", Score: 1.0, Title: "Fifth"},
	}
}

func TestApplyLimit_HonorsLimit(t *testing.T) {
	got := applyLimit(sampleRelevantResults(), 2)
	if len(got) != 2 {
		t.Fatalf("applyLimit(_, 2) returned %d items, want 2", len(got))
	}
	if got[0].ID != "feat-aaa" || got[1].ID != "bug-bbb" {
		t.Errorf("applyLimit did not preserve rank order: %+v", got)
	}
}

func TestApplyLimit_ZeroMeansUnbounded(t *testing.T) {
	items := sampleRelevantResults()
	got := applyLimit(items, 0)
	if len(got) != len(items) {
		t.Fatalf("applyLimit(_, 0) returned %d items, want unbounded %d", len(got), len(items))
	}
}

func TestApplyLimit_LimitGreaterThanLenReturnsAll(t *testing.T) {
	items := sampleRelevantResults()
	got := applyLimit(items, 100)
	if len(got) != len(items) {
		t.Fatalf("applyLimit(_, 100) returned %d items, want all %d", len(got), len(items))
	}
}

func TestFilterResults_ByType(t *testing.T) {
	got := filterResults(sampleRelevantResults(), "feature", "", 0)
	if len(got) != 2 {
		t.Fatalf("filterResults type=feature returned %d items, want 2", len(got))
	}
	for _, r := range got {
		if r.Type != "feature" {
			t.Errorf("filterResults type=feature leaked non-feature item %+v", r)
		}
	}
}

func TestFilterResults_ByStatus(t *testing.T) {
	got := filterResults(sampleRelevantResults(), "", "todo", 0)
	if len(got) != 2 {
		t.Fatalf("filterResults status=todo returned %d items, want 2", len(got))
	}
	for _, r := range got {
		if r.Status != "todo" {
			t.Errorf("filterResults status=todo leaked non-todo item %+v", r)
		}
	}
}

func TestFilterResults_ByMinScore(t *testing.T) {
	got := filterResults(sampleRelevantResults(), "", "", 3.5)
	if len(got) != 2 {
		t.Fatalf("filterResults minScore=3.5 returned %d items, want 2 (scores 5.0, 4.0)", len(got))
	}
	for _, r := range got {
		if r.Score < 3.5 {
			t.Errorf("filterResults minScore=3.5 leaked low-score item %+v", r)
		}
	}
}

func TestFilterResults_CombinedFilters(t *testing.T) {
	got := filterResults(sampleRelevantResults(), "feature", "todo", 0)
	if len(got) != 1 || got[0].ID != "feat-ddd" {
		t.Fatalf("filterResults type=feature status=todo = %+v, want only feat-ddd", got)
	}
}

func TestFilterResults_NoFiltersReturnsAll(t *testing.T) {
	items := sampleRelevantResults()
	got := filterResults(items, "", "", 0)
	if len(got) != len(items) {
		t.Fatalf("filterResults with no filters returned %d items, want %d", len(got), len(items))
	}
}

func TestPrintRelevantCompact_OneLinePerItem(t *testing.T) {
	items := sampleRelevantResults()[:2]
	out := captureStdout(t, func() {
		printRelevantCompact(items, len(items), len(items))
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("printRelevantCompact produced %d lines for 2 items, want 2 (no truncation summary): %q", len(lines), out)
	}
	for i, r := range items {
		line := lines[i]
		for _, want := range []string{r.ID, r.Type, r.Status, r.Title} {
			if !strings.Contains(line, want) {
				t.Errorf("compact line %q missing %q", line, want)
			}
		}
	}
}

func TestPrintRelevantCompact_TruncationSummaryLine(t *testing.T) {
	items := sampleRelevantResults()[:2]
	out := captureStdout(t, func() {
		printRelevantCompact(items, 2, 5)
	})
	if !strings.Contains(out, "showing 2 of 5") {
		t.Errorf("expected truncation summary mentioning 'showing 2 of 5', got: %q", out)
	}
	if !strings.Contains(out, "--limit") {
		t.Errorf("expected truncation summary to mention --limit, got: %q", out)
	}
}

func TestPrintRelevantCompact_NoSummaryWhenNotTruncated(t *testing.T) {
	items := sampleRelevantResults()
	out := captureStdout(t, func() {
		printRelevantCompact(items, len(items), len(items))
	})
	if strings.Contains(out, "--limit") {
		t.Errorf("did not expect truncation summary when shown == total, got: %q", out)
	}
}

func TestPrintRelevantText_TruncationSummaryLine(t *testing.T) {
	items := sampleRelevantResults()[:2]
	out := captureStdout(t, func() {
		printRelevantText(items, 2, 5)
	})
	if !strings.Contains(out, "showing 2 of 5") {
		t.Errorf("expected text output to include truncation summary, got: %q", out)
	}
}

func TestPrintRelevantJSON_TruncatedField(t *testing.T) {
	items := sampleRelevantResults()[:2]
	out := captureStdout(t, func() {
		if err := printRelevantJSON(items, 2, 5); err != nil {
			t.Fatalf("printRelevantJSON: %v", err)
		}
	})
	var resp relevantResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if !resp.Truncated {
		t.Error("expected Truncated=true when shown < total")
	}
	if resp.Shown != 2 || resp.Total != 5 {
		t.Errorf("resp.Shown=%d resp.Total=%d, want 2, 5", resp.Shown, resp.Total)
	}
	if len(resp.Results) != 2 {
		t.Errorf("resp.Results has %d items, want 2", len(resp.Results))
	}
}

func TestPrintRelevantJSON_NotTruncatedField(t *testing.T) {
	items := sampleRelevantResults()
	out := captureStdout(t, func() {
		if err := printRelevantJSON(items, len(items), len(items)); err != nil {
			t.Fatalf("printRelevantJSON: %v", err)
		}
	})
	var resp relevantResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if resp.Truncated {
		t.Error("expected Truncated=false when shown == total")
	}
}

func TestRelevantCmd_HasBoundedScopedFlags(t *testing.T) {
	cmd := relevantCmd()
	for _, name := range []string{"limit", "compact", "type", "status", "min-score", "format"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("relevant command missing --%s flag", name)
		}
	}
}

// TestEffectiveFormat_CompactOverridesDefaultJSON covers a piped (non-TTY)
// invocation of `wipnote relevant <q> --compact`: the default format is
// "json" there, but the user's explicit --compact must not be silently
// discarded in favor of JSON.
func TestEffectiveFormat_CompactOverridesDefaultJSON(t *testing.T) {
	got := effectiveFormat("json", true, false)
	if got != "text" {
		t.Errorf("effectiveFormat(json, compact=true, formatExplicit=false) = %q, want text", got)
	}
}

func TestEffectiveFormat_ExplicitFormatJSONWinsOverCompact(t *testing.T) {
	got := effectiveFormat("json", true, true)
	if got != "json" {
		t.Errorf("effectiveFormat(json, compact=true, formatExplicit=true) = %q, want json (explicit --format wins)", got)
	}
}

func TestEffectiveFormat_NoCompactUnaffected(t *testing.T) {
	if got := effectiveFormat("json", false, false); got != "json" {
		t.Errorf("effectiveFormat(json, compact=false, ...) = %q, want json", got)
	}
	if got := effectiveFormat("text", false, false); got != "text" {
		t.Errorf("effectiveFormat(text, compact=false, ...) = %q, want text", got)
	}
}

func TestRelevantCmd_DefaultLimitIsBounded(t *testing.T) {
	cmd := relevantCmd()
	f := cmd.Flags().Lookup("limit")
	if f == nil {
		t.Fatal("missing --limit flag")
	}
	if f.DefValue == "0" {
		t.Error("--limit default must not be 0 (unbounded) — default output must be bounded per GH#151")
	}
}
