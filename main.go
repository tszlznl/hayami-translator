package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

type providerResult struct {
	ID        string
	Name      string
	Kind      string
	Result    string
	ErrText   string
	Streaming bool
	MD        *markdown.Options
}

type trayController interface {
	SetMenu(*mygo.Menu)
	Destroy()
}

type app struct {
	cfg           Config
	providerName  string
	store         *historyStore
	initialAction string

	popup            *mygo.Window
	panelBackdrops   []*mygo.Window
	tray             trayController
	panelPage        panelPage
	selectionBlocked atomic.Bool
	selectionReqID   int
	hotkeys          []string
	hotkeyErr        string
	cfgWatchCancel   context.CancelFunc
	toolbarEnabled   bool

	// Translation shown by the popup.
	source        string
	result        string
	results       []providerResult
	multiEngine   bool
	quitting      bool
	errText       string
	errHint       string
	status        string
	streaming     bool
	hasImage      bool
	shot          []byte
	targetLang    string
	activeMode    string
	reqID         int
	cancel        context.CancelFunc
	shotCancel    context.CancelFunc
	md            markdown.Options
	popupScroll   ui.ScrollState
	focusInput    bool
	popupEditing  bool
	popupHadFocus bool
	popupPinned   bool
	inputHandle   ui.Handle

	// Follow-up turn on the current result.
	followUpInput     string
	followUpQ         string
	followUp          string
	followUpErr       string
	followUpStreaming bool
	followUpReqID     int
	followUpCancel    context.CancelFunc

	// Standalone input translation window.
	inputSource            string
	inputResult            string
	inputSourceLang        string
	inputTargetLang        string
	inputStreaming         bool
	inputErr               string
	inputReqID             int
	inputCancel            context.CancelFunc
	inputWindowNeedFocus   bool
	inputWindowInputHandle ui.Handle

	// Settings window. form is a draft; the running config changes only when
	// Save is pressed.
	form                 Config
	openAtLogin          bool
	settingsStatus       string
	settingsCloseAsk     bool
	providerDeleteArmed  string
	settingsTab          int
	settingsModesEditing bool
	settingsScroll       ui.ScrollState

	// History window.
	hist       []historyEntry
	histQuery  string
	histStatus string
	histReqID  int
	histList   ui.ListState
	clearArmed bool
}

const appVersion = "0.2.2"

func parseCLIAction(args []string) string {
	for _, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "selection", "--selection", "-s", "-sel":
			return "selection"
		case "input", "--input", "-i":
			return "input"
		case "screenshot", "--screenshot", "-c", "-ss", "--shot", "shot":
			return "screenshot"
		case "settings", "--settings", "-p":
			return "settings"
		case "toggle", "--toggle", "-t":
			return "toggle"
		}
	}
	return ""
}

func printCLIHelp() {
	fmt.Println(`Hayami Translator - 极简原生的划词与多引擎翻译工具

用法:
  hayami [选项]

选项:
  -s, --selection   读取当前选区并翻译 (默认快捷键: Alt+F)
  -i, --input       打开独立输入翻译窗口 (默认快捷键: Alt+D)
  -c, --screenshot  区域截图翻译 (默认快捷键: Alt+S)
  -p, --settings    打开设置界面
  -t, --toggle      显示或隐藏主面板
  -h, --help        显示帮助信息
  -v, --version     显示版本信息`)
}

func printCLIVersion() {
	fmt.Printf("Hayami Translator v%s\n", appVersion)
}

