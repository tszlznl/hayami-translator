package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

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

var (
	kdeStatOnce sync.Once
	kdeGlobals  bool
)

// isKDE detects whether the current environment is running under KDE Plasma / Linux.
func isKDE() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	desktop := strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP"))
	if strings.Contains(desktop, "KDE") || strings.Contains(desktop, "PLASMA") || os.Getenv("KDE_FULL_SESSION") != "" {
		return true
	}
	kdeStatOnce.Do(func() {
		if dir, err := os.UserConfigDir(); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "kdeglobals")); err == nil {
				kdeGlobals = true
			}
		}
	})
	return kdeGlobals
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
	if e1 != nil || e2 != nil || e3 != nil || r < 0 || r > 255 || g < 0 || g > 255 || b < 0 || b > 255 {
		return ui.Color{}, false
	}
	return ui.RGB(uint8(r), uint8(g), uint8(b)), true
}

func isDarkColor(c ui.Color) bool {
	luma := int(c.R)*299 + int(c.G)*587 + int(c.B)*114
	return luma < 128000
}

type kdePaletteCache struct {
	mu        sync.Mutex
	checkedAt time.Time
	modTime   time.Time
	path      string
	dark      win11Palette
	light     win11Palette
	valid     bool
}

var kdeCache kdePaletteCache

// loadKDEPalette reads the user's active KDE Plasma theme from kdeglobals (and
// matching system .colors scheme for the opposite polarity) with mtime + 2s
// caching so per-frame UI builds never hit disk repeatedly.
func loadKDEPalette(dark bool) (win11Palette, bool) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return win11Palette{}, false
	}
	path := filepath.Join(configDir, "kdeglobals")
	now := time.Now()

	kdeCache.mu.Lock()
	defer kdeCache.mu.Unlock()

	if kdeCache.path == path && now.Sub(kdeCache.checkedAt) < 2*time.Second {
		if !kdeCache.valid {
			return win11Palette{}, false
		}
		if dark {
			return kdeCache.dark, true
		}
		return kdeCache.light, true
	}

	kdeCache.path = path
	kdeCache.checkedAt = now

	info, err := os.Stat(path)
	if err != nil {
		kdeCache.valid = false
		kdeCache.modTime = time.Time{}
		return win11Palette{}, false
	}
	if kdeCache.valid && info.ModTime().Equal(kdeCache.modTime) {
		if dark {
			return kdeCache.dark, true
		}
		return kdeCache.light, true
	}

	props, ok := parseINIFile(path)
	if !ok {
		kdeCache.valid = false
		kdeCache.modTime = time.Time{}
		return win11Palette{}, false
	}

	kdeCache.modTime = info.ModTime()
	kdeCache.dark = resolveKDEPalette(props, true)
	kdeCache.light = resolveKDEPalette(props, false)
	kdeCache.valid = true

	if dark {
		return kdeCache.dark, true
	}
	return kdeCache.light, true
}

func resolveKDEPalette(kdeProps map[string]map[string]string, dark bool) win11Palette {
	if winBg, ok := iniColor(kdeProps, "Colors:Window", "BackgroundNormal"); ok && isDarkColor(winBg) == dark {
		return buildKDEPalette(kdeProps, dark)
	}
	if counterProps := findKDECounterpartProps(kdeProps, dark); counterProps != nil {
		pal := buildKDEPalette(counterProps, dark)
		// Preserve the user's active Plasma selection accent across light/dark modes.
		if sel, ok := iniColor(kdeProps, "Colors:Selection", "BackgroundNormal"); ok {
			applyAccent(&pal, sel, dark)
		}
		if selFg, ok := iniColor(kdeProps, "Colors:Selection", "ForegroundNormal"); ok {
			pal.AccentText = selFg
		}
		return pal
	}
	return buildKDEPalette(kdeProps, dark)
}

func findKDECounterpartProps(kdeProps map[string]map[string]string, targetDark bool) map[string]map[string]string {
	var dirs []string
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		dirs = append(dirs, filepath.Join(dataHome, "color-schemes"))
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "color-schemes"))
	}
	dirs = append(dirs, "/usr/share/color-schemes")

	for _, name := range kdeColorSchemeCandidates(kdeProps, targetDark) {
		for _, dir := range dirs {
			props, ok := parseINIFile(filepath.Join(dir, name))
			if !ok {
				continue
			}
			if winBg, ok := iniColor(props, "Colors:Window", "BackgroundNormal"); ok && isDarkColor(winBg) == targetDark {
				return props
			}
		}
	}
	return nil
}

