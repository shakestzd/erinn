package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/workitem"
)

// TestRecommend_SurfacesRollupOnReopenedWIPItem is the CLI-level check for
// feat-f9118b9c's recommend half: `wipnote recommend --json` must surface a
// reopened item's feat-7ee73444 rollup on its WIP row, and must NOT surface
// one for a freshly-started item that has no completion history at all.
// NOTE: Cannot call t.Parallel() — mutates the package-level projectDirFlag.
func TestRecommend_SurfacesRollupOnReopenedWIPItem(t *testing.T) {
	tmpDir := t.TempDir()
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "sessions", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(tmpDir, ".wipnote", sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	p, err := workitem.Open(filepath.Join(tmpDir, ".wipnote"), "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	thrasher, err := p.Features.Create("Thrasher")
	if err != nil {
		t.Fatalf("Create thrasher: %v", err)
	}

	// Seed the FK rows and a failing/passing pair of otel signals, mirroring
	// core/workitem/rollup_test.go's seedSession/seedSignal shape.
	if _, err := p.DB.Exec(`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status) VALUES ('sess-cli', 'claude-code', 'active')`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := p.DB.Exec(`INSERT OR IGNORE INTO features (id, type, title, status) VALUES (?, 'feature', 'seeded', 'in-progress')`, thrasher.ID); err != nil {
		t.Fatalf("seed feature row: %v", err)
	}
	yes, no := true, false
	if _, err := p.DB.Exec(`INSERT INTO otel_signals (signal_id, harness, session_id, feature_id, kind, canonical, native, ts_micros, success, attrs_json)
		VALUES ('sig-1', 'claude-code', 'sess-cli', ?, 'log', 'tool_result', 'tool_result', 1000000, ?, '{}')`, thrasher.ID, yes); err != nil {
		t.Fatalf("seed signal 1: %v", err)
	}
	if _, err := p.DB.Exec(`INSERT INTO otel_signals (signal_id, harness, session_id, feature_id, kind, canonical, native, ts_micros, success, attrs_json)
		VALUES ('sig-2', 'claude-code', 'sess-cli', ?, 'log', 'tool_result', 'tool_result', 2000000, ?, '{}')`, thrasher.ID, no); err != nil {
		t.Fatalf("seed signal 2: %v", err)
	}

	if _, err := p.Features.Start(thrasher.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := p.Features.Complete(thrasher.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Reopen — Start does not touch rollup properties, so this in-progress
	// item now carries the failure it measured before this run started.
	if _, err := p.Features.Start(thrasher.ID); err != nil {
		t.Fatalf("Start (reopen): %v", err)
	}

	clean, err := p.Features.Create("Freshly started, never completed")
	if err != nil {
		t.Fatalf("Create clean: %v", err)
	}
	if _, err := p.Features.Start(clean.ID); err != nil {
		t.Fatalf("Start clean: %v", err)
	}

	origProjectDir := projectDirFlag
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = origProjectDir }()

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	runErr := runRecommend(5, true)

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	os.Stdout = origStdout

	if runErr != nil {
		t.Fatalf("runRecommend: %v", runErr)
	}

	var out recommendOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal recommend JSON: %v\noutput: %s", err, buf.String())
	}

	var thrasherRow, cleanRow *wipRow
	for i := range out.WIP.Items {
		switch out.WIP.Items[i].ID {
		case thrasher.ID:
			thrasherRow = &out.WIP.Items[i]
		case clean.ID:
			cleanRow = &out.WIP.Items[i]
		}
	}
	if thrasherRow == nil || cleanRow == nil {
		t.Fatalf("expected both WIP items in output, got: %+v", out.WIP.Items)
	}

	if thrasherRow.Rollup == nil || !thrasherRow.Rollup.Measured {
		t.Error("reopened item's WIP row carries no measured rollup")
	}
	if thrasherRow.Rollup != nil && !thrasherRow.Rollup.Thrashed() {
		t.Errorf("reopened item's rollup does not read as thrashed: %+v", thrasherRow.Rollup)
	}
	if cleanRow.Rollup != nil {
		t.Errorf("freshly-started item unexpectedly carries a rollup: %+v", cleanRow.Rollup)
	}

	// The Bottlenecks section must independently flag the same item with an
	// itemized, sourced reason — not just a bare boolean.
	found := false
	for _, b := range out.Bottlenecks {
		if b.ItemID == thrasher.ID {
			found = true
			if b.Rollup == nil {
				t.Error("bottleneck entry for thrasher carries no rollup")
			}
		}
	}
	if !found {
		t.Errorf("expected a bottleneck entry for %s, got: %+v", thrasher.ID, out.Bottlenecks)
	}
}
