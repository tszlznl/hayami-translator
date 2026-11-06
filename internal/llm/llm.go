// Package llm speaks the OpenAI Compatible, Azure Translator and Baidu
// translation protocols. It depends on config for the service model but has
// no UI dependency.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"hayami/internal/config"
)

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ImageURLPart `json:"image_url,omitempty"`
}

type ImageURLPart struct {
	URL string `json:"url"`
}

var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
		ForceAttemptHTTP2:     true,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("重定向次数过多")
		}
		// Do not forward sensitive authorization headers across domains.
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
			req.Header.Del("Ocp-Apim-Subscription-Key")
		}
		return nil
	},
}

func ChatEndpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// StreamChat is the single-provider entry point kept for tests and older
// callers. New code uses StreamOpenAI with an explicit provider.
func StreamChat(ctx context.Context, cfg config.Config, apiKey string, messages []Message, onDelta func(string)) error {
	return StreamOpenAI(ctx, cfg, config.Provider{
		Kind:    config.ProviderOpenAI,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	}, apiKey, messages, onDelta)
}

// StreamOpenAI asks an OpenAI-compatible endpoint for a streamed reply and
// calls onDelta for every token. It returns when the stream ends, the context
// is canceled, or the endpoint fails.
func StreamOpenAI(ctx context.Context, cfg config.Config, p config.Provider, apiKey string, messages []Message, onDelta func(string)) error {
	if strings.TrimSpace(apiKey) == "" {
		return &ConfigError{Message: "还没有填写 API Key，请在设置里填写"}
	}
	body := map[string]any{
		"model":       p.Model,
		"messages":    messages,
		"stream":      true,
		"temperature": cfg.Temperature,
		"max_tokens":  cfg.MaxTokens,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ChatEndpoint(p.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return &HTTPStatusError{Status: resp.StatusCode, Message: msg}
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "event-stream") {
		// A few gateways ignore stream:true and answer with one JSON body.
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return err
		}
		var once struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(data, &once); err != nil {
			return fmt.Errorf("无法解析接口返回：%w", err)
		}
		if len(once.Choices) == 0 {
			return fmt.Errorf("接口没有返回内容")
		}
		onDelta(once.Choices[0].Message.Content)
		return nil
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var seenDone bool
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			seenDone = true
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // keep-alives and vendor-specific lines
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return fmt.Errorf("%s", chunk.Error.Message)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				onDelta(choice.Delta.Content)
			} else if choice.Message.Content != "" {
				onDelta(choice.Message.Content)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" && *choice.FinishReason != "null" {
				seenDone = true
			}
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !seenDone {
		return fmt.Errorf("流式传输异常中断：未收到结束标记")
	}
	return nil
}
