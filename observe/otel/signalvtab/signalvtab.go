// Package signalvtab exposes the canonical per-session telemetry NDJSON
// shards as a read-only SQLite virtual table, with no ingestion step.
//
// On-disk layout (written by observe/otel/sink/ndjson):
//
//	<sessionsDir>/<wipnote-session-id>/events.ndjson
//
// One shard per wipnote session. The virtual table's columns mirror the
// otel_signals table the ingest path currently populates, plus a leading
// "shard" column carrying the directory name that owns each row.
//
// # Why "shard" is a separate column from "session_id"
//
// The obvious assumption — that the shard filename IS the session index — is
// false on the real corpus. A shard directory is named with the *wipnote*
// session id, while the session_id recorded on each line is whatever the
// harness reported (Claude's own session UUID, for example). They are
// different identifiers, and empirically:
//
//   - a single shard holds rows from more than one harness session, and
//   - one harness session's rows are spread across several shards
//     (a resumed session keeps its harness id while wipnote opens a new one).
//
// So session_id equality cannot be resolved to a file by name. shard equality
// can, exactly and for free. The table therefore pushes down:
//
//   - shard = ?       — opens exactly one file, skipping the directory scan
//   - session_id = ?  — cannot prune files, but is applied as a byte-level
//     prefilter so non-matching lines never reach the JSON decoder
//
// # Column pruning
//
// SQLite reports which columns a query actually touches (IndexInfo.ColUsed).
// When neither attrs_json nor resource_attrs_json is referenced, rows are
// decoded without them. On the real corpus the attribute bags are the bulk of
// every line, so this is the difference between decoding ~194MB of JSON and
// scanning past most of it.
//
// # Registration order
//
// vtab.RegisterModule only affects connections opened *after* the call. Any
// query, or even a Ping, before registering leaves the module absent on that
// pooled connection and CREATE VIRTUAL TABLE fails with "no such module".
// Use Open, which sequences this correctly.
package signalvtab

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"modernc.org/sqlite/vtab"
)

// ShardFile is the fixed basename of a shard within a session directory.
const ShardFile = "events.ndjson"

// Column indices. The declared schema below must stay in this order.
const (
	ColShard = iota
	ColSignalID
	ColHarness
	ColSessionID
	ColPromptID
	ColTraceID
	ColSpanID
	ColParentSpan
	ColKind
	ColCanonical
	ColNative
	ColTS
	ColTSMicros
	ColToolName
	ColToolUseID
	ColModel
	ColDecision
	ColDecisionSource
	ColTokensIn
	ColTokensOut
	ColTokensCacheRead
	ColTokensCacheCreation
	ColTokensThought
	ColTokensTool
	ColTokensReasoning
	ColCostUSD
	ColCostSource
	ColDurationMs
	ColSuccess
	ColErrorMsg
	ColAttempt
	ColStatusCode
	ColAttrsJSON
	ColResourceAttrsJSON
	numColumns
)

// declaredSchema is the CREATE TABLE passed to Context.Declare. Column order
// must match the Col* constants above.
const declaredSchema = `CREATE TABLE x(
	shard                 TEXT,
	signal_id             TEXT,
	harness               TEXT,
	session_id            TEXT,
	prompt_id             TEXT,
	trace_id              TEXT,
	span_id               TEXT,
	parent_span           TEXT,
	kind                  TEXT,
	canonical             TEXT,
	native                TEXT,
	ts                    TEXT,
	ts_micros             INTEGER,
	tool_name             TEXT,
	tool_use_id           TEXT,
	model                 TEXT,
	decision              TEXT,
	decision_source       TEXT,
	tokens_in             INTEGER,
	tokens_out            INTEGER,
	tokens_cache_read     INTEGER,
	tokens_cache_creation INTEGER,
	tokens_thought        INTEGER,
	tokens_tool           INTEGER,
	tokens_reasoning      INTEGER,
	cost_usd              REAL,
	cost_source           TEXT,
	duration_ms           INTEGER,
	success               INTEGER,
	error_msg             TEXT,
	attempt               INTEGER,
	status_code           INTEGER,
	attrs_json            TEXT,
	resource_attrs_json   TEXT
)`

// Stats are cumulative counters across every cursor created from a Module.
// They exist so tests can assert on I/O that is otherwise invisible — in
// particular that a shard-scoped query opens exactly one file — and so
// malformed input is observable rather than silently dropped.
type Stats struct {
	FilesOpened     atomic.Int64
	FilesMissing    atomic.Int64
	LinesRead       atomic.Int64
	LinesMalformed  atomic.Int64
	BytesRead       atomic.Int64
	RowsEmitted     atomic.Int64
	RowsPrefiltered atomic.Int64
}

