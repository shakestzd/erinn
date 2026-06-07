package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// errConnRefused is the portable connection-refused errno. syscall.ECONNREFUSED
// is defined identically on Linux and darwin (and every other unix), so no
// build-tagged file is needed.
var errConnRefused = syscall.ECONNREFUSED

// ErrWriterUnavailable is the sentinel returned by WriterClient.Submit when
// the per-project writer socket cannot be reached — either it does not
// exist (no daemon / lease holder) or the dial is refused (owner mid-exit).
//
// MVP-3/4 callers match this with errors.Is and fall back to direct-open
// (the existing dbgate / CLI write path). MVP-2 deliberately does NOT
// auto-spawn the daemon here — spawning is owned by serve/callers and is a
// documented follow-on. Keeping the client dumb (dial-or-fail) means a
// caller's fallback decision stays in one obvious place.
var ErrWriterUnavailable = errors.New("daemon: writer unavailable")

// defaultDialTimeout bounds the connect attempt so a wedged socket inode
// can't hang a caller. The submit round-trip itself is bounded by the
// caller's context.
const defaultDialTimeout = 2 * time.Second

// WriterClient submits write-op envelopes to a per-project writer daemon
// over its Unix socket and awaits the ack. It is a thin, stateless dialer:
// each Submit opens a fresh connection, writes one envelope, reads one ack,
// and closes. (Connection reuse / pooling is a later optimization; MVP-2
// favors simplicity and crash-isolation.)
type WriterClient struct {
	socketPath  string
	dialTimeout time.Duration
}

// NewWriterClient returns a client targeting the writer socket for
// projectRoot. It performs no I/O — the dial happens on Submit.
func NewWriterClient(projectRoot string) *WriterClient {
	return &WriterClient{
		socketPath:  SocketPath(projectRoot),
		dialTimeout: defaultDialTimeout,
	}
}

// NewWriterClientForSocket targets an explicit socket path (tests, or a
// caller that already resolved the path).
func NewWriterClientForSocket(socketPath string) *WriterClient {
	return &WriterClient{socketPath: socketPath, dialTimeout: defaultDialTimeout}
}

// Submit dials the writer socket, sends env, and returns the daemon's ack.
//
// On a missing socket (ENOENT) or a refused connection (ECONNREFUSED), it
// returns ErrWriterUnavailable so the caller can fall back to its existing
// write path. ctx bounds the whole round-trip; ctx.Err() is returned if the
// context fires first. A successful round-trip returns the Ack as-is — an
// AckError status is NOT promoted to a Go error, because "the daemon
// reachable but rejected this op" is a different situation from "no daemon"
// and the caller must distinguish them.
func (c *WriterClient) Submit(ctx context.Context, env Envelope) (Ack, error) {
	// Default the version so callers don't have to set it on every op.
	if env.OpFormatVersion == 0 {
		env.OpFormatVersion = OpFormatVersion
	}

	dialer := net.Dialer{Timeout: c.dialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		if isUnavailable(err) {
			return Ack{}, ErrWriterUnavailable
		}
		return Ack{}, fmt.Errorf("dial writer socket: %w", err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	payload, err := json.Marshal(env)
	if err != nil {
		return Ack{}, fmt.Errorf("marshal envelope: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		return Ack{}, fmt.Errorf("write envelope: %w", err)
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		// Closed before any ack — treat as unavailable so the caller
		// falls back rather than hanging on a half-dead owner.
		if ctx.Err() != nil {
			return Ack{}, ctx.Err()
		}
		return Ack{}, ErrWriterUnavailable
	}
	var ack Ack
	if err := json.Unmarshal(line, &ack); err != nil {
		return Ack{}, fmt.Errorf("decode ack: %w", err)
	}
	return ack, nil
}

// isUnavailable reports whether a dial error means "no reachable daemon"
// (socket missing or connection refused) as opposed to a real transport
// fault the caller should see. ENOENT and ECONNREFUSED both map to the
// portable sentinels os.ErrNotExist and syscall.ECONNREFUSED.
func isUnavailable(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	// net.Dial wraps the syscall error; errors.Is unwraps to the syscall
	// errno on both Linux and darwin without any platform-specific code.
	return errors.Is(err, errConnRefused)
}
