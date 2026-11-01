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
