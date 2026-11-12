package feishu

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/rules/approval"
)

// Keys of a card button's value. The gateway passes them through untouched;
// the pending item, not the card, is the source of truth (§9.5).
const (
	ValPending  = "lagent_pending"
	ValTurn     = "lagent_turn"
	ValDecision = "decision"
	ValAnswer   = "answer"
)

// ParseAction extracts a lagent card action from a button value. ok is false
// for buttons that are not ours.
func ParseAction(v map[string]any) (pendingID, turnID, decision, answer string, ok bool) {
	str := func(k string) string { s, _ := v[k].(string); return s }
	pendingID, turnID, decision, answer = str(ValPending), str(ValTurn), str(ValDecision), str(ValAnswer)
	ok = decision != "" && (pendingID != "" || turnID != "")
	return
}

// User-facing texts (Chinese, the users' language).
const (
	msgUnbound       = "本群尚未绑定到任何租户，暂时无法使用助手。请联系租户管理员在控制台完成群绑定。"
	msgEmpty         = "请在 @我 之后写下你的问题。"
	msgUnsupported   = "暂时只支持文本消息，请用文字描述你的需求。"
	msgQueued        = "已收到，会在当前任务中一并处理。"
	msgWaitingButton = "已收到。当前任务正在等待处理，请使用卡片上的按钮操作；你的消息会在任务继续后一并处理。"
	msgNotOwner      = "这个话题由其他同事发起，只有发起人可以继续对话。请 @我 开启新的话题。"
	msgBusy          = "当前排队的消息过多，请稍后再试。"
	msgRoleMissing   = "本群配置的助手角色不可用，请联系租户管理员检查群绑定设置。"
	msgFailedStart   = "消息处理失败，请稍后重试。"
	msgCancelled     = "任务已取消。"
	msgDone          = "已完成。"
)

var atTag = regexp.MustCompile(`(?i)<\s*at\b`)

// sanitize neutralises Feishu mention tags in model or tool content, so an
// answer can never @all a group (§8.2: content is data, not instructions).
func sanitize(s string) string {
	return atTag.ReplaceAllString(s, "<​at")
}

// mdEscape makes content safe inside lark_md.
func mdEscape(s string) string {
	s = sanitize(s)
	return strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s)
}

func mention(p tenant.Principal) string {
	if p.Kind == tenant.KindFeishuUser && p.ID != "" {
		return fmt.Sprintf("<at id=%s></at>", p.ID)
	}
	if p.Name != "" {
		return mdEscape(p.Name)
	}
	if p.Kind == tenant.KindSystem {
		return "系统"
	}
	return mdEscape(p.ID)
}

func viaLabel(via string) string {
	switch via {
	case "feishu":
		return "飞书"
	case "web":
		return "web"
	case "api":
		return "API"
	case "system":
		return "系统"
	}
	return via
}

func decisionLabel(p *types.Pending, r *types.Resolution) string {
	switch r.Decision {
	case types.DecisionApprove:
		return "批准"
	case types.DecisionReject:
		return "拒绝"
	case types.DecisionAnswer:
		if p.Kind == types.PendingApproval {
			return "改为新的指示"
		}
		a, _ := truncateRunes(r.Answer, 200)
		return "回答「" + mdEscape(a) + "」"
	case types.DecisionContinue:
		return "继续"
	case types.DecisionStop:
		return "停止"
	case types.DecisionHappened:
		return "确认已发生"
	case types.DecisionNotHappened:
		return "确认未发生"
	case types.DecisionDone:
		return "完成"
	}
	return string(r.Decision)
}

func riskLabel(r types.RiskLevel) string {
	switch r {
	case types.RiskReadOnly:
		return "只读"
	case types.RiskLocalWrite:
		return "本地写入"
	case types.RiskCommand:
		return "执行命令（高风险）"
	case types.RiskExternal:
		return "外部操作（高风险）"
	}
	return string(r)
}

func button(text, typ string, value map[string]any) map[string]any {
	return map[string]any{"tag": "button", "text": map[string]any{"tag": "plain_text", "content": text}, "type": typ, "value": value}
}

func decide(p *types.Pending, d types.Decision, answer string) map[string]any {
	v := map[string]any{ValPending: p.ID, ValDecision: string(d)}
	if answer != "" {
		v[ValAnswer] = answer
	}
	return v
}

