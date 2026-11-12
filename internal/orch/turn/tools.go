package turn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/rules/artifact"
	"github.com/eternityx/lagent/internal/rules/budget"
	"github.com/eternityx/lagent/internal/rules/permission"
)

const maxToolOutput = 100 << 10

// handleTools drives every tool_use of an assistant message to a final ledger
// state, then writes all results as one user message. It returns true when the
// turn suspended on pending items.
func (r *run) handleTools(ctx context.Context, msg *types.Message) (bool, error) {
	uses := msg.ToolUses()
	ledger, err := r.ledger(ctx)
	if err != nil {
		return false, err
	}
	var newPendings []*types.Pending
	var waiting []string
	var runnable []*types.ToolCall

	for _, u := range uses {
		tc := ledger[u.ToolUseID]
		if tc == nil {
			tc, err = r.decide(ctx, u)
			if err != nil {
				return false, err
			}
			ledger[u.ToolUseID] = tc
			if tc.Status == types.CallAwaiting {
				p := r.approvalPending(tc, msg)
				newPendings = append(newPendings, p)
			}
		}
		switch tc.Status {
		case types.CallDecided:
			runnable = append(runnable, tc)
		case types.CallStarted:
			// Crashed mid-call (§8.3).
			if !tc.SideEffect || r.idempotent(tc.Tool) {
				runnable = append(runnable, tc)
			} else {
				tc, err = r.setLedger(ctx, tc.ID, func(c *types.ToolCall) { c.Status = types.CallAwaiting })
				if err != nil {
					return false, err
				}
				ledger[u.ToolUseID] = tc
				newPendings = append(newPendings, &types.Pending{
					Kind: types.PendingUnknown, ToolCallID: tc.ID, ToolName: tc.Tool, Risk: tc.Risk, Args: redact(tc.Input),
					Title:  fmt.Sprintf("「%s」执行中断，结果未知", tc.Tool),
					Detail: "该操作可能已经执行。请确认它是否已发生：已发生则不再重试；未发生则重新执行。",
				})
			}
		case types.CallAwaiting:
			if tc.PendingID == "" {
				if containsPendingFor(newPendings, tc.ID) {
					continue // created in this pass
				}
				// Crashed between the ledger write and the pending write.
				if t, ok := r.tools[tc.Tool]; ok && t.Meta().Source == types.SourcePlatform {
					tc, err = r.setLedger(ctx, tc.ID, func(c *types.ToolCall) { c.Status = types.CallDecided })
					if err != nil {
						return false, err
					}
					ledger[u.ToolUseID] = tc
					runnable = append(runnable, tc)
				} else {
					newPendings = append(newPendings, r.approvalPending(tc, msg))
				}
				continue
			}
			p, err := r.e.Store.Pendings.Get(ctx, tc.PendingID)
			if err != nil {
				return false, err
			}
			if p.Status == types.PendingOpen {
				waiting = append(waiting, p.ID)
				continue
			}
			tc, err = r.applyResolution(ctx, tc, p)
			if err != nil {
				return false, err
			}
			ledger[u.ToolUseID] = tc
			if tc.Status == types.CallDecided {
				runnable = append(runnable, tc)
			}
		}
	}

	if err := r.execute(ctx, runnable, ledger, msg, &newPendings); err != nil {
		return false, err
	}
	if len(newPendings) > 0 || len(waiting) > 0 {
		return true, r.suspendOn(ctx, newPendings, waiting...)
	}
	// Every call is final: one user message with all results, in call order.
	var blocks []types.Block
	for _, u := range uses {
		tc := ledger[u.ToolUseID]
		blocks = append(blocks, types.Block{Type: types.BlockToolResult, ToolUseID: u.ToolUseID, Text: tc.Output, IsError: tc.IsError})
	}
	_, err = r.appendMessage(ctx, types.RoleUser, blocks, "")
	return false, err
}

