package receiver_test

import (
	"testing"

	"github.com/shakestzd/wipnote/observe/otel/adapter"
	"github.com/shakestzd/wipnote/observe/otel/receiver"
)

func TestDeriveSignalIDStable(t *testing.T) {
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "claude-code"}}
	scope := adapter.OTLPScope{Name: "com.anthropic.claude_code", Version: "2.1.42"}
	attrs := map[string]any{
		"prompt.id":    "c1be9d1e-10c3-4662-99cf-3d0760787b4c",
		"session.id":   "6bfe7f17-971d-4c30-99f2-1c8b91c87f2b",
		"input_tokens": int64(10),
		"model":        "claude-haiku-4-5-20251001",
	}
	got1 := receiver.DeriveSignalID(res, scope, "claude_code.api_request", 1735000000000, attrs)
	got2 := receiver.DeriveSignalID(res, scope, "claude_code.api_request", 1735000000000, attrs)
	if got1 != got2 {
		t.Errorf("non-deterministic: %s != %s", got1, got2)
	}
	if len(got1) != 16 {
		t.Errorf("DeriveSignalID length = %d, want 16", len(got1))
	}
}

func TestDeriveSignalIDSortsKeys(t *testing.T) {
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "claude-code"}}
	scope := adapter.OTLPScope{}
	// Go map ranging is nondeterministic; the derivation must not care.
	a := map[string]any{"z": 1, "a": 2, "m": 3}
	b := map[string]any{"a": 2, "m": 3, "z": 1}
	id1 := receiver.DeriveSignalID(res, scope, "sig", 1, a)
	id2 := receiver.DeriveSignalID(res, scope, "sig", 1, b)
	if id1 != id2 {
		t.Errorf("sort instability: %s != %s", id1, id2)
	}
}

func TestDeriveSignalIDDiffers(t *testing.T) {
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "claude-code"}}
	scope := adapter.OTLPScope{}
	// Two signals that differ only by timestamp must hash differently.
	id1 := receiver.DeriveSignalID(res, scope, "sig", 1000, map[string]any{"k": "v"})
	id2 := receiver.DeriveSignalID(res, scope, "sig", 1001, map[string]any{"k": "v"})
	if id1 == id2 {
		t.Error("timestamp delta of 1 ns produced identical hash")
	}
	// Two signals differing only by attribute value.
	id3 := receiver.DeriveSignalID(res, scope, "sig", 1000, map[string]any{"k": "v1"})
	id4 := receiver.DeriveSignalID(res, scope, "sig", 1000, map[string]any{"k": "v2"})
	if id3 == id4 {
		t.Error("different attr values hashed identically")
	}
	// Confirm separator escaping: "ab" "cd" vs "a" "bcd" must differ.
	id5 := receiver.DeriveSignalID(res, scope, "sig", 1000, map[string]any{"ab": "cd"})
	id6 := receiver.DeriveSignalID(res, scope, "sig", 1000, map[string]any{"a": "bcd"})
	if id5 == id6 {
		t.Error("separator escape failure — (ab,cd) and (a,bcd) collide")
	}
}
