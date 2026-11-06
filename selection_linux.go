//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func readSelectedText() (string, error) {
	return readSelectedTextContext(context.Background())
}

func readSelectedTextContext(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// PRIMARY is independent of the regular clipboard. Never fall back to it.
	cmd := exec.CommandContext(ctx, "wl-paste", "--primary", "--no-newline", "--type", "text")
	output, err := cmd.Output()
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
