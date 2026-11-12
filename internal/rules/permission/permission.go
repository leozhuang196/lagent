// Package permission decides whether a tool call may run without a human.
// Pure functions: every input is a value, no storage or network.
package permission

import (
	"encoding/json"
	"fmt"

	"github.com/eternityx/lagent/internal/core/types"
)

// Decision is the outcome of a check.
type Decision string

const (
	Allow Decision = "allow"
	Ask   Decision = "ask"
)

// Input is everything the decision depends on.
type Input struct {
	Tool   string
	Risk   types.RiskLevel // effective risk (see EffectiveRisk)
	Mode   types.PermissionMode
	Grants []types.StandingGrant
	Args   json.RawMessage
}

// Decide returns Allow or Ask with a human-readable reason.
//
//   - read-only calls always run;
//   - a standing grant allows an exact tool → target pair (never commands);
//   - ask_all asks for everything else;
//   - ask_external asks for command and external calls;
//   - auto runs everything except commands, which always ask (§9.1).
func Decide(in Input) (Decision, string) {
	risk := in.Risk
	if !risk.Valid() {
		risk = types.RiskExternal
	}
	if risk == types.RiskReadOnly {
		return Allow, "read-only"
	}
	if risk != types.RiskCommand {
		if g, ok := MatchGrant(in.Tool, in.Args, in.Grants); ok {
			return Allow, fmt.Sprintf("standing grant %s.%s=%s", g.Tool, g.Field, g.Value)
		}
	}
	switch in.Mode {
	case types.ModeAuto:
		if risk == types.RiskCommand {
			return Ask, "commands always need approval"
		}
		return Allow, "auto mode"
	case types.ModeAskExternal:
		if risk == types.RiskLocalWrite {
			return Allow, "local write allowed in ask_external mode"
		}
		return Ask, fmt.Sprintf("%s call needs approval", risk)
	default:
		return Ask, fmt.Sprintf("%s call needs approval", risk)
	}
}

// MatchGrant finds a grant whose tool matches and whose field equals the argument.
func MatchGrant(tool string, args json.RawMessage, grants []types.StandingGrant) (types.StandingGrant, bool) {
	if len(grants) == 0 {
		return types.StandingGrant{}, false
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return types.StandingGrant{}, false
	}
	for _, g := range grants {
		if g.Tool != tool || g.Field == "" {
			continue
		}
		v, ok := m[g.Field]
		if !ok {
			continue
		}
		if s, ok := v.(string); ok && s == g.Value {
			return g, true
		}
	}
	return types.StandingGrant{}, false
}

// EffectiveRisk combines the base risk with overrides; overrides only raise it.
// Tool output can never lower the risk of later calls (§8.2), so callers never
// pass anything derived from model or tool content here.
func EffectiveRisk(base types.RiskLevel, overrides ...types.RiskLevel) types.RiskLevel {
	r := base
	if !r.Valid() {
		r = types.RiskExternal
	}
	for _, o := range overrides {
		if o.Valid() && o.Rank() > r.Rank() {
			r = o
		}
	}
	return r
}

// ClampMode returns the stricter of the requested mode and a caller's cap.
func ClampMode(requested, cap types.PermissionMode) types.PermissionMode {
	if !requested.Valid() {
		requested = types.ModeAskAll
	}
	if !cap.Valid() {
		return requested
	}
	return types.Stricter(requested, cap)
}
