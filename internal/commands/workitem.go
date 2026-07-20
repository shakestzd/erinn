package commands

import "github.com/spf13/cobra"

type Factory func() *cobra.Command

type WorkItemOptions struct {
	TypeName         string
	DirName          string
	Create           Factory
	List             Factory
	Show             Factory
	Start            Factory
	Complete         Factory
	Delete           Factory
	AddStep          Factory
	AddTaskStep      Factory
	CompleteTaskStep Factory
	CompleteStep     Factory
	Update           Factory
	SetDescription   Factory
	Move             Factory
	Extra            []Factory
}

func BuildWorkItemCommand(opts WorkItemOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   opts.TypeName,
		Short: "Manage " + opts.DirName,
	}
	addAll(cmd,
		opts.Create,
		opts.List,
		opts.Show,
		opts.Start,
		opts.Complete,
		opts.Delete,
		opts.AddStep,
		opts.AddTaskStep,
		opts.CompleteTaskStep,
		opts.CompleteStep,
		opts.Update,
		opts.SetDescription,
	)
	if opts.TypeName != "track" {
		addAll(cmd, opts.Move)
	}
	addAll(cmd, opts.Extra...)
	return cmd
}

func ReplaceSubcommand(parent *cobra.Command, name string, factory Factory) {
	RemoveSubcommand(parent, name)
	addAll(parent, factory)
}

func RemoveSubcommand(parent *cobra.Command, name string) {
	if parent == nil {
		return
	}
	for _, child := range parent.Commands() {
		if child.Name() == name {
			parent.RemoveCommand(child)
			return
		}
	}
}

func addAll(parent *cobra.Command, factories ...Factory) {
	for _, factory := range factories {
		if factory == nil {
			continue
		}
		if child := factory(); child != nil {
			parent.AddCommand(child)
		}
	}
}
