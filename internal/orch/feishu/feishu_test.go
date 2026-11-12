package feishu_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	connfeishu "github.com/eternityx/lagent/internal/capability/connector/feishu"
	"github.com/eternityx/lagent/internal/capability/tool"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/fake"
	"github.com/eternityx/lagent/internal/infra/queue"
	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/orch/feishu"
	"github.com/eternityx/lagent/internal/orch/orchtest"
)

// fakeClient records what would be sent to Feishu and, like Feishu, delivers
// one message per uuid.
type fakeClient struct {
	mu    sync.Mutex
	calls []call
	byID  map[string]string
	n     int
}

type call struct {
	Op, Target, Text, UUID string
	Card                   json.RawMessage
}

func (f *fakeClient) record(c call) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byID == nil {
		f.byID = map[string]string{}
	}
	if c.UUID != "" {
		if id, ok := f.byID[c.UUID]; ok {
			return id
		}
	}
	f.n++
	id := fmt.Sprintf("om_bot_%d", f.n)
	if c.UUID != "" {
		f.byID[c.UUID] = id
	}
	f.calls = append(f.calls, c)
	return id
}

func (f *fakeClient) SendText(_ context.Context, chatID, text, uuid string) (string, error) {
	return f.record(call{Op: "send", Target: chatID, Text: text, UUID: uuid}), nil
}
func (f *fakeClient) ReplyText(_ context.Context, msgID, text, uuid string) (string, error) {
	return f.record(call{Op: "reply", Target: msgID, Text: text, UUID: uuid}), nil
}
func (f *fakeClient) ReplyCard(_ context.Context, msgID string, card json.RawMessage, uuid string) (string, error) {
	return f.record(call{Op: "card", Target: msgID, Card: card, UUID: uuid}), nil
}
func (f *fakeClient) UpdateCard(_ context.Context, msgID string, card json.RawMessage) error {
	f.record(call{Op: "update", Target: msgID, Card: card})
	return nil
}

func (f *fakeClient) find(op string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Op == op {
			out = append(out, c)
		}
	}
	return out
}

type fixture struct {
	*orchtest.Env
	svc *feishu.Service
	fc  *fakeClient
	seq int
}

func setup(t *testing.T) *fixture {
	t.Helper()
	fc := &fakeClient{}
	f := &fixture{fc: fc}
	f.Env = orchtest.New(t, func(e *orchtest.Env) {
		f.svc = &feishu.Service{Store: e.Store, Sessions: e.Sessions, Queue: e.Queue, Counter: e.Coord, Client: fc,
			Links: feishu.LinksFunc(func(ctx context.Context, sid, tid string) ([]feishu.Link, error) {
				return []feishu.Link{{Name: "report.md", URL: "https://files/report.md?sig=x"}}, nil
			})}
		e.Engine.Hooks = append(e.Engine.Hooks, f.svc)
		e.Queue.Handle(queue.TypeFeishuInbound, f.svc.HandleInbound)
		e.Queue.Handle(queue.TypeFeishuDeliver, f.svc.HandleDeliver)
		e.Registry.Register(connfeishu.Tools(fc, connfeishu.StoreBindings{Store: e.Store})...)
	})
	if _, err := f.svc.BindChat(f.Admin, feishu.BindInput{ChatID: "oc_1", Name: "ops"}); err != nil {
		t.Fatal(err)
	}
	return f
}

// mention simulates the gateway accepting an @mention.
func (f *fixture) mention(user, msgID, root, text string) bool {
	f.T.Helper()
	f.seq++
	dup, err := f.svc.Ingest(context.Background(), feishu.Inbound{
		EventID: fmt.Sprintf("ev_%s", msgID), Kind: feishu.KindMessage, ChatID: "oc_1", OpenID: user,
		ChatType: "group", MessageID: msgID, RootID: root, Text: text, MsgType: "text",
	})
	if err != nil {
		f.T.Fatal(err)
	}
	f.Wait()
	return dup
}

func (f *fixture) click(user, eventID, cardMsg, pendingID, decision string) {
	f.T.Helper()
	if _, err := f.svc.Ingest(context.Background(), feishu.Inbound{
		EventID: eventID, Kind: feishu.KindCardAction, ChatID: "oc_1", OpenID: user,
		CardMsgID: cardMsg, PendingID: pendingID, Decision: decision,
	}); err != nil {
		f.T.Fatal(err)
	}
	f.Wait()
}

