// Package flowdef validates, freezes and expands declarative workflow
// definitions, computes step fingerprints and plans scheduling. Pure functions.
//
// Run start order is fixed (§5.2): collect params → freeze non-deterministic
// values → expand fan-out → fix the graph. Expansion reads params that may be
// resolved from relative periods, so it must follow freezing.
package flowdef

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
)

// Limits on definitions.
const (
	MaxSteps     = 50
	MaxFanOut    = 200
	MaxInputSize = 32 << 10
)

// Validate checks structure statically: unique ids, existing and acyclic
// dependencies, fan-out over list params, and placeholders that only refer to
// params, the fan-out item, frozen values or direct dependencies. A reference
// to anything else (e.g. the current time) is rejected here rather than
// producing unstable fingerprints at run time.
func Validate(def *types.WorkflowDef) error {
	if strings.TrimSpace(def.Key) == "" {
		return errs.Invalid("key is required")
	}
	if len(def.Steps) == 0 {
		return errs.Invalid("at least one step is required")
	}
	if len(def.Steps) > MaxSteps {
		return errs.Invalid("at most %d steps", MaxSteps)
	}
	params := map[string]types.ParamDef{}
	for _, p := range def.Params {
		if p.Name == "" {
			return errs.Invalid("param name is required")
		}
		if _, dup := params[p.Name]; dup {
			return errs.Invalid("duplicate param %q", p.Name)
		}
		switch p.Type {
		case types.ParamString, types.ParamNumber, types.ParamList, types.ParamPeriod:
		default:
			return errs.Invalid("param %q has unknown type %q", p.Name, p.Type)
		}
		params[p.Name] = p
	}
	steps := map[string]types.StepDef{}
	for i, s := range def.Steps {
		if s.ID == "" {
			return errs.Invalid("steps[%d].id is required", i)
		}
		if _, dup := steps[s.ID]; dup {
			return errs.Invalid("duplicate step id %q", s.ID)
		}
		if strings.TrimSpace(s.Instruction) == "" {
			return errs.Invalid("step %q: instruction is required", s.ID)
		}
		switch s.OnFailure {
		case "", types.FailAbort, types.FailSkipDependents, types.FailContinue:
		default:
			return errs.Invalid("step %q: unknown on_failure %q", s.ID, s.OnFailure)
		}
		if s.Retries < 0 || s.Retries > 10 {
			return errs.Invalid("step %q: retries must be 0..10", s.ID)
		}
		steps[s.ID] = s
	}
	for _, s := range def.Steps {
		deps := map[string]bool{}
		for _, d := range s.DependsOn {
			if _, ok := steps[d]; !ok {
				return errs.Invalid("step %q depends on unknown step %q", s.ID, d)
			}
			if d == s.ID {
				return errs.Invalid("step %q depends on itself", s.ID)
			}
			deps[d] = true
		}
		if s.FanOut != "" {
			p, ok := params[s.FanOut]
			if !ok || p.Type != types.ParamList {
				return errs.Invalid("step %q: fan_out must name a list param", s.ID)
			}
		}
		for _, r := range Refs(s.Instruction) {
			switch r.Space {
			case "params":
				if len(r.Path) == 0 {
					return errs.Invalid("step %q: %s needs a param name", s.ID, r.Raw)
				}
				if _, ok := params[r.Path[0]]; !ok {
					return errs.Invalid("step %q: unknown param in %s", s.ID, r.Raw)
				}
			case "item":
				if s.FanOut == "" {
					return errs.Invalid("step %q: %s used without fan_out", s.ID, r.Raw)
				}
			case "frozen":
				if len(r.Path) != 1 || (r.Path[0] != "date" && r.Path[0] != "now" && r.Path[0] != "seed") {
					return errs.Invalid("step %q: %s; use frozen.date, frozen.now or frozen.seed", s.ID, r.Raw)
				}
			case "steps":
				if len(r.Path) < 2 || (r.Path[1] != "summary" && r.Path[1] != "data") {
					return errs.Invalid("step %q: %s; use steps.<id>.summary or steps.<id>.data", s.ID, r.Raw)
				}
				if !deps[r.Path[0]] {
					return errs.Invalid("step %q: %s refers to a step it does not depend on", s.ID, r.Raw)
				}
			default:
				return errs.Invalid("step %q: %s is not deterministic or not known; freeze values as params", s.ID, r.Raw)
			}
		}
	}
	if cyc := findCycle(def.Steps); cyc != "" {
		return errs.Invalid("dependency cycle through step %q", cyc)
	}
	return nil
}

