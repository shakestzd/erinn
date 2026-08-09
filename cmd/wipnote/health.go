package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/spf13/cobra"
)

const (
	moduleLimitWarn = 300
	moduleLimitFail = 500
	funcLimitWarn   = 30
	funcLimitFail   = 50
)

type violation struct {
	Level string `json:"level"`
	File  string `json:"file"`
	Name  string `json:"name,omitempty"`
	Line  int    `json:"line,omitempty"`
	Count int    `json:"count"`
	Limit int    `json:"limit"`
	Kind  string `json:"kind"`
}

type healthResult struct {
	ModulesScanned   int         `json:"modules_scanned"`
	FunctionsScanned int         `json:"functions_scanned"`
	Violations       []violation `json:"violations"`
	Warnings         int         `json:"warnings"`
	Failures         int         `json:"failures"`
}

func healthCmd() *cobra.Command {
	var path string
	var goOnly, pythonOnly, jsonOut, failOnWarn bool
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check code health metrics (module sizes, function lengths)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runHealth(path, goOnly, pythonOnly, jsonOut, failOnWarn)
		},
	}
	cmd.Flags().StringVar(&path, "path", ".", "directory to scan")
	cmd.Flags().BoolVar(&goOnly, "go-only", false, "scan Go files only")
	cmd.Flags().BoolVar(&pythonOnly, "python-only", false, "scan Python files only")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	cmd.Flags().BoolVar(&failOnWarn, "fail-on-warning", false, "treat warnings as failures")
	return cmd
}

func runHealth(path string, goOnly, pythonOnly, jsonOut, failOnWarn bool) error {
	result := &healthResult{}
	if !pythonOnly {
		walkFiles(path, ".go", result, analyzeGoFile)
	}
	if !goOnly {
		walkFiles(path, ".py", result, analyzePythonFile)
	}
	for _, v := range result.Violations {
		if v.Level == "FAIL" {
			result.Failures++
		} else {
			result.Warnings++
		}
	}
	if jsonOut {
		// Surface the contention counters in the JSON output too so
		// dashboards and CI can read them without an extra status call.
		// Embedded under a top-level "runtime" key to keep the existing
		// schema intact for callers that only consume violations.
		envelope := struct {
			healthResult
			Runtime runtimeHealth `json:"runtime"`
		}{
			healthResult: *result,
			Runtime:      collectRuntimeHealth(),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(envelope)
	}
	// Slice-10 contention observability: surface runtime SQLITE_BUSY
	// counters + writer queue depth BEFORE the code-size report so
	// `wipnote health | head` captures the launch-gate signal even
	// when the user pipes through truncation.
	printRuntimeHealth()
	printHealthReport(*result)
	if result.Failures > 0 {
		return fmt.Errorf("health check failed: %d failure(s) — see violations above\nUse 'wipnote health --json' for machine-readable output.", result.Failures)
	}
	if failOnWarn && result.Warnings > 0 {
		return fmt.Errorf("health check failed: %d warning(s)", result.Warnings)
	}
	return nil
}

// runtimeHealth is the JSON envelope for the runtime portion of the
// `wipnote health --json` output. Subsystems is keyed by subsystem label.
type runtimeHealth struct {
	JournalMode         string              `json:"journal_mode,omitempty"`
	FirstPartyBusyTotal int64               `json:"first_party_busy_total"`
	BusySubsystems      map[string]int64    `json:"busy_subsystems,omitempty"`
	WriterQueue         WriterServiceStatus `json:"writer_queue"`
}

// collectRuntimeHealth returns a snapshot of the slice-10 contention
// observability counters for embedding in `wipnote health --json`.
func collectRuntimeHealth() runtimeHealth {
	subs := dbpkg.BusyCounts()
	stringy := make(map[string]int64, len(subs))
	for k, v := range subs {
		stringy[string(k)] = v
	}
	return runtimeHealth{
		FirstPartyBusyTotal: dbpkg.FirstPartyBusyTotal(),
		BusySubsystems:      stringy,
		WriterQueue:         readWriterServiceStatus(writerService.queue),
	}
}

// printRuntimeHealth writes a 3-4 line runtime block to stdout before
// the code-size report so `head -10` captures the launch-gate signal.
func printRuntimeHealth() {
	rt := collectRuntimeHealth()
	fmt.Println("Runtime Health")
	fmt.Println("--------------")
	fmt.Printf("SQLITE_BUSY (first-party): %d\n", rt.FirstPartyBusyTotal)
	for k, v := range rt.BusySubsystems {
		fmt.Printf("  %-16s %d\n", k, v)
	}
	fmt.Printf("Writer queue: state=%s depth=%d/%d rejected=%d errors=%d\n",
		rt.WriterQueue.State, rt.WriterQueue.Depth, rt.WriterQueue.Capacity,
		rt.WriterQueue.Rejected, rt.WriterQueue.Errors)
	fmt.Println()
}

func printHealthReport(r healthResult) {
	fmt.Printf("Code Health Report\n==================\nModules scanned:   %d\nFunctions scanned: %d\n",
		r.ModulesScanned, r.FunctionsScanned)
	if len(r.Violations) == 0 {
		fmt.Println("\nNo violations found.\n\nStatus: OK")
		return
	}
	fmt.Println("\nViolations:")
	for _, v := range r.Violations {
		if v.Kind == "module" {
			fmt.Printf("  %-6s %s: %d lines (limit: %d)\n", v.Level, v.File, v.Count, v.Limit)
		} else {
			fmt.Printf("  %-6s %s (%s:%d): %d lines (limit: %d)\n",
				v.Level, v.Name, v.File, v.Line, v.Count, v.Limit)
		}
	}
	fmt.Printf("\nStatus: %d warning(s), %d failure(s)\n", r.Warnings, r.Failures)
}

func walkFiles(root, ext string, result *healthResult, analyze func(string, *healthResult) error) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case "vendor", "testdata", "__pycache__", ".venv", "node_modules", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ext) {
			_ = analyze(path, result)
		}
		return nil
	})
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

