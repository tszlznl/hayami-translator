package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func resolveCLIUserDataDir() string {
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "Hayami Translator")
		}
	} else {
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(xdg, "Hayami Translator")
		}
		if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
			return filepath.Join(home, ".config", "Hayami Translator")
		}
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "Hayami Translator")
	}
	return userDataDir()
}

func loadCLIConfig() (Config, string, bool, error) {
	dir := resolveCLIUserDataDir()
	cfgPath := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return defaultConfig(), cfgPath, false, nil
	}
	if err != nil {
		return defaultConfig(), cfgPath, true, err
	}
	cfg, err := decodeConfig(data)
	if err != nil {
		return defaultConfig(), cfgPath, true, err
	}
	cfg.FillDefaults()
	if !cfg.ProvidersConfigured {
		if len(cfg.Providers) > 0 {
			cfg.ProvidersConfigured = true
		} else {
			cfg.Providers = []Provider{defaultOpenAIProvider(cfg.BaseURL, cfg.Model)}
			cfg.ProvidersConfigured = true
		}
	}
	cfg.NormalizeProviders()
	if err := validateConfig(cfg); err != nil {
		return cfg, cfgPath, true, err
	}
	return cfg, cfgPath, true, nil
}

// runCLITranslate executes `hayami translate [--target <lang>] [--mode <id>] [--json]`
// reading source text from stdin and writing the translated output to stdout.
func runCLITranslate(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	targetFlag := fs.String("target", "", "目标语言（例如 简体中文、English、日本語）")
	modeFlag := fs.String("mode", "", "提示词模式 id（例如 translate、polish、summarize、explain）")
	jsonFlag := fs.Bool("json", false, "以 JSON 格式输出结果")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	rawInput, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "读取标准输入失败: %v\n", err)
		return 1
	}
	sourceText := strings.TrimSpace(string(rawInput))
	if sourceText == "" {
		fmt.Fprintln(stderr, "标准输入为空：请通过管道或标准输入提供待翻译文本")
		return 1
	}

	cfg, _, _, cfgErr := loadCLIConfig()
	if cfgErr != nil {
		fmt.Fprintf(stderr, "配置文件错误: %v\n", cfgErr)
		return 1
	}
	cfg = loadProviderKeys(cfg)

	target := strings.TrimSpace(*targetFlag)
	if target == "" {
		target = cfg.TargetLang
	}
	if target == "" {
		target = autoTarget
	}
	cfg.TargetLang = effectiveTarget(target, sourceText)

	modeID := strings.TrimSpace(*modeFlag)
	if modeID == "" {
		modeID = cfg.ActiveMode
	}
	if modeID == "" {
		modeID = "translate"
	}
	m, ok := cfg.Mode(modeID)
	if !ok {
		fmt.Fprintf(stderr, "未知的提示词模式 %q\n", modeID)
		return 1
	}

	var providers []Provider
	if m.ID == "translate" {
		providers = cfg.EnabledProviders(false)
	} else {
		providers = cfg.EnabledPromptProviders()
	}
	configured := make([]Provider, 0, len(providers))
	for _, p := range providers {
		if providerConfigured(p, p.Key) {
			configured = append(configured, p)
		}
	}
	if len(configured) == 0 {
		fmt.Fprintln(stderr, "没有可用的翻译服务，请检查配置文件或填写 API Key（密钥）")
		return 1
	}

	messages := messagesForMode(cfg, sourceText, m)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var result strings.Builder
	var activeProvider string
	err = translateWithFallback(ctx, cfg, configured, messages, sourceText, func(p Provider) {
		result.Reset()
		activeProvider = providerLabel(p)
	}, func(delta string) {
		result.WriteString(delta)
	})
	if err != nil {
		title, hint := explainError(err)
		if hint != "" {
			fmt.Fprintf(stderr, "%s (%v)。%s\n", title, err, hint)
		} else {
			fmt.Fprintf(stderr, "%s: %v\n", title, err)
		}
		return 1
	}

	finalResult := result.String()
	if *jsonFlag {
		payload := map[string]any{
			"source":   sourceText,
			"result":   finalResult,
			"target":   cfg.TargetLang,
			"mode":     m.ID,
			"provider": activeProvider,
		}
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			fmt.Fprintf(stderr, "编码 JSON 输出失败: %v\n", err)
			return 1
		}
		return 0
	}

	if !strings.HasSuffix(finalResult, "\n") {
		finalResult += "\n"
	}
	_, _ = io.WriteString(stdout, finalResult)
	return 0
}

type doctorProviderCheck struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	OK         bool   `json:"ok"`
	LatencyMs  int64  `json:"latencyMs,omitempty"`
	Vision     string `json:"vision,omitempty"`
	Message    string `json:"message"`
}