func findCycle(steps []types.StepDef) string {
	deps := map[string][]string{}
	for _, s := range steps {
		deps[s.ID] = s.DependsOn
	}
	state := map[string]int{} // 0 new, 1 visiting, 2 done
	var visit func(string) string
	visit = func(n string) string {
		switch state[n] {
		case 1:
			return n
		case 2:
			return ""
		}
		state[n] = 1
		for _, d := range deps[n] {
			if c := visit(d); c != "" {
				return c
			}
		}
		state[n] = 2
		return ""
	}
	for _, s := range steps {
		if c := visit(s.ID); c != "" {
			return c
		}
	}
	return ""
}

// Freeze resolves parameters once: defaults, required checks, and relative
// periods evaluated against now in tz. Everything a step may read later comes
// from the returned value.
func Freeze(def *types.WorkflowDef, params map[string]json.RawMessage, now time.Time, tz string, seed int64) (types.Frozen, error) {
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return types.Frozen{}, errs.Invalid("unknown time zone %q", tz)
	}
	fr := types.Frozen{Now: now.UTC(), TZ: tz, Seed: seed, Params: map[string]json.RawMessage{}}
	known := map[string]bool{}
	for _, p := range def.Params {
		known[p.Name] = true
		v, ok := params[p.Name]
		if !ok || len(v) == 0 || string(v) == "null" {
			v = p.Default
		}
		if len(v) == 0 {
			if p.Required {
				return types.Frozen{}, errs.Invalid("param %q is required", p.Name).With("param", p.Name)
			}
			continue
		}
		switch p.Type {
		case types.ParamString:
			var s string
			if json.Unmarshal(v, &s) != nil {
				return types.Frozen{}, errs.Invalid("param %q must be a string", p.Name)
			}
		case types.ParamNumber:
			var f float64
			if json.Unmarshal(v, &f) != nil {
				return types.Frozen{}, errs.Invalid("param %q must be a number", p.Name)
			}
		case types.ParamList:
			var l []json.RawMessage
			if json.Unmarshal(v, &l) != nil {
				return types.Frozen{}, errs.Invalid("param %q must be a list", p.Name)
			}
			if len(l) > MaxFanOut {
				return types.Frozen{}, errs.Invalid("param %q has %d items; max %d", p.Name, len(l), MaxFanOut)
			}
		case types.ParamPeriod:
			resolved, err := ResolvePeriod(v, now.In(loc))
			if err != nil {
				return types.Frozen{}, errs.Invalid("param %q: %v", p.Name, err)
			}
			v = resolved
		}
		fr.Params[p.Name] = v
	}
	for k := range params {
		if !known[k] {
			return types.Frozen{}, errs.Invalid("unknown param %q", k)
		}
	}
	return fr, nil
}

