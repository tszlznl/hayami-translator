package main

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// Windows 11 Native Theme Colors
func win11Theme(t *ui.Theme) (bg, surface, cardBg, border, textPrimary, textSecondary, accent, footerBg ui.Color) {
	if t.Dark {
		return ui.RGB(32, 32, 32),
			ui.RGB(44, 44, 44),
			ui.RGB(40, 40, 40),
			ui.RGB(69, 69, 69),
			ui.RGB(255, 255, 255),
			ui.RGB(197, 197, 197),
			ui.RGB(96, 205, 255),
			ui.RGB(39, 39, 39)
	}
	return ui.RGB(243, 243, 243),
		ui.RGB(252, 252, 252),
		ui.RGB(251, 251, 251),
		ui.RGB(224, 224, 224),
		ui.RGB(26, 26, 26),
		ui.RGB(93, 93, 93),
		ui.RGB(0, 95, 184),
		ui.RGB(246, 246, 246)
}

// Windows 11 Vector Icons (SVGs from native design draft)
var (
	iconTranslate = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M4 5h8M8 3v2M6 5c0 4 3 7 6 8M10 5c0 3-2 6-6 8M13 20l4-10 4 10M14.5 17h5" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconArrowRight = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M5 12h14M13 6l6 6-6 6" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconPin = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M12 17v5M9 3h6l-1 6 3 3v2H7v-2l3-3-1-6z" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconClose = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M5 5l14 14M19 5L5 19" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconCopy = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><rect x="9" y="9" width="11" height="11" rx="2" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M5 15V6a2 2 0 0 1 2-2h8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconAudio = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M4 9v6h4l5 4V5L8 9H4zM16.5 8.5a5 5 0 0 1 0 7" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconSettings = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M4 7h10M18 7h2M4 17h2M10 17h10" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/><circle cx="16" cy="7" r="2" stroke="currentColor" stroke-width="1.6" fill="none"/><circle cx="8" cy="17" r="2" stroke="currentColor" stroke-width="1.6" fill="none"/></svg>`))
	iconSwap = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M7 4L3 8l4 4M3 8h14M17 12l4 4-4 4M21 16H7" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconGeneral = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="3" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M12 3v3M12 18v3M3 12h3M18 12h3" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`))
	iconKeyboard = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><rect x="3" y="6" width="18" height="12" rx="2" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M7 10h.01M11 10h.01M15 10h.01M8 14h8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`))
	iconAppearance = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="8" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M12 4v16" stroke="currentColor" stroke-width="1.6"/></svg>`))
	iconAbout = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M12 11v5M12 8h.01" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`))
	iconExpand = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M14 4h6v6M20 4l-8 8M10 6H6a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-4" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
)

// win11IconButton renders a Windows 11 style 32x32 icon button
func win11IconButton(c *ui.Context, icon *ui.SVG, tooltip string) ui.Element {
	btn := ui.ButtonBase(c).Width(32).Height(32).Radius(4).
		Justify(ui.Center).AlignItems(ui.Center).
		Tooltip(tooltip)
	btn.Children(func() {
		ui.Icon(c, icon)
	})
	return btn
}

var (
	speakLock sync.Mutex
	speakCmd  *exec.Cmd
)

func stopSpeaking() {
	speakLock.Lock()
	defer speakLock.Unlock()
	if speakCmd != nil && speakCmd.Process != nil {
		_ = speakCmd.Process.Kill()
		speakCmd = nil
	}
}

// speakText reads text aloud asynchronously using Windows Speech API
func speakText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	stopSpeaking()

	go func() {
		// Escape single quotes for PowerShell
		safe := strings.ReplaceAll(text, "'", "''")
		psScript := fmt.Sprintf("Add-Type -AssemblyName System.Speech; $s = New-Object System.Speech.Synthesis.SpeechSynthesizer; $s.Speak('%s')", safe)
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", psScript)

		speakLock.Lock()
		speakCmd = cmd
		speakLock.Unlock()

		_ = cmd.Run()

		speakLock.Lock()
		if speakCmd == cmd {
			speakCmd = nil
		}
		speakLock.Unlock()
	}()
}
