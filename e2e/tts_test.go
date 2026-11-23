//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Cross-OS local helpers for F12 TTS & panel state parity tests
// ---------------------------------------------------------------------------

func ttsPollUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for: %s", timeout, desc)
}

func ttsOpenMainPanel(t *testing.T, s *sandbox, args ...string) (*GUIProcess, *AutomationClient, WindowInfo) {
	t.Helper()
	proc := s.startGUI(args...)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	client, err := DialAutomation(ctx, proc.SocketPath, proc.Done)
	if err != nil {
		t.Fatalf("DialAutomation: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	win, err := client.WaitWindow(ctx, func(w WindowInfo) bool {
		return w.Title == "Hayami Translator"
	})
	if err != nil {
		t.Fatalf("WaitWindow Hayami Translator: %v", err)
	}
	return proc, client, win
}

func ttsTreeContains(tr TreeResult, substr string) bool {
	for _, n := range tr.Nodes {
		if strings.Contains(n.Label, substr) ||
			strings.Contains(n.Value, substr) ||
			strings.Contains(n.Placeholder, substr) ||
			strings.Contains(n.Description, substr) {
			return true
		}
	}
	return false
}

func ttsWaitTreeContains(t *testing.T, client *AutomationClient, winID int, timeout time.Duration, substr string) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	tr, err := client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		return ttsTreeContains(tr, substr)
	})
	if err != nil {
		t.Fatalf("WaitTree(%q): %v", substr, err)
	}
	return tr
}

func ttsFindNode(tr TreeResult, pred func(Node) bool) (Node, bool) {
	for _, n := range tr.Nodes {
		if pred(n) {
			return n, true
		}
	}
	return Node{}, false
}

func ttsSwitchPanelTab(t *testing.T, client *AutomationClient, winID int, label string) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	btn, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "button" && n.Label == label
	})
	if err != nil {
		t.Fatalf("WaitNode tab %q: %v", label, err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press tab %q: %v", label, err)
	}
	if err := client.WaitIdle(ctx, winID); err != nil {
		t.Fatalf("WaitIdle after tab %q: %v", label, err)
	}
	tr, err := client.Tree(ctx, winID)
	if err != nil {
		t.Fatalf("Tree after tab %q: %v", label, err)
	}
	return tr
}

func ttsFindAudioButton(t *testing.T, client *AutomationClient, winID int) Node {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	btn, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "button" && (strings.Contains(n.Description, "朗读") || strings.Contains(n.Label, "朗读"))
	})
	if err != nil {
		t.Fatalf("WaitNode audio button: %v", err)
	}
	return btn
}

func ttsPressAudioButton(t *testing.T, client *AutomationClient, winID int) {
	t.Helper()
	btn := ttsFindAudioButton(t, client, winID)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press audio button: %v", err)
	}
}

func ttsTranslateInPopup(t *testing.T, client *AutomationClient, winID int, source, wantResultSubstr string) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	tr, err := client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		if _, ok := ttsFindNode(tr, func(n Node) bool {
			return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
		}); ok {
			return true
		}
		_, ok := ttsFindNode(tr, func(n Node) bool {
			return n.Role == "button" && strings.Contains(n.Description, "点击编辑原文")
		})
		return ok
	})
	if err != nil {
		t.Fatalf("WaitTree popup source editor/preview: %v", err)
	}
	editor, ok := ttsFindNode(tr, func(n Node) bool {
		return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
	})
	if !ok {
		preview, _ := ttsFindNode(tr, func(n Node) bool {
			return n.Role == "button" && strings.Contains(n.Description, "点击编辑原文")
		})
		if err := client.Act(ctx, ActParams{Window: winID, ID: preview.ID, Action: "press"}); err != nil {
			t.Fatalf("open popup editor: %v", err)
		}
		editor, _, err = client.WaitNode(ctx, winID, func(n Node) bool {
			return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
		})
		if err != nil {
			t.Fatalf("WaitNode popup editor: %v", err)
		}
	}

	if err := client.Act(ctx, ActParams{Window: winID, ID: editor.ID, Action: "setValue", Text: source}); err != nil {
		t.Fatalf("setValue popup source: %v", err)
	}
	isSubmitBtn := func(n Node) bool {
		return n.Role == "button" &&
			(n.Label == "翻译" || n.Label == "重新翻译") &&
			n.Key == nil &&
			n.Description == ""
	}
	tr, err = client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		_, ok := ttsFindNode(tr, func(n Node) bool {
			return isSubmitBtn(n) && !n.HasState("disabled")
		})
		return ok
	})
	if err != nil {
		t.Fatalf("WaitTree enabled translate button: %v", err)
	}
	btn, _ := ttsFindNode(tr, isSubmitBtn)
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press translate button: %v", err)
	}

	tr, err = client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		_, hasRetranslate := ttsFindNode(tr, func(n Node) bool {
			return n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
		})
		return hasRetranslate && ttsTreeContains(tr, wantResultSubstr)
	})
	if err != nil {
		t.Fatalf("WaitTree popup translation result %q: %v", wantResultSubstr, err)
	}
	return tr
}