func (r *run) ledger(ctx context.Context) (map[string]*types.ToolCall, error) {
	cs, err := repo.All(ctx, r.e.Store.ToolCalls, repo.Query{Filter: repo.Filter{"turn_id": r.turn.ID}}, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]*types.ToolCall{}
	for _, c := range cs {
		out[c.ToolUseID] = c
	}
	return out, nil
}

func (r *run) idempotent(name string) bool {
	t, ok := r.tools[name]
	return ok && t.Meta().Idempotent
}

// decide creates the ledger entry for a new tool_use.
func (r *run) decide(ctx context.Context, u types.Block) (*types.ToolCall, error) {
	tc := &types.ToolCall{
		SessionID: r.sess.ID, TurnID: r.turn.ID, ToolUseID: u.ToolUseID, Tool: u.Name, Input: u.Input,
		IdempotencyKey: r.turn.ID + ":" + u.ToolUseID,
	}
	tc.TenantID = r.sess.TenantID
	t, ok := r.tools[u.Name]
	if !ok {
		tc.Status, tc.IsError = types.CallFailed, true
		tc.Output = fmt.Sprintf("Tool %q is not available in this session.", u.Name)
	} else {
		meta := t.Meta()
		tc.SideEffect = meta.SideEffect
		tc.Risk = permission.EffectiveRisk(meta.Risk, r.overrides[u.Name])
		d, why := permission.Decide(permission.Input{Tool: u.Name, Risk: tc.Risk, Mode: r.sess.PermissionMode, Grants: r.sess.StandingGrants, Args: u.Input})
		tc.Decision = string(d) + ": " + why
		if d == permission.Allow {
			tc.Status = types.CallDecided
		} else {
			tc.Status = types.CallAwaiting
		}
	}
	if err := r.e.Store.ToolCalls.Insert(ctx, tc); err != nil {
		if errs.Is(err, errs.CodeConflict) {
			return r.e.Store.ToolCalls.FindOne(ctx, repo.Filter{"turn_id": r.turn.ID, "tool_use_id": u.ToolUseID})
		}
		return nil, err
	}
	r.emit(ctx, types.EvToolRequested, map[string]any{"tool_call_id": tc.ID, "tool": tc.Tool, "risk": tc.Risk, "status": tc.Status, "input": redact(tc.Input)})
	return tc, nil
}

func (r *run) approvalPending(tc *types.ToolCall, msg *types.Message) *types.Pending {
	trigger := fmt.Sprintf("第 %d 次迭代", r.turn.Iterations)
	if txt := strings.TrimSpace(msg.PlainText()); txt != "" {
		trigger += "；模型说明：" + truncate(txt, 300)
	}
	if last := r.lastToolSource(); last != "" {
		trigger += "；此前读取的数据来源：" + last
	}
	return &types.Pending{
		Kind: types.PendingApproval, ToolCallID: tc.ID, ToolName: tc.Tool, Risk: tc.Risk, Args: redact(tc.Input),
		Title: fmt.Sprintf("是否允许调用「%s」？", tc.Tool), TriggerSource: trigger,
	}
}

// lastToolSource names the tools whose output the model read most recently,
// so an approver can spot instructions injected through tool data (§8.2).
func (r *run) lastToolSource() string {
	for i := len(r.history) - 1; i >= 0; i-- {
		m := r.history[i]
		if m.Role == types.RoleAssistant {
			var names []string
			for _, u := range m.ToolUses() {
				names = append(names, u.Name)
			}
			if len(names) > 0 && i < len(r.history)-1 {
				return strings.Join(names, ", ")
			}
		}
	}
	return ""
}

func (r *run) setLedger(ctx context.Context, id string, fn func(c *types.ToolCall)) (*types.ToolCall, error) {
	return repo.Update(ctx, r.e.Store.ToolCalls, id, func(c *types.ToolCall) error { fn(c); return nil })
}

