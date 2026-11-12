// Package subtask implements dispatch_subtask: the model fans work out to
// child turns. The parent suspends (it never waits in memory) and is resumed
// when every child has ended. Children cannot dispatch further (no recursion):
// their sessions deny the tool, and the capability layer cannot reach
// orchestration at all (R-4).
package subtask

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/session"
	"github.com/eternityx/lagent/internal/orch/turn"
)

// ToolName is the dispatch tool's name.
const ToolName = "dispatch_subtask"

// Dispatcher creates child turns and collects their results.
type Dispatcher struct {
	Sessions    *session.Service
	Store       *repo.Store
	Queue       queue.Queue
	MaxParallel int
}

// Tool returns the dispatch_subtask tool.
func (d *Dispatcher) Tool() tool.Tool {
	max := d.MaxParallel
	if max <= 0 {
		max = 4
	}
	return &tool.Func{
		S: types.ToolSpec{
			Name: ToolName,
			Description: fmt.Sprintf("Split work into up to %d independent subtasks that run in parallel, each in a fresh context. "+
				"Each instruction must be self-contained. You receive every subtask's result when all have finished.", max),
			InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["tasks"],"properties":{"tasks":{"type":"array","maxItems":%d,"minItems":1,"items":{"type":"object","additionalProperties":false,"required":["instruction"],"properties":{"instruction":{"type":"string"},"role":{"type":"string","description":"Optional role key for this subtask"}}}}}}`, max)),
		},
		M: types.ToolMeta{Source: types.SourcePlatform, Category: "orchestration", Risk: types.RiskReadOnly},
		Fn: func(ctx context.Context, in json.RawMessage, env tool.Env) (tool.Result, error) {
			var a struct {
				Tasks []struct {
					Instruction string `json:"instruction"`
					Role        string `json:"role"`
				} `json:"tasks"`
			}
			if r := tool.Decode(in, &a); r != nil {
				return *r, nil
			}
			if len(a.Tasks) == 0 {
				return tool.Errorf("tasks is empty"), nil
			}
			if len(a.Tasks) > max {
				return tool.Errorf("at most %d subtasks per call; got %d", max, len(a.Tasks)), nil
			}
			parent, err := d.Store.Sessions.Get(ctx, env.SessionID)
			if err != nil {
				return tool.Result{}, err
			}
			pctx := tenant.With(ctx, parent.CreatedBy)
			var ids []string
			for i, t := range a.Tasks {
				role := t.Role
				if role == "" {
					role = parent.RoleKey
				}
				child, err := d.Sessions.Create(pctx, session.CreateInput{
					Title: fmt.Sprintf("subtask %d", i+1), RoleKey: role, PermissionMode: parent.PermissionMode,
					MemorySpaces: parent.MemorySpaces, StandingGrants: parent.StandingGrants, Origin: types.OriginSubtask,
					Debug: parent.Debug, ParentTurnID: env.TurnID, ToolDeny: append([]string{ToolName}, parent.ToolDeny...),
					Labels: map[string]string{"parent_session": parent.ID},
				})
				if err != nil {
					return tool.Errorf("cannot start subtask %d: %v", i+1, err), nil
				}
				instr := t.Instruction + "\n\nWhen done, call submit_result with a concise summary of the outcome."
				tr, err := turn.StartTurn(pctx, d.Store, d.Queue, child.ID, turn.StartInput{Text: instr, Trigger: types.TriggerSubtask, Author: "parent:" + env.TurnID})
				if err != nil {
					return tool.Result{}, err
				}
				ids = append(ids, tr.ID)
			}
			return tool.Result{Control: &tool.Control{Kind: tool.ControlSubtasks, ChildTurnIDs: ids}}, nil
		},
	}
}

// OnPending implements turn.Hooks.
func (d *Dispatcher) OnPending(context.Context, *types.Session, *types.Turn, *types.Pending) {}

// OnTurnEnd resolves the parent's subtasks item once all children ended.
func (d *Dispatcher) OnTurnEnd(ctx context.Context, s *types.Session, t *types.Turn) {
	if t.ParentTurnID == "" {
		return
	}
	ps, err := repo.All(ctx, d.Store.Pendings, repo.Query{Filter: repo.Filter{"turn_id": t.ParentTurnID, "kind": string(types.PendingSubtasks), "status": string(types.PendingOpen)}}, 0)
	if err != nil {
		return
	}
	for _, p := range ps {
		if !contains(p.ChildTurnIDs, t.ID) {
			continue
		}
		var parts []string
		for i, cid := range p.ChildTurnIDs {
			c, err := d.Store.Turns.Get(ctx, cid)
			if err != nil || !c.Status.Terminal() {
				return // not all done yet; the last child resolves it
			}
			parts = append(parts, describe(i+1, c))
		}
		answer := tool.WrapData("subtasks", strings.Join(parts, "\n\n"))
		_, _ = d.Sessions.ResolveSystem(ctx, p.ID, types.DecisionDone, answer)
	}
}

func describe(n int, c *types.Turn) string {
	switch {
	case c.Status == types.TurnCompleted && c.Result != nil:
		s := fmt.Sprintf("## Subtask %d (completed)\n%s", n, c.Result.Summary)
		if len(c.Result.Data) > 0 {
			s += "\nData: " + string(c.Result.Data)
		}
		if len(c.Result.Artifacts) > 0 {
			s += "\nArtifacts: " + strings.Join(c.Result.Artifacts, ", ")
		}
		return s
	case c.Status == types.TurnCompleted:
		return fmt.Sprintf("## Subtask %d (completed)\n%s", n, c.FinalText)
	case c.Error != nil:
		return fmt.Sprintf("## Subtask %d (%s)\n%s", n, c.Status, c.Error.Message)
	default:
		return fmt.Sprintf("## Subtask %d (%s)", n, c.Status)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

var _ turn.Hooks = (*Dispatcher)(nil)
