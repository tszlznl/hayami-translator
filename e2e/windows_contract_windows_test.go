//go:build e2e && windows

package e2e_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Local AST & source contract helpers (stdlib only)
// ---------------------------------------------------------------------------

func parseRepoGoFile(t *testing.T, relPath string) (*token.FileSet, *ast.File, string) {
	t.Helper()
	abs := filepath.Join("..", relPath)
	raw, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read %s: %v", abs, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, raw, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", abs, err)
	}
	return fset, file, string(raw)
}

func findFuncDecl(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func astContainsCall(node ast.Node, fnName string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == fnName {
				found = true
				return false
			}
		case *ast.SelectorExpr:
			if fun.Sel != nil && fun.Sel.Name == fnName {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func astImportsCGO(f *ast.File) bool {
	for _, imp := range f.Imports {
		if imp.Path != nil && imp.Path.Value == `"C"` {
			return true
		}
	}
	return false
}

func configureWindowsSandboxEnv(s *sandbox) {
	s.ExtraEnv["APPDATA"] = s.ConfigHome
	s.ExtraEnv["LOCALAPPDATA"] = s.CacheHome
}

// ---------------------------------------------------------------------------
// F4 Windows Contract Tests (Two-Tier UIA + Ctrl+C Fallback & Terminal Guard)
// ---------------------------------------------------------------------------

func TestE2E_F4_Win_T1_TwoTierUIAAndClipboardFallbackContract(t *testing.T) {
	// 1. Opaque-box Windows sandbox & CLI pre-lock check with APPDATA/LOCALAPPDATA isolation.
	s := newSandbox(t)
	configureWindowsSandboxEnv(s)
	res := s.runCLI("", 5*time.Second, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit code = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	if _, err := os.Stat(s.debugLogPath()); !os.IsNotExist(err) {
		t.Fatalf("--help created debug.log before lock: %v", err)
	}

	// 2. AST contract: selection_windows.go delegates readSelectedText -> readSelectedTextTwoTier.
	_, selFile, _ := parseRepoGoFile(t, "selection_windows.go")
	if astImportsCGO(selFile) {
		t.Fatalf("selection_windows.go must not import CGO")
	}
	readSel := findFuncDecl(selFile, "readSelectedText")
	if readSel == nil {
		t.Fatalf("selection_windows.go missing readSelectedText")
	}
	var twoTierCall *ast.CallExpr
	ast.Inspect(readSel, func(n ast.Node) bool {
		if twoTierCall != nil || n == nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "readSelectedTextTwoTier" {
			twoTierCall = call
			return false
		}
		return true
	})
	if twoTierCall == nil {
		t.Fatalf("readSelectedText must call readSelectedTextTwoTier(readSelectedTextUIA, readClipboardFallback)")
	}
	if len(twoTierCall.Args) != 2 {
		t.Fatalf("readSelectedTextTwoTier arg count = %d, want 2", len(twoTierCall.Args))
	}
	arg0, ok0 := twoTierCall.Args[0].(*ast.Ident)
	arg1, ok1 := twoTierCall.Args[1].(*ast.Ident)
	if !ok0 || arg0.Name != "readSelectedTextUIA" || !ok1 || arg1.Name != "readClipboardFallback" {
		t.Fatalf("readSelectedTextTwoTier args must be (readSelectedTextUIA, readClipboardFallback), got (%T:%v, %T:%v)",
			twoTierCall.Args[0], twoTierCall.Args[0], twoTierCall.Args[1], twoTierCall.Args[1])
	}

	// 3. AST contract: ui_helpers.go readSelectedTextTwoTier calls readUIA first, readFallback second.
	fsetHelpers, helpersFile, _ := parseRepoGoFile(t, "ui_helpers.go")
	twoTier := findFuncDecl(helpersFile, "readSelectedTextTwoTier")
	if twoTier == nil {
		t.Fatalf("ui_helpers.go missing readSelectedTextTwoTier")
	}
	var uiaPos, fallbackPos token.Pos
	ast.Inspect(twoTier, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			if id.Name == "readUIA" && !uiaPos.IsValid() {
				uiaPos = call.Pos()
			}
			if id.Name == "readFallback" && !fallbackPos.IsValid() {
				fallbackPos = call.Pos()
			}
		}
		return true
	})
	if !uiaPos.IsValid() || !fallbackPos.IsValid() {
		t.Fatalf("readSelectedTextTwoTier must invoke both readUIA and readFallback")
	}
	if fsetHelpers.Position(uiaPos).Offset >= fsetHelpers.Position(fallbackPos).Offset {
		t.Fatalf("readSelectedTextTwoTier must try readUIA before readFallback")
	}

	// 4. AST/source contract: pure-Go COM UIAutomation in selection_uia_windows.go.
	_, uiaFile, uiaSrc := parseRepoGoFile(t, "selection_uia_windows.go")
	if astImportsCGO(uiaFile) {
		t.Fatalf("selection_uia_windows.go must remain pure-Go (zero CGO)")
	}
	for _, tokenStr := range []string{
		"ole32.dll",
		"CoInitializeEx",
		"CoCreateInstance",
		"oleaut32.dll",
		"SysStringLen",
		"SysFreeString",
		"SyscallN",
		"vtUIAGetFocusedElement",
		"vtUIAElemGetCurrentPatternAs",
		"vtTextPatternGetSelection",
		"vtTextRangeGetText",
	} {
		if !strings.Contains(uiaSrc, tokenStr) {
			t.Fatalf("selection_uia_windows.go missing UIA COM symbol %q", tokenStr)
		}
	}
}

func TestE2E_F4_Win_T2_TerminalUIAOnlyAndClipboardRestoreContract(t *testing.T) {
	_, hookFile, hookSrc := parseRepoGoFile(t, "selection_hook_windows.go")
	if astImportsCGO(hookFile) {
		t.Fatalf("selection_hook_windows.go must not import CGO")
	}
	if findFuncDecl(hookFile, "terminalWindowClass") == nil || findFuncDecl(hookFile, "windowClassPolicy") == nil {
		t.Fatalf("selection_hook_windows.go must define terminalWindowClass and windowClassPolicy")
	}
	for _, termClass := range []string{"consolewindowclass", "cascadia_hosting_window_class", "virtualconsoleclass", "mintty", "putty"} {
		if !strings.Contains(hookSrc, termClass) {
			t.Fatalf("terminalWindowClass missing terminal class %q", termClass)
		}
	}

	captureFn := findFuncDecl(hookFile, "captureToolbarSelection")
	if captureFn == nil {
		t.Fatalf("selection_hook_windows.go missing captureToolbarSelection")
	}
	if !astContainsCall(captureFn, "readSelectedTextUIA") || !astContainsCall(captureFn, "readSelectedText") {
		t.Fatalf("captureToolbarSelection must dispatch uiaOnly -> readSelectedTextUIA and non-terminal -> readSelectedText")
	}
	var sawUiaOnlyIf bool
	ast.Inspect(captureFn, func(n ast.Node) bool {
		if stmt, ok := n.(*ast.IfStmt); ok {
			if id, ok := stmt.Cond.(*ast.Ident); ok && id.Name == "uiaOnly" {
				if astContainsCall(stmt.Body, "readSelectedTextUIA") &&
					!astContainsCall(stmt.Body, "readSelectedText") &&
					stmt.Else != nil &&
					astContainsCall(stmt.Else, "readSelectedText") {
					sawUiaOnlyIf = true
				}
			}
		}
		return true
	})
	if !sawUiaOnlyIf {
		t.Fatalf("captureToolbarSelection must strictly guard Ctrl+C fallback with `if uiaOnly { readSelectedTextUIA() } else { readSelectedText() }`")
	}

	// Verify clipboard snapshot & lossless restore contract in selection_windows.go.
	_, selFile, selSrc := parseRepoGoFile(t, "selection_windows.go")
	fallbackFn := findFuncDecl(selFile, "readClipboardFallback")
	if fallbackFn == nil {
		t.Fatalf("selection_windows.go missing readClipboardFallback")
	}
	if !astContainsCall(fallbackFn, "readSelectedTextWith") {
		t.Fatalf("readClipboardFallback must use readSelectedTextWith(saveClipboard, copySelectedText)")
	}
	if !strings.Contains(selSrc, "selectionReadMu.Lock()") || !strings.Contains(selSrc, "errClipboardSnapshot") {
		t.Fatalf("selection_windows.go missing selectionReadMu serialization or errClipboardSnapshot abort guard")
	}
	copyFn := findFuncDecl(selFile, "copySelectedText")
	if copyFn == nil || !astContainsCall(copyFn, "restoreClipboard") || !astContainsCall(copyFn, "sendCtrlC") {
		t.Fatalf("copySelectedText must pair sendCtrlC with deferred restoreClipboard")
	}
}

// ---------------------------------------------------------------------------
// F12 Windows Contract Tests (In-Process COM SAPI.SpVoice & Zero PowerShell)
// ---------------------------------------------------------------------------

func TestE2E_F12_Win_T1_InProcessSAPISpVoiceContract(t *testing.T) {
	// 1. Scan production Windows & shared speech Go files for zero powershell(.exe) / System.Speech.
	var windowsSpeechSources []string
	rootFiles, err := filepath.Glob(filepath.Join("..", "*.go"))
	if err != nil {
		t.Fatalf("glob root go files: %v", err)
	}
	var ttsFiles []string
	ttsDir := filepath.Join("..", "internal", "tts")
	if info, statErr := os.Stat(ttsDir); statErr == nil && info.IsDir() {
		if walkErr := filepath.WalkDir(ttsDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
				ttsFiles = append(ttsFiles, path)
			}
			return nil
		}); walkErr != nil {
			t.Fatalf("walk internal/tts go files: %v", walkErr)
		}
	}

	type sourceEntry struct {
		path    string
		inTTPkg bool
	}
	var entries []sourceEntry
	for _, p := range rootFiles {
		entries = append(entries, sourceEntry{path: p, inTTPkg: false})
	}
	for _, p := range ttsFiles {
		entries = append(entries, sourceEntry{path: p, inTTPkg: true})
	}

	for _, ent := range entries {
		p := ent.path
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		src := string(raw)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, raw, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		if astImportsCGO(f) {
			t.Fatalf("%s must not import CGO", p)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			unquoted, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			lower := strings.ToLower(strings.TrimSpace(unquoted))
			if strings.Contains(lower, "powershell.exe") ||
				lower == "powershell" ||
				strings.HasPrefix(lower, "powershell ") ||
				strings.Contains(lower, "system.speech") {
				t.Fatalf("production speech code in %s still references %q (expected in-process COM SAPI.SpVoice)", p, unquoted)
			}
			return true
		})
		base := filepath.Base(p)
		if ent.inTTPkg || strings.Contains(base, "speech") || strings.Contains(base, "tts") || base == "icons.go" {
			windowsSpeechSources = append(windowsSpeechSources, src)
		}
	}

	joined := strings.Join(windowsSpeechSources, "\n")
	if !strings.Contains(joined, "SAPI.SpVoice") && !strings.Contains(joined, "SpVoice") {
		t.Fatalf("expected in-process COM SAPI.SpVoice implementation in Windows TTS sources")
	}
	if !strings.Contains(joined, "SVSFlagsAsync") || !strings.Contains(joined, "SVSFPurgeBeforeSpeak") {
		t.Fatalf("expected SVSFlagsAsync and SVSFPurgeBeforeSpeak flags in Windows SAPI.SpVoice TTS")
	}

	// 2. Opaque-box PATH sentinel guard when running live on Windows GUI session.
	if os.Getenv("HAYAMI_E2E_WINDOWS_GUI") == "1" {
		s := newSandbox(t)
		configureWindowsSandboxEnv(s)
		m := newMockLLMServer(t)
		m.SetDefault("chat", MockResponse{Body: "Windows SAPI spoken"})

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		// Install a fake powershell.bat/.cmd sentinel on PATH; if invoked it logs to powershell.log.
		s.IsolatePATH = true
		psLog := filepath.Join(s.ShimLogDir, "powershell.log")
		bat := "@echo invoked %* >> \"" + psLog + "\"\r\n@exit /b 1\r\n"
		for _, name := range []string{"powershell.bat", "powershell.cmd"} {
			if err := os.WriteFile(filepath.Join(s.BinDir, name), []byte(bat), 0o755); err != nil {
				t.Fatalf("write %s sentinel: %v", name, err)
			}
		}

		proc := s.startGUI()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		client, err := DialAutomation(ctx, proc.SocketPath, proc.Done)
		if err != nil {
			t.Fatalf("DialAutomation: %v", err)
		}
		defer client.Close()

		win, err := client.WaitWindow(ctx, func(w WindowInfo) bool {
			return w.Title == "Hayami Translator"
		})
		if err != nil {
			t.Fatalf("WaitWindow: %v", err)
		}

		editor, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
			return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
		})
		if err != nil {
			t.Fatalf("WaitNode popup editor: %v", err)
		}
		if err := client.Act(ctx, ActParams{Window: win.ID, ID: editor.ID, Action: "setValue", Text: "Speak SAPI"}); err != nil {
			t.Fatalf("setValue popup editor: %v", err)
		}
		btn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
			return n.Role == "button" && n.Label == "翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
		})
		if err != nil {
			t.Fatalf("WaitNode footer translate button: %v", err)
		}
		if err := client.Act(ctx, ActParams{Window: win.ID, ID: btn.ID, Action: "press"}); err != nil {
			t.Fatalf("press translate button: %v", err)
		}
		if _, err := client.WaitTree(ctx, win.ID, func(tr TreeResult) bool {
			for _, n := range tr.Nodes {
				if strings.Contains(n.Label, "Windows SAPI spoken") || strings.Contains(n.Value, "Windows SAPI spoken") {
					return true
				}
			}
			return false
		}); err != nil {
			t.Fatalf("WaitTree Windows SAPI spoken: %v", err)
		}
		audioBtn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
			return n.Role == "button" && (strings.Contains(n.Description, "朗读") || strings.Contains(n.Label, "朗读"))
		})
		if err != nil {
			t.Fatalf("WaitNode audio button: %v", err)
		}
		if err := client.Act(ctx, ActParams{Window: win.ID, ID: audioBtn.ID, Action: "press"}); err != nil {
			t.Fatalf("press audio button: %v", err)
		}
		if err := client.WaitIdle(ctx, win.ID); err != nil {
			t.Fatalf("WaitIdle after audio press: %v", err)
		}

		if logData, err := os.ReadFile(psLog); err == nil && len(logData) > 0 {
			t.Fatalf("powershell sentinel was invoked during Windows TTS: %s", string(logData))
		}
	}
}

