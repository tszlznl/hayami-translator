package llm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hayami/internal/config"
)

// TranslateWithProvider sends one translation request to one service. OpenAI
// Compatible streams tokens; Azure and Baidu return the whole result, which is
// delivered through onDelta once.
func TranslateWithProvider(ctx context.Context, cfg config.Config, p config.Provider, key string, messages []Message, source string, onDelta func(string)) error {
	switch p.Kind {
	case config.ProviderOpenAI:
		return StreamOpenAI(ctx, cfg, p, key, messages, onDelta)
	case config.ProviderAzure:
		return translateAzure(ctx, cfg.TargetLang, p, key, source, onDelta)
	case config.ProviderBaidu:
		return translateBaidu(ctx, cfg.TargetLang, p, key, source, onDelta)
	default:
		return &ConfigError{Message: "不支持的翻译服务：" + p.Kind}
	}
}

// TranslateWithFallback tries the given providers in priority order until one
// translates the whole text. onProvider, when not nil, runs before each
// attempt (to label the engine in the UI); onDelta receives the translation
// incrementally. A service that already emitted output is never followed by
// another, so partial results are not concatenated.
func TranslateWithFallback(ctx context.Context, cfg config.Config, providers []config.Provider, messages []Message, text string, onProvider func(config.Provider), onDelta func(string)) error {
	var lastErr error
	for _, p := range providers {
		if ctx.Err() != nil {
			break
		}
		key := p.Key
		if !config.Configured(p, key) {
			continue
		}
		if onProvider != nil {
			onProvider(p)
		}
		emitted := false
		err := TranslateWithProvider(ctx, cfg, p, key, messages, text, func(delta string) {
			emitted = true
			onDelta(delta)
		})
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		lastErr = err
		if emitted {
			// A service that already produced text is not silently
			// replaced by another one; keep the partial result.
			return err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return &ConfigError{Message: "没有可用的翻译服务"}
}

func azureRequestURL(p config.Provider, target string) (string, error) {
	endpoint := strings.TrimSpace(p.Endpoint)
	if endpoint == "" {
		endpoint = config.DefaultAzureEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", &ConfigError{Message: "Azure 接口地址无效：" + err.Error()}
	}
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, "/translate") {
		u.Path = path + "/translate"
	}
	q := u.Query()
	q.Set("api-version", "3.0")
	q.Set("to", config.AzureLanguage(target))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func translateAzure(ctx context.Context, target string, p config.Provider, key, source string, onDelta func(string)) error {
	if strings.TrimSpace(key) == "" {
		return &ConfigError{Message: "还没有填写 Azure 翻译 Key，请在设置里填写"}
	}
	if strings.TrimSpace(source) == "" {
		return &ConfigError{Message: "没有要翻译的文本"}
	}
	u, err := azureRequestURL(p, target)
	if err != nil {
		return err
	}
	body, err := json.Marshal([]map[string]string{{"Text": source}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Ocp-Apim-Subscription-Key", strings.TrimSpace(key))
	if region := strings.TrimSpace(p.Region); region != "" {
		req.Header.Set("Ocp-Apim-Subscription-Region", region)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		msg := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		return &HTTPStatusError{Service: "Azure 翻译", Status: resp.StatusCode, Message: msg}
	}
	var out []struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("无法解析 Azure 翻译返回：%w", err)
	}
	var parts []string
	for _, item := range out {
		for _, tr := range item.Translations {
			if strings.TrimSpace(tr.Text) != "" {
				parts = append(parts, tr.Text)
			}
		}
	}
	result := strings.Join(parts, "\n")
	if strings.TrimSpace(result) == "" {
		return fmt.Errorf("Azure 翻译没有返回内容")
	}
	onDelta(result)
	return nil
}

func baiduRequestURL(p config.Provider) string {
	endpoint := strings.TrimSpace(p.Endpoint)
	if endpoint == "" {
		endpoint = config.DefaultBaiduEndpoint
	}
	return endpoint
}

func translateBaidu(ctx context.Context, target string, p config.Provider, key, source string, onDelta func(string)) error {
	if strings.TrimSpace(p.AppID) == "" || strings.TrimSpace(key) == "" {
		return &ConfigError{Message: "还没有填写百度翻译 APP ID 或密钥，请在设置里填写"}
	}
	if strings.TrimSpace(source) == "" {
		return &ConfigError{Message: "没有要翻译的文本"}
	}
	salt := fmt.Sprintf("%d", time.Now().UnixNano())
	sum := md5.Sum([]byte(p.AppID + source + salt + key))
	sign := hex.EncodeToString(sum[:])
	form := url.Values{
		"q":     {source},
		"from":  {"auto"},
		"to":    {config.BaiduLanguage(target)},
		"appid": {p.AppID},
		"salt":  {salt},
		"sign":  {sign},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baiduRequestURL(p), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &HTTPStatusError{Service: "百度翻译", Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	var out struct {
		TransResult []struct {
			Dst string `json:"dst"`
		} `json:"trans_result"`
		// The official endpoint sends error_code as a quoted string, but
		// accepting a number as well keeps the parser working through
		// gateways that normalize it.
		ErrorCode json.RawMessage `json:"error_code"`
		ErrorMsg  string          `json:"error_msg"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("无法解析百度翻译返回：%w", err)
	}
	code := ""
	if raw := strings.TrimSpace(string(out.ErrorCode)); raw != "" && raw != "null" {
		code = strings.Trim(raw, `"`)
	}
	// 52000 is Baidu's success code. A normal success response omits
	// error_code, but a gateway may still include it.
	if code != "" && code != "0" && code != "52000" {
		msg := strings.TrimSpace(out.ErrorMsg)
		if msg == "" {
			msg = config.BaiduErrorMessage(code)
		}
		return &ServiceError{Service: "百度翻译", Code: code, Message: msg}
	}
	var parts []string
	for _, item := range out.TransResult {
		if strings.TrimSpace(item.Dst) != "" {
			parts = append(parts, item.Dst)
		}
	}
	result := strings.Join(parts, "\n")
	if strings.TrimSpace(result) == "" {
		return fmt.Errorf("百度翻译没有返回内容")
	}
	onDelta(result)
	return nil
}
