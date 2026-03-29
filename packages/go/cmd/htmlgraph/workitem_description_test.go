package main

import (
	"strings"
	"testing"
)

func TestEditDescription(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Desc Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	// Content must be wrapped in an element (e.g. <p>) to survive the HTML
	// round-trip, because the parser reads child elements, not raw text nodes.
	if err := runWiEditDescription("feature", featID, "<p>New description text</p>"); err != nil {
		t.Fatalf("edit description: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if !strings.Contains(node.Content, "New description text") {
		t.Errorf("content = %q, want it to contain %q", node.Content, "New description text")
	}
}

func TestEditDescriptionOverwrite(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Overwrite Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	if err := runWiEditDescription("feature", featID, "<p>Original</p>"); err != nil {
		t.Fatalf("set original description: %v", err)
	}
	if err := runWiEditDescription("feature", featID, "<p>Updated</p>"); err != nil {
		t.Fatalf("overwrite description: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if strings.Contains(node.Content, "Original") {
		t.Errorf("content should not contain %q after overwrite, got %q", "Original", node.Content)
	}
	if !strings.Contains(node.Content, "Updated") {
		t.Errorf("content = %q, want it to contain %q", node.Content, "Updated")
	}
}