// applyResolution turns a resolved pending item into a ledger state.
func (r *run) applyResolution(ctx context.Context, tc *types.ToolCall, p *types.Pending) (*types.ToolCall, error) {
	res := p.Resolution
	who := res.By.Name
	if who == "" {
		who = res.By.ID
	}
	r.emit(ctx, types.EvPendingResolved, map[string]any{"pending_id": p.ID, "decision": res.Decision, "via": res.Via})
	return r.setLedger(ctx, tc.ID, func(c *types.ToolCall) {
		switch p.Kind {
		case types.PendingApproval:
			switch res.Decision {
			case types.DecisionApprove:
				c.Status, c.Decision = types.CallDecided, "approved by "+who
			case types.DecisionAnswer:
				c.Status, c.IsError, c.Decision = types.CallDenied, true, "redirected by "+who
				c.Output = "The user did not allow this call and gave new instructions: " + res.Answer
			default:
				c.Status, c.IsError, c.Decision = types.CallDenied, true, "rejected by "+who
				c.Output = "The user rejected this call. Do not retry it in another form; adapt or ask."
			}
		case types.PendingQuestion:
			if res.Decision == types.DecisionAnswer {
				c.Status, c.Output = types.CallSucceeded, tool.WrapData("user_answer", res.Answer)
			} else {
				c.Status, c.IsError, c.Output = types.CallDenied, true, "The user chose not to answer and asked to stop."
			}
		case types.PendingSubtasks, types.PendingExternalJob:
			if res.Decision == types.DecisionDone {
				c.Status, c.Output = types.CallSucceeded, res.Answer
			} else {
				c.Status, c.IsError, c.Output = types.CallFailed, true, "Stopped by "+who+"."
			}
		case types.PendingUnknown:
			switch res.Decision {
			case types.DecisionHappened:
				c.Status, c.Output = types.CallSucceeded, "A person confirmed this call did take effect. Do not repeat it."
			case types.DecisionNotHappened:
				c.Status, c.Decision = types.CallDecided, "confirmed not executed by "+who
			default:
				c.Status, c.IsError, c.Output = types.CallFailed, true, "Stopped by "+who+"; the outcome of this call is unknown."
			}
		}
		now := r.e.Clock.Now()
		if c.Status != types.CallDecided {
			c.EndedAt = &now
		}
	})
}

