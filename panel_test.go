package main

import (
	"context"
	"errors"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"hayami/internal/config"
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
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
		translationState: translationState{
			targetLang: cfg.TargetLang,
		},
	}
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
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
	}
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
		a := &app{
			cfg:            cfg,
			toolbarEnabled: !cfg.DisableSelectionToolbar,
			settingsState: settingsState{
				form: cfg.Clone(),
			},
		}
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

func TestSettingsSidebarAndDiscardAtDefaultSize(t *testing.T) {
	cfg := defaultConfig()
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelSettings,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
	}
	w, h := a.desiredPanelSize()
	tt := ui.NewTester(a.panelView, w, h)
	tt.Frame()

	for _, label := range []string{"常规", "翻译引擎", "快捷键", "外观与风格", "关于"} {
		if !tt.HasText(label) {
			t.Fatalf("settings sidebar label %q missing at default size %dx%d", label, w, h)
		}
	}
	if tt.HasText("放弃修改") {
		t.Fatal("discard button should be hidden when draft is clean")
	}

	a.form.Hotkey = "Alt+Shift+Q"
	tt.Frame()
	if !tt.HasText("放弃修改") {
		t.Fatal("discard button should appear when draft is dirty")
	}
	if err := tt.Click("放弃修改"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settingsDirty() || a.form.Hotkey != cfg.Hotkey {
		t.Fatal("clicking 放弃修改 must restore clean draft")
	}
}

func TestInputWindowDualPaneAtDefaultSize(t *testing.T) {
	cfg := defaultConfig()
	a := &app{
		cfg:            cfg,
		toolbarEnabled: !cfg.DisableSelectionToolbar,
		panelPage:      panelInput,
		settingsState: settingsState{
			form: cfg.Clone(),
		},
	}
	w, h := a.desiredPanelSize()
	tt := ui.NewTester(a.panelView, w, h)
	tt.Frame()

	if !tt.HasText("在左侧输入文本后点击翻译") {
		t.Fatalf("input window should use side-by-side dual pane at %dx%d", w, h)
	}
	if tt.HasText("在上方输入文本后点击翻译") {
		t.Fatalf("input window unexpectedly stacked vertically at %dx%d", w, h)
	}
}

func TestKDEPalettePreservesAccent(t *testing.T) {
	props := map[string]map[string]string{
		"Colors:Window": {
			"BackgroundNormal":   "32,35,38",
			"ForegroundNormal":   "252,252,252",
			"ForegroundInactive": "161,169,177",
		},
		"Colors:Header": {
			"BackgroundNormal": "41,44,48",
		},
		"Colors:View": {
			"BackgroundNormal": "20,22,24",
		},
		"Colors:Selection": {
			"BackgroundNormal": "61,174,233",
			"ForegroundNormal": "252,252,252",
		},
		"Colors:Button": {
			"BackgroundNormal": "41,44,48",
		},
	}
	pal := buildKDEPalette(props, true)
	wantAccent := ui.RGB(61, 174, 233)
	if pal.Accent != wantAccent {
		t.Fatalf("KDE accent = %+v, want %+v", pal.Accent, wantAccent)
	}
	if !isDarkColor(pal.Surface) {
		t.Fatalf("dark surface should be dark, got %+v", pal.Surface)
	}
	if pal.Surface == pal.Card || pal.Bg == pal.Card || pal.Header == pal.Bg {
		t.Fatalf("dark palette surface layers must stay distinct: bg=%+v header=%+v card=%+v surface=%+v", pal.Bg, pal.Header, pal.Card, pal.Surface)
	}

	// Forcing light mode on a dark kdeglobals must keep light background polarity
	// while still honoring KDE selection accent and keeping layers distinct.
	lightPal := buildKDEPalette(props, false)
	if isDarkColor(lightPal.Bg) || isDarkColor(lightPal.Surface) {
		t.Fatalf("forced light palette should stay bright: bg=%+v surface=%+v", lightPal.Bg, lightPal.Surface)
	}
	if lightPal.Surface == lightPal.Card || lightPal.Bg == lightPal.Card || lightPal.Header == lightPal.Bg {
		t.Fatalf("light palette surface layers must stay distinct: bg=%+v header=%+v card=%+v surface=%+v", lightPal.Bg, lightPal.Header, lightPal.Card, lightPal.Surface)
	}
	if lightPal.Accent != wantAccent {
		t.Fatalf("forced light palette should preserve KDE accent: %+v", lightPal.Accent)
	}

	// Parsing actual light scheme properties should preserve bright View surface.
	lightProps := map[string]map[string]string{
		"Colors:Window": {
			"BackgroundNormal":   "239,240,241",
			"ForegroundNormal":   "35,38,41",
			"ForegroundInactive": "112,125,138",
		},
		"Colors:Header": {
			"BackgroundNormal": "222,224,226",
		},
		"Colors:View": {
			"BackgroundNormal":    "255,255,255",
			"BackgroundAlternate": "247,247,247",
		},
		"Colors:Selection": {
			"BackgroundNormal": "61,174,233",
			"ForegroundNormal": "255,255,255",
		},
	}
	resolvedLight := resolveKDEPalette(lightProps, false)
	if resolvedLight.Surface != ui.RGB(255, 255, 255) || resolvedLight.Card != ui.RGB(247, 247, 247) || resolvedLight.Header != ui.RGB(222, 224, 226) {
		t.Fatalf("resolved light palette mismatch: %+v", resolvedLight)
	}
	resolvedDarkFallback := resolveKDEPalette(props, false)
	if isDarkColor(resolvedDarkFallback.Bg) || isDarkColor(resolvedDarkFallback.Surface) || resolvedDarkFallback.Accent != wantAccent {
		t.Fatalf("counterpart light palette fallback mismatch: %+v", resolvedDarkFallback)
	}
}

