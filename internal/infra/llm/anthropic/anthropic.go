// Package anthropic adapts the Claude Messages API to llm.Provider.
//
// The adapter always streams: long agent turns with large max_tokens would
// otherwise hit HTTP timeouts. Retries are left to llm.Router (the SDK's own
// retries are disabled) so that concurrency slots, Retry-After handling and
// fallback stay in one place (§10).
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/llm"
)

// ProviderTag marks thinking blocks produced by this adapter; only those are
// replayed, because a block's signature is meaningless to other providers.
const ProviderTag = "anthropic"

// DefaultMaxTokens is used when the request leaves MaxTokens unset. Streaming
// removes the timeout concern, so give the model room rather than truncate.
const DefaultMaxTokens = 64000

// Config configures a Provider.
type Config struct {
	// Name is what the router addresses ("anthropic/<model>"); defaults to "anthropic".
	Name string
	// APIKey falls back to ANTHROPIC_API_KEY.
	APIKey string
	// BaseURL overrides the API endpoint (internal gateway, test server).
	BaseURL string
	// Timeout bounds one streamed call; defaults to 30 minutes because a single
	// high-effort turn can legitimately run for many minutes.
	Timeout time.Duration
	// HTTPClient is optional (tests, custom transports).
	HTTPClient *http.Client
}

// Provider implements llm.Provider on the official SDK.
type Provider struct {
	name   string
	client sdk.Client
}

// New builds a Provider.
func New(cfg Config) (*Provider, error) {
	if cfg.Name == "" {
		cfg.Name = "anthropic"
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("anthropic: no api key (set api_key or ANTHROPIC_API_KEY)")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Minute
	}
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(0),
		option.WithRequestTimeout(cfg.Timeout),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	return &Provider{name: cfg.Name, client: sdk.NewClient(opts...)}, nil
}

func (p *Provider) Name() string { return p.name }

// Stream sends req to model and streams text and thinking deltas to onDelta.
func (p *Provider) Stream(ctx context.Context, model string, req llm.Request, onDelta func(llm.Delta)) (*llm.Response, error) {
	params, err := buildParams(model, req)
	if err != nil {
		return nil, &llm.Error{Kind: llm.ErrInvalid, Err: err}
	}
	stream := p.client.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	var msg sdk.Message
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, &llm.Error{Kind: llm.ErrOther, Err: fmt.Errorf("anthropic: accumulate stream: %w", err)}
		}
		if onDelta == nil || ev.Type != "content_block_delta" {
			continue
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				onDelta(llm.Delta{Text: ev.Delta.Text})
			}
		case "thinking_delta":
			if ev.Delta.Thinking != "" {
				onDelta(llm.Delta{Text: ev.Delta.Thinking, Thinking: true})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	if msg.Type == "" {
		// The stream ended without message_start: a gateway that closed early.
		return nil, &llm.Error{Kind: llm.ErrOther, Err: errors.New("anthropic: empty stream")}
	}
	return convertMessage(msg), nil
}

// buildParams maps the provider-neutral request onto the Messages API.
func buildParams(model string, req llm.Request) (sdk.MessageNewParams, error) {
	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	params := sdk.MessageNewParams{
		Model:     sdk.Model(model),
		MaxTokens: maxTokens,
	}
	// System parts keep their order; a cache breakpoint goes on each part that
	// ends a stable prefix (§10.3). The API allows at most 4 breakpoints, so
	// only the last 4 marked parts keep theirs (later prefixes subsume earlier ones).
	marked := 0
	for i := len(req.System) - 1; i >= 0; i-- {
		if req.System[i].Cache {
			marked++
		}
	}
	skip := marked - 4
	for _, s := range req.System {
		if s.Text == "" {
			continue // the API rejects empty text blocks
		}
		tb := sdk.TextBlockParam{Text: s.Text}
		if s.Cache {
			if skip > 0 {
				skip--
			} else {
				tb.CacheControl = sdk.NewCacheControlEphemeralParam()
			}
		}
		params.System = append(params.System, tb)
	}
	for _, t := range req.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		tp := sdk.ToolParam{
			Name: t.Name,
			// The schema is already JSON; override sends it verbatim instead of
			// round-tripping it through the SDK's typed fields.
			InputSchema: param.Override[sdk.ToolInputSchemaParam](schema),
		}
		if t.Description != "" {
			tp.Description = sdk.String(t.Description)
		}
		params.Tools = append(params.Tools, sdk.ToolUnionParam{OfTool: &tp})
	}
	for _, m := range req.Messages {
		mp, ok, err := convertMessageParam(m)
		if err != nil {
			return params, err
		}
		if ok {
			params.Messages = append(params.Messages, mp)
		}
	}
	if !legacyThinking(model) {
		// Adaptive thinking is the only mode current models accept (budget_tokens
		// and "disabled" are 400s on Opus 5.5). Summarized display keeps the
		// thinking stream visible instead of a silent pause.
		params.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{
			Display: sdk.ThinkingConfigAdaptiveDisplaySummarized,
		}}
	}
	if req.Effort != "" {
		params.OutputConfig = sdk.OutputConfigParam{Effort: sdk.OutputConfigEffort(req.Effort)}
	}
	return params, nil
}

