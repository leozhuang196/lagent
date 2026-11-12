// Package llm is the model gateway. Adapters (anthropic, openai, fake) live in
// sub-packages; Router adds global concurrency limits, retries and fallback.
package llm

import (
	"context"
	"errors"
	"strings"

	"github.com/eternityx/lagent/internal/core/types"
)

// SystemPart is one ordered piece of the system prompt. Stable parts come
// first; Cache marks the end of a prefix worth caching (§10.3).
type SystemPart struct {
	Text  string `json:"text"`
	Cache bool   `json:"cache,omitempty"`
}

// Request is provider-neutral.
type Request struct {
	Model     string           `json:"model"` // "provider/model", e.g. "anthropic/claude-opus-5-5"
	System    []SystemPart     `json:"system"`
	Tools     []types.ToolSpec `json:"tools,omitempty"`
	Messages  []types.Message  `json:"messages"`
	MaxTokens int              `json:"max_tokens"`
	Effort    string           `json:"effort,omitempty"`
}

// StopReason is why the model stopped.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopPause     StopReason = "pause_turn"
)

// Response is a complete assistant reply.
type Response struct {
	Model   string        `json:"model"`
	Content []types.Block `json:"content"`
	Stop    StopReason    `json:"stop"`
	Usage   types.Usage   `json:"usage"`
}

// Delta is an incremental piece of streamed output.
type Delta struct {
	Text     string
	Thinking bool
}

// Provider is implemented by adapters.
type Provider interface {
	Name() string
	Stream(ctx context.Context, model string, req Request, onDelta func(Delta)) (*Response, error)
}

// Gateway is what the engine uses.
type Gateway interface {
	Stream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error)
}

// ErrorKind classifies provider errors for the router.
type ErrorKind string

const (
	ErrRateLimited ErrorKind = "rate_limited"
	ErrOverloaded  ErrorKind = "overloaded"
	ErrInvalid     ErrorKind = "invalid"
	ErrAuth        ErrorKind = "auth"
	ErrOther       ErrorKind = "other"
)

// Error is a classified provider error.
type Error struct {
	Kind       ErrorKind
	RetryAfter int // seconds, 0 if unknown
	Err        error
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Retryable reports whether err is worth retrying or falling back on.
func Retryable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind == ErrRateLimited || e.Kind == ErrOverloaded || e.Kind == ErrOther
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// SplitModel splits "provider/model".
func SplitModel(m string) (provider, model string) {
	if i := strings.IndexByte(m, '/'); i > 0 {
		return m[:i], m[i+1:]
	}
	return "", m
}
