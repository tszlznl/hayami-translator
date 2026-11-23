package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

type historyState struct {
	hist       []historyEntry
	histTotal  int
	histQuery  string
	histStatus string
	histReqID  int
	histList   ui.ListState
	clearArmed bool
}

func (a *app) reloadHistory() {
	if a.store == nil {
		return
	}
	a.histReqID++
	id := a.histReqID
	q := a.histQuery
	go func() {
		entries, err := a.store.Search(q, 200)
		total, countErr := a.store.Count()
		a.updatePopup(func() {
			if a.histReqID != id || a.histQuery != q {
				return
			}
			if err != nil {
				a.histStatus = err.Error()
				return
			}
			a.hist = entries
			if countErr == nil {
				a.histTotal = total
			} else {
				a.histTotal = len(entries)
			}
			if len(entries) == 0 && strings.TrimSpace(q) != "" {
				a.histStatus = "没有匹配的记录"
			} else {
				a.histStatus = ""
			}
		})
	}()
}

// refreshHistory may be called from background goroutines; all UI state reads
// happen on the main thread inside updatePopup.
func (a *app) refreshHistory() {
	a.updatePopup(func() {
		if a.popup != nil && a.popup.IsVisible() && a.panelPage == panelHistory {
			a.reloadHistory()
		}
	})
}

func (a *app) useHistoryEntry(e historyEntry) {
	a.resetTranslation()
	a.activeHistoryID = e.ID
	a.source = e.Source
	a.result = e.Result
	a.followUpTurns = fromHistoryFollowUpTurns(e.FollowUps)
	a.targetLang = e.Target
	if a.targetLang == "" {
		a.targetLang = a.cfg.TargetLang
	}
	if _, ok := a.cfg.Mode(e.Mode); ok {
		a.activeMode = e.Mode
	}
	a.hasImage = false
	a.showPopup()
}
