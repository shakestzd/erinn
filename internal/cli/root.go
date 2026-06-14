package cli

import "github.com/spf13/cobra"

type GroupedCommand struct {
	GroupID string
	Command *cobra.Command
}

type RootOptions struct {
	ProjectDirFlag    *string
	PersistentPreRunE func(*cobra.Command, []string) error
	WorkItems         []GroupedCommand
	Query             []GroupedCommand
	Quality           []GroupedCommand
	Data              []GroupedCommand
	Dev               []GroupedCommand
	Ungrouped         []*cobra.Command
}

func BuildRoot(opts RootOptions) *cobra.Command {
	root := &cobra.Command{
		Use:           "wipnote",
		Short:         "Causal lineage and observability for AI-assisted development",
		Long:          "wipnote — trace causal lineage across work items, commits, sessions, and agent spawns. Local-first observability and coordination for AI-assisted development.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	if opts.ProjectDirFlag != nil {
		root.PersistentFlags().StringVar(
			opts.ProjectDirFlag,
			"project-dir",
			"",
			"explicit project root containing .wipnote/ (overrides CLAUDE_PROJECT_DIR and CWD walk-up)",
		)
	}

	root.PersistentPreRunE = opts.PersistentPreRunE

	root.AddGroup(&cobra.Group{ID: "workitems", Title: "Work Items"})
	root.AddGroup(&cobra.Group{ID: "query", Title: "Query & Status"})
	root.AddGroup(&cobra.Group{ID: "quality", Title: "Quality"})
	root.AddGroup(&cobra.Group{ID: "data", Title: "Data"})
	root.AddGroup(&cobra.Group{ID: "dev", Title: "Dev"})

	addGrouped(root, opts.WorkItems...)
	addGrouped(root, opts.Query...)
	addGrouped(root, opts.Quality...)
	addGrouped(root, opts.Data...)
	addGrouped(root, opts.Dev...)

	for _, cmd := range opts.Ungrouped {
		if cmd != nil {
			root.AddCommand(cmd)
		}
	}

	return root
}

func addGrouped(root *cobra.Command, grouped ...GroupedCommand) {
	for _, item := range grouped {
		if item.Command == nil {
			continue
		}
		item.Command.GroupID = item.GroupID
		root.AddCommand(item.Command)
	}
}
