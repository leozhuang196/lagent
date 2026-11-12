package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/fake"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/orchtest"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/orch/subtask"
	"github.com/eternityx/lagent/internal/orch/workflow"
)

// setup wires the workflow service into an orchtest environment: the turn
// hook and the advance handler, with a short retry backoff.
func setup(t *testing.T, cfg workflow.Config, opts ...orchtest.Option) (*orchtest.Env, *workflow.Service) {
	t.Helper()
	var wf *workflow.Service
	if cfg.RetryBase == 0 {
		cfg.RetryBase = 30 * time.Millisecond
	}
	opts = append(opts, func(e *orchtest.Env) {
		wf = (&workflow.Service{Store: e.Store, Queue: e.Queue, Bus: e.Bus, Sessions: e.Sessions, Cfg: cfg}).Init()
		e.Engine.Hooks = append(e.Engine.Hooks, wf)
		e.Queue.Handle(queue.TypeWorkflowAdvance, wf.HandleAdvance)
	})
	e := orchtest.New(t, opts...)
	return e, wf
}

// seedDef publishes a definition (version ver) in the built-in layer.
func seedDef(t *testing.T, e *orchtest.Env, d types.WorkflowDef, ver int) {
	t.Helper()
	d.Ver, d.Status = ver, types.AssetPublished
	d.TenantID = tenant.System
	if err := e.Store.WorkflowDefs.Insert(repo.SystemLayer(context.Background()), &d); err != nil {
		t.Fatal(err)
	}
}

// stepText is the instruction a step turn received (its first user message).
func stepText(req llm.Request) string {
	if len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[0].PlainText()
}

// script replies "R(<instruction>)" unless fail says the step should fail.
type script struct {
	mu    sync.Mutex
	fail  func(text string, n int) bool
	calls map[string]int
}

func (s *script) run(req llm.Request) (*llm.Response, error) {
	text := stepText(req)
	s.mu.Lock()
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[text]++
	n := s.calls[text]
	fail := s.fail != nil && s.fail(text, n)
	s.mu.Unlock()
	if fail {
		return nil, &llm.Error{Kind: llm.ErrInvalid, Err: errors.New("scripted failure")}
	}
	return fake.TextReply("R(" + strings.SplitN(text, "\n", 2)[0] + ")"), nil
}

func (s *script) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		n += c
	}
	return n
}

func fanoutDef() types.WorkflowDef {
	return types.WorkflowDef{
		Versioned: types.Versioned{Key: "fan"}, Name: "Fan",
		Params: []types.ParamDef{{Name: "items", Type: types.ParamList, Required: true}},
		Steps: []types.StepDef{
			{ID: "search", FanOut: "items", OnFailure: types.FailContinue, Instruction: "Search {{item}}"},
			{ID: "sum", DependsOn: []string{"search"}, Instruction: "Summarise:\n{{steps.search.summary}}"},
		},
	}
}

func items(vs ...string) map[string]json.RawMessage {
	raw, _ := json.Marshal(vs)
	return map[string]json.RawMessage{"items": raw}
}

