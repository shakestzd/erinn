package cli

import "testing"

import "github.com/spf13/cobra"

func TestBuildRoot_ConfiguresGroupsAndProjectFlag(t *testing.T) {
	var projectDir string
	root := BuildRoot(RootOptions{
		ProjectDirFlag: &projectDir,
		WorkItems: []GroupedCommand{{
			GroupID: "workitems",
			Command: &cobra.Command{Use: "feature"},
		}},
	})

	if root == nil {
		t.Fatal("BuildRoot returned nil")
	}
	if flag := root.PersistentFlags().Lookup("project-dir"); flag == nil {
		t.Fatal("expected project-dir persistent flag")
	}
	if got := findCommand(root, "feature"); got == nil || got.GroupID != "workitems" {
		t.Fatal("expected grouped feature command")
	}
	if len(root.Groups()) != 5 {
		t.Fatalf("expected 5 groups, got %d", len(root.Groups()))
	}
}

func TestBuildRoot_PreservesPersistentPreRunAndUngroupedCommands(t *testing.T) {
	preRunCalled := false
	root := BuildRoot(RootOptions{
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			preRunCalled = true
			return nil
		},
		Ungrouped: []*cobra.Command{{Use: "version"}},
	})

	if root.PersistentPreRunE == nil {
		t.Fatal("expected persistent pre-run hook")
	}
	if err := root.PersistentPreRunE(root, nil); err != nil {
		t.Fatalf("persistent pre-run returned error: %v", err)
	}
	if !preRunCalled {
		t.Fatal("expected persistent pre-run hook to be invoked")
	}
	if findCommand(root, "version") == nil {
		t.Fatal("expected ungrouped command to be registered")
	}
}

func findCommand(root *cobra.Command, name string) *cobra.Command {
	for _, cmd := range root.Commands() {
		if cmd.Name() == name {
			return cmd
		}
	}
	return nil
}
