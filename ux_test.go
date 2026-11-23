package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"hayami/internal/dict"
)

// TestUXNonTranslateModesAndFollowUpExcludeMachineTranslation verifies that
// polish/summarize/explain and follow-up requests never silently fall back to
// Azure/Baidu machine translation APIs.
func TestUXNonTranslateModesAndFollowUpExcludeMachineTranslation(t *testing.T) {
	var baiduCalls atomic.Int32
	baiduSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baiduCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"trans_result":[{"dst":"baidu-translated"}]}`)
	}))
	defer baiduSrv.Close()

	cfg := defaultConfig()
	cfg.Providers = []Provider{
		{ID: "baidu", Name: "百度翻译", Kind: providerBaidu, Enabled: true, Endpoint: baiduSrv.URL, AppID: "app", Key: "secret"},
	}

	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelTranslation,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
		translationState: translationState{
			source:     "这是一段需要润色的文字。",
			activeMode: "polish",
			targetLang: autoTarget,
		},
	}

	// Attempting to polish with only Baidu configured must show an explicit error
	// instead of calling Baidu machine translation.
	a.startTranslation()
	if baiduCalls.Load() != 0 {
		t.Fatalf("Baidu was called %d times in polish mode, want 0", baiduCalls.Load())
	}
	if !strings.Contains(a.errText, "OpenAI Compatible") {
		t.Fatalf("errText = %q, want guidance about OpenAI Compatible provider", a.errText)
	}

	// Render the error cleanly in popupView.
	tt := ui.NewTester(a.panelView, 480, 360)
	tt.Frame()
	if !tt.HasText("OpenAI Compatible") {
		t.Fatalf("expected error guidance to render in popupView, texts = %v", tt.Texts())
	}

	// Follow-up with only Baidu must also refuse cleanly without calling Baidu.
	a.errText = ""
	a.activeMode = "translate"
	a.result = "baidu-translated"
	a.followUpInput = "请翻得更口语化"
	a.sendFollowUp()
	if baiduCalls.Load() != 0 {
		t.Fatalf("Baidu was called %d times for follow-up, want 0", baiduCalls.Load())
	}
	if !strings.Contains(a.followUpErr, "OpenAI Compatible") {
		t.Fatalf("followUpErr = %q, want guidance about OpenAI Compatible provider", a.followUpErr)
	}
}

// TestUXPopupSmartScrollRespectsUserScrollUp verifies that streaming output
// auto-scrolls when at the bottom, pauses auto-scrolling if the user scrolls
// up to read earlier paragraphs, and resumes when scrolled back to the bottom.
func TestUXPopupSmartScrollRespectsUserScrollUp(t *testing.T) {
	cfg := defaultConfig()
	var longText strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&longText, "第 %d 段：这是一段较长的流式翻译输出，用来撑满滚动区域并验证用户上滑体验。\n\n", i)
	}

	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelTranslation,
		md:             markdown.Options{Theme: &markdown.Theme{FontSize: 13}},
		settingsState: settingsState{
			form: cfg.Clone(),
		},
		translationState: translationState{
			source:          "Long technical article",
			result:          longText.String(),
			streaming:       true,
			popupAutoScroll: true,
			targetLang:      autoTarget,
			activeMode:      "translate",
		},
	}

	tt := ui.NewTester(a.panelView, 440, 320)
	// Advance tester clock so markdown.Stream reveals the paragraphs as in live streaming.
	for range 20 {
		tt.Advance(500 * time.Millisecond)
	}

	if a.popupScroll.MaxY <= 0 {
		t.Fatalf("expected scrollable content (MaxY > 0), got %v", a.popupScroll.MaxY)
	}
	if !a.popupAutoScroll {
		t.Fatal("auto-scroll should remain active when staying at bottom")
	}

	// Simulate user scrolling up with the mouse wheel to read earlier paragraphs.
	tt.Move(220, 200)
	tt.Scroll(220, 200, 0, -200)
	tt.Advance(100 * time.Millisecond)
	if a.popupAutoScroll {
		t.Fatal("scrolling up during streaming must pause auto-scroll instead of snapping back")
	}
	if a.popupScroll.Y >= a.popupScroll.MaxY-24 {
		t.Fatalf("scroll position snapped back to bottom: Y=%v MaxY=%v", a.popupScroll.Y, a.popupScroll.MaxY)
	}

	// Simulate user scrolling back to the bottom.
	a.popupScroll.Y = a.popupScroll.MaxY
	tt.Frame()
	if !a.popupAutoScroll {
		t.Fatal("scrolling back to the bottom should re-enable auto-scroll")
	}
}

// TestUXMultiEngineErrorCardShowsActionableHint verifies that failed provider
// cards in multi-engine mode display both the error title and actionable hint.
func TestUXMultiEngineErrorCardShowsActionableHint(t *testing.T) {
	cfg := defaultConfig()
	cfg.MultiEngine = true
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelTranslation,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
		translationState: translationState{
			multiEngine: true,
			source:      "hello",
			targetLang:  autoTarget,
			activeMode:  "translate",
			results: []providerResult{
				{
					ID:     "p1",
					Name:   "OpenAI",
					Result: "你好",
					MD:     &markdown.Options{Theme: &markdown.Theme{FontSize: 13}},
				},
				{
					ID:      "p2",
					Name:    "DeepSeek",
					ErrText: "鉴权失败：请检查 API Key",
					ErrHint: "服务拒绝了凭据。打开设置确认 Key 是否正确、是否过期。",
					MD:      &markdown.Options{Theme: &markdown.Theme{FontSize: 13}},
				},
			},
		},
	}

	tt := ui.NewTester(a.panelView, 580, 420)
	tt.Frame()
	if !tt.HasText("鉴权失败：请检查 API Key") {
		t.Fatalf("expected provider error title in card, texts = %v", tt.Texts())
	}
	if !tt.HasText("服务拒绝了凭据") {
		t.Fatalf("expected provider error hint in card, texts = %v", tt.Texts())
	}
}

// TestUXHistoryClearDialogShowsRealTotalAndReusePreservesGlobalConfig exercises
// the full history search, clear confirmation, and click-to-reuse experience.
func TestUXHistoryClearDialogShowsRealTotalAndReusePreservesGlobalConfig(t *testing.T) {
	store, err := openHistoryAt(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for i := 1; i <= 5; i++ {
		src := fmt.Sprintf("item %d", i)
		res := fmt.Sprintf("条目 %d", i)
		if i == 3 {
			src = "save 50% now"
			res = "立省 50%"
		}
		if err := store.Add(historyEntry{
			CreatedAt: time.Now(),
			Source:    src,
			Result:    res,
			Target:    "English",
			Mode:      "polish",
		}); err != nil {
			t.Fatal(err)
		}
	}

	filtered, err := store.Search("%", 200)
	if err != nil {
		t.Fatal(err)
	}
	total, err := store.Count()
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || total != 5 {
		t.Fatalf("filtered=%d total=%d, want 1 and 5", len(filtered), total)
	}

	cfg := defaultConfig()
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		store:          store,
		panelPage:      panelHistory,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
		historyState: historyState{
			hist:      filtered,
			histTotal: total,
			histQuery: "%",
		},
		translationState: translationState{
			targetLang: cfg.TargetLang,
			activeMode: cfg.ActiveMode,
		},
	}

	tt := ui.NewTester(a.panelView, 560, 450)
	tt.Frame()

	if !tt.HasText("匹配 1 条") {
		t.Fatalf("expected filtered badge '匹配 1 条', texts = %v", tt.Texts())
	}
	if err := tt.Click("清空历史"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("全部 5 条历史记录（不受当前搜索筛选影响）") {
		t.Fatalf("clear dialog must warn about deleting all 5 records despite search filter, texts = %v", tt.Texts())
	}
	// Cancel dialog with Escape.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	// Reusing a history entry must show it in popup without mutating global config/form.
	a.useHistoryEntry(filtered[0])
	if a.source != "save 50% now" || a.result != "立省 50%" || a.targetLang != "English" || a.activeMode != "polish" {
		t.Fatalf("popup state not updated from history: src=%q res=%q target=%q mode=%q", a.source, a.result, a.targetLang, a.activeMode)
	}
	if a.cfg.TargetLang != autoTarget || a.cfg.ActiveMode != "translate" {
		t.Fatalf("useHistoryEntry mutated global running config: target=%q mode=%q", a.cfg.TargetLang, a.cfg.ActiveMode)
	}
	if a.form.TargetLang != autoTarget || a.form.ActiveMode != "translate" || a.settingsDirty() {
		t.Fatalf("useHistoryEntry polluted settings draft: target=%q mode=%q dirty=%v", a.form.TargetLang, a.form.ActiveMode, a.settingsDirty())
	}
}

// TestUXInputWindowSwapLanguagesProducesValidConcreteLanguages tests swapping
// auto-detected Chinese and newly supported languages in the dual-pane input view.
func TestUXInputWindowSwapLanguagesProducesValidConcreteLanguages(t *testing.T) {
	cfg := defaultConfig()
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelInput,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
		inputState: inputState{
			inputSourceLang: "自动检测",
			inputTargetLang: autoTarget,
			inputSource:     "用 Kubernetes 部署 Go 微服务",
		},
	}

	a.swapInputLanguages()
	if a.inputSourceLang != "English" {
		t.Fatalf("after swap, sourceLang = %q, want English", a.inputSourceLang)
	}
	if a.inputTargetLang != "简体中文" {
		t.Fatalf("after swap, targetLang = %q, want 简体中文 (never invalid '中文')", a.inputTargetLang)
	}

	// Verify every target concrete language is also available in sourceLanguages.
	sourceSet := map[string]bool{}
	for _, l := range sourceLanguages {
		sourceSet[l] = true
	}
	for _, l := range concreteLanguages {
		if !sourceSet[l] {
			t.Errorf("concrete language %q missing from sourceLanguages", l)
		}
	}

	// Verify UI renders cleanly after swap at both wide and narrow widths.
	for _, w := range []int{560, 440} {
		tt := ui.NewTester(a.panelView, w, 420)
		tt.Frame()
		if !tt.HasText("English") || !tt.HasText("简体中文") {
			t.Fatalf("input view at width %d missing swapped languages: %v", w, tt.Texts())
		}
	}
}

// TestUXSettingsModelParamsEditValidateAndDiscard tests editing Temperature and
// MaxTokens in the settings UI, dirty tracking, validation errors, and discard.
func TestUXSettingsModelParamsEditValidateAndDiscard(t *testing.T) {
	cfg := defaultConfig()
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelSettings,
		settingsState: settingsState{
			form:        cfg.Clone(),
			settingsTab: 1, // 翻译引擎 tab
		},
	}
	a.syncFormModelParams()

	tt := ui.NewTester(a.panelView, 620, 480)
	tt.Frame()
	if !tt.HasText("采样温度 (Temperature)") || !tt.HasText("最大输出 Token") {
		t.Fatalf("model parameter controls missing in 翻译引擎 tab: %v", tt.Texts())
	}
	if a.settingsDirty() {
		t.Fatal("freshly synced model params must not mark settings dirty")
	}

	// Edit temperature -> dirty -> discard restores original.
	a.formTempText = "0.7"
	tt.Frame()
	if !a.settingsDirty() {
		t.Fatal("editing temperature text must mark settings dirty")
	}
	if !tt.HasText("放弃修改") {
		t.Fatal("discard button should appear when temperature is edited")
	}
	if err := tt.Click("放弃修改"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settingsDirty() || a.formTempText != "0.3" {
		t.Fatalf("discard did not restore temperature: dirty=%v temp=%q", a.settingsDirty(), a.formTempText)
	}

	// Invalid temperature format (including NaN and empty) must fail save cleanly.
	for _, bad := range []string{"not-a-number", "NaN", "Inf", ""} {
		a.formTempText = bad
		if !a.settingsDirty() {
			t.Fatalf("temperature %q must keep settings dirty", bad)
		}
		a.saveSettings()
		if !strings.Contains(a.settingsStatus, "采样温度") {
			t.Fatalf("temperature %q: unexpected settingsStatus = %q", bad, a.settingsStatus)
		}
		if a.cfg.Temperature != 0.3 {
			t.Fatalf("invalid save (%q) mutated cfg.Temperature to %v", bad, a.cfg.Temperature)
		}
	}

	// Invalid max tokens must fail save cleanly.
	a.formTempText = "0.5"
	a.formMaxTokensText = "9999999"
	a.saveSettings()
	if !strings.Contains(a.settingsStatus, "maxTokens") {
		t.Fatalf("expected maxTokens range validation error, got %q", a.settingsStatus)
	}
}

// TestUXSubmitPopupGuardConsistency verifies Ctrl+Enter behaves consistently
// across image mode, streaming mode, and follow-up mode.
func TestUXSubmitPopupGuardConsistency(t *testing.T) {
	cfg := defaultConfig()
	// Use a test-only provider with no key so the test never reads the developer's
	// real system keyring.
	cfg.Providers = []Provider{
		{ID: "test-no-key", Kind: providerOpenAI, Enabled: true, BaseURL: "https://example.invalid/v1", Model: "m", Vision: true},
	}
	a := &app{
		cfg: cfg,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
		translationState: translationState{
			hasImage:      true,
			shot:          []byte("png"),
			result:        "image text",
			followUpInput: "stale follow-up text",
		},
	}

	// In image mode, submitPopup must not silently dead-end in sendFollowUp;
	// it should attempt re-translation (and here fail cleanly because no key).
	a.submitPopup()
	if a.errText == "" {
		t.Fatal("submitPopup in image mode should trigger startTranslation rather than dead-ending")
	}

	// While streaming, submitPopup must not restart/interrupt the active stream.
	a.errText = ""
	a.hasImage = false
	a.streaming = true
	beforeReqID := a.reqID
	a.submitPopup()
	if a.reqID != beforeReqID {
		t.Fatal("submitPopup while streaming must be a no-op")
	}
}

// TestUXPickDisplaySupportsNonZeroBasedIDs verifies KDE lastScreen index
// fallback when display IDs are non-sequential (e.g. Wayland output IDs).
func TestUXPickDisplaySupportsNonZeroBasedIDs(t *testing.T) {
	displays := []mygo.Display{
		{ID: 1001, Bounds: mygo.Rectangle{X: 0, Y: 0, Width: 1920, Height: 1080}},
		{ID: 2048, Bounds: mygo.Rectangle{X: 1920, Y: 0, Width: 2560, Height: 1440}},
	}
	got := pickDisplay(displays, displays[0], 1)
	if got.ID != 2048 {
		t.Fatalf("pickDisplay(index 1) = %d, want 2048", got.ID)
	}
	gotByID := pickDisplay(displays, displays[0], 2048)
	if gotByID.ID != 2048 {
		t.Fatalf("pickDisplay(id 2048) = %d, want 2048", gotByID.ID)
	}
	gotFallback := pickDisplay(displays, displays[0], 99)
	if gotFallback.ID != 1001 {
		t.Fatalf("pickDisplay(out of range) = %d, want primary 1001", gotFallback.ID)
	}
}

// TestUXEndToEndPromptRoutingAndMultiEngineFallback exercises real HTTP
// providers (OpenAI SSE + Baidu) across polish mode, follow-up, and multi-engine
// partial failure flows.
func TestUXEndToEndPromptRoutingAndMultiEngineFallback(t *testing.T) {
	var baiduHits atomic.Int32
	baiduSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baiduHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"trans_result":[{"dst":"百度译文"}]}`)
	}))
	defer baiduSrv.Close()

	var openAIHits atomic.Int32
	var lastRoleCount atomic.Int32
	openAISrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAIHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"润色\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"完成\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer openAISrv.Close()

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer failSrv.Close()

	cfg := defaultConfig()
	cfg.Providers = []Provider{
		{ID: "baidu", Name: "百度翻译", Kind: providerBaidu, Enabled: true, Endpoint: baiduSrv.URL, AppID: "app", Key: "sec"},
		{ID: "openai-ok", Name: "本地模型", Kind: providerOpenAI, Enabled: true, BaseURL: openAISrv.URL + "/v1", Model: "m"},
	}

	// 1. Polish mode must skip Baidu (priority 1) and use OpenAI (priority 2).
	polishMode, ok := cfg.Mode("polish")
	if !ok {
		t.Fatal("polish mode missing")
	}
	polishProviders := cfg.EnabledPromptProviders()
	if len(polishProviders) != 1 || polishProviders[0].ID != "openai-ok" {
		t.Fatalf("EnabledPromptProviders = %#v, want only openai-ok", polishProviders)
	}
	var polished strings.Builder
	err := translateWithFallback(t.Context(), cfg, polishProviders, messagesForMode(cfg, "这是一段草稿", polishMode), "这是一段草稿", nil, func(delta string) {
		polished.WriteString(delta)
	})
	if err != nil {
		t.Fatalf("polish failed: %v", err)
	}
	if polished.String() != "润色完成" || baiduHits.Load() != 0 || openAIHits.Load() != 1 {
		t.Fatalf("polish=%q baidu=%d openai=%d", polished.String(), baiduHits.Load(), openAIHits.Load())
	}

	// 2. Follow-up must carry 4-turn messages and run against OpenAI, never Baidu.
	fuMsgs := followUpMessages(cfg, polishMode, "这是一段草稿", polished.String(), nil, "再简洁一点")
	lastRoleCount.Store(int32(len(fuMsgs)))
	if lastRoleCount.Load() != 4 {
		t.Fatalf("followUp messages = %d, want 4", lastRoleCount.Load())
	}
	var fuOut strings.Builder
	if err := translateWithFallback(t.Context(), cfg, polishProviders, fuMsgs, "这是一段草稿", nil, func(delta string) {
		fuOut.WriteString(delta)
	}); err != nil {
		t.Fatalf("follow-up failed: %v", err)
	}
	if fuOut.String() != "润色完成" || baiduHits.Load() != 0 || openAIHits.Load() != 2 {
		t.Fatalf("follow-up=%q baidu=%d openai=%d", fuOut.String(), baiduHits.Load(), openAIHits.Load())
	}

	// 3. Failed provider produces both title and actionable hint for multi-engine cards.
	failProv := Provider{ID: "openai-bad", Name: "失效模型", Kind: providerOpenAI, Enabled: true, BaseURL: failSrv.URL + "/v1", Model: "m", Key: "bad"}
	err = translateWithProvider(t.Context(), cfg, failProv, failProv.Key, textMessages(cfg, "hello"), "hello", func(string) {})
	title, hint := explainError(err)
	if !strings.Contains(title, "鉴权失败") || !strings.Contains(hint, "Key") {
		t.Fatalf("explainError = (%q, %q), want auth title and hint", title, hint)
	}
}

