package workflow

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/id"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/rules/flowdef"
)

// subtaskSuffix is what dispatch_subtask appends to each child instruction;
// it is platform boilerplate, not part of what the user asked.
const subtaskSuffix = "\n\nWhen done, call submit_result with a concise summary of the outcome."

// Promote turns a session's runtime execution graph into a draft workflow
// definition (§9.14 "沉淀"): one step per subtask instruction, or per user
// turn without subtasks, serial by default. Concrete values are left as-is
// and listed in the description so the author can parameterise them.
func (s *Service) Promote(ctx context.Context, sessionID string) (*types.WorkflowDef, error) {
	p, ok := tenant.From(ctx)
	if !ok {
		return nil, errs.New(errs.CodeUnauthorized, "no principal")
	}
	sess, err := s.Store.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !(p.IsAdmin() || sess.CreatedBy.ID == p.ID) {
		return nil, errs.New(errs.CodeForbidden, "not your session")
	}
	turns, err := repo.All(ctx, s.Store.Turns, repo.Query{Filter: repo.Filter{"session_id": sessionID}}, 500)
	if err != nil {
		return nil, err
	}
	b := &promoter{s: s, ctx: ctx, root: sess}
	for _, t := range turns { // ids are ULIDs: creation order
		if t.Status != types.TurnCompleted {
			continue
		}
		if err := b.addTurn(t); err != nil {
			return nil, err
		}
	}
	if len(b.steps) == 0 {
		return nil, errs.Invalid("session %s has no completed turns to promote", sessionID)
	}
	def := b.build()
	if err := flowdef.Validate(def); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "promoted definition is invalid")
	}
	base := def.Key
	for attempt := 0; ; attempt++ {
		err := s.Store.WorkflowDefs.Insert(ctx, def)
		if err == nil {
			break
		}
		if !errs.Is(err, errs.CodeConflict) || attempt >= 3 {
			return nil, err
		}
		// A draft of this key already exists (promoted before): pick a fresh key
		// rather than overwrite someone's edits.
		def.ID = ""
		def.Key = base + "-" + strings.ToLower(id.New()[20:])
	}
	audit(ctx, s.Store, "workflow.promote", def.Key, map[string]any{"session_id": sessionID, "steps": len(def.Steps)})
	return def, nil
}

type promoter struct {
	s       *Service
	ctx     context.Context
	root    *types.Session
	steps   []types.StepDef
	texts   []string
	notes   []string
	trimmed int
}

func (b *promoter) addTurn(t *types.Turn) error {
	text, err := b.userText(t.SessionID, t.ID)
	if err != nil {
		return err
	}
	children, err := repo.All(b.ctx, b.s.Store.Sessions, repo.Query{Filter: repo.Filter{"labels.parent_session": b.root.ID, "parent_turn_id": t.ID}}, 100)
	if err != nil {
		return err
	}
	if len(children) == 0 {
		if strings.TrimSpace(text) != "" {
			b.add(text, "", nil, true, "")
		}
		return nil
	}
	var subIDs []string
	for _, c := range children {
		instr, err := b.firstText(c.ID)
		if err != nil {
			return err
		}
		role := ""
		if c.RoleKey != b.root.RoleKey {
			role = c.RoleKey
		}
		if sid := b.add(instr, role, nil, false, ""); sid != "" {
			subIDs = append(subIDs, sid)
		}
		// Children cannot normally dispatch further; if they did, their work
		// is flattened into this level.
		grand, err := repo.All(b.ctx, b.s.Store.Sessions, repo.Query{Filter: repo.Filter{"labels.parent_session": c.ID}}, 100)
		if err != nil {
			return err
		}
		for _, g := range grand {
			gi, err := b.firstText(g.ID)
			if err != nil {
				return err
			}
			if sid := b.add(gi, "", nil, false, ""); sid != "" {
				subIDs = append(subIDs, sid)
				b.notes = append(b.notes, fmt.Sprintf("步骤 %s 原为嵌套子任务，已展平一层", sid))
			}
		}
	}
	// The turn that dispatched the subtasks combined their results.
	if strings.TrimSpace(text) != "" {
		var refs []string
		for _, sid := range subIDs {
			refs = append(refs, fmt.Sprintf("{{steps.%s.summary}}", sid))
		}
		b.add(text, "", subIDs, false, "\n\n子任务结果：\n"+strings.Join(refs, "\n---\n"))
	}
	return nil
}

