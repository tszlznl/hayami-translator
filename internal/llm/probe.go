package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"hayami/internal/config"
)

// ProbeReport summarizes a provider connection check.
type ProbeReport struct {
	OK           bool
	Latency      time.Duration
	VisionStatus string
	Summary      string
	ErrTitle     string
	ErrHint      string
}

func formatProbeLatency(d time.Duration) string {
	if d < time.Millisecond {
		us := max(1, int(d/time.Microsecond))
		return fmt.Sprintf("%dµs", us)
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

func modelsEndpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	if strings.HasSuffix(base, "/models") {
		return base
	}
	return base + "/models"
}

// ProbeProvider verifies a provider's credentials, endpoint reachability,
// model availability, latency, and vision status without mutating config.
func ProbeProvider(ctx context.Context, cfg config.Config, p config.Provider, key string) ProbeReport {
	key = strings.TrimSpace(key)
	switch p.Kind {
	case config.ProviderOpenAI:
		baseURL := strings.TrimSpace(p.BaseURL)
		if baseURL == "" {
			return ProbeReport{
				ErrTitle: "配置不完整：请填写接口地址 Base URL",
			}
		}
		if key == "" && !config.IsLoopbackURL(baseURL) {
			return ProbeReport{
				ErrTitle: "请先填写 API Key（密钥）后再测试连接",
			}
		}
		model := strings.TrimSpace(p.Model)
		if model == "" {
			return ProbeReport{
				ErrTitle: "配置不完整：请填写模型名称",
			}
		}
		return probeOpenAI(ctx, cfg, p, key)

	case config.ProviderAzure:
		if key == "" {
			return ProbeReport{
				ErrTitle: "请先填写 Azure 订阅 Key（密钥）后再测试连接",
			}
		}
		start := time.Now()
		err := translateAzureWithSource(ctx, "自动检测", "English", p, key, "ok", func(string) {})
		lat := time.Since(start)
		if err != nil {
			return probeErrorReport(p.Kind, err, lat)
		}
		return ProbeReport{
			OK:      true,
			Latency: lat,
			Summary: fmt.Sprintf("连接测试成功 · 延迟 %s · 鉴权正常", formatProbeLatency(lat)),
		}

	case config.ProviderBaidu:
		if strings.TrimSpace(p.AppID) == "" || key == "" {
			return ProbeReport{
				ErrTitle: "请先填写百度翻译 APP ID 和密钥 Secret 后再测试连接",
			}
		}
		start := time.Now()
		err := translateBaiduWithSource(ctx, "自动检测", "English", p, key, "ok", func(string) {})
		lat := time.Since(start)
		if err != nil {
			return probeErrorReport(p.Kind, err, lat)
		}
		return ProbeReport{
			OK:      true,
			Latency: lat,
			Summary: fmt.Sprintf("连接测试成功 · 延迟 %s · 签名鉴权正常", formatProbeLatency(lat)),
		}

	default:
		return ProbeReport{
			ErrTitle: "不支持的翻译服务类型：" + p.Kind,
		}
	}
}

func probeOpenAI(ctx context.Context, cfg config.Config, p config.Provider, key string) ProbeReport {
	start := time.Now()
	model := strings.TrimSpace(p.Model)

	// 1. Optional model list check (/models) when available.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsEndpoint(p.BaseURL), nil)
	if err != nil {
		return ProbeReport{ErrTitle: "接口地址无效：" + err.Error()}
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return probeErrorReport(p.Kind, err, time.Since(start))
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	_ = resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		he := &HTTPStatusError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
		return probeErrorReport(p.Kind, he, time.Since(start))
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &list) == nil && len(list.Data) > 0 {
			found := false
			for _, item := range list.Data {
				if strings.EqualFold(strings.TrimSpace(item.ID), model) {
					found = true
					break
				}
			}
			if !found {
				lat := time.Since(start)
				return ProbeReport{
					Latency:  lat,
					ErrTitle: fmt.Sprintf("模型 %q 不存在或不可用 (404)", model),
					ErrHint:  "当前服务端 /models 列表未包含该模型，请检查模型名称。",
				}
			}
		}
	}

	// 2. Verify chat completion endpoint with a minimal probe message.
	probeCfg := cfg
	probeCfg.MaxTokens = 8
	if probeCfg.Temperature < 0 || probeCfg.Temperature > 2 {
		probeCfg.Temperature = 0.2
	}
	msgs := []Message{
		{Role: "user", Content: "ping"},
	}
	err = StreamOpenAI(ctx, probeCfg, p, key, msgs, func(string) {})
	lat := time.Since(start)
	if err != nil {
		return probeErrorReport(p.Kind, err, lat)
	}

	visionLabel := "未开启视觉（仅文本）"
	if p.Vision {
		visionLabel = "已开启视觉 (Vision)"
	}
	return ProbeReport{
		OK:           true,
		Latency:      lat,
		VisionStatus: visionLabel,
		Summary:      fmt.Sprintf("连接测试成功 · 延迟 %s · 模型 %s 可用 · %s", formatProbeLatency(lat), model, visionLabel),
	}
}

func probeErrorReport(kind string, err error, lat time.Duration) ProbeReport {
	var se *ServiceError
	if errors.As(err, &se) {
		title, hint := ExplainError(err)
		return ProbeReport{
			Latency:  lat,
			ErrTitle: fmt.Sprintf("连接测试失败：%s（错误码 %s：%s）", title, se.Code, se.Message),
			ErrHint:  hint,
		}
	}
	var he *HTTPStatusError
	if errors.As(err, &he) {
		title, hint := ExplainError(err)
		return ProbeReport{
			Latency:  lat,
			ErrTitle: fmt.Sprintf("连接测试失败：%s (HTTP %d)", title, he.Status),
			ErrHint:  hint,
		}
	}
	title, hint := ExplainError(err)
	return ProbeReport{
		Latency:  lat,
		ErrTitle: "连接测试失败：" + title,
		ErrHint:  hint,
	}
}
