package plantmpl_test

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
)

func TestOutlineSectionRendersID(t *testing.T) {
	o := &plantmpl.OutlineSection{}
	var buf bytes.Buffer
	if err := o.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `id="outline"`) {
		t.Error(`output missing id="outline"`)
	}
	if !strings.Contains(html, `class="section-card"`) {
		t.Error(`output missing class="section-card"`)
	}
}

func TestOutlineSectionRendersDataPhase(t *testing.T) {
	o := &plantmpl.OutlineSection{}
	var buf bytes.Buffer
	if err := o.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `data-phase="outline"`) {
		t.Error(`output missing data-phase="outline"`)
	}
}

func TestOutlineSectionRendersContent(t *testing.T) {
	o := &plantmpl.OutlineSection{Content: template.HTML("<p>Implementation outline</p>")}
	var buf bytes.Buffer
	if err := o.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "<p>Implementation outline</p>") {
		t.Error("expected content to be rendered as raw HTML")
	}
}

func TestOutlineSectionEmptyContent(t *testing.T) {
	o := &plantmpl.OutlineSection{}
	var buf bytes.Buffer
	if err := o.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `data-action="approve"`) {
		t.Error("expected approval checkbox even with empty content")
	}
}

func TestOutlineSectionApprovalCheckbox(t *testing.T) {
	o := &plantmpl.OutlineSection{}
	var buf bytes.Buffer
	if err := o.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `data-section="outline"`) {
		t.Error(`output missing data-section="outline"`)
	}
	if !strings.Contains(html, `data-comment-for="outline"`) {
		t.Error(`output missing data-comment-for="outline"`)
	}
}
