package openai

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

	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
)

type fakeAPI struct {
	mu     sync.Mutex
	bodies [][]byte
	reply  func(w http.ResponseWriter)
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
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
	var m map[string]any
	if err := json.Unmarshal(f.bodies[len(f.bodies)-1], &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func sse(chunks ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func chunk(delta string, finish string) string {
	f := "null"
	if finish != "" {
		f = fmt.Sprintf("%q", finish)
	}
	return `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + f + `}]}`
}

const usageChunk = `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[],"usage":{"prompt_tokens":120,"completion_tokens":9,"total_tokens":129,"prompt_tokens_details":{"cached_tokens":100}}}`

func newTestProvider(t *testing.T, f *fakeAPI) *Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p, err := New(Config{Name: "gw", BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTextReply(t *testing.T) {
	f := &fakeAPI{reply: sse(
		chunk(`{"role":"assistant","content":""}`, ""),
		chunk(`{"content":"Hello "}`, ""),
		chunk(`{"content":"world"}`, ""),
		chunk(`{}`, "stop"),
		usageChunk,
	)}
	p := newTestProvider(t, f)
	if p.Name() != "gw" {
		t.Fatal(p.Name())
	}
	var deltas []string
	resp, err := p.Stream(context.Background(), "gpt-x", llm.Request{
		System:    []llm.SystemPart{{Text: "A", Cache: true}, {Text: "B"}},
		Messages:  []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}}},
		MaxTokens: 500,
		Effort:    "high",
	}, func(d llm.Delta) { deltas = append(deltas, d.Text) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "Hello world" || resp.Content[0].Text != "Hello world" || resp.Stop != llm.StopEndTurn {
		t.Fatalf("resp %+v deltas %v", resp, deltas)
	}
	if u := resp.Usage; u.InputTokens != 20 || u.CacheReadTokens != 100 || u.OutputTokens != 9 {
		t.Fatalf("usage %+v", u)
	}
	body := f.lastBody(t)
	msgs := body["messages"].([]any)
	if sys := msgs[0].(map[string]any); sys["role"] != "system" || sys["content"] != "A\n\nB" {
		t.Fatalf("system %v", sys)
	}
	if body["max_tokens"].(float64) != 500 || body["stream"] != true {
		t.Fatalf("body %v", body)
	}
	if so := body["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Fatalf("stream_options %v", so)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatal("reasoning_effort sent to a non-reasoning model")
	}
}

func TestToolCallsStreamed(t *testing.T) {
	f := &fakeAPI{reply: sse(
		chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}`, ""),
		chunk(`{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":"{}"}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]}`, ""),
		chunk(`{}`, "tool_calls"),
	)}
	p := newTestProvider(t, f)
	resp, err := p.Stream(context.Background(), "gpt-x", llm.Request{
		Tools: []types.ToolSpec{{Name: "get_weather", Description: "w",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}},
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != llm.StopToolUse || len(resp.Content) != 2 {
		t.Fatalf("resp %+v", resp)
	}
	a, b := resp.Content[0], resp.Content[1]
	if a.ToolUseID != "call_a" || a.Name != "get_weather" || string(a.Input) != `{"city":"Paris"}` {
		t.Fatalf("call a %+v (%s)", a, a.Input)
	}
	if b.ToolUseID != "call_b" || b.Name != "get_time" || string(b.Input) != `{}` {
		t.Fatalf("call b %+v", b)
	}
	tool := f.lastBody(t)["tools"].([]any)[0].(map[string]any)
	fn := tool["function"].(map[string]any)
	if tool["type"] != "function" || fn["name"] != "get_weather" || fn["parameters"].(map[string]any)["required"].([]any)[0] != "city" {
		t.Fatalf("tool %v", tool)
	}
}

func TestHistoryMapping(t *testing.T) {
	f := &fakeAPI{reply: sse(chunk(`{"content":"ok"}`, "stop"))}
	p := newTestProvider(t, f)
	history := []types.Message{
		{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}},
		{Role: types.RoleAssistant, Content: []types.Block{
			{Type: types.BlockThinking, Provider: "anthropic", Raw: json.RawMessage(`{"type":"thinking"}`)},
			types.Text("calling"),
			{Type: types.BlockToolUse, ToolUseID: "call_a", Name: "f", Input: json.RawMessage(`{"x":1}`)},
		}},
		{Role: types.RoleUser, Content: []types.Block{
			{Type: types.BlockToolResult, ToolUseID: "call_a", Text: "boom", IsError: true},
			types.Text("also this"),
		}},
	}
	if _, err := p.Stream(context.Background(), "gpt-x", llm.Request{Messages: history}, nil); err != nil {
		t.Fatal(err)
	}
	msgs := f.lastBody(t)["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages %v", msgs)
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" || asst["content"] != "calling" {
		t.Fatalf("assistant %v", asst)
	}
	if strings.Contains(fmt.Sprint(asst), "thinking") {
		t.Fatal("thinking block leaked")
	}
	tc := asst["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "call_a" || tc["function"].(map[string]any)["arguments"] != `{"x":1}` {
		t.Fatalf("tool_calls %v", tc)
	}
	tm := msgs[2].(map[string]any)
	if tm["role"] != "tool" || tm["tool_call_id"] != "call_a" || tm["content"] != "Error: boom" {
		t.Fatalf("tool message %v", tm)
	}
	if um := msgs[3].(map[string]any); um["role"] != "user" || um["content"] != "also this" {
		t.Fatalf("user %v", um)
	}
}

