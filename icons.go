package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"

	"hayami/internal/config"
)

// Vector Icons (SVGs from native design draft)
var (
	iconTranslate  = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M4 5h8M8 3v2M6 5c0 4 3 7 6 8M10 5c0 3-2 6-6 8M13 20l4-10 4 10M14.5 17h5" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconArrowRight = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M5 12h14M13 6l6 6-6 6" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconPin        = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M12 17v5M9 3h6l-1 6 3 3v2H7v-2l3-3-1-6z" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconClose      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M5 5l14 14M19 5L5 19" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconCopy       = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><rect x="9" y="9" width="11" height="11" rx="2" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M5 15V6a2 2 0 0 1 2-2h8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconAudio      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M4 9v6h4l5 4V5L8 9H4zM16.5 8.5a5 5 0 0 1 0 7" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconSwap       = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M7 4L3 8l4 4M3 8h14M17 12l4 4-4 4M21 16H7" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconGeneral    = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="3" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M12 3v3M12 18v3M3 12h3M18 12h3" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`))
	iconKeyboard   = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><rect x="3" y="6" width="18" height="12" rx="2" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M7 10h.01M11 10h.01M15 10h.01M8 14h8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`))
	iconAppearance = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="8" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M12 4v16" stroke="currentColor" stroke-width="1.6"/></svg>`))
	iconAbout      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M12 11v5M12 8h.01" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`))
)

// speechPlayback tracks the active read-aloud from the moment it starts, not
// from when the speech engine's process starts. Cancelling the context also stops
// a command whose process has not started yet.
type speechPlayback struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

var speech speechPlayback

func (p *speechPlayback) begin(cmd *exec.Cmd, cancel context.CancelFunc) {
	p.mu.Lock()
	previous := p.cancel
	p.cmd = cmd
	p.cancel = cancel
	p.mu.Unlock()
	if previous != nil {
		previous()
	}
}

func (p *speechPlayback) stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cmd = nil
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *speechPlayback) active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancel != nil
}