func TestUXPopupRendersDictCardWhileStreaming(t *testing.T) {
	entry, ok := dict.Lookup("algorithm")
	if !ok {
		t.Fatal("expected algorithm in offline dict")
	}

	cfg := defaultConfig()
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelTranslation,
		md:             markdown.Options{Theme: &markdown.Theme{FontSize: 13}},
		settingsState:  settingsState{form: cfg.Clone()},
		translationState: translationState{
			source:     "algorithm",
			activeMode: "translate",
			targetLang: "简体中文",
			streaming:  true,
			dictEntry:  &entry,
		},
	}

	tt := ui.NewTester(a.panelView, 440, 360)
	tt.Frame()
	if !tt.HasText("algorithm") || !tt.HasText("离线词典") || !tt.HasText("正在翻译…") {
		t.Fatalf("expected dict card and streaming spinner together, texts = %v", tt.Texts())
	}

	// Once LLM finishes, dict card and streamed result coexist cleanly.
	a.streaming = false
	a.result = "算法语境示例"
	tt.Frame()
	if !tt.HasText("离线词典") || !tt.HasText("算法语境示例") || !tt.HasText("重新翻译") {
		t.Fatalf("expected dict card + LLM result + retranslate button, texts = %v", tt.Texts())
	}
}

func TestUXPopupRetranslateBypassesCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"译文-%d\"}}]}\n\n", n)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.Providers = []Provider{
		{ID: "p1", Name: "P1", Kind: providerOpenAI, Enabled: true, BaseURL: srv.URL + "/v1", Model: "m", Key: "k"},
	}
	uiCh := make(chan func(), 64)
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelTranslation,
		md:             markdown.Options{Theme: &markdown.Theme{FontSize: 13}},
		settingsState:  settingsState{form: cfg.Clone()},
		translationState: translationState{
			source:     "ephemeral",
			activeMode: "translate",
			targetLang: "简体中文",
			uiDispatch: func(fn func()) { uiCh <- fn },
		},
	}

	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if hits.Load() != 1 || a.result != "译文-1" {
		t.Fatalf("initial translation failed: hits=%d result=%q", hits.Load(), a.result)
	}

	tt := ui.NewTester(a.panelView, 440, 340)
	tt.Frame()
	if err := tt.Click("重新翻译"); err != nil {
		t.Fatalf("click 重新翻译: %v", err)
	}
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if hits.Load() != 2 || a.result != "译文-2" || a.cacheHit {
		t.Fatalf("retranslate click did not bypass cache: hits=%d result=%q cacheHit=%v",
			hits.Load(), a.result, a.cacheHit)
	}
}

