package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// historyView lists past translations, newest first, and opens one in the
// popup where it can be copied again.
func (a *app) historyView(c *ui.Context) {
	t := c.Theme()
	c.Root().Background(t.Background)

	ui.Column(c).Fill().Padding(20).Gap(14).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "历史记录").FontSize(20).Bold()
				ui.Text(c, "点击任意记录可重新打开译文").FontSize(11).TextColor(t.TextMuted)
			})
			ui.Spacer(c)
			ui.Badge(c, fmt.Sprintf("%d 条", len(a.hist))).
				Background(t.Surface).TextColor(t.TextMuted)
			if ui.Button(c, "刷新").Tooltip("重新加载历史记录").Clicked() {
				a.reloadHistory()
			}
		})

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.TextInput(c, &a.histQuery).
				Placeholder("搜索原文或译文…").Grow(1).Changed() {
				a.clearArmed = false
				a.reloadHistory()
			}
			if ui.Button(c, "清除搜索").Disabled(a.histQuery == "").Clicked() {
				a.histQuery = ""
				a.reloadHistory()
			}
		})

		if len(a.hist) == 0 {
			ui.Column(c).Grow(1).Radius(10).Border(1, t.Border).
				Background(t.Surface).Justify(ui.Center).AlignItems(ui.Center).
				Gap(6).Padding(24).Children(func() {
				if a.histQuery != "" {
					ui.Text(c, "没有匹配的记录").Bold()
					ui.Text(c, "试试更短的关键词，或清除搜索查看全部。").
						FontSize(12).TextColor(t.TextMuted)
				} else if a.histStatus != "" {
					ui.Text(c, a.histStatus).Bold().TextColor(statusColor(t, a.histStatus))
				} else {
					ui.Text(c, "还没有历史记录").Bold()
					ui.Text(c, "完成一次翻译后会自动保存在这里。").
						FontSize(12).TextColor(t.TextMuted)
				}
			})
		} else {
			a.histList.Key = func(i int) any {
				if i >= 0 && i < len(a.hist) {
					return a.hist[i].ID
				}
				return i
			}
			ui.List(c, &a.histList, len(a.hist), func(i int) {
				e := a.hist[i]
				row := ui.Row(c).Gap(12).Padding(12, 9).Radius(8).
					Cursor(ui.CursorPointer)
				if row.Hovered() {
					row.Background(t.SurfaceHover)
				}
				row.OnClick(func() { a.useHistoryEntry(e) }).Children(func() {
					ui.Column(c).Grow(1).Gap(3).Children(func() {
						title := oneLine(e.Source, 120)
						if e.Mode == "image" {
							title = "截图翻译"
						}
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, title).SingleLine()
							if e.Mode == "image" {
								ui.Badge(c, "截图").Background(t.Accent.Alpha(0.12)).TextColor(t.Accent)
							}
						})
						ui.Text(c, oneLine(e.Result, 180)).FontSize(12).
							TextColor(t.TextMuted).SingleLine()
					})
					ui.Column(c).Gap(3).AlignItems(ui.End).Children(func() {
						ui.Text(c, e.Target).FontSize(11).TextColor(t.TextMuted).SingleLine()
						ui.Text(c, e.CreatedAt.Format("01-02 15:04")).
							FontSize(11).TextColor(t.TextMuted)
					})
				})
			}).Grow(1)
		}

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if a.histStatus != "" && len(a.hist) > 0 {
				ui.Text(c, a.histStatus).FontSize(12).
					TextColor(statusColor(t, a.histStatus)).SingleLine()
			}
			ui.Spacer(c)
			label := "清空历史"
			if a.clearArmed {
				label = "确认清空？"
			}
			clear := ui.Button(c, label).Disabled(len(a.hist) == 0)
			if a.clearArmed {
				clear.Background(t.Danger).TextColor(t.AccentText)
			}
			if clear.Tooltip("清空后无法恢复").Clicked() {
				if a.clearArmed {
					a.clearHistory()
				} else {
					a.clearArmed = true
				}
			}
		})
	})
}

func (a *app) clearHistory() {
	a.clearArmed = false
	a.histReqID++
	if a.store == nil {
		return
	}
	if err := a.store.clear(); err != nil {
		a.histStatus = "清空失败：" + err.Error()
		return
	}
	a.hist = nil
	a.histStatus = "已清空"
}

// oneLine collapses whitespace and cuts s to at most n runes, for a row.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
