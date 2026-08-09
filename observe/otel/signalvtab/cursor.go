package signalvtab

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"time"

	"modernc.org/sqlite/vtab"
)

// readBufSize is the bufio.Reader size. Lines longer than this are assembled
// into the cursor's own line buffer, so there is no maximum line length.
//
// The prototype used bufio.Scanner, whose default token limit is 64KiB. The
// largest line in the corpus this was measured against is 63,782 bytes — the
// scanner would have started returning bufio.ErrTooLong, aborting the scan, on
// the next slightly larger attribute bag.
const readBufSize = 256 << 10

// defaultMalformedLogLimit caps how many malformed lines the default handler
// prints per cursor. The Stats counter remains authoritative for the total.
const defaultMalformedLogLimit = 5

// rowCore holds every column except the two attribute bags. Decoding into
// rowCore rather than rowFull skips allocating and copying the attribute JSON,
// which is the bulk of every line on a real shard.
type rowCore struct {
	SignalID  string `json:"signal_id"`
	Harness   string `json:"harness"`
	SessionID string `json:"session_id"`
	PromptID  string `json:"prompt_id"`

	TraceID    string `json:"trace_id"`
	SpanID     string `json:"span_id"`
	ParentSpan string `json:"parent_span"`

	Kind      string `json:"kind"`
	Canonical string `json:"canonical"`
	Native    string `json:"native"`
	TS        string `json:"ts"`

	ToolName       string `json:"tool_name"`
	ToolUseID      string `json:"tool_use_id"`
	Model          string `json:"model"`
	Decision       string `json:"decision"`
	DecisionSource string `json:"decision_source"`

	TokensInput         *int64 `json:"tokens_input"`
	TokensOutput        *int64 `json:"tokens_output"`
	TokensCacheRead     *int64 `json:"tokens_cache_read"`
	TokensCacheCreation *int64 `json:"tokens_cache_creation"`
	TokensThought       *int64 `json:"tokens_thought"`
	TokensTool          *int64 `json:"tokens_tool"`
	TokensReasoning     *int64 `json:"tokens_reasoning"`

	CostUSD    *float64 `json:"cost_usd"`
	CostSource string   `json:"cost_source"`

	DurationMs *int64 `json:"duration_ms"`
	Success    *bool  `json:"success"`
	ErrorMsg   string `json:"error_msg"`
	Attempt    *int64 `json:"attempt"`
	StatusCode *int64 `json:"status_code"`
}

// rowFull is rowCore plus the attribute bags, kept as raw JSON so the columns
// can be handed to SQLite as TEXT without building Go maps.
type rowFull struct {
	rowCore
	Attrs         json.RawMessage `json:"attrs"`
	ResourceAttrs json.RawMessage `json:"resource_attrs"`
}

// cursor scans the selected shards line by line.
type cursor struct {
	tab  *table
	plan plan

	files []shardRef
	fi    int

	rc  io.ReadCloser
	br  *bufio.Reader
	cur shardRef

	lineNo   int
	lineBuf  []byte
	needle   []byte // `"session_id":"<value>"`, nil when not filtering
	wantAttr bool

	row   rowFull
	rowid int64
	eof   bool

	malformedLogged int
}

// Filter selects the shards to scan and installs the row prefilter.
// It may be called more than once on the same cursor (a join re-filters), so
// all scan state is reset here.
func (c *cursor) Filter(_ int, idxStr string, vals []vtab.Value) error {
	c.plan = decodePlan(idxStr)
	c.wantAttr = c.plan.needsAttrs()

	if err := c.closeCurrent(); err != nil {
		return err
	}
	c.files = nil
	c.fi = 0
	c.rowid = 0
	c.eof = false
	c.needle = nil
	c.malformedLogged = 0

	// Shard equality: resolve straight to one path, no directory scan.
	if c.plan.shardArg >= 0 && c.plan.shardArg < len(vals) {
		shard, ok := asString(vals[c.plan.shardArg])
		if !ok {
			c.eof = true
			return nil
		}
		c.files = []shardRef{{id: shard, path: c.tab.shardPath(shard)}}
	} else {
		refs, err := c.tab.allShards()
		if err != nil {
			return err
		}
		c.files = refs
	}

	// session_id equality: cannot select files (a harness session spans
	// several shards and a shard holds several harness sessions), but a
	// substring test on the raw line is far cheaper than decoding it.
	if c.plan.sessionArg >= 0 && c.plan.sessionArg < len(vals) {
		if sess, ok := asString(vals[c.plan.sessionArg]); ok {
			c.needle = []byte(`"session_id":"` + sess + `"`)
		}
	}

	return c.advance()
}

