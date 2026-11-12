package permission

import (
	"testing"

	"github.com/eternityx/lagent/internal/core/types"
)

func TestDecide(t *testing.T) {
	grant := []types.StandingGrant{{Tool: "feishu_reply", Field: "thread_id", Value: "th1"}}
	cases := []struct {
		name string
		in   Input
		want Decision
	}{
		{"readonly always", Input{Risk: types.RiskReadOnly, Mode: types.ModeAskAll}, Allow},
		{"ask_all local write", Input{Risk: types.RiskLocalWrite, Mode: types.ModeAskAll}, Ask},
		{"ask_external local write", Input{Risk: types.RiskLocalWrite, Mode: types.ModeAskExternal}, Allow},
		{"ask_external external", Input{Risk: types.RiskExternal, Mode: types.ModeAskExternal}, Ask},
		{"auto external", Input{Risk: types.RiskExternal, Mode: types.ModeAuto}, Allow},
		{"auto command still asks", Input{Risk: types.RiskCommand, Mode: types.ModeAuto}, Ask},
		{"unknown risk treated external", Input{Risk: "weird", Mode: types.ModeAskExternal}, Ask},
		{"unknown mode asks", Input{Risk: types.RiskLocalWrite, Mode: ""}, Ask},
		{"grant matches", Input{Tool: "feishu_reply", Risk: types.RiskExternal, Mode: types.ModeAskAll, Grants: grant, Args: []byte(`{"thread_id":"th1","text":"x"}`)}, Allow},
		{"grant other target", Input{Tool: "feishu_reply", Risk: types.RiskExternal, Mode: types.ModeAskAll, Grants: grant, Args: []byte(`{"thread_id":"th2"}`)}, Ask},
		{"grant other tool", Input{Tool: "feishu_send", Risk: types.RiskExternal, Mode: types.ModeAskAll, Grants: grant, Args: []byte(`{"thread_id":"th1"}`)}, Ask},
		{"grant never for commands", Input{Tool: "run_command", Risk: types.RiskCommand, Mode: types.ModeAskAll,
			Grants: []types.StandingGrant{{Tool: "run_command", Field: "command", Value: "ls"}}, Args: []byte(`{"command":"ls"}`)}, Ask},
	}
	for _, c := range cases {
		if got, why := Decide(c.in); got != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, got, why, c.want)
		}
	}
}

func TestEffectiveRiskOnlyRaises(t *testing.T) {
	if r := EffectiveRisk(types.RiskExternal, types.RiskReadOnly); r != types.RiskExternal {
		t.Fatalf("override lowered risk: %s", r)
	}
	if r := EffectiveRisk(types.RiskReadOnly, types.RiskLocalWrite, ""); r != types.RiskLocalWrite {
		t.Fatalf("override not applied: %s", r)
	}
	if r := EffectiveRisk(""); r != types.RiskExternal {
		t.Fatalf("missing risk must be external: %s", r)
	}
}

func TestClampMode(t *testing.T) {
	if m := ClampMode(types.ModeAuto, types.ModeAskExternal); m != types.ModeAskExternal {
		t.Fatal(m)
	}
	if m := ClampMode(types.ModeAskAll, types.ModeAuto); m != types.ModeAskAll {
		t.Fatal(m)
	}
	if m := ClampMode("", ""); m != types.ModeAskAll {
		t.Fatal(m)
	}
}
