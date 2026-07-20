package commands

import "testing"

import "github.com/spf13/cobra"

func named(name string) Factory {
	return func() *cobra.Command {
		return &cobra.Command{Use: name}
	}
}

func TestBuildWorkItemCommand_AddsStandardSubcommands(t *testing.T) {
	cmd := BuildWorkItemCommand(WorkItemOptions{
		TypeName:         "feature",
		DirName:          "features",
		Create:           named("create"),
		List:             named("list"),
		Show:             named("show"),
		Start:            named("start"),
		Complete:         named("complete"),
		Delete:           named("delete"),
		AddStep:          named("add-step"),
		AddTaskStep:      named("add-task-step"),
		CompleteTaskStep: named("complete-task-step"),
		CompleteStep:     named("complete-step"),
		Update:           named("update"),
		SetDescription:   named("set-description"),
		Move:             named("move"),
	})

	for _, sub := range []string{
		"create", "list", "show", "start", "complete", "delete",
		"add-step", "add-task-step", "complete-task-step", "complete-step", "update",
		"set-description", "move",
	} {
		if findSubcommand(cmd, sub) == nil {
			t.Fatalf("expected subcommand %q to be registered", sub)
		}
	}
}

func TestBuildWorkItemCommand_SkipsMoveForTrack(t *testing.T) {
	cmd := BuildWorkItemCommand(WorkItemOptions{
		TypeName: "track",
		DirName:  "tracks",
		Move:     named("move"),
	})

	if findSubcommand(cmd, "move") != nil {
		t.Fatal("track command should not include move")
	}
}

func TestReplaceSubcommand_ReplacesByName(t *testing.T) {
	cmd := &cobra.Command{Use: "parent"}
	cmd.AddCommand(&cobra.Command{Use: "show", Short: "old"})

	ReplaceSubcommand(cmd, "show", func() *cobra.Command {
		return &cobra.Command{Use: "show", Short: "new"}
	})

	show := findSubcommand(cmd, "show")
	if show == nil {
		t.Fatal("expected show command to exist")
	}
	if show.Short != "new" {
		t.Fatalf("expected replacement command, got short=%q", show.Short)
	}
}

func findSubcommand(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}
