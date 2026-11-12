package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
)

// fakeAPI emulates POST /v1/messages with a canned SSE body and records the
// request bodies the adapter sent.
type fakeAPI struct {
	mu     sync.Mutex
	bodies [][]byte
	reply  func(w http.ResponseWriter)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/messages" {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.bodies = append(f.bodies, b)
	f.mu.Unlock()
	f.reply(w)
}

func (f *fakeAPI) lastBody(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatal("no request made")
	}
	var m map[string]any
	if err := json.Unmarshal(f.bodies[len(f.bodies)-1], &m); err != nil {
		t.Fatalf("request body: %v", err)
	}
	return m
}

func sse(events ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, e := range events {
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(e), &head)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, e)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
	}
}

func msgStart(usage string) string {
	return `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":` + usage + `}}`
}

func msgDelta(stop string, out int) string {
	return fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`, stop, out)
}

const msgStop = `{"type":"message_stop"}`

func newTestProvider(t *testing.T, f *fakeAPI) *Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p, err := New(Config{APIKey: "test-key", BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTextReply(t *testing.T) {
	f := &fakeAPI{reply: sse(
		msgStart(`{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}`),
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
		`{"type":"content_block_stop","index":0}`,
		msgDelta("end_turn", 7),
		msgStop,
	)}
	p := newTestProvider(t, f)
	var deltas []string
	resp, err := p.Stream(context.Background(), "claude-opus-5-5", llm.Request{
		System: []llm.SystemPart{
			{Text: "stable instructions"},
			{Text: "tool guide", Cache: true},
			{Text: "volatile: today is Monday"},
		},
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}}},
		Effort:   "high",
	}, func(d llm.Delta) { deltas = append(deltas, d.Text) })
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(deltas, ""); got != "Hello world" {
		t.Fatalf("deltas %q", got)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "Hello world" || resp.Stop != llm.StopEndTurn {
		t.Fatalf("resp %+v", resp)
	}
	u := resp.Usage
	if u.InputTokens != 10 || u.OutputTokens != 7 || u.CacheReadTokens != 100 || u.CacheWriteTokens != 20 {
		t.Fatalf("usage %+v", u)
	}

	body := f.lastBody(t)
	if body["model"] != "claude-opus-5-5" || body["stream"] != true {
		t.Fatalf("model/stream: %v %v", body["model"], body["stream"])
	}
	if body["max_tokens"].(float64) != DefaultMaxTokens {
		t.Fatalf("max_tokens %v", body["max_tokens"])
	}
	sys := body["system"].([]any)
	if len(sys) != 3 {
		t.Fatalf("system %v", sys)
	}
	for i, want := range []bool{false, true, false} {
		_, has := sys[i].(map[string]any)["cache_control"]
		if has != want {
			t.Fatalf("system[%d] cache_control=%v, want %v", i, has, want)
		}
	}
	if cc := sys[1].(map[string]any)["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("cache_control %v", cc)
	}
	th := body["thinking"].(map[string]any)
	if th["type"] != "adaptive" {
		t.Fatalf("thinking %v", th)
	}
	if _, bad := th["budget_tokens"]; bad {
		t.Fatal("budget_tokens must never be sent")
	}
	if oc := body["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Fatalf("output_config %v", oc)
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatal("tool_choice must not be forced")
	}
}

func TestToolUseStreamedJSON(t *testing.T) {
	f := &fakeAPI{reply: sse(
		msgStart(`{"input_tokens":5,"output_tokens":1}`),
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Checking."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\": \"Par"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"is\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		msgDelta("tool_use", 30),
		msgStop,
	)}
	p := newTestProvider(t, f)
	resp, err := p.Stream(context.Background(), "claude-opus-5-5", llm.Request{
		Tools: []types.ToolSpec{{
			Name: "get_weather", Description: "Weather by city",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`),
		}},
		Messages:  []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("weather?")}}},
		MaxTokens: 1000,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != llm.StopToolUse || len(resp.Content) != 2 {
		t.Fatalf("resp %+v", resp)
	}
	tu := resp.Content[1]
	if tu.Type != types.BlockToolUse || tu.ToolUseID != "toolu_1" || tu.Name != "get_weather" {
		t.Fatalf("tool_use %+v", tu)
	}
	var in struct{ City string }
	if err := json.Unmarshal(tu.Input, &in); err != nil || in.City != "Paris" {
		t.Fatalf("input %s (%v)", tu.Input, err)
	}

	body := f.lastBody(t)
	tools := body["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["name"] != "get_weather" || tool["description"] != "Weather by city" {
		t.Fatalf("tool %v", tool)
	}
	schema := tool["input_schema"].(map[string]any)
	if schema["additionalProperties"] != false || schema["required"].([]any)[0] != "city" {
		t.Fatalf("schema not passed verbatim: %v", schema)
	}
	if body["max_tokens"].(float64) != 1000 {
		t.Fatalf("max_tokens %v", body["max_tokens"])
	}
}