func main() {
	// Tune memory footprint for resident background daemon operation:
	// Avoid loading heavy proprietary GPU shader compiler & LLVM libs unless requested.
	if os.Getenv("MYGO_GPU") == "" {
		_ = os.Setenv("MYGO_GPU", "0")
	}
	debug.SetGCPercent(40)
	debug.SetMemoryLimit(48 * 1024 * 1024)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--help", "-h", "help":
			printCLIHelp()
			return
		case "--version", "-v", "version":
			printCLIVersion()
			return
		}
	}

	initialAction := parseCLIAction(os.Args[1:])

	mygo.App.SetName("Hayami Translator")

	if dir := userDataDir(); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		f, err := os.OpenFile(filepath.Join(dir, "debug.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err == nil {
			log.SetOutput(f)
			defer f.Close()
		}
	}
	log.Println("[debug] entering main()")

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[debug] PANIC in main: %v\n%s", r, debug.Stack())
		}
	}()

	if !mygo.App.RequestSingleInstanceLock() {
		log.Println("[debug] single instance lock refused, exiting")
		return
	}
	log.Println("[debug] single instance lock acquired")
	a := &app{
		initialAction: initialAction,
	}
	mygo.App.OnWindowAllClosed(func() {}) // a tray app: closing a window does not quit
	mygo.App.OnSecondInstance(func(args []string, _ string) {
		log.Println("[debug] OnSecondInstance received:", args)
		a.dispatchAction(parseCLIAction(args))
	})
	mygo.App.WhenReady(a.ready)
	log.Println("[debug] calling mygo.App.Run()")
	if err := mygo.App.Run(); err != nil {
		log.Println("[debug] App.Run returned error:", err)
		log.Fatal(err)
	}
	log.Println("[debug] main() finished cleanly")
}

func (a *app) ready() {
	log.Println("[debug] ready() started")
	migrateUserData()
	log.Println("[debug] migrateUserData() done")
	migrateLegacyStartup()
	log.Println("[debug] migrateLegacyStartup() done")
	if err := writeConfigSchema(); err != nil {
		log.Print("writing config schema: ", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Print("loading config: ", err)
	}
	log.Printf("[debug] loadConfig() done, providers=%d", len(cfg.Providers))
	a.cfg = loadProviderKeys(cfg)
	for _, p := range a.cfg.Providers {
		log.Printf("[debug] provider %s (%s): enabled=%v, keyLen=%d, configured=%v",
			p.ID, p.Kind, p.Enabled, len(p.Key), providerConfigured(p, p.Key))
	}
	a.form = a.cfg.Clone()
	a.providerName = activeProviderLabel(a.cfg)
	a.targetLang = a.cfg.TargetLang
	a.activeMode = a.cfg.ActiveMode
	a.multiEngine = a.cfg.MultiEngine
	a.toolbarEnabled = !a.cfg.DisableSelectionToolbar
	a.popupPinned = a.cfg.PopupPinned
	applyThemeMode(a.cfg.Theme)
	a.openAtLogin = mygo.App.OpenAtLogin()
	a.md = markdown.Options{Theme: &markdown.Theme{FontSize: 13}}

	a.store, err = openHistory()
	if err != nil {
		log.Print("opening history: ", err)
		a.histStatus = "历史数据库不可用：" + err.Error()
	}
	log.Println("[debug] openHistory() done")

	a.createWindows()
	log.Println("[debug] createWindows() done")
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		a.quitting = true
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if a.cfgWatchCancel != nil {
			a.cfgWatchCancel()
			a.cfgWatchCancel = nil
		}
		a.cancelFollowUp()
		stopSelectionHook()
		a.cancelStream()
		if a.shotCancel != nil {
			a.shotCancel()
			a.shotCancel = nil
		}
		if a.inputCancel != nil {
			a.inputCancel()
			a.inputCancel = nil
		}
		stopSpeaking()
		if a.tray != nil {
			a.tray.Destroy()
		}
		a.store.Close()
	})
	a.registerHotkeys()
	log.Printf("[debug] registerHotkeys() done, hotkeys=%v, hotkeyErr=%q", a.hotkeys, a.hotkeyErr)
	a.setUpTray()
	log.Printf("[debug] setUpTray() done, tray=%v", a.tray != nil)
	setSelectionToolbarEnabled(a.toolbarEnabled)
	if err := startSelectionHook(a); err != nil {
		log.Print("starting selection toolbar hook: ", err)
	}
	log.Println("[debug] startSelectionHook() done")
	a.startConfigWatch()

	configured := hasConfiguredProvider(a.cfg)
	openedAtLogin := mygo.App.WasOpenedAtLogin()
	log.Printf("[debug] configured=%v, openedAtLogin=%v, initialAction=%q", configured, openedAtLogin, a.initialAction)
	if a.initialAction != "" {
		a.dispatchAction(a.initialAction)
	} else if !configured {
		log.Println("[debug] showing settings because no configured provider")
		a.showSettings()
	} else if !openedAtLogin {
		log.Println("[debug] showing centered popup on launch")
		a.showPopup()
	}
	runtime.GC()
	debug.FreeOSMemory()
	log.Println("[debug] ready() finished")
}

