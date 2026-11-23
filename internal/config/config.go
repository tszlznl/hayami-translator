// Package config owns the on-disk configuration model, defaults, validation,
// schema and atomic save. It has no UI dependency.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/egoist/mygo"
)

// SchemaRef is written into config.json so editors and coding agents can
// discover what each field accepts.
const SchemaRef = "./config.schema.json"

// Selection trigger modes, shared by the settings page, the tray and the
// toolbar itself.
const (
	TriggerModeIcon   = "显示图标，点击翻译"
	TriggerModeBubble = "取词即译气泡"
	TriggerModePopup  = "直接弹出完整翻译"
)

const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

const (
	TTSBackendSystem = "system"
	TTSBackendPiper  = "piper"
	TTSBackendOpenAI = "openai"

	TTSFormatMP3 = "mp3"
	TTSFormatWAV = "wav"
)

// Mode kinds: material treats the first user message as text to process;
// instruction treats it as the user's own question or command.
const (
	ModeMaterial    = "material"
	ModeInstruction = "instruction"
)

// Mode is a prompt preset. Built-in modes cover translation, polishing,
// summarizing and explanation; config.json can replace the list.
type Mode struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
	Kind   string `json:"kind"`
}

// Config is everything the app remembers between runs. API keys are not part
// of it: they live in the Windows credential store.
type Config struct {
	Schema           string  `json:"$schema,omitempty"`
	BaseURL          string  `json:"baseURL"`
	Model            string  `json:"model"`
	TargetLang       string  `json:"targetLang"`
	SourceLang       string  `json:"sourceLang"`
	SystemPrompt     string  `json:"systemPrompt"`
	Hotkey           string  `json:"hotkey"`
	ScreenshotHotkey string  `json:"screenshotHotkey"`
	InputHotkey      string  `json:"inputHotkey,omitempty"`
	Temperature      float64 `json:"temperature"`
	MaxTokens        int     `json:"maxTokens"`
	PopupPinned      bool    `json:"popupPinned,omitempty"`
	TriggerMode      string  `json:"triggerMode,omitempty"`
	Theme            string  `json:"theme,omitempty"`
	ActiveMode       string  `json:"activeMode,omitempty"`

	// Modes replaces the built-in prompt presets when non-empty. Each entry
	// is an id, display name, prompt and kind (material or instruction).
	Modes []Mode `json:"modes,omitempty"`

	// DisableSelectionToolbar turns off the automatic toolbar that appears
	// after a mouse selection. The zero value keeps it on.
	DisableSelectionToolbar bool `json:"disableSelectionToolbar,omitempty"`

	// MultiEngine enables concurrent translation using all enabled providers
	// instead of trying them sequentially as fallbacks.
	MultiEngine bool `json:"multiEngine,omitempty"`

	// Providers is the translation service list in priority order. BaseURL
	// and Model above are legacy single-provider fields kept so older
	// configs keep working.
	Providers           []Provider `json:"providers,omitempty"`
	ProvidersConfigured bool       `json:"providersConfigured,omitempty"`

	// HistoryLimit caps the number of history entries retained in SQLite.
	// Zero or negative uses DefaultHistoryLimit (1000).
	HistoryLimit int `json:"historyLimit,omitempty"`

	// Optional TTS backend settings ("system", "piper", "openai").
	TTSBackend    string `json:"ttsBackend,omitempty"`
	TTSPiperPath  string `json:"ttsPiperPath,omitempty"`
	TTSPiperModel string `json:"ttsPiperModel,omitempty"`
	TTSBaseURL    string `json:"ttsBaseURL,omitempty"`
	TTSModel      string `json:"ttsModel,omitempty"`
	TTSVoice      string `json:"ttsVoice,omitempty"`
	TTSFormat     string `json:"ttsFormat,omitempty"`
}

