// Package openai adapts OpenAI-compatible chat completions (OpenAI itself and
// internal gateways that speak the same protocol) to llm.Provider.
//
// The protocol is poorer than the Messages API: there is one system string,
// no cache breakpoints and no replayable reasoning, so system parts are
// concatenated and thinking blocks are dropped. Retries belong to llm.Router,
// so the SDK's own retries are disabled.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"

	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
)

// Config configures a Provider.
type Config struct {
	// Name is what the router addresses ("<name>/<model>"); defaults to "openai".
	Name string
	// APIKey falls back to OPENAI_API_KEY; may stay empty for a gateway (BaseURL set).
	APIKey string
	// BaseURL points at an OpenAI-compatible endpoint, e.g. https://gw.internal/v1.
	BaseURL string
	// Timeout bounds one streamed call; defaults to 30 minutes.
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Provider implements llm.Provider over chat completions.
type Provider struct {
	name    string
	gateway bool
	client  sdk.Client
}

// New builds a Provider.
func New(cfg Config) (*Provider, error) {
	if cfg.Name == "" {
		cfg.Name = "openai"
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	if cfg.APIKey == "" && cfg.BaseURL == "" {
		return nil, fmt.Errorf("openai: no api key (set api_key or OPENAI_API_KEY)")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Minute
	}
	// Options are built explicitly instead of from DefaultClientOptions so that
	// OPENAI_BASE_URL in the environment cannot silently redirect a provider.
	opts := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithRequestTimeout(cfg.Timeout),
	}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	return &Provider{name: cfg.Name, gateway: cfg.BaseURL != "", client: sdk.NewClient(opts...)}, nil
}

func (p *Provider) Name() string { return p.name }

// Stream sends req to model and streams content deltas to onDelta.
func (p *Provider) Stream(ctx context.Context, model string, req llm.Request, onDelta func(llm.Delta)) (*llm.Response, error) {
	params, err := p.buildParams(model, req)
	if err != nil {
		return nil, &llm.Error{Kind: llm.ErrInvalid, Err: err}
	}
	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	var (
		text, refusal strings.Builder
		finish        string
		modelName     string
		usage         sdk.CompletionUsage
		calls         = map[int64]*toolCall{}
		gotChunk      bool
	)
	for stream.Next() {
		ch := stream.Current()
		gotChunk = true
		if ch.Model != "" {
			modelName = ch.Model
		}
		// Usage arrives once, on the final chunk (include_usage). Some gateways
		// repeat a cumulative usage on every chunk, so keep the last, never sum.
		if ch.JSON.Usage.Valid() && (ch.Usage.PromptTokens > 0 || ch.Usage.CompletionTokens > 0) {
			usage = ch.Usage
		}
		for _, c := range ch.Choices {
			if c.Index != 0 {
				continue // n is never set above 1
			}
			d := c.Delta
			if d.Content != "" {
				text.WriteString(d.Content)
				if onDelta != nil {
					onDelta(llm.Delta{Text: d.Content})
				}
			}
			if d.Refusal != "" {
				refusal.WriteString(d.Refusal)
			}
			// Reasoning gateways (DeepSeek, vLLM) expose reasoning as an extra
			// field; it is shown live but not kept, since it cannot be replayed.
			if f, ok := d.JSON.ExtraFields["reasoning_content"]; ok && onDelta != nil {
				var s string
				if json.Unmarshal([]byte(f.Raw()), &s) == nil && s != "" {
					onDelta(llm.Delta{Text: s, Thinking: true})
				}
			}
			for _, tc := range d.ToolCalls {
				acc := calls[tc.Index]
				if acc == nil {
					acc = &toolCall{}
					calls[tc.Index] = acc
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				acc.name += tc.Function.Name
				acc.args.WriteString(tc.Function.Arguments)
			}
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	if !gotChunk {
		return nil, &llm.Error{Kind: llm.ErrOther, Err: errors.New("openai: empty stream")}
	}

	resp := &llm.Response{
		Model: modelName,
		Stop:  mapFinish(finish),
		Usage: types.Usage{
			// Prompt tokens include cached ones; split them so Usage means the
			// same as for Anthropic (input excludes cache reads).
			InputTokens:     usage.PromptTokens - usage.PromptTokensDetails.CachedTokens,
			OutputTokens:    usage.CompletionTokens,
			CacheReadTokens: usage.PromptTokensDetails.CachedTokens,
		},
	}
	if text.Len() > 0 {
		resp.Content = append(resp.Content, types.Text(text.String()))
	} else if refusal.Len() > 0 {
		resp.Content = append(resp.Content, types.Text(refusal.String()))
		resp.Stop = llm.StopRefusal
	}
	idx := make([]int64, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, b int) bool { return idx[a] < idx[b] })
	for _, i := range idx {
		c := calls[i]
		args := json.RawMessage(c.args.String())
		if len(args) == 0 || !json.Valid(args) {
			// A cut-off call (length) leaves broken JSON; keep the block valid
			// and let the engine see max_tokens as the stop reason.
			args = json.RawMessage(`{}`)
		}
		resp.Content = append(resp.Content, types.Block{
			Type: types.BlockToolUse, ToolUseID: c.id, Name: c.name, Input: args,
		})
	}
	// Some gateways report "stop" even when they emitted tool calls.
	if len(calls) > 0 && resp.Stop == llm.StopEndTurn {
		resp.Stop = llm.StopToolUse
	}
	return resp, nil
}

type toolCall struct {
	id, name string
	args     strings.Builder
}

func (p *Provider) buildParams(model string, req llm.Request) (sdk.ChatCompletionNewParams, error) {
	params := sdk.ChatCompletionNewParams{
		Model:         shared.ChatModel(model),
		StreamOptions: sdk.ChatCompletionStreamOptionsParam{IncludeUsage: sdk.Bool(true)},
	}
	if req.MaxTokens > 0 {
		// OpenAI deprecated max_tokens (reasoning models reject it), while many
		// gateways still only understand max_tokens.
		if p.gateway {
			params.MaxTokens = sdk.Int(int64(req.MaxTokens))
		} else {
			params.MaxCompletionTokens = sdk.Int(int64(req.MaxTokens))
		}
	}
	if e := reasoningEffort(model, req.Effort); e != "" {
		params.ReasoningEffort = e
	}
	var sys []string
	for _, s := range req.System {
		if s.Text != "" {
			sys = append(sys, s.Text)
		}
	}
	if len(sys) > 0 {
		params.Messages = append(params.Messages, sdk.SystemMessage(strings.Join(sys, "\n\n")))
	}
	for _, t := range req.Tools {
		fp := shared.FunctionParameters{"type": "object", "properties": map[string]any{}}
		if len(t.InputSchema) > 0 {
			fp = shared.FunctionParameters{}
			if err := json.Unmarshal(t.InputSchema, &fp); err != nil {
				return params, fmt.Errorf("openai: tool %s schema: %w", t.Name, err)
			}
		}
		fd := shared.FunctionDefinitionParam{Name: t.Name, Parameters: fp}
		if t.Description != "" {
			fd.Description = sdk.String(t.Description)
		}
		params.Tools = append(params.Tools, sdk.ChatCompletionToolParam{Function: fd})
	}
	for _, m := range req.Messages {
		params.Messages = append(params.Messages, convertMessage(m)...)
	}
	return params, nil
}

// convertMessage maps one neutral message to one or more chat messages: tool
// results become separate role "tool" messages, which must directly follow
// the assistant message that made the calls, so they are emitted first.
func convertMessage(m types.Message) []sdk.ChatCompletionMessageParamUnion {
	var out []sdk.ChatCompletionMessageParamUnion
	var text []string
	if m.Role == types.RoleAssistant {
		a := sdk.ChatCompletionAssistantMessageParam{}
		for _, b := range m.Content {
			switch b.Type {
			case types.BlockText:
				if b.Text != "" {
					text = append(text, b.Text)
				}
			case types.BlockToolUse:
				args := string(b.Input)
				if args == "" {
					args = "{}"
				}
				a.ToolCalls = append(a.ToolCalls, sdk.ChatCompletionMessageToolCallParam{
					ID:       b.ToolUseID,
					Function: sdk.ChatCompletionMessageToolCallFunctionParam{Name: b.Name, Arguments: args},
				})
			}
			// Thinking blocks are dropped: chat completions cannot replay them.
		}
		if len(text) == 0 && len(a.ToolCalls) == 0 {
			return nil
		}
		if len(text) > 0 {
			a.Content.OfString = param.NewOpt(strings.Join(text, "\n"))
		}
		return []sdk.ChatCompletionMessageParamUnion{{OfAssistant: &a}}
	}
	for _, b := range m.Content {
		switch b.Type {
		case types.BlockToolResult:
			content := b.Text
			if b.IsError {
				// Chat completions has no is_error flag; say it in the content.
				content = "Error: " + content
			}
			out = append(out, sdk.ToolMessage(content, b.ToolUseID))
		case types.BlockText:
			if b.Text != "" {
				text = append(text, b.Text)
			}
		}
	}
	if len(text) > 0 {
		out = append(out, sdk.UserMessage(strings.Join(text, "\n")))
	}
	return out
}

// reasoningEffort maps the neutral effort onto OpenAI's narrower scale, and
// only for reasoning models: others reject the parameter.
func reasoningEffort(model, effort string) shared.ReasoningEffort {
	if effort == "" {
		return ""
	}
	m := model
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	if !(strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") || strings.HasPrefix(m, "gpt-5")) {
		return ""
	}
	switch effort {
	case "low":
		return shared.ReasoningEffortLow
	case "medium":
		return shared.ReasoningEffortMedium
	default: // high, xhigh, max
		return shared.ReasoningEffortHigh
	}
}

func mapFinish(r string) llm.StopReason {
	switch r {
	case "tool_calls", "function_call":
		return llm.StopToolUse
	case "length":
		return llm.StopMaxTokens
	case "content_filter":
		return llm.StopRefusal
	default: // stop, or empty from a gateway that omits it
		return llm.StopEndTurn
	}
}

// classify turns SDK and transport errors into *llm.Error for the router.
func classify(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		e := &llm.Error{Kind: kindForStatus(apiErr.StatusCode), Err: err}
		if apiErr.Response != nil {
			e.RetryAfter = retryAfter(apiErr.Response.Header)
		}
		return e
	}
	// Network failures and mid-stream errors are transient.
	return &llm.Error{Kind: llm.ErrOther, Err: err}
}

func kindForStatus(code int) llm.ErrorKind {
	switch {
	case code == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case code == 529:
		return llm.ErrOverloaded
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return llm.ErrAuth
	case code == http.StatusRequestTimeout || code == http.StatusConflict:
		return llm.ErrOther
	case code >= 400 && code < 500:
		return llm.ErrInvalid
	default:
		return llm.ErrOther
	}
}

// retryAfter reads Retry-After (seconds or HTTP date), falling back to
// OpenAI's retry-after-ms.
func retryAfter(h http.Header) int {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return int(d.Seconds() + 0.999)
		}
	}
	if ms, err := strconv.ParseFloat(h.Get("Retry-After-Ms"), 64); err == nil && ms > 0 {
		return int((ms + 999) / 1000)
	}
	return 0
}
