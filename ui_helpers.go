package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// win11Palette is the Windows 11 Fluent color set for one theme mode.
type win11Palette struct {
	Bg            ui.Color
	Surface       ui.Color
	Card          ui.Color
	Border        ui.Color
	TextPrimary   ui.Color
	TextSecondary ui.Color
	Accent        ui.Color
	Footer        ui.Color
}

// win11Theme returns the app's Windows 11 Fluent palette for the frame's
// current light or dark mode.
func win11Theme(t *ui.Theme) win11Palette {
	if t.Dark {
		return win11Palette{
			Bg:            ui.RGB(32, 32, 32),
			Surface:       ui.RGB(44, 44, 44),
			Card:          ui.RGB(40, 40, 40),
			Border:        ui.RGB(69, 69, 69),
			TextPrimary:   ui.RGB(255, 255, 255),
			TextSecondary: ui.RGB(197, 197, 197),
			Accent:        ui.RGB(96, 205, 255),
			Footer:        ui.RGB(39, 39, 39),
		}
	}
	return win11Palette{
		Bg:            ui.RGB(243, 243, 243),
		Surface:       ui.RGB(252, 252, 252),
		Card:          ui.RGB(251, 251, 251),
		Border:        ui.RGB(224, 224, 224),
		TextPrimary:   ui.RGB(26, 26, 26),
		TextSecondary: ui.RGB(93, 93, 93),
		Accent:        ui.RGB(0, 95, 184),
		Footer:        ui.RGB(246, 246, 246),
	}
}

// fluentTheme installs the Windows 11 font family on the frame and returns
// the frame's theme.
func fluentTheme(c *ui.Context) *ui.Theme {
	t := *c.Theme()
	t.Font = "Segoe UI Variable, Segoe UI, system-ui"
	c.SetTheme(&t)
	return &t
}

// win11IconButton renders a Windows 11 style 32x32 icon button
func win11IconButton(c *ui.Context, icon *ui.SVG, tooltip string) ui.Element {
	btn := ui.ButtonBase(c).Width(32).Height(32).Radius(4).
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
	ui.Column(c).Gap(10).Padding(14).Radius(10).
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