func TestReadSelectedTextTwoTier(t *testing.T) {
	t.Run("UIA hit returns trimmed text without calling fallback", func(t *testing.T) {
		fallbackCalled := false
		got, err := readSelectedTextTwoTier(
			func() (string, error) { return "  selected via UIA \r\n", nil },
			func() (string, error) {
				fallbackCalled = true
				return "fallback", nil
			},
		)
		if err != nil || got != "selected via UIA" {
			t.Fatalf("got=%q, err=%v", got, err)
		}
		if fallbackCalled {
			t.Fatal("fallback must not be called when UIA returns non-empty text")
		}
	})

	t.Run("UIA empty triggers fallback", func(t *testing.T) {
		got, err := readSelectedTextTwoTier(
			func() (string, error) { return "", nil },
			func() (string, error) { return "from-fallback", nil },
		)
		if err != nil || got != "from-fallback" {
			t.Fatalf("got=%q, err=%v", got, err)
		}
	})

	t.Run("UIA whitespace-only triggers fallback", func(t *testing.T) {
		got, err := readSelectedTextTwoTier(
			func() (string, error) { return "  \r\n\t ", nil },
			func() (string, error) { return "from-fallback", nil },
		)
		if err != nil || got != "from-fallback" {
			t.Fatalf("got=%q, err=%v", got, err)
		}
	})

	t.Run("UIA error triggers fallback", func(t *testing.T) {
		got, err := readSelectedTextTwoTier(
			func() (string, error) { return "", errors.New("no text pattern") },
			func() (string, error) { return "from-fallback", nil },
		)
		if err != nil || got != "from-fallback" {
			t.Fatalf("got=%q, err=%v", got, err)
		}
	})

	t.Run("nil UIA reader triggers fallback", func(t *testing.T) {
		got, err := readSelectedTextTwoTier(
			nil,
			func() (string, error) { return "from-fallback", nil },
		)
		if err != nil || got != "from-fallback" {
			t.Fatalf("got=%q, err=%v", got, err)
		}
	})

	t.Run("fallback error propagates", func(t *testing.T) {
		wantErr := errors.New("clipboard snapshot failed")
		_, err := readSelectedTextTwoTier(
			func() (string, error) { return "", errors.New("uia failed") },
			func() (string, error) { return "", wantErr },
		)
		if !errors.Is(err, wantErr) {
			t.Fatalf("err=%v, want %v", err, wantErr)
		}
	})
}

