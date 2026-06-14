package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

func TestConcurrentCLIRepeatedStartAndCompleteAreIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("drives concurrent CLI lifecycle")
	}

	const sessionID = "test-cli-concurrent-status"
	tmpDir, hgDir := testHgDirWithDB(t, sessionID)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)
	if err := testCreate("bug", "Concurrent Status Bug", trackID, "high", false, false); err != nil {
		t.Fatalf("create bug: %v", err)
	}
	bugID := onlyNodeID(t, filepath.Join(hgDir, "bugs", "bug-*.html"))
	if err := runWiAddStep("bug", bugID, "Fix it", false); err != nil {
		t.Fatalf("add step: %v", err)
	}

	runCLIConcurrently(t, 16, func(i int) error {
		return wiSetStatusWithAgent("bug", bugID, "in-progress", sessionID, fmt.Sprintf("agent-%02d", i))
	})
	bug := parseNode(t, filepath.Join(hgDir, "bugs", bugID+".html"))
	if string(bug.Status) != "in-progress" {
		t.Fatalf("after starts: status = %q, want in-progress", bug.Status)
	}

	runCLIConcurrently(t, 16, func(i int) error {
		return wiSetStatusWithAgent("bug", bugID, "done", sessionID, fmt.Sprintf("agent-%02d", i))
	})
	bug = parseNode(t, filepath.Join(hgDir, "bugs", bugID+".html"))
	if string(bug.Status) != "done" {
		t.Fatalf("after completes: status = %q, want done", bug.Status)
	}
	for i, step := range bug.Steps {
		if !step.Completed {
			t.Fatalf("step %d was not completed: %#v", i, step)
		}
	}
}

func TestConcurrentCLIMutationsDoNotLoseCanonicalUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("drives concurrent CLI lifecycle")
	}

	tmpDir, hgDir := testHgDirWithDB(t, "test-cli-concurrent-mutations")
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)
	if err := testCreate("bug", "Concurrent Mutation Bug", trackID, "high", false, false); err != nil {
		t.Fatalf("create bug: %v", err)
	}
	bugID := onlyNodeID(t, filepath.Join(hgDir, "bugs", "bug-*.html"))

	const count = 8
	targetIDs := make([]string, count)
	for i := 0; i < count; i++ {
		if err := testCreate("feature", fmt.Sprintf("Concurrent Target %02d", i), trackID, "medium", false, false); err != nil {
			t.Fatalf("create target %d: %v", i, err)
		}
		targetIDs[i] = nodeIDByTitle(t, filepath.Join(hgDir, "features", "feat-*.html"), fmt.Sprintf("Concurrent Target %02d", i))
	}

	runCLIConcurrently(t, count, func(i int) error {
		if err := runLinkAdd(bugID, targetIDs[i], "blocks"); err != nil {
			return err
		}
		if err := runWiAddStep("bug", bugID, fmt.Sprintf("Step %02d", i), false); err != nil {
			return err
		}
		return runWiUpdate("bug", bugID, &wiUpdateOpts{
			title:    fmt.Sprintf("Concurrent Mutation Bug %02d", i),
			priority: "critical",
		})
	})

	bug := parseNode(t, filepath.Join(hgDir, "bugs", bugID+".html"))
	if len(bug.Edges["blocks"]) != count {
		t.Fatalf("blocks edges: got %d, want %d", len(bug.Edges["blocks"]), count)
	}
	for i := 0; i < count; i++ {
		want := fmt.Sprintf("Step %02d", i)
		found := false
		for _, step := range bug.Steps {
			if step.Description == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing step %q; got %#v", want, bug.Steps)
		}
	}
	if bug.Priority != "critical" {
		t.Fatalf("priority = %q, want critical", bug.Priority)
	}
	if !strings.HasPrefix(bug.Title, "Concurrent Mutation Bug") {
		t.Fatalf("title = %q, want updated title prefix", bug.Title)
	}
}

func runCLIConcurrently(t *testing.T, count int, fn func(int) error) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- fn(i)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent CLI mutation: %v", err)
		}
	}
}

func onlyNodeID(t *testing.T, pattern string) string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	if len(matches) != 1 {
		t.Fatalf("glob %s: got %d matches, want 1", pattern, len(matches))
	}
	return parseNode(t, matches[0]).ID
}

func nodeIDByTitle(t *testing.T, pattern, title string) string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	for _, match := range matches {
		node := parseNode(t, match)
		if node.Title == title {
			return node.ID
		}
	}
	t.Fatalf("glob %s: no node titled %q", pattern, title)
	return ""
}

func parseNode(t *testing.T, path string) *models.Node {
	t.Helper()
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return node
}
