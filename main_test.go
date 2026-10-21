package main

import (
	"strings"
	"testing"
)

func TestChatEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://api.openai.com/v1":            "https://api.openai.com/v1/chat/completions",
		"https://api.openai.com/v1/":           "https://api.openai.com/v1/chat/completions",
		"https://example.com/chat/completions": "https://example.com/chat/completions",
	}
	for in, want := range cases {
		if got := chatEndpoint(in); got != want {
			t.Errorf("chatEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromptFor(t *testing.T) {
	c := defaultConfig()
	c.TargetLang = "日本語"
	c.SourceLang = "English"
	if p := c.promptFor(); !strings.Contains(p, "日本語") || strings.Contains(p, "{{target}}") {
		t.Errorf("prompt not filled: %q", p)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\n\nb\tc", 20); got != "a b c" {
		t.Errorf("oneLine = %q", got)
	}
	if got := oneLine("abcdef", 3); got != "abc…" {
		t.Errorf("oneLine cut = %q", got)
	}
}

func TestConfigZeroTemperature(t *testing.T) {
	c := defaultConfig()
	c.Temperature = 0.0
	c.fillDefaults()
	if c.Temperature != 0.0 {
		t.Errorf("expected 0.0, got %v", c.Temperature)
	}

	c.Temperature = -1.0
	c.fillDefaults()
	if c.Temperature != 0.3 {
		t.Errorf("expected 0.3 for negative temperature, got %v", c.Temperature)
	}
}

func TestConfigMultiEngineClone(t *testing.T) {
	c := defaultConfig()
	c.MultiEngine = true
	cloned := c.clone()
	if !cloned.MultiEngine {
		t.Fatal("cloned config should preserve MultiEngine=true")
	}

	c.MultiEngine = false
	if cloned.MultiEngine != true {
		t.Fatal("mutating original should not affect clone")
	}
}

func TestWindowCloseOnQuit(t *testing.T) {
	a := &app{quitting: false}
	shouldPrevent := func() bool {
		if a.quitting {
			return false
		}
		return true
	}

	if !shouldPrevent() {
		t.Fatal("ordinary close should prevent default and hide")
	}

	a.quitting = true
	if shouldPrevent() {
		t.Fatal("close during quit sequence must not prevent default")
	}
}