// execute runs decided calls: read-only ones concurrently, others in order.
func (r *run) execute(ctx context.Context, calls []*types.ToolCall, ledger map[string]*types.ToolCall, msg *types.Message, pendings *[]*types.Pending) error {
	var parallel, serial []*types.ToolCall
	for _, c := range calls {
		if t, ok := r.tools[c.Tool]; ok && t.Meta().ReadOnly {
			parallel = append(parallel, c)
		} else {
			serial = append(serial, c)
		}
	}
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for _, c := range parallel {
		wg.Add(1)
		go func(c *types.ToolCall) {
			defer wg.Done()
			out, p, err := r.call(ctx, c)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if out != nil {
				ledger[c.ToolUseID] = out
			}
			if p != nil {
				*pendings = append(*pendings, p)
			}
		}(c)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	for _, c := range serial {
		if err := r.checkCancel(ctx); err != nil {
			return err
		}
		out, p, err := r.call(ctx, c)
		if err != nil {
			return err
		}
		ledger[c.ToolUseID] = out
		if p != nil {
			*pendings = append(*pendings, p)
		}
	}
	return nil
}

// call executes one decided call through the ledger. A returned pending means
// the tool asked to suspend (question, subtasks, external job).
func (r *run) call(ctx context.Context, c *types.ToolCall) (*types.ToolCall, *types.Pending, error) {
	t := r.tools[c.Tool]
	now := r.e.Clock.Now()
	c, err := repo.Update(ctx, r.e.Store.ToolCalls, c.ID, func(x *types.ToolCall) error {
		if x.Status != types.CallDecided && x.Status != types.CallStarted {
			return errs.New(errs.CodeAlreadyResolved, "call is %s", x.Status)
		}
		x.Status, x.StartedAt = types.CallStarted, &now
		return nil
	})
	if errs.Is(err, errs.CodeAlreadyResolved) {
		return c, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	r.emit(ctx, types.EvToolStarted, map[string]any{"tool_call_id": c.ID, "tool": c.Tool})
	env := r.env(c)
	start := time.Now()
	res, callErr := t.Call(ctx, c.Input, env)
	dur := time.Since(start).Milliseconds()
	if callErr != nil && ctx.Err() != nil {
		// Interrupted: leave "started"; recovery decides (§8.3).
		return nil, nil, context.Cause(ctx)
	}
	var pending *types.Pending
	status := types.CallSucceeded
	output, isErr := res.Content, res.IsError
	if callErr != nil {
		status, isErr = types.CallFailed, true
		output = "Tool failed: " + callErr.Error()
	}
	if res.Control != nil && callErr == nil {
		switch res.Control.Kind {
		case tool.ControlResult:
			result := res.Control.Result
			if err := r.updateTurn(ctx, func(t *types.Turn) { t.Result = result }); err != nil {
				return nil, nil, err
			}
		case tool.ControlQuestion:
			status = types.CallAwaiting
			pending = &types.Pending{Kind: types.PendingQuestion, ToolCallID: c.ID, ToolName: c.Tool, Title: "需要补充信息", Question: res.Control.Question, Options: res.Control.Options}
		case tool.ControlSubtasks:
			status = types.CallAwaiting
			pending = &types.Pending{Kind: types.PendingSubtasks, ToolCallID: c.ID, ToolName: c.Tool, Title: fmt.Sprintf("等待 %d 个子任务完成", len(res.Control.ChildTurnIDs)), ChildTurnIDs: res.Control.ChildTurnIDs}
		case tool.ControlExternalJob:
			status = types.CallAwaiting
			pending = &types.Pending{Kind: types.PendingExternalJob, ToolCallID: c.ID, ToolName: c.Tool, Title: "等待外部作业完成", ExternalRef: res.Control.ExternalRef}
		}
	}
	if len(output) > maxToolOutput {
		output = output[:maxToolOutput] + fmt.Sprintf("\n[output truncated at %d bytes]", maxToolOutput)
	}
	end := r.e.Clock.Now()
	c, err = r.setLedger(ctx, c.ID, func(x *types.ToolCall) {
		x.Status, x.Output, x.IsError, x.DurationMs = status, output, isErr, dur
		if status != types.CallAwaiting {
			x.EndedAt = &end
		}
	})
	if err != nil {
		return nil, nil, err
	}
	if status != types.CallAwaiting {
		r.emit(ctx, types.EvToolCompleted, map[string]any{"tool_call_id": c.ID, "tool": c.Tool, "is_error": isErr, "duration_ms": dur, "artifacts": res.Artifacts})
	}
	if c.Risk != types.RiskReadOnly || c.Tool == "load_skill" {
		r.audit(ctx, "tool.call", c.Tool, map[string]any{"tool_call_id": c.ID, "status": status, "decision": c.Decision, "risk": c.Risk})
	}
	return c, pending, nil
}

func (r *run) env(c *types.ToolCall) tool.Env {
	return tool.Env{
		TenantID: r.sess.TenantID, SessionID: r.sess.ID, TurnID: r.turn.ID, ToolUseID: c.ToolUseID,
		IdempotencyKey: c.IdempotencyKey, Principal: r.sess.CreatedBy, Workspace: r.ws,
		RoleSkills: r.role.Skills, MemorySpaces: uniq(append(append([]string{}, r.role.MemorySpaces...), r.sess.MemorySpaces...)),
		Channels: r.sess.Channels, Debug: r.sess.Debug,
		Archive: r.archive,
		Secret: func(ctx context.Context, ref string) (string, error) {
			if r.e.Secrets == nil {
				return "", errs.New(errs.CodeUnavailable, "no secret store")
			}
			return r.e.Secrets.Get(ctx, ref)
		},
	}
}

// archive stores a workspace file as an artifact right away (§9.12).
func (r *run) archive(ctx context.Context, relPath, name string) (string, error) {
	if r.e.Objects == nil {
		return "", errs.New(errs.CodeUnavailable, "no object store")
	}
	p, err := r.ws.Resolve(relPath)
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", errs.Wrap(errs.CodeInvalid, err, "cannot open %s", relPath)
	}
	defer f.Close()
	st, _ := f.Stat()
	a := &types.Artifact{SessionID: r.sess.ID, TurnID: r.turn.ID, RunID: r.sess.RunID, StepRunID: r.sess.StepRunID, Name: artifact.SafeName(name), Size: st.Size()}
	a.TenantID = r.sess.TenantID
	a.ID = "art_" + strings.TrimPrefix(r.turn.ID, "trn_") + "_" + fmt.Sprint(time.Now().UnixNano())
	a.ObjectKey = artifact.Key(artifact.Owner{TenantID: r.sess.TenantID, SessionID: r.sess.ID, TurnID: r.turn.ID, RunID: r.sess.RunID, StepRunID: r.sess.StepRunID}, a.ID+"-"+a.Name)
	h := newHashReader(f)
	if err := r.e.Objects.Put(ctx, a.ObjectKey, h, st.Size(), contentType(a.Name)); err != nil {
		return "", errs.Wrap(errs.CodeUnavailable, err, "archive failed")
	}
	a.SHA256, a.ContentType = h.sum(), contentType(a.Name)
	if err := r.e.Store.Artifacts.Insert(ctx, a); err != nil {
		return "", err
	}
	r.emit(ctx, types.EvArtifactCreated, map[string]any{"artifact": a})
	return a.ID, nil
}

// applyNonToolResolutions handles resolved pending items not tied to a tool
// call (budget prompts) when a suspended turn resumes.
func (r *run) applyNonToolResolutions(ctx context.Context) error {
	for _, pid := range r.turn.WaitingOn {
		p, err := r.e.Store.Pendings.Get(ctx, pid)
		if err != nil {
			return err
		}
		if p.Kind != types.PendingBudget || p.Status != types.PendingResolved {
			continue
		}
		r.emit(ctx, types.EvPendingResolved, map[string]any{"pending_id": p.ID, "decision": p.Resolution.Decision})
		if p.Resolution.Decision == types.DecisionContinue {
			if err := r.updateTurn(ctx, func(t *types.Turn) {
				t.MaxIterations += budget.DefaultMaxIterations
				if t.TokenBudget > 0 {
					t.TokenBudget += budget.Limits(r.role, 0, 0).TokenBudget
				}
			}); err != nil {
				return err
			}
			continue
		}
		return r.finish(ctx, types.TurnCompleted, &types.TurnError{Code: string(errs.CodeBudget), Message: "stopped at budget limit by " + p.Resolution.By.ID})
	}
	return r.updateTurn(ctx, func(t *types.Turn) { t.WaitingOn = nil })
}

// redact shortens long argument values for display in approval cards.
func redact(in json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(in, &m) != nil {
		return in
	}
	for k, v := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "token") || strings.Contains(lk, "password") || strings.Contains(lk, "secret") {
			m[k] = "***"
			continue
		}
		if s, ok := v.(string); ok && len(s) > 2000 {
			m[k] = s[:2000] + "…"
		}
	}
	out, _ := json.Marshal(m)
	return out
}

func containsPendingFor(ps []*types.Pending, toolCallID string) bool {
	for _, p := range ps {
		if p.ToolCallID == toolCallID {
			return true
		}
	}
	return false
}