func (f *fixture) thread(root string) *types.FeishuThread {
	f.T.Helper()
	th, err := f.Store.FeishuThreads.FindOne(f.Ctx, repo.Filter{"root_msg_id": root})
	if err != nil {
		f.T.Fatalf("thread %s: %v", root, err)
	}
	return th
}

func (f *fixture) turns(sessionID string) []*types.Turn {
	ts, err := repo.All(f.Ctx, f.Store.Turns, repo.Query{Filter: repo.Filter{"session_id": sessionID}}, 0)
	if err != nil {
		f.T.Fatal(err)
	}
	return ts
}

func TestUnboundChatGetsExplanation(t *testing.T) {
	f := setup(t)
	if _, err := f.svc.Ingest(context.Background(), feishu.Inbound{EventID: "e1", Kind: feishu.KindMessage, ChatID: "oc_other", OpenID: "ou_1", ChatType: "group", MessageID: "om_x", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	f.Wait()
	rs := f.fc.find("reply")
	if len(rs) != 1 || rs[0].Target != "om_x" || !strings.Contains(rs[0].Text, "尚未绑定") {
		t.Fatalf("replies: %+v", rs)
	}
	if n, _ := f.Store.Sessions.Count(f.Ctx, repo.Filter{}); n != 0 {
		t.Fatalf("no session expected, got %d", n)
	}
}

func TestFirstMentionCreatesBoundSessionAndReplies(t *testing.T) {
	f := setup(t)
	// The model replies in the thread through the tool (pre-approved by the
	// standing grant even though the role asks for everything), then finishes.
	f.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("final answer after " + res.Text), nil
		}
		return fake.ToolCall("tu1", connfeishu.ToolReply, map[string]string{"thread_id": "om_1", "text": "working on it"}), nil
	})
	f.mention("ou_1", "om_1", "", "weekly numbers please")

	th := f.thread("om_1")
	if th.ChatID != "oc_1" || th.TenantID != "t1" {
		t.Fatalf("thread: %+v", th)
	}
	sess, err := f.Store.Sessions.Get(f.Ctx, th.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Origin != types.OriginFeishu || sess.CreatedBy.ID != "ou_1" || sess.CreatedBy.Kind != "feishu_user" {
		t.Fatalf("session: %+v", sess)
	}
	if c, ok := sess.HasChannel(types.ChannelFeishu); !ok || c.Target != "oc_1/om_1" {
		t.Fatalf("channels: %+v", sess.Channels)
	}
	if len(sess.StandingGrants) != 1 || sess.StandingGrants[0] != (types.StandingGrant{Tool: "feishu_reply", Field: "thread_id", Value: "om_1"}) {
		t.Fatalf("grants: %+v", sess.StandingGrants)
	}
	ts := f.turns(sess.ID)
	if len(ts) != 1 || ts[0].Status != types.TurnCompleted {
		t.Fatalf("turns: %+v", ts)
	}
	if ps := f.OpenPendings(ts[0].ID); len(ps) != 0 {
		t.Fatalf("thread reply must not need approval: %+v", ps)
	}
	rs := f.fc.find("reply")
	if len(rs) != 2 || rs[0].Text != "working on it" || rs[0].Target != "om_1" {
		t.Fatalf("replies: %+v", rs)
	}
	if rs[1].Target != "om_1" || !strings.HasPrefix(rs[1].Text, "final answer") || !strings.Contains(rs[1].Text, "https://files/report.md?sig=x") {
		t.Fatalf("final reply: %+v", rs[1])
	}
}

func TestSecondMessageInThreadReusesSession(t *testing.T) {
	f := setup(t)
	f.mention("ou_1", "om_1", "", "hello")
	f.mention("ou_1", "om_2", "om_1", "again")
	th := f.thread("om_1")
	if n, _ := f.Store.Sessions.Count(f.Ctx, repo.Filter{}); n != 1 {
		t.Fatalf("sessions: %d", n)
	}
	ts := f.turns(th.SessionID)
	if len(ts) != 2 || ts[1].Status != types.TurnCompleted || ts[1].FinalText != "echo: again" {
		t.Fatalf("turns: %+v", ts)
	}
	// Another member cannot drive someone else's session.
	f.mention("ou_2", "om_3", "om_1", "me too")
	rs := f.fc.find("reply")
	if last := rs[len(rs)-1]; last.Target != "om_3" || !strings.Contains(last.Text, "只有发起人") {
		t.Fatalf("last reply: %+v", last)
	}
}

