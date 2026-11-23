package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"hayami/internal/cache"
	"hayami/internal/dict"
	"hayami/internal/lang"
)

func (a *app) inputWindowView(c *ui.Context) {
	t := fluentTheme(c)
	winW, _ := c.Size()
	pal := win11Theme(t)

	c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { a.startInputTranslation() })

	if a.inputWindowNeedFocus {
		a.inputWindowNeedFocus = false
		a.inputWindowInputHandle.Focus(c)
	}

	padH := float32(16)
	if winW < 500 {
		padH = 12
	}
	verticalStack := winW < 500
	cardRad := float32(10)
	if isKDE() {
		cardRad = 6
	}

	ui.Column(c).Grow(1).MinHeight(0).Padding(10, 0, 0, 0).Children(func() {
		// Language Select Bar
		ui.Row(c).Padding(0, padH, 8, padH).Gap(8).AlignItems(ui.Center).Children(func() {
			if a.inputSourceLang == "" {
				a.inputSourceLang = "自动检测"
			}
			ui.Column(c).Grow(1).Children(func() {
				ui.Select(c, &a.inputSourceLang, sourceLanguages).
					Height(30).Label("源语言")
			})

			swapBtn := win11IconButton(c, iconSwap, "交换语言").OnClick(a.swapInputLanguages)
			_ = swapBtn

			if a.inputTargetLang == "" {
				a.inputTargetLang = a.cfg.TargetLang
				if a.inputTargetLang == "" {
					a.inputTargetLang = autoTarget
				}
			}
			ui.Column(c).Grow(1).Children(func() {
				if ui.Select(c, &a.inputTargetLang, targetLanguages).Height(30).Label("目标语言").Changed() {
					if strings.TrimSpace(a.inputSource) != "" {
						a.startInputTranslation()
					}
				}
			})
		})

		// Dual-Pane Translation Workspace (Responsive Row or Column)
		renderInputPane := func() {
			ui.Column(c).Grow(1).Radius(cardRad).Background(pal.Card).Border(1, pal.Border).
				Padding(10).Gap(8).Children(func() {
				ui.TextArea(c, &a.inputSource).
					Bind(&a.inputWindowInputHandle).
					Grow(1).
					Placeholder("在此输入要翻译的内容，按 Ctrl+Enter 立即翻译…")

				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					if n := len([]rune(a.inputSource)); n > 0 {
						ui.Text(c, fmt.Sprintf("%d 字符", n)).FontSize(11).TextColor(pal.TextSecondary)
					}
					ui.Spacer(c)
					if strings.TrimSpace(a.inputSource) != "" {
						if ui.Button(c, "清空").Clicked() {
							if a.inputCancel != nil {
								a.inputCancel()
								a.inputCancel = nil
							}
							a.inputReqID++
							a.inputStreaming = false
							a.inputSource = ""
							a.inputResult = ""
							a.inputErr = ""
							a.inputStatus = ""
							a.inputDictEntry = nil
							a.inputCacheHit = false
						}
					}
					if ui.PrimaryButton(c, "翻译 (Ctrl+Enter)").Clicked() {
						a.startInputTranslation()
					}
				})
			})
		}

		renderOutputPane := func() {
			emptyHint := "在左侧输入文本后点击翻译"
			if verticalStack {
				emptyHint = "在上方输入文本后点击翻译"
			}
			ui.Column(c).Grow(1).MinHeight(0).Radius(cardRad).Background(pal.Card).Border(1, pal.Border).
				Padding(10).Gap(8).Children(func() {
				ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
					if a.inputDictEntry != nil {
						ui.Column(c.Key("input-dict-card")).Padding(0, 0, 8, 0).Children(func() {
							a.renderDictCard(c, pal, a.inputDictEntry, cardRad)
						})
					}
					switch {
					case a.inputStreaming:
						if a.inputResult != "" {
							markdown.Stream(c, a.inputResult, &a.md)
							ui.Row(c).Gap(6).Padding(4, 0, 0, 0).AlignItems(ui.Center).Children(func() {
								ui.Spinner(c)
								ui.Text(c, "正在生成…").FontSize(11).TextColor(pal.TextSecondary)
							})
						} else {
							ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
								ui.Spinner(c)
								ui.Text(c, "正在翻译…").FontSize(14).TextColor(pal.TextSecondary)
							})
						}
					case a.inputResult != "":
						markdown.View(c, a.inputResult, &a.md)
						if a.inputErr != "" {
							ui.Text(c, a.inputErr).TextColor(t.Danger).FontSize(12)
						}
					case a.inputErr != "":
						ui.Text(c, a.inputErr).TextColor(t.Danger).FontSize(13)
					default:
						if a.inputDictEntry == nil {
							ui.Column(c).Gap(6).Padding(20, 0).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "译文将显示在这里").Bold().TextColor(pal.TextSecondary)
								ui.Text(c, emptyHint).FontSize(12).TextColor(pal.TextSecondary)
							})
						}
					}
				})

				ui.Row(c).Height(30).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, "引擎："+a.engineLabel()).FontSize(11).TextColor(pal.TextSecondary).SingleLine().Shrink(1)
					if a.inputStatus != "" {
						ui.Text(c, a.inputStatus).FontSize(11).TextColor(statusColor(t, a.inputStatus)).SingleLine().Shrink(1)
					}

					ui.Spacer(c)

					copyBtn := win11IconButton(c, iconCopy, "复制译文").OnClick(func() {
						if strings.TrimSpace(a.inputResult) != "" {
							mygo.Clipboard.WriteText(a.inputResult)
							a.inputStatus = "已复制译文"
						}
					})
					_ = copyBtn

					audioBtn := win11IconButton(c, iconAudio, "朗读（朗读中点击停止）").OnClick(func() {
						if isSpeaking() {
							stopSpeaking()
							a.inputStatus = "已停止朗读"
						} else if err := speakResultWithNotify(a.inputResult, a.inputSource, func(err error) {
							a.postUI(func() {
								a.inputStatus = "朗读失败：" + err.Error()
							})
						}); err != nil {
							a.inputStatus = err.Error()
						}
					})
					_ = audioBtn
				})
			})
		}

		if verticalStack {
			ui.Column(c).Grow(1).MinHeight(0).Padding(0, padH, 12, padH).Gap(10).Children(func() {
				renderInputPane()
				renderOutputPane()
			})
		} else {
			ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Padding(0, padH, 12, padH).Gap(10).Children(func() {
				renderInputPane()
				renderOutputPane()
			})
		}
	})
}

