package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// historyView lists past translations, newest first, and opens one in the
// popup where it can be copied again.
func (a *app) historyView(c *ui.Context) {
	t := fluentTheme(c)
	winW, _ := c.Size()
	pal := win11Theme(t)

	c.Root().Background(pal.Bg)
	titleH := float32(44)

	padH := float32(24)
	if winW < 540 {
		padH = 12
	}

	ui.Column(c).Fill().Children(func() {
		// Header: the window controls float over its right side.
		ui.Row(c).Height(titleH).Padding(0, padH, 0, padH).
			AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "历史记录").FontSize(15).Bold().TextColor(pal.TextPrimary)
			ui.Badge(c, fmt.Sprintf("%d 条", len(a.hist))).
				Background(pal.Accent.Alpha(0.12)).TextColor(pal.Accent)
			ui.Spacer(c)
			if ui.Button(c, "刷新").Tooltip("重新加载历史记录").Clicked() {
				a.reloadHistory()
			}
		})

		ui.Column(c).Grow(1).Padding(0, padH, 14, padH).Gap(10).Children(func() {
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
				ui.Column(c).Grow(1).Radius(10).Border(1, pal.Border).
					Background(pal.Card).Justify(ui.Center).AlignItems(ui.Center).
					Gap(6).Padding(24).Children(func() {
					if a.histQuery != "" {
						ui.Text(c, "没有匹配的记录").Bold().TextColor(pal.TextPrimary)
						ui.Text(c, "试试更短的关键词，或清除搜索查看全部。").
							FontSize(12).TextColor(pal.TextSecondary)
					} else if a.histStatus != "" {
						ui.Text(c, a.histStatus).Bold().TextColor(statusColor(t, a.histStatus))
					} else {
						ui.Text(c, "还没有历史记录").Bold().TextColor(pal.TextPrimary)
						ui.Text(c, "完成一次翻译后会自动保存在这里。").
							FontSize(12).TextColor(pal.TextSecondary)
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
					row := ui.Row(c).Gap(10).Padding(10, 12).Radius(8).
						Background(pal.Card).Border(1, pal.Border).
						Cursor(ui.CursorPointer)
					if row.Hovered() {
						row.Background(pal.SurfaceHover)
					}
					row.OnClick(func() { a.useHistoryEntry(e) }).Children(func() {
						ui.Column(c).Grow(1).Shrink(1).Gap(3).Children(func() {
							title := oneLine(e.Source, 120)
							if e.Mode == "image" {
								title = "截图翻译"
							}
							ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
								ui.Text(c, title).SingleLine().Bold().TextColor(pal.TextPrimary)
								if e.Mode == "image" {
									ui.Badge(c, "截图").Background(pal.Accent.Alpha(0.12)).TextColor(pal.Accent)
								}
							})
							ui.Text(c, oneLine(e.Result, 180)).FontSize(12).
								TextColor(pal.TextSecondary).SingleLine()
						})
						ui.Column(c).Shrink(0).Gap(3).AlignItems(ui.End).Children(func() {
							ui.Text(c, e.Target).FontSize(11).TextColor(pal.Accent).SingleLine()
							ui.Text(c, e.CreatedAt.Format("01-02 15:04")).
								FontSize(11).TextColor(pal.TextSecondary)
						})
					})
				}).Grow(1)
			}

			// Bottom Action Bar
			ui.Row(c).Height(38).Gap(8).AlignItems(ui.Center).
				Background(pal.Footer).Radius(4).Padding(0, 8).Children(func() {
				if a.histStatus != "" && len(a.hist) > 0 {
					ui.Text(c, a.histStatus).FontSize(12).
						TextColor(statusColor(t, a.histStatus)).SingleLine().Shrink(1)
				}
				ui.Spacer(c)
				clear := ui.Button(c, "清空历史").Disabled(len(a.hist) == 0)
				if clear.Tooltip("清空后无法恢复").Clicked() {
					a.clearArmed = true
				}
			})
		})
	})

	if ui.AlertDialog(c, &a.clearArmed, "清空全部历史？",
		fmt.Sprintf("将删除全部 %d 条翻译记录，且无法恢复。", len(a.hist)),
		"取消", "清空") == 1 {
		a.clearHistory()
	}
}

func (a *app) clearHistory() {
	a.clearArmed = false
	a.histReqID++
	reqID := a.histReqID
	if a.store == nil {
		return
	}
	go func() {
		err := a.store.Clear()
		if a.popup != nil {
			a.popup.Update(func() {
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
