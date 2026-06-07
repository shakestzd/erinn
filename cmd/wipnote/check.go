package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/guardprofile"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/spf13/cobra"
)

// gateResult holds the outcome of a single quality gate.
type gateResult struct {
	name   string
	passed bool
	err    error
}

func checkCmd() *cobra.Command {
	var goOnly, pythonOnly, skipTests, gateOnly bool

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Run automated quality gate checks",
		Long: `Run quality gate checks for the project.

Detects which languages are present and runs the appropriate gates:
  Go:     go build ./...  |  go vet ./...  |  go test ./...
  Python: uv run ruff check --fix  |  uv run ruff format  |  uv run mypy src/  |  uv run pytest

Launch-readiness contention gate (plan-ae0c37b2, feat-156e0a1a):

  The SQLITE_BUSY contention stress fixture is a launch gate, not a
  routine quality gate — it is heavy (20 producers × 30 seconds × 3
  consecutive runs) and is skipped by default to keep iteration fast.
  Run it explicitly before tagging a release:

      go test -run TestSQLiteContentionStress -count=3 ./cmd/wipnote/

  Pass criterion: ZERO SQLITE_BUSY from first-party producers
  (hook_writer / indexer / cli_mutation / writer_service) across all
  three runs. External producers (MCP, user-installed tools) are not
  gated — see the boundary inventory in cmd/wipnote/sqlite_write_boundary_test.go.

  This complements the always-on writable-open boundary
  (TestWritableDBOpenBoundary) which fails CI if any direct writable
  open is added in hook/indexer/receiver/event-capture paths.

Returns exit code 0 if all gates pass, 1 if any fail.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectRoot, err := resolveProjectRoot()
			if err != nil {
				return err
			}
			if gateOnly {
				if goOnly || pythonOnly || skipTests {
					return fmt.Errorf("--gate runs the full project gate and cannot be combined with --go-only, --python-only, or --skip-tests")
				}
				sessionID := hooks.EnvSessionID("")
				agentID := dbpkg.NormaliseAgentID(os.Getenv("WIPNOTE_AGENT_ID"))
				workItemID := activeWorkItemForGate(sessionID, agentID)
				result, err := runSessionGate(projectRoot, sessionID, workItemID, "check", guardprofile.PhaseQuality, os.Stdout, os.Stderr)
				if err != nil {
					return err
				}
				if result.Record != nil {
					fmt.Printf("\nRecorded gate result for session %s", result.Record.SessionID)
					if result.Record.WorkItemID != "" {
						fmt.Printf(" (work item %s)", result.Record.WorkItemID)
					}
					fmt.Printf(" with signature %s\n", truncate(result.Record.Signature, 12))
				}
				if !result.Passed {
					return fmt.Errorf("quality gates failed — no passing session-local gate record was written")
				}
				defer printContentionGateReminder()
				return nil
			}

			var results []gateResult
			ranAny := false

			if !pythonOnly && hasGoProject(projectRoot) {
				ranAny = true
				results = append(results, runGoGates(projectRoot, skipTests)...)
			}

			if !pythonOnly && hasNodeProject(projectRoot) {
				ranAny = true
			}

			if !goOnly && hasPythonProject(projectRoot) {
				ranAny = true
				results = append(results, runPythonGates(projectRoot, skipTests)...)
			}

			if !ranAny {
				fmt.Println("No supported project detected (Go: look for go.mod at project root or subdirectories; Python: src/python; Node: package.json).")
				return nil
			}

			// Slice-10 reminder: even when `wipnote check` passes the
			// routine gates, the contention stress fixture must be run
			// before a release. We surface this as a soft reminder
			// rather than a hard failure so iteration speed stays high.
			defer printContentionGateReminder()

			return printResults(results)
		},
	}

	cmd.Flags().BoolVar(&goOnly, "go-only", false, "Run Go quality gates only")
	cmd.Flags().BoolVar(&pythonOnly, "python-only", false, "Run Python quality gates only")
	cmd.Flags().BoolVar(&skipTests, "skip-tests", false, "Skip test execution (run lint/build only)")
	cmd.Flags().BoolVar(&gateOnly, "gate", false, "Run the full project quality gate and write a session-local gate record")

	cmd.AddCommand(checkOrphansCmd())
	cmd.AddCommand(checkIncompleteCmd())
	cmd.AddCommand(checkCrossProjectCmd())
	cmd.AddCommand(checkHostPathsCmd())
	cmd.AddCommand(checkDupsCmd())
	cmd.AddCommand(checkAcceptedAdvisoryCmd())
	return cmd
}

// checkAcceptedAdvisoryCmd surfaces work items that were completed via the
// provenance-gate override (feat-7b593272). Each such item carries an
// `accepted_advisory` property recording the operator rationale for
// completing a code-bearing item with zero linked source commits. This is
// the compliance/audit surface for those overrides.
//
// Reads canonical HTML (graph.LoadDir) so it works on a fresh clone with no
// SQLite read index, mirroring `wipnote check dups`.
func checkAcceptedAdvisoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accepted-advisory",
		Short: "Report items completed via the zero-commit provenance override",
		Long: `Scan features, bugs, and spikes for the accepted_advisory marker
recorded when an item was completed with --accepted-advisory (an audited
override of the zero-commit provenance gate).

Reads canonical HTML, so it works even when the SQLite read index is absent.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCheckAcceptedAdvisory(os.Stdout)
		},
	}
}

