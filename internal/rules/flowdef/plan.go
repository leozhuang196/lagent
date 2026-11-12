package flowdef

import (
	"sort"

	"github.com/eternityx/lagent/internal/core/types"
)

// Plan is what the scheduler should do next for a run.
type Plan struct {
	Dispatch []string // step run ids to dispatch now
	Skip     []string // step run ids to mark skipped
	Cancel   []string // step run ids to cancel (run aborted)
	Abort    bool     // a step failed with the abort policy
	Done     bool     // every instance is terminal
}

// Advance computes the next scheduling actions. Dependents wait for every
// instance of their dependencies to be terminal (step-level wait); a failed
// instance's own policy decides what happens downstream (instance-level).
func Advance(steps []*types.StepRun, maxParallel int) Plan {
	byStep := map[string][]*types.StepRun{}
	for _, s := range steps {
		byStep[s.StepID] = append(byStep[s.StepID], s)
	}
	var p Plan
	active := 0
	for _, s := range steps {
		if s.Status == types.StepDispatched || s.Status == types.StepRunning {
			active++
		}
		if s.Status == types.StepFailed && policy(s) == types.FailAbort {
			p.Abort = true
		}
	}
	ordered := append([]*types.StepRun(nil), steps...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, s := range ordered {
		if s.Status != types.StepPending {
			continue
		}
		if p.Abort {
			p.Cancel = append(p.Cancel, s.ID)
			continue
		}
		ready, skip := depsState(s, byStep)
		if skip {
			p.Skip = append(p.Skip, s.ID)
			continue
		}
		if !ready {
			continue
		}
		if maxParallel > 0 && active >= maxParallel {
			continue
		}
		p.Dispatch = append(p.Dispatch, s.ID)
		active++
	}
	p.Done = active == 0 && len(p.Dispatch) == 0
	if p.Done {
		for _, s := range steps {
			if !s.Status.Terminal() && !contains(p.Skip, s.ID) && !contains(p.Cancel, s.ID) {
				p.Done = false
				break
			}
		}
	}
	return p
}

func policy(s *types.StepRun) types.FailurePolicy {
	if s.OnFailure == "" {
		return types.FailAbort
	}
	return s.OnFailure
}

// depsState: ready when every dependency instance is terminal; skip when any
// dependency instance was skipped or failed with skip_dependents.
func depsState(s *types.StepRun, byStep map[string][]*types.StepRun) (ready, skip bool) {
	ready = true
	for _, d := range s.DependsOn {
		for _, dep := range byStep[d] {
			switch {
			case dep.Status == types.StepSkipped || dep.Status == types.StepCancelled:
				skip = true
			case dep.Status == types.StepFailed && policy(dep) == types.FailSkipDependents:
				skip = true
			case !dep.Status.Terminal():
				ready = false
			}
		}
	}
	if skip {
		return false, true
	}
	return ready, false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// FinalStatus judges a finished run: succeeded when nothing failed; partial
// when at least one sink step (nothing depends on it) produced a result;
// otherwise failed (§13.3).
func FinalStatus(steps []*types.StepRun) types.RunStatus {
	failed := false
	dependedOn := map[string]bool{}
	for _, s := range steps {
		for _, d := range s.DependsOn {
			dependedOn[d] = true
		}
		if s.Status == types.StepFailed || s.Status == types.StepCancelled || s.Status == types.StepSkipped {
			failed = true
		}
	}
	if !failed {
		return types.RunSucceeded
	}
	for _, s := range steps {
		if !dependedOn[s.StepID] && s.Status.Ok() && s.Result != nil {
			return types.RunPartial
		}
	}
	return types.RunFailed
}
