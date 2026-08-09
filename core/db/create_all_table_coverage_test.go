package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// createTableNameRE extracts the table name from a
// `CREATE TABLE IF NOT EXISTS <name> (` DDL literal. Matches regardless of
// surrounding whitespace/newlines, which is what raw backtick DDL strings in
// this package use.
var createTableNameRE = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+(\w+)`)

// preVersioningTables lists every table that was already declared in
// CreateAllTables/CreateOtelTables at the commit that introduced schema
// versioning (e1afe5ef1, 2026-06-07, "feat: carve core/ go.mod module
// boundary + repo restructure" — currentSchemaVersion started at 12 with
// steps 001-012 already registered).
//
// Every database that exists today has already passed through that commit's
// step 1 (stepCreateBaseTables) at least once: either directly (a fresh v0
// database migrating today runs the full chain including step 1), or
// historically, because the PRE-versioning code unconditionally re-ran
// CreateAllTables/CreateOtelTables on EVERY Open (see the
// currentSchemaVersion doc comment) — so any database that predates
// versioning already had every table below before versioning even began
// tracking schema state. There is no reachable database state in which one
// of these tables is absent, so none of them need a dedicated versioned
// step.
//
// This is a closed, historical list — it must NOT be extended just because a
// table is convenient to add to CreateAllTables today. A table added to
// CreateAllTables/CreateOtelTables AFTER this baseline needs its own
// versioned step (version > 1) that also creates it, full stop. That is
// exactly the defect TestCreateAllTables_EveryTableHasMigrationCoverage
// guards: claim_episodes (bug-8af46da3) was added straight to CreateAllTables
// with neither a step nor an entry here, and silently never reached any
// database that had already migrated past version 1.
var preVersioningTables = map[string]bool{
	"agent_events":            true,
	"features":                true,
	"sessions":                true,
	"tracks":                  true,
	"claims":                  true,
	"graph_edges":             true,
	"git_commits":             true,
	"live_events":             true,
	"agent_lineage_trace":     true,
	"messages":                true,
	"tool_calls":              true,
	"feature_files":           true,
	"session_files":           true,
	"agent_presence":          true,
	"metadata":                true,
	"plan_feedback":           true,
	"gate_records":            true,
	"otel_signals":            true,
	"otel_resource_attrs":     true,
	"otel_session_rollup":     true,
	"pending_subagent_starts": true,
}

// TestCreateAllTables_EveryTableHasMigrationCoverage guards against
// bug-8af46da3's defect class: a table declared inside CreateAllTables or
// CreateOtelTables — the create-all path, reachable ONLY from step 1
// (stepCreateBaseTables), which runs exactly once, the first time a database
// goes from user_version 0 to current — with no other versioned step
// (version > 1) that also creates it.
//
// Such a table is silently absent, forever, from every database that had
// already migrated past version 1 before the table was added to source. No
// error at migration time — the failure surfaces later and elsewhere,
// whenever something tries to use the table (for claim_episodes: every
// reindex, with a missing-table error).
//
// This test discovers table names by parsing the real source (go/ast, not a
// hand-maintained duplicate list) so it fails the moment a future table is
// added to the create-all path without matching coverage — and it names the
// table in the failure message.
func TestCreateAllTables_EveryTableHasMigrationCoverage(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate this package's source directory")
	}
	dir := filepath.Dir(thisFile)

	// 1. Every table the create-all path declares.
	declared := make(map[string]bool)
	mergeTableNames(t, declared, extractTableNamesInFuncs(t,
		filepath.Join(dir, "schema.go"), map[string]bool{"CreateAllTables": true}))
	mergeTableNames(t, declared, extractTableNamesInFuncs(t,
		filepath.Join(dir, "otel_schema.go"), map[string]bool{"CreateOtelTables": true}))
	if len(declared) == 0 {
		t.Fatal("discovered zero tables in CreateAllTables/CreateOtelTables — extractor is broken, not the schema")
	}

	// 2. Every table created by a VERSIONED step (version > 1). Resolve the
	// real Go function backing each step's apply field via reflection (not a
	// hardcoded function-name list), then group by the source file that
	// function actually lives in.
	baseTablesFuncPtr := reflect.ValueOf(stepCreateBaseTables).Pointer()
	funcNamesByFile := make(map[string]map[string]bool)
	for _, step := range migrations {
		if step.version <= 1 {
			continue
		}
		fnVal := reflect.ValueOf(step.apply)
		if fnVal.Pointer() == baseTablesFuncPtr {
			// Defensive: never happens today (stepCreateBaseTables is always
			// version 1), but guards against a future reordering silently
			// exempting the create-all path from its own check.
			continue
		}
		rtFn := runtime.FuncForPC(fnVal.Pointer())
		if rtFn == nil {
			t.Fatalf("step %q (v%d): runtime.FuncForPC returned nil", step.name, step.version)
		}
		file, _ := rtFn.FileLine(fnVal.Pointer())
		fullName := rtFn.Name()
		short := fullName[strings.LastIndex(fullName, ".")+1:]
		if funcNamesByFile[file] == nil {
			funcNamesByFile[file] = make(map[string]bool)
		}
		funcNamesByFile[file][short] = true
	}

	covered := make(map[string]bool)
	for file, funcNames := range funcNamesByFile {
		mergeTableNames(t, covered, extractTableNamesInFuncs(t, file, funcNames))
	}

	// 3. Every declared table must be either pre-versioning (provably present
	// in every reachable database) or covered by a versioned step.
	var uncovered []string
	for table := range declared {
		if preVersioningTables[table] {
			continue
		}
		if covered[table] {
			continue
		}
		uncovered = append(uncovered, table)
	}

	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Fatalf("table(s) declared in CreateAllTables/CreateOtelTables have no versioned migration "+
			"step (version > 1) creating them, and are not in the pre-versioning baseline "+
			"(preVersioningTables in this file): %v\n\n"+
			"A database that migrated past version 1 before this table existed in source will "+
			"NEVER receive it (bug-8af46da3's exact defect shape). Fix: add a migrationStep "+
			"whose apply function issues `CREATE TABLE IF NOT EXISTS <table>` (see "+
			"stepClaimEpisodesTable / stepArchCards / stepRecapsTable for the pattern), bump "+
			"currentSchemaVersion, and append it to the migrations slice.", uncovered)
	}
}

// mergeTableNames copies every key of src into dst.
func mergeTableNames(t *testing.T, dst, src map[string]bool) {
	t.Helper()
	for k := range src {
		dst[k] = true
	}
}

// extractTableNamesInFuncs parses the Go source file at path and returns the
// set of table names created (via a `CREATE TABLE IF NOT EXISTS <name>`
// literal) anywhere within the bodies of the top-level functions named in
// wantFuncs.
func extractTableNamesInFuncs(t *testing.T, path string, wantFuncs map[string]bool) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	found := make(map[string]bool)
	matchedAny := make(map[string]bool)
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !wantFuncs[fn.Name.Name] {
			continue
		}
		matchedAny[fn.Name.Name] = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, m := range createTableNameRE.FindAllStringSubmatch(lit.Value, -1) {
				found[m[1]] = true
			}
			return true
		})
	}
	for name := range wantFuncs {
		if !matchedAny[name] {
			t.Fatalf("function %q not found (or has no body) in %s — extractor cannot verify create-all coverage", name, path)
		}
	}
	return found
}
