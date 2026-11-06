package config

import (
	"fmt"
	"strings"
)

const (
	ProviderOpenAI = "openai"
	ProviderAzure  = "azure"
	ProviderBaidu  = "baidu"
)

const (
	DefaultAzureEndpoint = "https://api.cognitive.microsofttranslator.com"
	DefaultBaiduEndpoint = "https://fanyi-api.baidu.com/api/trans/vip/translate"
)

// Provider is one translation service. The order of Config.Providers is the
// priority used when a translation is requested.
type Provider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`

	// OpenAI Compatible.
	BaseURL string `json:"baseURL,omitempty"`
	Model   string `json:"model,omitempty"`
	Vision  bool   `json:"vision,omitempty"`

	// Azure Translator and Baidu. Endpoint is optional and defaults to the
	// provider's public endpoint; it exists so a private gateway can be used.
	Endpoint string `json:"endpoint,omitempty"`
	Region   string `json:"region,omitempty"`
	AppID    string `json:"appID,omitempty"`

	// Key is the API key or secret. It is kept in the Windows credential
	// store, never in config.json.
	Key string `json:"-"`
}

func KindName(kind string) string {
	switch kind {
	case ProviderOpenAI:
		return "OpenAI Compatible"
	case ProviderAzure:
		return "Azure 翻译"
	case ProviderBaidu:
		return "百度翻译"
	default:
		return kind
	}
}

func DefaultProviderName(kind string) string {
	return KindName(kind)
}

func DefaultOpenAIProvider(baseURL, model string) Provider {
	return Provider{
		ID:      ProviderOpenAI,
		Name:    "OpenAI Compatible",
		Kind:    ProviderOpenAI,
		Enabled: true,
		BaseURL: baseURL,
		Model:   model,
		Vision:  true,
	}
}

func SecretKey(p Provider) string {
	return "provider." + p.ID
}

func Configured(p Provider, key string) bool {
	switch p.Kind {
	case ProviderOpenAI:
		return strings.TrimSpace(p.BaseURL) != "" &&
			strings.TrimSpace(p.Model) != "" &&
			strings.TrimSpace(key) != ""
	case ProviderAzure:
		return strings.TrimSpace(key) != ""
	case ProviderBaidu:
		return strings.TrimSpace(p.AppID) != "" && strings.TrimSpace(key) != ""
	default:
		return false
	}
}

func Label(p Provider) string {
	switch p.Kind {
	case ProviderOpenAI:
		if strings.TrimSpace(p.Model) != "" {
			return p.Name + " · " + p.Model
		}
	case ProviderAzure, ProviderBaidu:
	}
	return p.Name
}

// EnabledProviders returns the enabled services in priority order. Image
// requests can only use OpenAI Compatible services that support vision.
func (c Config) EnabledProviders(forImage bool) []Provider {
	out := make([]Provider, 0, len(c.Providers))
	for _, p := range c.Providers {
		if !p.Enabled {
			continue
		}
		if forImage && (p.Kind != ProviderOpenAI || !p.Vision) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func ActiveLabel(cfg Config) string {
	for _, p := range cfg.EnabledProviders(false) {
		if Configured(p, p.Key) {
			return Label(p)
		}
	}
	return "未配置翻译服务"
}

func HasConfigured(cfg Config) bool {
	for _, p := range cfg.EnabledProviders(false) {
		if Configured(p, p.Key) {
			return true
		}
	}
	return false
}

func AzureLanguage(target string) string {
	m := map[string]string{
		"简体中文": "zh-Hans", "繁體中文": "zh-Hant", "English": "en",
		"日本語": "ja", "한국어": "ko", "Français": "fr", "Deutsch": "de",
		"Español": "es", "Русский": "ru", "Português": "pt", "Italiano": "it",
		"العربية": "ar", "ไทย": "th", "Tiếng Việt": "vi",
	}
	if v, ok := m[target]; ok {
		return v
	}
	return target
}

func BaiduLanguage(target string) string {
	m := map[string]string{
		"简体中文": "zh", "繁體中文": "cht", "English": "en",
		"日本語": "jp", "한국어": "kor", "Français": "fra", "Deutsch": "de",
		"Español": "spa", "Русский": "ru", "Português": "pt", "Italiano": "it",
		"العربية": "ara", "ไทย": "th", "Tiếng Việt": "vie",
	}
	if v, ok := m[target]; ok {
		return v
	}
	return target
}

func BaiduErrorMessage(code string) string {
	m := map[string]string{
		"52001": "请求超时，请重试",
		"52002": "百度翻译系统错误，请重试",
		"52003": "未授权用户，请检查 APP ID",
		"54000": "必填参数为空",
		"54001": "签名错误，请检查密钥",
		"54003": "访问频率受限，请降低请求频率",
		"54004": "账户余额不足",
		"54005": "长文本请求频率受限，请稍后重试",
		"58000": "客户端 IP 非法",
		"58001": "译文语言方向不支持",
		"58002": "服务当前已关闭",
		"90107": "认证未通过或未生效",
	}
	if v, ok := m[code]; ok {
		return v
	}
	return "未知错误"
}

func NextProviderID(cfg Config, kind string) string {
	base := kind
	exists := func(id string) bool {
		for _, p := range cfg.Providers {
			if p.ID == id {
				return true
			}
		}
		return false
	}
	if !exists(base) {
		return base
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s-%d", base, i)
		if !exists(id) {
			return id
		}
	}
}
