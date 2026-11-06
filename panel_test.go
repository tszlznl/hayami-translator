package main

import (
	"context"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestTrayPanelBounds(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		display, work, anchor mygo.Rectangle
		edge                  panelEdge
	}{
		{"top", mygo.Rectangle{Width: 1920, Height: 1080}, mygo.Rectangle{Y: 40, Width: 1920, Height: 1040}, mygo.Rectangle{}, panelTop},
		{"wayland-top", mygo.Rectangle{Width: 1920, Height: 1080}, mygo.Rectangle{Width: 1920, Height: 1080}, mygo.Rectangle{}, panelTop},
		{"bottom-tray", mygo.Rectangle{Width: 1920, Height: 1080}, mygo.Rectangle{Width: 1920, Height: 1040}, mygo.Rectangle{X: 1840, Y: 1040, Width: 24, Height: 40}, panelBottom},
		{"left-monitor", mygo.Rectangle{X: -1280, Width: 1280, Height: 720}, mygo.Rectangle{X: -1240, Width: 1240, Height: 720}, mygo.Rectangle{}, panelLeft},
		{"small", mygo.Rectangle{Width: 480, Height: 400}, mygo.Rectangle{Width: 480, Height: 360}, mygo.Rectangle{}, panelBottom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := trayPanelBounds(tc.display, tc.work, tc.anchor, tc.edge)
			if got.X < tc.work.X || got.Y < tc.work.Y || got.X+got.Width > tc.work.X+tc.work.Width || got.Y+got.Height > tc.work.Y+tc.work.Height {
				t.Fatalf("panel escapes work area: %+v in %+v", got, tc.work)
			}
			if tc.edge == panelTop && got.Y > 70 {
				t.Fatalf("panel is not next to top taskbar: %+v", got)
			}
			if got.Width > 760 || got.Height > 600 {
				t.Fatalf("unexpected panel size: %+v", got)
			}
		})
	}
}

func TestPanelNavigationPreservesDraftAndInput(t *testing.T) {
	cfg := defaultConfig()
	a := &app{cfg: cfg, form: cfg.Clone(), toolbarEnabled: !cfg.DisableSelectionToolbar, targetLang: cfg.TargetLang}
	tt := ui.NewTester(a.panelView, 760, 600)
	if err := tt.Click("输入"); err != nil {
		t.Fatal(err)
	}
	tt.Type("a persistent input draft")
	if a.inputSource != "a persistent input draft" {
		t.Fatalf("input did not receive focus: %q", a.inputSource)
	}
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	a.form.TargetLang = "日本語"
	tt.Frame()
	for _, label := range []string{"历史", "翻译", "输入", "设置"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
	}
	if a.form.TargetLang != "日本語" || !a.settingsDirty() {
		t.Fatal("navigation discarded the settings draft")
	}
	if a.inputSource != "a persistent input draft" {
		t.Fatal("navigation discarded input text")
	}
	a.requestClosePanel()
	tt.Frame()
	if !a.settingsCloseAsk || !tt.HasText("有未保存的修改") {
		t.Fatal("closing settings should ask before discarding changes")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.settingsCloseAsk || a.form.TargetLang != "日本語" {
		t.Fatal("Escape should cancel the dialog and retain the draft")
	}
}

func TestPanelNavigationInvalidatesCapture(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &app{panelPage: panelSettings, selectionReqID: 7, shotCancel: cancel}
	a.showPanel(panelSettings)
	if a.selectionReqID == 7 || ctx.Err() == nil {
		t.Fatal("reopening the same page must cancel a pending capture")
	}
	id := a.selectionReqID
	a.hidePopup()
	if a.selectionReqID == id {
		t.Fatal("closing must invalidate pending selection reads")
	}
}

func TestPanelDraftSurvivesReload(t *testing.T) {
	cfg := defaultConfig()
	cfg.Modes = cfg.BuiltinModes()
	a := &app{cfg: cfg, form: cfg.Clone(), toolbarEnabled: !cfg.DisableSelectionToolbar}
	a.form.Modes[0].Name = "draft mode"
	a.form.DisableSelectionToolbar = true
	next := cfg.Clone()
	next.Theme = "dark"
	a.applyReloadedConfig(next)
	defer applyThemeMode("system")
	if a.cfg.Modes[0].Name == "draft mode" || a.form.Modes[0].Name != "draft mode" || !a.form.DisableSelectionToolbar {
		t.Fatal("draft must remain separate from running config across reloads")
	}
}

func TestPanelPagesRenderAtSmallSize(t *testing.T) {
	for _, dark := range []bool{false, true} {
		cfg := defaultConfig()
		a := &app{cfg: cfg, form: cfg.Clone(), toolbarEnabled: !cfg.DisableSelectionToolbar}
		tt := ui.NewTester(a.panelView, 460, 380)
		tt.SetDark(dark)
		for _, page := range []panelPage{panelTranslation, panelInput, panelHistory, panelSettings, panelTranslation} {
			a.panelPage = page
			tt.Frame()
			if !tt.HasText("设置") || !tt.HasText("历史") {
				t.Fatal("panel navigation disappeared")
			}
		}
	}
}

func TestDesiredPanelSize(t *testing.T) {
	a := &app{}

	// Short word translation: compact size
	a.panelPage = panelTranslation
	a.source = "apple"
	a.result = "苹果"
	w, h := a.desiredPanelSize()
	if w > 460 || h > 280 {
		t.Fatalf("short translation too large: %dx%d", w, h)
	}

	// Long text translation: taller size
	a.source = "A longer paragraph of text that contains several complex concepts and explanations across multiple sentences."
	a.result = "包含多个复杂概念和多句话解释的较长段落文本，用于测试自适应尺寸机制。"
	wLong, hLong := a.desiredPanelSize()
	if hLong <= h {
		t.Fatalf("long translation should be taller than short translation: %d vs %d", hLong, h)
	}
	if wLong > 500 || hLong > 520 {
		t.Fatalf("long translation exceeded bounds: %dx%d", wLong, hLong)
	}

	// Other pages have their dedicated sizes
	a.panelPage = panelInput
	wInput, hInput := a.desiredPanelSize()
	if wInput != 560 || hInput != 420 {
		t.Fatalf("unexpected input window size: %dx%d", wInput, hInput)
	}

	a.panelPage = panelSettings
	wSet, hSet := a.desiredPanelSize()
	if wSet != 620 || hSet != 480 {
		t.Fatalf("unexpected settings window size: %dx%d", wSet, hSet)
	}
}