func ttsTranslateInInputTab(t *testing.T, client *AutomationClient, winID int, source, wantResultSubstr string) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	editor, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "textField" && strings.Contains(n.Placeholder, "在此输入要翻译的内容")
	})
	if err != nil {
		t.Fatalf("WaitNode input tab editor: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: editor.ID, Action: "setValue", Text: source}); err != nil {
		t.Fatalf("setValue input tab source: %v", err)
	}

	btn, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "button" && n.Label == "翻译 (Ctrl+Enter)" && !n.HasState("disabled")
	})
	if err != nil {
		t.Fatalf("WaitNode input tab translate button: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press input tab translate button: %v", err)
	}

	tr, err := client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		return ttsTreeContains(tr, wantResultSubstr)
	})
	if err != nil {
		t.Fatalf("WaitTree input tab result %q: %v", wantResultSubstr, err)
	}
	return tr
}

type ttsConfigOpts struct {
	Backend   string
	PiperPath string
	ModelPath string
	BaseURL   string
	Model     string
	Voice     string
	Format    string
}

type schemaProp struct {
	Type        string                `json:"type"`
	Enum        []string              `json:"enum"`
	Description string                `json:"description"`
	Properties  map[string]schemaProp `json:"properties"`
}

func isTTSKey(key string) bool {
	kl := strings.ToLower(key)
	return strings.Contains(kl, "tts") || strings.Contains(kl, "piper") || strings.Contains(kl, "speech")
}

func cloneConfigMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			out[k] = cloneConfigMap(sub)
		} else {
			out[k] = v
		}
	}
	return out
}

func matchEnumValue(prop schemaProp, wanted string) string {
	for _, ev := range prop.Enum {
		if strings.EqualFold(ev, wanted) {
			return ev
		}
	}
	for _, ev := range prop.Enum {
		if strings.Contains(strings.ToLower(ev), strings.ToLower(wanted)) {
			return ev
		}
	}
	return wanted
}

func populateTTSProps(dst map[string]any, props map[string]schemaProp, opts ttsConfigOpts, inTTS, inPiper bool) int {
	populated := 0
	for k, prop := range props {
		if !inTTS && !isTTSKey(k) {
			continue
		}
		kl := strings.ToLower(k)
		childInPiper := inPiper || strings.Contains(kl, "piper")

		if prop.Type == "object" || len(prop.Properties) > 0 {
			var sub map[string]any
			if existing, ok := dst[k].(map[string]any); ok {
				sub = cloneConfigMap(existing)
			} else {
				sub = make(map[string]any)
			}
			n := populateTTSProps(sub, prop.Properties, opts, true, childInPiper)
			if n > 0 {
				dst[k] = sub
				populated += n
			}
			continue
		}

		switch {
		case strings.Contains(kl, "backend") || strings.Contains(kl, "engine") || strings.Contains(kl, "provider"):
			if opts.Backend != "" {
				dst[k] = matchEnumValue(prop, opts.Backend)
				populated++
			}
		case childInPiper && (strings.Contains(kl, "bin") || strings.Contains(kl, "path") || strings.Contains(kl, "cmd") || strings.Contains(kl, "exe")):
			if opts.PiperPath != "" {
				dst[k] = opts.PiperPath
				populated++
			}
		case childInPiper && strings.Contains(kl, "model"):
			if opts.ModelPath != "" {
				dst[k] = opts.ModelPath
				populated++
			}
		case !childInPiper && strings.Contains(kl, "model"):
			// Never write opts.ModelPath (.onnx) into non-Piper model keys.
			if opts.Model != "" {
				dst[k] = opts.Model
				populated++
			}
		case strings.Contains(kl, "url") || strings.Contains(kl, "endpoint"):
			if opts.BaseURL != "" {
				dst[k] = opts.BaseURL
				populated++
			}
		case strings.Contains(kl, "voice"):
			if opts.Voice != "" {
				dst[k] = opts.Voice
				populated++
			}
		case strings.Contains(kl, "format"):
			if opts.Format != "" {
				dst[k] = matchEnumValue(prop, opts.Format)
				populated++
			}
		case inTTS && (kl == "enabled" || kl == "ttsenabled") && prop.Type == "boolean":
			dst[k] = true
		}
	}
	return populated
}

