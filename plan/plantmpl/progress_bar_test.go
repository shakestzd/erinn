package plantmpl_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
)

func TestProgressBarRendersSection(t *testing.T) {
	pb := &plantmpl.ProgressBar{Approved: 3, Total: 10, Pending: 7}
	var buf bytes.Buffer
	if err := pb.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `id="feedback-summary"`) {
		t.Error(`output missing id="feedback-summary"`)
	}
	if !strings.Contains(html, `class="progress-zone"`) {
		t.Error(`output missing class="progress-zone"`)
	}
}

func TestProgressBarRendersApprovedCount(t *testing.T) {
	pb := &plantmpl.ProgressBar{Approved: 5, Total: 10, Pending: 5}
	var buf bytes.Buffer
	if err := pb.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `>5</strong> approved`) {
		t.Error("expected approved count of 5")
	}
}

func TestProgressBarRendersTotalSections(t *testing.T) {
	pb := &plantmpl.ProgressBar{Approved: 0, Total: 8, Pending: 8}
	var buf bytes.Buffer
	if err := pb.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `>8</strong> sections`) {
		t.Error("expected total of 8 sections")
	}
}

func TestProgressBarRendersPendingCount(t *testing.T) {
	pb := &plantmpl.ProgressBar{Approved: 2, Total: 6, Pending: 4}
	var buf bytes.Buffer
	if err := pb.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `>4</strong> pending`) {
		t.Error("expected pending count of 4")
	}
}

func TestProgressBarPercent(t *testing.T) {
	cases := []struct {
		approved, total, want int
	}{
		{0, 10, 0},
		{5, 10, 50},
		{10, 10, 100},
		{0, 0, 0},
		{3, 10, 30},
	}
	for _, c := range cases {
		pb := &plantmpl.ProgressBar{Approved: c.approved, Total: c.total}
		if got := pb.Percent(); got != c.want {
			t.Errorf("Percent(%d/%d) = %d, want %d", c.approved, c.total, got, c.want)
		}
	}
}

func TestProgressBarRendersProgressFill(t *testing.T) {
	pb := &plantmpl.ProgressBar{Approved: 3, Total: 10, Pending: 7}
	var buf bytes.Buffer
	if err := pb.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `style="width:30%"`) {
		t.Error("expected progress fill width of 30%")
	}
}

func TestProgressBarRendersFinalizeButton(t *testing.T) {
	pb := &plantmpl.ProgressBar{Approved: 0, Total: 5, Pending: 5}
	var buf bytes.Buffer
	if err := pb.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `id="finalizeBtn"`) {
		t.Error("expected finalize button")
	}
}

func TestProgressBarZeroValues(t *testing.T) {
	pb := &plantmpl.ProgressBar{}
	var buf bytes.Buffer
	if err := pb.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `style="width:0%"`) {
		t.Error("expected 0% progress with zero values")
	}
}
