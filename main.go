package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

type app struct {
	cfg          Config
	providerName string
	store        *historyStore

	popup      *mygo.Window
	settings   *mygo.Window
	historyWin *mygo.Window
	toolbar    *mygo.Window
	inputWin   *mygo.Window
	tray       *mygo.Tray
	hotkeys    []string
	hotkeyErr  string

	// Drag-selection toolbar. The hook captures the text before the panel is
	// shown; clicking the panel would otherwise move the foreground and make
	// a second Ctrl+C target the panel itself.
	triggerMode     atomic.Pointer[string]
	bubbleReqID     int
	bubbleCancel    context.CancelFunc
	toolbarText     string
	toolbarPoint    mygo.Point
	toolbarHadFocus bool
	toolbarShowID   int
	toolbarEnabled  bool
	toolbarIsBubble bool
	toolbarWord     string
	toolbarPhonetic string
	toolbarPos      string
	toolbarDef      string

	// Translation shown by the popup.
	source        string
	result        string
	results       []providerResult
	multiEngine   bool
	quitting      bool
	errText       string
	status        string
	streaming     bool
	hasImage      bool
	shot          []byte
	targetLang    string
	reqID         int
	cancel        context.CancelFunc
	shotCancel    context.CancelFunc
	md            markdown.Options
	popupScroll   ui.ScrollState
	focusInput    bool
	popupHadFocus bool
	popupPinned   bool
	inputHandle   ui.Handle

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
	form                Config
	openAtLogin         bool
	settingsStatus      string
	providerDeleteArmed string
	settingsTab         int
	settingsScroll      ui.ScrollState

	// History window.
	hist        []historyEntry
	histQuery   string
	histStatus  string
	histReqID   int
	histList    ui.ListState
	historyOpen bool
	clearArmed  bool
}

