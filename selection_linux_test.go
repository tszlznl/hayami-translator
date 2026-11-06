//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxSelectionDebouncer(t *testing.T) {
	readErr := errors.New("read failed")
	type sample struct {
		text       string
		suppressed bool
		err        error
		want       bool
	}
	for _, test := range []struct {
		name    string
		samples []sample
	}{
		{"startup establishes baseline", []sample{
			{text: "old"}, {text: "old"}, {text: "old"},
		}},
		{"two stable samples trigger once", []sample{
			{}, {text: "new"}, {text: "new", want: true}, {text: "new"},
		}},
		{"changing selection restarts debounce", []sample{
			{}, {text: "a"}, {text: "ab"}, {text: "abc"}, {text: "abc", want: true},
		}},
		{"baseline interrupts consecutive samples", []sample{
			{text: "old"}, {text: "new"}, {text: "old"}, {text: "new"}, {text: "new", want: true},
		}},
		{"empty allows the same text again", []sample{
			{}, {text: "same"}, {text: "same", want: true}, {}, {text: "same"}, {text: "same", want: true},
		}},
		{"disabled updates baseline immediately", []sample{
			{text: "old"}, {text: "disabled", suppressed: true}, {text: "disabled"}, {text: "disabled"},
			{text: "new"}, {text: "new", want: true},
		}},
		{"panel focus discards pending selection", []sample{
			{}, {text: "own"}, {text: "own", suppressed: true}, {text: "own"}, {text: "own"},
			{text: "external"}, {text: "external", want: true},
		}},
		{"empty while blocked allows reselection", []sample{
			{text: "same"}, {suppressed: true}, {text: "same"}, {text: "same", want: true},
		}},
		{"read error is not an empty selection", []sample{
			{text: "old"}, {err: readErr}, {text: "old"}, {text: "old"},
		}},
		{"read error interrupts stable samples", []sample{
			{}, {text: "new"}, {err: readErr}, {text: "new"}, {text: "new", want: true},
		}},
		{"first successful read establishes baseline", []sample{
			{err: readErr}, {text: "old"}, {text: "old"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var debounce selectionDebouncer
			for i, sample := range test.samples {
				if got := debounce.sample(sample.text, !sample.suppressed, sample.err); got != sample.want {
					t.Fatalf("sample %d (%q): trigger = %v, want %v", i, sample.text, got, sample.want)
				}
			}
		})
	}
}

func TestLinuxReadSelectedTextUsesPrimary(t *testing.T) {
	installLinuxTestCommand(t, "wl-paste", `
[ "$#" -eq 4 ] && [ "$1" = --primary ] && [ "$2" = --no-newline ] && [ "$3" = --type ] && [ "$4" = text ] || exit 9
printf '  selected text\n'
`)
	text, err := readSelectedText()
	if err != nil || text != "selected text" {
		t.Fatalf("selection = %q, error = %v", text, err)
	}
}

func TestLinuxReadSelectedTextEmptyAndErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		script  string
		wantErr bool
	}{
		{"empty output", "exit 0", false},
		{"no selection", "printf 'Nothing is copied\\n' >&2; exit 1", false},
		{"non-text selection", "printf 'Clipboard content is not available as text\\n' >&2; exit 1", false},
		{"connection error", "printf 'Failed to connect to a Wayland server\\n' >&2; exit 1", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			installLinuxTestCommand(t, "wl-paste", test.script)
			text, err := readSelectedText()
			if text != "" || (err != nil) != test.wantErr {
				t.Fatalf("selection = %q, error = %v, want error = %v", text, err, test.wantErr)
			}
		})
	}
}

func TestLinuxReadSelectedTextCanceled(t *testing.T) {
	installLinuxTestCommand(t, "wl-paste", "exit 9")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readSelectedTextContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// Only these temporary executables are visible to command tests. No real
// selection, screenshot, clipboard or audio service is accessed.
func installLinuxTestCommand(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}
