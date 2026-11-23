//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestLinuxReadSelectedTextFallsBackToX11(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")

	t.Run("xclip", func(t *testing.T) {
		installLinuxTestCommand(t, "xclip", `
[ "$#" -eq 3 ] && [ "$1" = -o ] && [ "$2" = -selection ] && [ "$3" = primary ] || exit 9
printf '  x11 selected\n'
`)
		text, err := readSelectedText()
		if err != nil || text != "x11 selected" {
			t.Fatalf("xclip selection = %q, err = %v", text, err)
		}
	})

	t.Run("xclip empty selection", func(t *testing.T) {
		installLinuxTestCommand(t, "xclip", `printf 'Error: target STRING not available\n' >&2; exit 1`)
		text, err := readSelectedText()
		if err != nil || text != "" {
			t.Fatalf("xclip empty = %q, err = %v", text, err)
		}
	})

	t.Run("xclip preferred over wl-paste in X11 session", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wl-paste"), []byte("#!/bin/sh\nprintf 'Failed to connect to a Wayland server\\n' >&2; exit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "xclip"), []byte("#!/bin/sh\nprintf 'from-x11'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		text, err := readSelectedText()
		if err != nil || text != "from-x11" {
			t.Fatalf("X11 session with wl-paste installed returned %q, err = %v", text, err)
		}
	})

	t.Run("xsel", func(t *testing.T) {
		installLinuxTestCommand(t, "xsel", `
[ "$#" -eq 2 ] && [ "$1" = -o ] && [ "$2" = -p ] || exit 9
printf 'xsel primary'
`)
		text, err := readSelectedText()
		if err != nil || text != "xsel primary" {
			t.Fatalf("xsel selection = %q, err = %v", text, err)
		}
	})
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

type fakeWatchProcess struct {
	pr     *io.PipeReader
	pw     *io.PipeWriter
	waitCh chan error
	once   sync.Once
}

func newFakeWatchProcess(ctx context.Context) *fakeWatchProcess {
	pr, pw := io.Pipe()
	p := &fakeWatchProcess{
		pr:     pr,
		pw:     pw,
		waitCh: make(chan error, 1),
	}
	go func() {
		<-ctx.Done()
		p.finish(ctx.Err())
	}()
	return p
}

func (p *fakeWatchProcess) Stdout() io.ReadCloser {
	return p.pr
}

func (p *fakeWatchProcess) emit(line string) {
	_, _ = io.WriteString(p.pw, line)
}

func (p *fakeWatchProcess) finish(err error) {
	p.once.Do(func() {
		_ = p.pw.Close()
		p.waitCh <- err
		close(p.waitCh)
	})
}

func (p *fakeWatchProcess) Wait() error {
	err := <-p.waitCh
	_ = p.pr.Close()
	return err
}

