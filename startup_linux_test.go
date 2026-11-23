//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLegacyStartupLinux(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempHome, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))

	autostartDir := filepath.Join(tempHome, ".config", "autostart")
	if err := os.MkdirAll(autostartDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyAutostart := filepath.Join(autostartDir, "Hayami-Translator.desktop")
	if err := os.WriteFile(legacyAutostart, []byte("[Desktop Entry]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	appDir := filepath.Join(tempHome, ".local", "share", "applications")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(appDir, "hayami-translator.desktop")
	if err := os.WriteFile(targetFile, []byte("[Desktop Entry]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create legacy alias symlinks
	for _, alias := range []string{"hayami.desktop", "com.hayami.translator.desktop"} {
		link := filepath.Join(appDir, alias)
		if err := os.Symlink("hayami-translator.desktop", link); err != nil {
			t.Fatal(err)
		}
	}

	migrateLegacyStartup()

	if _, err := os.Stat(legacyAutostart); !os.IsNotExist(err) {
		t.Errorf("legacy autostart file %s still exists", legacyAutostart)
	}

	for _, alias := range []string{"hayami.desktop", "com.hayami.translator.desktop"} {
		link := filepath.Join(appDir, alias)
		if _, err := os.Lstat(link); !os.IsNotExist(err) {
			t.Errorf("legacy alias symlink %s should have been removed", link)
		}
	}

	if _, err := os.Stat(targetFile); err != nil {
		t.Errorf("canonical target %s should remain: %v", targetFile, err)
	}
}

func TestPatchLinuxAutostartDesktop(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempHome, ".config"))

	// Missing autostart file must be a no-op and not create anything.
	if err := patchLinuxAutostartDesktop(); err != nil {
		t.Fatalf("missing desktop entry returned error: %v", err)
	}
	desktopPath := filepath.Join(tempHome, ".config", "autostart", currentAppIdentifier+".desktop")
	if _, err := os.Stat(desktopPath); !os.IsNotExist(err) {
		t.Fatalf("expected no autostart file when none existed")
	}

	if err := os.MkdirAll(filepath.Dir(desktopPath), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "[Desktop Entry]\nType=Application\nName=Hayami Translator\nExec=/usr/bin/hayami-translator\nTerminal=false\nHidden=true\nX-GNOME-Autostart-enabled=true\n"
	if err := os.WriteFile(desktopPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := patchLinuxAutostartDesktop(); err != nil {
		t.Fatalf("patchLinuxAutostartDesktop failed: %v", err)
	}

	gotBytes, err := os.ReadFile(desktopPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes)
	for _, want := range []string{
		"Exec=/usr/bin/hayami-translator --mygo-opened-at-login",
		"Comment=轻量桌面划词与截图翻译工具",
		"Icon=hayami-translator",
		"StartupNotify=false",
		"X-KDE-autostart-after=panel",
		"Hidden=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("patched desktop entry missing %q:\n%s", want, got)
		}
	}

	// Second run must be idempotent.
	if err := patchLinuxAutostartDesktop(); err != nil {
		t.Fatalf("second patch failed: %v", err)
	}
	got2, _ := os.ReadFile(desktopPath)
	if string(got2) != got {
		t.Fatalf("patching is not idempotent:\n%s\n---\n%s", got, got2)
	}
}