func view(t *testing.T, e *orchtest.Env, wf *workflow.Service, runID string) *workflow.RunView {
	t.Helper()
	v, err := wf.Get(e.Ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func byStep(v *workflow.RunView) map[string][]*types.StepRun {
	out := map[string][]*types.StepRun{}
	for _, s := range v.Steps {
		out[s.StepID] = append(out[s.StepID], s)
	}
	return out
}

func eventTypes(e *orchtest.Env, stream string) map[types.EventType]int {
	out := map[types.EventType]int{}
	for _, ev := range e.Events(stream) {
		out[ev.Type]++
	}
	return out
}

func TestFanOutContinuePolicyAndPartial(t *testing.T) {
	e, wf := setup(t, workflow.Config{MaxParallel: 2})
	seedDef(t, e, fanoutDef(), 1)
	sc := &script{fail: func(text string, _ int) bool { return text == "Search b" }}
	e.Fake.SetScript(sc.run)
	run, err := wf.Start(e.Ctx, workflow.StartInput{DefKey: "fan", Params: items("a", "b", "c"), PermissionMode: types.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	v := view(t, e, wf, run.ID)
	if v.Run.Status != types.RunPartial {
		t.Fatalf("run status %s (%+v)", v.Run.Status, v.Run.Error)
	}
	steps := byStep(v)
	if len(steps["search"]) != 3 || len(steps["sum"]) != 1 {
		t.Fatalf("graph: %d search, %d sum", len(steps["search"]), len(steps["sum"]))
	}
	for _, s := range steps["search"] {
		want := types.StepSucceeded
		if string(s.Item) == `"b"` {
			want = types.StepFailed
			if s.Error == nil || !strings.Contains(s.Error.Message, "scripted failure") {
				t.Fatalf("failure reason not recorded: %+v", s.Error)
			}
		}
		if s.Status != want {
			t.Fatalf("search %s: %s", s.Item, s.Status)
		}
		if s.SessionID == "" || s.TurnID == "" {
			t.Fatal("step session/turn not recorded")
		}
	}
	sum := steps["sum"][0]
	if sum.Status != types.StepSucceeded || !strings.Contains(sum.Input, "R(Search a)") || !strings.Contains(sum.Input, "R(Search c)") || strings.Contains(sum.Input, "{{") {
		t.Fatalf("sum: %s %q", sum.Status, sum.Input)
	}
	sess, _ := e.Store.Sessions.Get(e.Ctx, sum.SessionID)
	if sess.Origin != types.OriginWorkflow || sess.RunID != run.ID || sess.StepRunID != sum.ID {
		t.Fatalf("step session: %+v", sess)
	}
	if v.Run.Usage.Total() == 0 || v.Run.EndedAt == nil {
		t.Fatalf("usage not aggregated: %+v", v.Run.Usage)
	}
	ev := eventTypes(e, run.ID)
	if ev[types.EvRunCreated] != 1 || ev[types.EvRunCompleted] != 1 || ev[types.EvStepStarted] < 4 || ev[types.EvStepCompleted] != 4 {
		t.Fatalf("events: %v", ev)
	}
}

func TestContinueReusesSucceededSteps(t *testing.T) {
	e, wf := setup(t, workflow.Config{})
	seedDef(t, e, fanoutDef(), 1)
	sc := &script{fail: func(text string, _ int) bool { return text == "Search b" }}
	e.Fake.SetScript(sc.run)
	first, err := wf.Start(e.Ctx, workflow.StartInput{DefKey: "fan", Params: items("a", "b", "c"), PermissionMode: types.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()

	// The preview predicts what Continue will reuse.
	dry, err := wf.DryRun(e.Ctx, workflow.DryRunInput{OriginRunID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	reuse := map[string]bool{}
	for _, in := range dry.Instances {
		reuse[in.StepID+string(in.Item)] = in.WouldReuse
	}
	if !reuse[`search"a"`] || reuse[`search"b"`] || !reuse[`search"c"`] || reuse["sum"] {
		t.Fatalf("dry-run reuse: %v", reuse)
	}
	if n, _ := e.Store.Runs.Count(e.Ctx, repo.Filter{}); n != 1 {
		t.Fatal("dry run wrote a run")
	}

	sc.mu.Lock()
	sc.fail, sc.calls = nil, nil
	sc.mu.Unlock()
	cont, err := wf.Continue(e.Ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	v := view(t, e, wf, cont.ID)
	if v.Run.Status != types.RunSucceeded || v.Run.Mode != types.ModeContinue || v.Run.OriginRunID != first.ID {
		t.Fatalf("continued run: %s %s", v.Run.Status, v.Run.Mode)
	}
	if !v.Run.Frozen.Now.Equal(first.Frozen.Now) || v.Run.Frozen.Seed != first.Frozen.Seed {
		t.Fatal("continue must reuse frozen values")
	}
	for _, s := range v.Steps {
		switch {
		case s.StepID == "search" && string(s.Item) != `"b"`:
			if s.Status != types.StepReused || s.ReusedFrom == "" || s.Result == nil || s.Attempts != 0 {
				t.Fatalf("search %s should be reused: %s", s.Item, s.Status)
			}
		default:
			if s.Status != types.StepSucceeded {
				t.Fatalf("%s %s should re-run: %s", s.StepID, s.Item, s.Status)
			}
		}
	}
	if sc.total() != 2 { // search b + sum
		t.Fatalf("model calls in continued run: %d (%v)", sc.total(), sc.calls)
	}
	if ev := eventTypes(e, cont.ID); ev[types.EvStepReused] != 2 {
		t.Fatalf("events: %v", ev)
	}
	// The original run is never rewritten.
	if o := view(t, e, wf, first.ID); o.Run.Status != types.RunPartial {
		t.Fatalf("origin changed: %s", o.Run.Status)
	}

	// Rerun freezes again: nothing is reused.
	rr, err := wf.Rerun(e.Ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	for _, s := range view(t, e, wf, rr.ID).Steps {
		if s.Status != types.StepSucceeded {
			t.Fatalf("rerun step %s: %s", s.StepID, s.Status)
		}
	}
}

func TestSideEffectStepNeverReused(t *testing.T) {
	e, wf := setup(t, workflow.Config{})
	seedDef(t, e, types.WorkflowDef{
		Versioned: types.Versioned{Key: "notify"},
		Steps: []types.StepDef{
			{ID: "draft", Instruction: "Draft the note"},
			{ID: "send", DependsOn: []string{"draft"}, SideEffect: true, Retries: 3, Instruction: "Send {{steps.draft.summary}}"},
		},
	}, 1)
	sc := &script{}
	e.Fake.SetScript(sc.run)
	first, err := wf.Start(e.Ctx, workflow.StartInput{DefKey: "notify", PermissionMode: types.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if st := view(t, e, wf, first.ID).Run.Status; st != types.RunSucceeded {
		t.Fatal(st)
	}
	cont, err := wf.Continue(e.Ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	steps := byStep(view(t, e, wf, cont.ID))
	if steps["draft"][0].Status != types.StepReused {
		t.Fatalf("draft: %s", steps["draft"][0].Status)
	}
	send := steps["send"][0]
	if send.Status != types.StepSucceeded || send.ReusedFrom != "" || send.MaxRetries != 0 {
		t.Fatalf("side-effect step: %s reused_from=%q retries=%d", send.Status, send.ReusedFrom, send.MaxRetries)
	}
}

func TestAbortCancelsPendingSteps(t *testing.T) {
	e, wf := setup(t, workflow.Config{})
	seedDef(t, e, types.WorkflowDef{
		Versioned: types.Versioned{Key: "abort"}, MaxParallel: 1,
		Steps: []types.StepDef{
			{ID: "a", Instruction: "Step A"}, // default policy: abort
			{ID: "b", Instruction: "Step B"},
			{ID: "c", DependsOn: []string{"a"}, Instruction: "Step C"},
		},
	}, 1)
	e.Fake.SetScript((&script{fail: func(text string, _ int) bool { return text == "Step A" }}).run)
	run, err := wf.Start(e.Ctx, workflow.StartInput{DefKey: "abort", PermissionMode: types.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	v := view(t, e, wf, run.ID)
	steps := byStep(v)
	if steps["a"][0].Status != types.StepFailed || steps["b"][0].Status != types.StepCancelled || steps["c"][0].Status != types.StepCancelled {
		t.Fatalf("a=%s b=%s c=%s", steps["a"][0].Status, steps["b"][0].Status, steps["c"][0].Status)
	}
	if !strings.Contains(steps["b"][0].Error.Message, "step a failed") {
		t.Fatalf("reason: %q", steps["b"][0].Error.Message)
	}
	if v.Run.Status != types.RunFailed || v.Run.Error == nil {
		t.Fatalf("run: %s %+v", v.Run.Status, v.Run.Error)
	}
}

func TestRetryWithBackoffKeepsOneStepRun(t *testing.T) {
	e, wf := setup(t, workflow.Config{})
	seedDef(t, e, types.WorkflowDef{
		Versioned: types.Versioned{Key: "flaky"},
		Steps:     []types.StepDef{{ID: "x", Retries: 2, Instruction: "Flaky"}},
	}, 1)
	e.Fake.SetScript((&script{fail: func(_ string, n int) bool { return n <= 2 }}).run)
	run, err := wf.Start(e.Ctx, workflow.StartInput{DefKey: "flaky", PermissionMode: types.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	v := view(t, e, wf, run.ID)
	if len(v.Steps) != 1 {
		t.Fatalf("%d step runs", len(v.Steps))
	}
	s := v.Steps[0]
	if s.Status != types.StepSucceeded || s.Attempts != 3 || s.Error != nil || v.Run.Status != types.RunSucceeded {
		t.Fatalf("step %s attempts=%d err=%+v run=%s", s.Status, s.Attempts, s.Error, v.Run.Status)
	}
	turns, _ := repo.All(e.Ctx, e.Store.Turns, repo.Query{Filter: repo.Filter{"step_run_id": s.ID}}, 0)
	if len(turns) != 3 {
		t.Fatalf("turns: %d", len(turns))
	}
	var pending int
	for _, ev := range e.Events(run.ID) {
		if ev.Type == types.EvStepCompleted && ev.Data["status"] == types.StepPending {
			pending++
		}
	}
	if pending != 2 {
		t.Fatalf("retry notices: %d", pending)
	}
}

func TestPreCheckFailureCreatesNoRecords(t *testing.T) {
	e, wf := setup(t, workflow.Config{})
	seedDef(t, e, fanoutDef(), 1)
	seedDef(t, e, types.WorkflowDef{Versioned: types.Versioned{Key: "norole"}, Steps: []types.StepDef{{ID: "a", Role: "ghost", Instruction: "x"}}}, 1)
	seedDef(t, e, types.WorkflowDef{Versioned: types.Versioned{Key: "nosrc"}, Steps: []types.StepDef{{ID: "a", Sources: []string{"kb"}, Instruction: "x"}}}, 1)
	cases := []workflow.StartInput{
		{DefKey: "fan"},                         // required param missing
		{DefKey: "missing"},                     // no such definition
		{DefKey: "norole"},                      // step role does not exist
		{DefKey: "nosrc"},                       // retrieval source does not exist
		{DefKey: "fan", Params: items("a"), Draft: true}, // draft without debug
	}
	for i, in := range cases {
		if _, err := wf.Start(e.Ctx, in); err == nil {
			t.Fatalf("case %d: expected rejection", i)
		}
	}
	nr, _ := e.Store.Runs.Count(e.Ctx, repo.Filter{})
	ns, _ := e.Store.StepRuns.Count(e.Ctx, repo.Filter{})
	if nr != 0 || ns != 0 {
		t.Fatalf("records written: %d runs, %d steps", nr, ns)
	}
}

func writeThenDone(req llm.Request) (*llm.Response, error) {
	if res, ok := fake.LastToolResult(req); ok {
		return fake.TextReply("done: " + res.Text), nil
	}
	return fake.ToolCall("tu1", "write_file", map[string]string{"path": "out.md", "content": "x"}), nil
}

func TestCancelIsIdempotent(t *testing.T) {
	e, wf := setup(t, workflow.Config{})
	seedDef(t, e, types.WorkflowDef{
		Versioned: types.Versioned{Key: "slow"},
		Steps:     []types.StepDef{{ID: "w", Instruction: "Write it"}, {ID: "next", DependsOn: []string{"w"}, Instruction: "Then"}},
	}, 1)
	e.Fake.SetScript(writeThenDone)
	// ask_all: the step suspends waiting for approval, i.e. it stays running.
	run, err := wf.Start(e.Ctx, workflow.StartInput{DefKey: "slow", PermissionMode: types.ModeAskAll})
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	steps := byStep(view(t, e, wf, run.ID))
	if st := steps["w"][0].Status; st != types.StepRunning {
		t.Fatalf("step should be running while waiting for approval: %s", st)
	}
	c1, err := wf.Cancel(e.Ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := wf.Cancel(e.Ctx, run.ID)
	if err != nil {
		t.Fatal("second cancel:", err)
	}
	e.Wait()
	if c1.Status != types.RunCancelled || c2.Status != types.RunCancelled || !c1.EndedAt.Equal(*c2.EndedAt) {
		t.Fatalf("cancel: %s %s", c1.Status, c2.Status)
	}
	v := view(t, e, wf, run.ID)
	for _, s := range v.Steps {
		if s.Status != types.StepCancelled {
			t.Fatalf("%s: %s", s.StepID, s.Status)
		}
	}
	sess, _ := e.Store.Sessions.Get(e.Ctx, steps["w"][0].SessionID)
	if tr := e.Turn(steps["w"][0].TurnID); tr.Status != types.TurnCancelled || sess.ActiveTurnID != "" {
		t.Fatalf("step turn %s, session active %q", tr.Status, sess.ActiveTurnID)
	}
	if n := eventTypes(e, run.ID)[types.EvRunCompleted]; n != 1 {
		t.Fatalf("run.completed emitted %d times", n)
	}
	// Someone else's run is not theirs to cancel.
	other := tenant.With(context.Background(), tenant.Principal{Kind: tenant.KindAPIKey, ID: "u2", TenantID: "t1", Role: tenant.RoleMember})
	if _, err := wf.Cancel(other, run.ID); err == nil {
		t.Fatal("foreign cancel accepted")
	}
}

func TestSweeperFailsTimedOutStep(t *testing.T) {
	e, wf := setup(t, workflow.Config{StepTimeout: 50 * time.Millisecond})
	seedDef(t, e, types.WorkflowDef{Versioned: types.Versioned{Key: "stuck"}, Steps: []types.StepDef{{ID: "w", Instruction: "Write it"}}}, 1)
	e.Fake.SetScript(writeThenDone)
	run, err := wf.Start(e.Ctx, workflow.StartInput{DefKey: "stuck", PermissionMode: types.ModeAskAll})
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	time.Sleep(80 * time.Millisecond)
	if err := wf.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	v := view(t, e, wf, run.ID)
	s := v.Steps[0]
	if s.Status != types.StepFailed || s.Error == nil || s.Error.Code != "timeout" || v.Run.Status != types.RunFailed {
		t.Fatalf("step %s %+v run %s", s.Status, s.Error, v.Run.Status)
	}
	if tr := e.Turn(s.TurnID); tr.Status != types.TurnCancelled {
		t.Fatalf("step turn should be cancelled: %s", tr.Status)
	}
}

func TestPromoteProducesValidDraft(t *testing.T) {
	var wf *workflow.Service
	e, wf := setup(t, workflow.Config{}, func(e *orchtest.Env) {
		d := &subtask.Dispatcher{Sessions: e.Sessions, Store: e.Store, Queue: e.Queue}
		e.Registry.Register(d.Tool())
		e.Engine.Hooks = append(e.Engine.Hooks, d)
	})
	e.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		first := stepText(req)
		switch {
		case strings.Contains(first, "When done"):
			return fake.TextReply("sub: " + strings.SplitN(first, "\n", 2)[0]), nil
		case strings.HasPrefix(first, "Research"):
			if _, ok := fake.LastToolResult(req); ok && len(req.Messages) <= 3 {
				return fake.TextReply("combined"), nil
			}
			if len(req.Messages) == 1 {
				return fake.ToolCall("d1", subtask.ToolName, map[string]any{"tasks": []map[string]string{
					{"instruction": "Look up sales in 「华东」"}, {"instruction": "Look up sales in 「华南」"},
				}}), nil
			}
		}
		return fake.TextReply("ok"), nil
	})
	sess, err := e.Sessions.Create(e.Ctx, session.CreateInput{Title: "销售分析", PermissionMode: types.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"Research regional sales", "Write a report for 2026-10-01 with {{braces}}"} {
		if _, err := e.Sessions.Submit(e.Ctx, sess.ID, msg, ""); err != nil {
			t.Fatal(err)
		}
		e.Wait()
	}
	def, err := wf.Promote(e.Ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if def.Status != types.AssetDraft || def.Ver != 0 || len(def.Steps) != 4 {
		t.Fatalf("def: %s v%d %d steps", def.Status, def.Ver, len(def.Steps))
	}
	if !strings.Contains(def.Steps[0].Instruction, "华东") || !strings.Contains(def.Steps[2].Instruction, "{{steps.step1.summary}}") {
		t.Fatalf("steps: %+v", def.Steps)
	}
	if len(def.Steps[3].DependsOn) != 1 || def.Steps[3].DependsOn[0] != "step3" || strings.Contains(def.Steps[3].Instruction, "{{braces}}") {
		t.Fatalf("serial chain / escaping: %+v", def.Steps[3])
	}
	if !strings.Contains(def.Description, "未参数化的具体值") || !strings.Contains(def.Description, "2026-10-01") || !strings.Contains(def.Description, "「华东」") {
		t.Fatalf("description: %s", def.Description)
	}
	stored, err := repo.Version(e.Ctx, e.Store.WorkflowDefs, def.Key, 0)
	if err != nil || stored.Status != types.AssetDraft {
		t.Fatalf("stored draft: %v", err)
	}
	// Promoting again does not clobber the first draft.
	def2, err := wf.Promote(e.Ctx, sess.ID)
	if err != nil || def2.Key == def.Key {
		t.Fatalf("second promote: %v %s", err, def2.Key)
	}
	// The draft runs in debug mode.
	run, err := wf.Start(e.Ctx, workflow.StartInput{DefKey: def.Key, Draft: true, Debug: true})
	if err != nil || run.DefVer != 0 {
		t.Fatalf("debug run of draft: %v", err)
	}
	if _, err := wf.Cancel(e.Ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	e.Wait()
}
