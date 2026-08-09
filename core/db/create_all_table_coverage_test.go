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

// createIndexNameRE extracts the index name from a
// `CREATE [UNIQUE] INDEX IF NOT EXISTS <name>` DDL literal.
var createIndexNameRE = regexp.MustCompile(`(?i)CREATE\s+(?:UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS\s+(\w+)`)

// indexCarrierFuncs are the functions that versioned step 002_create_indexes
// applies. An index declared in either of them reaches every database, because
// step 002 runs for any database below version 2 and CREATE INDEX IF NOT
// EXISTS is idempotent for the rest.
var indexCarrierFuncs = map[string]map[string]bool{
	"schema.go":      {"CreateAllIndexes": true},
	"otel_schema.go": {"CreateOtelIndexes": true},
}

// TestCreateAllTables_EveryIndexHasMigrationCoverage is the index-level twin of
// the table guard above, added for bug-0fc17d53.
//
// The table guard parses CREATE TABLE only, so an INDEX added to the create-all
// path slipped straight through it — which is exactly how the UNIQUE index on
// otel_signals(span_id) landed inside CreateOtelTables. That placement is the
// same defect one level down: the create-all path is reachable only from step 1,
// which runs once, on a database going from user_version 0 to current. An index
// added there never reaches a database that has already migrated.
//
// For that particular index the consequence was worse than absence. It DID get
// created on every database that ran the create-all path, and because span_id is
// not a signal identity, it silently discarded 56% of all telemetry through the
// writer's INSERT OR IGNORE. The guard therefore protects against both halves:
// an index that never arrives, and an index that arrives only somewhere.
func TestCreateAllTables_EveryIndexHasMigrationCoverage(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate this package's source directory")
	}
	dir := filepath.Dir(thisFile)

	// The guard's premise: step 002 really does apply the two carrier
	// functions. If that wiring is ever removed, indexes declared in them
	// stop being covered and this test would silently start passing for the
	// wrong reason.
	assertStepCreateIndexesCalls(t, filepath.Join(dir, "migrations.go"),
		[]string{"CreateAllIndexes", "CreateOtelIndexes"})

	// 1. Indexes declared by the create-all path.
	declared := make(map[string]bool)
	mergeTableNames(t, declared, extractIndexNamesInFuncs(t,
		filepath.Join(dir, "schema.go"), map[string]bool{"CreateAllTables": true}))
	mergeTableNames(t, declared, extractIndexNamesInFuncs(t,
		filepath.Join(dir, "otel_schema.go"), map[string]bool{"CreateOtelTables": true}))

	// 2. Indexes covered by a versioned step — the two carrier functions step
	// 002 applies, plus any index a later step creates directly.
	covered := make(map[string]bool)
	for file, funcs := range indexCarrierFuncs {
		mergeTableNames(t, covered, extractIndexNamesInFuncs(t, filepath.Join(dir, file), funcs))
	}
	baseTablesFuncPtr := reflect.ValueOf(stepCreateBaseTables).Pointer()
	for _, step := range migrations {
		if step.version <= 1 {
			continue
		}
		fnVal := reflect.ValueOf(step.apply)
		if fnVal.Pointer() == baseTablesFuncPtr {
			continue
		}
		rtFn := runtime.FuncForPC(fnVal.Pointer())
		if rtFn == nil {
			t.Fatalf("step %q (v%d): runtime.FuncForPC returned nil", step.name, step.version)
		}
		file, _ := rtFn.FileLine(fnVal.Pointer())
		fullName := rtFn.Name()
		short := fullName[strings.LastIndex(fullName, ".")+1:]
		mergeTableNames(t, covered, extractIndexNamesInFuncs(t, file, map[string]bool{short: true}))
	}

	var uncovered []string
	for index := range declared {
		if !covered[index] {
			uncovered = append(uncovered, index)
		}
	}
	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Fatalf("index(es) declared in CreateAllTables/CreateOtelTables have no versioned migration "+
			"coverage: %v\n\n"+
			"The create-all path runs only for a database going from user_version 0 to current, so an "+
			"index declared there reaches fresh installs and nothing else — and an index that exists on "+
			"some databases but not others changes what writes succeed, not just what queries cost "+
			"(bug-0fc17d53). Fix: declare it in CreateAllIndexes / CreateOtelIndexes, and if existing "+
			"databases need it too, add a versioned step that issues the same CREATE INDEX IF NOT EXISTS.",
			uncovered)
	}
}

// assertStepCreateIndexesCalls verifies stepCreateIndexes still calls each of
// wantCalls, so the coverage argument above stays true.
func assertStepCreateIndexesCalls(t *testing.T, migrationsFile string, wantCalls []string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, migrationsFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", migrationsFile, err)
	}
	called := make(map[string]bool)
	found := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != "stepCreateIndexes" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				called[id.Name] = true
			}
			return true
		})
	}
	if !found {
		t.Fatalf("stepCreateIndexes not found in %s — the index-coverage guard's premise is gone", migrationsFile)
	}
	for _, want := range wantCalls {
		if !called[want] {
			t.Fatalf("stepCreateIndexes no longer calls %s(); indexes declared there are no longer "+
				"applied by a versioned step, so the index-coverage guard would pass vacuously", want)
		}
	}
}

// extractIndexNamesInFuncs is extractTableNamesInFuncs for CREATE INDEX.
func extractIndexNamesInFuncs(t *testing.T, path string, wantFuncs map[string]bool) map[string]bool {
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
			for _, m := range createIndexNameRE.FindAllStringSubmatch(lit.Value, -1) {
				found[m[1]] = true
			}
			return true
		})
	}
	for name := range wantFuncs {
		if !matchedAny[name] {
			t.Fatalf("function %q not found (or has no body) in %s — extractor cannot verify index coverage", name, path)
		}
	}
	return found
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
