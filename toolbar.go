package main

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var selectionToolbarEnabled atomic.Bool

func setSelectionToolbarEnabled(enabled bool) {
	selectionToolbarEnabled.Store(enabled)
}

// toolbarView renders either the 34x34 icon trigger or the 224px concise translation bubble.
func (a *app) toolbarView(c *ui.Context) {
	t := c.Theme()
	_, surface, _, border, textPrimary, textSecondary, accent, _ := win11Theme(t)

	c.Root().Background(surface).Border(1, border).Radius(8).
		Shadow(0, 4, 14, 0, ui.RGBA(0, 0, 0, 0.16))
	c.OnShortcut(0, ui.KeyEscape, func() { a.hideSelectionToolbar() })

	isBubble := a.getTriggerMode() == "取词即译气泡" || (a.toolbarIsBubble && a.getTriggerMode() != "显示图标，点击翻译")

	if isBubble {
		// 224px Windows 11 Fluent concise word bubble
		ui.Column(c).Fill().Padding(8, 12).Justify(ui.SpaceBetween).Children(func() {
			// Top row: Word + Phonetic + [Speak] [Expand]
			ui.Row(c).AlignItems(ui.Baseline).Children(func() {
				word := a.toolbarWord
				if word == "" {
					word = a.toolbarText
				}
				if len([]rune(word)) > 14 {
					word = string([]rune(word)[:14]) + "…"
				}
				ui.Text(c, word).Bold().FontSize(15).TextColor(textPrimary)

				if a.toolbarPhonetic != "" {
					ui.Box(c).Width(6)
					ui.Text(c, a.toolbarPhonetic).FontSize(11).TextColor(textSecondary)
				}

				ui.Spacer(c)

				// Audio TTS button
				speakBtn := ui.ButtonBase(c).Width(24).Height(24).Radius(4).
					Justify(ui.Center).AlignItems(ui.Center).Tooltip("朗读")
				if speakBtn.Clicked() {
					speakText(a.toolbarText)
				}
				speakBtn.Children(func() {
					ui.Icon(c, iconAudio).TextColor(textSecondary)
				})

				// Expand to full translation popup button
				expandBtn := ui.ButtonBase(c).Width(24).Height(24).Radius(4).
					Justify(ui.Center).AlignItems(ui.Center).Tooltip("展开完整翻译")
				if expandBtn.Clicked() {
					a.toolbarTranslate()
				}
				expandBtn.Children(func() {
					ui.Icon(c, iconExpand).TextColor(textSecondary)
				})
			})

			// Bottom row: Part of speech + Concise definition
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
				if a.toolbarPos != "" {
					ui.Text(c, a.toolbarPos).FontSize(13).TextColor(textSecondary)
				}
				def := a.toolbarDef
				if def == "" {
					def = "查询中…"
				}
				if len([]rune(def)) > 16 {
					def = string([]rune(def)[:16]) + "…"
				}
				ui.Text(c, def).FontSize(13).TextColor(textPrimary).SingleLine()
			})
		})
		return
	}

	// 34x34 Windows 11 Fluent selection trigger button
	btn := ui.ButtonBase(c).Fill().Radius(6).
		Justify(ui.Center).AlignItems(ui.Center).
		Tooltip("翻译选中文本")

	if btn.Clicked() {
		a.toolbarTranslate()
	}

	btn.Children(func() {
		ui.Box(c).Width(20).Height(20).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconTranslate).TextColor(accent)
		})
	})
}

// placeToolbar positions the panel near the selection
func (a *app) placeToolbar(p mygo.Point) {
	d := mygo.Screen.DisplayNearestPoint(p)
	w, h := a.toolbar.Size()
	x, y := p.X+10, p.Y+14
	if x+w > d.WorkArea.X+d.WorkArea.Width {
		x = p.X - w - 10
	}
	if y+h > d.WorkArea.Y+d.WorkArea.Height {
		y = p.Y - h - 14
	}
	x = max(d.WorkArea.X, x)
	y = max(d.WorkArea.Y, y)
	a.toolbar.SetPosition(x, y)
}

