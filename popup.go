package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

var targetLanguages = []string{
	autoTarget, "简体中文", "繁體中文", "English", "日本語", "한국어",
	"Français", "Deutsch", "Español", "Русский", "Português",
	"Italiano", "العربية", "ไทย", "Tiếng Việt",
}

// popupView is the translation panel: the source, a target language, the
// streamed Markdown reply, and the ways to copy or retry it.
func (a *app) popupView(c *ui.Context) {
	t := c.Theme()
	c.Root().Background(t.Background).Border(1, t.Border).Radius(10)
	c.OnShortcut(0, ui.KeyEscape, func() { a.hidePopup() })
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() { a.startTranslation() })

	ui.Column(c).Fill().Children(func() {
		// The header is the drag region. Keeping the actions here makes the
		// panel feel like a small window instead of a form with a title bar.
		ui.Row(c).Height(52).Padding(10, 16).Gap(10).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
			ui.Box(c).Width(28).Height(28).Radius(8).Background(t.Accent).
				AlignItems(ui.Center).Justify(ui.Center).Children(func() {
				ui.Text(c, "H").Bold().TextColor(t.AccentText)
			})
			ui.Column(c).Gap(1).Justify(ui.Center).Children(func() {
				ui.Text(c, "Hayami Translator").Bold().FontSize(14)
				ui.Text(c, "选中即译 · 支持输入和截图").FontSize(11).TextColor(t.TextMuted)
			})
			ui.Spacer(c)
			pinTip := "固定后点击其他窗口不自动关闭"
			if a.popupPinned {
				pinTip = "取消固定"
			}
			pin := ui.Button(c, "固定").Tooltip(pinTip)
			if a.popupPinned {
				pin.Background(t.Accent.Alpha(0.14)).TextColor(t.Accent)
			}
			if pin.Clicked() {
				a.popupPinned = !a.popupPinned
				if a.popupPinned {
					a.status = "弹窗已固定"
				} else {
					a.status = "已取消固定"
				}
			}
			if ui.Button(c, "历史").Tooltip("查看翻译历史").Clicked() {
				a.hidePopup()
				a.showHistory()
			}
			if ui.Button(c, "设置").Tooltip("打开设置").Clicked() {
				a.hidePopup()
				a.showSettings()
			}
			if ui.Button(c, "✕").Label("关闭").Tooltip("关闭 (Esc)").Clicked() {
				a.hidePopup()
			}
		})

		ui.Column(c).Grow(1).Padding(16).Gap(10).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				label := "原文"
				if a.hasImage {
					label = "截图"
				}
				ui.Text(c, label).FontSize(12).Bold().TextColor(t.TextMuted)
				if a.hasImage {
					ui.Badge(c, "视觉模型识别").
						Background(t.Accent.Alpha(0.12)).TextColor(t.Accent)
				} else if n := len([]rune(a.source)); n > 0 {
					ui.Text(c, fmt.Sprintf("%d 字符", n)).FontSize(11).TextColor(t.TextMuted)
				}
				ui.Spacer(c)
				if !a.hasImage && !a.streaming && strings.TrimSpace(a.source) != "" {
					if ui.Button(c, "清空").Tooltip("清空原文和译文").Clicked() {
						a.resetTranslation()
						a.focusInput = true
					}
				}
			})

			if a.hasImage {
				ui.Column(c).Gap(3).Padding(12, 10).Radius(8).
					Background(t.Surface).Border(1, t.Border).Children(func() {
					ui.Text(c, "已截取屏幕区域").Bold().FontSize(13)
					ui.Text(c, "结果由视觉模型识别并翻译。").FontSize(12).TextColor(t.TextMuted)
				})
			} else {
				ui.TextArea(c, &a.source).
					Bind(&a.inputHandle).
					Lines(2, 6).
					Placeholder("输入文字，或选中任意窗口里的文字后按 " + a.cfg.Hotkey)
				if a.focusInput {
					a.inputHandle.Focus(c)
					a.focusInput = false
				}
			}

			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if a.streaming {
					if ui.Button(c, "停止").Tooltip("停止当前翻译").Clicked() {
						a.cancelStream()
						a.status = "已停止"
					}
				} else if ui.PrimaryButton(c, "翻译").Tooltip("翻译 (Ctrl+Enter)").Clicked() {
					a.startTranslation()
				}

				if ui.Button(c, "复制").Disabled(strings.TrimSpace(a.result) == "").
					Tooltip("复制译文").Clicked() {
					a.copyResult()
				}
				if a.result != "" && !a.streaming {
					if ui.Button(c, "重译").Tooltip("重新翻译原文").Clicked() {
						a.startTranslation()
					}
				}

				ui.Spacer(c)
				ui.Text(c, "目标").FontSize(11).TextColor(t.TextMuted)
				if ui.Select(c, &a.targetLang, targetLanguages).
					Width(164).Label("目标语言").Tooltip("翻译成哪种语言").Changed() {
					a.cfg.TargetLang = a.targetLang
					a.form.TargetLang = a.targetLang
					if err := a.cfg.save(); err != nil {
						a.status = "目标语言保存失败：" + err.Error()
					}
					if a.result != "" && !a.streaming {
						a.startTranslation()
					}
				}
			})

			ui.Column(c).Grow(1).Radius(10).Background(t.Surface).
				Border(1, t.Border).Children(func() {
				ui.Row(c).Padding(10, 12).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "译文").FontSize(12).Bold().TextColor(t.TextMuted)
					switch {
					case a.errText != "":
						ui.Badge(c, "失败").Background(t.Danger.Alpha(0.12)).TextColor(t.Danger)
					case a.streaming:
						ui.Badge(c, "翻译中").Background(t.Warning.Alpha(0.12)).TextColor(t.Warning)
					case a.result != "":
						ui.Badge(c, "完成").Background(t.Success.Alpha(0.12)).TextColor(t.Success)
					}
					ui.Spacer(c)
					if a.status != "" && a.result != "" && !a.streaming {
						ui.Text(c, a.status).FontSize(11).
							TextColor(statusColor(t, a.status)).SingleLine().Tooltip(a.status)
					}
				})

				if a.streaming {
					a.popupScroll.Y = math.MaxFloat32
				}
				ui.Scroll(c).Grow(1).TrackScroll(&a.popupScroll).
					Padding(0, 12, 12, 12).Children(func() {
					switch {
					case a.errText != "":
						ui.Column(c).Gap(6).Children(func() {
							ui.Text(c, a.errText).TextColor(t.Danger)
							ui.Text(c, "按 Ctrl+Enter 重试，或检查设置里的翻译服务。").FontSize(11).TextColor(t.TextMuted)
						})
					case a.result != "" && a.streaming:
						markdown.Stream(c, a.result, &a.md)
					case a.result != "":
						markdown.View(c, a.result, &a.md)
					case a.streaming:
						ui.Row(c).Gap(8).Children(func() {
							ui.Spinner(c)
							ui.Text(c, "正在翻译…").TextColor(t.TextMuted)
						})
					default:
						ui.Column(c).Gap(5).Padding(18, 14).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "译文将显示在这里").Bold().TextColor(t.TextMuted)
							ui.Text(c, "输入文字后按 Ctrl+Enter，或选中文本后按 "+a.cfg.Hotkey).
								FontSize(11).TextColor(t.TextMuted)
						})
					}
				})
			})

			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				foot := a.providerName
				if foot == "" {
					foot = a.cfg.Model
				}
				if foot != "" {
					ui.Badge(c, foot).Background(t.Surface).TextColor(t.TextMuted)
				}
				if a.status != "" && (a.result == "" || a.streaming) {
					ui.Text(c, a.status).FontSize(11).
						TextColor(statusColor(t, a.status)).SingleLine().Tooltip(a.status)
				}
				ui.Spacer(c)
				ui.Text(c, "Esc 关闭 · Ctrl+Enter 翻译").FontSize(11).TextColor(t.TextMuted)
			})
		})
	})
}
