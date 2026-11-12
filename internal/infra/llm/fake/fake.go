// Package fake is a scripted model provider for tests and local runs without
// API keys. A script is a function of the request, so tests can react to tool
// results; Steps replays fixed responses in order.
package fake

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
)

// Script decides the reply to a request.
type Script func(req llm.Request) (*llm.Response, error)

// Provider is a fake llm.Provider named "fake".
type Provider struct {
	mu       sync.Mutex
	script   Script
	Requests []llm.Request
}

func New(s Script) *Provider { return &Provider{script: s} }

func (p *Provider) Name() string { return "fake" }

// SetScript swaps the script.
func (p *Provider) SetScript(s Script) {
	p.mu.Lock()
	p.script = s
	p.mu.Unlock()
}

// Calls returns how many requests were made.
func (p *Provider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.Requests)
}

func (p *Provider) Stream(ctx context.Context, _ string, req llm.Request, onDelta func(llm.Delta)) (*llm.Response, error) {
	p.mu.Lock()
	p.Requests = append(p.Requests, req)
	s := p.script
	p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		s = Echo
	}
	resp, err := s(req)
	if err != nil {
		return nil, err
	}
	for _, b := range resp.Content {
		if b.Type == types.BlockText && onDelta != nil {
			for _, w := range strings.SplitAfter(b.Text, " ") {
				onDelta(llm.Delta{Text: w})
			}
		}
	}
	if resp.Usage.InputTokens == 0 {
		resp.Usage.InputTokens = int64(approxTokens(req))
		resp.Usage.OutputTokens = 10
	}
	if resp.Stop == "" {
		resp.Stop = llm.StopEndTurn
		for _, b := range resp.Content {
			if b.Type == types.BlockToolUse {
				resp.Stop = llm.StopToolUse
			}
		}
	}
	return resp, nil
}

func approxTokens(req llm.Request) int {
	n := 0
	for _, s := range req.System {
		n += len(s.Text) / 4
	}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			n += (len(b.Text) + len(b.Input)) / 4
		}
	}
	return n + 1
}

// Echo replies with the last user text.
func Echo(req llm.Request) (*llm.Response, error) {
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].PlainText()
	}
	return TextReply("echo: " + last), nil
}

// TextReply builds a final text response.
func TextReply(s string) *llm.Response {
	return &llm.Response{Content: []types.Block{types.Text(s)}, Stop: llm.StopEndTurn}
}

// ToolCall builds a response requesting one tool call.
func ToolCall(useID, name string, input any) *llm.Response {
	raw, _ := json.Marshal(input)
	return &llm.Response{Content: []types.Block{{Type: types.BlockToolUse, ToolUseID: useID, Name: name, Input: raw}}, Stop: llm.StopToolUse}
}

// Steps replays responses in order, then repeats the last one.
func Steps(rs ...*llm.Response) Script {
	var mu sync.Mutex
	i := 0
	return func(llm.Request) (*llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		r := rs[min(i, len(rs)-1)]
		i++
		cp := *r
		cp.Content = append([]types.Block(nil), r.Content...)
		return &cp, nil
	}
}

// LastToolResult returns the newest tool_result block in req, if any.
func LastToolResult(req llm.Request) (types.Block, bool) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		for _, b := range req.Messages[i].Content {
			if b.Type == types.BlockToolResult {
				return b, true
			}
		}
	}
	return types.Block{}, false
}