func Default() Config {
	cfg := Config{
		Schema:           SchemaRef,
		BaseURL:          "https://api.openai.com/v1",
		Model:            "gpt-4o-mini",
		TargetLang:       "自动（中→英）",
		SourceLang:       "自动检测",
		SystemPrompt:     "你是专业翻译。把用户提供的文本翻译成{{target}}。只输出译文，不要解释；保留原文的段落、列表和 Markdown 格式；代码、命令、专有名词保持原样。如果原文已经是{{target}}，就把内容改写得更自然。",
		Hotkey:           "Alt+F",
		ScreenshotHotkey: "Alt+S",
		InputHotkey:      "Alt+D",
		Temperature:      0.3,
		MaxTokens:        2048,
		PopupPinned:      false,
		Theme:            ThemeSystem,
		ActiveMode:       "translate",
	}
	cfg.Providers = []Provider{DefaultOpenAIProvider(cfg.BaseURL, cfg.Model)}
	cfg.ProvidersConfigured = true
	return cfg
}

const (
	polishPrompt    = "把用户提供的文字改得通顺、准确、自然，保持原文语言和语气；内容杂乱时整理成标题、列表或表格。只输出结果。"
	summarizePrompt = "总结用户提供的文字，保持原文语言：先给一句话结论，再列出 3-5 个要点，保留关键数字、名称和结论。只输出结果。"
	explainPrompt   = "解释用户提供的概念或文字，使用原文语言：先给简短定义，再说明关键点，必要时给一个例子。只输出结果。"
)

// BuiltinModes are used when config.json has no modes list. The translate
// prompt stays user-editable through systemPrompt.
func (c Config) BuiltinModes() []Mode {
	return []Mode{
		{ID: "translate", Name: "翻译", Prompt: c.SystemPrompt, Kind: ModeMaterial},
		{ID: "polish", Name: "润色", Prompt: polishPrompt, Kind: ModeMaterial},
		{ID: "summarize", Name: "总结", Prompt: summarizePrompt, Kind: ModeMaterial},
		{ID: "explain", Name: "解释", Prompt: explainPrompt, Kind: ModeMaterial},
	}
}

// AllModes returns the configured list, or the built-in presets when the
// list is empty.
func (c Config) AllModes() []Mode {
	if len(c.Modes) > 0 {
		return append([]Mode(nil), c.Modes...)
	}
	return c.BuiltinModes()
}

func (c Config) Mode(id string) (Mode, bool) {
	for _, m := range c.AllModes() {
		if m.ID == id {
			return m, true
		}
	}
	return Mode{}, false
}

func (c Config) ModeName(id string) string {
	if m, ok := c.Mode(id); ok {
		return m.Name
	}
	return id
}

// SameConfig reports whether two configs are equivalent, treating a nil and
// an empty provider list alike: both mean "no providers".
func (c Config) SameConfig(o Config) bool {
	if len(c.Providers) == 0 {
		c.Providers = nil
	}
	if len(o.Providers) == 0 {
		o.Providers = nil
	}
	// An empty modes list means the built-in presets, just like nil.
	if len(c.Modes) == 0 {
		c.Modes = nil
	}
	if len(o.Modes) == 0 {
		o.Modes = nil
	}
	return reflect.DeepEqual(c, o)
}

func (c Config) Clone() Config {
	if c.Providers != nil {
		c.Providers = append([]Provider{}, c.Providers...)
	}
	if c.Modes != nil {
		c.Modes = append([]Mode{}, c.Modes...)
	}
	return c
}

// SyncLegacy keeps the old BaseURL/Model fields pointing at the first OpenAI
// Compatible service, for older config readers.
func (c *Config) SyncLegacy() {
	for _, p := range c.Providers {
		if p.Kind == ProviderOpenAI {
			c.BaseURL = p.BaseURL
			c.Model = p.Model
			return
		}
	}
}

