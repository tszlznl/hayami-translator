package lang

import "testing"

func TestIsChineseSourceMixedTechnicalText(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"用 Kubernetes 部署 Go microservice 服务的最佳实践", true},
		{"这是 API 使用文档", true},
		{"如何在 PostgreSQL 中优化 JSONB 索引", true},
		{"The OpenAI API 文档", false},
		{"Kubernetes cluster deployment guide", false},
		{"こんにちは世界", false},
		{"한국어 漢字 테스트", false},
		{"12345 !@#", false},
	}
	for _, tc := range cases {
		if got := IsChineseSource(tc.text); got != tc.want {
			t.Errorf("IsChineseSource(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestDetectNameScripts(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"你好，世界", "中文"},
		{"こんにちは", "日本語"},
		{"안녕하세요", "한국어"},
		{"Hello, world!", "English"},
		{"Привет, мир", "Русский"},
		{"مرحبا بالعالم", "العربية"},
		{"สวัสดีชาวโลก", "ไทย"},
		// Accented Latin languages should not be mislabeled as English.
		{"Bonjour, où est le café ?", "自动检测"},
		{"Überraschung und Grüße", "自动检测"},
		{"12345", "自动检测"},
	}
	for _, tc := range cases {
		if got := DetectName(tc.text); got != tc.want {
			t.Errorf("DetectName(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestEffectiveTargetCompatibility(t *testing.T) {
	if got := EffectiveTarget("自动检测", "你好"); got != "English" {
		t.Fatalf("EffectiveTarget(自动检测, 中文) = %q, want English", got)
	}
	if got := EffectiveTarget("自动检测", "Hello"); got != "简体中文" {
		t.Fatalf("EffectiveTarget(自动检测, English) = %q, want 简体中文", got)
	}
}