// Period is a resolved date range (inclusive, local dates).
type Period struct {
	Label string `json:"label"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// ResolvePeriod turns "yesterday", "last_7_days", "last_week", "last_month",
// "this_month" or an explicit {"start","end"} into a Period relative to now.
func ResolvePeriod(v json.RawMessage, now time.Time) (json.RawMessage, error) {
	var explicit Period
	if json.Unmarshal(v, &explicit) == nil && explicit.Start != "" && explicit.End != "" {
		explicit.Label = "custom"
		return json.Marshal(explicit)
	}
	var label string
	if err := json.Unmarshal(v, &label); err != nil {
		return nil, fmt.Errorf("period must be a name or {start,end}")
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	f := func(t time.Time) string { return t.Format("2006-01-02") }
	var p Period
	switch label {
	case "today":
		p = Period{label, f(day), f(day)}
	case "yesterday":
		y := day.AddDate(0, 0, -1)
		p = Period{label, f(y), f(y)}
	case "last_7_days":
		p = Period{label, f(day.AddDate(0, 0, -7)), f(day.AddDate(0, 0, -1))}
	case "last_week": // Monday..Sunday of the previous ISO week
		wd := int(day.Weekday())
		if wd == 0 {
			wd = 7
		}
		monday := day.AddDate(0, 0, -(wd - 1))
		p = Period{label, f(monday.AddDate(0, 0, -7)), f(monday.AddDate(0, 0, -1))}
	case "last_month":
		first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location())
		p = Period{label, f(first.AddDate(0, -1, 0)), f(first.AddDate(0, 0, -1))}
	case "this_month":
		first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location())
		p = Period{label, f(first), f(day)}
	default:
		return nil, fmt.Errorf("unknown period %q", label)
	}
	return json.Marshal(p)
}

// Instance is one step instance of the fixed graph.
type Instance struct {
	StepID    string
	Index     int
	Item      json.RawMessage
	DependsOn []string
	// Input has params, item and frozen values substituted; steps.* refs remain
	// until upstream results are known at dispatch.
	Input string
	Def   types.StepDef
}

// Expand produces every step instance. Fan-out steps yield one instance per
// list element of their frozen param.
func Expand(def *types.WorkflowDef, fr types.Frozen) ([]Instance, error) {
	var out []Instance
	for _, s := range def.Steps {
		items := []json.RawMessage{nil}
		if s.FanOut != "" {
			var l []json.RawMessage
			if raw, ok := fr.Params[s.FanOut]; ok {
				if err := json.Unmarshal(raw, &l); err != nil {
					return nil, errs.Invalid("fan_out param %q is not a list", s.FanOut)
				}
			}
			items = l
		}
		for i, item := range items {
			in, err := renderStatic(s.Instruction, fr, item)
			if err != nil {
				return nil, errs.Invalid("step %q: %v", s.ID, err)
			}
			if len(in) > MaxInputSize {
				return nil, errs.Invalid("step %q: instruction too long after substitution", s.ID)
			}
			out = append(out, Instance{StepID: s.ID, Index: i, Item: item, DependsOn: s.DependsOn, Input: in, Def: s})
		}
	}
	return out, nil
}

func renderStatic(tpl string, fr types.Frozen, item json.RawMessage) (string, error) {
	return render(tpl, func(r Ref) (string, bool, error) {
		switch r.Space {
		case "params":
			raw, ok := fr.Params[r.Path[0]]
			if !ok {
				return "", true, nil // optional param left empty
			}
			v, err := dig(raw, r.Path[1:])
			if err != nil {
				return "", false, err
			}
			return jsonText(v), true, nil
		case "item":
			v, err := dig(item, r.Path)
			if err != nil {
				return "", false, err
			}
			return jsonText(v), true, nil
		case "frozen":
			loc, _ := time.LoadLocation(fr.TZ)
			if loc == nil {
				loc = time.UTC
			}
			switch r.Path[0] {
			case "date":
				return fr.Now.In(loc).Format("2006-01-02"), true, nil
			case "now":
				return fr.Now.In(loc).Format(time.RFC3339), true, nil
			case "seed":
				return fmt.Sprint(fr.Seed), true, nil
			}
		}
		return "", false, nil
	})
}

// RenderUpstream substitutes {{steps.<id>.summary|data}} with the results of
// all ok instances of that step (joined in index order).
func RenderUpstream(input string, upstream map[string][]*types.StepResult) (string, error) {
	return render(input, func(r Ref) (string, bool, error) {
		if r.Space != "steps" {
			return "", false, nil
		}
		results := upstream[r.Path[0]]
		var parts []string
		for _, res := range results {
			if res == nil {
				continue
			}
			if r.Path[1] == "summary" {
				parts = append(parts, res.Summary)
			} else if len(res.Data) > 0 {
				parts = append(parts, string(res.Data))
			}
		}
		if len(parts) == 0 {
			return "(no result)", true, nil
		}
		return strings.Join(parts, "\n---\n"), true, nil
	})
}

// StepKey is the content-addressed fingerprint of what a step actually runs.
// Same key ⇒ same semantics ⇒ the result may be reused (unless side-effecting).
func StepKey(defKey string, defVer int, stepID string, item json.RawMessage, resolvedInput, roleKey string, roleVer int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%d", defKey, defVer, stepID, item, resolvedInput, roleKey, roleVer)
	return hex.EncodeToString(h.Sum(nil))
}
