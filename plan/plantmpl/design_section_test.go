package plantmpl_test

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
)

func TestDesignSectionRendersID(t *testing.T) {
	d := &plantmpl.DesignSection{}
	var buf bytes.Buffer
	if err := d.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `id="design"`) {
		t.Error(`output missing id="design"`)
	}
	if !strings.Contains(html, `class="section-card"`) {
		t.Error(`output missing class="section-card"`)
	}
}

func TestDesignSectionRendersDataPhase(t *testing.T) {
	d := &plantmpl.DesignSection{}
	var buf bytes.Buffer
	if err := d.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `data-phase="design"`) {
		t.Error(`output missing data-phase="design"`)
	}
}

func TestDesignSectionRendersContent(t *testing.T) {
	d := &plantmpl.DesignSection{Content: template.HTML("<p>Architecture notes</p>")}
	var buf bytes.Buffer
	if err := d.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "<p>Architecture notes</p>") {
		t.Error("expected content to be rendered as raw HTML")
	}
}

func TestDesignSectionEmptyContent(t *testing.T) {
	d := &plantmpl.DesignSection{}
	var buf bytes.Buffer
	if err := d.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `data-action="approve"`) {
		t.Error("expected approval checkbox even with empty content")
	}
}

func TestDesignSectionApprovalCheckbox(t *testing.T) {
	d := &plantmpl.DesignSection{}
	var buf bytes.Buffer
	if err := d.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `data-section="design"`) {
		t.Error(`output missing data-section="design"`)
	}
	if !strings.Contains(html, `data-comment-for="design"`) {
		t.Error(`output missing data-comment-for="design"`)
	}
}
