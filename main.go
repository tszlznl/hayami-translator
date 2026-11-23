package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

type trayController interface {
	SetMenu(*mygo.Menu)
	Destroy()
}

type app struct {
	cfg              Config
	providerName     string
	store            *historyStore
	initialAction    string
	quitting         bool
	popup            *mygo.Window
	panelBackdrops   []*mygo.Window
	displaysOff      func()
	tray             trayController
	panelPage        panelPage
	popupHadFocus    bool
	popupPinned      bool
	selectionBlocked atomic.Bool
	selectionReqID   int
	shotCancel       context.CancelFunc
	hotkeys          []string
	hotkeyErr        string
	cfgWatchCancel   context.CancelFunc
	toolbarEnabled   bool
	openAtLogin      bool
	md               markdown.Options

	translationState
	inputState
	settingsState
	historyState
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
		case "silent", "--silent", "--background", "-b", "--autostart", "--minimized":
			return "silent"
		}
	}
	return ""
}

func printCLIHelp() {
	fmt.Println(`Hayami Translator - 极简原生的划词与多引擎翻译工具

用法:
  hayami [选项]
  hayami translate [--target <语言>] [--mode <模式>] [--json] < input.txt
  hayami doctor [--json]

子命令:
  translate         从标准输入读取文本并输出翻译结果（无头 CLI 模式）
  doctor            检查配置文件、凭据、SQLite 历史库及翻译服务连通性

选项:
  -s, --selection   读取当前选区并翻译 (默认快捷键: Alt+F)
  -i, --input       打开独立输入翻译窗口 (默认快捷键: Alt+D)
  -c, --screenshot  区域截图翻译 (默认快捷键: Alt+S)
  -p, --settings    打开设置界面
  -t, --toggle      显示或隐藏主面板
  -b, --silent      后台静默启动（仅驻留系统托盘）
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
	debug.SetGCPercent(60)
	debug.SetMemoryLimit(128 * 1024 * 1024)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--help", "-h", "help":
			printCLIHelp()
			return
		case "--version", "-v", "version":
			printCLIVersion()
			return
		case "translate":
			os.Exit(runCLITranslate(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		case "doctor":
			os.Exit(runCLIDoctor(os.Args[2:], os.Stdout, os.Stderr))
		}
	}

	initialAction := parseCLIAction(os.Args[1:])

	mygo.App.SetName("Hayami Translator")

	// Acquire the single-instance lock before opening/truncating debug.log so
	// secondary CLI invocations (e.g. `hayami -s`) never wipe the running
	// daemon's log file.
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}

	if dir := userDataDir(); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		f, err := os.OpenFile(filepath.Join(dir, "debug.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err == nil {
			log.SetOutput(f)
			defer f.Close()
		}
	}
	log.Println("[debug] single instance lock acquired")

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[debug] PANIC in main: %v\n%s", r, debug.Stack())
		}
	}()
	a := &app{
		initialAction: initialAction,
	}
	mygo.App.OnWindowAllClosed(func() {}) // a tray app: closing a window does not quit
	mygo.App.OnSecondInstance(func(args []string, _ string) {
		log.Println("[debug] OnSecondInstance received:", args)
		action := parseCLIAction(args)
		if action == "silent" {
			return
		}
		a.dispatchAction(action)
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
	a.syncFormModelParams()
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
	} else if a.cfg.HistoryLimit > 0 {
		_ = a.store.SetMaxEntries(a.cfg.HistoryLimit)
	}
	setSpeechConfig(a.cfg)
	log.Println("[debug] openHistory() done")

	a.createWindows()
	portalAppearanceChangedHook = func() {
		mygo.RunOnMain(func() {
			a.refreshTheme()
		})
	}
	a.displaysOff = mygo.Screen.OnDisplaysChanged(a.onDisplaysChanged)
	log.Println("[debug] createWindows() done")
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		a.quitting = true
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if a.displaysOff != nil {
			a.displaysOff()
			a.displaysOff = nil
		}
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
	silent := a.isSilentLaunch()
	log.Printf("[debug] configured=%v, silent=%v, initialAction=%q", configured, silent, a.initialAction)
	if a.initialAction != "" && a.initialAction != "silent" {
		a.dispatchAction(a.initialAction)
	} else if silent {
		log.Println("[debug] silent launch at login: staying in tray")
	} else if !configured {
		log.Println("[debug] showing settings because no configured provider")
		a.showSettings()
	} else {
		log.Println("[debug] showing centered popup on launch")
		a.showPopup()
	}
	runtime.GC()
	debug.FreeOSMemory()
	log.Println("[debug] ready() finished")
}

func (a *app) isSilentLaunch() bool {
	return mygo.App.WasOpenedAtLogin() || a.initialAction == "silent"
}

func (a *app) ensureProviderKeysLoaded() {
	if hasConfiguredProvider(a.cfg) {
		return
	}
	wasClean := !a.settingsDirty()
	a.cfg = loadProviderKeys(a.cfg)
	a.providerName = activeProviderLabel(a.cfg)
	if wasClean {
		a.form = a.cfg.Clone()
		a.syncFormModelParams()
	}
}

func (a *app) dispatchAction(action string) {
	if action == "silent" {
		return
	}
	if action == "toggle" && a.popup != nil && a.popup.IsVisible() {
		a.requestClosePanel()
		return
	}
	a.ensureProviderKeysLoaded()
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

func (a *app) updatePopup(fn func()) {
	win := a.popup
	if win == nil {
		return
	}
	win.Update(func() {
		if a.quitting {
			return
		}
		fn()
	})
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
	return "未配置翻译服务"
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
	rad := int(panelRadius())
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
	a.ensureProviderKeysLoaded()
	if !a.settingsDirty() {
		a.form = a.cfg.Clone()
		a.syncFormModelParams()
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
	if !a.isSilentLaunch() {
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
				if err := a.updateOpenAtLogin(it.Checked, setPlatformOpenAtLogin); err != nil {
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

//go:embed resources/tray.png
var trayIconPNG []byte

// trayIcon is the Hayami mascot cropped for the Windows notification area.
func trayIcon() []byte {
	return trayIconPNG
}

func timeNow() time.Time { return time.Now() }
