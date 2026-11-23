//go:build linux

package main

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
)

func setPlatformOpenAtLogin(enabled bool) error {
	if err := mygo.App.SetOpenAtLogin(enabled); err != nil {
		return err
	}
	if enabled {
		return patchLinuxAutostartDesktop()
	}
	return nil
}

// migrateLegacyStartup cleans up obsolete autostart and application desktop entries
// from early development builds to prevent duplicate launcher icons, and ensures
// the current autostart entry includes KDE silent-startup metadata.
func migrateLegacyStartup() {
	configDir, err := os.UserConfigDir()
	if err == nil {
		legacyAutostart := filepath.Join(configDir, "autostart", "Hayami-Translator.desktop")
		if _, err := os.Stat(legacyAutostart); err == nil {
			_ = os.Remove(legacyAutostart)
		}
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dataHome = filepath.Join(home, ".local", "share")
		}
	}
	if dataHome != "" {
		appDir := filepath.Join(dataHome, "applications")
		for _, legacy := range []string{"hayami.desktop", "com.hayami.translator.desktop"} {
			aliasPath := filepath.Join(appDir, legacy)
			if info, err := os.Lstat(aliasPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(aliasPath)
			}
		}
	}

	if err := patchLinuxAutostartDesktop(); err != nil {
		log.Print("patching linux autostart desktop entry: ", err)
	}
}

func resolveLinuxDesktopIcon() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		installed := filepath.Join(home, ".local", "hayami-translator.app", "hayami-translator.png")
		if _, err := os.Stat(installed); err == nil {
			return installed
		}
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "hayami-translator.png")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "hayami-translator"
}

// patchLinuxAutostartDesktop augments an existing XDG autostart desktop entry
// with silent login flags and KDE Plasma autostart hints without disturbing
// user-toggled fields such as Hidden=true.
func patchLinuxAutostartDesktop() error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(configDir, "autostart", currentAppIdentifier+".desktop")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}

	updated := updateDesktopEntryContent(string(raw), resolveLinuxDesktopIcon())
	if updated == string(raw) {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func updateDesktopEntryContent(content, icon string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	// Drop a single trailing empty split element so we re-add trailing \n cleanly.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	inDesktopEntry := false
	seenDesktopEntry := false
	insertAt := -1
	var seenComment, seenIcon, seenStartupNotify, seenKDEAfter bool

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inDesktopEntry && insertAt == -1 {
				insertAt = i
			}
			inDesktopEntry = trimmed == "[Desktop Entry]"
			if inDesktopEntry {
				seenDesktopEntry = true
			}
			continue
		}
		if !inDesktopEntry || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Exec":
			v := strings.TrimSpace(val)
			if v != "" && !strings.Contains(v, "--mygo-opened-at-login") && !strings.Contains(v, "--silent") {
				lines[i] = "Exec=" + v + " --mygo-opened-at-login"
			}
		case "Comment":
			if strings.TrimSpace(val) == "" {
				lines[i] = "Comment=轻量桌面划词与截图翻译工具"
			}
			seenComment = true
		case "Icon":
			if strings.TrimSpace(val) == "" && icon != "" {
				lines[i] = "Icon=" + icon
			}
			seenIcon = true
		case "StartupNotify":
			lines[i] = "StartupNotify=false"
			seenStartupNotify = true
		case "X-KDE-autostart-after":
			lines[i] = "X-KDE-autostart-after=panel"
			seenKDEAfter = true
		}
	}

	if !seenDesktopEntry {
		return content
	}

	var extras []string
	if !seenComment {
		extras = append(extras, "Comment=轻量桌面划词与截图翻译工具")
	}
	if !seenIcon && icon != "" {
		extras = append(extras, "Icon="+icon)
	}
	if !seenStartupNotify {
		extras = append(extras, "StartupNotify=false")
	}
	if !seenKDEAfter {
		extras = append(extras, "X-KDE-autostart-after=panel")
	}

	if len(extras) > 0 {
		if insertAt < 0 {
			lines = append(lines, extras...)
		} else {
			out := make([]string, 0, len(lines)+len(extras))
			out = append(out, lines[:insertAt]...)
			out = append(out, extras...)
			out = append(out, lines[insertAt:]...)
			lines = out
		}
	}

	return strings.Join(lines, "\n") + "\n"
}
