package htmlparse_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

const sampleHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>W2: Go Module Scaffold + Core Models</title>
</head>
<body>
    <article id="feat-5cf17fca"
             data-type="feature"
             data-status="in-progress"
             data-priority="medium"
             data-created="2026-03-26T08:51:02.613892"
             data-updated="2026-03-26T12:53:54.695067+00:00"
             data-agent-assigned="claude-code"
             data-track-id="trk-696ae199">

        <header>
            <h1>W2: Go Module Scaffold + Core Models</h1>
        </header>

        <nav data-graph-edges>
            <section data-edge-type="implemented-in">
                <h3>Implemented-In:</h3>
                <ul>
                    <li><a href="sess-001.html" data-relationship="implemented-in" data-since="2026-03-26T08:51:02.622637">sess-001</a></li>
                    <li><a href="sess-002.html" data-relationship="implemented-in" data-since="2026-03-26T08:53:17.432640">sess-002</a></li>
                </ul>
            </section>
        </nav>
        <section data-steps>
            <h3>Implementation Steps</h3>
            <ol>
                <li data-completed="true" data-step-id="step-feat-5cf17fca-0">&#x2705; Scaffold Go module</li>
                <li data-completed="false" data-step-id="step-feat-5cf17fca-1">&#x23F3; Port core data models</li>
                <li data-completed="false" data-step-id="step-feat-5cf17fca-2" data-depends-on="step-feat-5cf17fca-1">&#x23F3; Port SQLite schema</li>
            </ol>
        </section>
        <section data-content>
            <h3>Description</h3>
            <p>Go module scaffold for wipnote</p>
        </section>
    </article>
</body>
</html>`

func TestParseString(t *testing.T) {
	node, err := htmlparse.ParseString(sampleHTML)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}

	assertString(t, "ID", node.ID, "feat-5cf17fca")
	assertString(t, "Type", node.Type, "feature")
	assertString(t, "Status", string(node.Status), "in-progress")
	assertString(t, "Priority", string(node.Priority), "medium")
	assertString(t, "Title", node.Title, "W2: Go Module Scaffold + Core Models")
	assertString(t, "AgentAssigned", node.AgentAssigned, "claude-code")
	assertString(t, "TrackID", node.TrackID, "trk-696ae199")

	// Timestamps
	if node.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if node.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}

	// Edges
	edges, ok := node.Edges["implemented-in"]
	if !ok {
		t.Fatal("missing implemented-in edges")
	}
	if len(edges) != 2 {
		t.Fatalf("edge count: got %d, want 2", len(edges))
	}
	assertString(t, "edge[0].TargetID", edges[0].TargetID, "sess-001")
	assertString(t, "edge[1].TargetID", edges[1].TargetID, "sess-002")
	if edges[0].Since.IsZero() {
		t.Error("edge[0].Since should not be zero")
	}

	// Steps
	if len(node.Steps) != 3 {
		t.Fatalf("steps count: got %d, want 3", len(node.Steps))
	}
	if !node.Steps[0].Completed {
		t.Error("step 0 should be completed")
	}
	if node.Steps[1].Completed {
		t.Error("step 1 should not be completed")
	}
	assertString(t, "step[0].StepID", node.Steps[0].StepID, "step-feat-5cf17fca-0")
	if len(node.Steps[2].DependsOn) != 1 || node.Steps[2].DependsOn[0] != "step-feat-5cf17fca-1" {
		t.Errorf("step 2 depends_on: got %v", node.Steps[2].DependsOn)
	}

	// Content
	if node.Content == "" {
		t.Error("Content should not be empty")
	}
}

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feat-test.html")
	if err := os.WriteFile(path, []byte(sampleHTML), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	node, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	assertString(t, "ID", node.ID, "feat-5cf17fca")
	assertString(t, "Title", node.Title, "W2: Go Module Scaffold + Core Models")
}

func TestParseMinimalHTML(t *testing.T) {
	html := `<html><body>
		<article id="bug-001" data-type="bug" data-status="todo" data-priority="critical">
			<header><h1>Critical Bug</h1></header>
		</article>
	</body></html>`

	node, err := htmlparse.ParseString(html)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}
	assertString(t, "ID", node.ID, "bug-001")
	assertString(t, "Type", node.Type, "bug")
	assertString(t, "Status", string(node.Status), "todo")
	assertString(t, "Priority", string(node.Priority), "critical")
	assertString(t, "Title", node.Title, "Critical Bug")
}

func TestParseNoArticle(t *testing.T) {
	html := `<html><body><p>no article here</p></body></html>`
	_, err := htmlparse.ParseString(html)
	if err == nil {
		t.Error("expected error for missing article")
	}
}

func TestParseRealWorkItem(t *testing.T) {
	// This test uses a real HTML feature file from the .wipnote/ directory.
	// It validates that the Go parser produces the same results as the Python parser.
	realHTML := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="wipnote-version" content="1.0">
    <title>Test Feature 1</title>
    <link rel="stylesheet" href="../styles.css">
</head>
<body>
    <article id="feat-01902455"
             data-type="feature"
             data-status="done"
             data-priority="medium"
             data-created="2026-03-22T23:17:08.414652"
             data-updated="2026-03-22T23:17:13.331617" data-agent-assigned="test-agent" data-track-id="trk-e5d6a365">

        <header>
            <h1>Test Feature 1</h1>
            <div class="metadata">
                <span class="badge status-done">Done</span>
                <span class="badge priority-medium">Medium Priority</span>
            </div>
        </header>

        <nav data-graph-edges>
            <section data-edge-type="implemented-in">
                <h3>Implemented-In:</h3>
                <ul>
                    <li><a href="sess-e1f8958b.html" data-relationship="implemented-in" data-since="2026-03-22T23:17:13.318561">sess-e1f8958b</a></li>
                </ul>
            </section>
        </nav>
    </article>
</body>
</html>`

	node, err := htmlparse.ParseString(realHTML)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}

	assertString(t, "ID", node.ID, "feat-01902455")
	assertString(t, "Type", node.Type, "feature")
	assertString(t, "Status", string(node.Status), string(models.StatusDone))
	assertString(t, "Priority", string(node.Priority), string(models.PriorityMedium))
	assertString(t, "Title", node.Title, "Test Feature 1")
	assertString(t, "AgentAssigned", node.AgentAssigned, "test-agent")
	assertString(t, "TrackID", node.TrackID, "trk-e5d6a365")

	edges := node.Edges["implemented-in"]
	if len(edges) != 1 {
		t.Fatalf("edge count: got %d, want 1", len(edges))
	}
	assertString(t, "edge target", edges[0].TargetID, "sess-e1f8958b")
}

func assertString(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %q, want %q", field, got, want)
	}
}
