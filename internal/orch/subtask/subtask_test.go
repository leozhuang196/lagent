package subtask_test

import (
	"strings"
	"testing"

	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/fake"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/orchtest"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/orch/subtask"
)

func TestDispatchSuspendsParentUntilChildrenEnd(t *testing.T) {
	var d *subtask.Dispatcher
	e := orchtest.New(t, func(e *orchtest.Env) {
		d = &subtask.Dispatcher{Sessions: e.Sessions, Store: e.Store, Queue: e.Queue, MaxParallel: 3}
		e.Registry.Register(d.Tool())
	})
	// The engine is built before options run, so hooks are attached here.
	e.Engine.Hooks = append(e.Engine.Hooks, d)
	e.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		last := req.Messages[len(req.Messages)-1].PlainText()
		// Children: report a result.
		if strings.Contains(last, "submit_result") {
			for _, tl := range req.Tools {
				if tl.Name == subtask.ToolName {
					t.Error("children must not see dispatch_subtask")
				}
			}
			if res, ok := fake.LastToolResult(req); ok && res.Text == "result recorded" {
				return fake.TextReply("child done"), nil
			}
			topic := strings.SplitN(last, "\n", 2)[0]
			return fake.ToolCall("r1", "submit_result", map[string]string{"summary": "found " + topic}), nil
		}
		// Parent.
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("combined: " + res.Text), nil
		}
		return fake.ToolCall("d1", subtask.ToolName, map[string]any{"tasks": []map[string]string{{"instruction": "alpha"}, {"instruction": "beta"}}}), nil
	})
	s, err := e.Sessions.Create(e.Ctx, session.CreateInput{PermissionMode: types.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Sessions.Submit(e.Ctx, s.ID, "research", "")
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	tr := e.Turn(r.Turn.ID)
	if tr.Status != types.TurnCompleted {
		t.Fatalf("parent %s %+v", tr.Status, tr.Error)
	}
	if !strings.Contains(tr.FinalText, "found alpha") || !strings.Contains(tr.FinalText, "found beta") {
		t.Fatalf("parent did not get child results: %q", tr.FinalText)
	}
	children, _ := repo.All(e.Admin, e.Store.Sessions, repo.Query{Filter: repo.Filter{"origin": string(types.OriginSubtask)}}, 0)
	if len(children) != 2 || children[0].ParentTurnID != tr.ID {
		t.Fatalf("children: %d", len(children))
	}
}

func TestDispatchRejectsTooMany(t *testing.T) {
	var d *subtask.Dispatcher
	e := orchtest.New(t, func(e *orchtest.Env) {
		d = &subtask.Dispatcher{Sessions: e.Sessions, Store: e.Store, Queue: e.Queue, MaxParallel: 1}
		e.Registry.Register(d.Tool())
	})
	e.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("got: " + res.Text), nil
		}
		return fake.ToolCall("d1", subtask.ToolName, map[string]any{"tasks": []map[string]string{{"instruction": "a"}, {"instruction": "b"}}}), nil
	})
	s, _ := e.Sessions.Create(e.Ctx, session.CreateInput{PermissionMode: types.ModeAuto})
	r, _ := e.Sessions.Submit(e.Ctx, s.ID, "go", "")
	e.Wait()
	if tr := e.Turn(r.Turn.ID); !strings.Contains(tr.FinalText, "at most 1") {
		t.Fatalf("%q", tr.FinalText)
	}
}