// add appends a serial step depending on the previous one (plus extra deps).
// withPrev passes the previous step's summary along, standing in for the
// conversation history the original turn could see. suffix is template text
// built by the promoter (placeholders kept); instr is user text (escaped).
func (b *promoter) add(instr, role string, extra []string, withPrev bool, suffix string) string {
	instr = strings.TrimSpace(strings.TrimSuffix(instr, subtaskSuffix))
	if instr == "" {
		return ""
	}
	if len(b.steps) >= flowdef.MaxSteps {
		b.trimmed++
		return ""
	}
	b.texts = append(b.texts, instr)
	stepID := fmt.Sprintf("step%d", len(b.steps)+1)
	instr = escapeBraces(instr) + suffix
	deps := append([]string(nil), extra...)
	if n := len(b.steps); n > 0 {
		prev := b.steps[n-1].ID
		if !contains(deps, prev) {
			deps = append(deps, prev)
		}
		if withPrev {
			instr += fmt.Sprintf("\n\n上一步结果：\n{{steps.%s.summary}}", prev)
		}
	}
	b.steps = append(b.steps, types.StepDef{ID: stepID, Name: firstRunes(instr, 30), Role: role, Instruction: instr, DependsOn: deps})
	return stepID
}

func (b *promoter) build() *types.WorkflowDef {
	var desc strings.Builder
	fmt.Fprintf(&desc, "从会话 %s 提炼的草稿：%d 个步骤，默认串行；可并行的步骤请在编辑时调整依赖。", b.root.ID, len(b.steps))
	if b.trimmed > 0 {
		fmt.Fprintf(&desc, "\n超过 %d 个步骤，已省略 %d 个。", flowdef.MaxSteps, b.trimmed)
	}
	for _, n := range b.notes {
		desc.WriteString("\n" + n + "。")
	}
	if vals := concreteValues(b.texts); len(vals) > 0 {
		desc.WriteString("\n未参数化的具体值：" + strings.Join(vals, "、"))
	}
	name := b.root.Title
	if name == "" {
		name = "从会话提炼的流程"
	}
	tail := strings.ToLower(b.root.ID)
	if len(tail) > 10 {
		tail = tail[len(tail)-10:]
	}
	def := &types.WorkflowDef{Name: name, Description: desc.String(), Role: b.root.RoleKey, Steps: b.steps}
	def.Key, def.Ver, def.Status = "promoted-"+tail, 0, types.AssetDraft
	return def
}

// userText joins the user messages of one turn (the request, plus anything
// folded in from the inbox).
func (b *promoter) userText(sessionID, turnID string) (string, error) {
	msgs, err := repo.All(b.ctx, b.s.Store.Messages, repo.Query{Filter: repo.Filter{"session_id": sessionID, "turn_id": turnID, "role": string(types.RoleUser)}}, 50)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, m := range msgs {
		if t := strings.TrimSpace(m.PlainText()); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func (b *promoter) firstText(sessionID string) (string, error) {
	p, err := b.s.Store.Messages.List(b.ctx, repo.Query{Filter: repo.Filter{"session_id": sessionID, "role": string(types.RoleUser)}, Limit: 1})
	if err != nil || len(p.Items) == 0 {
		return "", err
	}
	return p.Items[0].PlainText(), nil
}

// escapeBraces keeps literal "{{" in user text from being read as a
// placeholder (which Validate would reject).
func escapeBraces(s string) string {
	return strings.NewReplacer("{{", "{ {", "}}", "} }").Replace(s)
}

var concrete = []*regexp.Regexp{
	regexp.MustCompile(`\d{4}-\d{1,2}-\d{1,2}`),
	regexp.MustCompile(`\d{4}年\d{1,2}月(\d{1,2}日)?`),
	regexp.MustCompile(`\d{1,2}月\d{1,2}日`),
	regexp.MustCompile(`https?://[^\s，。]+`),
	regexp.MustCompile(`「[^」]{1,40}」|“[^”]{1,40}”|"[^"]{1,40}"`),
	regexp.MustCompile(`\d+(\.\d+)?%|\b\d{3,}\b`),
}

// concreteValues lists dates, links, quoted names and notable numbers that a
// template author probably wants to turn into parameters.
func concreteValues(texts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, re := range concrete {
			for _, m := range re.FindAllString(t, -1) {
				if !seen[m] && len(out) < 30 {
					seen[m] = true
					out = append(out, m)
				}
			}
		}
	}
	return out
}

func firstRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
