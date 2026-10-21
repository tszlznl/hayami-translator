package main

import (
	"strings"
	"testing"
)

func TestEffectiveTarget(t *testing.T) {
	cases := []struct {
		name   string
		target string
		source string
		want   string
	}{
		{name: "chinese to english", target: autoTarget, source: "你好，世界", want: "English"},
		{name: "english to chinese", target: autoTarget, source: "Hello, world", want: "简体中文"},
		{name: "japanese to chinese", target: autoTarget, source: "こんにちは世界", want: "简体中文"},
		{name: "korean to chinese", target: autoTarget, source: "안녕하세요", want: "简体中文"},
		{name: "manual target wins", target: "日本語", source: "你好", want: "日本語"},
		{name: "mixed chinese", target: autoTarget, source: "这是 API 使用文档", want: "English"},
		{name: "mostly english", target: autoTarget, source: "The OpenAI API 文档", want: "简体中文"},
		{name: "no letters", target: autoTarget, source: "12345", want: "简体中文"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveTarget(tc.target, tc.source); got != tc.want {
				t.Fatalf("effectiveTarget(%q, %q) = %q, want %q", tc.target, tc.source, got, tc.want)
			}
		})
	}
}

func TestDefaultTargetIsAutomatic(t *testing.T) {
	if got := defaultConfig().TargetLang; got != autoTarget {
		t.Fatalf("default target = %q, want %q", got, autoTarget)
	}
}

func TestImageMessagesAutomaticDirection(t *testing.T) {
	cfg := defaultConfig()
	msgs := imageMessages(cfg, []byte("png"))
	parts, ok := msgs[1].Content.([]any)
	if !ok || len(parts) == 0 {
		t.Fatalf("content %#v", msgs[1].Content)
	}
	text, ok := parts[0].(contentPart)
	if !ok || !strings.Contains(text.Text, "English") || !strings.Contains(text.Text, "简体中文") {
		t.Fatalf("instruction %#v", parts[0])
	}
	if strings.Contains(text.Text, autoTarget) {
		t.Fatalf("automatic target leaked into instruction: %q", text.Text)
	}
}