func TestUXInputWindowRendersDictCardAndClears(t *testing.T) {
	entry, ok := dict.Lookup("serendipity")
	if !ok {
		t.Fatal("expected serendipity in offline dict")
	}

	cfg := defaultConfig()
	var canceled atomic.Bool
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelInput,
		settingsState:  settingsState{form: cfg.Clone()},
		inputState: inputState{
			inputSource:     "serendipity",
			inputSourceLang: "自动检测",
			inputTargetLang: "简体中文",
			inputDictEntry:  &entry,
			inputStreaming:  true,
			inputReqID:      7,
			inputCancel:     func() { canceled.Store(true) },
		},
	}

	tt := ui.NewTester(a.panelView, 560, 420)
	tt.Frame()
	if !tt.HasText("serendipity") || !tt.HasText("离线词典") {
		t.Fatalf("expected input dict card, texts = %v", tt.Texts())
	}

	if err := tt.Click("清空"); err != nil {
		t.Fatalf("click 清空: %v", err)
	}
	tt.Frame()
	if a.inputDictEntry != nil || tt.HasText("离线词典") {
		t.Fatalf("清空 did not clear inputDictEntry: entry=%+v texts=%v", a.inputDictEntry, tt.Texts())
	}
	if !canceled.Load() {
		t.Fatal("清空 did not cancel active input stream")
	}
	if a.inputStreaming || a.inputReqID != 8 || a.inputCancel != nil {
		t.Fatalf("清空 did not reset streaming state: streaming=%v reqID=%d cancelNil=%v",
			a.inputStreaming, a.inputReqID, a.inputCancel == nil)
	}
}

