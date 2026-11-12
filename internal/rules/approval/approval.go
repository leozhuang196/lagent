// Package approval is the pending-item state machine. Resolving an item that
// another channel already resolved is a normal outcome, not an error the
// caller should surface as a failure.
package approval

import (
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
)

// allowed lists the decisions each kind accepts.
var allowed = map[types.PendingKind][]types.Decision{
	types.PendingApproval:    {types.DecisionApprove, types.DecisionReject, types.DecisionAnswer, types.DecisionStop},
	types.PendingQuestion:    {types.DecisionAnswer, types.DecisionStop},
	types.PendingBudget:      {types.DecisionContinue, types.DecisionStop},
	types.PendingSubtasks:    {types.DecisionDone, types.DecisionStop},
	types.PendingExternalJob: {types.DecisionDone, types.DecisionStop},
	types.PendingUnknown:     {types.DecisionHappened, types.DecisionNotHappened, types.DecisionStop},
}

// systemOnly kinds are resolved by the platform, not by people.
var systemOnly = map[types.PendingKind]bool{
	types.PendingSubtasks:    true,
	types.PendingExternalJob: true,
}

// Resolve applies r to p in place. It returns CodeAlreadyResolved when p is no
// longer open; p then shows who resolved it and how.
func Resolve(p *types.Pending, r types.Resolution, bySystem bool) error {
	if p.Status != types.PendingOpen {
		return errs.New(errs.CodeAlreadyResolved, "already resolved via %s", via(p))
	}
	ok := false
	for _, d := range allowed[p.Kind] {
		if d == r.Decision {
			ok = true
			break
		}
	}
	if !ok {
		return errs.Invalid("decision %q is not valid for a %s item", r.Decision, p.Kind)
	}
	if systemOnly[p.Kind] && !bySystem && r.Decision != types.DecisionStop {
		return errs.New(errs.CodeForbidden, "%s items are resolved by the platform", p.Kind)
	}
	if p.Kind == types.PendingQuestion && r.Decision == types.DecisionAnswer && r.Answer == "" {
		return errs.Invalid("an answer is required")
	}
	if p.Kind == types.PendingApproval && r.Decision == types.DecisionAnswer && r.Answer == "" {
		return errs.Invalid("new instructions are required when redirecting")
	}
	p.Status = types.PendingResolved
	rc := r
	p.Resolution = &rc
	return nil
}

func via(p *types.Pending) string {
	if p.Resolution == nil {
		return "unknown"
	}
	return p.Resolution.Via
}

// Batchable reports whether p may be approved by "approve all". High-risk items
// must be confirmed one by one to avoid blind approval.
func Batchable(p *types.Pending) bool {
	if p.Kind != types.PendingApproval || p.Status != types.PendingOpen {
		return false
	}
	return p.Risk != types.RiskExternal && p.Risk != types.RiskCommand
}