func (a *app) showInputWindow() {
	if a.inputTargetLang == "" {
		a.inputTargetLang = a.cfg.TargetLang
		if a.inputTargetLang == "" {
			a.inputTargetLang = autoTarget
		}
	}
	a.inputWindowNeedFocus = true
	a.showPanel(panelInput)
}

func normalizeConcreteLanguage(name string) string {
	switch strings.TrimSpace(name) {
	case "", "自动检测", "中文":
		return "简体中文"
	default:
		return name
	}
}

func (a *app) swapInputLanguages() {
	prevSourceLang := a.inputSourceLang
	prevTargetLang := a.inputTargetLang
	if prevSourceLang == "" || prevSourceLang == "自动检测" {
		if strings.TrimSpace(a.inputSource) != "" {
			prevSourceLang = normalizeConcreteLanguage(detectLanguageName(a.inputSource))
		} else {
			prevSourceLang = "简体中文"
		}
	}
	effTarget := effectiveTarget(prevTargetLang, a.inputSource)
	if strings.TrimSpace(a.inputSource) == "" && (prevTargetLang == "" || prevTargetLang == autoTarget || prevTargetLang == "自动检测") {
		if prevSourceLang == "简体中文" {
			effTarget = "English"
		} else {
			effTarget = "简体中文"
		}
	}
	a.inputSourceLang = normalizeConcreteLanguage(effTarget)
	a.inputTargetLang = normalizeConcreteLanguage(prevSourceLang)

	if a.inputResult != "" && a.inputSource != "" {
		a.inputSource, a.inputResult = a.inputResult, a.inputSource
		a.startInputTranslation()
	}
}

