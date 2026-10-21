package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
)

const apiKeySecret = "api-key"

// Config is everything the app remembers between runs. The API key is not
// part of it: it lives in the system's credential store.
type Config struct {
	BaseURL          string  `json:"baseURL"`
	Model            string  `json:"model"`
	TargetLang       string  `json:"targetLang"`
	SourceLang       string  `json:"sourceLang"`
	SystemPrompt     string  `json:"systemPrompt"`
	Hotkey           string  `json:"hotkey"`
	ScreenshotHotkey string  `json:"screenshotHotkey"`
	Temperature      float64 `json:"temperature"`
	MaxTokens        int     `json:"maxTokens"`

	// DisableSelectionToolbar turns off the automatic toolbar that appears
	// after a mouse selection. The zero value keeps it on, so configs from
	// before this feature keep working.
	DisableSelectionToolbar bool `json:"disableSelectionToolbar,omitempty"`

	// MultiEngine enables concurrent translation using all enabled providers
	// instead of trying them sequentially as fallbacks.
	MultiEngine bool `json:"multiEngine,omitempty"`

	// Providers is the translation service list in priority order. The
	// BaseURL and Model above are kept as the legacy single-provider fields
	// so existing configs keep working; new installs use Providers.
	Providers           []Provider `json:"providers,omitempty"`
	ProvidersConfigured bool       `json:"providersConfigured,omitempty"`
}

func defaultConfig() Config {
	cfg := Config{
		BaseURL:          "https://api.openai.com/v1",
		Model:            "gpt-4o-mini",
		TargetLang:       autoTarget,
		SourceLang:       "自动检测",
		SystemPrompt:     "你是专业翻译。把用户提供的文本翻译成{{target}}。只输出译文，不要解释；保留原文的段落、列表和 Markdown 格式；代码、命令、专有名词保持原样。如果原文已经是{{target}}，就把内容改写得更自然。",
		Hotkey:           "Alt+Shift+D",
		ScreenshotHotkey: "Alt+Shift+S",
		Temperature:      0.3,
		MaxTokens:        2048,
	}
	cfg.Providers = []Provider{defaultOpenAIProvider(cfg.BaseURL, cfg.Model)}
	cfg.ProvidersConfigured = true
	return cfg
}

func (c Config) clone() Config {
	c.Providers = append([]Provider(nil), c.Providers...)
	return c
}

// syncLegacy keeps the old BaseURL/Model fields pointing at the first
// OpenAI Compatible service, for older code and config readers.
func (c *Config) syncLegacy() {
	for _, p := range c.Providers {
		if p.Kind == providerOpenAI {
			c.BaseURL = p.BaseURL
			c.Model = p.Model
			return
		}
	}
}

// normalizeProviders fills the defaults of providers loaded from JSON and
// drops duplicate IDs. It does not add a provider to an intentionally empty
// list.
func (c *Config) normalizeProviders() {
	seen := map[string]bool{}
	out := make([]Provider, 0, len(c.Providers))
	for _, p := range c.Providers {
		p.Kind = strings.TrimSpace(p.Kind)
		if p.Kind == "" {
			p.Kind = providerOpenAI
		}
		if p.Kind != providerOpenAI && p.Kind != providerAzure && p.Kind != providerBaidu {
			p.Kind = providerOpenAI
		}
		p.ID = strings.TrimSpace(p.ID)
		if p.ID == "" || seen[p.ID] {
			p.ID = nextProviderID(Config{Providers: out}, p.Kind)
		}
		seen[p.ID] = true
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			p.Name = defaultProviderName(p.Kind)
		}
		switch p.Kind {
		case providerOpenAI:
			p.BaseURL = strings.TrimSpace(p.BaseURL)
			p.Model = strings.TrimSpace(p.Model)
			if p.BaseURL == "" {
				p.BaseURL = c.BaseURL
			}
			if p.Model == "" {
				p.Model = c.Model
			}
			if p.BaseURL == "" {
				p.BaseURL = "https://api.openai.com/v1"
			}
			if p.Model == "" {
				p.Model = "gpt-4o-mini"
			}
		case providerAzure:
			p.Endpoint = strings.TrimSpace(p.Endpoint)
			p.Region = strings.TrimSpace(p.Region)
		case providerBaidu:
			p.Endpoint = strings.TrimSpace(p.Endpoint)
			p.AppID = strings.TrimSpace(p.AppID)
		}
		out = append(out, p)
	}
	c.Providers = out
	if len(out) > 0 {
		c.syncLegacy()
	}
}

func configPath() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func userDataDir() string {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return ""
	}
	return dir
}

func loadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return defaultConfig(), err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultConfig(), nil
	}
	if err != nil {
		return defaultConfig(), err
	}

	// Start empty, not from defaultConfig: a config written before providers
	// existed has no providers key, and json.Unmarshal would otherwise leave
	// the default provider in place.
	cfg := Config{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultConfig(), err
	}
	cfg.fillDefaults()
	migrated := false
	if !cfg.ProvidersConfigured {
		if len(cfg.Providers) > 0 {
			cfg.ProvidersConfigured = true
		} else {
			cfg.Providers = []Provider{defaultOpenAIProvider(cfg.BaseURL, cfg.Model)}
			cfg.ProvidersConfigured = true
		}
		migrated = true
	}
	cfg.normalizeProviders()
	if migrated {
		if err := cfg.save(); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func (c *Config) fillDefaults() {
	d := defaultConfig()
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = d.BaseURL
	}
	if strings.TrimSpace(c.Model) == "" {
		c.Model = d.Model
	}
	if strings.TrimSpace(c.TargetLang) == "" {
		c.TargetLang = d.TargetLang
	}
	if strings.TrimSpace(c.SystemPrompt) == "" {
		c.SystemPrompt = d.SystemPrompt
	}
	if strings.TrimSpace(c.Hotkey) == "" {
		c.Hotkey = d.Hotkey
	}
	if strings.TrimSpace(c.ScreenshotHotkey) == "" {
		c.ScreenshotHotkey = d.ScreenshotHotkey
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = d.MaxTokens
	}
	if c.Temperature < 0 {
		c.Temperature = d.Temperature
	}
}

func (c Config) save() error {
	c.syncLegacy()
	path, err := configPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// promptFor fills {{target}} and {{source}} in the configured prompt.
func (c Config) promptFor() string {
	p := strings.ReplaceAll(c.SystemPrompt, "{{target}}", c.TargetLang)
	return strings.ReplaceAll(p, "{{source}}", c.SourceLang)
}
