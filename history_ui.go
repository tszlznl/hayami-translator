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
	winW, _ := c.Size()
	bg, surface, cardBg, border, textPrimary, textSecondary, accent, footerBg := win11Theme(t)

	c.Root().Background(bg)

	padH := float32(20)
	if winW < 540 {
		padH = 12
	}

	ui.Column(c).Fill().Padding(14, padH).Gap(12).Children(func() {
		// Header Bar
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, "历史记录").FontSize(22).Bold().TextColor(textPrimary)
				ui.Text(c, "点击任意记录可在浮窗中查看并复制").FontSize(11).TextColor(textSecondary)
			})
			ui.Spacer(c)
			ui.Badge(c, fmt.Sprintf("%d 条", len(a.hist))).
				Background(accent.Alpha(0.12)).TextColor(accent)
			if ui.Button(c, "刷新").Tooltip("重新加载历史记录").Clicked() {
				a.reloadHistory()
			}
		})

		// Search Bar
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

		// History List
		if len(a.hist) == 0 {
			ui.Column(c).Grow(1).Radius(6).Border(1, border).
				Background(cardBg).Justify(ui.Center).AlignItems(ui.Center).
				Gap(6).Padding(24).Children(func() {
				if a.histQuery != "" {
					ui.Text(c, "没有匹配的记录").Bold().TextColor(textPrimary)
					ui.Text(c, "试试更短的关键词，或清除搜索查看全部。").
						FontSize(12).TextColor(textSecondary)
				} else if a.histStatus != "" {
					ui.Text(c, a.histStatus).Bold().TextColor(statusColor(t, a.histStatus))
				} else {
					ui.Text(c, "还没有历史记录").Bold().TextColor(textPrimary)
					ui.Text(c, "完成一次翻译后会自动保存在这里。").
						FontSize(12).TextColor(textSecondary)
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
				row := ui.Row(c).Gap(10).Padding(10, 12).Radius(6).
					Background(surface).Border(1, border).
					Cursor(ui.CursorPointer)
				if row.Hovered() {
					row.Background(ui.RGBA(0, 0, 0, 0.04))
				}
				row.OnClick(func() { a.useHistoryEntry(e) }).Children(func() {
					ui.Column(c).Grow(1).Shrink(1).Gap(3).Children(func() {
						title := oneLine(e.Source, 120)
						if e.Mode == "image" {
							title = "截图翻译"
						}
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Text(c, title).SingleLine().Bold().TextColor(textPrimary)
							if e.Mode == "image" {
								ui.Badge(c, "截图").Background(accent.Alpha(0.12)).TextColor(accent)
							}
						})
						ui.Text(c, oneLine(e.Result, 180)).FontSize(12).
							TextColor(textSecondary).SingleLine()
					})
					ui.Column(c).Shrink(0).Gap(3).AlignItems(ui.End).Children(func() {
						ui.Text(c, e.Target).FontSize(11).TextColor(accent).SingleLine()
						ui.Text(c, e.CreatedAt.Format("01-02 15:04")).
							FontSize(11).TextColor(textSecondary)
					})
				})
			}).Grow(1)
		}

		// Bottom Action Bar
		ui.Row(c).Height(38).Gap(8).AlignItems(ui.Center).
			Background(footerBg).Radius(4).Padding(0, 8).Children(func() {
			if a.histStatus != "" && len(a.hist) > 0 {
				ui.Text(c, a.histStatus).FontSize(12).
					TextColor(statusColor(t, a.histStatus)).SingleLine().Shrink(1)
			}
			ui.Spacer(c)
			label := "清空历史"
			if a.clearArmed {
				label = "确认清空？"
			}
			clear := ui.Button(c, label).Disabled(len(a.hist) == 0)
			if a.clearArmed {
				clear.Background(t.Danger).TextColor(ui.RGB(255, 255, 255))
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
	reqID := a.histReqID
	if a.store == nil {
		return
	}
	go func() {
		err := a.store.clear()
		if a.historyWin != nil {
			a.historyWin.Update(func() {
				if a.histReqID != reqID {
					return
				}
				if err != nil {
					a.histStatus = "清空失败：" + err.Error()
					return
				}
				a.hist = nil
				a.histStatus = "已清空"
			})
		}
	}()
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
