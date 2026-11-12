package turn_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/fake"
	"github.com/eternityx/lagent/internal/orch/orchtest"
	"github.com/eternityx/lagent/internal/orch/session"
)

func newSession(t *testing.T, e *orchtest.Env, in session.CreateInput) *types.Session {
	t.Helper()
	s, err := e.Sessions.Create(e.Ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func submit(t *testing.T, e *orchtest.Env, sid, text string) *types.Turn {
	t.Helper()
	r, err := e.Sessions.Submit(e.Ctx, sid, text, "")
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	return e.Turn(r.Turn.ID)
}

func TestSimpleTurnCompletes(t *testing.T) {
	e := orchtest.New(t)
	s := newSession(t, e, session.CreateInput{})
	tr := submit(t, e, s.ID, "hello")
	if tr.Status != types.TurnCompleted || tr.FinalText != "echo: hello" {
		t.Fatalf("turn: %s %q %+v", tr.Status, tr.FinalText, tr.Error)
	}
	msgs := e.Messages(s.ID)
	if len(msgs) != 2 || msgs[1].Role != types.RoleAssistant {
		t.Fatalf("messages: %d", len(msgs))
	}
	var types_ []string
	for _, ev := range e.Events(s.ID) {
		types_ = append(types_, string(ev.Type))
	}
	joined := strings.Join(types_, ",")
	for _, want := range []string{"turn.started", "message.delta", "message.completed", "turn.completed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	snap, _ := e.Sessions.Snapshot(e.Ctx, s.ID, 0)
	if snap.Session.Status != types.SessionIdle {
		t.Fatalf("session %s", snap.Session.Status)
	}
	// A second turn sees the history.
	tr2 := submit(t, e, s.ID, "again")
	if tr2.Status != types.TurnCompleted || len(e.Fake.Requests[len(e.Fake.Requests)-1].Messages) != 3 {
		t.Fatalf("second turn: %s", tr2.Status)
	}
}

func writeThenDone() fake.Script {
	return func(req llm.Request) (*llm.Response, error) {
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("done: " + res.Text), nil
		}
		return fake.ToolCall("tu1", "write_file", map[string]string{"path": "out/report.md", "content": "# hi"}), nil
	}
}

func TestApprovalSuspendsAndResumes(t *testing.T) {
	e := orchtest.New(t)
	e.Fake.SetScript(writeThenDone())
	s := newSession(t, e, session.CreateInput{})
	tr := submit(t, e, s.ID, "write a report")
	if tr.Status != types.TurnSuspended {
		t.Fatalf("want suspended, got %s %+v", tr.Status, tr.Error)
	}
	ps := e.OpenPendings(tr.ID)
	if len(ps) != 1 || ps[0].Kind != types.PendingApproval || ps[0].ToolName != "write_file" || ps[0].Risk != types.RiskLocalWrite {
		t.Fatalf("pendings: %+v", ps)
	}
	snap, _ := e.Sessions.Snapshot(e.Ctx, s.ID, 0)
	if snap.Session.Status != types.SessionWaiting {
		t.Fatalf("session should be waiting, is %s", snap.Session.Status)
	}
	view, _ := e.Sessions.GetTurn(e.Ctx, tr.ID)
	if view.State != "waiting_human" {
		t.Fatal(view.State)
	}
	if _, err := e.Sessions.Resolve(e.Ctx, ps[0].ID, types.DecisionApprove, "", "web"); err != nil {
		t.Fatal(err)
	}
	// A second channel resolving later sees the first decision.
	again, err := e.Sessions.Resolve(e.Admin, ps[0].ID, types.DecisionReject, "", "feishu")
	if err != nil || !again.AlreadyResolved || again.Pending.Resolution.Via != "web" {
		t.Fatalf("second resolve: %v %+v", err, again)
	}
	e.Wait()
	tr = e.Turn(tr.ID)
	if tr.Status != types.TurnCompleted || !strings.HasPrefix(tr.FinalText, "done: wrote") {
		t.Fatalf("after approval: %s %q %+v", tr.Status, tr.FinalText, tr.Error)
	}
	ws, _ := e.Sandbox.Open(context.Background(), "t1", s.ID)
	if b, err := os.ReadFile(filepath.Join(ws.Dir, "out/report.md")); err != nil || string(b) != "# hi" {
		t.Fatalf("file: %v %q", err, b)
	}
	msgs := e.Messages(s.ID)
	if len(msgs) != 4 || msgs[2].Content[0].Type != types.BlockToolResult {
		t.Fatalf("transcript: %d", len(msgs))
	}
}

func TestRejectTellsModel(t *testing.T) {
	e := orchtest.New(t)
	e.Fake.SetScript(writeThenDone())
	s := newSession(t, e, session.CreateInput{})
	tr := submit(t, e, s.ID, "write")
	p := e.OpenPendings(tr.ID)[0]
	if _, err := e.Sessions.Resolve(e.Ctx, p.ID, types.DecisionReject, "", "web"); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	tr = e.Turn(tr.ID)
	if tr.Status != types.TurnCompleted || !strings.Contains(tr.FinalText, "rejected") {
		t.Fatalf("%s %q", tr.Status, tr.FinalText)
	}
}

func TestAutoModeSkipsApproval(t *testing.T) {
	e := orchtest.New(t)
	e.Fake.SetScript(writeThenDone())
	s := newSession(t, e, session.CreateInput{PermissionMode: types.ModeAuto})
	tr := submit(t, e, s.ID, "write")
	if tr.Status != types.TurnCompleted {
		t.Fatalf("%s", tr.Status)
	}
}

func TestAskUser(t *testing.T) {
	e := orchtest.New(t)
	e.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("thanks, " + res.Text), nil
		}
		return fake.ToolCall("q1", "ask_user", map[string]any{"question": "Which channel?", "options": []string{"A", "B"}}), nil
	})
	s := newSession(t, e, session.CreateInput{})
	tr := submit(t, e, s.ID, "go")
	ps := e.OpenPendings(tr.ID)
	if len(ps) != 1 || ps[0].Kind != types.PendingQuestion || ps[0].Question != "Which channel?" {
		t.Fatalf("%+v", ps)
	}
	if _, err := e.Sessions.Resolve(e.Ctx, ps[0].ID, types.DecisionAnswer, "B", "web"); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	tr = e.Turn(tr.ID)
	if tr.Status != types.TurnCompleted || !strings.Contains(tr.FinalText, "B") {
		t.Fatalf("%s %q", tr.Status, tr.FinalText)
	}
}