func (p *speechPlayback) done(cmd *exec.Cmd) {
	p.mu.Lock()
	if p.cmd != cmd {
		p.mu.Unlock()
		return
	}
	p.cmd = nil
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type ttsAudioCache struct {
	mu     sync.Mutex
	cfg    Config
	key    string
	data   []byte
	format string
}

var speechState ttsAudioCache

func setSpeechConfig(cfg Config) {
	speechState.mu.Lock()
	speechState.cfg = cfg.Clone()
	speechState.mu.Unlock()
}

func getSpeechConfig() Config {
	speechState.mu.Lock()
	defer speechState.mu.Unlock()
	return speechState.cfg
}

func getCachedAudio(key string) ([]byte, string, bool) {
	speechState.mu.Lock()
	defer speechState.mu.Unlock()
	if speechState.key == key && len(speechState.data) > 0 {
		out := make([]byte, len(speechState.data))
		copy(out, speechState.data)
		return out, speechState.format, true
	}
	return nil, "", false
}

func setCachedAudio(key string, data []byte, format string) {
	speechState.mu.Lock()
	defer speechState.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	speechState.key = key
	speechState.data = cp
	speechState.format = format
}

func stopSpeaking() {
	speech.stop()
}

// isSpeaking reports whether a read-aloud is still running.
func isSpeaking() bool {
	return speech.active()
}

// speakResult reads the translation aloud, falling back to the source text
// when no translation has arrived yet.
func speakResult(result, source string) error {
	return speakResultWithNotify(result, source, nil)
}

func speakResultWithNotify(result, source string, onAsyncErr func(error)) error {
	if strings.TrimSpace(result) != "" {
		return speakTextWithNotify(result, onAsyncErr)
	}
	return speakTextWithNotify(source, onAsyncErr)
}

// speakText reads text aloud asynchronously using the configured speech engine
// (Piper, OpenAI /v1/audio/speech, or system TTS) with per-utterance audio caching.
func speakText(text string) error {
	return speakTextWithNotify(text, nil)
}

func speakTextWithNotify(text string, onAsyncErr func(error)) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	cfg := getSpeechConfig()
	backend := strings.ToLower(strings.TrimSpace(cfg.TTSBackend))
	if backend == "" {
		backend = config.TTSBackendSystem
	}

	// On non-Windows system TTS, fail synchronously if neither espeak-ng nor spd-say is installed.
	if backend == config.TTSBackendSystem && runtime.GOOS == "linux" {
		if _, err := findLinuxSystemTTSBinary(); err != nil {
			log.Printf("朗读失败: %v", err)
			return err
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	token := &exec.Cmd{}
	speech.begin(token, cancel)

	go func() {
		defer speech.done(token)
		if err := runSpeechBackend(ctx, cfg, backend, text); err != nil && ctx.Err() == nil {
			log.Printf("朗读失败: %v", err)
			if onAsyncErr != nil {
				onAsyncErr(err)
			}
		}
	}()
	return nil
}

func runSpeechBackend(ctx context.Context, cfg Config, backend, text string) error {
	switch backend {
	case config.TTSBackendPiper:
		cacheKey := fmt.Sprintf("piper|%s|%s|%s", cfg.TTSPiperPath, cfg.TTSPiperModel, text)
		if data, format, ok := getCachedAudio(cacheKey); ok {
			return playSynthesizedAudio(ctx, data, format)
		}
		data, err := synthesizePiperWAV(ctx, cfg, text)
		if err != nil {
			log.Printf("Piper 合成失败，回退至系统朗读: %v", err)
			return speakPlatformSystem(ctx, text)
		}
		setCachedAudio(cacheKey, data, config.TTSFormatWAV)
		return playSynthesizedAudio(ctx, data, config.TTSFormatWAV)

	case config.TTSBackendOpenAI:
		format := strings.ToLower(strings.TrimSpace(cfg.TTSFormat))
		if format != config.TTSFormatWAV {
			format = config.TTSFormatMP3
		}
		model := strings.TrimSpace(cfg.TTSModel)
		if model == "" {
			model = "tts-1"
		}
		voice := strings.TrimSpace(cfg.TTSVoice)
		if voice == "" {
			voice = "alloy"
		}
		baseURL := strings.TrimSpace(cfg.TTSBaseURL)
		if baseURL == "" {
			for _, p := range cfg.Providers {
				if p.Kind == config.ProviderOpenAI && strings.TrimSpace(p.BaseURL) != "" {
					baseURL = strings.TrimSpace(p.BaseURL)
					break
				}
			}
		}
		if baseURL == "" {
			baseURL = cfg.BaseURL
		}
		cacheKey := fmt.Sprintf("openai|%s|%s|%s|%s|%s", baseURL, model, voice, format, text)
		if data, fmtCached, ok := getCachedAudio(cacheKey); ok {
			return playSynthesizedAudio(ctx, data, fmtCached)
		}
		data, err := synthesizeOpenAISpeech(ctx, cfg, baseURL, model, voice, format, text)
		if err != nil {
			log.Printf("OpenAI Speech 合成失败，回退至系统朗读: %v", err)
			_ = speakPlatformSystem(ctx, text)
			return err
		}
		setCachedAudio(cacheKey, data, format)
		return playSynthesizedAudio(ctx, data, format)

	default:
		return speakPlatformSystem(ctx, text)
	}
}

func synthesizePiperWAV(ctx context.Context, cfg Config, text string) ([]byte, error) {
	bin := strings.TrimSpace(cfg.TTSPiperPath)
	if bin == "" {
		found, err := exec.LookPath("piper")
		if err != nil {
			return nil, fmt.Errorf("未找到 piper 可执行文件: %w", err)
		}
		bin = found
	}
	modelPath := strings.TrimSpace(cfg.TTSPiperModel)
	if modelPath == "" {
		return nil, fmt.Errorf("未配置 Piper 语音模型 (ttsPiperModel)")
	}
	tmpDir := os.TempDir()
	outFile := filepath.Join(tmpDir, fmt.Sprintf("hayami-piper-%d.wav", time.Now().UnixNano()))
	defer os.Remove(outFile)

	cmd := exec.CommandContext(ctx, bin, "--model", modelPath, "--output_file", outFile)
	cmd.Stdin = strings.NewReader(text)
	hideCommandWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("piper 执行失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		return nil, fmt.Errorf("读取 piper 输出音频失败: %w", err)
	}
	if !isValidAudioPayload(data, config.TTSFormatWAV) {
		return nil, fmt.Errorf("piper 生成的 WAV 音频格式无效")
	}
	return data, nil
}

func speechEndpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/audio/speech") {
		return base
	}
	return base + "/audio/speech"
}

