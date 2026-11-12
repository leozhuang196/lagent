package app

import (
	"github.com/eternityx/lagent/internal/infra/llm"
	"github.com/eternityx/lagent/internal/infra/llm/anthropic"
	"github.com/eternityx/lagent/internal/infra/llm/openai"
)

func init() {
	llmDrivers["anthropic"] = newAnthropicProvider
	llmDrivers["openai"] = newOpenAIProvider
}

func newAnthropicProvider(name string, p LLMProvider) (llm.Provider, error) {
	return anthropic.New(anthropic.Config{Name: name, APIKey: p.APIKey, BaseURL: p.BaseURL})
}

func newOpenAIProvider(name string, p LLMProvider) (llm.Provider, error) {
	return openai.New(openai.Config{Name: name, APIKey: p.APIKey, BaseURL: p.BaseURL})
}

// llmDriverFor resolves a configured provider name to a driver. Names without
// a registered driver but with a base_url are treated as OpenAI-compatible
// gateways (e.g. "qwen", "internal-gw"), registered under their own name so
// the router can address them as "<name>/<model>".
func llmDriverFor(name string, p LLMProvider) (func(string, LLMProvider) (llm.Provider, error), bool) {
	if f, ok := llmDrivers[name]; ok {
		return f, true
	}
	if p.BaseURL != "" {
		return newOpenAIProvider, true
	}
	return nil, false
}
