package approval

import (
	"testing"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
)

func TestResolve(t *testing.T) {
	p := &types.Pending{Kind: types.PendingApproval, Status: types.PendingOpen}
	if err := Resolve(p, types.Resolution{Decision: types.DecisionApprove, Via: "web"}, false); err != nil {
		t.Fatal(err)
	}
	err := Resolve(p, types.Resolution{Decision: types.DecisionReject, Via: "feishu"}, false)
	if !errs.Is(err, errs.CodeAlreadyResolved) {
		t.Fatalf("want already_resolved, got %v", err)
	}
	if p.Resolution.Decision != types.DecisionApprove || p.Resolution.Via != "web" {
		t.Fatal("first resolution must stick")
	}
}

func TestResolveValidation(t *testing.T) {
	q := &types.Pending{Kind: types.PendingQuestion, Status: types.PendingOpen}
	if err := Resolve(q, types.Resolution{Decision: types.DecisionApprove}, false); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("approve on question: %v", err)
	}
	if err := Resolve(q, types.Resolution{Decision: types.DecisionAnswer}, false); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("empty answer: %v", err)
	}
	s := &types.Pending{Kind: types.PendingSubtasks, Status: types.PendingOpen}
	if err := Resolve(s, types.Resolution{Decision: types.DecisionDone}, false); !errs.Is(err, errs.CodeForbidden) {
		t.Fatalf("user resolving subtasks: %v", err)
	}
	if err := Resolve(s, types.Resolution{Decision: types.DecisionDone}, true); err != nil {
		t.Fatal(err)
	}
}

func TestBatchable(t *testing.T) {
	if Batchable(&types.Pending{Kind: types.PendingApproval, Status: types.PendingOpen, Risk: types.RiskExternal}) {
		t.Fatal("external must not be batchable")
	}
	if !Batchable(&types.Pending{Kind: types.PendingApproval, Status: types.PendingOpen, Risk: types.RiskLocalWrite}) {
		t.Fatal("local write should be batchable")
	}
}
