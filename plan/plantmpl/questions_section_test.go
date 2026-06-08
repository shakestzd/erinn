package plantmpl_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
)

// ---------------------------------------------------------------------------
// QuestionsSection.Render — structural output
// ---------------------------------------------------------------------------

func TestQuestionsSectionRendersID(t *testing.T) {
	qs := &plantmpl.QuestionsSection{}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `id="questions"`) {
		t.Error(`output missing id="questions"`)
	}
	if !strings.Contains(html, `class="section-card"`) {
		t.Error(`output missing class="section-card"`)
	}
}

func TestQuestionsSectionRendersDataPhase(t *testing.T) {
	qs := &plantmpl.QuestionsSection{}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-phase="questions"`) {
		t.Error(`output missing data-phase="questions"`)
	}
}

// ---------------------------------------------------------------------------
// QuestionsSection.Render — decision cards
// ---------------------------------------------------------------------------

func TestQuestionsSectionRendersCardBlock(t *testing.T) {
	qs := &plantmpl.QuestionsSection{
		Cards: []plantmpl.DecisionCard{
			{ID: "q1", Text: "What approach should we use?", Options: []string{"Option A", "Option B"}},
		},
	}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `class="question-block"`) {
		t.Error(`output missing class="question-block"`)
	}
	if !strings.Contains(html, `id="q1"`) {
		t.Error(`card block missing id="q1"`)
	}
	if !strings.Contains(html, "What approach should we use?") {
		t.Error("card block missing question text")
	}
}

func TestQuestionsSectionRadioButtonAttributes(t *testing.T) {
	qs := &plantmpl.QuestionsSection{
		Cards: []plantmpl.DecisionCard{
			{ID: "q2", Text: "Pick one", Options: []string{"Alpha", "Beta"}},
		},
	}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `name="q2"`) {
		t.Error(`radio missing name="q2"`)
	}
	if !strings.Contains(html, `data-question="q2"`) {
		t.Error(`radio missing data-question="q2"`)
	}
	if !strings.Contains(html, `value="Alpha"`) {
		t.Error(`radio missing value="Alpha"`)
	}
	if !strings.Contains(html, `value="Beta"`) {
		t.Error(`radio missing value="Beta"`)
	}
}

func TestQuestionsSectionPreSelectedRadioGetsChecked(t *testing.T) {
	qs := &plantmpl.QuestionsSection{
		Cards: []plantmpl.DecisionCard{
			{ID: "q3", Text: "Pick one", Options: []string{"Yes", "No"}, Selected: "Yes"},
		},
	}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `value="Yes" checked`) {
		t.Error(`selected option missing checked attribute`)
	}
	// Unselected option should not have checked
	if strings.Contains(html, `value="No" checked`) {
		t.Error(`unselected option should not have checked attribute`)
	}
}

func TestQuestionsSectionRationaleTextarea(t *testing.T) {
	qs := &plantmpl.QuestionsSection{
		Cards: []plantmpl.DecisionCard{
			{ID: "q4", Text: "Text", Options: []string{"A"}, Rationale: "Because of X"},
		},
	}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-question-rationale="q4"`) {
		t.Error(`textarea missing data-question-rationale="q4"`)
	}
	if !strings.Contains(html, "Because of X") {
		t.Error("textarea missing existing rationale text")
	}
}

// ---------------------------------------------------------------------------
// QuestionsSection.Render — recap table
// ---------------------------------------------------------------------------

func TestQuestionsSectionRecapTableStructure(t *testing.T) {
	qs := &plantmpl.QuestionsSection{
		Cards: []plantmpl.DecisionCard{
			{ID: "q1", Text: "First question", Options: []string{"A", "B"}},
		},
	}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `<th>Question</th>`) {
		t.Error("recap table missing Question column header")
	}
	if !strings.Contains(html, `<th>Selected</th>`) {
		t.Error("recap table missing Selected column header")
	}
	if !strings.Contains(html, `<th>Status</th>`) {
		t.Error("recap table missing Status column header")
	}
	if !strings.Contains(html, `id="questionsRecap"`) {
		t.Error(`recap table missing id="questionsRecap"`)
	}
	if !strings.Contains(html, `data-recap-for="q1"`) {
		t.Error(`recap row missing data-recap-for="q1"`)
	}
}

func TestQuestionsSectionRecapShowsDecidedBadge(t *testing.T) {
	qs := &plantmpl.QuestionsSection{
		Cards: []plantmpl.DecisionCard{
			{ID: "q1", Text: "Decided question", Options: []string{"A", "B"}, Selected: "A"},
		},
	}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Decided") {
		t.Error("recap should show Decided badge for card with Selected value")
	}
	if !strings.Contains(html, "badge-approved") {
		t.Error("recap should use badge-approved for decided card")
	}
}

func TestQuestionsSectionEmptyCardsRendersSection(t *testing.T) {
	qs := &plantmpl.QuestionsSection{
		Cards: []plantmpl.DecisionCard{},
	}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `id="questions"`) {
		t.Error(`empty Cards should still render section with id="questions"`)
	}
	if !strings.Contains(html, `id="questionsRecap"`) {
		t.Error(`empty Cards should still render recap tbody`)
	}
	// No question-block divs expected
	if strings.Contains(html, `class="question-block"`) {
		t.Error(`empty Cards should not render any question-block divs`)
	}
}

func TestQuestionsSectionRationaleTextareaWithExistingText(t *testing.T) {
	qs := &plantmpl.QuestionsSection{
		Cards: []plantmpl.DecisionCard{
			{ID: "q5", Text: "Method?", Options: []string{"REST", "GraphQL"}, Rationale: "REST is simpler"},
		},
	}

	var buf bytes.Buffer
	if err := qs.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "REST is simpler") {
		t.Error("rationale textarea should contain existing rationale text")
	}
}