func TestCancelSuspendedTurn(t *testing.T) {
	e := orchtest.New(t)
	e.Fake.SetScript(writeThenDone())
	s := newSession(t, e, session.CreateInput{})
	tr := submit(t, e, s.ID, "write")
	if _, err := e.Sessions.Cancel(e.Ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Sessions.Cancel(e.Ctx, s.ID); err != nil {
		t.Fatal("cancel must be idempotent:", err)
	}
	e.Wait()
	if st := e.Turn(tr.ID).Status; st != types.TurnCancelled {
		t.Fatal(st)
	}
	if len(e.OpenPendings(tr.ID)) != 0 {
		t.Fatal("pendings should be closed")
	}
	// Session is usable again.
	e.Fake.SetScript(fake.Echo)
	if st := submit(t, e, s.ID, "hi").Status; st != types.TurnCompleted {
		t.Fatal(st)
	}
}

func TestBudgetPausesTurn(t *testing.T) {
	e := orchtest.New(t)
	e.SeedRole(types.Role{Name: "Tiny", Tools: []string{"*"}, DefaultPermissionMode: types.ModeAuto, MaxIterations: 2}, "tiny")
	n := 0
	e.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		n++
		return fake.ToolCall("l"+string(rune('a'+n)), "list_files", map[string]string{}), nil
	})
	s := newSession(t, e, session.CreateInput{RoleKey: "tiny"})
	tr := submit(t, e, s.ID, "loop")
	ps := e.OpenPendings(tr.ID)
	if tr.Status != types.TurnSuspended || len(ps) != 1 || ps[0].Kind != types.PendingBudget {
		t.Fatalf("%s %+v", tr.Status, ps)
	}
	if tr.Iterations != 2 {
		t.Fatalf("iterations %d", tr.Iterations)
	}
	e.Fake.SetScript(func(req llm.Request) (*llm.Response, error) { return fake.TextReply("ok"), nil })
	if _, err := e.Sessions.Resolve(e.Ctx, ps[0].ID, types.DecisionContinue, "", "web"); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if tr := e.Turn(tr.ID); tr.Status != types.TurnCompleted {
		t.Fatalf("%s %+v", tr.Status, tr.Error)
	}
}

