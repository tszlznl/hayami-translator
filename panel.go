package main

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type panelPage uint8

const (
	panelTranslation panelPage = iota
	panelInput
	panelHistory
	panelSettings
)

type panelEdge uint8

const (
	panelBottom panelEdge = iota
	panelTop
	panelLeft
	panelRight
)

func (a *app) panelView(c *ui.Context) {
	t := fluentTheme(c)
	pal := win11Theme(t)
	panelBackground(c, t)
	c.OnShortcut(0, ui.KeyEscape, a.requestClosePanel)

	rad := float32(12)
	if isKDE() {
		rad = 8
	}

	ui.Column(c).Fill().Radius(rad).Border(1, pal.Border).Children(func() {
		ui.Row(c).Height(38).Padding(4, 10).Gap(4).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(pal.Border).Children(func() {
			for page, label := range []string{"翻译", "输入", "历史", "设置"} {
				page := panelPage(page)
				tab := ui.ButtonBase(c.Key(page)).Height(28).Padding(0, 10).Radius(5).
					AlignItems(ui.Center).Justify(ui.Center).
					Tooltip(label + "页").
					OnClick(func() { a.openPanelPage(page) })
				if a.panelPage == page {
					tab.Background(pal.Accent.Alpha(0.18)).Border(1, pal.Accent.Alpha(0.40))
					tab.Children(func() {
						ui.Text(c, label).FontSize(12).Bold().TextColor(pal.Accent)
					})
				} else {
					if tab.Hovered() {
						tab.Background(pal.SurfaceHover)
					}
					tab.Children(func() {
						ui.Text(c, label).FontSize(12).TextColor(pal.TextSecondary)
					})
				}
			}
			ui.Spacer(c)
			tip := "固定面板"
			if a.popupPinned {
				tip = "取消固定"
			}
			pin := win11IconButton(c, iconPin, tip).Label(tip)
			if a.popupPinned {
				pin.Background(pal.Accent.Alpha(0.20)).Border(1, pal.Accent.Alpha(0.40))
			}
			pin.OnClick(func() { a.setPanelPinned(!a.popupPinned) })
			win11IconButton(c, iconClose, "收起面板 (Esc)").Label("收起面板").OnClick(a.requestClosePanel)
		})
		ui.Column(c.Key(a.panelPage)).Grow(1).Children(func() {
			switch a.panelPage {
			case panelTranslation:
				a.popupView(c)
			case panelInput:
				a.inputWindowView(c)
			case panelHistory:
				a.historyView(c)
			case panelSettings:
				a.settingsView(c)
			}
		})
	})
}

func (a *app) createPanelBackdrops() {
	if !panelNeedsBackdrop() {
		return
	}
	// Layer-shell panels keep exclusive keyboard focus; a backdrop receives outside clicks.
	for _, display := range mygo.Screen.Displays() {
		backdrop := mygo.NewWindow(mygo.WindowOptions{
			Title: "Hayami backdrop", Hidden: true, Panel: true,
			Frameless: true, Transparent: true, SkipTaskbar: true, DisableResize: true,
			Content: ui.View(func(c *ui.Context) {
				c.Root().Background(ui.RGBA(0, 0, 0, 0))
				c.OnShortcut(0, ui.KeyEscape, a.requestClosePanel)
				ui.Box(c).Fill().OnClick(a.requestClosePanel)
			}),
		})
		backdrop.SetBounds(display.Bounds)
		backdrop.OnClose(func(e *mygo.CloseEvent) { a.handleWindowClose(e, a.requestClosePanel) })
		a.panelBackdrops = append(a.panelBackdrops, backdrop)
	}
}

func (a *app) openPanelPage(page panelPage) {
	switch page {
	case panelTranslation:
		a.showPopup()
	case panelInput:
		a.showInputWindow()
	case panelHistory:
		a.showHistory()
	case panelSettings:
		a.showSettings()
	}
}

func (a *app) showPanel(page panelPage) {
	a.invalidateCapture()
	a.panelPage = page
	if a.popup == nil {
		return
	}
	a.placePanel()
	a.popupHadFocus = a.popup.IsFocused()
	for _, backdrop := range a.panelBackdrops {
		if a.popupPinned {
			backdrop.Hide()
		} else {
			backdrop.ShowInactive()
		}
	}
	if a.popupPinned && len(a.panelBackdrops) > 0 {
		a.popup.ShowInactive()
	} else {
		a.popup.Show()
		a.popup.Focus()
	}
	a.popup.Invalidate()
}

func (a *app) setPanelPinned(pinned bool) {
	changed := a.popupPinned != pinned
	a.popupPinned = pinned
	if changed && a.popup != nil && a.popup.IsVisible() {
		a.showPanel(a.panelPage)
	}
}

func (a *app) invalidateCapture() {
	a.selectionReqID++
	if a.shotCancel != nil {
		a.shotCancel()
		a.shotCancel = nil
	}
}

