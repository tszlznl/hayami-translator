//go:build linux

package main

import (
	"context"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godbus/dbus/v5"
)

type mockPortalClient struct {
	screenshotFn func(ctx context.Context, interactive bool) ([]byte, bool, error)
	appearanceFn func(ctx context.Context) (PortalAppearance, error)
}

func (m mockPortalClient) Screenshot(ctx context.Context, interactive bool) ([]byte, bool, error) {
	if m.screenshotFn != nil {
		return m.screenshotFn(ctx, interactive)
	}
	return nil, false, errors.New("not implemented")
}

func (m mockPortalClient) AppearanceSettings(ctx context.Context) (PortalAppearance, error) {
	if m.appearanceFn != nil {
		return m.appearanceFn(ctx)
	}
	return PortalAppearance{}, errors.New("not implemented")
}

func TestParsePortalAppearance(t *testing.T) {
	t.Run("color-scheme values", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			val     any
			wantOK  bool
			wantVal uint32
		}{
			{"unset zero", uint32(0), false, 0},
			{"prefer dark", uint32(1), true, 1},
			{"prefer light", uint32(2), true, 2},
			{"out of range", uint32(3), false, 0},
			{"wrong type", "dark", false, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := parsePortalAppearance(map[string]dbus.Variant{
					"color-scheme": dbus.MakeVariant(tc.val),
				})
				if got.HasColorScheme != tc.wantOK || got.ColorScheme != tc.wantVal {
					t.Fatalf("got (%v, %d), want (%v, %d)", got.HasColorScheme, got.ColorScheme, tc.wantOK, tc.wantVal)
				}
			})
		}
	})

	t.Run("accent-color values", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			val       any
			wantOK    bool
			wantColor ui.Color
		}{
			{
				name:      "slice any float64",
				val:       []any{0.2392156869, 0.6823529601, 0.9137254953},
				wantOK:    true,
				wantColor: ui.RGB(61, 174, 233),
			},
			{
				name:      "nested dbus variant",
				val:       dbus.MakeVariant([]any{1.0, 0.0, 0.5}),
				wantOK:    true,
				wantColor: ui.RGB(255, 0, 128),
			},
			{
				name:   "unset sentinel (-1,-1,-1)",
				val:    []any{-1.0, -1.0, -1.0},
				wantOK: false,
			},
			{
				name:   "NaN component",
				val:    []any{math.NaN(), 0.5, 0.5},
				wantOK: false,
			},
			{
				name:   "Inf component",
				val:    []any{math.Inf(1), 0.5, 0.5},
				wantOK: false,
			},
			{
				name:   "out of range > 1",
				val:    []any{1.01, 0.2, 0.3},
				wantOK: false,
			},
			{
				name:   "wrong element count",
				val:    []any{0.1, 0.2},
				wantOK: false,
			},
			{
				name:   "wrong element type",
				val:    []any{"0.1", 0.2, 0.3},
				wantOK: false,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := parsePortalAppearance(map[string]dbus.Variant{
					"accent-color": dbus.MakeVariant(tc.val),
				})
				if got.HasAccent != tc.wantOK {
					t.Fatalf("HasAccent = %v, want %v", got.HasAccent, tc.wantOK)
				}
				if tc.wantOK && got.AccentColor != tc.wantColor {
					t.Fatalf("AccentColor = %+v, want %+v", got.AccentColor, tc.wantColor)
				}
			})
		}
	})
}

func TestParseScreenshotResponse(t *testing.T) {
	t.Run("success with encoded path", func(t *testing.T) {
		uri, cancelled, err := parseScreenshotResponse([]any{
			uint32(0),
			map[string]dbus.Variant{
				"uri": dbus.MakeVariant("file:///tmp/my%20shot%20%E6%88%AA%E5%9B%BE.png"),
			},
		})
		if err != nil || cancelled {
			t.Fatalf("uri=%q, cancelled=%v, err=%v", uri, cancelled, err)
		}
		path, err := parseFileURI(uri)
		if err != nil || path != "/tmp/my shot 截图.png" {
			t.Fatalf("path=%q, err=%v", path, err)
		}
	})

	t.Run("cancelled code 1", func(t *testing.T) {
		_, cancelled, err := parseScreenshotResponse([]any{
			uint32(1),
			map[string]dbus.Variant{},
		})
		if err != nil || !cancelled {
			t.Fatalf("cancelled=%v, err=%v", cancelled, err)
		}
	})

	t.Run("error code 2", func(t *testing.T) {
		_, cancelled, err := parseScreenshotResponse([]any{
			uint32(2),
			map[string]dbus.Variant{},
		})
		if err == nil || cancelled {
			t.Fatalf("expected error, got cancelled=%v, err=%v", cancelled, err)
		}
	})

	t.Run("missing uri", func(t *testing.T) {
		_, _, err := parseScreenshotResponse([]any{
			uint32(0),
			map[string]dbus.Variant{},
		})
		if err == nil {
			t.Fatal("expected error when uri missing")
		}
	})

	t.Run("non-file scheme", func(t *testing.T) {
		if _, err := parseFileURI("https://example.com/shot.png"); err == nil {
			t.Fatal("expected error for non-file URI scheme")
		}
	})
}