func (a *app) startInputTranslation() {
	a.runInputTranslation(false)
}

func (a *app) runInputTranslation(bypassCache bool) {
	a.ensureProviderKeysLoaded()
	text := strings.TrimSpace(a.inputSource)
	if text == "" {
		return
	}
	if a.inputCancel != nil {
		a.inputCancel()
	}
	a.inputReqID++
	reqID := a.inputReqID

	a.inputErr = ""
	a.inputStatus = ""
	a.inputResult = ""
	a.inputDictEntry = nil
	a.inputCacheHit = false

	if norm, ok := lang.IsSingleWord(text); ok {
		if entry, hit := dict.Lookup(norm); hit {
			a.inputDictEntry = &entry
		}
	}

	target := effectiveTarget(a.inputTargetLang, text)
	cfg := a.cfg
	if a.inputSourceLang != "" && a.inputSourceLang != "自动检测" {
		cfg.SourceLang = a.inputSourceLang
	} else {
		cfg.SourceLang = ""
	}
	cfg.TargetLang = target

	providers := cfg.EnabledProviders(false)
	configured := providers[:0]
	for _, p := range providers {
		if providerConfigured(p, p.Key) {
			configured = append(configured, p)
		}
	}
	if len(configured) == 0 {
		a.inputStreaming = false
		a.inputErr = "没有可用的翻译服务，请在设置里填写 Key"
		return
	}

	m, ok := cfg.Mode("translate")
	if !ok {
		m = cfg.AllModes()[0]
	}
	resCache := a.ensureResultCache()
	cacheKey := translationCacheKey(cfg, m, text, false, configured)
	if !bypassCache {
		if cached, hit := resCache.Get(cacheKey); hit {
			a.inputStreaming = false
			a.inputResult = cached.Result
			a.inputStatus = "已命中缓存"
			a.inputCacheHit = true
			return
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.inputCancel = cancel
	a.inputStreaming = true

	go func() {
		defer cancel()
		var pending strings.Builder
		var lastFlush time.Time
		flush := func() {
			if pending.Len() == 0 {
				return
			}
			accum := pending.String()
			a.postUI(func() {
				if a.inputReqID == reqID {
					a.inputResult = accum
				}
			})
			lastFlush = time.Now()
		}

		var activeProviderLabel string
		err := translateWithFallback(ctx, cfg, configured, textMessages(cfg, text), text, func(p Provider) {
			activeProviderLabel = providerLabel(p)
		}, func(delta string) {
			pending.WriteString(delta)
			if time.Since(lastFlush) >= 30*time.Millisecond {
				flush()
			}
		})
		flush()
		finalProvider := activeProviderLabel
		wasCanceled := ctx.Err() != nil

		a.postUI(func() {
			if a.inputReqID != reqID {
				return
			}
			a.inputStreaming = false
			if err != nil && pending.Len() == 0 {
				if !wasCanceled {
					title, hint := explainError(err)
					a.inputErr = title
					if hint != "" {
						a.inputErr += "。" + hint
					}
				}
			} else if err != nil {
				if !wasCanceled {
					title, _ := explainError(err)
					a.inputErr = "部分传输中断：" + title
				}
			} else {
				a.inputResult = pending.String()
				if !wasCanceled && strings.TrimSpace(a.inputResult) != "" {
					resCache.Set(cacheKey, cache.Value{
						Result:       a.inputResult,
						ProviderName: finalProvider,
					})
				}
			}
			if a.store != nil && pending.Len() > 0 && !wasCanceled {
				entry := historyEntry{
					CreatedAt: timeNow(),
					Source:    text,
					Result:    pending.String(),
					Target:    target,
					Mode:      "input",
				}
				go func() {
					_ = a.store.Add(entry)
					a.refreshHistory()
				}()
			}
		})
	}()
}