func addViolation(result *healthResult, kind, path, name string, line, count, warnLim, failLim int) {
	level, limit := "", 0
	if count > failLim {
		level, limit = "FAIL", failLim
	} else if count > warnLim {
		level, limit = "WARN", warnLim
	}
	if level == "" {
		return
	}
	result.Violations = append(result.Violations, violation{
		Level: level, File: path, Name: name, Line: line, Count: count, Limit: limit, Kind: kind,
	})
}

func analyzeGoFile(path string, result *healthResult) error {
	lines, err := readLines(path)
	if err != nil {
		return nil
	}
	result.ModulesScanned++
	addViolation(result, "module", path, "", 0, len(lines), moduleLimitWarn, moduleLimitFail)

	type frame struct {
		name             string
		startLine, depth int
	}
	var stack []frame
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") && len(stack) == 0 {
			stack = append(stack, frame{extractGoFuncName(trimmed), i + 1, countBraces(line)})
			continue
		}
		if len(stack) > 0 {
			top := &stack[len(stack)-1]
			top.depth += countBraces(line)
			if top.depth <= 0 {
				result.FunctionsScanned++
				addViolation(result, "function", path, top.name, top.startLine, i+1-top.startLine+1, funcLimitWarn, funcLimitFail)
				stack = stack[:len(stack)-1]
			}
		}
	}
	return nil
}

func analyzePythonFile(path string, result *healthResult) error {
	lines, err := readLines(path)
	if err != nil {
		return nil
	}
	result.ModulesScanned++
	addViolation(result, "module", path, "", 0, len(lines), moduleLimitWarn, moduleLimitFail)

	type frame struct {
		name              string
		startLine, indent int
	}
	var stack []frame
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := countIndent(line)
		trimmed := strings.TrimSpace(line)
		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			result.FunctionsScanned++
			addViolation(result, "function", path, top.name, top.startLine, i-top.startLine, funcLimitWarn, funcLimitFail)
		}
		if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "async def ") {
			stack = append(stack, frame{extractPyFuncName(trimmed), i + 1, indent})
		}
	}
	for _, f := range stack {
		result.FunctionsScanned++
		addViolation(result, "function", path, f.name, f.startLine, len(lines)-f.startLine+1, funcLimitWarn, funcLimitFail)
	}
	return nil
}

func countBraces(line string) int {
	n, inStr := 0, false
	for _, ch := range line {
		switch ch {
		case '"':
			inStr = !inStr
		case '{':
			if !inStr {
				n++
			}
		case '}':
			if !inStr {
				n--
			}
		}
	}
	return n
}

func countIndent(line string) int {
	n := 0
OuterLoop:
	for _, ch := range line {
		switch ch {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			break OuterLoop
		}
	}
	return n
}

func extractGoFuncName(line string) string {
	s := strings.TrimPrefix(line, "func ")
	if strings.HasPrefix(s, "(") {
		if end := strings.Index(s, ")"); end >= 0 {
			s = strings.TrimSpace(s[end+1:])
		}
	}
	if idx := strings.IndexAny(s, "(["); idx > 0 {
		return s[:idx]
	}
	return s
}

func extractPyFuncName(line string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(line, "async "), "def ")
	if idx := strings.Index(s, "("); idx > 0 {
		return s[:idx]
	}
	return s
}