// NormalizeProviders fills defaults for missing fields on providers loaded
// from JSON without silently rewriting invalid kinds or duplicate IDs so that
// Validate can still report them explicitly.
func (c *Config) NormalizeProviders() {
	out := make([]Provider, 0, len(c.Providers))
	for _, p := range c.Providers {
		p.Kind = strings.TrimSpace(p.Kind)
		if p.Kind == "" {
			p.Kind = ProviderOpenAI
		}
		p.ID = strings.TrimSpace(p.ID)
		if p.ID == "" {
			p.ID = NextProviderID(Config{Providers: out}, p.Kind)
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			p.Name = DefaultProviderName(p.Kind)
		}
		switch p.Kind {
		case ProviderOpenAI:
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
		case ProviderAzure:
			p.Endpoint = strings.TrimSpace(p.Endpoint)
			p.Region = strings.TrimSpace(p.Region)
		case ProviderBaidu:
			p.Endpoint = strings.TrimSpace(p.Endpoint)
			p.AppID = strings.TrimSpace(p.AppID)
		}
		p.Key = strings.TrimSpace(p.Key)
		out = append(out, p)
	}
	c.Providers = out
	if len(out) > 0 {
		c.SyncLegacy()
	}
}

func Path() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func UserDataDir() string {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return ""
	}
	return dir
}

func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), err
	}

	// Start empty, not from Default: a config written before providers
	// existed has no providers key, and unmarshalling would otherwise leave
	// the default provider in place.
	cfg, err := Decode(data)
	if err != nil {
		return Default(), err
	}
	migrated := false
	if strings.TrimSpace(cfg.Schema) == "" {
		cfg.Schema = SchemaRef
		migrated = true
	}
	cfg.FillDefaults()
	if !cfg.ProvidersConfigured {
		if len(cfg.Providers) > 0 {
			cfg.ProvidersConfigured = true
		} else {
			cfg.Providers = []Provider{DefaultOpenAIProvider(cfg.BaseURL, cfg.Model)}
			cfg.ProvidersConfigured = true
		}
		migrated = true
	}
	cfg.NormalizeProviders()
	validationErr := Validate(cfg)
	if migrated && validationErr == nil {
		if err := cfg.Save(); err != nil {
			return cfg, err
		}
	}
	return cfg, validationErr
}

func (c *Config) FillDefaults() {
	d := Default()
	if strings.TrimSpace(c.Schema) == "" {
		c.Schema = SchemaRef
	}
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
	if strings.TrimSpace(c.InputHotkey) == "" {
		c.InputHotkey = d.InputHotkey
	}
	if strings.TrimSpace(c.Theme) == "" {
		c.Theme = d.Theme
	}
	if strings.TrimSpace(c.ActiveMode) == "" {
		if len(c.Modes) > 0 && strings.TrimSpace(c.Modes[0].ID) != "" {
			c.ActiveMode = strings.TrimSpace(c.Modes[0].ID)
		} else {
			c.ActiveMode = d.ActiveMode
		}
	}
	for i := range c.Modes {
		m := &c.Modes[i]
		m.ID = strings.TrimSpace(m.ID)
		m.Name = strings.TrimSpace(m.Name)
		m.Prompt = strings.TrimSpace(m.Prompt)
		m.Kind = strings.TrimSpace(m.Kind)
		if m.Kind == "" {
			m.Kind = ModeMaterial
		}
		if m.Name == "" {
			m.Name = m.ID
		}
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = d.MaxTokens
	}
	if c.Temperature < 0 {
		c.Temperature = d.Temperature
	}
}

func (c Config) Save() error {
	c.Schema = SchemaRef
	c.SyncLegacy()
	if err := Validate(c); err != nil {
		return err
	}
	path, err := Path()
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

// PromptFor fills {{target}} and {{source}} in the configured prompt.
func (c Config) PromptFor() string {
	source := strings.TrimSpace(c.SourceLang)
	if source == "" {
		source = "自动检测"
	}
	p := strings.ReplaceAll(c.SystemPrompt, "{{target}}", c.TargetLang)
	return strings.ReplaceAll(p, "{{source}}", source)
}
