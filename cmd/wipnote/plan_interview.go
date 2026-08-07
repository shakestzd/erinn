// Register in plan_cmds.go: cmd.AddCommand(planInterviewCmd())
//
// `wipnote plan interview <plan-id> <slice-num>` is the prototype of the
// harness-agnostic plan interview renderer (feat-2852d0c8). It serves the
// staged interview (defined by plan/interview) as a local web form, blocks
// until the user submits, then persists the answers to the slice's
// decisions_notes via the same path as `wipnote plan elicit-decisions`.
//
// Why a web form rather than a harness tool: it is the only renderer that
// works identically on Claude Code, Codex CLI, and Antigravity CLI — the agent
// just runs the command, prints the URL, and waits. Native ask-user tools
// (Claude AskUserQuestion) can later render the SAME
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
	"sync"
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
	IsIntake    bool // upfront plan-level intake (no slice) vs per-slice decisions
}

func planInterviewCmd() *cobra.Command {
	var bind string
	var port int
	var questionsPath string
	cmd := &cobra.Command{
		Use:   "interview <plan-id> [slice-num]",
		Short: "Run the plan interview as a local web form (cross-harness)",
		Long: "Two modes, both served as a local web form, portable across Claude Code,\n" +
			"Codex CLI, and Antigravity CLI:\n\n" +
			"  wipnote plan interview <plan-id>            UPFRONT INTAKE (no slice):\n" +
			"     The interview at the beginning of a plan. Leads with triage to assess\n" +
			"     complexity, then gathers problem/goals/constraints from limited info,\n" +
			"     and writes them to the plan's Design. The agent then drafts slices.\n\n" +
			"  wipnote plan interview <plan-id> <slice-num>   PER-SLICE DECISIONS:\n" +
			"     Fills an existing slice's decisions_notes. Questions default to the\n" +
			"     slice's canonical set (complexity template + the slice's open\n" +
			"     questions); pass --questions <file|-> to supply an agent-composed set\n" +
			"     for plan-specific or adaptive follow-up rounds.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				return runPlanIntake(wipnoteDir, args[0], bind, port)
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

// planInterviewQuestionsCmd prints the canonical staged interview question set
// (JSON) for a slice — the same model the web form renders. Any harness can
// fetch this and render it via its native ask-user tool (Claude
// AskUserQuestion), so the questions are defined once.
func planInterviewQuestionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "interview-questions <plan-id> [slice-num]",
		Short: "Print the canonical interview question set (JSON) for a plan or slice",
		Long: "Emits the same staged question model the web form renders ({\"stages\":[…]}),\n" +
			"so ANY harness can render it however it can — inline via a native ask-user\n" +
			"tool (Claude AskUserQuestion), as plain conversational\n" +
			"questions when the harness has no such tool (Codex), or piped to the web\n" +
			"form. One source of truth; the agent picks the lowest-friction renderer.\n\n" +
			"  interview-questions <plan>            upfront intake: triage + problem/\n" +
			"                                         goals/constraints (before slices).\n" +
			"  interview-questions <plan> <slice>    per-slice: complexity template +\n" +
			"                                         the slice's open questions.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			var stages []interview.Stage
			if len(args) == 1 {
				stages = interview.PlanIntakeStages()
			} else {
				sliceNum, err := parseSliceNum(args[1])
				if err != nil {
					return err
				}
				plan, err := planyaml.Load(filepath.Join(wipnoteDir, "plans", args[0]+".yaml"))
				if err != nil {
					return fmt.Errorf("load plan: %w", err)
				}
				_, slice, err := findPlanSlice(plan, sliceNum)
				if err != nil {
					return err
				}
				stages = attachBlockPrompts(interview.BuildForSlice(slice.Complexity, sliceOpenQuestions(slice)))
			}
			out, err := json.MarshalIndent(interview.Definition{Stages: stages}, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal questions: %w", err)
			}
			fmt.Println(string(out))
			return nil
		},
	}
}

// attachBlockPrompts decorates each stage with its blocks-first authoring
// prompts (interview.StageBlockPlan), keeping planyaml.BlockCatalog as the
// source of truth for WHICH block types exist: any prompt naming a type the live
// catalog no longer defines is dropped, and each surviving prompt is annotated
// with the catalog's own one-line description. This is how blocks drive the
// interview across every renderer (web form, native ask-user, plain chat) —
// block elicitation is folded into the stages, not bolted on in a post-pass.
func attachBlockPrompts(stages []interview.Stage) []interview.Stage {
	catalog := map[string]string{}
	for _, spec := range planyaml.BlockCatalog() {
		catalog[spec.Type] = spec.Description
	}
	for i := range stages {
		var prompts []interview.BlockPrompt
		for _, bp := range interview.StageBlockPlan(stages[i].Key) {
			desc, known := catalog[bp.Type]
			if !known {
				continue // catalog dropped this type — never emit a stale tag
			}
			bp.Description = desc
			prompts = append(prompts, bp)
		}
		stages[i].Blocks = prompts
	}
	return stages
}

