package main

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// selectionToolbarEnabled is read by the low-level mouse hook, which runs on
// its own thread, and written from the settings window.
var selectionToolbarEnabled atomic.Bool

func setSelectionToolbarEnabled(enabled bool) {
	selectionToolbarEnabled.Store(enabled)
}

// toolbarView is the small panel that appears next to a mouse selection.
// The window is shown with ShowInactive, so the source application keeps the
// foreground until the user actually clicks one of these buttons.
func (a *app) toolbarView(c *ui.Context) {
	t := c.Theme()
	c.Root().Background(t.Surface).Border(1, t.Border).Radius(10)
	c.OnShortcut(0, ui.KeyEscape, func() { a.hideSelectionToolbar() })

	ui.Toolbar(c, func() {
		if ui.Button(c, "文A").Label("翻译选中文本").Tooltip("翻译选中文本").Clicked() {
			a.toolbarTranslate()
		}
		if ui.Button(c, "复制").Label("复制选中文本").Tooltip("复制选中文本").Clicked() {
			a.toolbarCopy()
		}
		if ui.Button(c, "×").Label("关闭划词助手").Tooltip("关闭划词助手").Clicked() {
			a.hideSelectionToolbar()
		}
	}).Label("划词助手")
}

// placeToolbar puts the panel below and to the right of the selection, moving
// it above or to the left when the work area ends.
func (a *app) placeToolbar(p mygo.Point) {
	d := mygo.Screen.DisplayNearestPoint(p)
	w, h := a.toolbar.Size()
	x, y := p.X+10, p.Y+16
	if x+w > d.WorkArea.X+d.WorkArea.Width {
		x = p.X - w - 10
	}
	if y+h > d.WorkArea.Y+d.WorkArea.Height {
		y = p.Y - h - 16
	}
	x = max(d.WorkArea.X, x)
	y = max(d.WorkArea.Y, y)
	a.toolbar.SetPosition(x, y)
}

// showSelectionToolbar is called from the mouse-hook goroutine after it has
// read the selected text. Update moves the state change to the UI thread.
func (a *app) showSelectionToolbar(text string, p mygo.Point) {
	if a.toolbar == nil || strings.TrimSpace(text) == "" || !selectionToolbarEnabled.Load() {
		return
	}
	a.toolbar.Update(func() {
		if !selectionToolbarEnabled.Load() {
			return
		}
		a.toolbarHadFocus = false
		a.toolbarText = text
		a.toolbarPoint = p
		a.placeToolbar(p)
		a.toolbar.ShowInactive()

		a.toolbarShowID++
		id := a.toolbarShowID
		go func() {
			time.Sleep(6 * time.Second)
			a.toolbar.Update(func() {
				if a.toolbarShowID == id && a.toolbar.IsVisible() {
					a.hideSelectionToolbar()
				}
			})
		}()
	})
}

func (a *app) hideSelectionToolbar() {
	if a.toolbar != nil {
		a.toolbar.Hide()
	}
}

// toolbarTranslate opens the normal popup with the text captured before the
// toolbar appeared. Reading the selection again here would target the panel,
// because clicking it moves the foreground away from the source application.
func (a *app) toolbarTranslate() {
	text, p := a.toolbarText, a.toolbarPoint
	a.hideSelectionToolbar()
	if strings.TrimSpace(text) == "" {
		return
	}
	a.showTranslationAt(text, p)
}

func (a *app) toolbarCopy() {
	text := a.toolbarText
	a.hideSelectionToolbar()
	if strings.TrimSpace(text) == "" {
		return
	}
	mygo.Clipboard.WriteText(text)
}
