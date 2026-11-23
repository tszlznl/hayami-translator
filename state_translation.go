package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"hayami/internal/cache"
	"hayami/internal/config"
	"hayami/internal/dict"
	"hayami/internal/lang"
	"hayami/internal/vision"
)

type providerResult struct {
	ID        string
	Name      string
	Kind      string
	Result    string
	ErrText   string
	ErrHint   string
	Streaming bool
	MD        *markdown.Options
}

type translationState struct {
	source          string
	result          string
	results         []providerResult
	multiEngine     bool
	errText         string
	errHint         string
	status          string
	streaming       bool
	hasImage        bool
	shot            []byte
	targetLang      string
	activeMode      string
	reqID           int
	cancel          context.CancelFunc
	popupScroll     ui.ScrollState
	popupAutoScroll bool
	focusInput      bool
	popupEditing    bool
	inputHandle     ui.Handle

	// Offline dictionary, LRU cache, and UI thread dispatch hook (M3).
	dictEntry   *dict.Entry
	cacheHit    bool
	resultCache *cache.TranslationCache
	uiDispatch  func(func())

	// Follow-up turns on the current result.
	followUpTurns     []promptFollowUpTurn
	followUpInput     string
	followUpQ         string
	followUp          string
	followUpErr       string
	followUpStreaming bool
	followUpReqID     int
	followUpCancel    context.CancelFunc
	activeHistoryID   int64
}

func (s *translationState) cancelStream() {
	s.reqID++
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.streaming = false
}

func (s *translationState) hasAnyResult() bool {
	if s.multiEngine && len(s.results) > 0 {
		for i := range s.results {
			if strings.TrimSpace(s.results[i].Result) != "" {
				return true
			}
		}
		return false
	}
	return strings.TrimSpace(s.result) != ""
}

func (a *app) effectivePopupTarget() string {
	if strings.TrimSpace(a.targetLang) != "" {
		return a.targetLang
	}
	if strings.TrimSpace(a.cfg.TargetLang) != "" {
		return a.cfg.TargetLang
	}
	return autoTarget
}

// translateSelection reads the selection of the foreground window before
// showing the panel: showing it first would take the selection away.
func (a *app) translateSelection() {
	if a.popup == nil {
		return
	}
	if a.popup.IsVisible() {
		a.hidePopup()
	}
	a.selectionReqID++
	id := a.selectionReqID
	go func() {
		text, err := readSelectedText()
		a.updatePopup(func() {
			if id != a.selectionReqID {
				return
			}
			if err == nil && strings.TrimSpace(text) != "" {
				a.showTranslation(text)
				return
			}
			a.resetTranslation()
			a.source = text
			a.status = "输入文本后按 Ctrl+Enter 翻译"
			if err != nil {
				a.status = err.Error()
			}
			a.focusInput = true
			a.popupEditing = true
			a.showPopup()
		})
	}()
}

func (a *app) showTranslation(text string) {
	if a.popup == nil {
		return
	}
	a.resetTranslation()
	a.source = text
	a.hasImage = false
	a.shot = nil
	a.showPopup()
	if strings.TrimSpace(text) == "" {
		a.status = "没有读到选中文本，直接输入后点翻译"
		a.focusInput = true
		a.popupEditing = true
		return
	}
	a.startTranslation()
}

