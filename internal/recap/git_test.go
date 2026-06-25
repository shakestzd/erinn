package recap

import (
	"testing"
)

func TestIsWipnotePath(t *testing.T) {
	failures := testIsWipnotePath()
	for _, failure := range failures {
		t.Error(failure)
	}
	if len(failures) > 0 {
		t.Fatalf("TestIsWipnotePath: %d failures", len(failures))
	}
}

func TestParseUnifiedDiffFiltersWipnote(t *testing.T) {
	failures := testParseUnifiedDiffFiltersWipnote()
	for _, failure := range failures {
		t.Error(failure)
	}
	if len(failures) > 0 {
		t.Fatalf("TestParseUnifiedDiffFiltersWipnote: %d failures", len(failures))
	}
}
