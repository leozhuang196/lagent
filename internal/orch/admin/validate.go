package admin

import (
	"context"
	"regexp"
	"strings"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/rules/assembly"
	"github.com/eternityx/lagent/internal/rules/flowdef"
)

// Limits on editable assets. They keep one asset from crowding the context
// window: skill headers share the injection budget with memory (§9.3).
const (
	MaxPromptChars       = 100_000
	MaxSkillNameChars    = 64
	MaxSkillDescChars    = 1024
	MaxSkillBodyChars    = 256 << 10
	MaxRoleIterations    = 500
	MaxRoleTokenBudget   = 50_000_000
	MaxNameChars         = 200
	MaxMemoryKeyChars    = 200
	MaxMemoryContentChar = 8000
)

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

func validKey(key string) error {
	if !keyRe.MatchString(key) {
		return errs.Invalid("key %q must be 1-64 lowercase letters, digits, '-', '_' or '.'", key)
	}
	return nil
}

// validateRole enforces that roles only narrow (§9.3): the tool picker takes
// registered tool names, "mcp:<server>" of a known server, or "*"; risk levels
// are not part of a role at all, so they cannot be edited here.
func (s *Service) validateRole(ctx context.Context, r *types.Role) error {
	if strings.TrimSpace(r.Name) == "" || len(r.Name) > MaxNameChars {
		return errs.Invalid("name is required (max %d characters)", MaxNameChars)
	}
	if len(r.Prompt) > MaxPromptChars {
		return errs.Invalid("prompt is too long (max %d characters)", MaxPromptChars)
	}
	if r.DefaultPermissionMode != "" && !r.DefaultPermissionMode.Valid() {
		return errs.Invalid("unknown default_permission_mode %q", r.DefaultPermissionMode)
	}
	if r.MaxIterations < 0 || r.MaxIterations > MaxRoleIterations {
		return errs.Invalid("max_iterations must be 0..%d (0 = platform default)", MaxRoleIterations)
	}
	if r.TokenBudget < 0 || r.TokenBudget > MaxRoleTokenBudget {
		return errs.Invalid("token_budget must be 0..%d (0 = platform default)", MaxRoleTokenBudget)
	}
	for _, k := range r.Skills {
		if k != "*" && validKey(k) != nil {
			return errs.Invalid("skill %q is not a valid key", k)
		}
	}
	return s.validateTools(ctx, r.Tools)
}

func (s *Service) validateTools(ctx context.Context, declared []string) error {
	if s.Registry == nil {
		return nil
	}
	reg, err := s.Registry.List(ctx)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	servers := map[string]bool{}
	for _, m := range reg {
		names[m.Name] = true
		if m.Server != "" {
			servers[m.Server] = true
		}
	}
	var bad []string
	for _, d := range declared {
		switch {
		case d == "*":
		case strings.HasPrefix(d, "mcp:"):
			srv := strings.TrimPrefix(d, "mcp:")
			if servers[srv] {
				continue
			}
			// A configured server whose tools have not synced yet is still a
			// legitimate pick; an unknown server is a typo.
			if _, err := s.Store.MCPServers.FindOne(ctx, repo.Filter{"name": srv}); err != nil {
				bad = append(bad, d)
			}
		default:
			if !names[d] {
				bad = append(bad, d)
			}
		}
	}
	if len(bad) > 0 {
		return errs.Invalid("tools not registered for this tenant: %s", strings.Join(bad, ", ")).With("tools", bad)
	}
	return nil
}

// validateSkill keeps a header small relative to the injection budget: every
// skill header is in every turn's context, the body only on demand.
func (s *Service) validateSkill(_ context.Context, sk *types.Skill) error {
	if strings.TrimSpace(sk.Name) == "" || len(sk.Name) > MaxSkillNameChars {
		return errs.Invalid("name is required (max %d characters)", MaxSkillNameChars)
	}
	if strings.TrimSpace(sk.Description) == "" || len(sk.Description) > MaxSkillDescChars {
		return errs.Invalid("description is required (max %d characters)", MaxSkillDescChars)
	}
	if cost := len(sk.Name) + len(sk.Description) + 8; cost > assembly.DefaultInjectionBudget/20 {
		return errs.Invalid("skill header uses %d characters; at most %d (5%% of the injection budget)", cost, assembly.DefaultInjectionBudget/20)
	}
	if len(sk.Body) > MaxSkillBodyChars {
		return errs.Invalid("body is too long (max %d characters)", MaxSkillBodyChars)
	}
	if sk.Priority < -100 || sk.Priority > 100 {
		return errs.Invalid("priority must be -100..100")
	}
	return nil
}

func (s *Service) validateWorkflow(_ context.Context, d *types.WorkflowDef) error {
	if strings.TrimSpace(d.Name) == "" || len(d.Name) > MaxNameChars {
		return errs.Invalid("name is required (max %d characters)", MaxNameChars)
	}
	if d.Role != "" && validKey(d.Role) != nil {
		return errs.Invalid("role %q is not a valid key", d.Role)
	}
	return flowdef.Validate(d)
}