func kdeColorSchemeCandidates(kdeProps map[string]map[string]string, targetDark bool) []string {
	var candidates []string
	add := func(s string) {
		if s == "" {
			return
		}
		if !strings.HasSuffix(s, ".colors") {
			s += ".colors"
		}
		for _, existing := range candidates {
			if existing == s {
				return
			}
		}
		candidates = append(candidates, s)
	}

	scheme := ""
	if g := kdeProps["General"]; g != nil {
		scheme = strings.TrimSpace(g["ColorScheme"])
	}
	if scheme == "" {
		if k := kdeProps["KDE"]; k != nil {
			lnf := strings.ToLower(k["LookAndFeelPackage"])
			if strings.Contains(lnf, "breeze") {
				scheme = "Breeze"
			}
		}
	}

	if scheme != "" {
		stem := scheme
		for _, suffix := range []string{"Dark", "Lightly", "Light"} {
			if len(stem) > len(suffix) && strings.EqualFold(stem[len(stem)-len(suffix):], suffix) {
				stem = stem[:len(stem)-len(suffix)]
				break
			}
		}
		if targetDark {
			add(stem + "Dark")
			add(stem)
		} else {
			add(stem + "Light")
			add(stem + "Lightly")
			add(stem)
		}
	}

	if targetDark {
		add("BreezeDark")
	} else {
		add("BreezeLight")
	}
	return candidates
}

func iniColor(props map[string]map[string]string, sec, key string) (ui.Color, bool) {
	if m := props[sec]; m != nil {
		if v, ok := m[key]; ok {
			return parseRGBColor(v)
		}
	}
	return ui.Color{}, false
}

