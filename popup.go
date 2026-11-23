package main

import (
	"fmt"
	"log"
	"math"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"hayami/internal/dict"
)

var concreteLanguages = []string{
	"简体中文", "繁體中文", "English", "日本語", "한국어",
	"Français", "Deutsch", "Español", "Русский", "Português",
	"Italiano", "العربية", "ไทย", "Tiếng Việt",
}

var targetLanguages = append([]string{autoTarget}, concreteLanguages...)

var sourceLanguages = append([]string{"自动检测"}, concreteLanguages...)

// renderDictCard renders the instant offline dictionary card (word, phonetic,
// and concise meanings) while LLM contextual translation streams below it.
func (a *app) renderDictCard(c *ui.Context, pal win11Palette, entry *dict.Entry, rad float32) {
	if entry == nil {
		return
	}
	ui.Column(c).Gap(4).Padding(8, 10).Radius(rad).
		Background(pal.Card).Border(1, pal.Border).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, entry.Word).Bold().FontSize(14).TextColor(pal.TextPrimary).SingleLine()
			if entry.Phonetic != "" {
				ui.Text(c, "/"+entry.Phonetic+"/").FontSize(12).TextColor(pal.TextSecondary).SingleLine().Shrink(1)
			}
			ui.Spacer(c)
			ui.Badge(c, "离线词典").Background(pal.Accent.Alpha(0.14)).TextColor(pal.Accent)
		})
		limit := min(4, len(entry.Meanings))
		for i := 0; i < limit; i++ {
			m := entry.Meanings[i]
			ui.Row(c.Key(i)).Gap(6).AlignItems(ui.Start).Children(func() {
				if m.POS != "" {
					ui.Text(c, m.POS).Bold().FontSize(11).TextColor(pal.Accent).SingleLine()
				}
				ui.Text(c, m.Def).FontSize(12).TextColor(pal.TextPrimary).Grow(1)
			})
		}
	})
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

	c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { a.submitPopup() })

	padH := float32(14)
	if winW < 360 {
		padH = 10
	}
	ctrlRad := float32(6)
	if isKDE() {
		ctrlRad = 4
	}

	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		// Header: mode, detected source language, and target language selector.
		ui.Row(c).Height(38).Padding(0, padH).Gap(6).AlignItems(ui.Center).Children(func() {
			modeBtn := ui.ButtonBase(c).Height(26).Radius(ctrlRad).Padding(0, 8).
				AlignItems(ui.Center).Justify(ui.Center).
				Background(pal.Card).Border(1, pal.Border).Tooltip("切换模式").
				OnClick(a.cycleMode)
			if modeBtn.Hovered() {
				modeBtn.Background(pal.SurfaceHover)
			}
			modeBtn.Children(func() {
				ui.Text(c, a.modeName()).Bold().FontSize(12).TextColor(pal.Accent).SingleLine()
			})

			srcDisplay := "自动检测"
			if a.hasImage {
				srcDisplay = "截图"
			} else if strings.TrimSpace(a.source) != "" {
				srcDisplay = detectLanguageName(a.source)
			}
			ui.Text(c, srcDisplay).FontSize(12).TextColor(pal.TextSecondary).SingleLine().Shrink(1)

			ui.Box(c).Width(14).Height(14).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				ui.Icon(c, iconArrowRight).TextColor(pal.TextSecondary)
			})

			if a.targetLang == "" {
				a.targetLang = autoTarget
			}
			if ui.Select(c, &a.targetLang, targetLanguages).Width(140).Height(28).Label("目标语言").Changed() {
				a.cfg.TargetLang = a.targetLang
				a.form.TargetLang = a.targetLang
				if err := a.cfg.Save(); err != nil {
					log.Print("saving target lang: ", err)
				}
				if a.hasAnyResult() && !a.streaming {
					a.startTranslation()
				}
			}

			if a.targetLang == autoTarget && strings.TrimSpace(a.source) != "" {
				ui.Text(c, "→ "+effectiveTarget(autoTarget, a.source)).
					FontSize(11).TextColor(pal.TextSecondary).SingleLine().Shrink(1)
			}

			ui.Spacer(c)
		})

		ui.Column(c).Grow(1).MinHeight(0).Padding(2, padH, 8, padH).Gap(8).Children(func() {
			if a.hasImage {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Badge(c, "视觉模型截图识别").Background(pal.Accent.Alpha(0.14)).TextColor(pal.Accent)
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
				if a.hasAnyResult() {
					ui.Row(c).AlignItems(ui.Center).Children(func() {
						ui.Spacer(c)
						if ui.Button(c, "完成编辑").Clicked() {
							a.popupEditing = false
						}
					})
				}
			} else {
				preview := ui.ButtonBase(c).Radius(ctrlRad).Padding(8, 10).
					Background(pal.Card).Border(1, pal.Border).
					AlignItems(ui.Start).Tooltip("点击编辑原文").
					OnClick(func() {
						a.popupEditing = true
						a.focusInput = true
					})
				if preview.Hovered() {
					preview.Border(1, pal.Accent.Alpha(0.6))
				}
				preview.Children(func() {
					ui.Text(c, clipText(a.source, 400)).FontSize(13).TextColor(pal.TextSecondary)
				})
			}

			if (a.streaming || a.followUpStreaming) && !a.multiEngine {
				if a.popupScroll.MaxY > 0 && a.popupScroll.Y < a.popupScroll.MaxY-24 {
					// User manually scrolled up to read earlier content; stop forcing scroll.
					a.popupAutoScroll = false
				} else if a.popupScroll.MaxY > 0 && a.popupScroll.Y >= a.popupScroll.MaxY-8 {
					a.popupAutoScroll = true
				}
				if a.popupAutoScroll {
					a.popupScroll.Y = math.MaxFloat32
				}
			}

			ui.Scroll(c).Grow(1).MinHeight(0).TrackScroll(&a.popupScroll).Children(func() {
				if a.dictEntry != nil {
					ui.Column(c.Key("dict-card")).Padding(0, 0, 8, 0).Children(func() {
						a.renderDictCard(c, pal, a.dictEntry, ctrlRad)
					})
				}
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
							ui.Column(c.Key(r.ID)).Gap(4).Padding(8, 10).Radius(ctrlRad).
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
										if r.ErrHint != "" {
											ui.Text(c, r.ErrHint).FontSize(11).TextColor(pal.TextSecondary)
										}
									}
								} else if r.ErrText != "" {
									ui.Text(c, r.ErrText).TextColor(t.Danger).FontSize(12)
									if r.ErrHint != "" {
										ui.Text(c, r.ErrHint).FontSize(11).TextColor(pal.TextSecondary)
									}
								} else if r.Streaming {
									ui.Text(c, "正在请求…").FontSize(12).TextColor(pal.TextSecondary)
								}
							})
						}
					})
				case a.streaming:
					if a.result != "" {
						markdown.Stream(c, a.result, &a.md)
						ui.Row(c).Gap(6).Padding(4, 0, 0, 0).AlignItems(ui.Center).Children(func() {
							ui.Spinner(c)
							ui.Text(c, "正在生成…").FontSize(11).TextColor(pal.TextSecondary)
						})
					} else {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Spinner(c)
							ui.Text(c, "正在翻译…").FontSize(13).TextColor(pal.TextSecondary)
						})
					}
				case a.result != "":
					markdown.View(c, a.result, &a.md)
					for idx, turn := range a.followUpTurns {
						turnIdx := idx
						turnQ := turn.Question
						turnAns := turn.Answer
						ui.Column(c.Key(fmt.Sprintf("followup-turn-%d", turnIdx))).Gap(4).Padding(6, 0, 0, 0).Children(func() {
							ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "追问："+turnQ).Bold().FontSize(12).TextColor(pal.TextSecondary).Grow(1)
								if strings.TrimSpace(turnAns) != "" {
									if ui.Button(c, "复制").Tooltip("复制追问回答").Clicked() {
										mygo.Clipboard.WriteText(turnAns)
										a.status = "已复制追问回答"
									}
								}
							})
							if strings.TrimSpace(turnAns) != "" {
								ui.Text(c, turnAns).FontSize(13).TextColor(pal.TextPrimary).Selectable()
							}
						})
					}
					if a.followUpQ != "" || a.followUpStreaming || a.followUpErr != "" {
						ui.Column(c.Key("followup-inflight")).Gap(4).Padding(6, 0, 0, 0).Children(func() {
							if a.followUpQ != "" {
								ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
									ui.Text(c, "追问："+a.followUpQ).Bold().FontSize(12).TextColor(pal.TextSecondary).Grow(1)
								})
							}
							switch {
							case a.followUpStreaming && a.followUp != "":
								ui.Text(c, a.followUp).FontSize(13).TextColor(pal.TextPrimary).Selectable()
								ui.Row(c).Gap(6).Padding(4, 0, 0, 0).AlignItems(ui.Center).Children(func() {
									ui.Spinner(c)
									ui.Text(c, "正在回答…").FontSize(11).TextColor(pal.TextSecondary)
								})
							case a.followUpStreaming:
								ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
									ui.Spinner(c)
									ui.Text(c, "正在回答…").FontSize(12).TextColor(pal.TextSecondary)
								})
							case a.followUp != "":
								ui.Text(c, a.followUp).FontSize(13).TextColor(pal.TextPrimary).Selectable()
							}
							if a.followUpErr != "" {
								ui.Text(c, a.followUpErr).TextColor(t.Danger).FontSize(12)
							}
						})
					}
				default:
					if a.dictEntry == nil && strings.TrimSpace(a.source) != "" {
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
		ui.Row(c).Height(38).Padding(2, padH).Gap(6).AlignItems(ui.Center).
			Background(pal.Footer).BorderWidth(1, 0, 0, 0).BorderColor(pal.Border).Children(func() {
			engineBtn := ui.ButtonBase(c).Height(26).Radius(ctrlRad).Padding(0, 8).
				AlignItems(ui.Center).Justify(ui.Center).
				Background(pal.Card).Border(1, pal.Border).Tooltip("翻译设置").
				OnClick(a.showSettings)
			if engineBtn.Hovered() {
				engineBtn.Background(pal.SurfaceHover)
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

			audioBtn := win11IconButton(c, iconAudio, "朗读（朗读中点击停止）").OnClick(func() {
				if isSpeaking() {
					stopSpeaking()
					a.status = "已停止朗读"
				} else if err := speakResultWithNotify(a.result, a.source, func(err error) {
					a.postUI(func() {
						a.status = "朗读失败：" + err.Error()
					})
				}); err != nil {
					a.status = err.Error()
				}
			})
			_ = audioBtn

			copyBtn := win11IconButton(c, iconCopy, "复制译文").OnClick(a.copyResult)
			_ = copyBtn

			actionLabel := "翻译"
			if a.hasAnyResult() {
				actionLabel = "重新翻译"
			}
			retranslate := ui.Button(c, actionLabel)
			if a.streaming || (strings.TrimSpace(a.source) == "" && !a.hasImage) {
				retranslate.Disabled(true)
			}
			if retranslate.Clicked() {
				a.runTranslation(a.hasAnyResult())
			}
		})
	})
}
