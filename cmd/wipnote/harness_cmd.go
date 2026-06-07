package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/shakestzd/wipnote/core/harness"
	"github.com/spf13/cobra"
)

func harnessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "harness",
		Short: "Inspect registered harness configurations",
	}
	cmd.AddCommand(harnessListCmd())
	return cmd
}

func harnessListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all registered harnesses with their key configuration values",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHarnessList(cmd.OutOrStdout())
		},
	}
}

func runHarnessList(out io.Writer) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tAgentID\tServiceNames\tSessionAttr\tHookEventNames"); err != nil {
		return err
	}
	for _, cfg := range harness.All() {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			cfg.ID,
			cfg.AgentID,
			strings.Join(cfg.ServiceNames, ","),
			cfg.SessionAttr,
			strings.Join(cfg.HookEventNames, ","),
		); err != nil {
			return err
		}
	}
	return w.Flush()
}
