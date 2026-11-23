package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hayami/internal/dict"
	"hayami/internal/lang"
)

func TestTranslationStateZeroValueAndCancelStream(t *testing.T) {
	a := &app{}
	if a.hasAnyResult() {
		t.Fatal("zero-value app should not have any result")
	}
	if a.canFollowUp() {
		t.Fatal("zero-value app should not allow follow-up")
	}
	if a.effectivePopupTarget() != autoTarget {
		t.Fatalf("zero-value effectivePopupTarget() = %q, want %q", a.effectivePopupTarget(), autoTarget)
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.streaming = true
	beforeReqID := a.reqID
	a.cancelStream()

	if a.reqID != beforeReqID+1 {
		t.Fatalf("reqID = %d, want %d", a.reqID, beforeReqID+1)
	}
	if ctx.Err() == nil {
		t.Fatal("cancelStream must invoke active cancel function")
	}
	if a.cancel != nil || a.streaming {
		t.Fatalf("cancelStream did not reset stream state: cancel=%v streaming=%v", a.cancel != nil, a.streaming)
	}
}

func TestTranslationStateResetAndTargetFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fuCtx, fuCancel := context.WithCancel(context.Background())
	a := &app{
		cfg: Config{TargetLang: "日本語"},
		translationState: translationState{
			source:         "hello",
			result:         "你好",
			results:        []providerResult{{ID: "p1", Result: "你好"}},
			errText:        "err",
			errHint:        "hint",
			status:         "status",
			hasImage:       true,
			shot:           []byte("png"),
			popupEditing:   true,
			streaming:      true,
			cancel:         cancel,
			followUpInput:  "q",
			followUpQ:      "q",
			followUp:       "a",
			followUpErr:    "e",
			followUpCancel: fuCancel,
		},
	}

	if got := a.effectivePopupTarget(); got != "日本語" {
		t.Fatalf("effectivePopupTarget() = %q, want 日本語", got)
	}
	a.targetLang = "English"
	if got := a.effectivePopupTarget(); got != "English" {
		t.Fatalf("effectivePopupTarget() with override = %q, want English", got)
	}

	a.resetTranslation()
	if ctx.Err() == nil || fuCtx.Err() == nil {
		t.Fatal("resetTranslation must cancel both main and follow-up contexts")
	}
	if a.source != "" || a.result != "" || a.results != nil || a.errText != "" || a.errHint != "" || a.status != "" || a.hasImage || a.shot != nil || a.popupEditing {
		t.Fatalf("resetTranslation left translation fields populated: %+v", a.translationState)
	}
	if a.followUpInput != "" || a.followUpQ != "" || a.followUp != "" || a.followUpErr != "" {
		t.Fatalf("resetTranslation left follow-up fields populated: %+v", a.translationState)
	}
}

func TestSettingsStateSyncFormModelParams(t *testing.T) {
	cfg := defaultConfig()
	cfg.Temperature = 0.75
	cfg.MaxTokens = 4096

	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
	}
	a.syncFormModelParams()

	if !a.formModelParamsSynced {
		t.Fatal("syncFormModelParams must set formModelParamsSynced")
	}
	if a.formTempText != "0.75" {
		t.Fatalf("formTempText = %q, want 0.75", a.formTempText)
	}
	if a.formMaxTokensText != "4096" {
		t.Fatalf("formMaxTokensText = %q, want 4096", a.formMaxTokensText)
	}
	if a.settingsDirty() {
		t.Fatal("freshly synced settings state must not be dirty")
	}
}

func TestInputAndHistoryStateZeroValueSafety(t *testing.T) {
	a := &app{}
	a.reloadHistory()
	if a.hist != nil || a.histTotal != 0 || a.histStatus != "" {
		t.Fatalf("reloadHistory with nil store altered zero-value historyState: %+v", a.historyState)
	}
	if a.inputSource != "" || a.inputResult != "" || a.inputStreaming {
		t.Fatalf("zero-value inputState unexpectedly populated: %+v", a.inputState)
	}
}

