package flowdef

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eternityx/lagent/internal/core/types"
)

func def() *types.WorkflowDef {
	return &types.WorkflowDef{
		Versioned: types.Versioned{Key: "multi-search", Ver: 1},
		Params: []types.ParamDef{
			{Name: "query", Type: types.ParamString, Required: true},
			{Name: "sources", Type: types.ParamList, Default: json.RawMessage(`["kb","wiki"]`)},
			{Name: "period", Type: types.ParamPeriod, Default: json.RawMessage(`"last_week"`)},
		},
		Steps: []types.StepDef{
			{ID: "search", FanOut: "sources", Instruction: "Search {{item}} for {{params.query}} in {{params.period.start}}..{{params.period.end}}", OnFailure: types.FailContinue},
			{ID: "summarise", DependsOn: []string{"search"}, Instruction: "Summarise: {{steps.search.summary}} (as of {{frozen.date}})"},
		},
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(def()); err != nil {
		t.Fatal(err)
	}
	bad := []func(d *types.WorkflowDef){
		func(d *types.WorkflowDef) { d.Steps[1].DependsOn = []string{"nope"} },
		func(d *types.WorkflowDef) { d.Steps[0].DependsOn = []string{"summarise"} }, // cycle
		func(d *types.WorkflowDef) { d.Steps[1].Instruction = "today is {{now}}" },
		func(d *types.WorkflowDef) { d.Steps[1].Instruction = "{{steps.other.summary}}" },
		func(d *types.WorkflowDef) { d.Steps[0].FanOut = "query" },
		func(d *types.WorkflowDef) { d.Steps[1].Instruction = "{{item}}" },
		func(d *types.WorkflowDef) { d.Steps[1].ID = "search" },
	}
	for i, mut := range bad {
		d := def()
		mut(d)
		if err := Validate(d); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestFreezeExpandAndKeys(t *testing.T) {
	d := def()
	// Monday 2026-10-19 00:30 Shanghai = Sunday 16:30 UTC: the period must use local dates.
	now := time.Date(2026, 10, 18, 16, 30, 0, 0, time.UTC)
	fr, err := Freeze(d, map[string]json.RawMessage{"query": json.RawMessage(`"churn"`)}, now, "Asia/Shanghai", 7)
	if err != nil {
		t.Fatal(err)
	}
	var p Period
	_ = json.Unmarshal(fr.Params["period"], &p)
	if p.Start != "2026-10-12" || p.End != "2026-10-18" {
		t.Fatalf("last_week resolved to %+v", p)
	}
	inst, err := Expand(d, fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 3 {
		t.Fatalf("want 2 fan-out + 1, got %d", len(inst))
	}
	if inst[0].Input != "Search kb for churn in 2026-10-12..2026-10-18" {
		t.Fatalf("input: %q", inst[0].Input)
	}
	if !strings.Contains(inst[2].Input, "{{steps.search.summary}}") || !strings.Contains(inst[2].Input, "2026-10-19") {
		t.Fatalf("summarise input: %q", inst[2].Input)
	}
	// Same frozen values ⇒ same keys; refreezing a day later changes them.
	k1 := StepKey("multi-search", 1, "search", inst[0].Item, inst[0].Input, "r", 1)
	inst2, _ := Expand(d, fr)
	if k1 != StepKey("multi-search", 1, "search", inst2[0].Item, inst2[0].Input, "r", 1) {
		t.Fatal("keys not stable")
	}
	fr3, _ := Freeze(d, map[string]json.RawMessage{"query": json.RawMessage(`"churn"`)}, now.AddDate(0, 0, 7), "Asia/Shanghai", 7)
	inst3, _ := Expand(d, fr3)
	if k1 == StepKey("multi-search", 1, "search", inst3[0].Item, inst3[0].Input, "r", 1) {
		t.Fatal("refreeze should change keys")
	}
	if _, err := Freeze(d, nil, now, "", 0); err == nil {
		t.Fatal("missing required param accepted")
	}
	if _, err := Freeze(d, map[string]json.RawMessage{"query": json.RawMessage(`"x"`), "bogus": json.RawMessage(`1`)}, now, "", 0); err == nil {
		t.Fatal("unknown param accepted")
	}
}

func TestRenderUpstream(t *testing.T) {
	out, err := RenderUpstream("S: {{steps.a.summary}}", map[string][]*types.StepResult{"a": {{Summary: "one"}, nil, {Summary: "two"}}})
	if err != nil || out != "S: one\n---\ntwo" {
		t.Fatalf("%q %v", out, err)
	}
}

func sr(id, step string, st types.StepStatus, pol types.FailurePolicy, deps ...string) *types.StepRun {
	s := &types.StepRun{StepID: step, Status: st, OnFailure: pol, DependsOn: deps}
	s.ID = id
	return s
}

func TestAdvance(t *testing.T) {
	// fan-out with continue: one failed instance does not block the dependent
	steps := []*types.StepRun{
		sr("1", "search", types.StepSucceeded, types.FailContinue),
		sr("2", "search", types.StepFailed, types.FailContinue),
		sr("3", "sum", types.StepPending, "", "search"),
	}
	p := Advance(steps, 0)
	if len(p.Dispatch) != 1 || p.Dispatch[0] != "3" {
		t.Fatalf("%+v", p)
	}
	// still running instance blocks
	steps[1].Status = types.StepRunning
	if p := Advance(steps, 0); len(p.Dispatch) != 0 || p.Done {
		t.Fatalf("%+v", p)
	}
	// skip_dependents
	steps[1].Status, steps[1].OnFailure = types.StepFailed, types.FailSkipDependents
	if p := Advance(steps, 0); len(p.Skip) != 1 {
		t.Fatalf("%+v", p)
	}
	// abort cancels pending
	steps[1].OnFailure = types.FailAbort
	if p := Advance(steps, 0); !p.Abort || len(p.Cancel) != 1 || !p.Done {
		t.Fatalf("%+v", p)
	}
	// max parallel
	par := []*types.StepRun{sr("a", "x", types.StepRunning, ""), sr("b", "y", types.StepPending, ""), sr("c", "z", types.StepPending, "")}
	if p := Advance(par, 2); len(p.Dispatch) != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestFinalStatus(t *testing.T) {
	ok := sr("1", "a", types.StepSucceeded, "")
	ok.Result = &types.StepResult{Summary: "x"}
	failedSink := sr("2", "b", types.StepFailed, types.FailContinue)
	if s := FinalStatus([]*types.StepRun{ok, failedSink}); s != types.RunPartial {
		t.Fatal(s)
	}
	dep := sr("3", "c", types.StepFailed, "", "a")
	if s := FinalStatus([]*types.StepRun{ok, dep}); s != types.RunFailed {
		t.Fatal(s) // the only ok step is not a sink
	}
	if s := FinalStatus([]*types.StepRun{ok}); s != types.RunSucceeded {
		t.Fatal(s)
	}
}
