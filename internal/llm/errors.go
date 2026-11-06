package llm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
)

// HTTPStatusError is a non-2xx reply from a translation endpoint. Keeping the
// status typed lets the UI explain the failure instead of showing the raw
// provider text.
type HTTPStatusError struct {
	Service string
	Status  int
	Message string
}

func (e *HTTPStatusError) Error() string {
	if strings.TrimSpace(e.Service) == "" {
		return fmt.Sprintf("接口返回 %d：%s", e.Status, e.Message)
	}
	return fmt.Sprintf("%s返回 %d：%s", e.Service, e.Status, e.Message)
}

// ServiceError is an error code carried inside an otherwise successful HTTP
// reply, as Baidu does.
type ServiceError struct {
	Service string
	Code    string
	Message string
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("%s错误 %s：%s", e.Service, e.Code, e.Message)
}

// ConfigError is a local configuration problem: a missing key, an empty
// source, an unknown provider kind.
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string { return e.Message }

// ExplainError turns any translation failure into a short title and one
// actionable next step.
func ExplainError(err error) (title, hint string) {
	if err == nil {
		return "", ""
	}

	var ce *ConfigError
	if errors.As(err, &ce) {
		return "配置不完整", "打开设置，检查翻译服务的地址、模型和 Key。"
	}

	var se *ServiceError
	if errors.As(err, &se) {
		switch se.Code {
		case "52003", "54001", "90107":
			return "鉴权失败", "检查百度翻译的 APP ID 和密钥。"
		case "54003", "54005":
			return "请求过于频繁", "稍后按 Ctrl+Enter 重试。"
		case "54004":
			return "账户余额不足", "检查百度翻译账户余额。"
		case "58001":
			return "不支持该语言方向", "在弹窗顶部切换目标语言。"
		case "58000":
			return "客户端 IP 非法", "检查百度翻译的 IP 白名单或网络。"
		case "58002":
			return "服务已关闭", "稍后在百度翻译控制台确认服务状态。"
		}
		return "翻译服务返回错误", "按 Ctrl+Enter 重试；反复失败时检查服务配置。"
	}

	if errors.Is(err, context.Canceled) {
		return "已取消", ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "请求超时", "检查网络后按 Ctrl+Enter 重试。"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "请求超时", "检查网络后按 Ctrl+Enter 重试。"
	}

	var he *HTTPStatusError
	if errors.As(err, &he) {
		switch {
		case he.Status == 401 || he.Status == 403:
			return "鉴权失败：请检查 API Key", "服务拒绝了凭据。打开设置确认 Key 是否正确、是否过期。"
		case he.Status == 404:
			return "接口地址或模型不存在", "检查 Base URL 是否以 /v1 结尾，以及模型名是否可用。"
		case he.Status == 429:
			return "请求过于频繁或额度不足", "稍后按 Ctrl+Enter 重试，或检查账户额度。"
		case he.Status == 400 || he.Status == 413 || he.Status == 422:
			if looksTooLong(he.Message) {
				return "文本过长", "缩短选中文本后重试，或分段翻译。"
			}
			return "请求被服务拒绝", "检查 Base URL、模型名和请求参数。" + detail(he.Message)
		case he.Status >= 500:
			return "翻译服务暂时不可用", "稍后按 Ctrl+Enter 重试。"
		}
		return "翻译服务返回异常状态", "按 Ctrl+Enter 重试；反复失败时检查设置里的翻译服务。"
	}

	if isTLSFailure(err) {
		return "安全连接失败", "检查网络、代理或系统时间后按 Ctrl+Enter 重试。"
	}
	if isOffline(err) {
		return "网络连接失败", "检查网络或代理后按 Ctrl+Enter 重试。"
	}

	return "翻译失败", "按 Ctrl+Enter 重试；反复失败时检查设置里的翻译服务。"
}

func looksTooLong(message string) bool {
	m := strings.ToLower(message)
	for _, word := range []string{
		"maximum context", "context length", "context_length", "too long",
		"too many tokens", "token limit", "payload too large", "length limit",
		"文本过长", "内容过长", "超过长度",
	} {
		if strings.Contains(m, word) {
			return true
		}
	}
	return false
}

func detail(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	runes := []rune(message)
	if len(runes) > 80 {
		return "详情：" + string(runes[:80]) + "…"
	}
	return "详情：" + message
}

func isTLSFailure(err error) bool {
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return true
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		return true
	}
	var record tls.RecordHeaderError
	if errors.As(err, &record) {
		return true
	}
	text := err.Error()
	return strings.Contains(text, "x509:") || strings.Contains(text, "tls: failed to verify")
}

func isOffline(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, word := range []string{"connection refused", "no such host", "network is unreachable", "connection reset"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}