func (a *app) dispatchAction(action string) {
	if !hasConfiguredProvider(a.cfg) {
		a.showSettings()
		return
	}
	switch action {
	case "selection":
		a.translateSelection()
	case "input":
		a.showInputWindow()
	case "screenshot":
		a.translateScreenshot()
	case "settings":
		a.showSettings()
	case "toggle":
		a.togglePanel()
	default:
		a.showPopup()
	}
}

// engineLabel names the engine in the popup and input window footers: the
// active provider, else the legacy model field, else the built-in label.
func (a *app) engineLabel() string {
	if a.providerName != "" {
		return a.providerName
	}
	if a.cfg.Model != "" {
		return a.cfg.Model
	}
	return "Microsoft Translator"
}

func (a *app) inputHotkeyLabel() string {
	h := strings.TrimSpace(a.cfg.InputHotkey)
	if h == "" {
		return "Alt+D"
	}
	return h
}

func (a *app) createWindows() {
	a.createPanelBackdrops()
	w, h := a.desiredPanelSize()
	rad := 12
	if isKDE() {
		rad = 8
	}
	a.popup = mygo.NewWindow(mygo.WindowOptions{
		Title:         "Hayami Translator",
		Hidden:        true,
		Panel:         true,
		Frameless:     true,
		Transparent:   true,
		Vibrancy:      mygo.VibrancyPopover,
		CornerRadius:  rad,
		AlwaysOnTop:   true,
		SkipTaskbar:   true,
		DisableResize: true,
		Width:         w,
		Height:        h,
		Content:       ui.View(a.panelView),
	})
	a.popup.OnClose(func(e *mygo.CloseEvent) {
		a.handleWindowClose(e, a.requestClosePanel)
	})
	a.popup.OnFocus(func() {
		log.Print("[debug] panel focused")
		a.popupHadFocus = true
		a.selectionBlocked.Store(true)
	})
	a.popup.OnBlur(func() {
		log.Print("[debug] panel blurred")
		a.selectionBlocked.Store(false)
		if a.popupHadFocus && !a.popupPinned && len(a.panelBackdrops) == 0 {
			a.hidePopup()
		}
		a.popupHadFocus = false
	})
}

func (a *app) handleWindowClose(e *mygo.CloseEvent, hide func()) {
	if a.quitting {
		return
	}
	e.PreventDefault()
	if hide != nil {
		hide()
	}
}

func (a *app) showPopup() {
	a.showPanel(panelTranslation)
}

func (a *app) hidePopup() {
	a.invalidateCapture()
	a.popupHadFocus = false
	a.selectionBlocked.Store(false)
	a.popupPinned = a.cfg.PopupPinned
	if a.popup != nil {
		a.popup.Hide()
	}
	for _, backdrop := range a.panelBackdrops {
		backdrop.Hide()
	}
	runtime.GC()
	debug.FreeOSMemory()
}

func (a *app) refreshTheme() {
	if a.popup != nil && a.popup.IsVisible() {
		a.popup.Invalidate()
	}
}

