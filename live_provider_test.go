package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestLiveOpenAIProvider is an opt-in check against a real OpenAI-compatible
// gateway. It is skipped by default so the normal test suite stays offline.
func TestLiveOpenAIProvider(t *testing.T) {
	if os.Getenv("NEXTT_LIVE") == "" {
		t.Skip("set NEXTT_LIVE=1 to call the live gateway")
	}
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "http://127.0.0.1:3425/v1"
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("set OPENAI_API_KEY to call the live gateway")
	}
	cfg := defaultConfig()
	cfg.BaseURL = base
	cfg.Model = "auto/deepseek-v4.1-flash"
	cfg.Providers = []Provider{{
		ID:      "openai",
		Name:    "OpenAI Compatible",
		Kind:    providerOpenAI,
		Enabled: true,
		BaseURL: base,
		Model:   cfg.Model,
		Vision:  true,
	}}
	p := cfg.Providers[0]
	var out strings.Builder
	err := translateWithProvider(context.Background(), cfg, p, key, textMessages(cfg, "Hello"), "Hello", func(s string) {
		out.WriteString(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("empty translation")
	}
	t.Logf("provider=%s result=%q", providerLabel(p), out.String())
}
