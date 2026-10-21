package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

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