func TestFinishMapping(t *testing.T) {
	for in, want := range map[string]llm.StopReason{
		"stop": llm.StopEndTurn, "tool_calls": llm.StopToolUse, "length": llm.StopMaxTokens, "content_filter": llm.StopRefusal,
	} {
		if got := mapFinish(in); got != want {
			t.Errorf("%s → %s", in, got)
		}
	}
}

func TestContentFilterRefusal(t *testing.T) {
	f := &fakeAPI{reply: sse(chunk(`{"refusal":"I can't help"}`, ""), chunk(`{}`, "content_filter"))}
	p := newTestProvider(t, f)
	resp, err := p.Stream(context.Background(), "gpt-x", llm.Request{
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}}},
	}, nil)
	if err != nil || resp.Stop != llm.StopRefusal {
		t.Fatalf("resp %+v err %v", resp, err)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status int
		header string
		want   llm.ErrorKind
		retry  int
	}{
		{429, "7", llm.ErrRateLimited, 7},
		{401, "", llm.ErrAuth, 0},
		{400, "", llm.ErrInvalid, 0},
		{502, "", llm.ErrOther, 0},
		{529, "", llm.ErrOverloaded, 0},
	}
	for _, c := range cases {
		f := &fakeAPI{reply: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			if c.header != "" {
				w.Header().Set("Retry-After", c.header)
			}
			w.WriteHeader(c.status)
			io.WriteString(w, `{"error":{"message":"x","type":"t","code":"c"}}`)
		}}
		p := newTestProvider(t, f)
		_, err := p.Stream(context.Background(), "gpt-x", llm.Request{
			Messages: []types.Message{{Role: types.RoleUser, Content: []types.Block{types.Text("hi")}}},
		}, nil)
		var le *llm.Error
		if !errors.As(err, &le) || le.Kind != c.want || le.RetryAfter != c.retry {
			t.Errorf("%d: %#v", c.status, err)
		}
		if len(f.bodies) != 1 {
			t.Errorf("%d: SDK retried", c.status)
		}
	}
}

func TestReasoningEffort(t *testing.T) {
	p := &Provider{}
	params, _ := p.buildParams("o3-mini", llm.Request{Effort: "xhigh", MaxTokens: 10})
	b, _ := json.Marshal(params)
	if !strings.Contains(string(b), `"reasoning_effort":"high"`) || !strings.Contains(string(b), `"max_completion_tokens":10`) {
		t.Fatalf("params %s", b)
	}
}