func parseINIFile(path string) (map[string]map[string]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	curSec := ""
	props := make(map[string]map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
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
	return props, scanner.Err() == nil
}

func applyAccent(pal *win11Palette, accent ui.Color, dark bool) {
	pal.Accent = accent
	pal.AccentHover = accent.Alpha(0.85)
	pal.AccentPressed = accent.Alpha(0.70)
	if dark {
		pal.SurfaceHover = accent.Alpha(0.14)
		pal.SurfacePressed = accent.Alpha(0.24)
	} else {
		pal.SurfaceHover = accent.Alpha(0.12)
		pal.SurfacePressed = accent.Alpha(0.22)
	}
}

func buildKDEPalette(props map[string]map[string]string, dark bool) win11Palette {
	base := breezeLightPalette()
	if dark {
		base = breezeDarkPalette()
	}

	// Only apply surface/text colors when props' polarity matches the
	// requested theme mode, so explicitly forcing light/dark stays readable.
	polarityMatches := true
	if winBg, ok := iniColor(props, "Colors:Window", "BackgroundNormal"); ok {
		polarityMatches = isDarkColor(winBg) == dark
		if polarityMatches {
			base.Bg = winBg
		}
	}

	if polarityMatches {
		if c, ok := iniColor(props, "Colors:Window", "ForegroundNormal"); ok {
			base.TextPrimary = c
		}
		if c, ok := iniColor(props, "Colors:Window", "ForegroundInactive"); ok {
			base.TextSecondary = c
		}
		if c, ok := iniColor(props, "Colors:Header", "BackgroundNormal"); ok {
			base.Header = c
			base.Footer = c
		} else if c, ok := iniColor(props, "Colors:Window", "BackgroundAlternate"); ok {
			base.Header = c
			base.Footer = c
		}
		if c, ok := iniColor(props, "Colors:View", "BackgroundNormal"); ok {
			base.Surface = c
		}
		if dark {
			if btn, ok := iniColor(props, "Colors:Button", "BackgroundNormal"); ok {
				card := base.Bg.Mix(btn, 0.55)
				if card == base.Bg || card == base.Surface || card == base.Header {
					card = base.Bg.Mix(base.TextPrimary, 0.04)
				}
				base.Card = card
			} else {
				base.Card = base.Bg.Mix(base.TextPrimary, 0.04)
			}
		} else {
			if va, ok := iniColor(props, "Colors:View", "BackgroundAlternate"); ok && va != base.Surface && va != base.Bg {
				base.Card = va
			} else {
				base.Card = base.Bg.Mix(base.Surface, 0.5)
			}
		}
		if base.Card == base.Surface {
			if dark {
				base.Card = base.Bg.Mix(base.TextPrimary, 0.04)
			} else {
				base.Card = base.Bg.Mix(base.Surface, 0.5)
			}
		}
		base.Border = base.Bg.Mix(base.TextPrimary, 0.16)
		if c, ok := iniColor(props, "Colors:Button", "ForegroundPositive"); ok {
			base.Success = c
		}
		if c, ok := iniColor(props, "Colors:Button", "ForegroundNegative"); ok {
			base.Danger = c
		}
		if c, ok := iniColor(props, "Colors:Button", "ForegroundNeutral"); ok {
			base.Warning = c
		}
	}

	if c, ok := iniColor(props, "Colors:Selection", "BackgroundNormal"); ok {
		applyAccent(&base, c, dark)
	}
	if polarityMatches {
		if c, ok := iniColor(props, "Colors:Selection", "ForegroundNormal"); ok {
			base.AccentText = c
		}
	}

	return base
}

func breezeDarkPalette() win11Palette {
	accent := ui.Hex("#3daee9") // KDE Breeze Blue
	return win11Palette{
		Bg:             ui.Hex("#202326"), // KDE Breeze Dark Window (32,35,38)
		Header:         ui.Hex("#292c30"), // KDE Breeze Dark Header (41,44,48)
		Surface:        ui.Hex("#141618"), // KDE Breeze Dark View / Control (20,22,24)
		SurfaceHover:   ui.RGBA(61, 174, 233, 0.14),
		SurfacePressed: ui.RGBA(61, 174, 233, 0.24),
		Card:           ui.Hex("#25282c"), // Elevated card between Window & Header
		Border:         ui.Hex("#434648"), // Subtle 1px Breeze outline
		TextPrimary:    ui.Hex("#fcfcfc"),
		TextSecondary:  ui.Hex("#a1a9b1"),
		Accent:         accent,
		AccentHover:    ui.Hex("#5bc0f8"),
		AccentPressed:  ui.Hex("#289bd6"),
		AccentText:     ui.RGB(252, 252, 252),
		Danger:         ui.Hex("#da4453"), // Breeze Red
		Warning:        ui.Hex("#f67400"), // Breeze Orange
		Success:        ui.Hex("#27ae60"), // Breeze Green
		Footer:         ui.Hex("#292c30"),
		Scrollbar:      ui.RGBA(255, 255, 255, 0.22),
	}
}

func breezeLightPalette() win11Palette {
	accent := ui.Hex("#3daee9") // Unified KDE Breeze accent
	return win11Palette{
		Bg:             ui.Hex("#eff0f1"), // KDE Breeze Light Window (239,240,241)
		Header:         ui.Hex("#dee0e2"), // KDE Breeze Light Header (222,224,226)
		Surface:        ui.Hex("#ffffff"), // KDE Breeze Light View / Control (255,255,255)
		SurfaceHover:   ui.RGBA(61, 174, 233, 0.12),
		SurfacePressed: ui.RGBA(61, 174, 233, 0.22),
		Card:           ui.Hex("#f7f7f7"), // KDE Breeze Light Card (247,247,247)
		Border:         ui.Hex("#ced0d1"),
		TextPrimary:    ui.Hex("#232629"),
		TextSecondary:  ui.Hex("#707d8a"),
		Accent:         accent,
		AccentHover:    ui.Hex("#5bc0f8"),
		AccentPressed:  ui.Hex("#289bd6"),
		AccentText:     ui.RGB(255, 255, 255),
		Danger:         ui.Hex("#da4453"),
		Warning:        ui.Hex("#f67400"),
		Success:        ui.Hex("#27ae60"),
		Footer:         ui.Hex("#dee0e2"),
		Scrollbar:      ui.RGBA(0, 0, 0, 0.20),
	}
}

// win11Palette is the app's color set for one theme mode. The look follows
// system desktop conventions (KDE Breeze on Plasma, Fluent on Windows).
type win11Palette struct {
	Bg             ui.Color
	Header         ui.Color
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

// PortalAppearance captures color-scheme and accent-color preferences reported
// by org.freedesktop.portal.Settings.
type PortalAppearance struct {
	ColorScheme    uint32
	HasColorScheme bool
	AccentColor    ui.Color
	HasAccent      bool
}

// readPortalAppearanceHook is registered on Linux by portal_linux.go's init().
var readPortalAppearanceHook func(context.Context) (PortalAppearance, error)

// portalAppearanceChangedHook is invoked on Linux when org.freedesktop.portal.Settings
// emits a SettingChanged signal so the running GUI window can repaint immediately.
var portalAppearanceChangedHook func()

func currentPortalAppearance() PortalAppearance {
	if readPortalAppearanceHook == nil {
		return PortalAppearance{}
	}
	appearance, err := readPortalAppearanceHook(context.Background())
	if err != nil {
		return PortalAppearance{}
	}
	return appearance
}

func resolveThemeDark(themeDark bool, mode string, portal PortalAppearance) bool {
	switch mode {
	case config.ThemeLight:
		return false
	case config.ThemeDark:
		return true
	default:
		if portal.HasColorScheme {
			switch portal.ColorScheme {
			case 1:
				return true
			case 2:
				return false
			}
		}
		return themeDark
	}
}

func accentTextColor(accent ui.Color) ui.Color {
	if isDarkColor(accent) {
		return ui.RGB(255, 255, 255)
	}
	return ui.RGB(0, 0, 0)
}

// win11Theme returns the app's palette for the frame's current light or
// dark mode without clobbering KDE Breeze accent and semantic colors.
func win11Theme(t *ui.Theme) win11Palette {
	return win11PaletteFor(t.Dark)
}

func win11PaletteFor(dark bool) win11Palette {
	return win11PaletteForWithPortal(dark, currentPortalAppearance())
}

func win11PaletteForWithPortal(dark bool, portal PortalAppearance) win11Palette {
	var pal win11Palette
	switch {
	case isKDE():
		if kdePal, ok := loadKDEPalette(dark); ok {
			pal = kdePal
		} else if dark {
			pal = breezeDarkPalette()
		} else {
			pal = breezeLightPalette()
		}
	case dark:
		pal = win11Palette{
			Bg:             ui.Hex("#1e1e20"),
			Header:         ui.Hex("#252528"),
			Surface:        ui.Hex("#2d2d30"),
			SurfaceHover:   ui.RGBA(255, 255, 255, 0.06),
			SurfacePressed: ui.RGBA(255, 255, 255, 0.09),
			Card:           ui.RGBA(255, 255, 255, 0.045),
			Border:         ui.RGBA(255, 255, 255, 0.08),
			TextPrimary:    ui.RGBA(255, 255, 255, 0.92),
			TextSecondary:  ui.RGBA(255, 255, 255, 0.55),
			Accent:         ui.Hex("#4cc2ff"),
			AccentHover:    ui.Hex("#60cdff"),
			AccentPressed:  ui.Hex("#38b0f0"),
			AccentText:     ui.RGB(0, 0, 0),
			Danger:         ui.Hex("#ff99a4"),
			Warning:        ui.Hex("#fce100"),
			Success:        ui.Hex("#6ccb5f"),
			Footer:         ui.Hex("#1e1e20"),
			Scrollbar:      ui.RGBA(255, 255, 255, 0.20),
		}
	default:
		pal = win11Palette{
			Bg:             ui.Hex("#f3f3f3"),
			Header:         ui.Hex("#ebebed"),
			Surface:        ui.Hex("#fbfbfb"),
			SurfaceHover:   ui.RGBA(0, 0, 0, 0.04),
			SurfacePressed: ui.RGBA(0, 0, 0, 0.07),
			Card:           ui.RGB(255, 255, 255),
			Border:         ui.RGBA(0, 0, 0, 0.08),
			TextPrimary:    ui.RGBA(0, 0, 0, 0.86),
			TextSecondary:  ui.RGBA(0, 0, 0, 0.50),
			Accent:         ui.Hex("#0067c0"),
			AccentHover:    ui.Hex("#1975c5"),
			AccentPressed:  ui.Hex("#005ba8"),
			AccentText:     ui.RGB(255, 255, 255),
			Danger:         ui.Hex("#c42b1c"),
			Warning:        ui.Hex("#9d5d00"),
			Success:        ui.Hex("#0f7b0f"),
			Footer:         ui.Hex("#f3f3f3"),
			Scrollbar:      ui.RGBA(0, 0, 0, 0.18),
		}
	}
	if portal.HasAccent && pal.Accent != portal.AccentColor {
		applyAccent(&pal, portal.AccentColor, dark)
		pal.AccentText = accentTextColor(portal.AccentColor)
	}
	return pal
}

// panelRadius returns the outer window/panel corner radius for the active desktop.
func panelRadius() float32 {
	if isKDE() {
		return 6
	}
	return 12
}

// panelBackground fills a floating window. Over a vibrancy material the
// surface is a light tint, so the blurred desktop reads through; without
// one, the plain surface keeps text legible.
func panelBackground(c *ui.Context, t *ui.Theme) {
	pal := win11Theme(t)
	rad := panelRadius()
	c.Root().Radius(rad).Border(1, pal.Border)
	if c.Vibrancy() {
		tint := float32(0.86)
		if t.Dark {
			tint = 0.92
		}
		c.Root().Background(pal.Bg.Alpha(tint))
		return
	}
	c.Root().Background(pal.Bg)
}

// fluentTheme installs the app's palette, type scale and control shape on
// the frame. Setting the full palette, not only Dark, keeps MyGo's own
// widgets (buttons, inputs, switches) coherent with the hand-drawn surfaces.
func fluentTheme(c *ui.Context) *ui.Theme {
	t := *c.Theme()
	portal := currentPortalAppearance()
	t.Dark = resolveThemeDark(t.Dark, themeMode, portal)
	pal := win11PaletteForWithPortal(t.Dark, portal)
	t.Background = pal.Bg
	t.Surface = pal.Surface
	t.SurfaceHover = pal.SurfaceHover
	t.SurfacePressed = pal.SurfacePressed
	t.Border = pal.Border
	t.Text = pal.TextPrimary
	t.TextMuted = pal.TextSecondary
	t.Accent = pal.Accent
	t.AccentHover = pal.AccentHover
	t.AccentPressed = pal.AccentPressed
	t.AccentText = pal.AccentText
	t.InactiveAccent = pal.Accent
	t.Danger = pal.Danger
	t.Warning = pal.Warning
	t.Success = pal.Success
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
		t.Radius = 4
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
	pal := win11Theme(c.Theme())
	rad := float32(6)
	w, h := float32(32), float32(32)
	if isKDE() {
		rad = 4
		w, h = 28, 28
	}
	btn := ui.ButtonBase(c).Width(w).Height(h).Radius(rad).
		Justify(ui.Center).AlignItems(ui.Center).
		Tooltip(tooltip)
	if btn.Pressed() {
		btn.Background(pal.SurfacePressed)
	} else if btn.Hovered() {
		btn.Background(pal.SurfaceHover)
	}
	btn.Children(func() {
		ui.Icon(c, icon).TextColor(pal.TextPrimary)
	})
	return btn
}

// segmentedButton renders a Breeze-style segmented option button.
func segmentedButton(c *ui.Context, key, label string, active bool, onClick func()) {
	pal := win11Theme(c.Theme())
	rad := float32(6)
	if isKDE() {
		rad = 4
	}
	btn := ui.ButtonBase(c.Key(key)).Height(28).Padding(0, 12).Radius(rad).
		AlignItems(ui.Center).Justify(ui.Center).OnClick(onClick)
	if active {
		if isKDE() {
			btn.Background(pal.Accent.Alpha(0.22)).Border(1, pal.Accent)
			btn.Children(func() {
				ui.Text(c, label).FontSize(12).Bold().TextColor(pal.Accent).SingleLine()
			})
		} else {
			btn.Background(pal.Accent).Border(1, pal.Accent)
			btn.Children(func() {
				ui.Text(c, label).FontSize(12).Bold().TextColor(pal.AccentText).SingleLine()
			})
		}
	} else {
		btn.Background(pal.Surface).Border(1, pal.Border)
		if btn.Hovered() {
			btn.Background(pal.SurfaceHover)
		}
		btn.Children(func() {
			ui.Text(c, label).FontSize(12).TextColor(pal.TextPrimary).SingleLine()
		})
	}
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
		strings.Contains(s, "已移除"), strings.Contains(s, "已放弃"):
		return t.Success
	default:
		return t.TextMuted
	}
}

// readSelectedTextTwoTier tries a non-destructive UI Automation read first and
// only delegates to readFallback when UIA is nil, errors, or returns empty
// whitespace-only text.
func readSelectedTextTwoTier(readUIA func() (string, error), readFallback func() (string, error)) (string, error) {
	var uiaErr error
	if readUIA != nil {
		text, err := readUIA()
		if err == nil {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				return trimmed, nil
			}
		} else {
			uiaErr = err
		}
	}
	if readFallback == nil {
		return "", uiaErr
	}
	return readFallback()
}
