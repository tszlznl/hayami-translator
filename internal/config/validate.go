package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Decode rejects unknown fields and reports syntax and type errors with a
// line, column or JSON path. Configuration is an interface for coding agents:
// a misspelled field fails loudly instead of being silently ignored.
func Decode(data []byte) (Config, error) {
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, describeDecodeError(data, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("配置文件在第一个 JSON 对象后还有多余内容")
	}
	return cfg, nil
}

func describeDecodeError(data []byte, err error) error {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		line, column := lineColumn(data, syntaxErr.Offset)
		return fmt.Errorf("配置文件第 %d 行第 %d 列语法错误：%v", line, column, err)
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			field = "根对象"
		}
		return fmt.Errorf("配置项 %s 的类型错误：需要 %s，实际是 %s", field, typeErr.Type, typeErr.Value)
	}
	if strings.Contains(err.Error(), "unknown field") {
		name := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return fmt.Errorf("配置文件包含未知字段 %q；请检查拼写或删除该字段", name)
	}
	return fmt.Errorf("配置文件格式错误：%v", err)
}

func lineColumn(data []byte, offset int64) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	line, column := 1, 1
	for _, b := range data[:offset] {
		if b == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}
	return line, column
}

// Validate checks ranges, enums, duplicate IDs and hotkeys, and URL syntax.
// Secrets are not validated here: they live in the credential store and an
// incomplete provider is still a valid draft.
func Validate(c Config) error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if c.Temperature < 0 || c.Temperature > 2 {
		add("temperature 必须在 0 到 2 之间")
	}
	if c.MaxTokens <= 0 || c.MaxTokens > 200000 {
		add("maxTokens 必须在 1 到 200000 之间")
	}
	switch c.TriggerMode {
	case "", TriggerModeIcon, TriggerModeBubble, TriggerModePopup:
	default:
		add("triggerMode 只能是 %q、%q 或 %q", TriggerModeIcon, TriggerModeBubble, TriggerModePopup)
	}
	if strings.TrimSpace(c.TargetLang) == "" {
		add("targetLang 不能为空")
	}
	if strings.TrimSpace(c.SystemPrompt) == "" {
		add("systemPrompt 不能为空")
	}
	switch c.Theme {
	case ThemeSystem, ThemeLight, ThemeDark:
	default:
		add("theme 只能是 %q、%q 或 %q", ThemeSystem, ThemeLight, ThemeDark)
	}

	modes := c.AllModes()
	seenModes := map[string]int{}
	for i, m := range modes {
		path := fmt.Sprintf("modes[%d]", i)
		if strings.TrimSpace(m.ID) == "" {
			add("%s.id 不能为空", path)
		} else if previous, ok := seenModes[m.ID]; ok {
			add("%s.id 与 modes[%d] 重复", path, previous)
		} else {
			seenModes[m.ID] = i
		}
		if strings.TrimSpace(m.Name) == "" {
			add("%s.name 不能为空", path)
		}
		if strings.TrimSpace(m.Prompt) == "" {
			add("%s.prompt 不能为空", path)
		}
		switch m.Kind {
		case ModeMaterial, ModeInstruction:
		default:
			add("%s.kind 只能是 %q 或 %q", path, ModeMaterial, ModeInstruction)
		}
	}
	if _, ok := c.Mode(c.ActiveMode); !ok {
		add("activeMode %q 不在 modes 列表中", c.ActiveMode)
	}

	hotkeys := []struct{ name, value string }{
		{"hotkey", c.Hotkey},
		{"screenshotHotkey", c.ScreenshotHotkey},
		{"inputHotkey", c.InputHotkey},
	}
	seenHotkeys := map[string]string{}
	for _, item := range hotkeys {
		value := strings.ToLower(strings.TrimSpace(item.value))
		if value == "" {
			add("%s 不能为空", item.name)
			continue
		}
		if previous, ok := seenHotkeys[value]; ok {
			add("%s 与 %s 使用了同一个快捷键 %q", previous, item.name, item.value)
			continue
		}
		seenHotkeys[value] = item.name
	}

	seenIDs := map[string]int{}
	for i, p := range c.Providers {
		path := fmt.Sprintf("providers[%d]", i)
		switch strings.TrimSpace(p.Kind) {
		case ProviderOpenAI, ProviderAzure, ProviderBaidu:
		default:
			add("%s.kind 不支持 %q", path, p.Kind)
		}
		id := strings.TrimSpace(p.ID)
		if id == "" {
			add("%s.id 不能为空", path)
		} else if previous, ok := seenIDs[id]; ok {
			add("%s.id 与 providers[%d] 重复", path, previous)
		} else {
			seenIDs[id] = i
		}
		if endpoint := strings.TrimSpace(p.BaseURL); endpoint != "" && !validHTTPURL(endpoint) {
			add("%s.baseURL 不是有效的 http(s) 地址", path)
		}
		if endpoint := strings.TrimSpace(p.Endpoint); endpoint != "" && !validHTTPURL(endpoint) {
			add("%s.endpoint 不是有效的 http(s) 地址", path)
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("配置不合法：%s", strings.Join(problems, "；"))
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

// WriteSchema publishes the schema next to config.json. Agents can read it
// before editing the file.
func WriteSchema() error {
	path, err := Path()
	if err != nil {
		return err
	}
	target := filepath.Join(filepath.Dir(path), "config.schema.json")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(SchemaJSON+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

const SchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://hayami.local/config.schema.json",
  "title": "Hayami Translator 配置",
  "description": "config.json 的字段说明。API Key 不在本文件中，保存在 Windows 凭据管理器。",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "$schema": {
      "type": "string",
      "description": "本文件的 schema 引用，通常为 ./config.schema.json。"
    },
    "baseURL": {
      "type": "string",
      "description": "旧版单服务字段；新配置使用 providers。指向 OpenAI 兼容接口的根地址，例如 https://api.openai.com/v1。"
    },
    "model": {
      "type": "string",
      "description": "旧版单服务字段；新配置使用 providers。模型名，例如 gpt-4o-mini。"
    },
    "targetLang": {
      "type": "string",
      "description": "目标语言。设为 自动检测 时按原文语言在中英之间互译。"
    },
    "sourceLang": {
      "type": "string",
      "description": "源语言提示，默认 自动检测。"
    },
    "systemPrompt": {
      "type": "string",
      "description": "用户可编辑的翻译提示词，支持 {{target}} 与 {{source}} 占位符。应用会追加材料语义与可信运行参数。"
    },
    "hotkey": {
      "type": "string",
      "description": "划词翻译全局快捷键，例如 Alt+F。"
    },
    "screenshotHotkey": {
      "type": "string",
      "description": "截图翻译全局快捷键，例如 Alt+S。"
    },
    "inputHotkey": {
      "type": "string",
      "description": "独立输入翻译窗口的全局快捷键，例如 Alt+D。"
    },
    "temperature": {
      "type": "number",
      "minimum": 0,
      "maximum": 2,
      "description": "采样温度；翻译建议 0.2 到 0.4。"
    },
    "maxTokens": {
      "type": "integer",
      "minimum": 1,
      "maximum": 200000,
      "description": "单次回复的最大 token 数。"
    },
    "popupPinned": {
      "type": "boolean",
      "description": "弹窗是否固定在屏幕上。"
    },
    "triggerMode": {
      "type": "string",
      "enum": ["显示图标，点击翻译", "取词即译气泡", "直接弹出完整翻译"],
      "description": "划选文本后的触发方式。"
    },
    "theme": {
      "type": "string",
      "enum": ["system", "light", "dark"],
      "description": "界面明暗主题；system 跟随 Windows。"
    },
    "activeMode": {
      "type": "string",
      "description": "默认提示词模式的 id，例如 translate、polish、summarize、explain。"
    },
    "modes": {
      "type": "array",
      "description": "自定义提示词模式；非空时替换内置模式列表。",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "id": {
            "type": "string",
            "description": "模式唯一标识，用于 activeMode 和弹窗切换。"
          },
          "name": {
            "type": "string",
            "description": "显示名称。"
          },
          "prompt": {
            "type": "string",
            "description": "该模式的提示词，支持 {{target}} 与 {{source}}。"
          },
          "kind": {
            "type": "string",
            "enum": ["material", "instruction"],
            "description": "material：第一条用户消息是待处理材料；instruction：用户消息是问题或指令。"
          }
        },
        "required": ["id", "name", "prompt", "kind"]
      }
    },
    "disableSelectionToolbar": {
      "type": "boolean",
      "description": "关闭划选后的自动浮窗。"
    },
    "multiEngine": {
      "type": "boolean",
      "description": "并发请求所有已启用服务并并列展示结果；关闭时按顺序回退。"
    },
    "providersConfigured": {
      "type": "boolean",
      "description": "内部迁移标记，表示 providers 已初始化。"
    },
    "providers": {
      "type": "array",
      "description": "翻译服务列表，顺序即优先级。",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "id": {
            "type": "string",
            "description": "服务唯一标识，用于凭据管理器中的 Key。"
          },
          "name": {
            "type": "string",
            "description": "显示名称。"
          },
          "kind": {
            "type": "string",
            "enum": ["openai", "azure", "baidu"],
            "description": "服务协议。"
          },
          "enabled": {
            "type": "boolean",
            "description": "是否参与翻译。"
          },
          "baseURL": {
            "type": "string",
            "description": "OpenAI Compatible 接口根地址。"
          },
          "model": {
            "type": "string",
            "description": "OpenAI Compatible 模型名。"
          },
          "vision": {
            "type": "boolean",
            "description": "该模型是否支持图片输入，截图翻译只会使用 vision 为 true 的服务。"
          },
          "endpoint": {
            "type": "string",
            "description": "Azure 或百度的接口地址；留空使用官方地址。"
          },
          "region": {
            "type": "string",
            "description": "Azure 资源区域。"
          },
          "appID": {
            "type": "string",
            "description": "百度翻译 APP ID。"
          }
        },
        "required": ["id", "kind"]
      }
    }
  },
  "required": ["targetLang", "systemPrompt", "hotkey", "screenshotHotkey"]
}`