func pumpTestUI(t *testing.T, uiCh <-chan func(), cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond() {
		select {
		case fn := <-uiCh:
			fn()
		case <-deadline:
			t.Fatal("timed out waiting for async translation UI update")
		}
	}
	for {
		select {
		case fn := <-uiCh:
			fn()
		default:
			return
		}
	}
}

func TestTranslationStateDictAndCacheZeroValueReset(t *testing.T) {
	a := &app{}
	if a.dictEntry != nil || a.cacheHit || a.resultCache != nil || a.inputDictEntry != nil || a.inputCacheHit {
		t.Fatal("zero-value app should have zero dict/cache fields")
	}
	entry, ok := dict.Lookup("hello")
	if !ok {
		t.Fatal("expected hello in offline dict")
	}
	a.dictEntry = &entry
	a.cacheHit = true
	a.resetTranslation()
	if a.dictEntry != nil || a.cacheHit {
		t.Fatalf("resetTranslation did not clear dictEntry/cacheHit: dict=%v cacheHit=%v", a.dictEntry, a.cacheHit)
	}
}

func TestTieredWordLookupAndConcurrentLLM(t *testing.T) {
	release := make(chan struct{})
	inFlight := make(chan struct{}, 1)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			select {
			case inFlight <- struct{}{}:
			default:
			}
			<-release
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"算法语境释义\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.Providers = []Provider{
		{ID: "p1", Name: "TestOpenAI", Kind: providerOpenAI, Enabled: true, BaseURL: srv.URL + "/v1", Model: "m", Key: "k"},
	}

	uiCh := make(chan func(), 64)
	a := &app{
		cfg: cfg,
		translationState: translationState{
			source:     "algorithm",
			activeMode: "translate",
			targetLang: "简体中文",
			uiDispatch: func(fn func()) { uiCh <- fn },
		},
	}

	// Single word in translate mode: offline dict entry is populated immediately
	// BEFORE the LLM response arrives.
	a.startTranslation()
	if a.dictEntry == nil || a.dictEntry.Word != "algorithm" || a.dictEntry.Phonetic == "" || len(a.dictEntry.Meanings) == 0 {
		t.Fatalf("expected immediate offline dict entry for algorithm, got %+v", a.dictEntry)
	}
	if !a.streaming {
		t.Fatal("expected LLM stream to be active concurrently")
	}
	select {
	case <-inFlight:
	case <-time.After(3 * time.Second):
		t.Fatal("LLM request did not start")
	}
	if a.result != "" {
		t.Fatalf("expected empty LLM result while blocked, got %q", a.result)
	}
	close(release)
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if a.result != "算法语境释义" {
		t.Fatalf("final result = %q, want 算法语境释义", a.result)
	}

	// Non-translate mode (polish) bypasses offline dictionary.
	a.activeMode = "polish"
	a.source = "algorithm"
	a.startTranslation()
	if a.dictEntry != nil {
		t.Fatalf("non-translate mode must bypass offline dict, got %+v", a.dictEntry)
	}
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })

	// Multi-word / paragraph bypasses offline dictionary.
	a.activeMode = "translate"
	a.source = "hello world"
	a.startTranslation()
	if a.dictEntry != nil {
		t.Fatalf("multi-word input must bypass offline dict, got %+v", a.dictEntry)
	}
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })

	// Unknown word miss falls back gracefully to LLM without error.
	a.source = "zxqvbnm"
	a.startTranslation()
	if a.dictEntry != nil {
		t.Fatalf("unknown word should leave dictEntry nil, got %+v", a.dictEntry)
	}
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if a.result != "算法语境释义" || a.errText != "" {
		t.Fatalf("dict miss fallback failed: result=%q err=%q", a.result, a.errText)
	}
}

func TestInputStateTieredDictAndCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"朝生暮死的\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.Providers = []Provider{
		{ID: "p1", Name: "TestOpenAI", Kind: providerOpenAI, Enabled: true, BaseURL: srv.URL + "/v1", Model: "m", Key: "k"},
	}
	uiCh := make(chan func(), 64)
	a := &app{
		cfg: cfg,
		translationState: translationState{
			uiDispatch: func(fn func()) { uiCh <- fn },
		},
		inputState: inputState{
			inputSource:     "ephemeral",
			inputSourceLang: "自动检测",
			inputTargetLang: "简体中文",
		},
	}

	a.startInputTranslation()
	if a.inputDictEntry == nil || a.inputDictEntry.Word != "ephemeral" {
		t.Fatalf("expected inputDictEntry for ephemeral, got %+v", a.inputDictEntry)
	}
	pumpTestUI(t, uiCh, func() bool { return !a.inputStreaming })
	if a.inputResult != "朝生暮死的" || hits.Load() != 1 {
		t.Fatalf("inputResult=%q hits=%d", a.inputResult, hits.Load())
	}

	// Repeat identical call hits shared cache without calling HTTP.
	a.startInputTranslation()
	if a.inputStreaming || !a.inputCacheHit || a.inputStatus != "已命中缓存" || hits.Load() != 1 {
		t.Fatalf("expected input cache hit: streaming=%v cacheHit=%v status=%q hits=%d",
			a.inputStreaming, a.inputCacheHit, a.inputStatus, hits.Load())
	}

	// Multi-word input bypasses offline dict.
	a.inputSource = "good morning"
	a.startInputTranslation()
	if a.inputDictEntry != nil {
		t.Fatalf("multi-word input should bypass dict, got %+v", a.inputDictEntry)
	}
	pumpTestUI(t, uiCh, func() bool { return !a.inputStreaming })
}