func TestThinkingRoundTrip(t *testing.T) {
	sig := "EqQBCkYIBRgCKkB+sig/with+slashes=="
	f := &fakeAPI{reply: sse(
		msgStart(`{"input_tokens":5,"output_tokens":1}`),
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me <think> & check"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"`+sig+`"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"lookup","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		msgDelta("tool_use", 12),
		msgStop,
	)}
	p := newTestProvider(t, f)
	var thinking []llm.Delta
	user := types.Message{Role: types.RoleUser, Content: []types.Block{types.Text("go")}}
	resp, err := p.Stream(context.Background(), "claude-opus-5-5", llm.Request{Messages: []types.Message{user}},
		func(d llm.Delta) {
			if d.Thinking {
				thinking = append(thinking, d)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(thinking) != 1 || thinking[0].Text != "Let me <think> & check" {
		t.Fatalf("thinking deltas %+v", thinking)
	}
	tb := resp.Content[0]
	if tb.Type != types.BlockThinking || tb.Provider != "anthropic" || tb.Text != "Let me <think> & check" {
		t.Fatalf("thinking block %+v", tb)
	}
	var raw map[string]string
	if err := json.Unmarshal(tb.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["type"] != "thinking" || raw["signature"] != sig || raw["thinking"] != "Let me <think> & check" {
		t.Fatalf("raw %s", tb.Raw)
	}

	// Simulate persistence (JSON round trip) and replay with a tool result.
	stored, _ := json.Marshal(resp.Content)
	var content []types.Block
	if err := json.Unmarshal(stored, &content); err != nil {
		t.Fatal(err)
	}
	history := []types.Message{
		user,
		{Role: types.RoleAssistant, Content: content},
		{Role: types.RoleUser, Content: []types.Block{
			{Type: types.BlockToolResult, ToolUseID: "toolu_9", Text: "boom", IsError: true},
			{Type: types.BlockToolResult, ToolUseID: "toolu_8", Text: "ok"},
		}},
		// A thinking block from another provider must not be sent.
		{Role: types.RoleAssistant, Content: []types.Block{
			{Type: types.BlockThinking, Provider: "openai", Raw: json.RawMessage(`{"type":"reasoning"}`)},
			types.Text("done"),
		}},
	}
	f.reply = sse(msgStart(`{"input_tokens":5,"output_tokens":1}`), msgDelta("end_turn", 1), msgStop)
	if _, err := p.Stream(context.Background(), "claude-opus-5-5", llm.Request{Messages: history}, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	rawBody := f.bodies[len(f.bodies)-1]
	f.mu.Unlock()
	// The stored block goes out verbatim apart from Go's JSON HTML escaping
	// (< > & as \u003c...), which the SDK applies to the whole body and which
	// decodes to identical strings server-side.
	var sent struct {
		Messages []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rawBody, &sent); err != nil {
		t.Fatal(err)
	}
	if got := htmlUnescape(string(sent.Messages[1].Content[0])); got != string(tb.Raw) {
		t.Fatalf("thinking block not replayed unchanged:\nwant %s\ngot  %s", tb.Raw, got)
	}
	body := f.lastBody(t)
	msgs := body["messages"].([]any)
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[0].(map[string]any)["type"] != "thinking" || asst[1].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("assistant content %v", asst)
	}
	if in := asst[1].(map[string]any)["input"].(map[string]any); in["q"] != "x" {
		t.Fatalf("tool_use input %v", in)
	}
	results := msgs[2].(map[string]any)["content"].([]any)
	r0 := results[0].(map[string]any)
	if r0["type"] != "tool_result" || r0["tool_use_id"] != "toolu_9" || r0["is_error"] != true {
		t.Fatalf("tool_result %v", r0)
	}
	if c := r0["content"].([]any)[0].(map[string]any); c["type"] != "text" || c["text"] != "boom" {
		t.Fatalf("tool_result content %v", c)
	}
	if _, ok := results[1].(map[string]any)["is_error"]; ok {
		t.Fatal("is_error should be omitted on success")
	}
	last := msgs[3].(map[string]any)["content"].([]any)
	if len(last) != 1 || last[0].(map[string]any)["type"] != "text" {
		t.Fatalf("foreign thinking leaked: %v", last)
	}
}

func TestRateLimited(t *testing.T) {
	f := &fakeAPI{reply: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(429)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}}
	p := newTestProvider(t, f)
	_, err := p.Stream(context.Background(), "claude-opus-5-5", llm.Request{
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}}},
	}, nil)
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.ErrRateLimited || le.RetryAfter != 17 {
		t.Fatalf("err %#v", err)
	}
	if len(f.bodies) != 1 {
		t.Fatalf("SDK retried %d times; the router owns retries", len(f.bodies))
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		typ    string
		want   llm.ErrorKind
	}{
		{529, "overloaded_error", llm.ErrOverloaded},
		{401, "authentication_error", llm.ErrAuth},
		{403, "permission_error", llm.ErrAuth},
		{400, "invalid_request_error", llm.ErrInvalid},
		{500, "api_error", llm.ErrOther},
		{503, "", llm.ErrOther},
		{422, "", llm.ErrInvalid},
	}
	for _, c := range cases {
		f := &fakeAPI{reply: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(c.status)
			if c.typ != "" {
				fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":"x"}}`, c.typ)
			}
		}}
		p := newTestProvider(t, f)
		_, err := p.Stream(context.Background(), "claude-opus-5-5", llm.Request{
			Messages: []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}}},
		}, nil)
		var le *llm.Error
		if !errors.As(err, &le) || le.Kind != c.want {
			t.Errorf("status %d %s: got %v, want %s", c.status, c.typ, err, c.want)
		}
	}
}