// Snapshot is a plain-value copy of Stats for assertions and reporting.
type Snapshot struct {
	FilesOpened     int64
	FilesMissing    int64
	LinesRead       int64
	LinesMalformed  int64
	BytesRead       int64
	RowsEmitted     int64
	RowsPrefiltered int64
}

// Snapshot reads every counter. Not atomic as a group; intended for use
// between queries, not during one.
func (s *Stats) Snapshot() Snapshot {
	return Snapshot{
		FilesOpened:     s.FilesOpened.Load(),
		FilesMissing:    s.FilesMissing.Load(),
		LinesRead:       s.LinesRead.Load(),
		LinesMalformed:  s.LinesMalformed.Load(),
		BytesRead:       s.BytesRead.Load(),
		RowsEmitted:     s.RowsEmitted.Load(),
		RowsPrefiltered: s.RowsPrefiltered.Load(),
	}
}

// Reset zeroes every counter.
func (s *Stats) Reset() {
	s.FilesOpened.Store(0)
	s.FilesMissing.Store(0)
	s.LinesRead.Store(0)
	s.LinesMalformed.Store(0)
	s.BytesRead.Store(0)
	s.RowsEmitted.Store(0)
	s.RowsPrefiltered.Store(0)
}

// Module is the virtual table module. Construct with NewModule.
type Module struct {
	// defaultRoot is used when CREATE VIRTUAL TABLE supplies no argument.
	defaultRoot string

	// openFile opens one shard. Injectable so tests can observe or fail I/O.
	openFile func(path string) (io.ReadCloser, error)

	// onMalformed is called for every line that fails to decode. The scan
	// continues regardless; this is the "not silently" half of the contract.
	onMalformed func(path string, line int, err error)

	stats Stats

	// disablePushdown makes BestIndex claim no constraints. Only tests set
	// this, to prove the pushdown assertions would fail without pushdown.
	disablePushdown bool
}

// NewModule builds a Module rooted at sessionsDir (typically
// <project>/.wipnote/sessions).
func NewModule(sessionsDir string) *Module {
	return &Module{
		defaultRoot: sessionsDir,
		openFile:    openOS,
		onMalformed: nil,
	}
}

func openOS(path string) (io.ReadCloser, error) { return os.Open(path) }

// Stats returns the module's cumulative counters.
func (m *Module) Stats() *Stats { return &m.stats }

// SetOpener replaces the file opener. Intended for tests.
func (m *Module) SetOpener(fn func(string) (io.ReadCloser, error)) { m.openFile = fn }

// SetMalformedHandler installs a callback invoked once per undecodable line.
// When nil (the default) malformed lines are reported to stderr via the
// package's default logger.
func (m *Module) SetMalformedHandler(fn func(path string, line int, err error)) {
	m.onMalformed = fn
}

// SetPushdownDisabled turns constraint pushdown off. Tests use this to show
// that a pushdown assertion actually depends on pushdown: with it disabled the
// query still returns identical rows, but from a full directory scan.
func (m *Module) SetPushdownDisabled(v bool) { m.disablePushdown = v }

// Create implements vtab.Module.
//
// args mirrors the SQLite argv: [moduleName, dbName, tableName, moduleArgs...].
// Module arguments arrive as raw, unparsed text — SQLite does not strip the
// quotes a caller wrote in the CREATE VIRTUAL TABLE statement, so a perfectly
// valid path arrives as `"/some/dir"` and fails to open unless unquoted here.
func (m *Module) Create(ctx vtab.Context, args []string) (vtab.Table, error) {
	root := m.defaultRoot
	if len(args) > 3 {
		if a := unquoteArg(args[3]); a != "" {
			root = a
		}
	}
	if root == "" {
		return nil, errors.New("signalvtab: no sessions directory given and module has no default")
	}
	if err := ctx.Declare(declaredSchema); err != nil {
		return nil, fmt.Errorf("signalvtab: declare schema: %w", err)
	}
	return &table{mod: m, root: root}, nil
}

// Connect implements vtab.Module. A read-only table has nothing to
// distinguish creation from connection.
func (m *Module) Connect(ctx vtab.Context, args []string) (vtab.Table, error) {
	return m.Create(ctx, args)
}