func (a *app) refreshSettingsWindow() {
	if a.panelPage == panelSettings {
		a.refreshTheme()
	}
}

func (a *app) showSettings() {
	if !a.settingsDirty() {
		a.form = a.cfg.Clone()
		a.toolbarEnabled = !a.cfg.DisableSelectionToolbar
		a.providerDeleteArmed = ""
		a.settingsModesEditing = len(a.form.Modes) > 0
	}
	if a.popup != nil {
		a.openAtLogin = mygo.App.OpenAtLogin()
	}
	a.settingsCloseAsk = false
	a.showPanel(panelSettings)
}

func (a *app) showHistory() {
	a.clearArmed = false
	a.showPanel(panelHistory)
	a.reloadHistory()
}

func (a *app) registerHotkeys() {
	for _, h := range a.hotkeys {
		mygo.GlobalShortcut.Unregister(h)
	}
	a.hotkeys = nil
	a.hotkeyErr = ""
	if h := strings.TrimSpace(a.cfg.Hotkey); h != "" {
		if err := mygo.GlobalShortcut.Register(h, a.translateSelection); err != nil {
			a.hotkeyErr = "翻译快捷键 " + h + " 注册失败：" + err.Error()
		} else {
			a.hotkeys = append(a.hotkeys, h)
		}
	}
	if h := strings.TrimSpace(a.cfg.ScreenshotHotkey); h != "" {
		if err := mygo.GlobalShortcut.Register(h, a.translateScreenshot); err != nil {
			if a.hotkeyErr != "" {
				a.hotkeyErr += "\n"
			}
			a.hotkeyErr += "截图快捷键 " + h + " 注册失败：" + err.Error()
		} else {
			a.hotkeys = append(a.hotkeys, h)
		}
	}
	// Standalone input translation window shortcut
	inputHk := a.inputHotkeyLabel()
	if err := mygo.GlobalShortcut.Register(inputHk, a.showInputWindow); err != nil {
		if a.hotkeyErr != "" {
			a.hotkeyErr += "\n"
		}
		a.hotkeyErr += "输入翻译快捷键 " + inputHk + " 注册失败：" + err.Error()
	} else {
		a.hotkeys = append(a.hotkeys, inputHk)
	}
}

func (a *app) setUpTray() {
	tray, err := mygo.NewTray(mygo.TrayOptions{Icon: trayIcon(), ToolTip: "Hayami Translator 划词翻译"})
	if err != nil {
		log.Print("no tray icon: ", err)
		return
	}
	log.Println("[debug] mygo.NewTray succeeded")
	a.tray = tray
	tray.OnClick(a.togglePanel)
	a.refreshTrayMenu()
	if !mygo.App.WasOpenedAtLogin() {
		_ = mygo.NewNotification(mygo.NotificationOptions{
			Title: "Hayami Translator 划词翻译",
			Body:  "已在后台就绪：按 " + a.cfg.Hotkey + " 划词翻译，按 " + a.inputHotkeyLabel() + " 打开输入翻译",
		}).Show()
	}
}

// refreshTrayMenu rebuilds the menu after a setting changes.
func (a *app) refreshTrayMenu() {
	if a.tray == nil {
		return
	}
	a.tray.SetMenu(a.trayMenu())
}

