package eventsink

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreevents "github.com/shakestzd/wipnote/core/eventsink"
	"github.com/shakestzd/wipnote/internal/otel"
)

// TestInitRegistersFactory verifies the package init() wired the otel-backed
// factory into core/eventsink, and that emitting an Event through the core
// boundary writes a UnifiedSignal line to the session's events.ndjson.
func TestInitRegistersFactory(t *testing.T) {
	projectDir := t.TempDir()
	sid := "sess-rosetta-1"

	snk, err := coreevents.New(projectDir, sid)
	if err != nil {
		t.Fatalf("coreevents.New (should use registered otel factory): %v", err)
	}
	t.Cleanup(func() { _ = snk.Close() })

	ev := coreevents.Event{
		Harness:       "wipnote",
		SignalID:      "session-start-" + sid,
		Kind:          coreevents.KindLog,
		CanonicalName: coreevents.CanonicalSessionStart,
		NativeName:    "session_start",
		Timestamp:     time.Now().UTC(),
		SessionID:     sid,
		Attrs: map[string]any{
			"wipnote_sid":       sid,
			"claude_session_id": "claude-abc",
		},
	}
	if err := snk.EmitEvent(context.Background(), ev); err != nil {
		t.Fatalf("EmitEvent: %v", err)
	}
	if err := snk.Close(); err != nil {
		t.Fatalf("Close (flush): %v", err)
	}

	path := filepath.Join(projectDir, ".wipnote", "sessions", sid, "events.ndjson")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events.ndjson: %v", err)
	}
	out := string(data)
	for _, want := range []string{"session_start", sid, "claude-abc"} {
		if !strings.Contains(out, want) {
			t.Errorf("events.ndjson missing %q; got:\n%s", want, out)
		}
	}
}

// TestCanonicalConstantsMatchOtel guards against drift between the core
// vocabulary and otel's own constants. The adapter passes these through
// verbatim, so they must stay equal.
func TestCanonicalConstantsMatchOtel(t *testing.T) {
	if coreevents.KindLog != string(otel.KindLog) {
		t.Errorf("KindLog drift: core %q != otel %q", coreevents.KindLog, otel.KindLog)
	}
	if coreevents.CanonicalSessionStart != otel.CanonicalSessionStart {
		t.Errorf("CanonicalSessionStart drift: core %q != otel %q",
			coreevents.CanonicalSessionStart, otel.CanonicalSessionStart)
	}
}