func (a *app) togglePanel() {
	if a.popup != nil && a.popup.IsVisible() {
		a.requestClosePanel()
		return
	}
	a.openPanelPage(a.panelPage)
}

func (a *app) requestClosePanel() {
	if a.panelPage == panelSettings {
		a.requestCloseSettings()
		return
	}
	a.hidePopup()
}

func (a *app) desiredPanelSize() (int, int) {
	switch a.panelPage {
	case panelInput:
		return 560, 420
	case panelHistory:
		return 560, 450
	case panelSettings:
		return 620, 480
	case panelTranslation:
		fallthrough
	default:
		w := 440
		if a.multiEngine {
			w = 580
		}
		if a.hasImage {
			return w, 400
		}

		sourceLen := len([]rune(strings.TrimSpace(a.source)))
		resultLen := len([]rune(strings.TrimSpace(a.result)))
		followUpLen := len([]rune(strings.TrimSpace(a.followUp)))

		totalLen := sourceLen + resultLen + followUpLen

		var h int
		switch {
		case a.popupEditing || totalLen == 0:
			h = 280
		case totalLen < 40 && strings.Count(a.source, "\n") == 0 && strings.Count(a.result, "\n") <= 1:
			h = 230
		case totalLen < 120 && strings.Count(a.result, "\n") <= 2:
			h = 290
		case totalLen < 300:
			h = 360
		default:
			h = 440
		}

		if a.showFollowUp() {
			h += 48
		}
		if a.multiEngine && len(a.results) > 1 {
			h = min(500, max(h, 400))
		}

		h = min(500, max(220, h))
		return w, h
	}
}

func (a *app) placePanel() {
	edge, screen := desktopPanelLocation()
	display := mygo.Screen.PrimaryDisplay()
	for _, d := range mygo.Screen.Displays() {
		if d.ID == int64(screen) {
			display = d
			break
		}
	}
	var anchor mygo.Rectangle
	if tray, ok := a.tray.(interface{ Bounds() mygo.Rectangle }); ok {
		anchor = tray.Bounds()
		if anchor.Width > 0 && anchor.Height > 0 {
			display = mygo.Screen.DisplayMatching(anchor)
		}
	}
	targetW, targetH := a.desiredPanelSize()
	a.popup.SetBounds(trayPanelBoundsSized(display.Bounds, display.WorkArea, anchor, edge, targetW, targetH))
}

func trayPanelBounds(display, work, anchor mygo.Rectangle, edge panelEdge) mygo.Rectangle {
	return trayPanelBoundsSized(display, work, anchor, edge, 440, 320)
}

func trayPanelBoundsSized(display, work, anchor mygo.Rectangle, edge panelEdge, targetW, targetH int) mygo.Rectangle {
	const gap = 10
	if anchor.Width > 0 && anchor.Height > 0 {
		cx, cy := anchor.X+anchor.Width/2, anchor.Y+anchor.Height/2
		distances := []int{display.Y + display.Height - cy, cy - display.Y, cx - display.X, display.X + display.Width - cx}
		for i, distance := range distances {
			if distance < distances[edge] {
				edge = panelEdge(i)
			}
		}
	}
	// Wayland may report the whole output as its work area, including Plasma's panel.
	if work == display && anchor.Width == 0 {
		switch edge {
		case panelTop:
			work.Y += 56
			work.Height -= 56
		case panelLeft:
			work.X += 56
			work.Width -= 56
		case panelRight:
			work.Width -= 56
		default:
			work.Height -= 56
		}
	}
	w, h := min(targetW, max(1, work.Width-2*gap)), min(targetH, max(1, work.Height-2*gap))
	x, y := work.X+work.Width-w-gap, work.Y+work.Height-h-gap
	switch edge {
	case panelTop:
		y = work.Y + gap
	case panelLeft:
		x = work.X + gap
	}
	if anchor.Width > 0 && anchor.Height > 0 {
		switch edge {
		case panelTop, panelBottom:
			x = anchor.X + anchor.Width - w
		case panelLeft, panelRight:
			y = anchor.Y + anchor.Height - h
		}
	}
	return mygo.Rectangle{
		X:     min(max(x, work.X), work.X+work.Width-w),
		Y:     min(max(y, work.Y), work.Y+work.Height-h),
		Width: w, Height: h,
	}
}

func (a *app) receiveSelection(text string) {
	text = strings.TrimSpace(text)
	if text == "" || a.quitting || !selectionToolbarEnabled.Load() || a.selectionBlocked.Load() || a.settingsDirty() {
		return
	}
	if a.popup == nil || a.popup.IsFocused() || (a.popup.IsVisible() && a.panelPage != panelTranslation) {
		return
	}
	a.selectionReqID++
	if !hasConfiguredProvider(a.cfg) {
		a.showSettings()
		return
	}
	a.showTranslation(text)
}
