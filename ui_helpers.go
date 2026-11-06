package main

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"hayami/internal/config"
)

// themeMode is applied by every view on the main thread. It overrides the
// system light/dark mode when the user picks a fixed theme.
var themeMode = config.ThemeSystem

func applyThemeMode(mode string) {
	switch mode {
	case config.ThemeLight, config.ThemeDark, config.ThemeSystem:
		themeMode = mode
	default:
		themeMode = config.ThemeSystem
	}
}

// isKDE detects whether the current environment is running under KDE Plasma / Linux.
func isKDE() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	desktop := strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP"))
	if strings.Contains(desktop, "KDE") || strings.Contains(desktop, "PLASMA") {
		return true
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if _, err := os.Stat(filepath.Join(home, ".config", "kdeglobals")); err == nil {
			return true
		}
	}
	return false
}

// parseRGBColor parses an "R,G,B" triple from KDE config files.
func parseRGBColor(s string) (ui.Color, bool) {
	parts := strings.Split(s, ",")
	if len(parts) < 3 {
		return ui.Color{}, false
	}
	r, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	g, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	b, e3 := strconv.Atoi(strings.TrimSpace(parts[2]))
	if e1 != nil || e2 != nil || e3 != nil {
		return ui.Color{}, false
	}
	return ui.RGB(uint8(r), uint8(g), uint8(b)), true
}

// loadKDEPalette reads the user's active KDE Plasma theme from ~/.config/kdeglobals.
func loadKDEPalette(dark bool) (win11Palette, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return win11Palette{}, false
	}
	f, err := os.Open(filepath.Join(home, ".config", "kdeglobals"))
	if err != nil {
		return win11Palette{}, false
	}
	defer f.Close()

	curSec := ""
	props := make(map[string]map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			curSec = line[1 : len(line)-1]
			if props[curSec] == nil {
				props[curSec] = make(map[string]string)
			}
			continue
		}
		if idx := strings.IndexByte(line, '='); idx > 0 && curSec != "" {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			props[curSec][k] = v
		}
	}

	get := func(sec, key string) (ui.Color, bool) {
		if m := props[sec]; m != nil {
			if v, ok := m[key]; ok {
				return parseRGBColor(v)
			}
		}
		return ui.Color{}, false
	}

	base := breezeDarkPalette()
	if !dark {
		base = breezeLightPalette()
	}

	if c, ok := get("Colors:Window", "BackgroundNormal"); ok {
		base.Bg = c
		base.Surface = c
		base.Footer = c
	}
	if c, ok := get("Colors:View", "BackgroundNormal"); ok {
		base.Card = c
	}
	if c, ok := get("Colors:Window", "ForegroundNormal"); ok {
		base.TextPrimary = c
	}
	if c, ok := get("Colors:Window", "ForegroundInactive"); ok {
		base.TextSecondary = c
	}
	if c, ok := get("Colors:Selection", "BackgroundNormal"); ok {
		base.Accent = c
		base.AccentHover = c.Alpha(0.85)
		base.AccentPressed = c.Alpha(0.70)
	}
	if c, ok := get("Colors:Button", "ForegroundPositive"); ok {
		base.Success = c
	}
	if c, ok := get("Colors:Button", "ForegroundNegative"); ok {
		base.Danger = c
	}
	if c, ok := get("Colors:Button", "ForegroundNeutral"); ok {
		base.Warning = c
	}

	return base, true
}

func breezeDarkPalette() win11Palette {
	accent := ui.Hex("#3daee9") // KDE Breeze Blue
	return win11Palette{
		Bg:             ui.Hex("#202326"), // KDE Breeze Dark Window
		Surface:        ui.Hex("#202326"),
		SurfaceHover:   ui.RGBA(61, 174, 233, 0.14),
		SurfacePressed: ui.RGBA(61, 174, 233, 0.24),
		Card:           ui.Hex("#141618"), // KDE Breeze Dark View
		Border:         ui.Hex("#3e444a"), // Subtle 1px outline
		TextPrimary:    ui.Hex("#fcfcfc"),
		TextSecondary:  ui.Hex("#a1a9b1"),
		Accent:         accent,
		AccentHover:    ui.Hex("#5bc0f8"),
		AccentPressed:  ui.Hex("#289bd6"),
		AccentText:     ui.RGB(255, 255, 255),
		Danger:         ui.Hex("#da4453"), // Breeze Red
		Warning:        ui.Hex("#f67400"), // Breeze Orange
		Success:        ui.Hex("#27ae60"), // Breeze Green
		Footer:         ui.Hex("#202326"),
		Scrollbar:      ui.RGBA(255, 255, 255, 0.22),
	}
}

