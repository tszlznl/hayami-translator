//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type selectionCmdRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type selectionEnv struct {
	getenv   func(string) string
	lookPath func(string) (string, error)
	runCmd   selectionCmdRunner
}

func defaultSelectionCmdRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 200 * time.Millisecond
	return cmd.Output()
}

func defaultSelectionEnv() selectionEnv {
	return selectionEnv{
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		runCmd:   defaultSelectionCmdRunner,
	}
}

func (e selectionEnv) withDefaults() selectionEnv {
	if e.getenv == nil {
		e.getenv = os.Getenv
	}
	if e.lookPath == nil {
		e.lookPath = exec.LookPath
	}
	if e.runCmd == nil {
		e.runCmd = defaultSelectionCmdRunner
	}
	return e
}

func readSelectedText() (string, error) {
	return readSelectedTextContext(context.Background())
}

func useWaylandSelection() bool {
	return useWaylandSelectionWith(defaultSelectionEnv())
}

func useWaylandSelectionWith(env selectionEnv) bool {
	env = env.withDefaults()
	if env.getenv("WAYLAND_DISPLAY") != "" {
		return true
	}
	if env.getenv("DISPLAY") != "" {
		// In an X11 session, prefer xclip/xsel even if wl-paste is installed.
		if _, err := env.lookPath("xclip"); err == nil {
			return false
		}
		if _, err := env.lookPath("xsel"); err == nil {
			return false
		}
	}
	return true
}

func readSelectedTextContext(ctx context.Context) (string, error) {
	return readSelectedTextWithEnv(ctx, defaultSelectionEnv())
}

func readSelectedTextWithEnv(ctx context.Context, env selectionEnv) (string, error) {
	env = env.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if useWaylandSelectionWith(env) {
		if _, err := env.lookPath("wl-paste"); err == nil {
			return readWaylandPrimaryWith(ctx, env)
		}
		if env.getenv("DISPLAY") == "" {
			return readWaylandPrimaryWith(ctx, env)
		}
	}
	return readX11PrimaryWith(ctx, env)
}

func readWaylandPrimary(ctx context.Context) (string, error) {
	return readWaylandPrimaryWith(ctx, defaultSelectionEnv())
}

func readWaylandPrimaryWith(ctx context.Context, env selectionEnv) (string, error) {
	env = env.withDefaults()
	// PRIMARY is independent of the regular clipboard. Never fall back to it.
	output, err := env.runCmd(ctx, "wl-paste", "--primary", "--no-newline", "--type", "text")
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := strings.TrimSpace(string(exitErr.Stderr))
			if message == "Nothing is copied" || strings.HasPrefix(message, "Clipboard content is not available as text") {
				return "", nil
			}
		}
		return "", fmt.Errorf("读取 Wayland PRIMARY 选区失败: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func readX11Primary(ctx context.Context) (string, error) {
	return readX11PrimaryWith(ctx, defaultSelectionEnv())
}

func readX11PrimaryWith(ctx context.Context, env selectionEnv) (string, error) {
	env = env.withDefaults()
	if _, err := env.lookPath("xclip"); err == nil {
		output, err := env.runCmd(ctx, "xclip", "-o", "-selection", "primary")
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				msg := strings.ToLower(strings.TrimSpace(string(exitErr.Stderr)))
				if msg == "" || strings.Contains(msg, "not available") || strings.Contains(msg, "convert") {
					return "", nil
				}
			}
			return "", fmt.Errorf("读取 X11 PRIMARY 选区失败: %w", err)
		}
		return strings.TrimSpace(string(output)), nil
	}
	if _, err := env.lookPath("xsel"); err == nil {
		output, err := env.runCmd(ctx, "xsel", "-o", "-p")
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && strings.TrimSpace(string(exitErr.Stderr)) == "" {
				return "", nil
			}
			return "", fmt.Errorf("读取 X11 PRIMARY 选区失败: %w", err)
		}
		return strings.TrimSpace(string(output)), nil
	}
	return "", fmt.Errorf("读取选区失败：请安装 wl-paste（Wayland）或 xclip/xsel（X11）")
}

// Suppressed samples update the baseline to avoid replaying stale selections.
type selectionDebouncer struct {
	initialized bool
	baseline    string
	candidate   string
}

func (d *selectionDebouncer) sample(text string, allowed bool, err error) bool {
	if err != nil {
		d.candidate = ""
		return false
	}
	if !d.initialized || !allowed || text == "" {
		d.initialized = true
		d.baseline = text
		d.candidate = ""
		return false
	}
	if text == d.baseline {
		d.candidate = ""
		return false
	}
	if text != d.candidate {
		d.candidate = text
		return false
	}
	d.baseline = text
	d.candidate = ""
	return true
}
