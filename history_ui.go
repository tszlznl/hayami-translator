package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"hayami/internal/history"
)

// historyView lists past translations, newest first, and opens one in the
// popup where it can be copied again.
func (a *app) historyView(c *ui.Context) {
	t := fluentTheme(c)
	winW, _ := c.Size()
	pal := win11Theme(t)

	padH := float32(16)
	if winW < 540 {
		padH = 12
	}
	cardRad := float32(8)
	if isKDE() {
		cardRad = 6
	}

	total := a.histTotal
	if total < len(a.hist) {
		total = len(a.hist)
	}
	hasQuery := strings.TrimSpace(a.histQuery) != ""
	countLabel := fmt.Sprintf("共 %d 条", total)
	if hasQuery {
		countLabel = fmt.Sprintf("匹配 %d 条", len(a.hist))
	} else if total > len(a.hist) {
		countLabel = fmt.Sprintf("显示 %d / 共 %d 条", len(a.hist), total)
	}

	ui.Column(c).Grow(1).MinHeight(0).Padding(10, padH, 12, padH).Gap(8).Children(func() {
		// Compact Breeze toolbar: search + clear search + count badge + refresh + clear all
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.TextInput(c, &a.histQuery).
				Placeholder("搜索原文或译文…").Grow(1).Changed() {
				a.clearArmed = false
				a.reloadHistory()
			}
			if a.histQuery != "" {
				if ui.Button(c, "清除").Tooltip("清除搜索关键词").Clicked() {
					a.histQuery = ""
					a.reloadHistory()
				}
			}
			ui.Badge(c, countLabel).
				Background(pal.Accent.Alpha(0.14)).TextColor(pal.Accent)
			if ui.Button(c, "刷新").Tooltip("重新加载历史记录").Clicked() {
				a.reloadHistory()
			}
			if ui.Button(c, "导出 MD").Tooltip("导出为 Markdown 并复制到剪贴板").Disabled(len(a.hist) == 0).Clicked() {
				a.exportHistoryMarkdown()
			}
			if ui.Button(c, "导出 CSV").Tooltip("导出为 CSV 并复制到剪贴板").Disabled(len(a.hist) == 0).Clicked() {
				a.exportHistoryCSV()
			}
			clear := ui.Button(c, "清空历史").Disabled(total == 0)
			if clear.Tooltip("清空全部历史（无法恢复）").Clicked() {
				a.clearArmed = true
			}
		})

		if a.histStatus != "" && len(a.hist) > 0 {
			ui.Text(c, a.histStatus).FontSize(12).
				TextColor(statusColor(t, a.histStatus)).SingleLine().Shrink(1)
		}

		// History List
		if len(a.hist) == 0 {
			ui.Column(c).Grow(1).Radius(cardRad).Border(1, pal.Border).
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
				row := ui.ButtonBase(c).Gap(10).Padding(10, 12).Radius(cardRad).
					Background(pal.Card).Border(1, pal.Border).
					AlignItems(ui.Center).
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
								ui.Badge(c, "截图").Background(pal.Accent.Alpha(0.14)).TextColor(pal.Accent)
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
	})

	clearMsg := fmt.Sprintf("将永久删除全部 %d 条翻译记录，且无法恢复。", total)
	if hasQuery {
		clearMsg = fmt.Sprintf("将永久删除数据库中的全部 %d 条历史记录（不受当前搜索筛选影响），且无法恢复。", total)
	}
	if ui.AlertDialog(c, &a.clearArmed, "清空全部历史？",
		clearMsg,
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
		a.updatePopup(func() {
			if a.histReqID != reqID {
				return
			}
			if err != nil {
				a.histStatus = "清空失败：" + err.Error()
				return
			}
			a.hist = nil
			a.histTotal = 0
			a.histStatus = "已清空"
		})
	}()
}

func (a *app) exportHistoryMarkdown() {
	if len(a.hist) == 0 {
		return
	}
	md := history.ExportMarkdown(a.hist)
	mygo.Clipboard.WriteText(md)
	if dir := userDataDir(); dir != "" {
		path := filepath.Join(dir, "history-export.md")
		if err := os.WriteFile(path, []byte(md), 0o600); err == nil {
			a.histStatus = "已导出 Markdown（已复制到剪贴板并保存至 " + path + "）"
			return
		}
	}
	a.histStatus = "已复制 Markdown 到剪贴板"
}

func (a *app) exportHistoryCSV() {
	if len(a.hist) == 0 {
		return
	}
	csvText, err := history.ExportCSV(a.hist)
	if err != nil {
		a.histStatus = "导出 CSV 失败：" + err.Error()
		return
	}
	mygo.Clipboard.WriteText(csvText)
	if dir := userDataDir(); dir != "" {
		path := filepath.Join(dir, "history-export.csv")
		if err := os.WriteFile(path, []byte(csvText), 0o600); err == nil {
			a.histStatus = "已导出 CSV（已复制到剪贴板并保存至 " + path + "）"
			return
		}
	}
	a.histStatus = "已复制 CSV 到剪贴板"
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
