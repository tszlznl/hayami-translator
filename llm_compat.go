package main

import (
	"context"

	"hayami/internal/llm"
)

// The protocol implementation lives in internal/llm. These aliases keep the
// UI and the existing tests on their local names.
type chatMessage = llm.Message
type contentPart = llm.ContentPart
type imageURLPart = llm.ImageURLPart

type httpStatusError = llm.HTTPStatusError
type serviceError = llm.ServiceError
type configError = llm.ConfigError
type probeReport = llm.ProbeReport

func explainError(err error) (string, string) { return llm.ExplainError(err) }

func probeProvider(ctx context.Context, cfg Config, p Provider, key string) probeReport {
	return llm.ProbeProvider(ctx, cfg, p, key)
}

func chatEndpoint(base string) string { return llm.ChatEndpoint(base) }

func streamChat(ctx context.Context, cfg Config, apiKey string, messages []chatMessage, onDelta func(string)) error {
	return llm.StreamChat(ctx, cfg, apiKey, messages, onDelta)
}

func streamOpenAI(ctx context.Context, cfg Config, p Provider, apiKey string, messages []chatMessage, onDelta func(string)) error {
	return llm.StreamOpenAI(ctx, cfg, p, apiKey, messages, onDelta)
}

func translateWithProvider(ctx context.Context, cfg Config, p Provider, key string, messages []chatMessage, source string, onDelta func(string)) error {
	return llm.TranslateWithProvider(ctx, cfg, p, key, messages, source, onDelta)
}

func translateWithFallback(ctx context.Context, cfg Config, providers []Provider, messages []chatMessage, text string, onProvider func(Provider), onDelta func(string)) error {
	return llm.TranslateWithFallback(ctx, cfg, providers, messages, text, onProvider, onDelta)
}
