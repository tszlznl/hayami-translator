package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type contentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *imageURLPart `json:"image_url,omitempty"`
}

type imageURLPart struct {
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
}

func chatEndpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

func textMessages(cfg Config, source string) []chatMessage {
	return []chatMessage{
		{Role: "system", Content: cfg.promptFor()},
		{Role: "user", Content: source},
	}
}

func imageMessages(cfg Config, png []byte) []chatMessage {
	instruction := "识别图片中的文字，并翻译成" + cfg.TargetLang + "。只输出译文，不要描述排版，不要解释；保留段落和列表结构。如果图片里没有可翻译的文字，就用" + cfg.TargetLang + "简短说明图片内容。"
	if cfg.TargetLang == autoTarget {
		instruction = "识别图片中的文字。如果图片里的文字主要是中文，就翻译成English；否则翻译成简体中文。只输出译文，不要描述排版，不要解释；保留段落和列表结构。如果图片里没有可翻译的文字，就用简体中文简短说明图片内容。"
	}
	part := contentPart{
		Type: "image_url",
		ImageURL: &imageURLPart{
			URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		},
	}
	return []chatMessage{
		{Role: "system", Content: "你是专业的屏幕翻译助手。"},
		{Role: "user", Content: []any{
			contentPart{Type: "text", Text: instruction},
			part,
		}},
	}
}

// streamChat is the single-provider entry point kept for tests and older
// callers. New code uses streamOpenAI with an explicit provider.
func streamChat(ctx context.Context, cfg Config, apiKey string, messages []chatMessage, onDelta func(string)) error {
	return streamOpenAI(ctx, cfg, Provider{Kind: providerOpenAI, BaseURL: cfg.BaseURL, Model: cfg.Model}, apiKey, messages, onDelta)
}

// streamOpenAI asks an OpenAI-compatible endpoint for a streamed reply and
// calls onDelta for every token. It returns when the stream ends, the
// context is canceled, or the endpoint fails.
func streamOpenAI(ctx context.Context, cfg Config, p Provider, apiKey string, messages []chatMessage, onDelta func(string)) error {
	if strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("还没有填写 API Key，请在设置里填写")
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatEndpoint(p.BaseURL), bytes.NewReader(payload))
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
		return fmt.Errorf("接口返回 %d：%s", resp.StatusCode, msg)
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
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return ctx.Err()
}