// runPlanInterview is the per-slice decisions mode: fill an existing slice's
// decisions_notes.
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
	stages, err := loadInterviewStages(questionsPath, slice)
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
	onSubmit := func(answers map[string]string) (string, error) {
		scope, decisions, contextStr := interview.Compose(stages, answers)
		if err := elicitDecisionsForSlice(wipnoteDir, planID, sliceNum, elicitInput{
			scope: scope, decisions: decisions, context: contextStr,
		}); err != nil {
			return "", err
		}
		return fmt.Sprintf("Saved decisions to slice %d of %s. You can close this tab.", sliceNum, planID), nil
	}
	chatContext := func(answers map[string]string, stage string) string {
		return interviewChatContext(planID, sliceNum, slice, stages, answers, stage)
	}
	banner := fmt.Sprintf("Interview ready for slice %d (%q) of %s", sliceNum, slice.Title, planID)
	return serveInterviewForm(wipnoteDir, planID, page, stages, bind, port, banner, onSubmit, chatContext)
}

// runPlanIntake is the upfront mode: the interview at the beginning of a plan.
// It assesses complexity and gathers problem/goals/constraints, writing them to
// the plan's Design so the agent can draft slices.
func runPlanIntake(wipnoteDir, planID, bind string, port int) error {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	stages := interview.PlanIntakeStages()
	title := plan.Meta.Title
	if title == "" {
		title = planID
	}
	page := interviewPage{
		PlanID: planID, IsIntake: true,
		SliceTitle: title, SliceWhat: strings.TrimSpace(plan.Design.Problem),
		Complexity: "intake", Stages: stages,
	}
	onSubmit := func(answers map[string]string) (string, error) {
		res := interview.ComposeIntake(answers)
		if err := writePlanIntake(planPath, res); err != nil {
			return "", err
		}
		cx := res.Complexity
		if cx == "" {
			cx = "(skipped)"
		}
		return fmt.Sprintf("Saved plan design. Assessed complexity: %s. The agent will draft slices from this — you can close this tab.", cx), nil
	}
	chatContext := func(answers map[string]string, _ string) string {
		return intakeChatContext(planID, plan, stages, answers)
	}
	banner := fmt.Sprintf("Plan intake interview for %q (%s)", title, planID)
	return serveInterviewForm(wipnoteDir, planID, page, stages, bind, port, banner, onSubmit, chatContext)
}

// writePlanIntake persists the upfront interview's answers to the plan's Design
// section and git-commits, matching how other plan mutations are versioned.
func writePlanIntake(planPath string, res interview.IntakeResult) error {
	defer planyaml.LockPlanForWrite(planPath)()
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	if res.Problem != "" {
		plan.Design.Problem = res.Problem
	}
	if len(res.Goals) > 0 {
		plan.Design.Goals = res.Goals
	}
	if len(res.Constraints) > 0 {
		plan.Design.Constraints = res.Constraints
	}
	if res.Complexity != "" {
		note := "Assessed complexity: " + res.Complexity
		if strings.TrimSpace(plan.Design.Comment) == "" {
			plan.Design.Comment = note
		} else {
			plan.Design.Comment = plan.Design.Comment + "\n" + note
		}
	}
	if err := planyaml.SaveLocked(planPath, plan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}
	planID := strings.TrimSuffix(filepath.Base(planPath), ".yaml")
	if err := commitPlanChange(planPath, fmt.Sprintf("plan(%s): intake — design from interview", planID)); err != nil {
		return fmt.Errorf("autocommit intake: %w", err)
	}
	return nil
}

