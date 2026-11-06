//go:build linux

package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

var (
	selectionHookMu     sync.Mutex
	selectionHookCancel context.CancelFunc
)

func startSelectionHook(a *app) error {
	selectionHookMu.Lock()
	defer selectionHookMu.Unlock()
	if selectionHookCancel != nil {
		return nil
	}
	if _, err := exec.LookPath("wl-paste"); err != nil {
		return fmt.Errorf("划词翻译需要 wl-paste: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	selectionHookCancel = cancel
	go pollSelection(ctx, a)
	return nil
}

func stopSelectionHook() {
	selectionHookMu.Lock()
	defer selectionHookMu.Unlock()
	if selectionHookCancel != nil {
		selectionHookCancel()
		selectionHookCancel = nil
	}
	// Do not join here: a pending RunOnMain may be waiting on this UI thread.
}

func pollSelection(ctx context.Context, a *app) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	var debounce selectionDebouncer
	loggedReadError := false
	for ctx.Err() == nil {
		if !selectionToolbarEnabled.Load() {
			debounce = selectionDebouncer{}
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
				continue
			}
		}

		// Reject samples started before focus changed, even if wl-paste finishes afterward.
		allowed := !a.selectionBlocked.Load()
		text, err := readSelectedTextContext(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !loggedReadError {
			log.Printf("划词读取失败（本次监控仅记录一次）: %v", err)
			loggedReadError = true
		}
		if debounce.sample(text, allowed && !a.selectionBlocked.Load() && selectionToolbarEnabled.Load(), err) {
			mygo.RunOnMain(func() {
				if ctx.Err() == nil && !a.selectionBlocked.Load() && selectionToolbarEnabled.Load() {
					a.receiveSelection(text)
				}
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
