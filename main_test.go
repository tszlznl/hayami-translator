package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo"
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
	if p := c.PromptFor(); !strings.Contains(p, "日本語") || strings.Contains(p, "{{target}}") {
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
	c.FillDefaults()
	if c.Temperature != 0.0 {
		t.Errorf("expected 0.0, got %v", c.Temperature)
	}

	c.Temperature = -1.0
	c.FillDefaults()
	if c.Temperature != 0.3 {
		t.Errorf("expected 0.3 for negative temperature, got %v", c.Temperature)
	}
}

func TestConfigMultiEngineClone(t *testing.T) {
	c := defaultConfig()
	c.MultiEngine = true
	cloned := c.Clone()
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
	hidden := false
	hide := func() { hidden = true }

	e1 := &mygo.CloseEvent{}
	a.handleWindowClose(e1, hide)
	if !e1.DefaultPrevented() {
		t.Fatal("ordinary close must prevent default")
	}
	if !hidden {
		t.Fatal("ordinary close must invoke hide")
	}

	a.quitting = true
	hidden = false
	e2 := &mygo.CloseEvent{}
	a.handleWindowClose(e2, hide)
	if e2.DefaultPrevented() {
		t.Fatal("close during quit sequence must not prevent default")
	}
	if hidden {
		t.Fatal("close during quit sequence must not invoke hide")
	}
}

func TestParseCLIAction(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{}, ""},
		{[]string{"--unknown"}, ""},
		{[]string{"-s"}, "selection"},
		{[]string{"--selection"}, "selection"},
		{[]string{"selection"}, "selection"},
		{[]string{"-i"}, "input"},
		{[]string{"--input"}, "input"},
		{[]string{"input"}, "input"},
		{[]string{"-c"}, "screenshot"},
		{[]string{"--screenshot"}, "screenshot"},
		{[]string{"shot"}, "screenshot"},
		{[]string{"-p"}, "settings"},
		{[]string{"--settings"}, "settings"},
		{[]string{"-t"}, "toggle"},
		{[]string{"--toggle"}, "toggle"},
		{[]string{"-b"}, "silent"},
		{[]string{"--silent"}, "silent"},
		{[]string{"--background"}, "silent"},
		{[]string{"--autostart"}, "silent"},
		{[]string{"--minimized"}, "silent"},
		{[]string{"--foo", "-s", "--bar"}, "selection"},
	}
	for _, tc := range cases {
		if got := parseCLIAction(tc.args); got != tc.want {
			t.Errorf("parseCLIAction(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