// legacyThinking reports models that predate adaptive thinking; for them the
// thinking parameter is omitted rather than risking a 400.
func legacyThinking(model string) bool {
	for _, p := range []string{"claude-3", "claude-haiku-4-5", "claude-sonnet-4-5", "claude-opus-4-5", "claude-opus-4-1", "claude-opus-4-0", "claude-sonnet-4-0", "claude-opus-4-2", "claude-sonnet-4-2"} {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

func convertMessageParam(m types.Message) (sdk.MessageParam, bool, error) {
	role := sdk.MessageParamRoleUser
	if m.Role == types.RoleAssistant {
		role = sdk.MessageParamRoleAssistant
	}
	mp := sdk.MessageParam{Role: role}
	for _, b := range m.Content {
		switch b.Type {
		case types.BlockText:
			if b.Text == "" {
				continue
			}
			mp.Content = append(mp.Content, sdk.NewTextBlock(b.Text))
		case types.BlockToolUse:
			input := b.Input
			if len(input) == 0 || !json.Valid(input) {
				input = json.RawMessage(`{}`)
			}
			mp.Content = append(mp.Content, sdk.ContentBlockParamUnion{OfToolUse: &sdk.ToolUseBlockParam{
				ID: b.ToolUseID, Name: b.Name, Input: input,
			}})
		case types.BlockToolResult:
			tr := sdk.ToolResultBlockParam{ToolUseID: b.ToolUseID}
			if b.IsError {
				tr.IsError = sdk.Bool(true)
			}
			if b.Text != "" {
				tr.Content = []sdk.ToolResultBlockParamContentUnion{{OfText: &sdk.TextBlockParam{Text: b.Text}}}
			}
			mp.Content = append(mp.Content, sdk.ContentBlockParamUnion{OfToolResult: &tr})
		case types.BlockThinking:
			// Only our own blocks carry a signature the API can verify; others
			// (or blocks without Raw) are dropped.
			if b.Provider != ProviderTag || len(b.Raw) == 0 {
				continue
			}
			var head struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(b.Raw, &head); err != nil {
				return mp, false, fmt.Errorf("anthropic: bad thinking block: %w", err)
			}
			// Override sends the stored block verbatim: thinking blocks must be
			// passed back unchanged or the API rejects them.
			switch head.Type {
			case "redacted_thinking":
				v := param.Override[sdk.RedactedThinkingBlockParam](b.Raw)
				mp.Content = append(mp.Content, sdk.ContentBlockParamUnion{OfRedactedThinking: &v})
			default:
				v := param.Override[sdk.ThinkingBlockParam](b.Raw)
				mp.Content = append(mp.Content, sdk.ContentBlockParamUnion{OfThinking: &v})
			}
		}
	}
	if len(mp.Content) == 0 {
		return mp, false, nil
	}
	return mp, true, nil
}

func convertMessage(msg sdk.Message) *llm.Response {
	resp := &llm.Response{
		Model: string(msg.Model),
		Stop:  mapStop(msg.StopReason),
		Usage: types.Usage{
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		},
	}
	for _, c := range msg.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				resp.Content = append(resp.Content, types.Text(c.Text))
			}
		case "tool_use":
			input := json.RawMessage(c.Input)
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			resp.Content = append(resp.Content, types.Block{
				Type: types.BlockToolUse, ToolUseID: c.ID, Name: c.Name, Input: input,
			})
		case "thinking", "redacted_thinking":
			resp.Content = append(resp.Content, types.Block{
				Type:     types.BlockThinking,
				Text:     c.Thinking,
				Raw:      json.RawMessage(c.RawJSON()),
				Provider: ProviderTag,
			})
		}
	}
	return resp
}

func mapStop(r sdk.StopReason) llm.StopReason {
	switch r {
	case sdk.StopReasonToolUse:
		return llm.StopToolUse
	case sdk.StopReasonMaxTokens, sdk.StopReasonModelContextWindowExceeded:
		return llm.StopMaxTokens
	case sdk.StopReasonRefusal:
		return llm.StopRefusal
	case sdk.StopReasonPauseTurn:
		return llm.StopPause
	default: // end_turn, stop_sequence
		return llm.StopEndTurn
	}
}

// classify turns SDK and transport errors into *llm.Error for the router.
func classify(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		// Cancellation is not a provider failure; keep it recognisable.
		return ctx.Err()
	}
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		e := &llm.Error{Kind: llm.ErrOther, Err: err}
		// A mid-stream SSE error arrives with the 200 status of the stream, so
		// the error type is checked before the status code.
		switch apiErr.Type() {
		case sdk.ErrorTypeOverloadedError:
			e.Kind = llm.ErrOverloaded
		case sdk.ErrorTypeRateLimitError:
			e.Kind = llm.ErrRateLimited
		case sdk.ErrorTypeAuthenticationError, sdk.ErrorTypePermissionError:
			e.Kind = llm.ErrAuth
		case sdk.ErrorTypeInvalidRequestError, sdk.ErrorTypeNotFoundError, sdk.ErrorTypeBillingError:
			e.Kind = llm.ErrInvalid
		case sdk.ErrorTypeAPIError, sdk.ErrorTypeTimeoutError:
			e.Kind = llm.ErrOther
		default:
			e.Kind = kindForStatus(apiErr.StatusCode)
		}
		if apiErr.Response != nil {
			e.RetryAfter = retryAfter(apiErr.Response.Header.Get("Retry-After"))
		}
		return e
	}
	// Network failures and truncated streams are transient.
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
		return llm.ErrOther // transient by API contract
	case code >= 400 && code < 500:
		return llm.ErrInvalid
	default:
		return llm.ErrOther
	}
}

// retryAfter parses Retry-After as seconds or an HTTP date.
func retryAfter(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return int(d.Seconds() + 0.999)
		}
	}
	return 0
}
