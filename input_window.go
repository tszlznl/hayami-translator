package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

func (a *app) inputWindowView(c *ui.Context) {
	t := fluentTheme(c)
	winW, _ := c.Size()
	pal := win11Theme(t)

	c.Root().Background(pal.Bg)
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { a.startInputTranslation() })
	c.OnShortcut(0, ui.KeyEscape, func() {
		if a.inputCancel != nil {
			a.inputCancel()
			a.inputCancel = nil
		}
		if a.inputWin != nil {
			a.inputWin.Hide()
		}
	})

	if a.inputWindowNeedFocus {
		a.inputWindowNeedFocus = false
		a.inputWindowInputHandle.Focus(c)
	}

	padH := float32(24)
	if winW < 680 {
		padH = 12
	}
	verticalStack := winW < 640

	ui.Column(c).Fill().Children(func() {
		// Window Title / Top Bar
		ui.Row(c).Height(38).Padding(0, padH).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "输入翻译").FontSize(13).Bold().TextColor(pal.TextPrimary)
			ui.Spacer(c)
			if ui.Button(c, "历史记录").Tooltip("查看历史翻译").Clicked() {
				a.showHistory()
			}
			if ui.Button(c, "设置").Tooltip("翻译设置").Clicked() {
				a.showSettings()
			}
		})

		// Language Select Bar
		ui.Row(c).Padding(4, padH).Gap(8).AlignItems(ui.Center).Children(func() {
			if a.inputSourceLang == "" {
				a.inputSourceLang = "自动检测"
			}
			ui.Column(c).Grow(1).Children(func() {
				ui.Select(c, &a.inputSourceLang, []string{"自动检测", "English", "简体中文", "繁體中文", "日本語", "한국어", "Français", "Deutsch", "Español", "Русский"}).
					Height(32).Label("源语言")
			})

			swapBtn := win11IconButton(c, iconSwap, "交换语言")
			if swapBtn.Clicked() {
				a.swapInputLanguages()
			}

			if a.inputTargetLang == "" {
				a.inputTargetLang = a.cfg.TargetLang
				if a.inputTargetLang == "" {
					a.inputTargetLang = autoTarget
				}
			}
			ui.Column(c).Grow(1).Children(func() {
				if ui.Select(c, &a.inputTargetLang, targetLanguages).Height(32).Label("目标语言").Changed() {
					if strings.TrimSpace(a.inputSource) != "" {
						a.startInputTranslation()
					}
				}
			})
		})

		// Dual-Pane Translation Workspace (Responsive Row or Column)
		renderInputPane := func() {
			ui.Column(c).Grow(1).Radius(6).Background(pal.Surface).Border(1, pal.Border).
				BorderWidth(1, 1, 2, 1).BorderColor(pal.Border).
				Padding(12).Children(func() {
				ui.TextArea(c, &a.inputSource).
					Bind(&a.inputWindowInputHandle).
					Grow(1).
					Placeholder("在此输入要翻译的内容，按 Ctrl+Enter 立即翻译…")

				ui.Row(c).AlignItems(ui.Center).Children(func() {
					if n := len([]rune(a.inputSource)); n > 0 {
						ui.Text(c, fmt.Sprintf("%d 字符", n)).FontSize(11).TextColor(pal.TextSecondary)
					}
					ui.Spacer(c)
					if strings.TrimSpace(a.inputSource) != "" {
						if ui.Button(c, "清空").Clicked() {
							a.inputSource = ""
							a.inputResult = ""
							a.inputErr = ""
						}
					}
					if ui.PrimaryButton(c, "翻译 (Ctrl+Enter)").Clicked() {
						a.startInputTranslation()
					}
				})
			})
		}

		renderOutputPane := func() {
			ui.Column(c).Grow(1).Radius(6).Background(pal.Card).Border(1, pal.Border).
				Padding(12).Children(func() {
				ui.Scroll(c).Grow(1).Children(func() {
					switch {
					case a.inputStreaming:
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Spinner(c)
							if a.inputResult != "" {
								markdown.Stream(c, a.inputResult, &a.md)
							} else {
								ui.Text(c, "正在翻译…").FontSize(14).TextColor(pal.TextSecondary)
							}
						})
					case a.inputResult != "":
						markdown.View(c, a.inputResult, &a.md)
						if a.inputErr != "" {
							ui.Text(c, a.inputErr).TextColor(t.Danger).FontSize(12)
						}
					case a.inputErr != "":
						ui.Text(c, a.inputErr).TextColor(t.Danger).FontSize(13)
					default:
						ui.Column(c).Gap(6).Padding(20, 0).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "译文将显示在这里").Bold().TextColor(pal.TextSecondary)
							ui.Text(c, "在左侧输入文本后点击翻译").FontSize(12).TextColor(pal.TextSecondary)
						})
					}
				})

				ui.Row(c).Height(34).AlignItems(ui.Center).Gap(4).Children(func() {
					ui.Text(c, "引擎："+a.engineLabel()).FontSize(11).TextColor(pal.TextSecondary).SingleLine()

					ui.Spacer(c)

					copyBtn := win11IconButton(c, iconCopy, "复制译文")
					if copyBtn.Clicked() && strings.TrimSpace(a.inputResult) != "" {
						mygo.Clipboard.WriteText(a.inputResult)
					}

					audioBtn := win11IconButton(c, iconAudio, "朗读（朗读中点击停止）")
					if audioBtn.Clicked() {
						if isSpeaking() {
							stopSpeaking()
						} else {
							speakResult(a.inputResult, a.inputSource)
						}
					}
				})
			})
		}

		if verticalStack {
			ui.Column(c).Grow(1).Padding(4, padH, 14, padH).Gap(10).Children(func() {
				renderInputPane()
				renderOutputPane()
			})
		} else {
			ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(4, padH, 16, padH).Gap(12).Children(func() {
				renderInputPane()
				renderOutputPane()
			})
		}
	})
}

