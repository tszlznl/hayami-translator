package main

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

const configFileName = "config.json"

// startConfigWatch reloads config.json after an external edit. The file is an
// interface for coding agents: they can change it while the app runs. Our own
// saves also trigger the watch and settle as a no-op.
func (a *app) startConfigWatch() {
	dir := userDataDir()
	if dir == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cfgWatchCancel = cancel

	go func() {
		watcher, err := mygo.WatchFiles(ctx, dir, mygo.FileWatchOptions{
			Debounce: 300 * time.Millisecond,
		})
		if err != nil {
			log.Print("watching config: ", err)
			return
		}
		defer watcher.Close()

		for {
			events, err := watcher.Next(ctx)
			if err != nil {
				return
			}
			if !configChanged(events) {
				continue
			}

			cfg, err := loadConfig()
			if err != nil {
				log.Print("reloading config: ", err)
				message := err
				mygo.RunOnMain(func() { a.showConfigError(message) })
				continue
			}
			cfg = loadProviderKeys(cfg)
			mygo.RunOnMain(func() { a.applyReloadedConfig(cfg) })
		}
	}()
}

func configChanged(events []mygo.FileEvent) bool {
	for _, event := range events {
		if event.Op == mygo.FileRemove {
			continue
		}
		if isConfigPath(event.Path) || isConfigPath(event.OldPath) {
			return true
		}
	}
	return false
}

func isConfigPath(path string) bool {
	return strings.EqualFold(filepath.Base(path), configFileName)
}

// applyReloadedConfig runs on the main thread. An unsaved settings draft is
// kept so a reload never discards what the user is typing.
func (a *app) applyReloadedConfig(next Config) {
	if a.quitting {
		return
	}
	if a.cfg.SameConfig(next) {
		return
	}
	dirty := a.settingsDirty()
	oldHotkeys := a.cfg.Hotkey + "\x00" + a.cfg.ScreenshotHotkey + "\x00" + a.cfg.InputHotkey
	oldToolbar := a.toolbarEnabled

	a.cfg = next
	if !dirty {
		a.form = next.Clone()
	}
	a.providerName = activeProviderLabel(next)
	a.targetLang = next.TargetLang
	a.activeMode = next.ActiveMode
	a.multiEngine = next.MultiEngine
	a.toolbarEnabled = !next.DisableSelectionToolbar
	a.setPanelPinned(next.PopupPinned)
	applyThemeMode(next.Theme)
	a.refreshTheme()

	if a.toolbarEnabled != oldToolbar {
		setSelectionToolbarEnabled(a.toolbarEnabled)
	}
	if oldHotkeys != next.Hotkey+"\x00"+next.ScreenshotHotkey+"\x00"+next.InputHotkey {
		a.registerHotkeys()
	}
	a.refreshTrayMenu()
	a.refreshSettingsWindow()
	log.Println("[debug] config reloaded")
}

func (a *app) showConfigError(err error) {
	if a.quitting {
		return
	}
	if a.popup != nil && a.popup.IsVisible() && a.panelPage == panelSettings {
		a.settingsStatus = "配置文件有误：" + err.Error()
		a.refreshSettingsWindow()
	}
}
