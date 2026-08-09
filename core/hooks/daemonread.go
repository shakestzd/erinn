package hooks

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
)

// Hook-side client for the daemon read path (feat-f6759e37).
//
// THE PROBLEM THIS SOLVES. Hooks are fresh OS processes that the harness spawns
// on every tool call. The spawn is fixed by the harness contract. Parsing
// canonical work-item state inside one is not affordable — roughly 100ms warm
// and 250ms cold against a 1,000-file corpus — and that cost, not any property
// of the data, is the ONLY reason hooks have read work-item status from the
// derived SQLite index. Amortising the parse in the resident daemon removes the
// reason, and with it a second data path that could silently diverge from
// canonical.
//
// THE AVAILABILITY CONTRACT. There are exactly two, and which one applies is
// explicit and announced rather than inferred:
//
//   - GUARANTEED. A wipnote launcher started this session. The launcher ensured
//     a daemon (starting one, or attaching to a running one), proved it speaks
//     the read protocol, and wrote its socket path into the session
//     environment. Hooks do NOT negotiate availability: they read the path and
//     use it. If the daemon becomes unreachable mid-session they retry a
//     bounded number of times and then REPORT LOUDLY and pause execution
//     gracefully. They do NOT quietly read the index instead — a silent
//     fallback is exactly how two paths diverge without anyone noticing.
//
//   - NONE. No launcher started this session, so nothing promised a daemon.
//     Everything works precisely as it did before this feature existed: reads
//     go to the derived index. This is not a degraded mode, it is the
//     unchanged one.
//
// The mode is decided by the presence of the launcher-written socket path, not
// by probing — a hook that probed would be negotiating, and two hooks in one
// session could reach different conclusions.

// DaemonSocketEnv is the environment variable a wipnote launcher sets to the
// path of the daemon socket it guaranteed for this session. Its PRESENCE is
// the guarantee; its absence is the unguaranteed contract.
const DaemonSocketEnv = "WIPNOTE_DAEMON_SOCKET"

// daemonReadBudget bounds the whole read — including the bounded retries inside
// daemon.ReadClient — so a hook can never spend more than this on the daemon
// before it gives up and reports.
const daemonReadBudget = 750 * time.Millisecond

// DaemonContract is the availability contract this hook process runs under.
type DaemonContract int

const (
	// DaemonContractNone — no launcher guarantee; read the index as before.
	DaemonContractNone DaemonContract = iota
	// DaemonContractGuaranteed — a launcher promised a daemon at SocketPath.
	DaemonContractGuaranteed
)

// String renders the contract for logs and for the loud failure message.
func (c DaemonContract) String() string {
	if c == DaemonContractGuaranteed {
		return "launcher-guaranteed"
	}
	return "no-launcher-guarantee"
}

// DaemonContractForProcess reports the contract this hook runs under and, when
// guaranteed, the socket path the launcher promised.
func DaemonContractForProcess() (DaemonContract, string) {
	sock := os.Getenv(DaemonSocketEnv)
	if sock == "" {
		return DaemonContractNone, ""
	}
	return DaemonContractGuaranteed, sock
}

// breach latches the first guarantee failure seen in this hook process.
//
// A latch (rather than an error returned through every call site) is
// deliberate: the read helpers below are called from deep inside handlers whose
// signatures return plain values, and threading an error through all of them
// would create many places where a caller could forget to check and silently
// proceed on a zero value — reintroducing the quiet failure this design
// forbids. Instead the failure is recorded here and converted into a loud,
// execution-pausing result at the single point where every hook emits its
// response (runHookNamed).
var (
	breachMu     sync.Mutex
	breachReason string
)

// recordDaemonBreach latches a guarantee failure. Only the FIRST is kept: the
// first failure is the one that explains the rest.
func recordDaemonBreach(op, socket string, err error) {
	breachMu.Lock()
	defer breachMu.Unlock()
	if breachReason != "" {
		return
	}
	breachReason = fmt.Sprintf(
		"wipnote: the work-item daemon this session depends on is unreachable.\n"+
			"  read op : %s\n"+
			"  socket  : %s\n"+
			"  detail  : %v\n"+
			"This session was started by a wipnote launcher, which guarantees the daemon, "+
			"so wipnote will NOT silently read the derived index instead — a quiet fallback "+
			"is how two data paths diverge unnoticed.\n"+
			"Pausing here. Restart the daemon with `wipnote serve`, or start a fresh session "+
			"with `wipnote claude`, then retry.", op, socket, err)
}

// DaemonGuaranteeBreach returns the latched guarantee failure, or "" if none.
func DaemonGuaranteeBreach() string {
	breachMu.Lock()
	defer breachMu.Unlock()
	return breachReason
}

// ResetDaemonGuaranteeBreach clears the latch. Tests only — a hook process is
// short-lived and never needs to clear it.
func ResetDaemonGuaranteeBreach() {
	breachMu.Lock()
	defer breachMu.Unlock()
	breachReason = ""
}