type doctorReport struct {
	OK          bool                  `json:"ok"`
	Version     string                `json:"version"`
	UserDataDir string                `json:"userDataDir"`
	ConfigPath  string                `json:"configPath"`
	ConfigOK    bool                  `json:"configOK"`
	ConfigMsg   string                `json:"configMessage"`
	HistoryPath string                `json:"historyPath"`
	HistoryOK   bool                  `json:"historyOK"`
	HistoryRows int                   `json:"historyRows"`
	HistoryMsg  string                `json:"historyMessage"`
	Providers   []doctorProviderCheck `json:"providers"`
}

// runCLIDoctor executes `hayami doctor [--json]` checking configuration,
// credentials, SQLite history integrity, and live provider connectivity.
func runCLIDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonFlag := fs.Bool("json", false, "以 JSON 格式输出诊断报告")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir := resolveCLIUserDataDir()
	cfg, cfgPath, exists, cfgErr := loadCLIConfig()
	rep := doctorReport{
		OK:          true,
		Version:     appVersion,
		UserDataDir: dir,
		ConfigPath:  cfgPath,
		HistoryPath: filepath.Join(dir, "history.db"),
	}

	switch {
	case cfgErr != nil:
		rep.OK = false
		rep.ConfigOK = false
		rep.ConfigMsg = fmt.Sprintf("配置文件校验失败: %v", cfgErr)
	case !exists:
		rep.ConfigOK = true
		rep.ConfigMsg = "未找到 config.json（使用默认配置 / default config）"
	default:
		rep.ConfigOK = true
		rep.ConfigMsg = "config.json 校验通过"
	}

	// Check SQLite history database integrity if history.db exists or can be opened.
	histPath := rep.HistoryPath
	if _, statErr := os.Stat(histPath); errors.Is(statErr, os.ErrNotExist) {
		rep.HistoryOK = true
		rep.HistoryMsg = "history.db 尚未创建（正常）"
	} else {
		store, openErr := openHistoryAt(histPath)
		if openErr != nil {
			rep.OK = false
			rep.HistoryOK = false
			rep.HistoryMsg = fmt.Sprintf("history.db SQLite 打开失败: %v", openErr)
		} else {
			icErr := store.IntegrityCheck()
			cnt, cntErr := store.Count()
			store.Close()
			if icErr != nil {
				rep.OK = false
				rep.HistoryOK = false
				rep.HistoryMsg = fmt.Sprintf("history.db SQLite 完整性校验失败: %v", icErr)
			} else if cntErr != nil {
				rep.OK = false
				rep.HistoryOK = false
				rep.HistoryMsg = fmt.Sprintf("history.db SQLite 查询失败: %v", cntErr)
			} else {
				rep.HistoryOK = true
				rep.HistoryRows = cnt
				rep.HistoryMsg = fmt.Sprintf("history.db SQLite FTS5 正常（共 %d 条历史记录）", cnt)
			}
		}
	}

	// Load secrets and probe enabled providers when config is valid.
	if rep.ConfigOK {
		cfg = loadProviderKeys(cfg)
		enabledCount := 0
		for _, p := range cfg.Providers {
			chk := doctorProviderCheck{
				ID:         p.ID,
				Name:       p.Name,
				Kind:       p.Kind,
				Enabled:    p.Enabled,
				Configured: providerConfigured(p, p.Key),
			}
			if !p.Enabled {
				chk.OK = true
				chk.Message = "已禁用（跳过探测）"
				rep.Providers = append(rep.Providers, chk)
				continue
			}
			enabledCount++
			if !chk.Configured {
				rep.OK = false
				chk.OK = false
				chk.Message = "未配置密钥 (missing API key / secret)"
				rep.Providers = append(rep.Providers, chk)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			pr := probeProvider(ctx, cfg, p, p.Key)
			cancel()
			chk.LatencyMs = pr.Latency.Milliseconds()
			chk.Vision = pr.VisionStatus
			if pr.OK {
				chk.OK = true
				chk.Message = pr.Summary
			} else {
				rep.OK = false
				chk.OK = false
				msg := pr.ErrTitle
				if pr.ErrHint != "" {
					msg += " (" + pr.ErrHint + ")"
				}
				chk.Message = msg
			}
			rep.Providers = append(rep.Providers, chk)
		}
		if enabledCount == 0 {
			rep.OK = false
		}
	}

	if *jsonFlag {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(rep)
		if !rep.OK {
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "Hayami Translator Doctor v%s\n", rep.Version)
	fmt.Fprintf(stdout, "Config (%s): %s\n", rep.ConfigPath, rep.ConfigMsg)
	fmt.Fprintf(stdout, "History (%s): %s\n", rep.HistoryPath, rep.HistoryMsg)
	for _, p := range rep.Providers {
		state := "OK"
		if !p.OK {
			state = "FAIL"
		}
		fmt.Fprintf(stdout, "Provider [%s] %s (%s): %s - %s\n", state, p.Name, p.Kind, p.ID, p.Message)
	}
	if !rep.OK {
		return 1
	}
	return 0
}