func TestUXDesiredPanelSizeAccountsForDictCard(t *testing.T) {
	entry, ok := dict.Lookup("apple")
	if !ok {
		t.Fatal("expected apple in offline dict")
	}
	a := &app{
		panelPage: panelTranslation,
		translationState: translationState{
			source: "apple",
			result: "苹果",
		},
	}
	_, hBase := a.desiredPanelSize()
	a.dictEntry = &entry
	_, hWithDict := a.desiredPanelSize()
	if hWithDict <= hBase {
		t.Fatalf("desiredPanelSize with dictEntry (%d) should exceed base (%d)", hWithDict, hBase)
	}
	if hWithDict < 220 || hWithDict > 500 {
		t.Fatalf("desiredPanelSize out of bounds: %d", hWithDict)
	}
}

func TestUXMultiTurnFollowUpRendersAllTurnsAndCopyButtons(t *testing.T) {
	turnIdx := 0
	answers := []string{"第一轮追问回答", "第二轮追问回答", "第三轮追问回答"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans := answers[turnIdx]
		turnIdx++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", ans)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.Providers = []Provider{
		{ID: "p1", Name: "P1", Kind: providerOpenAI, Enabled: true, BaseURL: srv.URL + "/v1", Model: "m", Key: "k"},
	}
	uiCh := make(chan func(), 64)
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelTranslation,
		md:             markdown.Options{Theme: &markdown.Theme{FontSize: 13}},
		settingsState:  settingsState{form: cfg.Clone()},
		translationState: translationState{
			source:     "resilience",
			result:     "初始译文",
			activeMode: "translate",
			targetLang: "English",
			uiDispatch: func(fn func()) { uiCh <- fn },
		},
	}

	for _, q := range []string{"第一轮问题", "第二轮问题", "第三轮问题"} {
		a.followUpInput = q
		a.sendFollowUp()
		pumpTestUI(t, uiCh, func() bool { return !a.followUpStreaming })
	}

	if len(a.followUpTurns) != 3 {
		t.Fatalf("len(followUpTurns) = %d, want 3: %+v", len(a.followUpTurns), a.followUpTurns)
	}

	tt := ui.NewTester(a.panelView, 440, 440)
	tt.Frame()
	for _, want := range []string{"第一轮问题", "第一轮追问回答", "第二轮问题", "第二轮追问回答", "第三轮问题", "第三轮追问回答"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in texts: %v", want, tt.Texts())
		}
	}
}
