package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/shakestzd/wipnote/internal/guardprofile"
)

// guardInitDeps bundles the side-effecting seams ensureGuardProfile/guard init
// depend on, so tests can drive the propose/approve/persist core without
// touching the real TTY, wall clock, git, or stdin/stdout.
type guardInitDeps struct {
	// interactive reports whether the launch is attached to a TTY. Production
	// uses isTerminal(); a non-interactive launch is skipped silently.
	interactive func() bool
	// now supplies the approval timestamp.
	now func() time.Time
	// approver resolves the human identity recorded in approved.by.
	approver func(repoRoot string) string
	// commit stages and commits the written profile. Production wraps
	// runGitMutation; tests record the call.
	commit func(repoRoot string, paths []string, message string) error
	// in/out are the approval prompt's I/O. Production uses stdin/stderr.
	in  io.Reader
	out io.Writer
}

// defaultGuardInitDeps wires the production seams.
func defaultGuardInitDeps() guardInitDeps {
	return guardInitDeps{
		interactive: isTerminal,
		now:         time.Now,
		approver:    resolveGuardApprover,
		commit: func(repoRoot string, paths []string, message string) error {
			addArgs := append([]string{"add", "--"}, paths...)
			_, err := runGitMutationBatch(repoRoot,
				addArgs,
				[]string{"commit", "-m", message, "--"},
			)
			return err
		},
		in:  os.Stdin,
		out: os.Stderr,
	}
}

// resolveGuardApprover returns git config user.name, falling back to a
// generated session id when git identity is unavailable.
func resolveGuardApprover(repoRoot string) string {
	if out, err := runGit("-C", repoRoot, "config", "--get", "user.name"); err == nil {
		if name := strings.TrimSpace(out); name != "" {
			return name
		}
	}
	return generateSessionID()
}

// ensureGuardProfile is the launch-time initialization step (peer to the OTel
// collector bootstrap). On every interactive CLI launch it ensures the project
// has an APPROVED guard profile:
//
//   - If an approved profile already exists -> no-op.
//   - If the launch is NON-interactive -> skip silently (never block a
//     non-interactive launch; re-offered on the next interactive launch).
//   - Otherwise (interactive, unconfigured) -> propose a profile from manifests,
//     present it for approve/decline, and on approval sign + write
//     .wipnote/guard-profile.yaml and commit it. Decline/defer proceeds without
//     a profile (re-offered next launch).
//
// All failures are non-fatal: a launch is never blocked by guard setup.
func ensureGuardProfile(projectRoot string) {
	ensureGuardProfileWith(projectRoot, defaultGuardInitDeps())
}

func ensureGuardProfileWith(projectRoot string, deps guardInitDeps) {
	if projectRoot == "" {
		return
	}
	p, err := guardprofile.Load(projectRoot)
	if err != nil {
		// Unreadable/unparseable profile: surface nothing fatal here; the gate
		// path already reports malformed profiles. Skip setup.
		return
	}
	if guardprofile.IsApproved(p) {
		return // already configured
	}
	if !deps.interactive() {
		return // never block / prompt a non-interactive launch
	}
	if _, err := runGuardSetup(projectRoot, deps); err != nil {
		fmt.Fprintf(deps.out, "wipnote: guard setup skipped: %v\n", err)
	}
}

// runGuardSetup performs the propose -> approve -> sign -> persist -> commit
// core. It returns the written Profile (or nil when declined/deferred). It is
// the shared body behind both ensureGuardProfile and the explicit
// `wipnote guard init` command, so the two paths cannot drift.
func runGuardSetup(projectRoot string, deps guardInitDeps) (*guardprofile.Profile, error) {
	prop, err := guardprofile.Propose(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("propose guard profile: %w", err)
	}
	if prop == nil || prop.Profile == nil || len(prop.Guards) == 0 {
		fmt.Fprintln(deps.out, "wipnote: no quality/test signals detected — skipping guard profile setup.")
		return nil, nil
	}

	renderProposal(deps.out, prop)

	if !promptApprove(deps.in, deps.out) {
		fmt.Fprintln(deps.out, "wipnote: guard profile deferred — re-offered on next launch. Run `wipnote guard init` anytime.")
		return nil, nil
	}

	p := prop.Profile
	p.Approved = guardprofile.Approval{
		Signature: guardprofile.Signature(p),
		By:        deps.approver(projectRoot),
		At:        deps.now().UTC().Format(time.RFC3339),
	}
	if err := writeGuardProfile(projectRoot, p); err != nil {
		return nil, fmt.Errorf("write guard profile: %w", err)
	}
	rel := filepath.FromSlash(guardprofile.RelPath)
	msg := "chore(wipnote): approve project guard profile (feat-18dac61f)"
	if err := deps.commit(projectRoot, []string{rel}, msg); err != nil {
		return nil, fmt.Errorf("commit guard profile: %w", err)
	}
	fmt.Fprintf(deps.out, "wipnote: guard profile approved and committed at %s\n", guardprofile.RelPath)
	return p, nil
}

// renderProposal prints the proposed guards grouped by phase, flagging
// low-confidence entries so approval is prune-not-invent.
func renderProposal(out io.Writer, prop *guardprofile.Proposal) {
	fmt.Fprintln(out, "wipnote: proposed project guard profile (review, then approve):")
	for _, phase := range []string{guardprofile.PhaseQuality, guardprofile.PhaseCompletion, guardprofile.PhaseYolo} {
		var pgs []guardprofile.ProposedGuard
		for _, pg := range prop.Guards {
			if pg.Phase == phase {
				pgs = append(pgs, pg)
			}
		}
		if len(pgs) == 0 {
			continue
		}
		fmt.Fprintf(out, "  [%s]\n", phase)
		for _, pg := range pgs {
			flag := ""
			if pg.LowConfidence {
				flag = "  (low-confidence — prune if wrong)"
			}
			fmt.Fprintf(out, "    - %s: %s   # %s%s\n", pg.Guard.Name, pg.Guard.Cmd, pg.Source, flag)
		}
	}
}

// promptApprove reads a y/N answer. Default (empty/anything else) is decline so
// a profile is never committed without an explicit yes.
func promptApprove(in io.Reader, out io.Writer) bool {
	fmt.Fprint(out, "Approve and commit this guard profile? [y/N]: ")
	r := bufio.NewReader(in)
	line, _ := r.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// writeGuardProfile marshals p to .wipnote/guard-profile.yaml under repoRoot,
// creating the .wipnote directory if needed.
func writeGuardProfile(repoRoot string, p *guardprofile.Profile) error {
	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	path := filepath.Join(repoRoot, filepath.FromSlash(guardprofile.RelPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// guardCmd is the explicit, re-runnable `wipnote guard init` command. It is the
// drift re-approval path: it re-proposes and re-approves regardless of whether
// an approved profile already exists.
func guardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guard",
		Short: "Manage the project guard profile (.wipnote/guard-profile.yaml)",
	}
	cmd.AddCommand(guardInitCmd())
	return cmd
}

func guardInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Propose, approve, and commit a project guard profile",
		Long: "Inspect project manifests, CI configs, and Makefile targets, propose a " +
			"phase-grouped guard profile, and on approval sign and commit " +
			".wipnote/guard-profile.yaml. Re-runnable: use it to re-approve after drift.",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectRoot, err := resolveProjectRoot()
			if err != nil || projectRoot == "" {
				return fmt.Errorf("could not resolve project root: %w", err)
			}
			_, err = runGuardSetup(projectRoot, defaultGuardInitDeps())
			return err
		},
	}
}