func TestIsTempScreenshotPath(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)

	inside := filepath.Join(tmpRoot, "portal-shot.png")
	nested := filepath.Join(tmpRoot, "sub", "portal-shot.png")
	escaped := filepath.Join(tmpRoot, "..", "outside.png")
	persistent := filepath.Join(string(filepath.Separator), "home", "user", "Pictures", "Screenshots", "shot.png")
	docPortal := filepath.Join(string(filepath.Separator), "run", "user", "1000", "doc", "abc123", "shot.png")

	if !isTempScreenshotPath(inside) {
		t.Fatalf("expected %q to be temp path", inside)
	}
	if !isTempScreenshotPath(nested) {
		t.Fatalf("expected %q to be temp path", nested)
	}
	if isTempScreenshotPath(tmpRoot) {
		t.Fatalf("temp root itself must not be treated as removable screenshot file")
	}
	if isTempScreenshotPath(escaped) {
		t.Fatalf("traversal path %q must not be temp path", escaped)
	}
	if isTempScreenshotPath(persistent) {
		t.Fatalf("persistent path %q must not be temp path", persistent)
	}
	if isTempScreenshotPath(docPortal) {
		t.Fatalf("doc portal path %q must not be temp path", docPortal)
	}
}

func TestReadScreenshotFileURITempVsPersistent(t *testing.T) {
	tmpDir := filepath.Join(t.TempDir(), "tmp")
	persistDir := filepath.Join(t.TempDir(), "Pictures")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(persistDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmpDir)

	tempShot := filepath.Join(tmpDir, "temp shot.png")
	if err := os.WriteFile(tempShot, []byte("TEMPPNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	tempURI := (&url.URL{Scheme: "file", Path: tempShot}).String()
	gotTemp, err := readScreenshotFileURI(tempURI)
	if err != nil || string(gotTemp) != "TEMPPNG" {
		t.Fatalf("readScreenshotFileURI temp = %q, err = %v", gotTemp, err)
	}
	if _, err := os.Stat(tempShot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected temp screenshot to be removed, stat err = %v", err)
	}

	persistShot := filepath.Join(persistDir, "keep me.png")
	if err := os.WriteFile(persistShot, []byte("KEEPPNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	persistURI := (&url.URL{Scheme: "file", Path: persistShot}).String()
	gotPersist, err := readScreenshotFileURI(persistURI)
	if err != nil || string(gotPersist) != "KEEPPNG" {
		t.Fatalf("readScreenshotFileURI persistent = %q, err = %v", gotPersist, err)
	}
	if _, err := os.Stat(persistShot); err != nil {
		t.Fatalf("persistent screenshot must NOT be deleted: %v", err)
	}
}

func TestCaptureScreenshotWithPortalMock(t *testing.T) {
	origClient := defaultPortalClient
	t.Cleanup(func() { defaultPortalClient = origClient })

	t.Run("interactive screenshot success", func(t *testing.T) {
		var sawInteractive bool
		defaultPortalClient = mockPortalClient{
			screenshotFn: func(ctx context.Context, interactive bool) ([]byte, bool, error) {
				sawInteractive = interactive
				return []byte("PORTALPNG"), false, nil
			},
		}
		got, err := captureScreenshotWithPortal(context.Background())
		if err != nil || string(got) != "PORTALPNG" || !sawInteractive {
			t.Fatalf("got=%q, err=%v, interactive=%v", got, err, sawInteractive)
		}
	})

	t.Run("cancelled response maps to context.Canceled", func(t *testing.T) {
		defaultPortalClient = mockPortalClient{
			screenshotFn: func(ctx context.Context, interactive bool) ([]byte, bool, error) {
				return nil, true, nil
			},
		}
		_, err := captureScreenshotWithPortal(context.Background())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

func TestReadPortalAppearanceCached(t *testing.T) {
	origClient := defaultPortalClient
	t.Cleanup(func() {
		defaultPortalClient = origClient
		invalidatePortalAppearanceCache()
	})
	invalidatePortalAppearanceCache()

	var calls atomic.Int32
	defaultPortalClient = mockPortalClient{
		appearanceFn: func(ctx context.Context) (PortalAppearance, error) {
			calls.Add(1)
			return PortalAppearance{
				ColorScheme:    1,
				HasColorScheme: true,
				AccentColor:    ui.RGB(61, 174, 233),
				HasAccent:      true,
			}, nil
		},
	}

	a1, err := readPortalAppearanceCached(context.Background())
	if err != nil || !a1.HasColorScheme || a1.ColorScheme != 1 {
		t.Fatalf("first read = %+v, err = %v", a1, err)
	}
	a2, err := readPortalAppearanceCached(context.Background())
	if err != nil || a2 != a1 {
		t.Fatalf("cached read = %+v, err = %v", a2, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 D-Bus call within TTL, got %d", calls.Load())
	}

	// Negative cache test: errors within TTL should also be cached without hammering D-Bus.
	invalidatePortalAppearanceCache()
	calls.Store(0)
	defaultPortalClient = mockPortalClient{
		appearanceFn: func(ctx context.Context) (PortalAppearance, error) {
			calls.Add(1)
			return PortalAppearance{}, errors.New("portal absent")
		},
	}
	if _, err := readPortalAppearanceCached(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if _, err := readPortalAppearanceCached(context.Background()); err == nil {
		t.Fatal("expected cached error")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected negative cache to keep calls=1, got %d", calls.Load())
	}
}