func (a *app) trayMenu() *mygo.Menu {
	toolbarStatus := "已开启"
	if !a.toolbarEnabled {
		toolbarStatus = "已关闭"
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: "划词翻译 (" + toolbarStatus + ")", Click: func(*mygo.MenuItem, *mygo.Window) {
			old := a.toolbarEnabled
			a.toolbarEnabled = !old
			a.cfg.DisableSelectionToolbar = !a.toolbarEnabled
			if err := a.cfg.Save(); err != nil {
				log.Print("saving toolbar setting: ", err)
				a.toolbarEnabled = old
				a.cfg.DisableSelectionToolbar = !old
				a.refreshTrayMenu()
				return
			}
			a.form.DisableSelectionToolbar = !a.toolbarEnabled
			setSelectionToolbarEnabled(a.toolbarEnabled)
			a.refreshTrayMenu()
			a.refreshSettingsWindow()
		}},
		{Label: "显示翻译窗口", Click: func(*mygo.MenuItem, *mygo.Window) { a.showPopup() }},
		{Label: "翻译选中文本", Accelerator: a.cfg.Hotkey, Click: func(*mygo.MenuItem, *mygo.Window) { a.translateSelection() }},
		{Label: "输入翻译…", Accelerator: a.inputHotkeyLabel(), Click: func(*mygo.MenuItem, *mygo.Window) { a.showInputWindow() }},
		{Label: "截图翻译", Accelerator: a.cfg.ScreenshotHotkey, Click: func(*mygo.MenuItem, *mygo.Window) { a.translateScreenshot() }},
		mygo.Separator(),
		{Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) { a.showSettings() }},
		{Label: "历史记录…", Click: func(*mygo.MenuItem, *mygo.Window) { a.showHistory() }},
		mygo.Separator(),
		{Label: "开机启动", Type: mygo.MenuItemCheckbox, ID: "login", Checked: a.openAtLogin,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) {
				if err := a.updateOpenAtLogin(it.Checked, mygo.App.SetOpenAtLogin); err != nil {
					log.Print("setting open at login: ", err)
					a.refreshTrayMenu()
					return
				}
				a.refreshSettingsWindow()
			}},
		{Role: mygo.RoleQuit, Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) { a.quit() }},
	})
}

func (a *app) updateOpenAtLogin(enabled bool, apply func(bool) error) error {
	if err := apply(enabled); err != nil {
		return err
	}
	a.openAtLogin = enabled
	a.refreshTrayMenu()
	return nil
}

func (a *app) quit() {
	a.quitting = true
	mygo.App.Quit()
}

// translateSelection reads the selection of the foreground window before
// showing the panel: showing it first would take the selection away.
func (a *app) translateSelection() {
	if a.popup == nil {
		return
	}
	if a.popup.IsVisible() {
		a.hidePopup()
	}
	a.selectionReqID++
	id := a.selectionReqID
	go func() {
		text, err := readSelectedText()
		a.popup.Update(func() {
			if a.quitting || id != a.selectionReqID {
				return
			}
			if err == nil && strings.TrimSpace(text) != "" {
				a.showTranslation(text)
				return
			}
			a.resetTranslation()
			a.source = text
			a.status = "输入文本后按 Ctrl+Enter 翻译"
			if err != nil {
				a.status = err.Error()
			}
			a.focusInput = true
			a.popupEditing = true
			a.showPopup()
		})
	}()
}

func (a *app) showTranslation(text string) {
	if a.popup == nil {
		return
	}
	a.resetTranslation()
	a.source = text
	a.hasImage = false
	a.shot = nil
	a.showPopup()
	if strings.TrimSpace(text) == "" {
		a.status = "没有读到选中文本，直接输入后点翻译"
		a.focusInput = true
		a.popupEditing = true
		return
	}
	a.startTranslation()
}