func breezeLightPalette() win11Palette {
	accent := ui.Hex("#1d99f3") // Breeze Light Blue
	return win11Palette{
		Bg:             ui.Hex("#eff0f1"), // KDE Breeze Light Window
		Surface:        ui.Hex("#eff0f1"),
		SurfaceHover:   ui.RGBA(29, 153, 243, 0.10),
		SurfacePressed: ui.RGBA(29, 153, 243, 0.20),
		Card:           ui.Hex("#ffffff"), // KDE Breeze Light View
		Border:         ui.Hex("#bcc0c4"),
		TextPrimary:    ui.Hex("#232629"),
		TextSecondary:  ui.Hex("#656d76"),
		Accent:         accent,
		AccentHover:    ui.Hex("#3daee9"),
		AccentPressed:  ui.Hex("#1780cb"),
		AccentText:     ui.RGB(255, 255, 255),
		Danger:         ui.Hex("#da4453"),
		Warning:        ui.Hex("#f67400"),
		Success:        ui.Hex("#27ae60"),
		Footer:         ui.Hex("#eff0f1"),
		Scrollbar:      ui.RGBA(0, 0, 0, 0.20),
	}
}

// win11Palette is the app's color set for one theme mode. The look follows
// system desktop conventions (KDE Breeze on Plasma, Fluent on Windows).
type win11Palette struct {
	Bg             ui.Color
	Surface        ui.Color
	SurfaceHover   ui.Color
	SurfacePressed ui.Color
	Card           ui.Color
	Border         ui.Color
	TextPrimary    ui.Color
	TextSecondary  ui.Color
	Accent         ui.Color
	AccentHover    ui.Color
	AccentPressed  ui.Color
	AccentText     ui.Color
	Danger         ui.Color
	Warning        ui.Color
	Success        ui.Color
	Footer         ui.Color
	Scrollbar      ui.Color
}

// win11Theme returns the app's palette for the frame's current light or
// dark mode.
func win11Theme(t *ui.Theme) win11Palette {
	p := win11PaletteFor(t.Dark)
	p.Accent = t.Accent
	p.AccentHover = t.AccentHover
	p.AccentPressed = t.AccentPressed
	p.AccentText = t.AccentText
	p.Danger = t.Danger
	p.Warning = t.Warning
	p.Success = t.Success
	return p
}

func win11PaletteFor(dark bool) win11Palette {
	if isKDE() {
		if pal, ok := loadKDEPalette(dark); ok {
			return pal
		}
		if dark {
			return breezeDarkPalette()
		}
		return breezeLightPalette()
	}

	if dark {
		return win11Palette{
			Bg:             ui.Hex("#1e1e20"),
			Surface:        ui.Hex("#1e1e20"),
			SurfaceHover:   ui.RGBA(255, 255, 255, 0.06),
			SurfacePressed: ui.RGBA(255, 255, 255, 0.09),
			Card:           ui.RGBA(255, 255, 255, 0.035),
			Border:         ui.RGBA(255, 255, 255, 0.08),
			TextPrimary:    ui.RGBA(255, 255, 255, 0.92),
			TextSecondary:  ui.RGBA(255, 255, 255, 0.55),
			Footer:         ui.Hex("#1e1e20"),
			Scrollbar:      ui.RGBA(255, 255, 255, 0.20),
		}
	}
	return win11Palette{
		Bg:             ui.RGB(255, 255, 255),
		Surface:        ui.RGB(255, 255, 255),
		SurfaceHover:   ui.RGBA(0, 0, 0, 0.04),
		SurfacePressed: ui.RGBA(0, 0, 0, 0.07),
		Card:           ui.RGBA(0, 0, 0, 0.018),
		Border:         ui.RGBA(0, 0, 0, 0.07),
		TextPrimary:    ui.RGBA(0, 0, 0, 0.86),
		TextSecondary:  ui.RGBA(0, 0, 0, 0.50),
		Footer:         ui.RGB(255, 255, 255),
		Scrollbar:      ui.RGBA(0, 0, 0, 0.18),
	}
}