// configureTTSViaSchema reads runtime config.schema.json to discover M5 TTS
// fields (both flat top-level and nested object schemas) rather than hardcoding
// speculative JSON keys against DisallowUnknownFields.
func configureTTSViaSchema(t *testing.T, s *sandbox, baseCfg map[string]any, opts ttsConfigOpts) {
	t.Helper()
	schemaBytes, err := os.ReadFile(s.schemaPath())
	if err != nil {
		t.Fatalf("read config.schema.json: %v", err)
	}
	var schema struct {
		Properties map[string]schemaProp `json:"properties"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("parse config.schema.json: %v", err)
	}

	var hasTTSSchema bool
	for k := range schema.Properties {
		if isTTSKey(k) {
			hasTTSSchema = true
			break
		}
	}
	if !hasTTSSchema {
		t.Fatalf("M5 TTS config schema not yet published in %s (cannot configure %s backend without violating DisallowUnknownFields)", s.schemaPath(), opts.Backend)
	}

	cfg := cloneConfigMap(baseCfg)
	populated := populateTTSProps(cfg, schema.Properties, opts, false, false)
	if populated == 0 {
		t.Fatalf("M5 TTS config schema in %s matched 0 TTS fields for options %+v", s.schemaPath(), opts)
	}

	beforeLen := len(s.readDebugLog())
	s.writeConfigJSON(cfg)
	ttsPollUntil(t, 4*time.Second, "config hot-reload after TTS update", func() bool {
		logText := s.readDebugLog()
		if len(logText) <= beforeLen {
			return false
		}
		tail := logText[beforeLen:]
		return strings.Contains(tail, "[debug] config reloaded")
	})
}

func countShimCalls(s *sandbox, tool string) int {
	logText := s.readShimLog(tool)
	if logText == "" {
		return 0
	}
	n := 0
	for _, line := range strings.Split(logText, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func countSpeechRequests(m *MockLLMServer) int {
	n := 0
	for _, req := range m.Requests() {
		if req.Path == "/v1/audio/speech" || req.Path == "/audio/speech" {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// F12 Tier 1: Multi-lang, Cached, Piper, OpenAI Speech & State Parity (6 tests)
// ---------------------------------------------------------------------------

func TestE2E_F12_T1_PanelTabParityAndDraftPreservation(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "标签页同步译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := ttsOpenMainPanel(t, s)

	// 1. Translate in "翻译" tab.
	ttsTranslateInPopup(t, client, win.ID, "hello parity", "标签页同步译文")

	// 2. Switch to "输入" tab and type an unsent draft.
	ttsSwitchPanelTab(t, client, win.ID, "输入")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inputEditor, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "textField" && strings.Contains(n.Placeholder, "在此输入要翻译的内容")
	})
	if err != nil {
		t.Fatalf("WaitNode input editor: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: inputEditor.ID, Action: "setValue", Text: "input draft preserved"}); err != nil {
		t.Fatalf("setValue input draft: %v", err)
	}
	ttsWaitTreeContains(t, client, win.ID, 3*time.Second, "清空")

	// 3. Switch to "设置" -> "快捷键" and edit Hotkey to make settings dirty.
	ttsSwitchPanelTab(t, client, win.ID, "设置")
	navHotkeys, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "button" && n.Description == "快捷键"
	})
	if err != nil {
		t.Fatalf("WaitNode 快捷键 nav button: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: navHotkeys.ID, Action: "press"}); err != nil {
		t.Fatalf("press 快捷键 nav: %v", err)
	}
	hotkeyField, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "textField" && n.Placeholder == "Alt+F"
	})
	if err != nil {
		t.Fatalf("WaitNode Alt+F hotkey field: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: hotkeyField.ID, Action: "setValue", Text: "Alt+Shift+Q"}); err != nil {
		t.Fatalf("setValue hotkey draft: %v", err)
	}
	ttsWaitTreeContains(t, client, win.ID, 3*time.Second, "放弃修改")

	// 4. Trigger live config.json hot-reload while settings draft is dirty.
	reloadedCfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	reloadedCfg["popupPinned"] = true
	reloadedCfg["disableSelectionToolbar"] = true
	reloadedCfg["theme"] = "dark"
	s.writeConfigJSON(reloadedCfg)
	ttsPollUntil(t, 4*time.Second, "config reloaded in debug.log", func() bool {
		return strings.Contains(s.readDebugLog(), "[debug] config reloaded")
	})

	// 5. Cycle through all tabs and verify state preservation.
	ttsSwitchPanelTab(t, client, win.ID, "历史")
	trHistory := ttsWaitTreeContains(t, client, win.ID, 3*time.Second, "hello parity")
	if !ttsTreeContains(trHistory, "清空历史") {
		t.Fatalf("expected History tab controls alongside entry 'hello parity', got:\n%s", formatTreeSummary(trHistory))
	}

	trPopup := ttsSwitchPanelTab(t, client, win.ID, "翻译")
	if !ttsTreeContains(trPopup, "hello parity") || !ttsTreeContains(trPopup, "标签页同步译文") {
		t.Fatalf("popup translation state lost across tab cycle / reload")
	}

	trInput := ttsSwitchPanelTab(t, client, win.ID, "输入")
	if !ttsTreeContains(trInput, "input draft preserved") {
		t.Fatalf("input tab draft lost across tab cycle / reload")
	}

	trSettings := ttsSwitchPanelTab(t, client, win.ID, "设置")
	if !ttsTreeContains(trSettings, "放弃修改") || !ttsTreeContains(trSettings, "Alt+Shift+Q") {
		t.Fatalf("settings dirty draft lost across tab cycle / reload")
	}

	// 6. Discard dirty draft and verify clean state restored.
	discardBtn, ok := ttsFindNode(trSettings, func(n Node) bool {
		return n.Role == "button" && n.Label == "放弃修改"
	})
	if !ok {
		t.Fatalf("missing 放弃修改 button")
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: discardBtn.ID, Action: "press"}); err != nil {
		t.Fatalf("press 放弃修改: %v", err)
	}
	_, err = client.WaitTree(ctx, win.ID, func(tr TreeResult) bool {
		return !ttsTreeContains(tr, "放弃修改") && ttsTreeContains(tr, "Alt+F")
	})
	if err != nil {
		t.Fatalf("discard draft did not restore Alt+F: %v", err)
	}
}

func TestE2E_F12_T1_PopupAndInputAudioParityAndSpdSayFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX TTS PATH shims only apply on non-Windows hosts")
	}

	t.Run("espeak_dual_tab", func(t *testing.T) {
		s := newSandbox(t)
		m := newMockLLMServer(t)
		m.QueueResponse(
			MockResponse{Body: "Popup spoken"},
			MockResponse{Body: "Input spoken"},
		)
		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		s.IsolatePATH = true
		s.installShim("espeak-ng")
		s.setShimState("espeak_mode", "ok")

		_, client, win := ttsOpenMainPanel(t, s)

		ttsTranslateInPopup(t, client, win.ID, "Speak popup", "Popup spoken")
		ttsPressAudioButton(t, client, win.ID)
		ttsPollUntil(t, 4*time.Second, "espeak-ng spoken from popup tab", func() bool {
			return strings.Contains(s.readShimLog("espeak-ng"), "stdin=Popup spoken")
		})

		ttsSwitchPanelTab(t, client, win.ID, "输入")
		ttsTranslateInInputTab(t, client, win.ID, "Speak input", "Input spoken")
		ttsPressAudioButton(t, client, win.ID)
		ttsPollUntil(t, 4*time.Second, "espeak-ng spoken from input tab", func() bool {
			return strings.Contains(s.readShimLog("espeak-ng"), "stdin=Input spoken")
		})
	})

	t.Run("spd_say_fallback", func(t *testing.T) {
		s := newSandbox(t)
		m := newMockLLMServer(t)
		m.SetDefault("chat", MockResponse{Body: "Spoken via spd-say"})
		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		s.IsolatePATH = true
		s.installShim("spd-say")
		s.setShimState("spd_mode", "ok")

		_, client, win := ttsOpenMainPanel(t, s)
		ttsTranslateInPopup(t, client, win.ID, "Fallback speech", "Spoken via spd-say")
		ttsPressAudioButton(t, client, win.ID)

		ttsPollUntil(t, 3*time.Second, "spd-say shim invoked", func() bool {
			return strings.Contains(s.readShimLog("spd-say"), "Spoken via spd-say")
		})
	})
}

func TestE2E_F12_T1_MultiLangVoiceMatching(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX TTS PATH shims only apply on non-Windows hosts")
	}

	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.QueueResponse(
		MockResponse{Body: "Good morning"},
		MockResponse{Body: "你好，世界"},
		MockResponse{Body: "こんにちは世界"},
	)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("espeak-ng")
	s.setShimState("espeak_mode", "ok")

	_, client, win := ttsOpenMainPanel(t, s)

	utterances := []struct {
		src string
		dst string
	}{
		{"Morning", "Good morning"},
		{"Hello", "你好，世界"},
		{"Konbanwa", "こんにちは世界"},
	}

	for i, u := range utterances {
		ttsTranslateInPopup(t, client, win.ID, u.src, u.dst)
		ttsPressAudioButton(t, client, win.ID)
		wantCount := i + 1
		ttsPollUntil(t, 4*time.Second, "espeak-ng log line for "+u.dst, func() bool {
			return countShimCalls(s, "espeak-ng") >= wantCount &&
				strings.Contains(s.readShimLog("espeak-ng"), "stdin="+u.dst)
		})
	}

	logLines := strings.Split(strings.TrimSpace(s.readShimLog("espeak-ng")), "\n")
	if len(logLines) < 3 {
		t.Fatalf("expected 3 espeak-ng invocations, got:\n%s", s.readShimLog("espeak-ng"))
	}
	var zhLine, jaLine string
	for _, line := range logLines {
		if strings.Contains(line, "stdin=你好，世界") {
			zhLine = line
		}
		if strings.Contains(line, "stdin=こんにちは世界") {
			jaLine = line
		}
	}
	if !strings.Contains(zhLine, "-v") {
		t.Fatalf("expected Chinese utterance to pass -v voice flag, got %q", zhLine)
	}
	if !strings.Contains(jaLine, "-v") || (!strings.Contains(jaLine, "ja") && !strings.Contains(jaLine, "jp")) {
		t.Fatalf("expected Japanese utterance to select Japanese -v voice (ja/jpn), got %q", jaLine)
	}
}

func TestE2E_F12_T1_PiperNeuralSynthesisAndReplayCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Piper PATH shim only applies on non-Windows hosts")
	}

	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.QueueResponse(
		MockResponse{Body: "AlphaResult"},
		MockResponse{Body: "BetaResult"},
	)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	piperPath := s.installShim("piper")
	s.setShimState("piper_mode", "ok")

	wavBytes := makeWAV(100 * time.Millisecond)
	wavPath := filepath.Join(s.TmpDir, "piper_fixture.wav")
	if err := os.WriteFile(wavPath, wavBytes, 0o644); err != nil {
		t.Fatalf("write piper wav fixture: %v", err)
	}
	s.setShimState("fixture_wav", wavPath)

	pcmPath := filepath.Join(s.TmpDir, "piper_fixture.pcm")
	if err := os.WriteFile(pcmPath, wavBytes[44:], 0o644); err != nil {
		t.Fatalf("write piper pcm fixture: %v", err)
	}
	s.setShimState("fixture_pcm", pcmPath)

	modelPath := filepath.Join(s.TmpDir, "en_US-lessac-medium.onnx")
	if err := os.WriteFile(modelPath, []byte("onnx-stub"), 0o644); err != nil {
		t.Fatalf("write model stub: %v", err)
	}

	_, client, win := ttsOpenMainPanel(t, s)

	configureTTSViaSchema(t, s, cfg, ttsConfigOpts{
		Backend:   "piper",
		PiperPath: piperPath,
		ModelPath: modelPath,
	})

	// First utterance -> synthesizes via Piper.
	ttsTranslateInPopup(t, client, win.ID, "Alpha", "AlphaResult")
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 4*time.Second, "piper called once for AlphaResult", func() bool {
		return countShimCalls(s, "piper") == 1 && strings.Contains(s.readShimLog("piper"), "stdin=AlphaResult")
	})
	piperLog := s.readShimLog("piper")
	if !strings.Contains(piperLog, "--model") || !strings.Contains(piperLog, modelPath) {
		t.Fatalf("expected piper invocation to pass --model %q, got:\n%s", modelPath, piperLog)
	}

	// Wait for synthesis & 100ms audio playback to finish before clicking replay so
	// the audio button triggers replay rather than stopSpeaking() toggle.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle after first piper synthesis: %v", err)
	}
	time.Sleep(350 * time.Millisecond)

	// Replay identical result -> must hit per-translation audio cache (0 extra piper calls).
	ttsPressAudioButton(t, client, win.ID)
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle after piper replay: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	tr, err := client.Tree(ctx, win.ID)
	if err != nil {
		t.Fatalf("Tree after piper replay: %v", err)
	}
	if ttsTreeContains(tr, "已停止朗读") {
		t.Fatalf("replay click toggled stopSpeaking instead of replaying cached audio:\n%s", formatTreeSummary(tr))
	}
	if ttsTreeContains(tr, "失败") || ttsTreeContains(tr, "错误") {
		t.Fatalf("unexpected TTS error status on piper replay:\n%s", formatTreeSummary(tr))
	}
	if got := countShimCalls(s, "piper"); got != 1 {
		t.Fatalf("expected audio cache hit on replay (1 piper call), got %d:\n%s", got, s.readShimLog("piper"))
	}

	// New translation -> invalidates cache and calls Piper again.
	ttsTranslateInPopup(t, client, win.ID, "Beta", "BetaResult")
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 4*time.Second, "piper called 2nd time for BetaResult", func() bool {
		return countShimCalls(s, "piper") == 2 && strings.Contains(s.readShimLog("piper"), "stdin=BetaResult")
	})
}

func TestE2E_F12_T1_OpenAISpeechMP3WAVAndReplayCache(t *testing.T) {
	for _, format := range []string{"mp3", "wav"} {
		t.Run(format, func(t *testing.T) {
			s := newSandbox(t)
			m := newMockLLMServer(t)
			m.QueueResponse(
				MockResponse{Body: "GammaResult"},
				MockResponse{Body: "DeltaResult"},
			)

			cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
			cfg["popupPinned"] = true
			cfg["disableSelectionToolbar"] = true
			s.writeConfigJSON(cfg)

			_, client, win := ttsOpenMainPanel(t, s)

			configureTTSViaSchema(t, s, cfg, ttsConfigOpts{
				Backend: "openai",
				BaseURL: m.V1URL(),
				Model:   "tts-1",
				Voice:   "alloy",
				Format:  format,
			})

			ttsTranslateInPopup(t, client, win.ID, "Gamma", "GammaResult")
			ttsPressAudioButton(t, client, win.ID)
			ttsPollUntil(t, 4*time.Second, "POST /v1/audio/speech for GammaResult", func() bool {
				return countSpeechRequests(m) == 1
			})

			// Wait for synthesis & 100ms audio playback to finish before clicking replay.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := client.WaitIdle(ctx, win.ID); err != nil {
				t.Fatalf("WaitIdle after first %s speech synthesis: %v", format, err)
			}
			time.Sleep(350 * time.Millisecond)

			// Replay same translation -> cache hit (speech count stays 1, not stopSpeaking toggle).
			ttsPressAudioButton(t, client, win.ID)
			if err := client.WaitIdle(ctx, win.ID); err != nil {
				t.Fatalf("WaitIdle after %s replay: %v", format, err)
			}
			time.Sleep(150 * time.Millisecond)
			tr, err := client.Tree(ctx, win.ID)
			if err != nil {
				t.Fatalf("Tree after %s replay: %v", format, err)
			}
			if ttsTreeContains(tr, "已停止朗读") {
				t.Fatalf("replay click toggled stopSpeaking instead of replaying cached %s audio:\n%s", format, formatTreeSummary(tr))
			}
			if ttsTreeContains(tr, "失败") || ttsTreeContains(tr, "错误") {
				t.Fatalf("unexpected TTS error status on %s replay:\n%s", format, formatTreeSummary(tr))
			}
			if got := countSpeechRequests(m); got != 1 {
				t.Fatalf("expected replay cache hit (1 speech HTTP call), got %d", got)
			}

			// New translation -> triggers 2nd /v1/audio/speech request.
			ttsTranslateInPopup(t, client, win.ID, "Delta", "DeltaResult")
			ttsPressAudioButton(t, client, win.ID)
			ttsPollUntil(t, 4*time.Second, "2nd POST /v1/audio/speech for DeltaResult", func() bool {
				return countSpeechRequests(m) == 2
			})

			var speechPayloads []struct {
				Input          string `json:"input"`
				Model          string `json:"model"`
				Voice          string `json:"voice"`
				ResponseFormat string `json:"response_format"`
			}
			for _, req := range m.Requests() {
				if req.Path == "/v1/audio/speech" || req.Path == "/audio/speech" {
					var p struct {
						Input          string `json:"input"`
						Model          string `json:"model"`
						Voice          string `json:"voice"`
						ResponseFormat string `json:"response_format"`
					}
					if err := json.Unmarshal(req.Body, &p); err != nil {
						t.Fatalf("unmarshal /v1/audio/speech body %q: %v", string(req.Body), err)
					}
					speechPayloads = append(speechPayloads, p)
				}
			}
			if len(speechPayloads) != 2 {
				t.Fatalf("expected 2 /v1/audio/speech payloads, got %d", len(speechPayloads))
			}
			wantInputs := []string{"GammaResult", "DeltaResult"}
			for i, p := range speechPayloads {
				if p.Input != wantInputs[i] || p.Model != "tts-1" || p.Voice != "alloy" || !strings.EqualFold(p.ResponseFormat, format) {
					t.Fatalf("speech payload[%d] = %+v, want input=%q model=tts-1 voice=alloy response_format=%q",
						i, p, wantInputs[i], format)
				}
			}
		})
	}
}

func TestE2E_F12_T1_AsyncInterruptionAndStopSpeaking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX TTS PATH shims only apply on non-Windows hosts")
	}

	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "Long utterance to interrupt"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("espeak-ng")
	s.setShimState("espeak_mode", "slow")

	proc, client, win := ttsOpenMainPanel(t, s)
	ttsTranslateInPopup(t, client, win.ID, "Slow test", "Long utterance to interrupt")

	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 3*time.Second, "slow espeak-ng started", func() bool {
		return countShimCalls(s, "espeak-ng") == 1
	})

	stopStart := time.Now()
	ttsPressAudioButton(t, client, win.ID)
	ttsWaitTreeContains(t, client, win.ID, 1500*time.Millisecond, "已停止朗读")
	if elapsed := time.Since(stopStart); elapsed >= 1500*time.Millisecond {
		t.Fatalf("stopping active speech took %v, want < 1.5s", elapsed)
	}

	// Verify speech can be cleanly restarted after interruption.
	s.setShimState("espeak_mode", "ok")
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 3*time.Second, "espeak-ng restarted cleanly", func() bool {
		return countShimCalls(s, "espeak-ng") == 2
	})
	proc.Stop(t)
}

// ---------------------------------------------------------------------------
// F12 Tier 2: Error, Fallback, Cancellation & Hot-Reload Safety (6 tests)
// ---------------------------------------------------------------------------

func TestE2E_F12_T2_MissingAllTTSBinariesFailsGracefully(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX empty PATH binary test only applies on non-Windows hosts")
	}

	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.QueueResponse(
		MockResponse{Body: "No TTS binary"},
		MockResponse{Body: "Still translates cleanly"},
	)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)
	s.IsolatePATH = true

	_, client, win := ttsOpenMainPanel(t, s)
	ttsTranslateInPopup(t, client, win.ID, "First", "No TTS binary")

	ttsPressAudioButton(t, client, win.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	tr, err := client.WaitTree(ctx, win.ID, func(tr TreeResult) bool {
		return ttsTreeContains(tr, "朗读不可用") || ttsTreeContains(tr, "失败") || ttsTreeContains(tr, "未找到")
	})
	if err != nil {
		t.Fatalf("expected graceful TTS error status when all binaries missing, got: %v\n%s", err, formatTreeSummary(tr))
	}
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle failed after TTS error: %v", err)
	}

	// App must stay responsive and handle subsequent translations.
	ttsTranslateInPopup(t, client, win.ID, "Second", "Still translates cleanly")
}

func TestE2E_F12_T2_EmptyTextAndSourceFallbackBeforeResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX TTS PATH shims only apply on non-Windows hosts")
	}

	s := newSandbox(t)
	m := newMockLLMServer(t)
	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("espeak-ng")
	s.setShimState("espeak_mode", "ok")

	_, client, win := ttsOpenMainPanel(t, s)

	// Subtest A: empty source & empty result -> 0 TTS invocations.
	ttsPressAudioButton(t, client, win.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle on empty audio press: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if logText := s.readShimLog("espeak-ng"); logText != "" {
		t.Fatalf("expected no speech call when source and result are empty, got %q", logText)
	}

	// Subtest B: source entered without translation yet -> speakResult falls back to source.
	editor, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
	})
	if err != nil {
		t.Fatalf("WaitNode popup editor: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: editor.ID, Action: "setValue", Text: "fallback source"}); err != nil {
		t.Fatalf("setValue fallback source: %v", err)
	}
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 4*time.Second, "espeak-ng speaks fallback source", func() bool {
		return strings.Contains(s.readShimLog("espeak-ng"), "stdin=fallback source")
	})
	if got := len(m.Requests()); got != 0 {
		t.Fatalf("expected 0 LLM requests when only speaking source fallback, got %d", got)
	}
}

func TestE2E_F12_T2_PiperFailureFallsBackToSystemTTS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Piper/espeak-ng PATH shims only apply on non-Windows hosts")
	}

	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "Piper fallback text"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	piperPath := s.installShim("piper")
	s.installShim("espeak-ng")
	s.setShimState("piper_mode", "error")
	s.setShimState("espeak_mode", "ok")

	modelPath := filepath.Join(s.TmpDir, "broken.onnx")
	if err := os.WriteFile(modelPath, []byte("onnx-stub"), 0o644); err != nil {
		t.Fatalf("write model stub: %v", err)
	}

	_, client, win := ttsOpenMainPanel(t, s)
	configureTTSViaSchema(t, s, cfg, ttsConfigOpts{
		Backend:   "piper",
		PiperPath: piperPath,
		ModelPath: modelPath,
	})

	ttsTranslateInPopup(t, client, win.ID, "Fail piper", "Piper fallback text")
	ttsPressAudioButton(t, client, win.ID)

	ttsPollUntil(t, 4*time.Second, "piper attempted and fell back to espeak-ng", func() bool {
		return countShimCalls(s, "piper") >= 1 &&
			strings.Contains(s.readShimLog("espeak-ng"), "stdin=Piper fallback text")
	})
}

func TestE2E_F12_T2_OpenAISpeechHTTP500AndCorruptAudioNotCached(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.QueueResponse(
		MockResponse{Body: "Speak retry"},
		MockResponse{Body: "Corrupt audio"},
	)
	m.SetDefault("speech", MockResponse{Status: http.StatusInternalServerError, Body: "tts error"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	if runtime.GOOS != "windows" {
		s.IsolatePATH = true
		s.installShim("espeak-ng")
		s.setShimState("espeak_mode", "ok")
	}

	_, client, win := ttsOpenMainPanel(t, s)
	configureTTSViaSchema(t, s, cfg, ttsConfigOpts{
		Backend: "openai",
		BaseURL: m.V1URL(),
		Model:   "tts-1",
		Voice:   "alloy",
		Format:  "mp3",
	})

	// 1. HTTP 500 must NOT be cached.
	ttsTranslateInPopup(t, client, win.ID, "Retry", "Speak retry")
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 4*time.Second, "1st /v1/audio/speech (HTTP 500)", func() bool {
		return countSpeechRequests(m) == 1
	})
	ttsPollUntil(t, 4*time.Second, "HTTP 500 fallback or error status settled", func() bool {
		if runtime.GOOS != "windows" && countShimCalls(s, "espeak-ng") >= 1 {
			return true
		}
		cCtx, cCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cCancel()
		tr, err := client.Tree(cCtx, win.ID)
		if err != nil {
			return false
		}
		return ttsTreeContains(tr, "失败") || ttsTreeContains(tr, "错误") || ttsTreeContains(tr, "500") || ttsTreeContains(tr, "不可用")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle after HTTP 500 handling: %v", err)
	}

	m.SetDefault("speech", MockResponse{Status: http.StatusOK})
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 4*time.Second, "2nd /v1/audio/speech retry after HTTP 500", func() bool {
		return countSpeechRequests(m) == 2
	})
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle after HTTP 200 retry: %v", err)
	}
	time.Sleep(350 * time.Millisecond)

	// 2. Corrupt audio bytes must fail cleanly without panic or hang, and must NOT be cached.
	m.SetDefault("speech", MockResponse{
		Status:      http.StatusOK,
		ContentType: "audio/mpeg",
		RawBody:     []byte("corrupt-audio-bytes"),
	})
	ttsTranslateInPopup(t, client, win.ID, "Corrupt", "Corrupt audio")
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 4*time.Second, "3rd /v1/audio/speech (corrupt payload)", func() bool {
		return countSpeechRequests(m) == 3
	})
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("UI failed to stay responsive after corrupt audio: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 4*time.Second, "4th /v1/audio/speech retry (corrupt audio must not be cached)", func() bool {
		return countSpeechRequests(m) == 4
	})
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle after corrupt audio replay check: %v", err)
	}
}

func TestE2E_F12_T2_SlowSynthesisCancelledOnRetranslateAndQuit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX TTS PATH shims only apply on non-Windows hosts")
	}

	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.QueueResponse(
		MockResponse{Body: "First slow result"},
		MockResponse{Body: "Retranslated result"},
	)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("espeak-ng")
	s.setShimState("espeak_mode", "slow")

	proc, client, win := ttsOpenMainPanel(t, s)
	ttsTranslateInPopup(t, client, win.ID, "Hello", "First slow result")

	// Start slow synthesis (exec /bin/sleep 5), then trigger re-translate.
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 3*time.Second, "slow espeak-ng started", func() bool {
		return countShimCalls(s, "espeak-ng") == 1
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	retranslateBtn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
	})
	if err != nil {
		t.Fatalf("WaitNode 重新翻译: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: retranslateBtn.ID, Action: "press"}); err != nil {
		t.Fatalf("press 重新翻译: %v", err)
	}
	ttsWaitTreeContains(t, client, win.ID, 4*time.Second, "Retranslated result")

	// Quit while slow TTS may still be active: OnWillQuit must cancel cleanly well below proc.Stop's 2s SIGKILL timeout.
	stopStart := time.Now()
	proc.Stop(t)
	if elapsed := time.Since(stopStart); elapsed >= 1500*time.Millisecond {
		t.Fatalf("proc.Stop took %v with active slow TTS, expected fast cancellation (< 1.5s, below SIGKILL timeout)", elapsed)
	}
}

func TestE2E_F12_T2_InvalidConfigHotReloadPreservesDraftAndTTS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX TTS PATH shims only apply on non-Windows hosts")
	}

	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "Valid after bad reload"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("espeak-ng")
	s.setShimState("espeak_mode", "ok")

	_, client, win := ttsOpenMainPanel(t, s)

	// Make settings dirty in "设置" -> "快捷键".
	ttsSwitchPanelTab(t, client, win.ID, "设置")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	navHotkeys, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "button" && n.Description == "快捷键"
	})
	if err != nil {
		t.Fatalf("WaitNode 快捷键: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: navHotkeys.ID, Action: "press"}); err != nil {
		t.Fatalf("press 快捷键: %v", err)
	}
	hotkeyField, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "textField" && n.Placeholder == "Alt+F"
	})
	if err != nil {
		t.Fatalf("WaitNode Alt+F field: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: hotkeyField.ID, Action: "setValue", Text: "Alt+Shift+Z"}); err != nil {
		t.Fatalf("setValue Alt+Shift+Z: %v", err)
	}
	ttsWaitTreeContains(t, client, win.ID, 3*time.Second, "放弃修改")

	// Write invalid config.json -> strict DisallowUnknownFields rejects reload.
	s.writeRawConfig(`{"unknownTtsField": true}`)
	ttsPollUntil(t, 4*time.Second, "reloading config error logged", func() bool {
		return strings.Contains(s.readDebugLog(), "reloading config:")
	})

	// Dirty settings draft must survive invalid hot-reload.
	trSettings, err := client.Tree(ctx, win.ID)
	if err != nil {
		t.Fatalf("Tree settings: %v", err)
	}
	if !ttsTreeContains(trSettings, "放弃修改") || !ttsTreeContains(trSettings, "Alt+Shift+Z") {
		t.Fatalf("invalid hot-reload discarded unsaved settings draft")
	}

	// Translation & TTS must continue operating with the previous valid config.
	ttsSwitchPanelTab(t, client, win.ID, "翻译")
	ttsTranslateInPopup(t, client, win.ID, "Keep working", "Valid after bad reload")
	ttsPressAudioButton(t, client, win.ID)
	ttsPollUntil(t, 4*time.Second, "espeak-ng works after invalid hot-reload", func() bool {
		return strings.Contains(s.readShimLog("espeak-ng"), "stdin=Valid after bad reload")
	})
}