type advisoryItem struct {
	id, title, itemType, reason string
}

func runCheckAcceptedAdvisory(w io.Writer) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	var items []advisoryItem
	for _, d := range []struct{ subdir, typeName string }{
		{"features", "feature"},
		{"bugs", "bug"},
		{"spikes", "spike"},
	} {
		nodes, lerr := graph.LoadDir(filepath.Join(dir, d.subdir))
		if lerr != nil {
			// Missing subdir / unreadable store: graceful skip, never fail.
			continue
		}
		for _, n := range nodes {
			if reason := acceptedAdvisoryOf(n); reason != "" {
				items = append(items, advisoryItem{
					id: n.ID, title: n.Title,
					itemType: d.typeName, reason: reason,
				})
			}
		}
	}

	if len(items) == 0 {
		fmt.Fprintln(w, "No items completed via the accepted-advisory provenance override.")
		return nil
	}

	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })

	fmt.Fprintf(w, "Found %d item(s) completed via accepted-advisory (zero-commit provenance override):\n", len(items))
	fmt.Fprintln(w, strings.Repeat("-", 72))
	for _, it := range items {
		fmt.Fprintf(w, "  %-7s  %-20s  %s\n", it.itemType, it.id, truncate(it.title, 34))
		fmt.Fprintf(w, "           reason: %s\n", it.reason)
	}
	return nil
}