// buildCard renders a message card (schema 1.0). update_multi makes the card
// shared, so one PATCH updates it for every viewer of the group.
func buildCard(title, template, md string, buttons []map[string]any) json.RawMessage {
	elements := []any{map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": md}}}
	if len(buttons) > 0 {
		actions := make([]any, len(buttons))
		for i, b := range buttons {
			actions[i] = b
		}
		elements = append(elements, map[string]any{"tag": "action", "actions": actions})
	}
	c := map[string]any{
		"config":   map[string]any{"wide_screen_mode": true, "update_multi": true},
		"header":   map[string]any{"title": map[string]any{"tag": "plain_text", "content": title}, "template": template},
		"elements": elements,
	}
	raw, _ := json.Marshal(c)
	return raw
}

// PendingCard renders an open pending item, or nil for kinds people do not
// resolve (subtasks, external jobs).
func PendingCard(p *types.Pending, owner tenant.Principal) json.RawMessage {
	var b strings.Builder
	title := mdEscape(p.Title)
	if title != "" {
		fmt.Fprintf(&b, "**%s**\n", title)
	}
	switch p.Kind {
	case types.PendingApproval:
		fmt.Fprintf(&b, "工具：`%s`　风险：%s\n", mdEscape(p.ToolName), riskLabel(p.Risk))
		if p.Detail != "" {
			d, _ := truncateRunes(p.Detail, 800)
			b.WriteString(mdEscape(d) + "\n")
		}
		if len(p.Args) > 0 {
			a, cut := truncateRunes(string(p.Args), 1200)
			if cut {
				a += "…"
			}
			fmt.Fprintf(&b, "参数：\n```\n%s\n```\n", mdEscape(a))
		}
		if p.TriggerSource != "" {
			ts, _ := truncateRunes(p.TriggerSource, 300)
			fmt.Fprintf(&b, "触发来源：%s\n", mdEscape(ts))
		}
		fmt.Fprintf(&b, "请 %s 确认是否执行。", mention(owner))
		buttons := []map[string]any{
			button("批准", "primary", decide(p, types.DecisionApprove, "")),
			button("拒绝", "danger", decide(p, types.DecisionReject, "")),
		}
		// "Approve all" never covers high-risk items, so it is only offered on
		// cards that are themselves batchable (§9.5).
		if approval.Batchable(p) {
			buttons = append(buttons, button("全部批准（不含高风险）", "default", map[string]any{ValTurn: p.TurnID, ValPending: p.ID, ValDecision: DecisionApproveAll}))
		}
		return buildCard("需要审批", "orange", b.String(), buttons)
	case types.PendingQuestion:
		q, _ := truncateRunes(p.Question, 1500)
		b.WriteString(mdEscape(q) + "\n")
		var buttons []map[string]any
		for i, o := range p.Options {
			if i >= 10 {
				break
			}
			label, _ := truncateRunes(o, 40)
			buttons = append(buttons, button(label, "default", decide(p, types.DecisionAnswer, o)))
		}
		if len(buttons) == 0 {
			fmt.Fprintf(&b, "请 %s 在本话题中 @我 回复答案。", mention(owner))
		} else {
			fmt.Fprintf(&b, "请 %s 选择一个选项。", mention(owner))
		}
		return buildCard("需要你的回答", "blue", b.String(), buttons)
	case types.PendingBudget:
		if p.Detail != "" {
			b.WriteString(mdEscape(p.Detail) + "\n")
		}
		fmt.Fprintf(&b, "任务已达到预算上限，请 %s 决定是否继续。", mention(owner))
		return buildCard("预算已用尽", "yellow", b.String(), []map[string]any{
			button("继续", "primary", decide(p, types.DecisionContinue, "")),
			button("停止", "danger", decide(p, types.DecisionStop, "")),
		})
	case types.PendingUnknown:
		fmt.Fprintf(&b, "工具 `%s` 执行中断，无法确定是否已生效。请 %s 确认实际结果。", mdEscape(p.ToolName), mention(owner))
		return buildCard("操作结果未知", "red", b.String(), []map[string]any{
			button("已发生", "default", decide(p, types.DecisionHappened, "")),
			button("未发生（可重试）", "default", decide(p, types.DecisionNotHappened, "")),
		})
	}
	return nil
}

// ResolvedCard renders a pending item after resolution. already marks a
// decision made elsewhere first, which is a normal outcome (§9.5).
func ResolvedCard(p *types.Pending, already bool, extra string) json.RawMessage {
	var b strings.Builder
	if t := mdEscape(p.Title); t != "" {
		fmt.Fprintf(&b, "**%s**\n", t)
	}
	if p.Kind == types.PendingApproval && p.ToolName != "" {
		fmt.Fprintf(&b, "工具：`%s`\n", mdEscape(p.ToolName))
	}
	if p.Kind == types.PendingQuestion && p.Question != "" {
		q, _ := truncateRunes(p.Question, 500)
		b.WriteString(mdEscape(q) + "\n")
	}
	r := p.Resolution
	switch {
	case r == nil:
		b.WriteString("已处理。")
	case already:
		fmt.Fprintf(&b, "已由 %s 通过 %s 处理（%s）。", mention(r.By), viaLabel(r.Via), decisionLabel(p, r))
	default:
		fmt.Fprintf(&b, "已由 %s 通过 %s %s。", mention(r.By), viaLabel(r.Via), decisionLabel(p, r))
	}
	if extra != "" {
		b.WriteString("\n" + extra)
	}
	template := "green"
	if r != nil && (r.Decision == types.DecisionReject || r.Decision == types.DecisionStop) {
		template = "grey"
	}
	return buildCard("已处理", template, b.String(), nil)
}
