package main

import (
	"fmt"
	"strings"

	"github.com/shakestzd/wipnote/port/pluginbuild"
	"github.com/spf13/cobra"
)

// pluginCheckPortsCmd is `wipnote plugin check-ports`. It is the drift gate:
// it regenerates every target plugin tree into a tempdir from the shared
// manifest at packages/plugin-core/manifest.json and diffs the result against
// the committed trees. Any divergence exits non-zero and names every drifted
// path so the caller knows exactly what to regenerate.
//
// This is the authoritative check used by the scoped pre-commit hook and by
// scripts/deploy-all.sh — it is a full regenerate-and-compare with no checksum
// cache, because the generator is the only source of truth.
func pluginCheckPortsCmd() *cobra.Command {
	var (
		targetFlag   string
		manifestFlag string
	)
	cmd := &cobra.Command{
		Use:   "check-ports",
		Short: "Fail if any generated plugin tree is out of sync with plugin-core",
		Long: "Regenerate every target plugin tree (plugin/ for Claude Code, " +
			"packages/codex-marketplace/ for Codex CLI, " +
			"packages/gemini-extension/ for Gemini CLI) into a tempdir and diff " +
			"it against the committed trees. Exits non-zero on any drift, " +
			"listing each drifted path. Run 'wipnote plugin build-ports' and " +
			"commit the result to resolve drift.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := resolvePluginBuildContext(manifestFlag, targetFlag)
			if err != nil {
				return err
			}

			drifts, err := pluginbuild.CheckPorts(ctx.manifest, ctx.repoRoot, ctx.targets)
			if err != nil {
				return err
			}
			if len(drifts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "plugin ports in sync")
				return nil
			}

			out := cmd.OutOrStderr()
			fmt.Fprintf(out, "plugin ports out of sync (%d drifted path(s)):\n", len(drifts))
			for _, d := range drifts {
				fmt.Fprintf(out, "  %s\n", d)
			}
			fmt.Fprintln(out, "run 'wipnote plugin build-ports' and commit the regenerated trees")
			// SilenceUsage/SilenceErrors so the cobra usage dump does not bury
			// the drift list; the non-nil error still yields a non-zero exit.
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return fmt.Errorf("plugin ports drift detected")
		},
	}
	cmd.Flags().StringVar(&targetFlag, "target", "all",
		"target to check: all | "+strings.Join(pluginbuild.Names(), " | "))
	cmd.Flags().StringVar(&manifestFlag, "manifest", "",
		"path to plugin-core manifest (default: autodetect packages/plugin-core/manifest.json)")
	return cmd
}
