package eventsink

import (
	"context"
	"errors"
	"testing"
)

// resetRegistry restores the package-level factory after a test mutates it,
// keeping tests isolated despite the global registration point.
func resetRegistry(t *testing.T) {
	t.Helper()
	prev := registered
	t.Cleanup(func() { registered = prev })
}

func TestNew_NoFactory_ReturnsNopSink(t *testing.T) {
	resetRegistry(t)
	registered = nil

	snk, err := New("/proj", "sess-1")
	if err != nil {
		t.Fatalf("New with no factory: %v", err)
	}
	if snk == nil {
		t.Fatal("New returned a nil sink; expected a no-op sink")
	}
	// No-op sink must be safe to use and close without error.
	if err := snk.EmitEvent(context.Background(), Event{CanonicalName: CanonicalSessionStart}); err != nil {
		t.Errorf("nop EmitEvent: %v", err)
	}
	if err := snk.Close(); err != nil {
		t.Errorf("nop Close: %v", err)
	}
}

func TestRegister_RoutesToFactory(t *testing.T) {
	resetRegistry(t)

	var gotProject, gotSession string
	var emitted []Event
	Register(func(projectDir, sessionID string) (Sink, error) {
		gotProject, gotSession = projectDir, sessionID
		return &captureSink{out: &emitted}, nil
	})

	snk, err := New("/proj", "sess-9")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if gotProject != "/proj" || gotSession != "sess-9" {
		t.Fatalf("factory got (%q,%q), want (/proj,sess-9)", gotProject, gotSession)
	}
	ev := Event{Harness: "wipnote", CanonicalName: CanonicalSessionStart, Kind: KindLog}
	if err := snk.EmitEvent(context.Background(), ev); err != nil {
		t.Fatalf("EmitEvent: %v", err)
	}
	if len(emitted) != 1 || emitted[0].CanonicalName != CanonicalSessionStart {
		t.Fatalf("captured = %+v, want one session_start event", emitted)
	}
}

func TestNew_PropagatesFactoryError(t *testing.T) {
	resetRegistry(t)
	sentinel := errors.New("boom")
	Register(func(string, string) (Sink, error) { return nil, sentinel })

	if _, err := New("/proj", "sess"); !errors.Is(err, sentinel) {
		t.Fatalf("New error = %v, want %v", err, sentinel)
	}
}

type captureSink struct{ out *[]Event }

func (c *captureSink) EmitEvent(_ context.Context, ev Event) error {
	*c.out = append(*c.out, ev)
	return nil
}
func (c *captureSink) Close() error { return nil }