func TestLinuxWatcherDebounceCoalescesRapidEvents(t *testing.T) {
	procCh := make(chan *fakeWatchProcess, 1)
	var reads atomic.Int32
	var mu sync.Mutex
	var delivered []string
	deliveredCh := make(chan string, 4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := selectionHookDeps{
		newWatchCmd: func(c context.Context) (watchProcess, error) {
			p := newFakeWatchProcess(c)
			procCh <- p
			return p, nil
		},
		readSelection: func(context.Context) (string, error) {
			n := reads.Add(1)
			if n == 1 {
				return "initial", nil
			}
			return "settled text", nil
		},
		isEnabled:     func() bool { return true },
		isBlocked:     func() bool { return false },
		debounceDelay: 25 * time.Millisecond,
		deliver: func(_ context.Context, text string) {
			mu.Lock()
			delivered = append(delivered, text)
			mu.Unlock()
			deliveredCh <- text
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchWaylandSelectionWith(ctx, deps)
	}()

	proc := <-procCh
	for i := 0; i < 5; i++ {
		proc.emit("1\n")
		time.Sleep(4 * time.Millisecond)
	}

	select {
	case got := <-deliveredCh:
		if got != "settled text" {
			t.Fatalf("delivered = %q, want settled text", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coalesced delivery")
	}

	// Emitting duplicate event with identical text must not re-deliver.
	proc.emit("1\n")
	time.Sleep(80 * time.Millisecond)

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 {
		t.Fatalf("expected 1 delivery, got %d (%v)", len(delivered), delivered)
	}
	if reads.Load() != 3 {
		// 1 initial baseline read + 1 settled burst read + 1 duplicate event read.
		t.Fatalf("reads = %d, want 3 (1 baseline + 1 coalesced burst + 1 dedup check)", reads.Load())
	}
}

func TestLinuxWatcherSuppressesBlockedAndUpdatesBaseline(t *testing.T) {
	procCh := make(chan *fakeWatchProcess, 1)
	var blocked atomic.Bool
	blocked.Store(true)
	var currentText atomic.Value
	currentText.Store("while blocked")

	deliveredCh := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := selectionHookDeps{
		newWatchCmd: func(c context.Context) (watchProcess, error) {
			p := newFakeWatchProcess(c)
			procCh <- p
			return p, nil
		},
		readSelection: func(context.Context) (string, error) {
			return currentText.Load().(string), nil
		},
		isEnabled:     func() bool { return true },
		isBlocked:     func() bool { return blocked.Load() },
		debounceDelay: 15 * time.Millisecond,
		deliver: func(_ context.Context, text string) {
			deliveredCh <- text
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchWaylandSelectionWith(ctx, deps)
	}()

	proc := <-procCh
	proc.emit("1\n")
	time.Sleep(60 * time.Millisecond)

	// Unblocking and emitting the same text must not replay the stale selection.
	blocked.Store(false)
	proc.emit("1\n")
	time.Sleep(60 * time.Millisecond)

	select {
	case got := <-deliveredCh:
		t.Fatalf("unexpected delivery of suppressed/stale text %q", got)
	default:
	}

	// Selecting genuinely new text after unblocking delivers normally.
	currentText.Store("fresh selection")
	proc.emit("1\n")
	select {
	case got := <-deliveredCh:
		if got != "fresh selection" {
			t.Fatalf("got = %q, want fresh selection", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fresh selection after unblock")
	}

	cancel()
	<-done
}

func TestLinuxWatcherRestartsAfterTransientExit(t *testing.T) {
	var attempts atomic.Int32
	deliveredCh := make(chan string, 2)
	var currentText atomic.Value
	currentText.Store("initial")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := selectionHookDeps{
		newWatchCmd: func(c context.Context) (watchProcess, error) {
			n := attempts.Add(1)
			p := newFakeWatchProcess(c)
			if n == 1 {
				go func() {
					time.Sleep(10 * time.Millisecond)
					p.finish(errors.New("transient compositor hiccup"))
				}()
				return p, nil
			}
			go func() {
				time.Sleep(15 * time.Millisecond)
				currentText.Store("after restart")
				p.emit("1\n")
			}()
			return p, nil
		},
		readSelection: func(context.Context) (string, error) {
			return currentText.Load().(string), nil
		},
		isEnabled:       func() bool { return true },
		isBlocked:       func() bool { return false },
		debounceDelay:   15 * time.Millisecond,
		backoffBase:     10 * time.Millisecond,
		maxWatchFailure: 3,
		deliver: func(_ context.Context, text string) {
			deliveredCh <- text
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchWaylandSelectionWith(ctx, deps)
	}()

	select {
	case got := <-deliveredCh:
		if got != "after restart" {
			t.Fatalf("got = %q, want after restart", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for restarted watcher delivery")
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}

	cancel()
	<-done
}

func TestLinuxWatcherFallsBackToPollAfterThreeFailures(t *testing.T) {
	t.Run("three fast failures transition to pollSelection", func(t *testing.T) {
		var watchAttempts atomic.Int32
		var pollReads atomic.Int32
		deliveredCh := make(chan string, 2)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		deps := selectionHookDeps{
			newWatchCmd: func(context.Context) (watchProcess, error) {
				watchAttempts.Add(1)
				return nil, errors.New("Watch mode requires a compositor that supports the data-control protocol")
			},
			readSelection: func(context.Context) (string, error) {
				n := pollReads.Add(1)
				if n <= 2 {
					return "baseline", nil
				}
				return "polled text", nil
			},
			isEnabled:       func() bool { return true },
			isBlocked:       func() bool { return false },
			pollInterval:    10 * time.Millisecond,
			backoffBase:     5 * time.Millisecond,
			maxWatchFailure: 3,
			stableThreshold: time.Hour,
			deliver: func(_ context.Context, text string) {
				deliveredCh <- text
			},
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			watchWaylandSelectionWith(ctx, deps)
		}()

		select {
		case got := <-deliveredCh:
			if got != "polled text" {
				t.Fatalf("got = %q, want polled text", got)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for fallback poller delivery")
		}
		if watchAttempts.Load() != 3 {
			t.Fatalf("watchAttempts = %d, want 3", watchAttempts.Load())
		}

		cancel()
		<-done
	})

	t.Run("healthy uptime resets failure counter", func(t *testing.T) {
		var watchAttempts atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		deps := selectionHookDeps{
			newWatchCmd: func(c context.Context) (watchProcess, error) {
				n := watchAttempts.Add(1)
				p := newFakeWatchProcess(c)
				if n <= 4 {
					go func() {
						time.Sleep(25 * time.Millisecond)
						p.finish(errors.New("periodic exit"))
					}()
				} else {
					cancel()
				}
				return p, nil
			},
			readSelection:   func(context.Context) (string, error) { return "", nil },
			isEnabled:       func() bool { return true },
			backoffBase:     5 * time.Millisecond,
			maxWatchFailure: 3,
			stableThreshold: 15 * time.Millisecond,
			pollInterval:    10 * time.Millisecond,
		}

		watchWaylandSelectionWith(ctx, deps)
		if watchAttempts.Load() < 5 {
			t.Fatalf("expected failure counter reset to allow >=5 watcher starts without falling back to poll, got %d", watchAttempts.Load())
		}
	})
}

func TestLinuxSelectionHookCleanShutdown(t *testing.T) {
	t.Cleanup(func() {
		stopSelectionHookAndWait(time.Second)
	})

	t.Run("idle watcher stops promptly", func(t *testing.T) {
		started := make(chan struct{}, 1)
		err := startSelectionHookWith(selectionHookDeps{
			lookPath: func(name string) (string, error) {
				if name == "wl-paste" {
					return "/usr/bin/wl-paste", nil
				}
				return "", exec.ErrNotFound
			},
			getenv: func(k string) string {
				if k == "WAYLAND_DISPLAY" {
					return "wayland-0"
				}
				return ""
			},
			newWatchCmd: func(c context.Context) (watchProcess, error) {
				select {
				case started <- struct{}{}:
				default:
				}
				return newFakeWatchProcess(c), nil
			},
			readSelection: func(context.Context) (string, error) { return "", nil },
		})
		if err != nil {
			t.Fatalf("startSelectionHookWith error = %v", err)
		}
		<-started
		if !stopSelectionHookAndWait(500 * time.Millisecond) {
			t.Fatal("selection hook did not shut down within 500ms")
		}
	})

	t.Run("backoff and poller stop promptly", func(t *testing.T) {
		err := startSelectionHookWith(selectionHookDeps{
			lookPath: func(name string) (string, error) {
				if name == "xclip" {
					return "/usr/bin/xclip", nil
				}
				return "", exec.ErrNotFound
			},
			getenv:        func(string) string { return "" },
			readSelection: func(context.Context) (string, error) { return "", nil },
			isEnabled:     func() bool { return false },
			disabledSleep: 5 * time.Second,
			pollInterval:  5 * time.Second,
		})
		if err != nil {
			t.Fatalf("startSelectionHookWith error = %v", err)
		}
		time.Sleep(15 * time.Millisecond)
		if !stopSelectionHookAndWait(500 * time.Millisecond) {
			t.Fatal("poller hook did not shut down promptly")
		}
	})
}

func TestLinuxReadSelectedTextContextEnvMatrix(t *testing.T) {
	t.Run("wayland display without wl-paste falls back to xclip when DISPLAY set", func(t *testing.T) {
		env := selectionEnv{
			getenv: func(k string) string {
				switch k {
				case "WAYLAND_DISPLAY":
					return "wayland-0"
				case "DISPLAY":
					return ":0"
				}
				return ""
			},
			lookPath: func(name string) (string, error) {
				if name == "xclip" {
					return "/usr/bin/xclip", nil
				}
				return "", exec.ErrNotFound
			},
			runCmd: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "xclip" || strings.Join(args, " ") != "-o -selection primary" {
					t.Fatalf("unexpected cmd: %s %v", name, args)
				}
				return []byte("from xclip"), nil
			},
		}
		got, err := readSelectedTextWithEnv(context.Background(), env)
		if err != nil || got != "from xclip" {
			t.Fatalf("got=%q, err=%v", got, err)
		}
	})

	t.Run("x11 falls back to xsel when xclip absent", func(t *testing.T) {
		env := selectionEnv{
			getenv: func(k string) string {
				if k == "DISPLAY" {
					return ":0"
				}
				return ""
			},
			lookPath: func(name string) (string, error) {
				if name == "xsel" {
					return "/usr/bin/xsel", nil
				}
				return "", exec.ErrNotFound
			},
			runCmd: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "xsel" || strings.Join(args, " ") != "-o -p" {
					t.Fatalf("unexpected cmd: %s %v", name, args)
				}
				return []byte("from xsel"), nil
			},
		}
		got, err := readSelectedTextWithEnv(context.Background(), env)
		if err != nil || got != "from xsel" {
			t.Fatalf("got=%q, err=%v", got, err)
		}
	})

	t.Run("all selection tools missing returns actionable error", func(t *testing.T) {
		env := selectionEnv{
			getenv: func(k string) string {
				if k == "DISPLAY" {
					return ":0"
				}
				return ""
			},
			lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		}
		_, err := readSelectedTextWithEnv(context.Background(), env)
		if err == nil || !strings.Contains(err.Error(), "wl-paste") {
			t.Fatalf("expected actionable missing-tool error, got %v", err)
		}
	})
}
