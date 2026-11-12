package turn_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/coord"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/fake"
	"github.com/eternityx/lagent/internal/orch/orchtest"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/orch/turn"
)

// partitionedLease delegates to a real lease until cut, then every renewal
// fails — an instance that lost Redis but keeps running.
type partitionedLease struct {
	coord.Lease
	cut atomic.Bool
}

func (p *partitionedLease) Renew(ctx context.Context, key string, tok int64, ttl time.Duration) (bool, error) {
	if p.cut.Load() {
		return false, nil
	}
	return p.Lease.Renew(ctx, key, tok, ttl)
}

// slowRead is a read-only tool that blocks until released.
type slowRead struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (s *slowRead) Spec() types.ToolSpec {
	return types.ToolSpec{Name: "slow_read", Description: "slow", InputSchema: tool.Schema(map[string]any{})}
}
func (s *slowRead) Meta() types.ToolMeta {
	return types.ToolMeta{Name: "slow_read", Source: types.SourceBuiltin, Risk: types.RiskReadOnly, ReadOnly: true}
}
func (s *slowRead) Call(ctx context.Context, _ json.RawMessage, _ tool.Env) (tool.Result, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}
	return tool.Result{Content: "data"}, nil
}

func TestLeaseTakeoverNoDoubleWrites(t *testing.T) {
	sr := &slowRead{started: make(chan struct{}), release: make(chan struct{})}
	e := orchtest.New(t, func(e *orchtest.Env) { e.Registry.Register(sr) })
	e.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("final " + res.Text), nil
		}
		return fake.ToolCall("u1", "slow_read", map[string]any{}), nil
	})
	s, _ := e.Sessions.Create(e.Ctx, session.CreateInput{PermissionMode: types.ModeAuto})
	r, err := e.Sessions.Submit(e.Ctx, s.ID, "go", "")
	if err != nil {
		t.Fatal(err)
	}
	e.Queue.Wait() // execute by hand with two engines

	cfg := turn.Config{DefaultModel: "fake/test", LeaseTTL: 300 * time.Millisecond, RenewEvery: 100 * time.Millisecond}
	leaseA := &partitionedLease{Lease: e.Coord}
	depsA := e.Engine.Deps
	depsA.Lease = leaseA
	engineA := turn.New(depsA, cfg)
	engineB := turn.New(e.Engine.Deps, cfg)

	doneA := make(chan error, 1)
	go func() { doneA <- engineA.Execute(e.Ctx, r.Turn.ID) }()
	<-sr.started
	leaseA.cut.Store(true) // A is partitioned from coordination
	errA := <-doneA       // A notices on its next renewal and abandons
	if errA == nil {
		t.Fatal("A should report the lost lease so the job is retried")
	}
	time.Sleep(350 * time.Millisecond) // A's lease expires
	close(sr.release)
	if err := engineB.Execute(e.Ctx, r.Turn.ID); err != nil {
		t.Fatal(err)
	}
	tr := e.Turn(r.Turn.ID)
	if tr.Status != types.TurnCompleted || tr.FinalText != "final data" {
		t.Fatalf("%s %q %+v", tr.Status, tr.FinalText, tr.Error)
	}
	msgs := e.Messages(s.ID)
	if len(msgs) != 4 { // user, tool_use, tool_result, final
		for _, m := range msgs {
			t.Logf("seq=%d role=%s epoch=%d", m.Seq, m.Role, m.Epoch)
		}
		t.Fatalf("expected 4 messages without duplicates, got %d", len(msgs))
	}
	if tr.Epoch < 2 {
		t.Fatalf("B should have taken over with a newer epoch, got %d", tr.Epoch)
	}
}