func writeThenDone(req llm.Request) (*llm.Response, error) {
	if res, ok := fake.LastToolResult(req); ok {
		return fake.TextReply("done: " + res.Text), nil
	}
	return fake.ToolCall("tu1", "write_file", map[string]string{"path": "out/report.md", "content": "# hi"}), nil
}

func TestApprovalCardButtonResumesTurn(t *testing.T) {
	f := setup(t)
	f.Fake.SetScript(writeThenDone)
	f.mention("ou_1", "om_1", "", "write a report")
	sid := f.thread("om_1").SessionID
	tr := f.turns(sid)[0]
	if tr.Status != types.TurnSuspended {
		t.Fatalf("turn %s", tr.Status)
	}
	cards := f.fc.find("card")
	if len(cards) != 1 || cards[0].Target != "om_1" {
		t.Fatalf("cards: %+v", cards)
	}
	p := f.OpenPendings(tr.ID)[0]
	if !strings.Contains(string(cards[0].Card), p.ID) || !strings.Contains(string(cards[0].Card), "批准") {
		t.Fatalf("card: %s", cards[0].Card)
	}

	// Free text while waiting is not a decision: it is queued for later.
	f.mention("ou_1", "om_2", "om_1", "yes go ahead")
	if got := f.Turn(tr.ID).Status; got != types.TurnSuspended {
		t.Fatalf("free text must not approve; turn %s", got)
	}
	rs := f.fc.find("reply")
	if last := rs[len(rs)-1]; !strings.Contains(last.Text, "按钮") {
		t.Fatalf("expected button hint, got %+v", last)
	}
	if s, _ := f.Store.Sessions.Get(f.Ctx, sid); len(s.Inbox) != 1 || s.Inbox[0].Text != "yes go ahead" {
		t.Fatalf("message must be folded into the inbox: %+v", s.Inbox)
	}

	cardMsg := "om_card"
	f.click("ou_1", "ev_click_1", cardMsg, p.ID, "approve")
	tr = f.Turn(tr.ID)
	if tr.Status != types.TurnCompleted || !strings.HasPrefix(tr.FinalText, "done:") {
		t.Fatalf("after approval: %s %q", tr.Status, tr.FinalText)
	}
	ups := f.fc.find("update")
	if len(ups) != 1 || ups[0].Target != cardMsg || !strings.Contains(string(ups[0].Card), "通过 飞书 批准") {
		t.Fatalf("updates: %+v", ups)
	}
	got, err := f.Sessions.GetPending(f.Admin, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resolution.Via != "feishu" || got.Resolution.By.ID != "ou_1" {
		t.Fatalf("resolution: %+v", got.Resolution)
	}
	// The final answer is replied into the thread.
	found := false
	for _, r := range f.fc.find("reply") {
		if r.Target == "om_1" && strings.HasPrefix(r.Text, "done:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no final reply: %+v", f.fc.find("reply"))
	}
}

func TestCardAfterWebResolutionShowsAlreadyResolved(t *testing.T) {
	f := setup(t)
	f.Fake.SetScript(writeThenDone)
	f.mention("ou_1", "om_1", "", "write a report")
	tr := f.turns(f.thread("om_1").SessionID)[0]
	p := f.OpenPendings(tr.ID)[0]
	if _, err := f.Sessions.Resolve(f.Admin, p.ID, types.DecisionReject, "", "web"); err != nil {
		t.Fatal(err)
	}
	f.Wait()
	f.click("ou_1", "ev_click_2", "om_card", p.ID, "approve")
	ups := f.fc.find("update")
	if len(ups) != 1 || !strings.Contains(string(ups[0].Card), "通过 web 处理（拒绝）") {
		t.Fatalf("updates: %+v", ups)
	}
	got, err := f.Sessions.GetPending(f.Admin, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resolution.Decision != types.DecisionReject {
		t.Fatalf("first decision must stand: %+v", got.Resolution)
	}
}

func TestCardClickByOtherUserIsRefused(t *testing.T) {
	f := setup(t)
	f.Fake.SetScript(writeThenDone)
	f.mention("ou_1", "om_1", "", "write a report")
	tr := f.turns(f.thread("om_1").SessionID)[0]
	p := f.OpenPendings(tr.ID)[0]
	f.click("ou_2", "ev_click_3", "om_card", p.ID, "approve")
	if len(f.fc.find("update")) != 0 {
		t.Fatal("card must not change")
	}
	if len(f.OpenPendings(tr.ID)) != 1 {
		t.Fatal("pending must stay open")
	}
}

func TestQuestionAnsweredByExactOption(t *testing.T) {
	f := setup(t)
	f.Fake.SetScript(func(req llm.Request) (*llm.Response, error) {
		if res, ok := fake.LastToolResult(req); ok {
			return fake.TextReply("thanks, " + res.Text), nil
		}
		return fake.ToolCall("q1", "ask_user", map[string]any{"question": "Which region?", "options": []string{"East", "West"}}), nil
	})
	f.mention("ou_1", "om_1", "", "go")
	tr := f.turns(f.thread("om_1").SessionID)[0]
	f.mention("ou_1", "om_2", "om_1", "maybe")
	if f.Turn(tr.ID).Status != types.TurnSuspended {
		t.Fatal("unclear text must not answer")
	}
	f.mention("ou_1", "om_3", "om_1", "west")
	tr = f.Turn(tr.ID)
	if tr.Status != types.TurnCompleted || !strings.Contains(tr.FinalText, "West") {
		t.Fatalf("%s %q", tr.Status, tr.FinalText)
	}
}

func TestEventDedupe(t *testing.T) {
	f := setup(t)
	if f.mention("ou_1", "om_1", "", "hello") {
		t.Fatal("first delivery is not a duplicate")
	}
	if !f.mention("ou_1", "om_1", "", "hello") {
		t.Fatal("second delivery must be a duplicate")
	}
	ts := f.turns(f.thread("om_1").SessionID)
	if len(ts) != 1 {
		t.Fatalf("turns: %d", len(ts))
	}
}

func TestSendMessageOnlyToBoundChats(t *testing.T) {
	f := setup(t)
	send, err := f.Registry.Get(f.Ctx, connfeishu.ToolSend)
	if err != nil {
		t.Fatal(err)
	}
	if m := send.Meta(); m.Risk != types.RiskExternal || !m.SideEffect {
		t.Fatalf("meta: %+v", m)
	}
	env := tool.Env{TenantID: "t1", IdempotencyKey: "k1"}
	res, err := send.Call(f.Ctx, json.RawMessage(`{"chat_id":"oc_evil","text":"hi"}`), env)
	if err != nil || !res.IsError {
		t.Fatalf("unbound chat must be refused: %v %+v", err, res)
	}
	// A chat bound to another tenant is just as unbound for this one.
	res, err = send.Call(f.Ctx, json.RawMessage(`{"chat_id":"oc_1","text":"hi"}`), tool.Env{TenantID: "t2", IdempotencyKey: "k2"})
	if err != nil || !res.IsError {
		t.Fatalf("other tenant's chat must be refused: %v %+v", err, res)
	}
	res, err = send.Call(f.Ctx, json.RawMessage(`{"chat_id":"oc_1","text":"hi"}`), env)
	if err != nil || res.IsError {
		t.Fatalf("bound chat: %v %+v", err, res)
	}
	if s := f.fc.find("send"); len(s) != 1 || s[0].Target != "oc_1" || s[0].UUID != "k1" {
		t.Fatalf("sends: %+v", s)
	}
}

func TestReplyToolOnlyOwnThread(t *testing.T) {
	f := setup(t)
	reply, _ := f.Registry.Get(f.Ctx, connfeishu.ToolReply)
	env := tool.Env{TenantID: "t1", Channels: []types.Channel{{Kind: types.ChannelFeishu, Target: "oc_1/om_1"}}}
	res, _ := reply.Call(f.Ctx, json.RawMessage(`{"thread_id":"om_9","text":"x"}`), env)
	if !res.IsError {
		t.Fatal("other thread must be refused")
	}
}

func TestBindChatExclusiveAcrossTenants(t *testing.T) {
	f := setup(t)
	other := withTenant(f.Admin, "t2")
	if _, err := f.svc.BindChat(other, feishu.BindInput{ChatID: "oc_1"}); err == nil {
		t.Fatal("a chat bound to t1 cannot be bound to t2")
	}
	if _, err := f.svc.UnbindChat(f.Admin, "oc_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BindChat(other, feishu.BindInput{ChatID: "oc_1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BindChat(f.Ctx, feishu.BindInput{ChatID: "oc_2"}); err == nil {
		t.Fatal("members cannot bind chats")
	}
}

func withTenant(ctx context.Context, tid string) context.Context {
	p, _ := tenant.From(ctx)
	p.TenantID, p.ID = tid, "admin-"+tid
	return tenant.With(ctx, p)
}
