package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestExplainError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantTitle string
		wantHint  string
	}{
		{
			name:      "timeout",
			err:       context.DeadlineExceeded,
			wantTitle: "请求超时",
			wantHint:  "检查网络后按 Ctrl+Enter 重试。",
		},
		{
			name:      "unauthorized",
			err:       &httpStatusError{Status: 401, Message: "bad key"},
			wantTitle: "鉴权失败：请检查 API Key",
			wantHint:  "服务拒绝了凭据。打开设置确认 Key 是否正确、是否过期。",
		},
		{
			name:      "forbidden",
			err:       &httpStatusError{Status: 403, Message: "forbidden"},
			wantTitle: "鉴权失败：请检查 API Key",
			wantHint:  "服务拒绝了凭据。打开设置确认 Key 是否正确、是否过期。",
		},
		{
			name:      "not found",
			err:       &httpStatusError{Status: 404, Message: "model not found"},
			wantTitle: "接口地址或模型不存在",
			wantHint:  "检查 Base URL 是否以 /v1 结尾，以及模型名是否可用。",
		},
		{
			name:      "rate limited",
			err:       &httpStatusError{Status: 429, Message: "too many requests"},
			wantTitle: "请求过于频繁或额度不足",
			wantHint:  "稍后按 Ctrl+Enter 重试，或检查账户额度。",
		},
		{
			name:      "too long",
			err:       &httpStatusError{Status: 400, Message: "This model's maximum context length is 8192 tokens"},
			wantTitle: "文本过长",
			wantHint:  "缩短选中文本后重试，或分段翻译。",
		},
		{
			name:      "server error",
			err:       &httpStatusError{Status: 503, Message: "unavailable"},
			wantTitle: "翻译服务暂时不可用",
			wantHint:  "稍后按 Ctrl+Enter 重试。",
		},
		{
			name:      "offline",
			err:       &url.Error{Op: "Post", URL: "https://example.com", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}},
			wantTitle: "网络连接失败",
			wantHint:  "检查网络或代理后按 Ctrl+Enter 重试。",
		},
		{
			name:      "tls",
			err:       &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
			wantTitle: "安全连接失败",
			wantHint:  "检查网络、代理或系统时间后按 Ctrl+Enter 重试。",
		},
		{
			name:      "config",
			err:       &configError{Message: "还没有填写 API Key"},
			wantTitle: "配置不完整",
			wantHint:  "打开设置，检查翻译服务的地址、模型和 Key。",
		},
		{
			name:      "baidu quota",
			err:       &serviceError{Service: "百度翻译", Code: "54004", Message: "余额不足"},
			wantTitle: "账户余额不足",
			wantHint:  "检查百度翻译账户余额。",
		},
		{
			name:      "unknown",
			err:       errors.New("something odd"),
			wantTitle: "翻译失败",
			wantHint:  "按 Ctrl+Enter 重试；反复失败时检查设置里的翻译服务。",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, hint := explainError(tc.err)
			if title != tc.wantTitle || hint != tc.wantHint {
				t.Fatalf("explainError() = (%q, %q), want (%q, %q)", title, hint, tc.wantTitle, tc.wantHint)
			}
		})
	}
}

func TestExplainErrorNil(t *testing.T) {
	title, hint := explainError(nil)
	if title != "" || hint != "" {
		t.Fatalf("explainError(nil) = (%q, %q), want empty", title, hint)
	}
}

func TestHTTPStatusErrorKeepsLegacyFormat(t *testing.T) {
	err := &httpStatusError{Status: 401, Message: "bad key"}
	if got := err.Error(); !strings.Contains(got, "401") || !strings.Contains(got, "bad key") {
		t.Fatalf("error text = %q", got)
	}
	err = &httpStatusError{Service: "Azure 翻译", Status: 400, Message: "bad request"}
	if got := err.Error(); !strings.Contains(got, "Azure 翻译返回 400") {
		t.Fatalf("error text = %q", got)
	}
}
