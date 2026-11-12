package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	rulesmem "github.com/eternityx/lagent/internal/rules/memory"
	rulesskill "github.com/eternityx/lagent/internal/rules/skill"
)

func askUser() tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "ask_user",
			Description: "Ask a person for missing information. The task pauses until they answer (possibly much later).",
			InputSchema: tool.Schema(map[string]any{
				"question": tool.Str("A self-contained question"),
				"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional choices"},
			}, "question"),
		},
		M: types.ToolMeta{Source: types.SourcePlatform, Category: "control", Risk: types.RiskReadOnly},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				Question string   `json:"question"`
				Options  []string `json:"options"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			if strings.TrimSpace(a.Question) == "" {
				return tool.Errorf("question is required"), nil
			}
			return tool.Result{Control: &tool.Control{Kind: tool.ControlQuestion, Question: a.Question, Options: a.Options}}, nil
		},
	}
}

func submitResult() tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "submit_result",
			Description: "Report the structured result of this task (used by workflow steps and subtasks). Call once at the end.",
			InputSchema: tool.Schema(map[string]any{
				"summary":   tool.Str("A concise summary of what was found or done"),
				"data":      map[string]any{"description": "Optional structured data"},
				"artifacts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Artifact ids produced"},
			}, "summary"),
		},
		M: types.ToolMeta{Source: types.SourcePlatform, Category: "control", Risk: types.RiskReadOnly},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				Summary   string          `json:"summary"`
				Data      json.RawMessage `json:"data"`
				Artifacts []string        `json:"artifacts"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			if len(a.Data) > 64<<10 {
				return tool.Errorf("data is too large; archive it with publish_artifact and pass the id"), nil
			}
			res := &types.StepResult{Summary: a.Summary, Data: a.Data, Artifacts: a.Artifacts}
			return tool.Result{Content: "result recorded", Control: &tool.Control{Kind: tool.ControlResult, Result: res}}, nil
		},
	}
}

func remember(store *repo.Store) tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "remember",
			Description: "Save a fact for future sessions in a writable memory space.",
			InputSchema: tool.Schema(map[string]any{
				"space":   tool.Str("Memory space name"),
				"key":     tool.Str("Short unique key"),
				"content": tool.Str("The fact to remember"),
			}, "space", "key", "content"),
		},
		// Writing persistent memory is a local write and audited (§9.2).
		M: types.ToolMeta{Source: types.SourceBuiltin, Category: "memory", Risk: types.RiskLocalWrite, Idempotent: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct{ Space, Key, Content string }
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			allowed := false
			for _, s := range env.MemorySpaces {
				if s == a.Space {
					allowed = true
				}
			}
			if !allowed {
				return tool.Errorf("memory space %q is not attached to this session", a.Space), nil
			}
			sp, err := store.MemorySpaces.FindOne(ctx, repo.Filter{"name": a.Space})
			if err != nil {
				return tool.Errorf("memory space %q not found", a.Space), nil
			}
			if !rulesmem.Writable(*sp) {
				return tool.Errorf("memory space %q is read-only", a.Space), nil
			}
			if len(a.Content) > 2000 {
				return tool.Errorf("content too long (max 2000 characters)"), nil
			}
			cur, err := store.MemoryEntries.FindOne(ctx, repo.Filter{"space": a.Space, "key": a.Key})
			if err == nil {
				_, err = repo.Update(ctx, store.MemoryEntries, cur.ID, func(e *types.MemoryEntry) error {
					e.Content, e.SourceTurnID = a.Content, env.TurnID
					return nil
				})
			} else if errs.Is(err, errs.CodeNotFound) {
				err = store.MemoryEntries.Insert(ctx, &types.MemoryEntry{Space: a.Space, Key: a.Key, Content: a.Content, SourceTurnID: env.TurnID})
			}
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Content: fmt.Sprintf("remembered %s/%s", a.Space, a.Key)}, nil
		},
	}
}

func loadSkill(store *repo.Store) tool.Tool {
	return &tool.Func{
		S: types.ToolSpec{
			Name:        "load_skill",
			Description: "Load the full instructions of a skill listed in your system prompt.",
			InputSchema: tool.Schema(map[string]any{"name": tool.Str("Skill key")}, "name"),
		},
		// Read-only, but it changes behaviour, so the engine audits every load (§9.19).
		M: types.ToolMeta{Source: types.SourceBuiltin, Category: "skill", Risk: types.RiskReadOnly, ReadOnly: true},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct{ Name string }
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			if !rulesskill.CanLoad(env.RoleSkills, a.Name) {
				return tool.Errorf("skill %q is not available to this role", a.Name), nil
			}
			sk, err := repo.LatestPublished(ctx, store.Skills, a.Name)
			if err != nil {
				return tool.Errorf("skill %q not found", a.Name), nil
			}
			body := fmt.Sprintf("# Skill: %s\n\n%s", sk.Name, sk.Body)
			if len(sk.Attachments) > 0 {
				body += "\n\nAttachments (artifact ids): " + strings.Join(sk.Attachments, ", ")
			}
			return tool.Result{Content: body}, nil
		},
	}
}