// translateScreenshot opens the system snipping overlay and translates the
// image it puts on the clipboard with a vision model.
func (a *app) translateScreenshot() {
	if a.popup != nil && a.popup.IsVisible() {
		a.hidePopup()
	}
	if a.shotCancel != nil {
		a.shotCancel()
		a.shotCancel = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	a.shotCancel = cancel

	a.selectionReqID++
	id := a.selectionReqID
	go func() {
		defer cancel()
		img, err := captureScreenshot(ctx)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		a.popup.Update(func() {
			if a.quitting || id != a.selectionReqID {
				return
			}
			a.resetTranslation()
			if err != nil {
				a.status = "截图失败：" + err.Error()
				a.showPopup()
				return
			}
			a.hasImage = true
			a.shot = img
			a.source = ""
			a.showPopup()
			a.startTranslation()
		})
	}()
}

func (a *app) resetTranslation() {
	a.cancelStream()
	a.cancelFollowUp()
	a.followUpInput = ""
	a.followUpQ = ""
	a.followUp = ""
	a.followUpErr = ""
	a.source = ""
	a.result = ""
	a.results = nil
	a.errText = ""
	a.errHint = ""
	a.status = ""
	a.hasImage = false
	a.shot = nil
	a.popupEditing = false
}

func (a *app) cancelStream() {
	a.reqID++
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.streaming = false
}

func (a *app) startTranslation() {
	a.cancelStream()
	a.cancelFollowUp()
	a.followUpQ = ""
	a.followUp = ""
	a.followUpErr = ""
	cfg := a.cfg
	var messages []chatMessage
	var mode, sourceText string

	switch {
	case a.hasImage && len(a.shot) > 0:
		messages = imageMessages(cfg, a.shot)
		mode, sourceText = "image", "[截图]"
	default:
		sourceText = strings.TrimSpace(a.source)
		if sourceText == "" {
			a.status = "请输入要处理的文本"
			return
		}
		cfg.TargetLang = effectiveTarget(cfg.TargetLang, sourceText)
		m := a.currentMode()
		messages = messagesForMode(cfg, sourceText, m)
		mode = m.ID
	}

	providers := cfg.EnabledProviders(a.hasImage)
	configured := providers[:0]
	for _, p := range providers {
		if providerConfigured(p, p.Key) {
			configured = append(configured, p)
		}
	}
	providers = configured
	a.result = ""
	a.results = nil
	a.errText = ""
	a.errHint = ""
	a.status = ""
	a.focusInput = false
	a.popupEditing = false

	if len(providers) == 0 {
		if a.hasImage {
			a.errText = "截图翻译需要启用一个配置好的 OpenAI Compatible 视觉模型"
		} else {
			a.errText = "没有可用的翻译服务，请在设置里填写 Key"
		}
		a.errHint = "打开设置，确认服务已启用并填写了 Key。"
		a.streaming = false
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.reqID++
	id := a.reqID
	a.streaming = true

	if a.multiEngine {
		a.results = make([]providerResult, len(providers))
		for i, p := range providers {
			a.results[i] = providerResult{
				ID:        p.ID,
				Name:      p.Name,
				Kind:      p.Kind,
				Streaming: true,
				MD:        &markdown.Options{Theme: &markdown.Theme{FontSize: 13}},
			}
		}

		go func() {
			var wg sync.WaitGroup
			for i, p := range providers {
				idx := i
				prov := p
				key := prov.Key
				wg.Add(1)
				go func() {
					defer wg.Done()
					var pPending strings.Builder
					pFlush := func() {
						if pPending.Len() == 0 {
							return
						}
						chunk := pPending.String()
						pPending.Reset()
						a.popup.Update(func() {
							if a.reqID == id && idx < len(a.results) {
								a.results[idx].Result += chunk
							}
						})
					}
					pLast := time.Now()
					err := translateWithProvider(ctx, cfg, prov, key, messages, sourceText, func(delta string) {
						pPending.WriteString(delta)
						if time.Since(pLast) >= 30*time.Millisecond {
							pFlush()
							pLast = time.Now()
						}
					})
					pFlush()
					a.popup.Update(func() {
						if a.reqID == id && idx < len(a.results) {
							a.results[idx].Streaming = false
							if err != nil && ctx.Err() == nil {
								title, _ := explainError(err)
								a.results[idx].ErrText = title
							}
						}
					})
				}()
			}

			wg.Wait()

			a.popup.Update(func() {
				if a.reqID != id {
					return
				}
				a.streaming = false
				if a.panelPage == panelTranslation && a.popup != nil && a.popup.IsVisible() {
					a.placePanel()
				}
				var successCount int
				var combined strings.Builder
				for i := range a.results {
					r := &a.results[i]
					if strings.TrimSpace(r.Result) != "" && r.ErrText == "" {
						successCount++
					}
					if strings.TrimSpace(r.Result) != "" {
						if combined.Len() > 0 {
							combined.WriteString("\n\n---\n\n")
						}
						combined.WriteString("### " + r.Name + "\n\n" + r.Result)
					}
				}
				if successCount == 0 && ctx.Err() == nil {
					a.errText = "所有翻译服务均失败"
					a.errHint = "展开各服务的错误卡片，或检查设置里的翻译服务。"
				} else if successCount < len(a.results) {
					a.status = fmt.Sprintf("部分完成 (%d/%d 成功)", successCount, len(a.results))
					a.result = combined.String()
				} else {
					a.status = fmt.Sprintf("全部完成 (%d/%d 成功)", successCount, len(a.results))
					a.result = combined.String()
				}
				result := a.result
				if strings.TrimSpace(result) == "" {
					return
				}
				entry := historyEntry{
					CreatedAt: timeNow(),
					Source:    sourceText,
					Result:    result,
					Target:    cfg.TargetLang,
					Mode:      mode,
				}
				go func() {
					if err := a.store.Add(entry); err != nil {
						log.Print("history: ", err)
						return
					}
					a.refreshHistory()
				}()
			})
		}()
		return
	}

	a.results = nil
	go func() {
		var pending strings.Builder
		flush := func() {
			if pending.Len() == 0 {
				return
			}
			chunk := pending.String()
			pending.Reset()
			a.popup.Update(func() {
				if a.reqID == id {
					a.result += chunk
				}
			})
		}
		last := time.Now()

		err := translateWithFallback(ctx, cfg, providers, messages, sourceText, func(p Provider) {
			pending.Reset()
			a.popup.Update(func() {
				if a.reqID == id {
					a.providerName = providerLabel(p)
					a.result = ""
				}
			})
		}, func(delta string) {
			pending.WriteString(delta)
			// Repaint at most every 30ms instead of once per token.
			if time.Since(last) >= 30*time.Millisecond {
				flush()
				last = time.Now()
			}
		})
		flush()

		a.popup.Update(func() {
			if a.reqID != id {
				return
			}
			a.streaming = false
			if a.panelPage == panelTranslation && a.popup != nil && a.popup.IsVisible() {
				a.placePanel()
			}
			if err != nil && ctx.Err() == nil {
				title, hint := explainError(err)
				if strings.TrimSpace(a.result) == "" {
					a.errText = title
					a.errHint = hint
				} else {
					a.status = "部分失败：" + title
				}
			}
			result := a.result
			if strings.TrimSpace(result) == "" {
				return
			}
			entry := historyEntry{
				CreatedAt: timeNow(),
				Source:    sourceText,
				Result:    result,
				Target:    cfg.TargetLang,
				Mode:      mode,
			}
			go func() {
				if err := a.store.Add(entry); err != nil {
					log.Print("history: ", err)
					return
				}
				a.refreshHistory()
			}()
		})
	}()
}

func (a *app) hasAnyResult() bool {
	if a.multiEngine && len(a.results) > 0 {
		for i := range a.results {
			if strings.TrimSpace(a.results[i].Result) != "" {
				return true
			}
		}
		return false
	}
	return strings.TrimSpace(a.result) != ""
}

func (a *app) copyResult() {
	if a.multiEngine && len(a.results) > 0 {
		var b strings.Builder
		for i := range a.results {
			r := &a.results[i]
			if strings.TrimSpace(r.Result) != "" {
				if b.Len() > 0 {
					b.WriteString("\n\n---\n\n")
				}
				b.WriteString("【" + r.Name + "】\n" + r.Result)
			}
		}
		if b.Len() > 0 {
			mygo.Clipboard.WriteText(b.String())
			a.status = "已复制全部引擎译文"
		}
		return
	}
	if strings.TrimSpace(a.result) == "" {
		return
	}
	mygo.Clipboard.WriteText(a.result)
	a.status = "已复制译文"
}

func (a *app) saveSettings() {
	f := a.form.Clone()
	f.FillDefaults()
	f.NormalizeProviders()
	f.BaseURL = strings.TrimSpace(f.BaseURL)
	f.Model = strings.TrimSpace(f.Model)
	f.Hotkey = strings.TrimSpace(f.Hotkey)
	f.ScreenshotHotkey = strings.TrimSpace(f.ScreenshotHotkey)
	f.InputHotkey = strings.TrimSpace(f.InputHotkey)
	if err := validateConfig(f); err != nil {
		a.settingsStatus = err.Error()
		return
	}
	// Save keys first; if keyring fails, abort without overwriting config file
	if err := saveProviderKeys(f); err != nil {
		a.settingsStatus = "保存失败，密钥写入凭据管理器失败：" + err.Error()
		return
	}
	if err := f.Save(); err != nil {
		a.settingsStatus = "保存配置文件失败：" + err.Error()
		return
	}
	for _, old := range a.cfg.Providers {
		kept := false
		for _, p := range f.Providers {
			if p.ID == old.ID {
				kept = true
				break
			}
		}
		if !kept {
			deleteProviderKey(old)
		}
	}
	a.cfg = f
	a.form = f.Clone()
	a.toolbarEnabled = !f.DisableSelectionToolbar
	applyThemeMode(f.Theme)
	a.refreshTheme()
	a.setPanelPinned(f.PopupPinned)
	a.providerDeleteArmed = ""
	a.providerName = activeProviderLabel(f)
	a.targetLang = f.TargetLang
	a.activeMode = f.ActiveMode
	a.multiEngine = f.MultiEngine
	setSelectionToolbarEnabled(a.toolbarEnabled)
	a.registerHotkeys()
	a.refreshTrayMenu()
	if !hasConfiguredProvider(f) {
		a.settingsStatus = "已保存，但没有可用的翻译服务"
	} else if a.hotkeyErr != "" {
		a.settingsStatus = "已保存。" + a.hotkeyErr
	} else {
		a.settingsStatus = "已保存"
	}
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
		a.popup.Update(func() {
			if a.histReqID != id || a.histQuery != q {
				return
			}
			if err != nil {
				a.histStatus = err.Error()
				return
			}
			a.hist = entries
			if len(entries) == 0 && strings.TrimSpace(q) != "" {
				a.histStatus = "没有匹配的记录"
			} else {
				a.histStatus = ""
			}
		})
	}()
}

func (a *app) refreshHistory() {
	if a.popup == nil || !a.popup.IsVisible() || a.panelPage != panelHistory {
		return
	}
	a.popup.Update(func() { a.reloadHistory() })
}

func (a *app) useHistoryEntry(e historyEntry) {
	a.resetTranslation()
	a.source = e.Source
	a.result = e.Result
	a.targetLang = e.Target
	if a.targetLang == "" {
		a.targetLang = a.cfg.TargetLang
	}
	a.cfg.TargetLang = a.targetLang
	a.form.TargetLang = a.targetLang
	if _, ok := a.cfg.Mode(e.Mode); ok {
		a.activeMode = e.Mode
		a.cfg.ActiveMode = e.Mode
		a.form.ActiveMode = e.Mode
	}
	a.hasImage = false
	a.showPopup()
}

//go:embed resources/tray.png
var trayIconPNG []byte

// trayIcon is the Hayami mascot cropped for the Windows notification area.
func trayIcon() []byte {
	return trayIconPNG
}

func timeNow() time.Time { return time.Now() }