// checkDupsCmd surfaces needs-triage-dup clusters created by the
// dedup-at-create flow (slice-6, feat-e8879220). It scans canonical HTML
// (graph.LoadDir) — NOT the SQLite index — so it works on a fresh clone with
// no read index built, mirroring `wipnote check orphans`. A cluster is any
// item carrying a relates_to edge whose Title begins with "needs-triage-dup".
func checkDupsCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "dups",
		Short: "Report items flagged as possible duplicates (needs-triage-dup)",
		Long: `Scan features and bugs for the needs-triage-dup marker auto-attached
at create time when a strong title+description similarity match was found.

Reads canonical HTML, so it works even when the SQLite read index is absent.
Use --strict to return exit code 1 if any unresolved duplicate clusters exist.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCheckDups(strict)
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false,
		"exit code 1 if needs-triage-dup clusters exist")
	return cmd
}

func runCheckDups(strict bool) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	type dupItem struct {
		id, title, relatesTo, itemType string
	}
	var dups []dupItem

	for _, d := range []struct{ subdir, typeName string }{
		{"features", "feature"},
		{"bugs", "bug"},
	} {
		nodes, lerr := graph.LoadDir(filepath.Join(dir, d.subdir))
		if lerr != nil {
			// Missing subdir / unreadable store: graceful skip, never fail.
			continue
		}
		for _, n := range nodes {
			for _, e := range n.Edges[string(models.RelRelatesTo)] {
				isDup := strings.HasPrefix(e.Title, needsTriageDupTag) ||
					(e.Properties != nil && e.Properties["tag"] == needsTriageDupTag)
				if isDup {
					dups = append(dups, dupItem{
						id: n.ID, title: n.Title,
						relatesTo: e.TargetID, itemType: d.typeName,
					})
				}
			}
		}
	}

	if len(dups) == 0 {
		fmt.Println("No needs-triage-dup clusters found.")
		return nil
	}

	fmt.Printf("Found %d possible-duplicate item(s) needing triage:\n", len(dups))
	fmt.Println(strings.Repeat("-", 70))
	for _, d := range dups {
		fmt.Printf("  %-7s  %-18s  ~ %-18s  %s\n",
			d.itemType, d.id, d.relatesTo, truncate(d.title, 30))
	}
	fmt.Printf("\nResolve by closing the duplicate or removing the relates_to edge\n" +
		"(e.g. 'wipnote link remove <id> <target> relates_to').\n")

	if strict {
		os.Exit(1)
	}
	return nil
}

// resolveProjectRoot finds the project root from the .wipnote directory.
func resolveProjectRoot() (string, error) {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		// Fall back to CWD if not in an wipnote project.
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return "", fmt.Errorf("get working directory: %w", cwdErr)
		}
		return cwd, nil
	}
	return filepath.Dir(wipnoteDir), nil
}

func hasGoProject(root string) bool {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Join(root, "packages", "go", "go.mod"))
	return err == nil
}

func hasNodeProject(root string) bool {
	_, err := os.Stat(filepath.Join(root, "package.json"))
	return err == nil
}

func hasPythonProject(root string) bool {
	_, err := os.Stat(filepath.Join(root, "src", "python"))
	return err == nil
}

// runGate executes a command, capturing its combined output, and returns a gateResult.
func runGate(name, dir string, args ...string) gateResult {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return gateResult{name: name, passed: err == nil, err: err}
}

func runGoGates(root string, skipTests bool) []gateResult {
	goDir := filepath.Join(root, "packages", "go")
	gates := []gateResult{
		runGate("go build", goDir, "go", "build", "-buildvcs=false", "./..."),
		runGate("go vet", goDir, "go", "vet", "./..."),
	}
	if !skipTests {
		gates = append(gates, runGate("go test", goDir, "go", "test", "-buildvcs=false", "./..."))
	}
	return gates
}

func runPythonGates(root string, skipTests bool) []gateResult {
	gates := []gateResult{
		runGate("ruff check", root, "uv", "run", "ruff", "check", "--fix"),
		runGate("ruff format", root, "uv", "run", "ruff", "format"),
		runGate("mypy", root, "uv", "run", "mypy", "src/"),
	}
	if !skipTests {
		gates = append(gates, runGate("pytest", root, "uv", "run", "pytest"))
	}
	return gates
}

// printContentionGateReminder prints the slice-10 launch readiness
// reminder after every `wipnote check` run. The contention stress
// fixture is too heavy to run by default (~90 seconds for 3 passes) so
// we surface it as a soft notice rather than wiring it into the gate
// table. The full release flow (./scripts/deploy-all.sh) is expected to
// invoke the stress test explicitly before tagging.
func printContentionGateReminder() {
	fmt.Println()
	fmt.Println("Launch readiness (plan-ae0c37b2):")
	fmt.Println("  Run the SQLITE_BUSY contention stress fixture before tagging a release:")
	fmt.Println("    go test -run TestSQLiteContentionStress -count=3 ./cmd/wipnote/")
	fmt.Println("  Pass criterion: zero first-party SQLITE_BUSY across 3 consecutive runs.")
}

// printResults displays a summary table and returns an error if any gate failed.
func printResults(results []gateResult) error {
	fmt.Println()
	fmt.Println("Quality Gate Results")
	fmt.Println("--------------------")

	allPassed := true
	for _, r := range results {
		status := "\033[32mPASS\033[0m"
		if !r.passed {
			status = "\033[31mFAIL\033[0m"
			allPassed = false
		}
		fmt.Printf("  [%s]  %s\n", status, r.name)
	}

	fmt.Println()
	if allPassed {
		fmt.Println("\033[32mAll quality gates passed.\033[0m")
		return nil
	}
	fmt.Println("\033[31mOne or more quality gates failed.\033[0m")
	return fmt.Errorf("quality gates failed — see details above\nRun individual checks with 'wipnote check --go-only' to isolate failures.")
}
