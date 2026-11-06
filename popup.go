package main

import (
	"fmt"
	"log"
	"math"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

var targetLanguages = []string{
	autoTarget, "简体中文", "繁體中文", "English", "日本語", "한국어",
	"Français", "Deutsch", "Español", "Русский", "Português",
	"Italiano", "العربية", "ไทย", "Tiếng Việt",
}

// clipText keeps the first n runes of s, for a source preview.
func clipText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// popupView renders the floating translation panel: a source-and-target
// header, the source, the result, and one quiet action row.
func (a *app) popupView(c *ui.Context) {
	t := fluentTheme(c)
	winW, _ := c.Size()
	pal := win11Theme(t)

	panelBackground(c, t)
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { a.submitPopup() })

	padH := float32(14)
	if winW < 360 {
		padH = 10
	}

	ui.Column(c).Fill().Children(func() {
		// Header: mode, source and target language, then the window actions.
		ui.Row(c).Height(38).Padding(0, padH).AlignItems(ui.Center).Children(func() {
			modeBtn := ui.ButtonBase(c).Radius(5).Padding(3, 8).
				Background(pal.Card).Border(1, pal.Border).Tooltip("切换模式")
			if modeBtn.Clicked() {
				a.cycleMode()
			}
			modeBtn.Children(func() {
				ui.Text(c, a.modeName()).Bold().FontSize(12).TextColor(pal.Accent).SingleLine()
			})
			ui.Box(c).Width(8).Height(1)

			srcDisplay := "自动检测"
			if a.hasImage {
				srcDisplay = "截图"
			} else if strings.TrimSpace(a.source) != "" {
				srcDisplay = detectLanguageName(a.source)
			}
			ui.Text(c, srcDisplay).FontSize(12).TextColor(pal.TextSecondary).Shrink(1)

			ui.Box(c).Width(14).Height(14).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				ui.Icon(c, iconArrowRight)
			})

			targetLabel := a.targetLang
			if targetLabel == "" {
				targetLabel = autoTarget
			}
			displayLabel := targetLabel
			if targetLabel == autoTarget && strings.TrimSpace(a.source) != "" {
				displayLabel = "自动 (" + effectiveTarget(autoTarget, a.source) + ")"
			}
			targetBtn := ui.ButtonBase(c).Radius(5).Padding(3, 8).
				Background(pal.Card).Border(1, pal.Border).
				Tooltip("切换目标语言")
			if targetBtn.Clicked() {
				nextIdx := 0
				for i, lang := range targetLanguages {
					if lang == a.targetLang {
						nextIdx = (i + 1) % len(targetLanguages)
						break
					}
				}
				a.targetLang = targetLanguages[nextIdx]
				a.cfg.TargetLang = a.targetLang
				a.form.TargetLang = a.targetLang
				if err := a.cfg.Save(); err != nil {
					log.Print("saving target lang: ", err)
				}
				if a.hasAnyResult() && !a.streaming {
					a.startTranslation()
				}
			}
			targetBtn.Children(func() {
				ui.Text(c, displayLabel).Bold().FontSize(12).TextColor(pal.Accent).SingleLine()
			})

			ui.Spacer(c)

		})

		ui.Column(c).Grow(1).Padding(2, padH, 8, padH).Gap(8).Children(func() {
			if a.hasImage {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Badge(c, "视觉模型截图识别").Background(pal.Accent.Alpha(0.12)).TextColor(pal.Accent)
				})
			} else if a.popupEditing || strings.TrimSpace(a.source) == "" {
				editor := ui.TextArea(c, &a.source).
					Bind(&a.inputHandle).
					Placeholder("输入文字后按 Ctrl+Enter 翻译…")
				if a.hasAnyResult() {
					editor.Height(84)
				} else {
					editor.Grow(1)
				}
				if a.focusInput {
					a.inputHandle.Focus(c)
					a.focusInput = false
				}
			} else {
				preview := ui.ButtonBase(c).Radius(6).Padding(8, 10).
					Background(pal.Card).Border(1, pal.Border).
					AlignItems(ui.Start).Tooltip("点击编辑原文")
				if preview.Clicked() {
					a.popupEditing = true
					a.focusInput = true
				}
				preview.Children(func() {
					ui.Text(c, clipText(a.source, 400)).FontSize(13).TextColor(pal.TextSecondary)
				})
			}

			if (a.streaming || a.followUpStreaming) && !a.multiEngine {
				a.popupScroll.Y = math.MaxFloat32
			}

			ui.Scroll(c).Grow(1).TrackScroll(&a.popupScroll).Children(func() {
				switch {
				case a.errText != "":
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, a.errText).TextColor(t.Danger).FontSize(13)
						hint := a.errHint
						if hint == "" {
							hint = "按 Ctrl+Enter 重试，或检查设置里的翻译服务。"
						}
						ui.Text(c, hint).FontSize(11).TextColor(pal.TextSecondary)
					})
				case a.multiEngine && len(a.results) > 0:
					ui.Column(c).Gap(8).Children(func() {
						for i := range a.results {
							r := &a.results[i]
							ui.Column(c.Key(r.ID)).Gap(4).Padding(8, 10).Radius(6).
								Background(pal.Card).Border(1, pal.Border).Children(func() {
								ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
									ui.Text(c, r.Name).Bold().FontSize(12)
									if r.Streaming {
										ui.Spinner(c)
									}
									ui.Spacer(c)
									if r.Result != "" {
										if ui.Button(c, "复制").Tooltip("复制该服务译文").Clicked() {
											mygo.Clipboard.WriteText(r.Result)
											a.status = "已复制「" + r.Name + "」译文"
										}
									}
								})
								if r.Result != "" {
									if r.Streaming {
										markdown.Stream(c, r.Result, r.MD)
									} else {
										markdown.View(c, r.Result, r.MD)
									}
									if r.ErrText != "" {
										ui.Text(c, "中断："+r.ErrText).TextColor(t.Danger).FontSize(11)
									}
								} else if r.ErrText != "" {
									ui.Text(c, r.ErrText).TextColor(t.Danger).FontSize(12)
								} else if r.Streaming {
									ui.Text(c, "正在请求…").FontSize(12).TextColor(pal.TextSecondary)
								}
							})
						}
					})
				case a.streaming:
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Spinner(c)
						if a.result != "" {
							markdown.Stream(c, a.result, &a.md)
						} else {
							ui.Text(c, "正在翻译…").FontSize(13).TextColor(pal.TextSecondary)
						}
					})
				case a.result != "":
					markdown.View(c, a.result, &a.md)
					if a.followUpQ != "" || a.followUpStreaming {
						ui.Box(c).Height(4)
						ui.Text(c, "追问："+a.followUpQ).Bold().FontSize(12).TextColor(pal.TextSecondary)
						switch {
						case a.followUpStreaming && a.followUp != "":
							markdown.Stream(c, a.followUp, &a.md)
						case a.followUpStreaming:
							ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
								ui.Spinner(c)
								ui.Text(c, "正在回答…").FontSize(12).TextColor(pal.TextSecondary)
							})
						case a.followUp != "":
							markdown.View(c, a.followUp, &a.md)
						}
						if a.followUpErr != "" {
							ui.Text(c, a.followUpErr).TextColor(t.Danger).FontSize(12)
						}
					}
				default:
					if strings.TrimSpace(a.source) != "" {
						ui.Text(c, "按 Ctrl+Enter 立即翻译").FontSize(12).TextColor(pal.TextSecondary)
					}
				}
			})

			if a.showFollowUp() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c, &a.followUpInput).Grow(1).Placeholder("继续追问，例如：再短一点")
					send := ui.Button(c, "追问")
					if a.followUpStreaming {
						send.Disabled(true)
					}
					if send.Clicked() {
						a.sendFollowUp()
					}
				})
			}
		})

		// Footer: engine, feedback and the actions that apply to the result.
		ui.Row(c).Height(38).Padding(2, padH).Gap(4).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(pal.Border).Children(func() {
			engineBtn := ui.ButtonBase(c).Radius(5).Padding(3, 8).
				Background(pal.Card).Border(1, pal.Border).Tooltip("翻译设置")
			if engineBtn.Clicked() {
				a.showSettings()
			}
			engineBtn.Children(func() {
				label := a.engineLabel()
				if a.multiEngine && len(a.results) > 0 {
					label = fmt.Sprintf("多引擎 (%d)", len(a.results))
				}
				ui.Text(c, label).FontSize(11).TextColor(pal.TextSecondary).SingleLine()
			})

			if a.status != "" {
				ui.Text(c, a.status).FontSize(11).
					TextColor(statusColor(t, a.status)).SingleLine().Shrink(1)
			}

			ui.Spacer(c)

			audioBtn := win11IconButton(c, iconAudio, "朗读（朗读中点击停止）")
			if audioBtn.Clicked() {
				if isSpeaking() {
					stopSpeaking()
				} else {
					speakResult(a.result, a.source)
				}
			}

			copyBtn := win11IconButton(c, iconCopy, "复制译文")
			if copyBtn.Clicked() {
				a.copyResult()
			}

			retranslate := ui.Button(c, "重新翻译")
			if a.streaming || (strings.TrimSpace(a.source) == "" && !a.hasImage) {
				retranslate.Disabled(true)
			}
			if retranslate.Clicked() {
				a.startTranslation()
			}
		})
	})
}
