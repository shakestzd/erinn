package main

import (
	"fmt"
	"strconv"

	"github.com/shakestzd/htmlgraph/internal/workitem"
	"github.com/spf13/cobra"
)

func wiAddStepCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "add-step <id> <description>",
		Short: "Add an implementation step to a " + typeName,
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiAddStep(typeName, args[0], args[1])
		},
	}
}

func runWiAddStep(typeName, id, description string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := collectionFor(p, typeName)
	if err := col.Edit(id).AddStep(description).Save(); err != nil {
		return fmt.Errorf("add step: %w", err)
	}
	fmt.Printf("Added step to %s: %s\n", id, description)
	return nil
}

func wiRemoveStepCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "remove-step <id> <step-number>",
		Short: "Remove a step from a " + typeName,
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiRemoveStep(typeName, args[0], args[1])
		},
	}
}

func runWiRemoveStep(typeName, id, indexStr string) error {
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		return fmt.Errorf("invalid step number %q: %w", indexStr, err)
	}

	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := collectionFor(p, typeName)
	if err := col.Edit(id).RemoveStep(index).Save(); err != nil {
		return fmt.Errorf("remove step: %w", err)
	}
	fmt.Printf("Removed step %d from %s\n", index, id)
	return nil
}

func wiUpdateStepCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "update-step <id> <step-number> <new-description>",
		Short: "Update a step's description in a " + typeName,
		Args:  cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiUpdateStep(typeName, args[0], args[1], args[2])
		},
	}
}

func runWiUpdateStep(typeName, id, indexStr, description string) error {
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		return fmt.Errorf("invalid step number %q: %w", indexStr, err)
	}

	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := collectionFor(p, typeName)
	if err := col.Edit(id).UpdateStep(index, description).Save(); err != nil {
		return fmt.Errorf("update step: %w", err)
	}
	fmt.Printf("Updated step %d in %s\n", index, id)
	return nil
}

func wiCompleteStepCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "complete-step <id> <step-number>",
		Short: "Mark a step as done in a " + typeName,
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiCompleteStep(typeName, args[0], args[1])
		},
	}
}

func runWiCompleteStep(typeName, id, indexStr string) error {
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		return fmt.Errorf("invalid step number %q: %w", indexStr, err)
	}

	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := collectionFor(p, typeName)
	if err := col.Edit(id).CompleteStep(index).Save(); err != nil {
		return fmt.Errorf("complete step: %w", err)
	}
	fmt.Printf("Completed step %d in %s\n", index, id)
	return nil
}

func wiEditDescriptionCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "edit-description <id> <description>",
		Short: "Set or update the description of a " + typeName,
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiEditDescription(typeName, args[0], args[1])
		},
	}
}

func runWiEditDescription(typeName, id, description string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := collectionFor(p, typeName)
	if err := col.Edit(id).SetDescription(description).Save(); err != nil {
		return fmt.Errorf("edit description: %w", err)
	}
	fmt.Printf("Updated description for %s\n", id)
	return nil
}