func main() {
	appData := os.Getenv("APPDATA")
	if appData != "" {
		logDir := filepath.Join(appData, "Hayami Translator")
		_ = os.MkdirAll(logDir, 0o755)
		f, err := os.OpenFile(filepath.Join(logDir, "debug.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
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
	mygo.App.SetName("Hayami Translator")
	a := &app{}
	mygo.App.OnWindowAllClosed(func() {}) // a tray app: closing a window does not quit
	mygo.App.OnSecondInstance(func([]string, string) {
		log.Println("[debug] OnSecondInstance received")
		if !hasConfiguredProvider(a.cfg) {
			a.showSettings()
		} else {
			a.showCenterPopup(true)
		}
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
	a.form = a.cfg.clone()
	a.providerName = activeProviderLabel(a.cfg)
	a.targetLang = a.cfg.TargetLang
	a.multiEngine = a.cfg.MultiEngine
	a.toolbarEnabled = !a.cfg.DisableSelectionToolbar
	a.setTriggerMode(a.cfg.TriggerMode)
	a.popupPinned = a.cfg.PopupPinned
	a.openAtLogin = mygo.App.OpenAtLogin()
	a.md = markdown.Options{Theme: &markdown.Theme{FontSize: 14}}

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
		if a.bubbleCancel != nil {
			a.bubbleCancel()
			a.bubbleCancel = nil
		}
		stopSpeaking()
		if a.tray != nil {
			a.tray.Destroy()
		}
		a.store.close()
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

	configured := hasConfiguredProvider(a.cfg)
	openedAtLogin := mygo.App.WasOpenedAtLogin()
	log.Printf("[debug] configured=%v, openedAtLogin=%v", configured, openedAtLogin)
	if !configured {
		log.Println("[debug] showing settings because no configured provider")
		a.showSettings()
	} else if !openedAtLogin {
		log.Println("[debug] showing centered popup on launch")
		a.showCenterPopup(true)
	}
	log.Println("[debug] ready() finished")
}

func (a *app) getTriggerMode() string {
	if p := a.triggerMode.Load(); p != nil {
		return *p
	}
	if a.cfg.TriggerMode != "" {
		return a.cfg.TriggerMode
	}
	return "显示图标，点击翻译"
}

func (a *app) setTriggerMode(m string) {
	if m == "" {
		m = "显示图标，点击翻译"
	}
	a.triggerMode.Store(&m)
}

func (a *app) inputHotkeyLabel() string {
	h := strings.TrimSpace(a.cfg.InputHotkey)
	if h == "" {
		return "Ctrl+Alt+I"
	}
	return h
}

func cleanSpeechText(s string) string {
	lines := strings.Split(s, "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "---") {
			continue
		}
		if trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, " ")
}

func (a *app) createWindows() {
	a.popup = mygo.NewWindow(mygo.WindowOptions{
		Title:         "Hayami Translator",
		Hidden:        true,
		Frameless:     true,
		AlwaysOnTop:   true,
		DisableResize: false,
		Width:         380,
		Height:        260,
		MinWidth:      320,
		MinHeight:     180,
		Content:       ui.View(a.popupView),
	})
	a.popup.OnClose(func(e *mygo.CloseEvent) {
		a.handleWindowClose(e, a.popup.Hide)
	})
	a.popup.OnFocus(func() {
		log.Println("[debug] popup.OnFocus()")
		a.popupHadFocus = true
	})
	a.popup.OnBlur(func() {
		log.Printf("[debug] popup.OnBlur(), popupHadFocus=%v, popupPinned=%v", a.popupHadFocus, a.popupPinned)
		// A panel that could not take the foreground (for example when a
		// second instance asks for it) gets a blur without a focus first.
		// Keep it visible so the user can click it instead of losing it.
		if a.popupHadFocus {
			a.popupHadFocus = false
			if !a.popupPinned {
				a.popup.Hide()
			}
		}
	})

	a.settings = mygo.NewWindow(mygo.WindowOptions{
		Title:     "设置 · Hayami Translator",
		Hidden:    true,
		Width:     880,
		Height:    560,
		MinWidth:  640,
		MinHeight: 480,
		Content:   ui.View(a.settingsView),
	})
	a.settings.OnClose(func(e *mygo.CloseEvent) {
		a.handleWindowClose(e, a.settings.Hide)
	})

	a.inputWin = mygo.NewWindow(mygo.WindowOptions{
		Title:     "输入翻译 · Hayami Translator",
		Hidden:    true,
		Width:     880,
		Height:    520,
		MinWidth:  640,
		MinHeight: 400,
		Content:   ui.View(a.inputWindowView),
	})
	a.inputWin.OnClose(func(e *mygo.CloseEvent) {
		a.handleWindowClose(e, a.inputWin.Hide)
	})

	a.historyWin = mygo.NewWindow(mygo.WindowOptions{
		Title:     "历史记录 · Hayami Translator",
		Hidden:    true,
		Width:     720,
		Height:    620,
		MinWidth:  480,
		MinHeight: 360,
		Content:   ui.View(a.historyView),
	})
	a.historyWin.OnClose(func(e *mygo.CloseEvent) {
		a.handleWindowClose(e, a.historyWin.Hide)
	})

	a.toolbar = mygo.NewWindow(mygo.WindowOptions{
		Title:         "划词助手",
		Panel:         true,
		Hidden:        true,
		Frameless:     true,
		DisableResize: true,
		Width:         34,
		Height:        34,
		Content:       ui.View(a.toolbarView),
	})
	a.toolbar.OnClose(func(e *mygo.CloseEvent) {
		a.handleWindowClose(e, a.hideSelectionToolbar)
	})
	a.toolbar.OnFocus(func() { a.toolbarHadFocus = true })
	a.toolbar.OnBlur(func() {
		if a.toolbarHadFocus {
			a.toolbarHadFocus = false
			a.hideSelectionToolbar()
		}
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

// showCenterPopup places the window in the center of the display and optionally pins it.
func (a *app) showCenterPopup(pinned bool) {
	if a.popup == nil {
		return
	}
	a.popup.Center()
	if pinned {
		a.popupPinned = true
	}
	a.showPopup()
}

// showPopupAtCursor places the panel next to the pointer, inside the work
// area of the display the pointer is on.
func (a *app) showPopupAtCursor() {
	if a.popup == nil {
		log.Println("[debug] showPopupAtCursor: a.popup is nil!")
		return
	}
	p := mygo.Screen.CursorScreenPoint()
	log.Printf("[debug] showPopupAtCursor at %v", p)
	a.placePopup(p)
	a.showPopup()
}

// showPopup shows the panel and asks for the keyboard. Windows may refuse
// SetForegroundWindow to a process the user is not in, in which case the
// panel stays visible but inactive until it is clicked.
func (a *app) showPopup() {
	a.popupHadFocus = a.popup.IsFocused()
	log.Printf("[debug] showPopup: isFocused before show=%v", a.popupHadFocus)
	a.popup.Show()
	a.popup.Focus()
	log.Printf("[debug] showPopup: isVisible after show=%v", a.popup.IsVisible())
}

func (a *app) hidePopup() {
	a.popupHadFocus = false
	a.popupPinned = a.cfg.PopupPinned
	a.popup.Hide()
}

func (a *app) placePopup(p mygo.Point) {
	d := mygo.Screen.DisplayNearestPoint(p)
	w, h := a.popup.Size()
	x, y := p.X+14, p.Y+18
	if x+w > d.WorkArea.X+d.WorkArea.Width {
		x = p.X - w - 14
	}
	if y+h > d.WorkArea.Y+d.WorkArea.Height {
		y = p.Y - h - 18
	}
	x = max(d.WorkArea.X, x)
	y = max(d.WorkArea.Y, y)
	a.popup.SetPosition(x, y)
}

func (a *app) refreshSettingsWindow() {
	if a.settings != nil && a.settings.IsVisible() {
		a.settings.Update(func() {})
	}
}

func (a *app) showSettings() {
	a.form = a.cfg.clone()
	if a.form.TriggerMode == "" {
		a.form.TriggerMode = "显示图标，点击翻译"
	}
	a.toolbarEnabled = !a.cfg.DisableSelectionToolbar
	a.setTriggerMode(a.cfg.TriggerMode)
	a.popupPinned = a.cfg.PopupPinned
	a.openAtLogin = mygo.App.OpenAtLogin()
	a.providerDeleteArmed = ""
	a.settings.Center()
	a.settings.Show()
	a.settings.Focus()
}

func (a *app) showHistory() {
	a.clearArmed = false
	a.historyWin.Center()
	a.historyWin.Show()
	a.historyWin.Focus()
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
	toolbarStatus := "已开启"
	if !a.toolbarEnabled {
		toolbarStatus = "已关闭"
	}
	a.tray.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: "划词翻译 (" + toolbarStatus + ")", Click: func(*mygo.MenuItem, *mygo.Window) {
			old := a.toolbarEnabled
			a.toolbarEnabled = !old
			a.cfg.DisableSelectionToolbar = !a.toolbarEnabled
			if err := a.cfg.save(); err != nil {
				log.Print("saving toolbar setting: ", err)
				a.toolbarEnabled = old
				a.cfg.DisableSelectionToolbar = !old
				a.refreshTrayMenu()
				return
			}
			a.form.DisableSelectionToolbar = !a.toolbarEnabled
			setSelectionToolbarEnabled(a.toolbarEnabled)
			if !a.toolbarEnabled {
				a.hideSelectionToolbar()
			}
			a.refreshTrayMenu()
			a.refreshSettingsWindow()
		}},
		{Label: "翻译选中文本", Accelerator: a.cfg.Hotkey, Click: func(*mygo.MenuItem, *mygo.Window) { a.translateSelection() }},
		{Label: "输入翻译…", Accelerator: a.inputHotkeyLabel(), Click: func(*mygo.MenuItem, *mygo.Window) { a.showInputWindow() }},
		{Label: "截图翻译", Accelerator: a.cfg.ScreenshotHotkey, Click: func(*mygo.MenuItem, *mygo.Window) { a.translateScreenshot() }},
		mygo.Separator(),
		{Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) { a.showSettings() }},
		{Label: "历史记录…", Click: func(*mygo.MenuItem, *mygo.Window) { a.showHistory() }},
		mygo.Separator(),
		{Role: mygo.RoleQuit, Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) { a.quit() }},
	}))
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
	p := mygo.Screen.CursorScreenPoint()
	if a.popup.IsVisible() {
		a.hidePopup()
	}
	go func() {
		text, err := readSelectedText()
		a.popup.Update(func() {
			a.resetTranslation()
			a.source = text
			a.hasImage = false
			a.shot = nil
			switch {
			case err != nil:
				a.placePopup(p)
				a.showPopup()
				a.status = err.Error()
				a.focusInput = true
			case strings.TrimSpace(text) == "":
				a.showCenterPopup(true)
				a.status = "输入文本后按 Ctrl+Enter 翻译"
				a.focusInput = true
			default:
				a.placePopup(p)
				a.showPopup()
				a.startTranslation()
			}
		})
	}()
}

// showTranslationAt translates text that was already read, so the drag
// toolbar does not need to send a second Ctrl+C after it has taken focus.
func (a *app) showTranslationAt(text string, p mygo.Point) {
	if a.popup == nil {
		return
	}
	a.resetTranslation()
	a.source = text
	a.hasImage = false
	a.shot = nil
	a.placePopup(p)
	a.showPopup()
	if strings.TrimSpace(text) == "" {
		a.status = "没有读到选中文本，直接输入后点翻译"
		a.focusInput = true
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
	ctx, cancel := context.WithCancel(context.Background())
	a.shotCancel = cancel

	before := clipboardSeq()
	startSnipping()
	go func() {
		img := waitClipboardImage(ctx, before, 60*time.Second)
		if img == nil || ctx.Err() != nil {
			return
		}
		p := mygo.Screen.CursorScreenPoint()
		a.popup.Update(func() {
			a.resetTranslation()
			a.hasImage = true
			a.shot = img
			a.source = ""
			a.placePopup(p)
			a.showPopup()
			a.startTranslation()
		})
	}()
}

func (a *app) resetTranslation() {
	a.cancelStream()
	a.source = ""
	a.result = ""
	a.results = nil
	a.errText = ""
	a.status = ""
	a.hasImage = false
	a.shot = nil
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
			a.status = "请输入要翻译的文本"
			return
		}
		cfg.TargetLang = effectiveTarget(cfg.TargetLang, sourceText)
		messages = textMessages(cfg, sourceText)
		mode = "text"
	}

	providers := cfg.enabledProviders(a.hasImage)
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
	a.status = ""
	a.focusInput = false

	if len(providers) == 0 {
		if a.hasImage {
			a.errText = "截图翻译需要启用一个配置好的 OpenAI Compatible 视觉模型"
		} else {
			a.errText = "没有可用的翻译服务，请在设置里填写 Key"
		}
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
								a.results[idx].ErrText = err.Error()
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
				if a.cfg.AutoSpeak {
					speakText(cleanSpeechText(result))
				}
				entry := historyEntry{
					CreatedAt: timeNow(),
					Source:    sourceText,
					Result:    result,
					Target:    cfg.TargetLang,
					Mode:      mode,
				}
				go func() {
					if err := a.store.add(entry); err != nil {
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
		var lastErr error

		for _, p := range providers {
			if ctx.Err() != nil {
				break
			}
			key := p.Key
			if !providerConfigured(p, key) {
				lastErr = fmt.Errorf("%s 未配置完整", p.Name)
				continue
			}
			a.popup.Update(func() {
				if a.reqID == id {
					a.providerName = providerLabel(p)
				}
			})
			pending.Reset()
			emitted := false
			err := translateWithProvider(ctx, cfg, p, key, messages, sourceText, func(delta string) {
				emitted = true
				pending.WriteString(delta)
				// Repaint at most every 30ms instead of once per token.
				if time.Since(last) >= 30*time.Millisecond {
					flush()
					last = time.Now()
				}
			})
			flush()
			if err == nil {
				lastErr = nil
				break
			}
			if ctx.Err() != nil {
				lastErr = ctx.Err()
				break
			}
			lastErr = err
			if emitted {
				// A service that already produced text is not silently
				// replaced by another one; keep the partial result.
				break
			}
			a.popup.Update(func() {
				if a.reqID == id {
					a.result = ""
				}
			})
		}

		a.popup.Update(func() {
			if a.reqID != id {
				return
			}
			a.streaming = false
			if lastErr != nil && ctx.Err() == nil {
				if strings.TrimSpace(a.result) == "" {
					a.errText = lastErr.Error()
				} else {
					a.status = "部分失败：" + lastErr.Error()
				}
			}
			result := a.result
			if strings.TrimSpace(result) == "" {
				return
			}
			if a.cfg.AutoSpeak {
				speakText(cleanSpeechText(result))
			}
			entry := historyEntry{
				CreatedAt: timeNow(),
				Source:    sourceText,
				Result:    result,
				Target:    cfg.TargetLang,
				Mode:      mode,
			}
			go func() {
				if err := a.store.add(entry); err != nil {
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
	f := a.form.clone()
	f.fillDefaults()
	f.normalizeProviders()
	f.BaseURL = strings.TrimSpace(f.BaseURL)
	f.Model = strings.TrimSpace(f.Model)
	f.Hotkey = strings.TrimSpace(f.Hotkey)
	f.ScreenshotHotkey = strings.TrimSpace(f.ScreenshotHotkey)
	f.InputHotkey = strings.TrimSpace(f.InputHotkey)
	f.DisableSelectionToolbar = !a.toolbarEnabled
	// Save keys first; if keyring fails, abort without overwriting config file
	if err := saveProviderKeys(f); err != nil {
		a.settingsStatus = "保存失败，密钥写入凭据管理器失败：" + err.Error()
		return
	}
	if err := f.save(); err != nil {
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
	a.form = f.clone()
	a.setTriggerMode(f.TriggerMode)
	a.popupPinned = f.PopupPinned
	a.providerDeleteArmed = ""
	a.providerName = activeProviderLabel(f)
	a.targetLang = f.TargetLang
	a.multiEngine = f.MultiEngine
	setSelectionToolbarEnabled(a.toolbarEnabled)
	if !a.toolbarEnabled {
		a.hideSelectionToolbar()
	}
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
		entries, err := a.store.search(q, 200)
		a.historyWin.Update(func() {
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
	if a.historyWin == nil || !a.historyWin.IsVisible() {
		return
	}
	a.historyWin.Update(func() { a.reloadHistory() })
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
	a.hasImage = false
	if a.historyWin != nil {
		a.historyWin.Hide()
	}
	p := mygo.Screen.CursorScreenPoint()
	a.placePopup(p)
	a.showPopup()
}

//go:embed resources/tray.png
var trayIconPNG []byte

// trayIcon is the Hayami mascot cropped for the Windows notification area.
func trayIcon() []byte {
	return trayIconPNG
}

func timeNow() time.Time { return time.Now() }