func TestVisionCompressionInStartTranslation(t *testing.T) {
	var capturedDataURI string
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && len(body.Messages) >= 2 {
			var parts []struct {
				Type     string `json:"type"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			if json.Unmarshal(body.Messages[1].Content, &parts) == nil {
				for _, p := range parts {
					if p.Type == "image_url" && p.ImageURL != nil {
						capturedDataURI = p.ImageURL.URL
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"截图译文\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 2400, 1200))); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	cfg.Providers = []Provider{
		{ID: "p1", Name: "Vision", Kind: providerOpenAI, Enabled: true, BaseURL: srv.URL + "/v1", Model: "gpt-4o", Vision: true, Key: "k"},
	}
	uiCh := make(chan func(), 64)
	a := &app{
		cfg: cfg,
		translationState: translationState{
			hasImage:   true,
			shot:       pngBuf.Bytes(),
			targetLang: "简体中文",
			uiDispatch: func(fn func()) { uiCh <- fn },
		},
	}

	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })

	if a.dictEntry != nil {
		t.Fatal("image translation must not populate dictEntry")
	}
	if !strings.HasPrefix(capturedDataURI, "data:image/jpeg;base64,") {
		t.Fatalf("expected JPEG data URI, got prefix %q", capturedDataURI[:min(40, len(capturedDataURI))])
	}
	rawB64 := strings.TrimPrefix(capturedDataURI, "data:image/jpeg;base64,")
	jpgBytes, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	decoded, err := jpeg.DecodeConfig(bytes.NewReader(jpgBytes))
	if err != nil {
		t.Fatalf("jpeg.DecodeConfig: %v", err)
	}
	if decoded.Width != 1600 || decoded.Height != 800 {
		t.Fatalf("compressed dimensions = %dx%d, want 1600x800", decoded.Width, decoded.Height)
	}

	// Corrupt screenshot bytes fail cleanly without calling HTTP.
	a.shot = []byte("not-an-image")
	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if a.errText != "截图预处理失败" || hits.Load() != 1 {
		t.Fatalf("expected preprocessing error without HTTP call: errText=%q hits=%d", a.errText, hits.Load())
	}
}

func TestTranslationCacheHitBypassAndErrorIsolation(t *testing.T) {
	var hits atomic.Int32
	var failNext atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if failNext.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"server error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"结果-%d\"}}]}\n\n", n)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.Providers = []Provider{
		{ID: "p1", Name: "P1", Kind: providerOpenAI, Enabled: true, BaseURL: srv.URL + "/v1", Model: "m1", Key: "k"},
	}
	uiCh := make(chan func(), 64)
	a := &app{
		cfg: cfg,
		translationState: translationState{
			source:     "algorithm",
			activeMode: "translate",
			targetLang: lang.AutoTarget,
			uiDispatch: func(fn func()) { uiCh <- fn },
		},
	}

	// 1. First call populates cache.
	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if hits.Load() != 1 || a.result != "结果-1" {
		t.Fatalf("first call: hits=%d result=%q", hits.Load(), a.result)
	}

	// 2. Identical repeat hits cache (no HTTP) and keeps offline dictEntry.
	a.startTranslation()
	if a.streaming || hits.Load() != 1 || !a.cacheHit || a.status != "已命中缓存" || a.result != "结果-1" {
		t.Fatalf("cache hit failed: streaming=%v hits=%d cacheHit=%v status=%q result=%q",
			a.streaming, hits.Load(), a.cacheHit, a.status, a.result)
	}
	if a.dictEntry == nil || a.dictEntry.Word != "algorithm" {
		t.Fatalf("cache hit should still populate dictEntry, got %+v", a.dictEntry)
	}

	// 3. Changing model invalidates key -> misses cache.
	a.cfg.Providers[0].Model = "m2"
	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if hits.Load() != 2 || a.result != "结果-2" {
		t.Fatalf("model change miss: hits=%d result=%q", hits.Load(), a.result)
	}

	// 4. Retranslate bypasses cache and updates cached result.
	a.retranslate()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if hits.Load() != 3 || a.result != "结果-3" || a.cacheHit {
		t.Fatalf("retranslate: hits=%d result=%q cacheHit=%v", hits.Load(), a.result, a.cacheHit)
	}
	a.startTranslation()
	if hits.Load() != 3 || a.result != "结果-3" || !a.cacheHit {
		t.Fatalf("post-retranslate cache hit: hits=%d result=%q cacheHit=%v", hits.Load(), a.result, a.cacheHit)
	}

	// 5. Failed request must not populate cache.
	failNext.Store(true)
	a.source = "ephemeral"
	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if a.errText == "" {
		t.Fatal("expected error on HTTP 500")
	}
	failNext.Store(false)
	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if a.cacheHit || a.result != "结果-5" {
		t.Fatalf("failed run should not have cached: cacheHit=%v result=%q", a.cacheHit, a.result)
	}

	// 6. Multi-engine partial failure is NOT cached; full success IS cached.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"down"}}`)
	}))
	defer failSrv.Close()

	a.multiEngine = true
	a.source = "hello"
	a.cfg.Providers = []Provider{
		{ID: "p1", Name: "P1", Kind: providerOpenAI, Enabled: true, BaseURL: srv.URL + "/v1", Model: "m1", Key: "k"},
		{ID: "p2", Name: "P2", Kind: providerOpenAI, Enabled: true, BaseURL: failSrv.URL + "/v1", Model: "m1", Key: "k"},
	}
	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	beforePartialHits := hits.Load()
	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	if hits.Load() == beforePartialHits {
		t.Fatal("partial multi-engine failure must not be cached")
	}

	// Full multi-engine success IS cached and restores non-nil MD options.
	a.cfg.Providers[1].BaseURL = srv.URL + "/v1"
	a.startTranslation()
	pumpTestUI(t, uiCh, func() bool { return !a.streaming })
	hitsAfterFull := hits.Load()
	a.startTranslation()
	if a.streaming || hits.Load() != hitsAfterFull || !a.cacheHit || len(a.results) != 2 || a.results[0].MD == nil || a.results[1].MD == nil {
		t.Fatalf("multi-engine cache hit failed: streaming=%v hits=%d/%d cacheHit=%v results=%+v",
			a.streaming, hits.Load(), hitsAfterFull, a.cacheHit, a.results)
	}
}