func TestPortalThemeIntegration(t *testing.T) {
	origHook := readPortalAppearanceHook
	origMode := themeMode
	t.Cleanup(func() {
		readPortalAppearanceHook = origHook
		applyThemeMode(origMode)
	})

	t.Run("resolveThemeDark honors portal in system mode and respects explicit overrides", func(t *testing.T) {
		darkPortal := PortalAppearance{ColorScheme: 1, HasColorScheme: true}
		lightPortal := PortalAppearance{ColorScheme: 2, HasColorScheme: true}
		unsetPortal := PortalAppearance{}

		if !resolveThemeDark(false, config.ThemeSystem, darkPortal) {
			t.Fatal("system mode should follow portal color-scheme=1 (dark)")
		}
		if resolveThemeDark(true, config.ThemeSystem, lightPortal) {
			t.Fatal("system mode should follow portal color-scheme=2 (light)")
		}
		if resolveThemeDark(false, config.ThemeSystem, unsetPortal) {
			t.Fatal("unset portal should keep existing frame dark flag (false)")
		}
		if !resolveThemeDark(true, config.ThemeSystem, unsetPortal) {
			t.Fatal("unset portal should keep existing frame dark flag (true)")
		}
		if resolveThemeDark(true, config.ThemeLight, darkPortal) {
			t.Fatal("explicit light mode must override portal dark")
		}
		if !resolveThemeDark(false, config.ThemeDark, lightPortal) {
			t.Fatal("explicit dark mode must override portal light")
		}
	})

	t.Run("fluentTheme applies stubbed portal appearance end-to-end", func(t *testing.T) {
		customAccent := ui.RGB(200, 48, 96)
		readPortalAppearanceHook = func(context.Context) (PortalAppearance, error) {
			return PortalAppearance{
				ColorScheme:    1,
				HasColorScheme: true,
				AccentColor:    customAccent,
				HasAccent:      true,
			}, nil
		}
		applyThemeMode(config.ThemeSystem)

		var applied *ui.Theme
		tt := ui.NewTester(func(c *ui.Context) {
			applied = fluentTheme(c)
		}, 320, 240)
		tt.SetDark(false)
		tt.Frame()
		if applied == nil || !applied.Dark {
			t.Fatalf("expected fluentTheme to resolve dark=true from portal, got %+v", applied)
		}
		if applied.Accent != customAccent {
			t.Fatalf("expected fluentTheme accent %+v, got %+v", customAccent, applied.Accent)
		}
		if applied.AccentText != ui.RGB(255, 255, 255) {
			t.Fatalf("expected high-contrast white AccentText for dark accent, got %+v", applied.AccentText)
		}

		// Explicit light mode overrides portal dark while keeping portal accent.
		applyThemeMode(config.ThemeLight)
		tt.Frame()
		if applied.Dark {
			t.Fatalf("explicit light themeMode must override portal color-scheme=1")
		}
	})

	t.Run("win11PaletteForWithPortal applies accent and readable AccentText", func(t *testing.T) {
		brightAccent := ui.RGB(240, 230, 140)
		pal := win11PaletteForWithPortal(true, PortalAppearance{
			AccentColor: brightAccent,
			HasAccent:   true,
		})
		if pal.Accent != brightAccent {
			t.Fatalf("pal.Accent = %+v, want %+v", pal.Accent, brightAccent)
		}
		if pal.AccentText != ui.RGB(0, 0, 0) {
			t.Fatalf("expected black AccentText on bright accent, got %+v", pal.AccentText)
		}
		if pal.AccentHover != brightAccent.Alpha(0.85) || pal.AccentPressed != brightAccent.Alpha(0.70) {
			t.Fatalf("accent hover/pressed states were not updated: hover=%+v pressed=%+v", pal.AccentHover, pal.AccentPressed)
		}
	})
}