// asString accepts the string and []byte forms a TEXT constraint can arrive in.
func asString(v vtab.Value) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	default:
		return "", false
	}
}

func (c *cursor) closeCurrent() error {
	if c.rc == nil {
		return nil
	}
	err := c.rc.Close()
	c.rc = nil
	c.br = nil
	return err
}

// openNext advances to the next shard with content, or sets eof.
// A shard that has vanished between listing and opening is counted and
// skipped rather than failing the query: shards are live files that
// retention and archival can remove at any moment.
func (c *cursor) openNext() error {
	if err := c.closeCurrent(); err != nil {
		return err
	}
	for c.fi < len(c.files) {
		ref := c.files[c.fi]
		c.fi++
		rc, err := c.tab.mod.openFile(ref.path)
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return err
			}
			c.tab.mod.stats.FilesMissing.Add(1)
			continue
		}
		c.tab.mod.stats.FilesOpened.Add(1)
		c.rc = rc
		c.br = bufio.NewReaderSize(rc, readBufSize)
		c.cur = ref
		c.lineNo = 0
		return nil
	}
	c.eof = true
	return nil
}

// readLine returns the next line without its terminator. Lines longer than the
// read buffer are assembled into c.lineBuf, so there is no length limit. The
// returned slice is only valid until the next read.
func (c *cursor) readLine() ([]byte, error) {
	c.lineBuf = c.lineBuf[:0]
	for {
		chunk, err := c.br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			c.lineBuf = append(c.lineBuf, chunk...)
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && (len(chunk) > 0 || len(c.lineBuf) > 0) {
				c.lineBuf = append(c.lineBuf, chunk...)
				return trimCR(c.lineBuf), nil
			}
			return nil, err
		}
		if len(c.lineBuf) == 0 {
			return trimCR(chunk[:len(chunk)-1]), nil
		}
		c.lineBuf = append(c.lineBuf, chunk...)
		return trimCR(c.lineBuf[:len(c.lineBuf)-1]), nil
	}
}

func trimCR(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return b[:n-1]
	}
	return b
}

// advance moves to the next emittable row, crossing shard boundaries and
// skipping blank, prefiltered, and malformed lines.
func (c *cursor) advance() error {
	if c.rc == nil && c.fi == 0 && !c.eof {
		if err := c.openNext(); err != nil {
			return err
		}
	}
	for {
		if c.eof {
			return nil
		}
		line, err := c.readLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			if err := c.openNext(); err != nil {
				return err
			}
			continue
		}
		c.lineNo++
		c.tab.mod.stats.LinesRead.Add(1)
		c.tab.mod.stats.BytesRead.Add(int64(len(line)) + 1)

		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if c.needle != nil && !bytes.Contains(line, c.needle) {
			c.tab.mod.stats.RowsPrefiltered.Add(1)
			continue
		}
		if err := c.decode(line); err != nil {
			c.reportMalformed(err)
			continue
		}
		c.rowid++
		c.tab.mod.stats.RowsEmitted.Add(1)
		return nil
	}
}

// decode parses one line into c.row, zeroing it first so a field absent from
// this line cannot inherit the previous row's value.
func (c *cursor) decode(line []byte) error {
	c.row = rowFull{}
	if c.wantAttr {
		return json.Unmarshal(line, &c.row)
	}
	return json.Unmarshal(line, &c.row.rowCore)
}

// reportMalformed records an undecodable line and keeps scanning. A shard is
// an append-only log that a crash or a partial flush can truncate mid-line;
// one bad line must not lose the rest of the file. It must also not vanish
// without a trace, hence the counter and the handler.
func (c *cursor) reportMalformed(err error) {
	c.tab.mod.stats.LinesMalformed.Add(1)
	if h := c.tab.mod.onMalformed; h != nil {
		h(c.cur.path, c.lineNo, err)
		return
	}
	if c.malformedLogged < defaultMalformedLogLimit {
		c.malformedLogged++
		log.Printf("signalvtab: skipping malformed line %s:%d: %v", c.cur.path, c.lineNo, err)
		if c.malformedLogged == defaultMalformedLogLimit {
			log.Printf("signalvtab: further malformed lines in this scan will not be logged; see Stats.LinesMalformed")
		}
	}
}