// translateScreenshot opens the system snipping overlay and translates the
// image it puts on the clipboard with a vision model.
func (a *app) translateScreenshot() {
	if a.popup != nil && a.popup.IsVisible() {
		a.hidePopup()
	}
	if a.shotCancel != nil {
		a.shotCancel()
		a.shotCancel = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	a.shotCancel = cancel

	a.selectionReqID++
	id := a.selectionReqID
	go func() {
		defer cancel()
		img, err := captureScreenshot(ctx)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		a.updatePopup(func() {
			if id != a.selectionReqID {
				return
			}
			a.resetTranslation()
			if err != nil {
				a.status = "截图失败：" + err.Error()
				a.showPopup()
				return
			}
			a.hasImage = true
			a.shot = img
			a.source = ""
			a.showPopup()
			a.startTranslation()
		})
	}()
}

func (a *app) ensureResultCache() *cache.TranslationCache {
	if a.resultCache == nil {
		a.resultCache = cache.New(cache.DefaultCapacity, cache.DefaultTTL)
	}
	return a.resultCache
}

func (a *app) postUI(fn func()) {
	if a.uiDispatch != nil {
		a.uiDispatch(fn)
		return
	}
	a.updatePopup(fn)
}

func translationCacheKey(cfg Config, m config.Mode, source string, multiEngine bool, providers []Provider) cache.Key {
	effectiveSource := strings.TrimSpace(cfg.SourceLang)
	if effectiveSource == "" {
		effectiveSource = "自动检测"
	}
	modeSum := sha256.Sum256([]byte(m.Prompt + "\x00" + effectiveSource))
	modeKey := fmt.Sprintf("%s:%s:%s", m.ID, m.Kind, hex.EncodeToString(modeSum[:8]))

	var sb strings.Builder
	if multiEngine {
		sb.WriteString("multi")
	} else {
		sb.WriteString("single")
	}
	fmt.Fprintf(&sb, "|t=%.4g|m=%d", cfg.Temperature, cfg.MaxTokens)
	for _, p := range providers {
		fmt.Fprintf(&sb, "|%s:%s:%s:%s:%s:%s:%s",
			p.ID, p.Kind, p.BaseURL, p.Model, p.Endpoint, p.Region, p.AppID)
	}

	return cache.Key{
		Source:     strings.TrimSpace(source),
		TargetLang: outputLanguage(cfg, m, source),
		Mode:       modeKey,
		Service:    sb.String(),
	}
}

func (a *app) resetTranslation() {
	a.cancelStream()
	a.cancelFollowUp()
	a.followUpTurns = nil
	a.followUpInput = ""
	a.followUpQ = ""
	a.followUp = ""
	a.followUpErr = ""
	a.activeHistoryID = 0
	a.source = ""
	a.result = ""
	a.results = nil
	a.errText = ""
	a.errHint = ""
	a.status = ""
	a.hasImage = false
	a.shot = nil
	a.popupEditing = false
	a.dictEntry = nil
	a.cacheHit = false
}

func (a *app) startTranslation() {
	a.runTranslation(false)
}

func (a *app) retranslate() {
	a.runTranslation(true)
}

func (a *app) runTranslation(bypassCache bool) {
	a.ensureProviderKeysLoaded()
	a.cancelStream()
	a.cancelFollowUp()
	stopSpeaking()
	a.followUpTurns = nil
	a.followUpQ = ""
	a.followUp = ""
	a.followUpErr = ""
	a.activeHistoryID = 0
	a.dictEntry = nil
	a.cacheHit = false
	cfg := a.cfg
	cfg.TargetLang = a.effectivePopupTarget()
	var mode, sourceText string
	var providers []Provider
	var m config.Mode

	switch {
	case a.hasImage && len(a.shot) > 0:
		mode, sourceText = "image", "[截图]"
		providers = cfg.EnabledProviders(true)
	default:
		sourceText = strings.TrimSpace(a.source)
		if sourceText == "" {
			a.status = "请输入要处理的文本"
			return
		}
		cfg.TargetLang = effectiveTarget(cfg.TargetLang, sourceText)
		m = a.currentMode()
		mode = m.ID
		if mode == "translate" {
			providers = cfg.EnabledProviders(false)
			if norm, ok := lang.IsSingleWord(sourceText); ok {
				if entry, hit := dict.Lookup(norm); hit {
					a.dictEntry = &entry
				}
			}
		} else {
			providers = cfg.EnabledPromptProviders()
		}
	}

	configured := providers[:0]
	for _, p := range providers {
		if providerConfigured(p, p.Key) {
			configured = append(configured, p)
		}
	}
	providers = configured
	a.result = ""
	a.results = nil
	a.errText = ""
	a.errHint = ""
	a.status = ""
	a.focusInput = false
	a.popupEditing = false
	a.popupAutoScroll = true

	if len(providers) == 0 {
		switch {
		case a.hasImage:
			a.errText = "截图翻译需要启用一个配置好的 OpenAI Compatible 视觉模型"
		case mode != "translate":
			a.errText = fmt.Sprintf("「%s」模式需要启用一个配置好的 OpenAI Compatible 服务", a.modeName())
		default:
			a.errText = "没有可用的翻译服务，请在设置里填写 Key"
		}
		a.errHint = "打开设置，确认服务已启用并填写了 Key。"
		a.streaming = false
		if a.dictEntry != nil && a.panelPage == panelTranslation && a.popup != nil && a.popup.IsVisible() {
			a.placePanel()
		}
		return
	}

	var cacheKey cache.Key
	var resCache *cache.TranslationCache
	if !a.hasImage {
		resCache = a.ensureResultCache()
		cacheKey = translationCacheKey(cfg, m, sourceText, a.multiEngine, providers)
		if !bypassCache {
			if cached, ok := resCache.Get(cacheKey); ok {
				if a.multiEngine {
					a.results = make([]providerResult, len(cached.MultiResults))
					for i, snap := range cached.MultiResults {
						a.results[i] = providerResult{
							ID:      snap.ID,
							Name:    snap.Name,
							Kind:    snap.Kind,
							Result:  snap.Result,
							ErrText: snap.ErrText,
							ErrHint: snap.ErrHint,
							MD:      &markdown.Options{Theme: &markdown.Theme{FontSize: 13}},
						}
					}
					a.result = cached.Result
					a.status = fmt.Sprintf("全部完成 (%d/%d 成功 · 缓存)", len(a.results), len(a.results))
				} else {
					a.results = nil
					a.result = cached.Result
					a.providerName = cached.ProviderName
					a.status = "已命中缓存"
				}
				a.streaming = false
				a.cacheHit = true
				if a.panelPage == panelTranslation && a.popup != nil && a.popup.IsVisible() {
					a.placePanel()
				}
				return
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.reqID++
	id := a.reqID
	a.streaming = true

	if a.dictEntry != nil && a.panelPage == panelTranslation && a.popup != nil && a.popup.IsVisible() {
		a.placePanel()
	}

	hasImage := a.hasImage
	shot := a.shot
	var textMessagesPayload []chatMessage
	if !hasImage {
		textMessagesPayload = messagesForMode(cfg, sourceText, m)
	}

	if a.multiEngine {
		a.results = make([]providerResult, len(providers))
		for i, p := range providers {
			a.results[i] = providerResult{
				ID:        p.ID,
				Name:      p.Name,
				Kind:      p.Kind,
				Streaming: true,
				MD:        &markdown.Options{Theme: &markdown.Theme{FontSize: 13}},
			}
		}

		go func() {
			defer cancel()
			messages := textMessagesPayload
			if hasImage {
				imgData, imgMIME, prepErr := vision.PrepareVisionImage(shot, vision.DefaultMaxScreenshotEdge)
				if ctx.Err() != nil {
					return
				}
				if prepErr != nil {
					a.postUI(func() {
						if a.reqID != id {
							return
						}
						a.streaming = false
						a.results = nil
						a.errText = "截图预处理失败"
						a.errHint = prepErr.Error()
					})
					return
				}
				messages = imageMessagesWithMIME(cfg, imgData, imgMIME)
			}

			var wg sync.WaitGroup
			for i, p := range providers {
				idx := i
				prov := p
				key := prov.Key
				wg.Add(1)
				go func() {
					defer wg.Done()
					var pPending strings.Builder
					pFlush := func() {
						if pPending.Len() == 0 {
							return
						}
						chunk := pPending.String()
						pPending.Reset()
						a.postUI(func() {
							if a.reqID == id && idx < len(a.results) {
								a.results[idx].Result += chunk
							}
						})
					}
					pLast := time.Now()
					err := translateWithProvider(ctx, cfg, prov, key, messages, sourceText, func(delta string) {
						pPending.WriteString(delta)
						if time.Since(pLast) >= 30*time.Millisecond {
							pFlush()
							pLast = time.Now()
						}
					})
					pFlush()
					provCanceled := ctx.Err() != nil
					a.postUI(func() {
						if a.reqID == id && idx < len(a.results) {
							a.results[idx].Streaming = false
							if err != nil && !provCanceled {
								title, hint := explainError(err)
								a.results[idx].ErrText = title
								a.results[idx].ErrHint = hint
							}
						}
					})
				}()
			}

			wg.Wait()
			wasCanceled := ctx.Err() != nil

			a.postUI(func() {
				if a.reqID != id {
					return
				}
				a.streaming = false
				if a.panelPage == panelTranslation && a.popup != nil && a.popup.IsVisible() {
					a.placePanel()
				}
				var successCount int
				var combined strings.Builder
				for i := range a.results {
					r := &a.results[i]
					if strings.TrimSpace(r.Result) != "" && r.ErrText == "" {
						successCount++
						if combined.Len() > 0 {
							combined.WriteString("\n\n---\n\n")
						}
						combined.WriteString("### " + r.Name + "\n\n" + r.Result)
					}
				}
				if successCount == 0 && !wasCanceled {
					a.result = ""
					a.errText = "所有翻译服务均失败"
					a.errHint = "查看各服务的错误说明，或打开设置检查翻译服务。"
				} else if successCount < len(a.results) {
					a.status = fmt.Sprintf("部分完成 (%d/%d 成功)", successCount, len(a.results))
					a.result = combined.String()
				} else {
					a.status = fmt.Sprintf("全部完成 (%d/%d 成功)", successCount, len(a.results))
					a.result = combined.String()
				}
				result := a.result
				if strings.TrimSpace(result) == "" {
					return
				}
				if !hasImage && resCache != nil && !wasCanceled && successCount == len(a.results) {
					snaps := make([]cache.ProviderSnapshot, len(a.results))
					for i, r := range a.results {
						snaps[i] = cache.ProviderSnapshot{
							ID:      r.ID,
							Name:    r.Name,
							Kind:    r.Kind,
							Result:  r.Result,
							ErrText: r.ErrText,
							ErrHint: r.ErrHint,
						}
					}
					resCache.Set(cacheKey, cache.Value{
						Result:       result,
						MultiResults: snaps,
					})
				}
				if a.store == nil {
					return
				}
				entry := historyEntry{
					CreatedAt: timeNow(),
					Source:    sourceText,
					Result:    result,
					Target:    cfg.TargetLang,
					Mode:      mode,
				}
				go func() {
					rowID, err := a.store.AddReturningID(entry)
					if err != nil {
						log.Print("history: ", err)
						return
					}
					a.postUI(func() {
						if a.reqID == id {
							a.activeHistoryID = rowID
						}
					})
					a.refreshHistory()
				}()
			})
		}()
		return
	}

	a.results = nil
	go func() {
		defer cancel()
		messages := textMessagesPayload
		if hasImage {
			imgData, imgMIME, prepErr := vision.PrepareVisionImage(shot, vision.DefaultMaxScreenshotEdge)
			if ctx.Err() != nil {
				return
			}
			if prepErr != nil {
				a.postUI(func() {
					if a.reqID != id {
						return
					}
					a.streaming = false
					a.errText = "截图预处理失败"
					a.errHint = prepErr.Error()
				})
				return
			}
			messages = imageMessagesWithMIME(cfg, imgData, imgMIME)
		}

		var pending strings.Builder
		flushedFirst := false
		flush := func() {
			if pending.Len() == 0 {
				return
			}
			chunk := pending.String()
			pending.Reset()
			flushedFirst = true
			a.postUI(func() {
				if a.reqID == id {
					a.result += chunk
				}
			})
		}
		last := time.Now()
		var activeProviderLabel string

		err := translateWithFallback(ctx, cfg, providers, messages, sourceText, func(p Provider) {
			pending.Reset()
			flushedFirst = false
			label := providerLabel(p)
			activeProviderLabel = label
			a.postUI(func() {
				if a.reqID == id {
					a.providerName = label
					a.result = ""
				}
			})
		}, func(delta string) {
			pending.WriteString(delta)
			// Repaint immediately on the first chunk, then at most every 30ms.
			if !flushedFirst || time.Since(last) >= 30*time.Millisecond {
				flush()
				last = time.Now()
			}
		})
		flush()
		finalProvider := activeProviderLabel
		wasCanceled := ctx.Err() != nil

		a.postUI(func() {
			if a.reqID != id {
				return
			}
			a.streaming = false
			if a.panelPage == panelTranslation && a.popup != nil && a.popup.IsVisible() {
				a.placePanel()
			}
			if err != nil && !wasCanceled {
				title, hint := explainError(err)
				if strings.TrimSpace(a.result) == "" {
					a.errText = title
					a.errHint = hint
				} else {
					a.status = "部分失败：" + title
				}
			}
			result := a.result
			if strings.TrimSpace(result) == "" {
				return
			}
			if err == nil && !wasCanceled && !hasImage && resCache != nil {
				resCache.Set(cacheKey, cache.Value{
					Result:       result,
					ProviderName: finalProvider,
				})
			}
			if a.store == nil {
				return
			}
			entry := historyEntry{
				CreatedAt: timeNow(),
				Source:    sourceText,
				Result:    result,
				Target:    cfg.TargetLang,
				Mode:      mode,
			}
			go func() {
				rowID, err := a.store.AddReturningID(entry)
				if err != nil {
					log.Print("history: ", err)
					return
				}
				a.postUI(func() {
					if a.reqID == id {
						a.activeHistoryID = rowID
						if len(a.followUpTurns) > 0 && a.store != nil && rowID > 0 {
							turns := toHistoryFollowUpTurns(a.followUpTurns)
							go func() {
								if err := a.store.UpdateFollowUps(rowID, turns); err != nil {
									log.Print("history follow-up update: ", err)
								}
							}()
						}
					}
				})
				a.refreshHistory()
			}()
		})
	}()
}

func (a *app) copyResult() {
	if a.multiEngine && len(a.results) > 0 {
		var b strings.Builder
		for i := range a.results {
			r := &a.results[i]
			if strings.TrimSpace(r.Result) != "" {
				if b.Len() > 0 {
					b.WriteString("\n\n---\n\n")
				}
				b.WriteString("【" + r.Name + "】\n" + r.Result)
			}
		}
		if b.Len() > 0 {
			mygo.Clipboard.WriteText(b.String())
			a.status = "已复制全部引擎译文"
		}
		return
	}
	if strings.TrimSpace(a.result) == "" {
		return
	}
	mygo.Clipboard.WriteText(a.result)
	a.status = "已复制译文"
}
