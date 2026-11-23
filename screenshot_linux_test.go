//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func disableLinuxPortalForTest(t *testing.T) {
	t.Helper()
	orig := portalScreenshotFunc
	portalScreenshotFunc = func(context.Context) ([]byte, error) {
		return nil, errors.New("portal disabled in test")
	}
	t.Cleanup(func() {
		portalScreenshotFunc = orig
	})
}

func TestLinuxCaptureScreenshot(t *testing.T) {
	disableLinuxPortalForTest(t)
	for _, test := range []struct {
		name    string
		script  string
		want    string
		wantErr bool
		cancel  bool
	}{
		{"image", `printf '\211PNG\r\n\032\nfixture' > "$6"`, "\x89PNG\r\n\x1a\nfixture", false, false},
		{"user canceled", "exit 0", "", true, true},
		{"command failed", "exit 42", "", true, false},
		{"failed after writing", `printf 'partial' > "$6"; exit 42`, "", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputDir := filepath.Join(t.TempDir(), "screenshots")
			if err := os.Mkdir(outputDir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", outputDir)
			// Unrelated files must survive screenshot cleanup.
			keep := filepath.Join(outputDir, "keep.png")
			if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			installLinuxTestCommand(t, "spectacle", `
[ "$#" -eq 6 ] && [ "$1" = --region ] && [ "$2" = --background ] && [ "$3" = --nonotify ] && [ "$4" = --new-instance ] && [ "$5" = --output ] || exit 9
case "$6" in "$TMPDIR"/hayami-screenshot-*.png) ;; *) exit 9 ;; esac
`+test.script)
			image, err := captureScreenshot(context.Background())
			if string(image) != test.want || (err != nil) != test.wantErr || errors.Is(err, context.Canceled) != test.cancel {
				t.Fatalf("image = %q, error = %v", image, err)
			}
			entries, err := os.ReadDir(outputDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "keep.png" {
				t.Fatalf("temporary screenshot leaked or unrelated file removed: %v", entries)
			}
		})
	}
}

func TestLinuxCaptureScreenshotContextAndMissingCommand(t *testing.T) {
	disableLinuxPortalForTest(t)
	for _, test := range []struct {
		name string
		err  error
	}{
		{"already canceled", context.Canceled},
		{"deadline", context.DeadlineExceeded},
		{"missing command", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputDir := filepath.Join(t.TempDir(), "screenshots")
			if err := os.Mkdir(outputDir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", outputDir)
			// A shell builtin blocks until CommandContext kills this process;
			// it cannot spawn children that survive cancellation.
			installLinuxTestCommand(t, "spectacle", "while :; do :; done")
			ctx := context.Background()
			switch test.name {
			case "already canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			case "missing command":
				t.Setenv("PATH", t.TempDir())
			}
			image, err := captureScreenshot(ctx)
			if len(image) != 0 || err == nil {
				t.Fatalf("image = %q, error = %v", image, err)
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
			if test.err == nil && errors.Is(err, context.Canceled) {
				t.Fatalf("missing Spectacle is not user cancellation: %v", err)
			}
			entries, err := os.ReadDir(outputDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary screenshot leaked: entries = %v, error = %v", entries, err)
			}
		})
	}
}

func TestLinuxCaptureScreenshotFallbackTools(t *testing.T) {
	disableLinuxPortalForTest(t)
	t.Run("grim and slurp", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "slurp"), []byte("#!/bin/sh\necho '10,20 100x80'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "grim"), []byte("#!/bin/sh\n[ \"$1\" = -g ] && [ \"$2\" = '10,20 100x80' ] || exit 9\nprintf 'GRIMPNG' > \"$3\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		img, err := captureScreenshot(context.Background())
		if err != nil || string(img) != "GRIMPNG" {
			t.Fatalf("grim fallback = %q, err = %v", img, err)
		}
	})

	t.Run("flameshot", func(t *testing.T) {
		installLinuxTestCommand(t, "flameshot", `[ "$1" = gui ] && [ "$2" = --raw ] || exit 9; printf 'FLAMEPNG'`)
		img, err := captureScreenshot(context.Background())
		if err != nil || string(img) != "FLAMEPNG" {
			t.Fatalf("flameshot fallback = %q, err = %v", img, err)
		}
	})
}

func TestLinuxCaptureScreenshotPortalPrecedence(t *testing.T) {
	orig := portalScreenshotFunc
	t.Cleanup(func() { portalScreenshotFunc = orig })

	t.Run("portal success skips external tools", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "spectacle-ran")
		installLinuxTestCommand(t, "spectacle", "touch '"+marker+"'; exit 9")
		portalScreenshotFunc = func(context.Context) ([]byte, error) {
			return []byte("PORTALIMG"), nil
		}
		img, err := captureScreenshot(context.Background())
		if err != nil || string(img) != "PORTALIMG" {
			t.Fatalf("got=%q, err=%v", img, err)
		}
		if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("spectacle should not run when Portal succeeds")
		}
	})

	t.Run("portal user cancel does not fall back to external tools", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "spectacle-ran")
		installLinuxTestCommand(t, "spectacle", "touch '"+marker+"'; printf 'SPECTACLE' > \"$6\"")
		portalScreenshotFunc = func(context.Context) ([]byte, error) {
			return nil, context.Canceled
		}
		img, err := captureScreenshot(context.Background())
		if !errors.Is(err, context.Canceled) || len(img) != 0 {
			t.Fatalf("got=%q, err=%v, want context.Canceled", img, err)
		}
		if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("spectacle must not run after Portal user cancel")
		}
	})

	t.Run("portal error falls back to external tool", func(t *testing.T) {
		installLinuxTestCommand(t, "spectacle", `printf 'SPECTACLEPNG' > "$6"`)
		portalScreenshotFunc = func(context.Context) ([]byte, error) {
			return nil, errors.New("portal service unavailable")
		}
		img, err := captureScreenshot(context.Background())
		if err != nil || string(img) != "SPECTACLEPNG" {
			t.Fatalf("fallback got=%q, err=%v", img, err)
		}
	})
}
