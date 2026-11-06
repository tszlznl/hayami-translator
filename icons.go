package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// Windows 11 Vector Icons (SVGs from native design draft)
var (
	iconTranslate  = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M4 5h8M8 3v2M6 5c0 4 3 7 6 8M10 5c0 3-2 6-6 8M13 20l4-10 4 10M14.5 17h5" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconArrowRight = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M5 12h14M13 6l6 6-6 6" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconPin        = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M12 17v5M9 3h6l-1 6 3 3v2H7v-2l3-3-1-6z" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconClose      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M5 5l14 14M19 5L5 19" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconCopy       = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><rect x="9" y="9" width="11" height="11" rx="2" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M5 15V6a2 2 0 0 1 2-2h8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconAudio      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M4 9v6h4l5 4V5L8 9H4zM16.5 8.5a5 5 0 0 1 0 7" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconSettings   = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M4 7h10M18 7h2M4 17h2M10 17h10" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/><circle cx="16" cy="7" r="2" stroke="currentColor" stroke-width="1.6" fill="none"/><circle cx="8" cy="17" r="2" stroke="currentColor" stroke-width="1.6" fill="none"/></svg>`))
	iconSwap       = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M7 4L3 8l4 4M3 8h14M17 12l4 4-4 4M21 16H7" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
	iconGeneral    = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="3" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M12 3v3M12 18v3M3 12h3M18 12h3" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`))
	iconKeyboard   = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><rect x="3" y="6" width="18" height="12" rx="2" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M7 10h.01M11 10h.01M15 10h.01M8 14h8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`))
	iconAppearance = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="8" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M12 4v16" stroke="currentColor" stroke-width="1.6"/></svg>`))
	iconAbout      = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="1.6" fill="none"/><path d="M12 11v5M12 8h.01" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`))
	iconExpand     = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" width="24" height="24" xmlns="http://www.w3.org/2000/svg"><path d="M14 4h6v6M20 4l-8 8M10 6H6a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-4" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`))
)

// speechPlayback tracks the active read-aloud from the moment it starts, not
// from when the speech engine's process starts. Cancelling the context also stops
// a command whose process has not started yet.
type speechPlayback struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

var speech speechPlayback

func (p *speechPlayback) begin(cmd *exec.Cmd, cancel context.CancelFunc) {
	p.mu.Lock()
	previous := p.cancel
	p.cmd = cmd
	p.cancel = cancel
	p.mu.Unlock()
	if previous != nil {
		previous()
	}
}

func (p *speechPlayback) stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cmd = nil
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *speechPlayback) active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancel != nil
}

func (p *speechPlayback) done(cmd *exec.Cmd) {
	p.mu.Lock()
	if p.cmd != cmd {
		p.mu.Unlock()
		return
	}
	p.cmd = nil
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func stopSpeaking() {
	speech.stop()
}

// isSpeaking reports whether a read-aloud is still running.
func isSpeaking() bool {
	return speech.active()
}

// speakResult reads the translation aloud, falling back to the source text
// when no translation has arrived yet.
func speakResult(result, source string) {
	if strings.TrimSpace(result) != "" {
		speakText(result)
		return
	}
	speakText(source)
}

// speakText reads text aloud asynchronously using the platform speech engine.
func speakText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := speechCommand(ctx, text)
	if err != nil {
		cancel()
		log.Printf("朗读失败: %v", err)
		return
	}
	speech.begin(cmd, cancel)

	go func() {
		defer speech.done(cmd)
		if err := cmd.Run(); err != nil && ctx.Err() == nil {
			log.Printf("朗读失败: %v", err)
		}
	}()
}

func speechCommand(ctx context.Context, text string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "linux":
		cmd := exec.CommandContext(ctx, "espeak-ng", "--stdin")
		cmd.Stdin = strings.NewReader(text)
		return cmd, nil
	case "windows":
		// Escape single quotes for PowerShell.
		safe := strings.ReplaceAll(text, "'", "''")
		psScript := fmt.Sprintf("Add-Type -AssemblyName System.Speech; $s = New-Object System.Speech.Synthesis.SpeechSynthesizer; $s.Speak('%s')", safe)
		return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", psScript), nil
	default:
		return nil, fmt.Errorf("当前平台暂不支持朗读")
	}
}