// unquoteArg strips the surrounding quote characters SQLite leaves on module
// arguments. Single quotes, double quotes, and backticks are all accepted
// because all three are legal quoting in a CREATE VIRTUAL TABLE clause.
func unquoteArg(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if c := s[0]; (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// table is one virtual table instance.
type table struct {
	mod  *Module
	root string
}

// plan is the query plan BestIndex hands to Filter through IdxStr.
type plan struct {
	shardArg   int    // argv index of the shard equality value, -1 if absent
	sessionArg int    // argv index of the session_id equality value, -1 if absent
	colUsed    uint64 // bitmask of referenced columns
}

func (p plan) encode() string {
	return strconv.Itoa(p.shardArg) + "|" + strconv.Itoa(p.sessionArg) + "|" + strconv.FormatUint(p.colUsed, 10)
}

func decodePlan(s string) plan {
	p := plan{shardArg: -1, sessionArg: -1, colUsed: ^uint64(0)}
	parts := strings.Split(s, "|")
	if len(parts) != 3 {
		return p
	}
	if v, err := strconv.Atoi(parts[0]); err == nil {
		p.shardArg = v
	}
	if v, err := strconv.Atoi(parts[1]); err == nil {
		p.sessionArg = v
	}
	if v, err := strconv.ParseUint(parts[2], 10, 64); err == nil {
		p.colUsed = v
	}
	return p
}

// needsAttrs reports whether the query touches either attribute-bag column.
// When it does not, rows are decoded without them.
func (p plan) needsAttrs() bool {
	const mask = uint64(1)<<ColAttrsJSON | uint64(1)<<ColResourceAttrsJSON
	return p.colUsed&mask != 0
}

// BestIndex claims the constraints this table can act on.
//
// Only equality is claimed. shard equality selects a single file and is fully
// handled here, so its constraint is omitted from the parent query. session_id
// equality only prefilters rows, so SQLite is left to re-check it.
func (t *table) BestIndex(info *vtab.IndexInfo) error {
	p := plan{shardArg: -1, sessionArg: -1, colUsed: info.ColUsed}

	if !t.mod.disablePushdown {
		argc := 0
		for i := range info.Constraints {
			c := &info.Constraints[i]
			if !c.Usable || c.Op != vtab.OpEQ {
				continue
			}
			switch c.Column {
			case ColShard:
				if p.shardArg >= 0 {
					continue
				}
				c.ArgIndex = argc
				// Selecting the file by name is exact: every row in that
				// file has that shard value by construction, and no row
				// outside it does. Safe to omit the re-check.
				c.Omit = true
				p.shardArg = argc
				argc++
			case ColSessionID:
				if p.sessionArg >= 0 {
					continue
				}
				c.ArgIndex = argc
				// Prefilter only — leave SQLite to verify.
				c.Omit = false
				p.sessionArg = argc
				argc++
			}
		}
	}

	info.IdxStr = p.encode()
	info.IdxNum = 0
	if p.shardArg >= 0 {
		info.IdxNum |= 1
	}
	if p.sessionArg >= 0 {
		info.IdxNum |= 2
	}

	// Cost model: one shard is roughly two orders of magnitude cheaper than
	// the whole directory on the corpus this was measured against. The
	// absolute numbers only matter relative to each other.
	switch {
	case p.shardArg >= 0:
		info.EstimatedCost = 1e4
		info.EstimatedRows = 10000
	case p.sessionArg >= 0:
		// Still a full directory scan, but most lines skip JSON decoding.
		info.EstimatedCost = 1e6
		info.EstimatedRows = 10000
	default:
		info.EstimatedCost = 1e8
		info.EstimatedRows = 1000000
	}
	return nil
}

// Open implements vtab.Table. File selection happens in Filter, which is where
// the constraint values arrive.
func (t *table) Open() (vtab.Cursor, error) {
	return &cursor{tab: t}, nil
}

func (t *table) Disconnect() error { return nil }
func (t *table) Destroy() error    { return nil }

// shardPath returns the shard file for a shard id. filepath.Base defends
// against a caller smuggling a path separator through the constraint value.
func (t *table) shardPath(shard string) string {
	return filepath.Join(t.root, filepath.Base(shard), ShardFile)
}

// allShards lists every shard under the root, sorted for deterministic order.
// Session directories without an events.ndjson are skipped: the sessions tree
// also holds directories keyed by harness session id that carry only state
// files, plus rendered .html artifacts.
func (t *table) allShards() ([]shardRef, error) {
	entries, err := os.ReadDir(t.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	refs := make([]shardRef, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(t.root, e.Name(), ShardFile)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		refs = append(refs, shardRef{id: e.Name(), path: p})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].id < refs[j].id })
	return refs, nil
}

type shardRef struct {
	id   string
	path string
}