// serveInterviewForm runs the one-off blocking web form shared by both modes:
// it mounts the plan-review chat, renders the form, and on submit calls
// onSubmit(answers) and shows the returned message, then shuts down.
func serveInterviewForm(wipnoteDir, planID string, page interviewPage, stages []interview.Stage, bind string, port int, banner string, onSubmit func(map[string]string) (string, error), chatContext func(map[string]string, string) string) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bind, port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	done := make(chan error, 1)
	mux := http.NewServeMux()

	// Mount the same plan API the dashboard uses so the embedded chat panel is
	// the real plan-review chat (Claude-answered, AMEND directives honored).
	// Best-effort: if the DB can't open, the form still works (chat hidden).
	page.ChatEnabled = false
	if dbPath, derr := storage.CanonicalDBPath(filepath.Dir(wipnoteDir)); derr == nil {
		if db, oerr := dbpkg.Open(dbPath); oerr == nil {
			defer db.Close()
			mux.Handle("/api/plans/", planRouter(db, db, wipnoteDir))
			mux.Handle("/api/interview/chat", interviewChatHandler(db, wipnoteDir, planID, chatContext))
			page.ChatEnabled = true
		}
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = interviewTmpl.Execute(w, page)
	})
	// submitOnce guards against double-submit (double-click, resubmit-on-refresh,
	// concurrent POSTs): only the first accepted submission runs onSubmit and
	// signals done; later POSTs are rejected.
	var submitMu sync.Mutex
	var submitted bool
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
		// Reject empty submissions so the interview can't complete without
		// capturing anything.
		if len(answers) == 0 {
			http.Error(w, "answer at least one question before submitting", http.StatusBadRequest)
			return
		}
		submitMu.Lock()
		if submitted {
			submitMu.Unlock()
			http.Error(w, "already submitted", http.StatusConflict)
			return
		}
		submitted = true
		submitMu.Unlock()

		msg, perr := onSubmit(answers)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		result := page
		result.Submitted = true
		if perr != nil {
			result.Message = "Error saving: " + perr.Error()
		} else {
			result.Message = msg
		}
		_ = interviewTmpl.Execute(w, result)
		done <- perr
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	fmt.Println(banner)
	fmt.Printf("Open: http://%s/\n", ln.Addr().String())
	fmt.Println("Waiting for you to submit the form (Ctrl-C to cancel)...")

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigC)

	var runErr error
	select {
	case runErr = <-done:
	case <-sigC:
		fmt.Println("\nCancelled — nothing written.")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return runErr
}

// intakeChatContext builds the chat context for the upfront intake interview:
// the plan-level questions and the user's in-progress answers, so the assistant
// helps articulate problem/goals/constraints and pick a complexity.
func intakeChatContext(planID string, plan *planyaml.PlanYAML, stages []interview.Stage, answers map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The user is doing the UPFRONT intake interview for plan %s — before any slices exist. It assesses complexity and gathers the plan's problem, goals, and constraints from possibly limited information.\n", planID)
	if p := strings.TrimSpace(plan.Design.Problem); p != "" {
		b.WriteString("Current problem statement: " + p + "\n")
	}
	b.WriteString("\nIntake questions:\n")
	for _, st := range stages {
		for _, q := range st.Questions {
			b.WriteString("  • " + q.Prompt + "\n")
		}
	}
	b.WriteString("\nAnswers so far:\n")
	any := false
	for _, st := range stages {
		for _, q := range st.Questions {
			if v := strings.TrimSpace(answers[q.ID]); v != "" {
				fmt.Fprintf(&b, "  • %s: %s\n", q.Header, v)
				any = true
			}
		}
	}
	if !any {
		b.WriteString("  (nothing yet)\n")
	}
	b.WriteString("\nHelp the user sharpen the problem, goals, and constraints, and pick a complexity. If asked, propose candidate slices.\n")
	return b.String()
}

// loadInterviewStages returns the staged questions for an interview round.
// With questionsPath set ("-" = stdin), it parses an agent-composed question
// set; otherwise it builds the canonical per-slice set (complexity template +
// the slice's unanswered open questions).
func loadInterviewStages(questionsPath string, slice planyaml.PlanSlice) ([]interview.Stage, error) {
	if questionsPath == "" {
		return attachBlockPrompts(interview.BuildForSlice(slice.Complexity, sliceOpenQuestions(slice))), nil
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

// sliceOpenQuestions maps a slice's unanswered slice-local questions into
// interview questions so the interview asks the plan's real open questions.
// Choice when the question carries options, free text otherwise.
func sliceOpenQuestions(slice planyaml.PlanSlice) []interview.Question {
	var out []interview.Question
	for _, q := range slice.Questions {
		if strings.TrimSpace(q.Answer) != "" {
			continue // already answered
		}
		iq := interview.Question{
			ID:     "slicequestion." + q.ID,
			Header: q.ID,
			Prompt: q.Text,
		}
		if strings.TrimSpace(q.Description) != "" {
			iq.Prompt = strings.TrimSpace(q.Text + " — " + q.Description)
		}
		if len(q.Options) > 0 {
			iq.Type = interview.Choice
			for _, o := range q.Options {
				label := o.Label
				if label == "" {
					label = o.Key
				}
				iq.Options = append(iq.Options, interview.Option{Label: label})
			}
		} else {
			iq.Type = interview.Text
			iq.Placeholder = "your answer"
		}
		out = append(out, iq)
	}
	return out
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
func interviewChatHandler(db *sql.DB, wipnoteDir, planID string, buildContext func(answers map[string]string, stage string) string) http.HandlerFunc {
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

		ctxText := buildContext(req.Answers, req.Stage) +
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
		for _, bp := range st.Blocks {
			b.WriteString("  ▸ AUTHOR FIRST — " + bp.Type + " block: " + bp.Prompt + "\n")
		}
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