// DaemonGuaranteeBlockResult returns the loud, execution-pausing HookResult for
// a latched guarantee failure, or nil when there is none.
//
// "block" is what pauses gracefully: the harness stops the action and shows
// Reason to the agent, which is the only channel a hook has for being loud
// (stderr from a hook renders as an opaque "hook error" in the Claude Code UI
// and tells the agent nothing).
func DaemonGuaranteeBlockResult() *HookResult {
	reason := DaemonGuaranteeBreach()
	if reason == "" {
		return nil
	}
	return &HookResult{Decision: "block", Reason: reason}
}

// readClientForProcess returns a read client for the guaranteed socket, or nil
// when this process runs under no guarantee.
func readClientForProcess() (*daemon.ReadClient, string) {
	contract, sock := DaemonContractForProcess()
	if contract != DaemonContractGuaranteed {
		return nil, ""
	}
	return daemon.NewReadClientForSocket(sock), sock
}

// LookupWorkItem resolves one work item's canonical state.
//
// Under a launcher guarantee it reads the daemon and NEVER touches database: on
// failure it latches a breach and returns not-found, and the hook's response is
// replaced with a loud block before anything acts on that zero value. Without a
// guarantee it queries the derived index exactly as before.
func LookupWorkItem(database *sql.DB, id string) (daemon.WorkItem, bool) {
	if id == "" {
		return daemon.WorkItem{}, false
	}
	if client, sock := readClientForProcess(); client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), daemonReadBudget)
		defer cancel()
		res, _, err := client.GetWorkItem(ctx, id)
		if err != nil {
			recordDaemonBreach(daemon.ReadOpWorkItemGet, sock, err)
			return daemon.WorkItem{}, false
		}
		return res.Item, res.Found
	}
	return lookupWorkItemFromIndex(database, id)
}

// lookupWorkItemFromIndex is the pre-existing derived-index read, unchanged in
// behaviour. It remains the ONLY path for sessions no launcher started.
func lookupWorkItemFromIndex(database *sql.DB, id string) (daemon.WorkItem, bool) {
	if database == nil {
		return daemon.WorkItem{}, false
	}
	var (
		status  sql.NullString
		title   sql.NullString
		typ     sql.NullString
		trackID sql.NullString
	)
	err := database.QueryRow(
		`SELECT status, title, type, track_id FROM features WHERE id = ?`, id,
	).Scan(&status, &title, &typ, &trackID)
	if err != nil {
		return daemon.WorkItem{}, false
	}
	return daemon.WorkItem{
		ID:      id,
		Status:  status.String,
		Title:   title.String,
		Type:    typ.String,
		TrackID: trackID.String,
	}, true
}

// ListWorkItems lists work items matching args. Same contract split as
// LookupWorkItem.
func ListWorkItems(database *sql.DB, args daemon.WorkItemListArgs) []daemon.WorkItem {
	if client, sock := readClientForProcess(); client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), daemonReadBudget)
		defer cancel()
		res, _, err := client.ListWorkItems(ctx, args)
		if err != nil {
			recordDaemonBreach(daemon.ReadOpWorkItemList, sock, err)
			return nil
		}
		return res.Items
	}
	return listWorkItemsFromIndex(database, args)
}

// listWorkItemsFromIndex is the pre-existing derived-index read. The ORDER BY
// reproduces what the daemon's canonical scan sorts by, so the two paths agree
// on ordering as well as on membership.
func listWorkItemsFromIndex(database *sql.DB, args daemon.WorkItemListArgs) []daemon.WorkItem {
	if database == nil {
		return nil
	}
	query := `SELECT id, title, status, type, track_id FROM features WHERE 1=1`
	var qargs []any
	if args.TrackID != "" {
		query += ` AND track_id = ?`
		qargs = append(qargs, args.TrackID)
	}
	if len(args.Statuses) > 0 {
		query += ` AND status IN (` + placeholders(len(args.Statuses)) + `)`
		for _, s := range args.Statuses {
			qargs = append(qargs, s)
		}
	}
	if len(args.Types) > 0 {
		query += ` AND type IN (` + placeholders(len(args.Types)) + `)`
		for _, t := range args.Types {
			qargs = append(qargs, t)
		}
	}
	query += `
		ORDER BY
			CASE status WHEN 'in-progress' THEN 0 ELSE 1 END,
			CASE type WHEN 'feature' THEN 0 WHEN 'bug' THEN 1 ELSE 2 END,
			created_at DESC`
	if args.Limit > 0 {
		query += ` LIMIT ?`
		qargs = append(qargs, args.Limit)
	}

	rows, err := database.Query(query, qargs...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []daemon.WorkItem
	for rows.Next() {
		var (
			it      daemon.WorkItem
			title   sql.NullString
			typ     sql.NullString
			trackID sql.NullString
		)
		if err := rows.Scan(&it.ID, &title, &it.Status, &typ, &trackID); err != nil {
			continue
		}
		it.Title, it.Type, it.TrackID = title.String, typ.String, trackID.String
		out = append(out, it)
	}
	return out
}

// placeholders returns "?, ?, …" for n bind parameters.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, n*3)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',', ' ')
		}
		out = append(out, '?')
	}
	return string(out)
}
