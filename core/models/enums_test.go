package models_test

import (
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/models"
)

func TestNormalizeRelationship(t *testing.T) {
	tests := []struct {
		input string
		want  models.RelationshipType
	}{
		{"implemented-in", models.RelImplementedIn},
		{"implemented_in", models.RelImplementedIn},
		{"BLOCKED_BY", models.RelBlockedBy},
		{"Blocked_By", models.RelBlockedBy},
		{"relates-to", models.RelRelatesTo},
		{"relates_to", models.RelRelatesTo},
		{"blocks", models.RelBlocks},
		{"BLOCKS", models.RelBlocks},
		{"spawned-from", models.RelSpawnedFrom},
		{"spawned_from", models.RelSpawnedFrom},
		{"part-of", models.RelPartOf},
		{"part_of", models.RelPartOf},
		{"contains", models.RelContains},
		// Convenience aliases
		{"child", models.RelContains},
		{"CHILD", models.RelContains},
		{"parent", models.RelPartOf},
		{"dep", models.RelBlockedBy},
		{"depends", models.RelBlockedBy},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := models.NormalizeRelationship(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeRelationship(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsValidRelationship(t *testing.T) {
	valid := []models.RelationshipType{
		models.RelBlocks,
		models.RelBlockedBy,
		models.RelRelatesTo,
		models.RelImplements,
		models.RelCausedBy,
		models.RelSpawnedFrom,
		models.RelImplementedIn,
		models.RelPartOf,
		models.RelContains,
		models.RelPlannedIn,
		models.RelLearnedFrom,
		models.RelHasLearning,
	}
	for _, r := range valid {
		if !models.IsValidRelationship(r) {
			t.Errorf("IsValidRelationship(%q) = false, want true", r)
		}
	}

	invalid := []models.RelationshipType{
		"implemented-in",
		"blocked-by",
		"relates-to",
		"unknown",
		"",
	}
	for _, r := range invalid {
		if models.IsValidRelationship(r) {
			t.Errorf("IsValidRelationship(%q) = true, want false", r)
		}
	}
}

func TestAllConstantsUseUnderscores(t *testing.T) {
	for _, r := range models.ValidRelationshipTypes {
		s := string(r)
		if strings.Contains(s, "-") {
			t.Errorf("RelationshipType constant %q contains a hyphen; use underscores", s)
		}
	}
}

func TestAliasesProduceValidRelationships(t *testing.T) {
	aliases := []string{"child", "parent", "dep", "depends"}
	for _, a := range aliases {
		normalized := models.NormalizeRelationship(a)
		if !models.IsValidRelationship(normalized) {
			t.Errorf("alias %q normalizes to %q which is not a valid relationship", a, normalized)
		}
	}
}

func TestNewRelationshipTypes(t *testing.T) {
	if models.RelPartOf != "part_of" {
		t.Errorf("RelPartOf = %q, want %q", models.RelPartOf, "part_of")
	}
	if models.RelContains != "contains" {
		t.Errorf("RelContains = %q, want %q", models.RelContains, "contains")
	}
}
