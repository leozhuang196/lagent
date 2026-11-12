package feishu

import (
	"context"
	"sync"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	orchfeishu "github.com/eternityx/lagent/internal/orch/feishu"
)

type recIngest struct {
	mu  sync.Mutex
	got []orchfeishu.Inbound
}

func (r *recIngest) Ingest(_ context.Context, in orchfeishu.Inbound) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, in)
	return false, nil
}

// Events go through the SDK dispatcher exactly as long-connection frames do.
func TestGatewayParsesEvents(t *testing.T) {
	rec := &recIngest{}
	g := &Gateway{Ingest: rec}
	d := g.Dispatcher()

	group := `{"schema":"2.0","header":{"event_id":"ev1","event_type":"im.message.receive_v1"},"event":{
		"sender":{"sender_id":{"open_id":"ou_1"},"sender_type":"user"},
		"message":{"message_id":"om_2","root_id":"om_1","chat_id":"oc_1","chat_type":"group","message_type":"text",
		"content":"{\"text\":\"@_user_1 周报 \"}","mentions":[{"key":"@_user_1","id":{"open_id":"ou_bot"},"name":"bot"}]}}}`
	if _, err := d.Do(context.Background(), []byte(group)); err != nil {
		t.Fatal(err)
	}
	noMention := `{"schema":"2.0","header":{"event_id":"ev2","event_type":"im.message.receive_v1"},"event":{
		"sender":{"sender_id":{"open_id":"ou_1"},"sender_type":"user"},
		"message":{"message_id":"om_3","chat_id":"oc_1","chat_type":"group","message_type":"text","content":"{\"text\":\"hi\"}"}}}`
	if _, err := d.Do(context.Background(), []byte(noMention)); err != nil {
		t.Fatal(err)
	}
	card := `{"schema":"2.0","header":{"event_id":"ev3","event_type":"card.action.trigger"},"event":{
		"operator":{"open_id":"ou_1"},"action":{"tag":"button","value":{"lagent_pending":"pnd_1","decision":"approve"}},
		"context":{"open_message_id":"om_card","open_chat_id":"oc_1"}}}`
	resp, err := d.Do(context.Background(), []byte(card))
	if err != nil || resp == nil {
		t.Fatalf("card: %v %v", resp, err)
	}

	if len(rec.got) != 2 {
		t.Fatalf("ingested: %+v", rec.got)
	}
	m := rec.got[0]
	if m.EventID != "ev1" || m.Kind != orchfeishu.KindMessage || m.ChatID != "oc_1" || m.OpenID != "ou_1" || m.MessageID != "om_2" || m.RootID != "om_1" || m.Text != "周报" {
		t.Fatalf("message: %+v", m)
	}
	c := rec.got[1]
	if c.Kind != orchfeishu.KindCardAction || c.PendingID != "pnd_1" || c.Decision != "approve" || c.CardMsgID != "om_card" || c.ChatID != "oc_1" || c.OpenID != "ou_1" {
		t.Fatalf("card action: %+v", c)
	}
}

func TestMentionsBotWithConfiguredID(t *testing.T) {
	s := func(v string) *string { return &v }
	g := &Gateway{BotOpenID: "ou_bot"}
	if g.mentionsBot(nil) {
		t.Fatal("no mention")
	}
	other := []*larkim.MentionEvent{{Key: s("@_user_1"), Id: &larkim.UserId{OpenId: s("ou_alice")}}}
	if g.mentionsBot(other) {
		t.Fatal("mentioning someone else is not addressing the bot")
	}
	bot := append(other, &larkim.MentionEvent{Key: s("@_user_2"), Id: &larkim.UserId{OpenId: s("ou_bot")}})
	if !g.mentionsBot(bot) {
		t.Fatal("bot mention")
	}
}
