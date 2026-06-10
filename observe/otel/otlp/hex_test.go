package otlp_test

import (
	"testing"

	"github.com/shakestzd/wipnote/observe/otel/otlp"
)

func TestHexEncodeRoundTrip(t *testing.T) {
	// Exact trace ID captured from the live Claude OTLP run.
	original := []byte{
		0xa4, 0xe2, 0x8f, 0x48, 0xfb, 0xdb, 0x66, 0x44,
		0xa9, 0x2b, 0x20, 0x8f, 0x21, 0x45, 0xae, 0xe1,
	}
	encoded := otlp.HexEncodeID(original)
	if encoded != "a4e28f48fbdb6644a92b208f2145aee1" {
		t.Errorf("HexEncodeID = %q, want a4e28f48fbdb6644a92b208f2145aee1", encoded)
	}
	decoded, err := otlp.HexDecodeID(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded) != string(original) {
		t.Errorf("round-trip mismatch")
	}
}

func TestHexEncodeEmpty(t *testing.T) {
	if got := otlp.HexEncodeID(nil); got != "" {
		t.Errorf("HexEncodeID(nil) = %q, want empty", got)
	}
	if got := otlp.HexEncodeID([]byte{}); got != "" {
		t.Errorf("HexEncodeID(empty) = %q, want empty", got)
	}
	b, err := otlp.HexDecodeID("")
	if err != nil || b != nil {
		t.Errorf("HexDecodeID(empty) = (%v, %v), want (nil, nil)", b, err)
	}
}

func TestValidTraceID(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		ok   bool
	}{
		{"valid_16_bytes", make([]byte, 16), false}, // all zero → invalid
		{"valid_nonzero", []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, true},
		{"too_short", []byte{1, 2, 3, 4, 5, 6, 7, 8}, false},
		{"too_long", make([]byte, 32), false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := otlp.ValidTraceID(tc.in); got != tc.ok {
				t.Errorf("ValidTraceID(%v) = %v, want %v", tc.in, got, tc.ok)
			}
		})
	}
}

func TestValidSpanID(t *testing.T) {
	if !otlp.ValidSpanID([]byte{0, 0, 0, 0, 0, 0, 0, 1}) {
		t.Error("8-byte nonzero span ID rejected")
	}
	if otlp.ValidSpanID(make([]byte, 8)) {
		t.Error("all-zero span ID accepted")
	}
	if otlp.ValidSpanID([]byte{1, 2, 3, 4}) {
		t.Error("4-byte span ID accepted")
	}
}