func synthesizeOpenAISpeech(ctx context.Context, cfg Config, baseURL, model, voice, format, text string) ([]byte, error) {
	var apiKey string
	for _, p := range cfg.Providers {
		if p.Kind == config.ProviderOpenAI && strings.TrimSpace(p.Key) != "" {
			apiKey = strings.TrimSpace(p.Key)
			break
		}
	}
	payload, err := json.Marshal(map[string]any{
		"model":           model,
		"input":           text,
		"voice":           voice,
		"response_format": format,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, speechEndpoint(baseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("OpenAI Speech 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if !isValidAudioPayload(data, format) {
		return nil, fmt.Errorf("OpenAI Speech 返回的 %s 音频数据损坏或格式无效", format)
	}
	return data, nil
}

func isValidAudioPayload(data []byte, format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case config.TTSFormatWAV:
		return len(data) >= 44 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE"
	case config.TTSFormatMP3:
		if len(data) < 4 {
			return false
		}
		if string(data[0:3]) == "ID3" {
			return true
		}
		// MPEG frame sync: 11 set bits (0xFF followed by 0xE0..0xFF, excluding 0xFF free format invalid header).
		return data[0] == 0xff && (data[1]&0xe0) == 0xe0 && (data[1]&0x18) != 0x08 && (data[1]&0x06) != 0x00
	default:
		return len(data) > 0
	}
}

func estimateAudioDuration(data []byte, format string) time.Duration {
	if strings.EqualFold(format, config.TTSFormatWAV) && len(data) >= 44 {
		byteRate := binary.LittleEndian.Uint32(data[28:32])
		dataBytes := uint32(len(data) - 44)
		if byteRate > 0 {
			d := time.Duration(float64(dataBytes) / float64(byteRate) * float64(time.Second))
			if d > 20*time.Millisecond && d < 30*time.Second {
				return d
			}
		}
	}
	return 80 * time.Millisecond
}

func playSynthesizedAudio(ctx context.Context, data []byte, format string) error {
	if !isValidAudioPayload(data, format) {
		return fmt.Errorf("无效的音频数据")
	}
	dur := estimateAudioDuration(data, format)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(dur):
		return nil
	}
}

// detectSpeechVoice maps the text's detected script/language to an espeak-ng
// voice identifier. Returns "" for default English.
func detectSpeechVoice(text string) string {
	switch detectLanguageName(text) {
	case "中文":
		return "cmn"
	case "日本語":
		return "ja"
	case "한국어":
		return "ko"
	case "Русский":
		return "ru"
	case "العربية":
		return "ar"
	case "ไทย":
		return "th"
	}
	for _, r := range text {
		switch r {
		case 'ä', 'ö', 'ü', 'Ä', 'Ö', 'Ü', 'ß':
			return "de"
		case 'ñ', 'Ñ', '¿', '¡':
			return "es"
		case 'ç', 'Ç', 'œ', 'Œ', 'è', 'ê', 'ë', 'à', 'ù':
			return "fr"
		case 'ă', 'đ', 'Đ', 'ơ', 'ư':
			if unicode.IsLetter(r) {
				return "vi"
			}
		}
	}
	return ""
}

func findLinuxSystemTTSBinary() (string, error) {
	if p, err := exec.LookPath("espeak-ng"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("spd-say"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("朗读不可用：请先安装 espeak-ng 或 speech-dispatcher (spd-say)")
}

func speechCommand(ctx context.Context, text string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("espeak-ng"); err == nil {
			args := []string{"--stdin"}
			if voice := detectSpeechVoice(text); voice != "" {
				args = []string{"-v", voice, "--stdin"}
			}
			cmd := exec.CommandContext(ctx, "espeak-ng", args...)
			cmd.Stdin = strings.NewReader(text)
			return cmd, nil
		}
		if _, err := exec.LookPath("spd-say"); err == nil {
			args := []string{"-w"}
			if voice := detectSpeechVoice(text); voice != "" {
				args = []string{"-w", "-l", voice}
			}
			args = append(args, text)
			cmd := exec.CommandContext(ctx, "spd-say", args...)
			cmd.Stdin = strings.NewReader(text)
			return cmd, nil
		}
		return nil, fmt.Errorf("朗读不可用：请先安装 espeak-ng 或 spd-say")
	default:
		return nil, fmt.Errorf("当前平台暂不支持外部命令朗读")
	}
}
