package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeRejectsUnknownField(t *testing.T) {
	_, err := Decode([]byte(`{"baseURL":"https://api.openai.com/v1","bogus":1}`))
	if err == nil || !strings.Contains(err.Error(), "未知字段") || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeReportsTypePath(t *testing.T) {
	_, err := Decode([]byte(`{"temperature":"hot"}`))
	if err == nil || !strings.Contains(err.Error(), "temperature") || !strings.Contains(err.Error(), "类型") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeReportsLineAndColumn(t *testing.T) {
	_, err := Decode([]byte("{\n  \"temperature\": ,\n}"))
	if err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeRejectsTrailingContent(t *testing.T) {
	_, err := Decode([]byte(`{"model":"a"} {"model":"b"}`))
	if err == nil || !strings.Contains(err.Error(), "多余内容") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeDefaultConfigRoundTrip(t *testing.T) {
	want := Default()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	got.FillDefaults()
	got.NormalizeProviders()
	if !got.SameConfig(want) {
		t.Fatalf("round trip changed the config:\n got %#v\nwant %#v", got, want)
	}
}

func TestValidateDefaultConfig(t *testing.T) {
	if err := Validate(Default()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfigRanges(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"temperature", func(c *Config) { c.Temperature = 3 }, "temperature"},
		{"maxTokens", func(c *Config) { c.MaxTokens = 0 }, "maxTokens"},
		{"triggerMode", func(c *Config) { c.TriggerMode = "别的模式" }, "triggerMode"},
		{"targetLang", func(c *Config) { c.TargetLang = " " }, "targetLang"},
		{"systemPrompt", func(c *Config) { c.SystemPrompt = " " }, "systemPrompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.edit(&c)
			err := Validate(c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestValidateConfigDuplicateHotkeys(t *testing.T) {
	c := Default()
	c.InputHotkey = strings.ToLower(c.Hotkey)
	err := Validate(c)
	if err == nil || !strings.Contains(err.Error(), "同一个快捷键") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateConfigProviderIDsAndURLs(t *testing.T) {
	c := Default()
	c.Providers = []Provider{
		{ID: "dup", Kind: ProviderOpenAI, BaseURL: "ftp://example.com"},
		{ID: "dup", Kind: ProviderAzure, Endpoint: "not a url"},
	}
	err := Validate(c)
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"providers[0].baseURL", "providers[1].endpoint", "providers[1].id 与 providers[0] 重复"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestDefaultModes(t *testing.T) {
	c := Default()
	modes := c.AllModes()
	if len(modes) != 4 {
		t.Fatalf("modes = %d, want 4", len(modes))
	}
	translate, ok := c.Mode("translate")
	if !ok {
		t.Fatal("translate mode missing")
	}
	if translate.Prompt != c.SystemPrompt || translate.Kind != ModeMaterial {
		t.Fatalf("translate mode = %#v", translate)
	}
	if c.ModeName("polish") != "润色" {
		t.Fatalf("mode name = %q", c.ModeName("polish"))
	}
}

func TestCustomModesReplaceBuiltins(t *testing.T) {
	c := Default()
	c.Modes = []Mode{{ID: "custom", Name: "自定义", Prompt: "处理", Kind: ModeMaterial}}
	c.ActiveMode = "custom"
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	if len(c.AllModes()) != 1 || c.AllModes()[0].ID != "custom" {
		t.Fatalf("modes = %#v", c.AllModes())
	}
}

func TestValidateRejectsBadModes(t *testing.T) {
	c := Default()
	c.Theme = "neon"
	c.ActiveMode = "missing"
	c.Modes = []Mode{
		{ID: "same", Name: "A", Prompt: "", Kind: "other"},
		{ID: "same", Name: "", Prompt: "p", Kind: ModeMaterial},
	}
	err := Validate(c)
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"theme", "activeMode", "modes[0].prompt", "modes[0].kind", "modes[1].id 与 modes[0] 重复", "modes[1].name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestSchemaIsValidJSON(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v", schema["type"])
	}
}

func TestSchemaCoversConfigFields(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(SchemaJSON), &schema); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < reflect.TypeOf(Config{}).NumField(); i++ {
		tag := reflect.TypeOf(Config{}).Field(i).Tag.Get("json")
		key := strings.Split(tag, ",")[0]
		if key == "" || key == "-" {
			continue
		}
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("Config.%s is missing from config.schema.json", key)
		}
	}
}
