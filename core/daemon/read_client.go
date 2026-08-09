package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// Client side of the daemon read protocol (feat-f6759e37).

// ErrDaemonUnreachable is returned when the read socket could not be reached
// after every bounded retry — it does not exist, the dial was refused, or the
// daemon closed the connection without replying.
//
// This sentinel exists so a caller can distinguish "no daemon" from "daemon
// said no". What a caller does with that distinction is policy, not transport:
// under a launcher-guaranteed session the correct response is to report loudly
// and pause, NOT to quietly read something else.
var ErrDaemonUnreachable = errors.New("daemon: read path unreachable")

// ErrReadUnsupported is returned when the daemon was reached but does not
// speak this read protocol — an older build, a write-only daemon with no
// Reader wired, or an unknown read_op. It is deliberately NOT merged with
// ErrDaemonUnreachable: an unreachable daemon may come back on the next
// attempt, whereas an unsupported one will answer identically forever, so
// retrying it only burns a hook's latency budget.
var ErrReadUnsupported = errors.New("daemon: read protocol unsupported")

// readDialTimeout bounds a single connect attempt on the read path. It is much
// tighter than defaultDialTimeout (2s, used by the write path) because a read
// runs inside a hook that the harness spawns on every tool call: a wedged
// socket inode must not spend the hook's whole budget on one dial.
const readDialTimeout = 150 * time.Millisecond

// readRetryAttempts is the bounded retry count for an unreachable daemon. The
// policy calls for a BOUNDED retry, then a loud report — the bound is what
// stops a hook from hanging while a daemon that is never coming back is waited
// on. Three attempts covers the realistic transient (a daemon mid-restart
// rebinding its socket) without covering up a real outage.
const readRetryAttempts = 3

// readRetryBackoff is the pause between retry attempts.
const readRetryBackoff = 50 * time.Millisecond

// ReadClient issues read requests to a daemon socket. Like WriterClient it is
// a thin stateless dialer: one connection per request, no pooling.
type ReadClient struct {
	socketPath  string
	dialTimeout time.Duration
	attempts    int
	backoff     time.Duration
}

// NewReadClient returns a client targeting the read socket for projectRoot.
// It performs no I/O.
func NewReadClient(projectRoot string) *ReadClient {
	return NewReadClientForSocket(SocketPath(projectRoot))
}

// NewReadClientForSocket targets an explicit socket path — the shape a hook
// uses, since the launcher hands it the path directly rather than making it
// re-derive one.
func NewReadClientForSocket(socketPath string) *ReadClient {
	return &ReadClient{
		socketPath:  socketPath,
		dialTimeout: readDialTimeout,
		attempts:    readRetryAttempts,
		backoff:     readRetryBackoff,
	}
}

// SocketPath returns the socket this client targets.
func (c *ReadClient) SocketPath() string { return c.socketPath }

// Read issues one read op and returns the raw result body.
//
// Retry semantics: only ErrDaemonUnreachable is retried, up to the configured
// bound. ReadStatusUnsupported and ReadStatusError are terminal on the first
// reply — the daemon answered, and asking again cannot change the answer.
func (c *ReadClient) Read(ctx context.Context, op string, args any) (json.RawMessage, *CacheStats, error) {
	var rawArgs json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal read args: %w", err)
		}
		rawArgs = b
	}
	req := ReadRequest{
		ReadOp:            op,
		ReadFormatVersion: ReadFormatVersion,
		Args:              rawArgs,
	}

	var lastErr error
	for attempt := 0; attempt < c.attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, nil, ErrDaemonUnreachable
			case <-time.After(c.backoff):
			}
		}
		result, stats, err := c.readOnce(ctx, req)
		if err == nil {
			return result, stats, nil
		}
		lastErr = err
		if !errors.Is(err, ErrDaemonUnreachable) {
			return nil, nil, err
		}
		if ctx.Err() != nil {
			return nil, nil, ErrDaemonUnreachable
		}
	}
	return nil, nil, lastErr
}

// readOnce performs a single dial-write-read round trip.
func (c *ReadClient) readOnce(ctx context.Context, req ReadRequest) (json.RawMessage, *CacheStats, error) {
	dialer := net.Dialer{Timeout: c.dialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		// Any dial failure — missing socket, refused, or timeout on a wedged
		// inode — is "unreachable" for retry purposes. A hook must not have to
		// classify errnos to know whether to try again.
		return nil, nil, ErrDaemonUnreachable
	}
	defer conn.Close()

	deadline := time.Now().Add(c.dialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal read request: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		return nil, nil, ErrDaemonUnreachable
	}

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, nil, ErrDaemonUnreachable
	}

	var resp ReadResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, nil, fmt.Errorf("decode read response: %w", err)
	}
	// An OLDER daemon decodes our frame as a write Envelope and replies with an
	// Ack, whose "status" field is an AckStatus and whose "read_status" is
	// absent. An empty ReadStatus is therefore exactly the signal "this peer
	// does not speak the read protocol" — and it can never be mistaken for a
	// successful empty result, because ReadStatusOK is a non-empty string.
	if resp.ReadStatus == "" {
		return nil, nil, fmt.Errorf("%w: daemon replied without read_status", ErrReadUnsupported)
	}
	switch resp.ReadStatus {
	case ReadStatusOK:
		return resp.Result, resp.Cache, nil
	case ReadStatusUnsupported:
		return nil, nil, fmt.Errorf("%w: %s", ErrReadUnsupported, resp.Error)
	default:
		return nil, nil, fmt.Errorf("daemon read %s: %s", req.ReadOp, resp.Error)
	}
}

// Ping probes the daemon's read protocol. A nil error proves the daemon is
// reachable AND speaks this version — which is exactly the precondition a
// launcher must establish before it promises availability to hooks.
func (c *ReadClient) Ping(ctx context.Context) (PingResult, error) {
	raw, _, err := c.Read(ctx, ReadOpPing, nil)
	if err != nil {
		return PingResult{}, err
	}
	var out PingResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return PingResult{}, fmt.Errorf("decode ping result: %w", err)
	}
	return out, nil
}

// Attach registers pid with the daemon so idle-exit is suppressed while that
// process lives. Called by a launcher with its own pid.
func (c *ReadClient) Attach(ctx context.Context, pid int) (AttachResult, error) {
	raw, _, err := c.Read(ctx, ReadOpAttach, AttachArgs{PID: pid})
	if err != nil {
		return AttachResult{}, err
	}
	var out AttachResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return AttachResult{}, fmt.Errorf("decode attach result: %w", err)
	}
	return out, nil
}

// GetWorkItem resolves one work item by ID from the daemon's canonical state.
func (c *ReadClient) GetWorkItem(ctx context.Context, id string) (WorkItemGetResult, *CacheStats, error) {
	raw, stats, err := c.Read(ctx, ReadOpWorkItemGet, WorkItemGetArgs{ID: id})
	if err != nil {
		return WorkItemGetResult{}, nil, err
	}
	var out WorkItemGetResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return WorkItemGetResult{}, nil, fmt.Errorf("decode workitem.get result: %w", err)
	}
	return out, stats, nil
}

// ListWorkItems lists work items matching args from the daemon's canonical
// state.
func (c *ReadClient) ListWorkItems(ctx context.Context, args WorkItemListArgs) (WorkItemListResult, *CacheStats, error) {
	raw, stats, err := c.Read(ctx, ReadOpWorkItemList, args)
	if err != nil {
		return WorkItemListResult{}, nil, err
	}
	var out WorkItemListResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return WorkItemListResult{}, nil, fmt.Errorf("decode workitem.list result: %w", err)
	}
	return out, stats, nil
}