// panelBackground fills a floating window. Over a vibrancy material the
// surface is a light tint, so the blurred desktop reads through; without
// one, the plain surface keeps text legible.
func panelBackground(c *ui.Context, t *ui.Theme) {
	pal := win11Theme(t)
	rad := float32(12)
	if isKDE() {
		rad = 8
	}
	c.Root().Radius(rad).Border(1, pal.Border)
	if c.Vibrancy() {
		tint := float32(0.85)
		if t.Dark {
			tint = 0.90
		}
		c.Root().Background(pal.Surface.Alpha(tint))
		return
	}
	c.Root().Background(pal.Surface)
}

// fluentTheme installs the app's palette, type scale and control shape on
// the frame. Setting the full palette, not only Dark, keeps MyGo's own
// widgets (buttons, inputs, switches) coherent with the hand-drawn surfaces.
func fluentTheme(c *ui.Context) *ui.Theme {
	t := *c.Theme()
	switch themeMode {
	case config.ThemeLight:
		t.Dark = false
	case config.ThemeDark:
		t.Dark = true
	}
	pal := win11Theme(&t)
	t.Background = pal.Bg
	t.Surface = pal.Surface
	t.SurfaceHover = pal.SurfaceHover
	t.SurfacePressed = pal.SurfacePressed
	t.Border = pal.Border
	t.Text = pal.TextPrimary
	t.TextMuted = pal.TextSecondary
	t.Selection = pal.Accent.Alpha(0.28)
	t.Focus = pal.Accent
	t.Scrollbar = pal.Scrollbar
	if t.Dark {
		t.Inverse = ui.Hex("#31363b")
	} else {
		t.Inverse = ui.Hex("#232629")
	}
	t.InverseText = ui.RGBA(255, 255, 255, 0.95)
	t.FontSize = 13
	t.Spacing = 4
	if isKDE() {
		t.Radius = 5
		t.Font = "Noto Sans, Noto Sans CJK SC, Noto Sans SC, Cantarell, system-ui, sans-serif"
	} else {
		t.Radius = 7
		t.Font = "Segoe UI Variable, Segoe UI, system-ui, sans-serif"
	}
	c.SetTheme(&t)
	return &t
}

// win11IconButton renders a compact tool button with a quiet hover fill.
func win11IconButton(c *ui.Context, icon *ui.SVG, tooltip string) ui.Element {
	rad := float32(6)
	w, h := float32(32), float32(32)
	if isKDE() {
		rad = 5
		w, h = 28, 28
	}
	btn := ui.ButtonBase(c).Width(w).Height(h).Radius(rad).
		Justify(ui.Center).AlignItems(ui.Center).
		Tooltip(tooltip)
	btn.Children(func() {
		ui.Icon(c, icon)
	})
	return btn
}

// section groups a titled block of settings. The title is outside the card so
// several cards can belong to one section.
func section(c *ui.Context, title, desc string, build func()) {
	t := c.Theme()
	ui.Column(c).Gap(10).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(15).Bold()
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(t.TextMuted)
			}
		})
		build()
	})
}

// card gives related fields one surface, which keeps long settings pages
// readable without adding a second layer of borders.
func card(c *ui.Context, build func()) {
	t := c.Theme()
	rad := float32(10)
	if isKDE() {
		rad = 6
	}
	ui.Column(c).Gap(10).Padding(14).Radius(rad).
		Background(t.Surface).Border(1, t.Border).Children(build)
}

// statusColor picks a readable color for short operation results.
func statusColor(t *ui.Theme, s string) ui.Color {
	switch {
	case strings.Contains(s, "失败"), strings.Contains(s, "错误"),
		strings.Contains(s, "不可用"):
		return t.Danger
	case strings.Contains(s, "没有可用"), strings.Contains(s, "未配置"),
		strings.Contains(s, "再点一次"), strings.Contains(s, "未保存"):
		return t.Warning
	case strings.Contains(s, "已保存"), strings.Contains(s, "已复制"),
		strings.Contains(s, "已更新"), strings.Contains(s, "已清空"),
		strings.Contains(s, "已添加"), strings.Contains(s, "已调整"),
		strings.Contains(s, "已移除"):
		return t.Success
	default:
		return t.TextMuted
	}
}
