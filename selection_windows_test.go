//go:build windows

package main

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/transfer"
)

func TestReadSelectedTextAbortsWhenClipboardSnapshotUnavailable(t *testing.T) {
	copied := false
	_, err := readSelectedTextWith(
		func() (transfer.Data, bool) { return transfer.Data{}, false },
		func(transfer.Data) (string, error) {
			copied = true
			return "selected", nil
		},
	)
	if !errors.Is(err, errClipboardSnapshot) {
		t.Fatalf("expected errClipboardSnapshot, got %v", err)
	}
	if copied {
		t.Fatal("copy must not run without a restorable clipboard snapshot")
	}
}

func TestReadSelectedTextContinuesWithSnapshot(t *testing.T) {
	saved := transfer.New(transfer.NewItem(transfer.Bytes(transfer.Text, []byte("previous"))))
	got, err := readSelectedTextWith(
		func() (transfer.Data, bool) { return saved, true },
		func(data transfer.Data) (string, error) {
			value, err := data.Read(transfer.Text)
			if err != nil || string(value) != "previous" {
				t.Fatalf("copy received wrong snapshot: %q, %v", value, err)
			}
			return "selected", nil
		},
	)
	if err != nil {
		t.Fatalf("readSelectedTextWith returned %v", err)
	}
	if got != "selected" {
		t.Fatalf("readSelectedTextWith = %q, want selected", got)
	}
}

func TestClipboardFormatCanBeDropped(t *testing.T) {
	cases := map[transfer.Format]bool{
		"EnterpriseDataProtectionId": true,
		"enterprisedataprotection":   true,
		transfer.Text:                false,
		"CustomBinaryFormat":         false,
	}
	for format, want := range cases {
		if got := canDropClipboardFormat(format); got != want {
			t.Errorf("canDropClipboardFormat(%q) = %v, want %v", format, got, want)
		}
	}
}

func TestWindowsTwoTierSkipsClipboardOnUIAHit(t *testing.T) {
	clipboardTouched := false
	got, err := readSelectedTextTwoTier(
		func() (string, error) {
			return "  hello from UIA \r\n", nil
		},
		func() (string, error) {
			clipboardTouched = true
			return readSelectedTextWith(
				func() (transfer.Data, bool) { return transfer.Data{}, false },
				func(transfer.Data) (string, error) { return "fallback", nil },
			)
		},
	)
	if err != nil {
		t.Fatalf("readSelectedTextTwoTier returned error: %v", err)
	}
	if got != "hello from UIA" {
		t.Fatalf("got = %q, want %q", got, "hello from UIA")
	}
	if clipboardTouched {
		t.Fatal("clipboard fallback must not run when UIA returns non-empty text")
	}
}

func TestWindowsTwoTierFallsBackToClipboardSnapshot(t *testing.T) {
	saved := transfer.New(transfer.NewItem(transfer.Bytes(transfer.Text, []byte("original clipboard"))))
	t.Run("empty UIA triggers snapshot fallback", func(t *testing.T) {
		copied := false
		got, err := readSelectedTextTwoTier(
			func() (string, error) { return " \t\r\n", nil },
			func() (string, error) {
				return readSelectedTextWith(
					func() (transfer.Data, bool) { return saved, true },
					func(data transfer.Data) (string, error) {
						copied = true
						v, err := data.Read(transfer.Text)
						if err != nil || string(v) != "original clipboard" {
							t.Fatalf("snapshot mismatch: %q, %v", v, err)
						}
						return "copied selection", nil
					},
				)
			},
		)
		if err != nil || got != "copied selection" || !copied {
			t.Fatalf("got=%q, err=%v, copied=%v", got, err, copied)
		}
	})

	t.Run("UIA error falls back and still respects snapshot guard", func(t *testing.T) {
		_, err := readSelectedTextTwoTier(
			func() (string, error) { return "", errors.New("uia unavailable") },
			func() (string, error) {
				return readSelectedTextWith(
					func() (transfer.Data, bool) { return transfer.Data{}, false },
					func(transfer.Data) (string, error) {
						t.Fatal("copy must not execute when snapshot fails")
						return "", nil
					},
				)
			},
		)
		if !errors.Is(err, errClipboardSnapshot) {
			t.Fatalf("expected errClipboardSnapshot, got %v", err)
		}
	})
}

func TestWindowClassPolicy(t *testing.T) {
	cases := []struct {
		class        string
		wantNonText  bool
		wantTerminal bool
		wantBlocked  bool
	}{
		{"ConsoleWindowClass", false, true, true},
		{"CASCADIA_HOSTING_WINDOW_CLASS", false, true, true},
		{"VirtualConsoleClass", false, true, true},
		{"mintty", false, true, true},
		{"PuTTY", false, true, true},
		{"SysListView32", true, false, true},
		{"Shell_TrayWnd", true, false, true},
		{"Progman", true, false, true},
		{"ToolbarWindow32", true, false, true},
		{"Chrome_WidgetWin_1", false, false, false},
		{"Notepad", false, false, false},
	}
	for _, tc := range cases {
		nonText, terminal := windowClassPolicy(tc.class)
		if nonText != tc.wantNonText || terminal != tc.wantTerminal {
			t.Errorf("windowClassPolicy(%q) = (%v, %v), want (%v, %v)",
				tc.class, nonText, terminal, tc.wantNonText, tc.wantTerminal)
		}
		if got := blockedWindowClass(tc.class); got != tc.wantBlocked {
			t.Errorf("blockedWindowClass(%q) = %v, want %v", tc.class, got, tc.wantBlocked)
		}
	}
}
