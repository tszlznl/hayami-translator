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
	t := fluentTheme(c)
	pal := win11Theme(t)

	c.Root().Background(pal.Surface).Border(1, pal.Border).Radius(8).
		Shadow(0, 4, 14, 0, ui.RGBA(0, 0, 0, 0.16))
	c.OnShortcut(0, ui.KeyEscape, func() { a.hideSelectionToolbar() })

	mode := a.getTriggerMode()
	// A bubble already on screen stays a bubble when the mode changes under
	// it; only the icon mode switches it back.
	isBubble := mode == triggerModeBubble || (a.toolbarIsBubble && mode == triggerModePopup)

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
				ui.Text(c, word).Bold().FontSize(15).TextColor(pal.TextPrimary)

				if a.toolbarPhonetic != "" {
					ui.Box(c).Width(6)
					ui.Text(c, a.toolbarPhonetic).FontSize(11).TextColor(pal.TextSecondary)
				}

				ui.Spacer(c)

				// Audio TTS button
				speakBtn := ui.ButtonBase(c).Width(24).Height(24).Radius(4).
					Justify(ui.Center).AlignItems(ui.Center).Tooltip("朗读（朗读中点击停止）")
				if speakBtn.Clicked() {
					if isSpeaking() {
						stopSpeaking()
					} else {
						speakText(a.toolbarText)
					}
				}
				speakBtn.Children(func() {
					ui.Icon(c, iconAudio).TextColor(pal.TextSecondary)
				})

				// Expand to full translation popup button
				expandBtn := ui.ButtonBase(c).Width(24).Height(24).Radius(4).
					Justify(ui.Center).AlignItems(ui.Center).Tooltip("展开完整翻译")
				if expandBtn.Clicked() {
					a.toolbarTranslate()
				}
				expandBtn.Children(func() {
					ui.Icon(c, iconExpand).TextColor(pal.TextSecondary)
				})
			})

			// Bottom row: Part of speech + Concise definition
			ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
				if a.toolbarPos != "" {
					ui.Text(c, a.toolbarPos).FontSize(13).TextColor(pal.TextSecondary)
				}
				def := a.toolbarDef
				if def == "" {
					def = "查询中…"
				}
				if len([]rune(def)) > 16 {
					def = string([]rune(def)[:16]) + "…"
				}
				ui.Text(c, def).FontSize(13).TextColor(pal.TextPrimary).SingleLine()
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
			ui.Icon(c, iconTranslate).TextColor(pal.Accent)
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
	if mode == triggerModePopup {
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
		isBubble := a.getTriggerMode() == triggerModeBubble
		a.toolbarIsBubble = isBubble

		if isBubble {
			info, found := lookupWord(text)
			targetLang := effectiveTarget(a.cfg.TargetLang, text)
			// The offline dictionary only covers Chinese-bound lookups of
			// words it knows; everything else resolves online.
			pending := !found || targetLang != "简体中文"
			if pending {
				info = wordInfo{Word: text, Phonetic: "/en/", Pos: "释义"}
			}
			a.toolbarWord = info.Word
			a.toolbarPhonetic = info.Phonetic
			a.toolbarPos = info.Pos
			a.toolbarDef = info.Def

			a.toolbar.SetSize(224, 76)

			if pending {
				ctx, cancel := context.WithCancel(context.Background())
				a.bubbleCancel = cancel
				a.bubbleReqID++
				reqID := a.bubbleReqID

				// Snapshot the config on the UI thread: the goroutine below
				// must not read a.cfg, which saveSettings replaces.
				cfg := a.cfg
				cfg.TargetLang = targetLang

				go func(targetText string, id int) {
					var b strings.Builder
					err := translateWithFallback(ctx, cfg, cfg.enabledProviders(false), textMessages(cfg, targetText), targetText, nil, func(delta string) {
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