// showSelectionToolbar is called from the mouse hook
func (a *app) showSelectionToolbar(text string, p mygo.Point) {
	text = strings.TrimSpace(text)
	if a.toolbar == nil || text == "" || !selectionToolbarEnabled.Load() {
		return
	}

	mode := a.getTriggerMode()
	// If trigger mode is direct popup, marshal to UI thread and open popup
	if mode == "直接弹出完整翻译" {
		if a.popup != nil {
			a.popup.Update(func() {
				a.hideSelectionToolbar()
				a.showTranslationAt(text, p)
			})
		}
		return
	}

	a.toolbar.Update(func() {
		if !selectionToolbarEnabled.Load() {
			return
		}
		a.toolbarHadFocus = false
		a.toolbarText = text
		a.toolbarPoint = p

		// Cancel any previous in-flight bubble query
		if a.bubbleCancel != nil {
			a.bubbleCancel()
			a.bubbleCancel = nil
		}

		// Determine if bubble mode
		isBubble := a.getTriggerMode() == "取词即译气泡"
		a.toolbarIsBubble = isBubble

		if isBubble {
			info := lookupWord(text)
			targetLang := effectiveTarget(a.cfg.TargetLang, text)
			if targetLang != "简体中文" {
				info.Def = "查询中…"
				info.Pos = "释义"
			}
			a.toolbarWord = info.Word
			a.toolbarPhonetic = info.Phonetic
			a.toolbarPos = info.Pos
			a.toolbarDef = info.Def

			a.toolbar.SetSize(224, 76)

			// If definition is query fallback, trigger async quick translation
			if info.Def == "查询中…" {
				ctx, cancel := context.WithCancel(context.Background())
				a.bubbleCancel = cancel
				a.bubbleReqID++
				reqID := a.bubbleReqID

				go func(targetText string, id int) {
					cfg := a.cfg
					cfg.TargetLang = targetLang
					var b strings.Builder
					err := a.translateInputText(ctx, cfg, targetText, func(delta string) {
						b.WriteString(delta)
					})
					res := strings.TrimSpace(b.String())
					if ctx.Err() != nil {
						return
					}
					a.toolbar.Update(func() {
						if a.bubbleReqID != id || a.toolbarText != targetText {
							return
						}
						if res != "" {
							a.toolbarDef = res
							a.toolbarPos = ""
						} else if err != nil {
							a.toolbarDef = "查询失败"
							a.toolbarPos = ""
						}
						a.extendToolbarTimer()
					})
				}(text, reqID)
			}
		} else {
			a.toolbar.SetSize(34, 34)
		}

		a.placeToolbar(p)
		a.toolbar.ShowInactive()
		a.extendToolbarTimer()
	})
}

func (a *app) extendToolbarTimer() {
	a.toolbarShowID++
	id := a.toolbarShowID
	go func() {
		time.Sleep(6 * time.Second)
		if a.toolbar != nil {
			a.toolbar.Update(func() {
				if a.toolbarShowID == id && a.toolbar.IsVisible() {
					a.hideSelectionToolbar()
				}
			})
		}
	}()
}

func (a *app) hideSelectionToolbar() {
	if a.bubbleCancel != nil {
		a.bubbleCancel()
		a.bubbleCancel = nil
	}
	if a.toolbar != nil {
		a.toolbar.Hide()
	}
}

// toolbarTranslate hides the toolbar and opens the main translation popup
func (a *app) toolbarTranslate() {
	text, p := a.toolbarText, a.toolbarPoint
	a.hideSelectionToolbar()
	if strings.TrimSpace(text) == "" {
		return
	}
	if a.popup != nil {
		a.popup.Update(func() {
			a.showTranslationAt(text, p)
		})
	}
}
