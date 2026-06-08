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
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
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
	"github.com/shakestzd/wipnote/plan/planamend"
	"github.com/shakestzd/wipnote/plan/planchat"
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
	var questionsPath string
	cmd := &cobra.Command{
		Use:   "interview <plan-id> <slice-num>",
		Short: "Run the staged plan interview as a local web form (cross-harness)",
		Long: "Serves the slice's staged interview as a local web form, waits for the\n" +
			"user to submit, then writes the answers to the slice's decisions_notes.\n" +
			"Portable across Claude Code, Codex CLI, and Gemini CLI — the agent just\n" +
			"launches it and waits.\n\n" +
			"By default the questions come from a built-in template keyed off slice\n" +
			"complexity. Pass --questions <file|-> to supply an agent-composed staged\n" +
			"question set (JSON: {\"stages\":[...]}) — this is how the plan skill asks\n" +
			"plan-specific and adaptive follow-up questions across interview rounds.",
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
			return runPlanInterview(wipnoteDir, args[0], sliceNum, bind, port, questionsPath)
		},
	}
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "Bind address")
	cmd.Flags().IntVar(&port, "port", 0, "Port (0 = pick a free port)")
	cmd.Flags().StringVar(&questionsPath, "questions", "", "Path to an agent-composed question set (JSON); '-' reads stdin. Defaults to the built-in template.")
	return cmd
}

func runPlanInterview(wipnoteDir, planID string, sliceNum int, bind string, port int, questionsPath string) error {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	_, slice, err := findPlanSlice(plan, sliceNum)
	if err != nil {
		return err
	}

	// Questions come from an agent-supplied set when --questions is given (the
	// skill composes plan-specific / adaptive rounds), else the built-in
	// complexity template.
	stages, err := loadInterviewStages(questionsPath, slice.Complexity)
	if err != nil {
		return err
	}
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
			// planRouter serves the shared chat history (/feedback) and amendments;
			// the interview-aware chat endpoint injects the current slice, stages,
			// questions, and the user's in-progress selections into the context so
			// the assistant answers about the form, not just the plan.
			mux.Handle("/api/plans/", planRouter(db, db, wipnoteDir))
			mux.Handle("/api/interview/chat", interviewChatHandler(db, wipnoteDir, planID, sliceNum, slice, stages))
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

// loadInterviewStages returns the staged questions for an interview round.
// With questionsPath set ("-" = stdin), it parses an agent-composed question
// set; otherwise it falls back to the built-in complexity template.
func loadInterviewStages(questionsPath, complexity string) ([]interview.Stage, error) {
	if questionsPath == "" {
		return interview.ForComplexity(complexity), nil
	}
	var data []byte
	var err error
	if questionsPath == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(questionsPath)
	}
	if err != nil {
		return nil, fmt.Errorf("read questions: %w", err)
	}
	return interview.ParseDefinition(data)
}

// interviewChatReq is the POST body for the interview-aware chat endpoint. It
// carries the user's message plus their in-progress answers and current stage
// so the assistant has the form's live state, not just the plan.
type interviewChatReq struct {
	Message string            `json:"message"`
	Answers map[string]string `json:"answers"`
	Stage   string            `json:"stage"`
}

// interviewChatHandler streams a Claude reply (SSE) like the dashboard plan
// chat, but seeds the context with the interview itself: the slice, every
// stage/question/option, and what the user has selected so far. This is what
// lets the user ask "what does this option mean?" or "recommend a choice" and
// get an answer grounded in the form. AMEND directives are honored so plan
// changes can be requested without leaving the interview.
func interviewChatHandler(db *sql.DB, wipnoteDir, planID string, sliceNum int, slice planyaml.PlanSlice, stages []interview.Stage) http.HandlerFunc {
	projectDir := filepath.Dir(wipnoteDir)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req interviewChatReq
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if json.Unmarshal(body, &req) != nil || strings.TrimSpace(req.Message) == "" {
			http.Error(w, "message is required", http.StatusBadRequest)
			return
		}

		ctxText := interviewChatContext(planID, sliceNum, slice, stages, req.Answers, req.Stage) +
			"\n\n--- FULL PLAN YAML ---\n" + loadPlanContext(wipnoteDir, planID)
		backend := planchat.New(db, planID, ctxText, projectDir)
		if !backend.IsAvailable() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Claude unavailable. Install claude CLI or set ANTHROPIC_API_KEY."})
			return
		}
		_ = backend.SaveMessage("user", req.Message)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		chunks, errCh := backend.Send(r.Context(), req.Message)
		var full strings.Builder
		for chunk := range chunks {
			full.WriteString(chunk)
			payload, _ := json.Marshal(map[string]string{"type": "chunk", "text": chunk})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
		if err := <-errCh; err != nil {
			payload, _ := json.Marshal(map[string]string{"type": "error", "error": err.Error()})
			fmt.Fprintf(w, "data: %s\n\n", payload)
		} else if full.Len() > 0 {
			_ = backend.SaveMessage("assistant", full.String())
			for _, a := range planamend.ParseAmendments(full.String()) {
				value, _ := json.Marshal(a)
				if serr := dbpkg.StorePlanFeedback(db, planID, fmt.Sprintf("slice-%d", a.SliceNum), "amendment", string(value), ""); serr != nil {
					log.Printf("warning: store amendment for plan %s slice %d: %v", planID, a.SliceNum, serr)
				}
			}
		}
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"done"}`)
		flusher.Flush()
	}
}

// interviewChatContext renders the interview's live state as plain-text context
// for the assistant: the slice, every stage with its questions and options, a
// marker for the stage the user is on, and the answers chosen so far.
func interviewChatContext(planID string, sliceNum int, slice planyaml.PlanSlice, stages []interview.Stage, answers map[string]string, currentStage string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The user is filling out wipnote's staged PLANNING INTERVIEW for slice %d (%q) of plan %s.\n", sliceNum, slice.Title, planID)
	if w := strings.TrimSpace(slice.What); w != "" {
		b.WriteString("Slice intent: " + w + "\n")
	}
	b.WriteString("\nThe interview collects Scope/Decisions/Context that become the slice's decisions_notes. Stages and questions:\n")
	for i, st := range stages {
		here := ""
		if st.Key == currentStage {
			here = "   <-- the user is on this stage"
		}
		fmt.Fprintf(&b, "\nStage %d — %s (feeds %s)%s\n", i+1, st.Title, st.Bucket, here)
		for _, q := range st.Questions {
			b.WriteString("  • " + q.Prompt + "\n")
			for _, o := range q.Options {
				b.WriteString("      - " + o.Label + ": " + o.Description + "\n")
			}
		}
	}
	b.WriteString("\nThe user's selections so far:\n")
	any := false
	for _, st := range stages {
		for _, q := range st.Questions {
			if v := strings.TrimSpace(answers[q.ID]); v != "" {
				fmt.Fprintf(&b, "  • %s: %s\n", q.Header, v)
				any = true
			}
		}
		if n := strings.TrimSpace(answers["note:"+st.Key]); n != "" {
			fmt.Fprintf(&b, "  • note (%s): %s\n", st.Title, n)
			any = true
		}
	}
	if !any {
		b.WriteString("  (nothing selected yet)\n")
	}
	b.WriteString("\nAnswer the user's question in THIS interview's context — explain what a question or option means, recommend a choice for this slice, or help them phrase a decision. If they ask to change the plan itself, emit AMEND directives as in plan review.\n")
	return b.String()
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
