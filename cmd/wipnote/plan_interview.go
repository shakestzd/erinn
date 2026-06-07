// Register in plan_cmds.go: cmd.AddCommand(planInterviewCmd())
//
// `wipnote plan interview <plan-id> <slice-num>` is the prototype of the
// harness-agnostic plan interview renderer (feat-2852d0c8). It serves the
// staged interview (defined by plan/interview) as a local web form, blocks
// until the user submits, then persists the answers to the slice's
// decisions_notes via the same path as `wipnote plan elicit-decisions`.
//
// Why a web form rather than a harness tool: it is the only renderer that
// works identically on Claude Code, Codex CLI, and Gemini CLI — the agent
// just runs the command, prints the URL, and waits. Native ask-user tools
// (Claude AskUserQuestion, Gemini ask_user) can later render the SAME
// interview model as an optional inline fast-path; Codex has no such tool, so
// the web form is its only structured path.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/shakestzd/wipnote/plan/interview"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

//go:embed templates/interview.gohtml
var interviewFormHTML string

var interviewTmpl = template.Must(template.New("interview").
	Funcs(template.FuncMap{"add": func(a, b int) int { return a + b }}).
	Parse(interviewFormHTML))

// interviewPage is the template/render model for the form.
type interviewPage struct {
	PlanID      string
	SliceNum    int
	SliceTitle  string
	SliceWhat   string
	Complexity  string
	Stages      []interview.Stage
	Submitted   bool
	Message     string
	ChatEnabled bool
}

func planInterviewCmd() *cobra.Command {
	var bind string
	var port int
	cmd := &cobra.Command{
		Use:   "interview <plan-id> <slice-num>",
		Short: "Run the staged plan interview as a local web form (cross-harness)",
		Long: "Serves the slice's staged interview as a local web form, waits for the\n" +
			"user to submit, then writes the answers to the slice's decisions_notes.\n" +
			"Portable across Claude Code, Codex CLI, and Gemini CLI — the agent just\n" +
			"launches it and waits.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			sliceNum, err := parseSliceNum(args[1])
			if err != nil {
				return err
			}
			return runPlanInterview(wipnoteDir, args[0], sliceNum, bind, port)
		},
	}
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "Bind address")
	cmd.Flags().IntVar(&port, "port", 0, "Port (0 = pick a free port)")
	return cmd
}

func runPlanInterview(wipnoteDir, planID string, sliceNum int, bind string, port int) error {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	_, slice, err := findPlanSlice(plan, sliceNum)
	if err != nil {
		return err
	}

	stages := interview.ForComplexity(slice.Complexity)
	if len(stages) == 0 {
		fmt.Printf("Slice %d of %s is trivial — no interview needed.\n", sliceNum, planID)
		return nil
	}

	page := interviewPage{
		PlanID: planID, SliceNum: sliceNum,
		SliceTitle: slice.Title, SliceWhat: slice.What,
		Complexity: slice.Complexity, Stages: stages,
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bind, port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	done := make(chan error, 1)
	mux := http.NewServeMux()

	// Mount the same plan API the dashboard uses so the embedded chat panel
	// is the real plan-review chat (Claude-answered, AMEND directives honored)
	// — the user can ask questions or request plan changes without leaving the
	// interview. Best-effort: if the DB can't open, the form still works and
	// the chat panel reports itself unavailable.
	page.ChatEnabled = false
	if dbPath, derr := storage.CanonicalDBPath(filepath.Dir(wipnoteDir)); derr == nil {
		if db, oerr := dbpkg.Open(dbPath); oerr == nil {
			defer db.Close()
			mux.Handle("/api/plans/", planRouter(db, db, wipnoteDir))
			page.ChatEnabled = true
		}
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = interviewTmpl.Execute(w, page)
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		answers := collectAnswers(r.Form, stages)
		scope, decisions, contextStr := interview.Compose(stages, answers)
		perr := elicitDecisionsForSlice(wipnoteDir, planID, sliceNum, elicitInput{
			scope: scope, decisions: decisions, context: contextStr,
		})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		result := page
		result.Submitted = true
		if perr != nil {
			result.Message = "Error saving: " + perr.Error()
		} else {
			result.Message = fmt.Sprintf("Saved decisions to slice %d of %s. You can close this tab.", sliceNum, planID)
		}
		_ = interviewTmpl.Execute(w, result)
		done <- perr
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	formURL := fmt.Sprintf("http://%s/", ln.Addr().String())
	fmt.Printf("Interview ready for slice %d (%q) of %s\n", sliceNum, slice.Title, planID)
	fmt.Printf("Open: %s\n", formURL)
	fmt.Println("Waiting for you to submit the form (Ctrl-C to cancel)...")

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigC)

	var runErr error
	select {
	case runErr = <-done:
	case <-sigC:
		fmt.Println("\nCancelled — no decisions written.")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return runErr
}

// collectAnswers flattens posted form values into the answers map Compose
// expects: question IDs (multi-select joined with "; ") plus per-stage
// free-text notes keyed "note:<stageKey>".
func collectAnswers(form url.Values, stages []interview.Stage) map[string]string {
	out := map[string]string{}
	for _, st := range stages {
		for _, q := range st.Questions {
			var vals []string
			for _, v := range form[q.ID] {
				if v = strings.TrimSpace(v); v != "" {
					vals = append(vals, v)
				}
			}
			if len(vals) > 0 {
				out[q.ID] = strings.Join(vals, "; ")
			}
		}
		if n := strings.TrimSpace(form.Get("note:" + st.Key)); n != "" {
			out["note:"+st.Key] = n
		}
	}
	return out
}
