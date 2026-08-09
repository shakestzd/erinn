package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// Server side of the daemon read protocol (feat-f6759e37). See readwire.go for
// the wire contract and the reasoning behind it.

// Reader answers a ReadRequest from whatever state its owner holds. It is the
// read-side peer of Applier, and it is injected for the same reason: the
// daemon package stays transport-only, so the thing that knows how to parse
// canonical work-item state lives outside it (see core/daemon/readsrv) and
// cannot drag its dependencies into every daemon client.
//
// A Reader runs on the connection goroutine, NOT on the writequeue worker.
// Reads therefore never queue behind a slow write, and a slow read can never
// delay a write — the two paths share only the socket.
//
// Returning an error yields ReadStatusError. Returning ErrUnknownReadOp yields
// ReadStatusUnsupported, which is the signal a client uses to conclude "this
// daemon does not serve that read" rather than "that read came back empty".
type Reader func(req ReadRequest) (json.RawMessage, *CacheStats, error)

// ErrUnknownReadOp is returned by a Reader for a read_op it does not
// implement. It maps to ReadStatusUnsupported so the distinction between
// "unimplemented" and "implemented but empty" survives the wire.
var ErrUnknownReadOp = fmt.Errorf("daemon: unknown read op")

// isReadFrame reports whether a received line is a ReadRequest rather than a
// write Envelope. The discriminator is a non-empty "read_op" field. Decoding
// into a one-field probe struct is cheap and, critically, TOTAL: a malformed
// line simply is not a read frame and falls through to the write path, which
// already rejects malformed input with an error ack.
func isReadFrame(line []byte) bool {
	var probe struct {
		ReadOp string `json:"read_op"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return false
	}
	return probe.ReadOp != ""
}

// processRead decodes one ReadRequest and returns the ReadResponse.
//
// Read requests participate in the SAME idle accounting as writes. That is not
// bookkeeping tidiness — it is load-bearing. A launcher-guaranteed session
// whose only daemon traffic is hook reads must not have the daemon idle-exit
// out from under it mid-session, because the availability policy gives hooks no
// way to negotiate: they would report loudly and pause, on a daemon that left
// for lack of the very traffic it was serving.
func (l *Listener) processRead(line []byte) ReadResponse {
	l.inFlight.Add(1)
	l.lastActivity.Store(time.Now().UnixNano())
	defer func() {
		l.lastActivity.Store(time.Now().UnixNano())
		l.inFlight.Add(-1)
	}()

	var req ReadRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return readResponseErr(ReadStatusError, "malformed read request: "+err.Error())
	}
	if req.ReadFormatVersion != ReadFormatVersion {
		return readResponseErr(ReadStatusUnsupported, fmt.Sprintf(
			"unsupported read_format_version %d (daemon speaks %d)",
			req.ReadFormatVersion, ReadFormatVersion))
	}

	// Transport-level ops are answered by the Listener itself: they are about
	// the daemon, not about project state, so they must work even when no
	// Reader is wired.
	switch req.ReadOp {
	case ReadOpPing:
		return l.readPing()
	case ReadOpAttach:
		return l.readAttach(req)
	}

	if l.reader == nil {
		return readResponseErr(ReadStatusUnsupported,
			"no reader registered (daemon serves writes only)")
	}
	result, stats, err := l.reader(req)
	if err != nil {
		if err == ErrUnknownReadOp {
			return readResponseErr(ReadStatusUnsupported,
				"unknown read_op "+req.ReadOp)
		}
		return readResponseErr(ReadStatusError, req.ReadOp+": "+err.Error())
	}
	resp := readResponseOK(result)
	resp.Cache = stats
	return resp
}

// readPing answers ReadOpPing from Listener state alone.
func (l *Listener) readPing() ReadResponse {
	body, err := json.Marshal(PingResult{
		ReadFormatVersion: ReadFormatVersion,
		PID:               os.Getpid(),
		SocketPath:        l.sockPath,
		AttachedPIDs:      l.liveAttachedCount(),
	})
	if err != nil {
		return readResponseErr(ReadStatusError, "encode ping: "+err.Error())
	}
	return readResponseOK(body)
}

// readAttach answers ReadOpAttach, registering a launcher pid whose lifetime
// suppresses idle-exit.
func (l *Listener) readAttach(req ReadRequest) ReadResponse {
	var args AttachArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return readResponseErr(ReadStatusError, "decode attach args: "+err.Error())
		}
	}
	if args.PID <= 0 {
		return readResponseErr(ReadStatusError, "attach requires a positive pid")
	}
	l.attachMu.Lock()
	if l.attached == nil {
		l.attached = make(map[int]struct{})
	}
	l.attached[args.PID] = struct{}{}
	l.attachMu.Unlock()

	body, err := json.Marshal(AttachResult{Attached: true, AttachedPIDs: l.liveAttachedCount()})
	if err != nil {
		return readResponseErr(ReadStatusError, "encode attach result: "+err.Error())
	}
	return readResponseOK(body)
}

// liveAttachedCount prunes dead attached pids and returns how many remain.
//
// Liveness is kill(pid, 0). EPERM means the process EXISTS under another uid,
// so it counts as alive — the safe direction here is to keep the daemon up.
// The unsafe direction (dropping a live attachment) would let the daemon
// idle-exit under a session that was promised it.
func (l *Listener) liveAttachedCount() int {
	l.attachMu.Lock()
	defer l.attachMu.Unlock()
	for pid := range l.attached {
		if !pidAlive(pid) {
			delete(l.attached, pid)
		}
	}
	return len(l.attached)
}

// pidAlive reports whether pid names a live process. ESRCH (no such process)
// is the only outcome treated as dead; every other errno degrades to alive.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err != syscall.ESRCH
}

// attachState is embedded into Listener via the fields below. Kept as a named
// block so the read-protocol additions to the Listener struct are reviewable in
// one place rather than scattered through socket.go.
type attachState struct {
	attachMu sync.Mutex
	attached map[int]struct{}
}

func readResponseOK(result json.RawMessage) ReadResponse {
	return ReadResponse{
		ReadStatus:        ReadStatusOK,
		ReadFormatVersion: ReadFormatVersion,
		Result:            result,
	}
}

func readResponseErr(status ReadStatus, msg string) ReadResponse {
	return ReadResponse{
		ReadStatus:        status,
		ReadFormatVersion: ReadFormatVersion,
		Error:             msg,
	}
}
