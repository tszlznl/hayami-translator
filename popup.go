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

// popupView renders the Windows 11 Fluent floating translation card with responsive layout.
func (a *app) popupView(c *ui.Context) {
	t := c.Theme()
	winW, _ := c.Size()
	_, surface, _, border, textPrimary, textSecondary, accent, footerBg := win11Theme(t)

	c.Root().Background(surface).Border(1, border).Radius(8).
		Shadow(0, 8, 20, 0, ui.RGBA(0, 0, 0, 0.18))
	c.OnShortcut(0, ui.KeyEscape, func() { a.hidePopup() })
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { a.startTranslation() })

	ui.Column(c).Fill().Children(func() {
		// Responsive Header Bar
		padH := float32(14)
		if winW < 340 {
			padH = 8
		}
		ui.Row(c).Height(38).Padding(6, 6, 0, padH).AlignItems(ui.Center).DragWindow().Children(func() {
			srcDisplay := "自动检测"
			if a.hasImage {
				srcDisplay = "截图"
			} else if strings.TrimSpace(a.source) != "" {
				srcDisplay = detectLanguageName(a.source)
			}
			ui.Text(c, srcDisplay).FontSize(12).TextColor(textSecondary).Shrink(1)

			ui.Box(c).Width(14).Height(14).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				ui.Icon(c, iconArrowRight)
			})

			// Target language selection
			targetLabel := a.targetLang
			if targetLabel == "" {
				targetLabel = autoTarget
			}
			displayLabel := targetLabel
			if targetLabel == autoTarget && strings.TrimSpace(a.source) != "" {
				displayLabel = "自动 (" + effectiveTarget(autoTarget, a.source) + ")"
			}
			targetBtn := ui.ButtonBase(c).Radius(4).Padding(2, 6).
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
				if err := a.cfg.save(); err != nil {
					log.Print("saving target lang: ", err)
				}
				if a.hasAnyResult() && !a.streaming {
					a.startTranslation()
				}
			}
			targetBtn.Children(func() {
				ui.Text(c, displayLabel).Bold().FontSize(12).TextColor(accent).SingleLine()
			})

			ui.Spacer(c)

			// Pin window button
			pinTip := "固定窗口"
			if a.popupPinned {
				pinTip = "取消固定"
			}
			pinBtn := ui.ButtonBase(c).Width(28).Height(28).Radius(4).
				Justify(ui.Center).AlignItems(ui.Center).Tooltip(pinTip)
			if a.popupPinned {
				pinBtn.Background(accent.Alpha(0.14))
			}
			if pinBtn.Clicked() {
				a.popupPinned = !a.popupPinned
				if a.popupPinned {
					a.status = "弹窗已固定"
				} else {
					a.status = "已取消固定"
				}
			}
			pinBtn.Children(func() {
				ui.Icon(c, iconPin)
			})

			// Close button
			closeBtn := ui.ButtonBase(c).Width(28).Height(28).Radius(4).
				Justify(ui.Center).AlignItems(ui.Center).Tooltip("关闭 (Esc)")
			if closeBtn.Clicked() {
				a.hidePopup()
			}
			closeBtn.Children(func() {
				ui.Icon(c, iconClose)
			})
		})

		// Translation Content Area (Fluid / Scrollable)
		ui.Column(c).Grow(1).Padding(4, padH, 8, padH).Gap(6).Children(func() {
			if a.hasImage {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Badge(c, "视觉模型截图识别").Background(accent.Alpha(0.12)).TextColor(accent)
				})
			} else if strings.TrimSpace(a.source) == "" {
				ui.TextArea(c, &a.source).
					Bind(&a.inputHandle).
					Grow(1).
					Placeholder("输入文字后按 Ctrl+Enter 翻译…")
				if a.focusInput {
					a.inputHandle.Focus(c)
					a.focusInput = false
				}
			} else {
				srcText := strings.TrimSpace(a.source)
				maxRune := 80
				if winW < 340 {
					maxRune = 40
				}
				if len([]rune(srcText)) > maxRune {
					srcText = string([]rune(srcText)[:maxRune]) + "…"
				}
				ui.Text(c, srcText).FontSize(12).TextColor(textSecondary).SingleLine()
			}

			if a.streaming && !a.multiEngine {
				a.popupScroll.Y = math.MaxFloat32
			}

			ui.Scroll(c).Grow(1).TrackScroll(&a.popupScroll).Children(func() {
				switch {
				case a.errText != "":
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, a.errText).TextColor(t.Danger).FontSize(13)
						ui.Text(c, "按 Ctrl+Enter 重试，或检查设置里的翻译服务。").FontSize(11).TextColor(textSecondary)
					})
				case a.multiEngine && len(a.results) > 0:
					ui.Column(c).Gap(8).Children(func() {
						for i := range a.results {
							r := &a.results[i]
							ui.Column(c.Key(r.ID)).Gap(4).Padding(8, 10).Radius(6).
								Background(t.Background).Border(1, border).Children(func() {
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
									ui.Text(c, "正在请求…").FontSize(12).TextColor(textSecondary)
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
							ui.Text(c, "正在翻译…").FontSize(14).TextColor(textSecondary)
						}
					})
				case a.result != "":
					// Responsive font sizing based on window width and text length
					resText := a.result
					fontSize := float32(20)
					if winW < 360 || len([]rune(resText)) > 60 {
						fontSize = 16
					}
					if !strings.Contains(resText, "\n") && !strings.Contains(resText, "#") {
						ui.Text(c, resText).Bold().FontSize(fontSize).TextColor(textPrimary)
					} else {
						markdown.View(c, resText, &a.md)
					}
				default:
					if strings.TrimSpace(a.source) != "" {
						ui.Text(c, "按 Ctrl+Enter 立即翻译").FontSize(13).TextColor(textSecondary)
					}
				}
			})

			// Engine info line
			engineLabel := a.providerName
			if a.multiEngine && len(a.results) > 0 {
				engineLabel = fmt.Sprintf("多引擎并发 (%d 个服务)", len(a.results))
			} else if engineLabel == "" {
				engineLabel = a.cfg.Model
			}
			if engineLabel == "" {
				engineLabel = "Microsoft Translator"
			}
			ui.Text(c, "引擎："+engineLabel).FontSize(11).TextColor(textSecondary).SingleLine()
		})

		// Bottom Action Bar
		ui.Row(c).Height(38).Padding(4, padH).Gap(4).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(border).
			Background(footerBg).Radius(0, 0, 8, 8).Children(func() {
			copyBtn := win11IconButton(c, iconCopy, "复制译文")
			if copyBtn.Clicked() {
				a.copyResult()
			}

			audioBtn := win11IconButton(c, iconAudio, "朗读译文")
			if audioBtn.Clicked() {
				if a.result != "" {
					speakText(a.result)
				} else if a.source != "" {
					speakText(a.source)
				}
			}

			if a.status != "" {
				ui.Text(c, a.status).FontSize(11).
					TextColor(statusColor(t, a.status)).SingleLine().Shrink(1)
			}

			ui.Spacer(c)

			settingsBtn := win11IconButton(c, iconSettings, "翻译设置")
			if settingsBtn.Clicked() {
				a.hidePopup()
				a.showSettings()
			}
		})
	})
}