func (a *app) showInputWindow() {
	if a.inputWin == nil {
		return
	}
	if a.inputTargetLang == "" {
		a.inputTargetLang = a.cfg.TargetLang
		if a.inputTargetLang == "" {
			a.inputTargetLang = autoTarget
		}
	}
	a.inputWindowNeedFocus = true
	a.inputWin.Center()
	a.inputWin.Show()
	a.inputWin.Focus()
}

func (a *app) swapInputLanguages() {
	prevSourceLang := a.inputSourceLang
	prevTargetLang := a.inputTargetLang
	if prevSourceLang == "" || prevSourceLang == "自动检测" {
		if strings.TrimSpace(a.inputSource) != "" {
			prevSourceLang = detectLanguageName(a.inputSource)
		} else {
			prevSourceLang = "简体中文"
		}
	}
	effTarget := effectiveTarget(prevTargetLang, a.inputSource)
	a.inputSourceLang = effTarget
	a.inputTargetLang = prevSourceLang

	if a.inputResult != "" && a.inputSource != "" {
		a.inputSource, a.inputResult = a.inputResult, a.inputSource
		a.startInputTranslation()
	}
}

func (a *app) startInputTranslation() {
	text := strings.TrimSpace(a.inputSource)
	if text == "" {
		return
	}
	if a.inputCancel != nil {
		a.inputCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.inputCancel = cancel
	a.inputReqID++
	reqID := a.inputReqID

	a.inputStreaming = true
	a.inputErr = ""
	a.inputResult = ""

	target := effectiveTarget(a.inputTargetLang, text)
	cfg := a.cfg
	if a.inputSourceLang != "" && a.inputSourceLang != "自动检测" {
		cfg.SourceLang = a.inputSourceLang
	} else {
		cfg.SourceLang = ""
	}
	cfg.TargetLang = target

	go func() {
		var pending strings.Builder
		var lastFlush time.Time
		flush := func() {
			if pending.Len() == 0 {
				return
			}
			accum := pending.String()
			if a.inputWin != nil {
				a.inputWin.Update(func() {
					if a.inputReqID == reqID {
						a.inputResult = accum
					}
				})
			}
			lastFlush = time.Now()
		}

		err := translateWithFallback(ctx, cfg, cfg.enabledProviders(false), textMessages(cfg, text), text, nil, func(delta string) {
			pending.WriteString(delta)
			if time.Since(lastFlush) >= 30*time.Millisecond {
				flush()
			}
		})
		flush()

		if a.inputWin != nil {
			a.inputWin.Update(func() {
				if a.inputReqID != reqID {
					return
				}
				a.inputStreaming = false
				if err != nil && pending.Len() == 0 {
					if ctx.Err() == nil {
						a.inputErr = "翻译失败：" + err.Error()
					}
				} else if err != nil {
					if ctx.Err() == nil {
						a.inputErr = "部分传输中断：" + err.Error()
					}
				} else {
					a.inputResult = pending.String()
				}
				if a.store != nil && pending.Len() > 0 && ctx.Err() == nil {
					entry := historyEntry{
						CreatedAt: timeNow(),
						Source:    text,
						Result:    pending.String(),
						Target:    target,
						Mode:      "input",
					}
					go func() {
						_ = a.store.add(entry)
					}()
				}
			})
		}
	}()
}