// blockingSend is a side-effect tool that blocks until its context ends,
// emulating an instance that dies mid-call.
type blockingSend struct{ started chan struct{} }

func (b *blockingSend) Spec() types.ToolSpec {
	return types.ToolSpec{Name: "send_message", Description: "send", InputSchema: tool.Schema(map[string]any{"text": tool.Str("t")})}
}
func (b *blockingSend) Meta() types.ToolMeta {
	return types.ToolMeta{Name: "send_message", Source: types.SourceConnector, Risk: types.RiskExternal, SideEffect: true}
}
func (b *blockingSend) Call(ctx context.Context, _ json.RawMessage, _ tool.Env) (tool.Result, error) {
	close(b.started)
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

func TestCrashDuringSideEffectAsksPerson(t *testing.T) {
	bs := &blockingSend{started: make(chan struct{})}
	e := orchtest.New(t, func(e *orchtest.Env) { e.Registry.Register(bs) })
	e.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("after: " + res.Text), nil
		}
		return fake.ToolCall("s1", "send_message", map[string]string{"text": "hi"}), nil
	})
	s := newSession(t, e, session.CreateInput{PermissionMode: types.ModeAuto})
	// Drive the turn directly so we control the "crash".
	r, err := e.Sessions.Submit(e.Ctx, s.ID, "send it", "")
	if err != nil {
		t.Fatal(err)
	}
	e.Queue.Wait() // stop the background queue; we execute by hand
	_ = r
	ctx, cancel := context.WithCancel(e.Ctx)
	done := make(chan error, 1)
	go func() { done <- e.Engine.Execute(ctx, r.Turn.ID) }()
	<-bs.started
	cancel() // instance dies while the side effect may have happened
	<-done
	// Recovery on another instance.
	if err := e.Engine.Execute(e.Ctx, r.Turn.ID); err != nil {
		t.Fatal(err)
	}
	tr := e.Turn(r.Turn.ID)
	ps := e.OpenPendings(tr.ID)
	if tr.Status != types.TurnSuspended || len(ps) != 1 || ps[0].Kind != types.PendingUnknown {
		t.Fatalf("%s %+v", tr.Status, ps)
	}
	if _, err := e.Sessions.Resolve(e.Ctx, ps[0].ID, types.DecisionHappened, "", "web"); err != nil {
		t.Fatal(err)
	}
	if err := e.Engine.Execute(e.Ctx, r.Turn.ID); err != nil {
		t.Fatal(err)
	}
	tr = e.Turn(r.Turn.ID)
	if tr.Status != types.TurnCompleted || !strings.Contains(tr.FinalText, "Do not repeat") {
		t.Fatalf("%s %q", tr.Status, tr.FinalText)
	}
}

func TestBusySessionInbox(t *testing.T) {
	e := orchtest.New(t)
	e.Fake.SetScript(writeThenDone())
	s := newSession(t, e, session.CreateInput{Origin: types.OriginWeb})
	tr := submit(t, e, s.ID, "write")
	res, err := e.Sessions.Submit(e.Ctx, s.ID, "also add a title", "")
	if err != nil || !res.Queued || res.Turn.ID != tr.ID {
		t.Fatalf("inbox: %v %+v", err, res)
	}
	api := newSession(t, e, session.CreateInput{})
	submit(t, e, api.ID, "write")
	if _, err := e.Sessions.Submit(e.Ctx, api.ID, "more", ""); err == nil {
		t.Fatal("API sessions must get a conflict")
	}
}