func TestE2E_F12_Win_T2_NoPowerShellChildProcessAndCancelContract(t *testing.T) {
	// Verify OnWillQuit calls stopSpeaking() so quitting cancels active speech immediately.
	_, mainFile, _ := parseRepoGoFile(t, "main.go")
	readyFn := findFuncDecl(mainFile, "ready")
	if readyFn == nil || !astContainsCall(readyFn, "stopSpeaking") {
		t.Fatalf("main.go ready()/OnWillQuit must invoke stopSpeaking()")
	}

	// Verify Windows speech implementation does not use os/exec to spawn child processes
	// and implements async interruption via SVSFPurgeBeforeSpeak / Stop().
	_, iconsFile, iconsSrc := parseRepoGoFile(t, "icons.go")
	if strings.Contains(iconsSrc, "powershell.exe") || strings.Contains(iconsSrc, "windowsSpeechScript") {
		t.Fatalf("icons.go must not retain legacy powershell.exe windowsSpeechScript")
	}
	if winCmd := findFuncDecl(iconsFile, "windowsSpeechCommand"); winCmd != nil {
		if astContainsCall(winCmd, "CommandContext") || astContainsCall(winCmd, "Command") {
			t.Fatalf("windowsSpeechCommand must not spawn child processes via os/exec")
		}
	}

	var hasCancelContract bool
	for _, candidate := range []string{"tts_windows.go", "speech_windows.go", filepath.Join("internal", "tts", "sapi_windows.go")} {
		abs := filepath.Join("..", candidate)
		raw, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		src := string(raw)
		if strings.Contains(src, "SVSFPurgeBeforeSpeak") && (strings.Contains(src, "Stop()") || strings.Contains(src, "stop")) {
			hasCancelContract = true
		}
	}
	if !hasCancelContract {
		t.Fatalf("expected Windows TTS implementation with SVSFPurgeBeforeSpeak async Stop() cancellation contract")
	}
}