func (c *cursor) Next() error { return c.advance() }
func (c *cursor) Eof() bool   { return c.eof }

func (c *cursor) Rowid() (int64, error) { return c.rowid, nil }

func (c *cursor) Close() error { return c.closeCurrent() }

// Column returns one column of the current row.
//
// Empty optional TEXT fields are returned as NULL rather than "": the sink
// writes them with omitempty, so absent and empty are indistinguishable on
// disk, and NULL is what the ingested otel_signals table holds for them.
func (c *cursor) Column(col int) (vtab.Value, error) {
	r := &c.row
	switch col {
	case ColShard:
		return c.cur.id, nil
	case ColSignalID:
		return text(r.SignalID), nil
	case ColHarness:
		return text(r.Harness), nil
	case ColSessionID:
		return text(r.SessionID), nil
	case ColPromptID:
		return text(r.PromptID), nil
	case ColTraceID:
		return text(r.TraceID), nil
	case ColSpanID:
		return text(r.SpanID), nil
	case ColParentSpan:
		return text(r.ParentSpan), nil
	case ColKind:
		return text(r.Kind), nil
	case ColCanonical:
		return text(r.Canonical), nil
	case ColNative:
		return text(r.Native), nil
	case ColTS:
		return text(r.TS), nil
	case ColTSMicros:
		return tsMicros(r.TS), nil
	case ColToolName:
		return text(r.ToolName), nil
	case ColToolUseID:
		return text(r.ToolUseID), nil
	case ColModel:
		return text(r.Model), nil
	case ColDecision:
		return text(r.Decision), nil
	case ColDecisionSource:
		return text(r.DecisionSource), nil
	case ColTokensIn:
		return i64(r.TokensInput), nil
	case ColTokensOut:
		return i64(r.TokensOutput), nil
	case ColTokensCacheRead:
		return i64(r.TokensCacheRead), nil
	case ColTokensCacheCreation:
		return i64(r.TokensCacheCreation), nil
	case ColTokensThought:
		return i64(r.TokensThought), nil
	case ColTokensTool:
		return i64(r.TokensTool), nil
	case ColTokensReasoning:
		return i64(r.TokensReasoning), nil
	case ColCostUSD:
		if r.CostUSD == nil {
			return nil, nil
		}
		return *r.CostUSD, nil
	case ColCostSource:
		return text(r.CostSource), nil
	case ColDurationMs:
		return i64(r.DurationMs), nil
	case ColSuccess:
		if r.Success == nil {
			return nil, nil
		}
		return *r.Success, nil
	case ColErrorMsg:
		return text(r.ErrorMsg), nil
	case ColAttempt:
		return i64(r.Attempt), nil
	case ColStatusCode:
		return i64(r.StatusCode), nil
	case ColAttrsJSON:
		return rawText(r.Attrs), nil
	case ColResourceAttrsJSON:
		return rawText(r.ResourceAttrs), nil
	}
	return nil, errUnknownColumn(col)
}

func errUnknownColumn(col int) error {
	return &columnError{col: col}
}

type columnError struct{ col int }

func (e *columnError) Error() string {
	return "signalvtab: unknown column index " + itoa(e.col)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func text(s string) vtab.Value {
	if s == "" {
		return nil
	}
	return s
}

func i64(p *int64) vtab.Value {
	if p == nil {
		return nil
	}
	return *p
}

// rawText hands the attribute bag to SQLite as TEXT. Absent bags are NULL;
// the sink omits them entirely rather than writing "null".
func rawText(m json.RawMessage) vtab.Value {
	if len(m) == 0 || bytes.Equal(m, []byte("null")) {
		return nil
	}
	return string(m)
}

// tsMicros converts the on-disk RFC3339 timestamp to the microsecond epoch the
// otel_signals table stores, so a query can be ported between the two without
// rewriting its time arithmetic. Unparseable timestamps yield NULL; the row
// itself is still returned, since a bad clock string is not a reason to drop a
// signal.
func tsMicros(ts string) vtab.Value {
	if ts == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			return nil
		}
	}
	return t.UnixMicro()
}