func TestMidStreamOverloaded(t *testing.T) {
	f := &fakeAPI{reply: sse(
		msgStart(`{"input_tokens":5,"output_tokens":1}`),
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	)}
	p := newTestProvider(t, f)
	_, err := p.Stream(context.Background(), "claude-opus-5-5", llm.Request{
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}}},
	}, nil)
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.ErrOverloaded {
		t.Fatalf("err %v", err)
	}
}

func TestRefusal(t *testing.T) {
	f := &fakeAPI{reply: sse(
		msgStart(`{"input_tokens":5,"output_tokens":1}`),
		`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"no"}},"usage":{"output_tokens":0}}`,
		msgStop,
	)}
	p := newTestProvider(t, f)
	resp, err := p.Stream(context.Background(), "claude-opus-5-5", llm.Request{
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != llm.StopRefusal || len(resp.Content) != 0 {
		t.Fatalf("resp %+v", resp)
	}
}

func TestStopMapping(t *testing.T) {
	for in, want := range map[string]llm.StopReason{
		"end_turn": llm.StopEndTurn, "stop_sequence": llm.StopEndTurn, "tool_use": llm.StopToolUse,
		"max_tokens": llm.StopMaxTokens, "pause_turn": llm.StopPause, "refusal": llm.StopRefusal,
	} {
		if got := mapStop(sdk.StopReason(in)); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}

func TestSystemCacheBreakpointLimit(t *testing.T) {
	var parts []llm.SystemPart
	for i := 0; i < 6; i++ {
		parts = append(parts, llm.SystemPart{Text: fmt.Sprintf("p%d", i), Cache: true})
	}
	params, err := buildParams("claude-opus-5-5", llm.Request{System: parts})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i, s := range params.System {
		if s.CacheControl.Type != "" {
			n++
			if i < 2 {
				t.Fatalf("early part %d kept its breakpoint", i)
			}
		}
	}
	if n != 4 {
		t.Fatalf("%d breakpoints, want 4", n)
	}
}

func TestLegacyModelOmitsThinking(t *testing.T) {
	params, _ := buildParams("claude-haiku-4-5", llm.Request{})
	b, _ := json.Marshal(params)
	if strings.Contains(string(b), `"thinking"`) {
		t.Fatalf("legacy model got thinking config: %s", b)
	}
}

func TestMissingAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error without api key")
	}
	t.Setenv("ANTHROPIC_API_KEY", "from-env")
	p, err := New(Config{})
	if err != nil || p.Name() != "anthropic" {
		t.Fatalf("p=%v err=%v", p, err)
	}
}

func htmlUnescape(s string) string {
	return strings.NewReplacer(`\u003c`, "<", `\u003e`, ">", `\u0026`, "&").Replace(s)
}
