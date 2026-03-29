package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/shakestzd/htmlgraph/internal/workitem"
	"github.com/spf13/cobra"
)

func wiAddStepCmd(typeName string) *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "add-step <id> [descriptions...]",
		Short: "Add implementation steps to a " + typeName,
		Long:  "Add one or more steps. Pass descriptions as arguments or use --stdin for newline-separated input.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runWiAddStep(typeName, args[0], args[1:], fromStdin)
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read step descriptions from stdin (one per line)")
	return cmd
}

func runWiAddStep(typeName, id string, descriptions []string, fromStdin bool) error {
	if fromStdin {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				descriptions = append(descriptions, line)
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}

	if len(descriptions) == 0 {
		return fmt.Errorf("no step descriptions provided")
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
	eb := col.Edit(id)
	for _, desc := range descriptions {
		eb = eb.AddStep(desc)
	}
	if err := eb.Save(); err != nil {
		return fmt.Errorf("add step: %w", err)
	}

	if len(descriptions) == 1 {
		fmt.Printf("Added step to %s: %s\n", id, descriptions[0])
	} else {
		fmt.Printf("Added %d steps to %s\n", len(descriptions), id)
	}
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
