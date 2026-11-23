//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/egoist/mygo"
)

const (
	defaultWatcherStderrLimit = 4096
	defaultWatchDebounce      = 250 * time.Millisecond
	defaultPollInterval       = 450 * time.Millisecond
	defaultDisabledSleep      = 1 * time.Second
	defaultWatchBackoffBase   = 1 * time.Second
	defaultWatchMaxFailures   = 3
	defaultWatchStableWindow  = 30 * time.Second
)

var (
	selectionHookMu     sync.Mutex
	selectionHookCancel context.CancelFunc
	selectionHookDone   chan struct{}
)

type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.max <= 0 {
		return n, nil
	}
	avail := b.max - b.buf.Len()
	if avail > 0 {
		if len(p) > avail {
			p = p[:avail]
		}
		_, _ = b.buf.Write(p)
	}
	return n, nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

type watchProcess interface {
	Stdout() io.ReadCloser
	Wait() error
}

type watchCmdFactory func(ctx context.Context) (watchProcess, error)

type execWatchProcess struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr limitedBuffer
}

func (p *execWatchProcess) Stdout() io.ReadCloser {
	return p.stdout
}

func (p *execWatchProcess) Wait() error {
	err := p.cmd.Wait()
	if err != nil {
		if msg := strings.TrimSpace(p.stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

func defaultWatchCmdFactory(ctx context.Context) (watchProcess, error) {
	cmd := exec.CommandContext(ctx, "wl-paste", "--primary", "--watch", "echo", "1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 200 * time.Millisecond

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	proc := &execWatchProcess{
		cmd:    cmd,
		stdout: stdout,
		stderr: limitedBuffer{max: defaultWatcherStderrLimit},
	}
	cmd.Stderr = &proc.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return proc, nil
}

type selectionHookDeps struct {
	lookPath        func(string) (string, error)
	getenv          func(string) string
	newWatchCmd     watchCmdFactory
	readSelection   func(context.Context) (string, error)
	isEnabled       func() bool
	isBlocked       func() bool
	deliver         func(context.Context, string)
	debounceDelay   time.Duration
	pollInterval    time.Duration
	disabledSleep   time.Duration
	backoffBase     time.Duration
	maxWatchFailure int
	stableThreshold time.Duration
}

func defaultSelectionHookDeps(a *app) selectionHookDeps {
	return selectionHookDeps{
		lookPath:      exec.LookPath,
		getenv:        os.Getenv,
		newWatchCmd:   defaultWatchCmdFactory,
		readSelection: readSelectedTextContext,
		isEnabled:     func() bool { return selectionToolbarEnabled.Load() },
		isBlocked:     func() bool { return a.selectionBlocked.Load() },
		deliver: func(ctx context.Context, text string) {
			delivered := text
			mygo.RunOnMain(func() {
				if ctx.Err() == nil && !a.selectionBlocked.Load() && selectionToolbarEnabled.Load() {
					a.receiveSelection(delivered)
				}
			})
		},
		debounceDelay:   defaultWatchDebounce,
		pollInterval:    defaultPollInterval,
		disabledSleep:   defaultDisabledSleep,
		backoffBase:     defaultWatchBackoffBase,
		maxWatchFailure: defaultWatchMaxFailures,
		stableThreshold: defaultWatchStableWindow,
	}
}

func (d selectionHookDeps) withDefaults() selectionHookDeps {
	if d.lookPath == nil {
		d.lookPath = exec.LookPath
	}
	if d.getenv == nil {
		d.getenv = os.Getenv
	}
	if d.newWatchCmd == nil {
		d.newWatchCmd = defaultWatchCmdFactory
	}
	if d.readSelection == nil {
		d.readSelection = readSelectedTextContext
	}
	if d.isEnabled == nil {
		d.isEnabled = func() bool { return selectionToolbarEnabled.Load() }
	}
	if d.isBlocked == nil {
		d.isBlocked = func() bool { return false }
	}
	if d.deliver == nil {
		d.deliver = func(context.Context, string) {}
	}
	if d.debounceDelay <= 0 {
		d.debounceDelay = defaultWatchDebounce
	}
	if d.pollInterval <= 0 {
		d.pollInterval = defaultPollInterval
	}
	if d.disabledSleep <= 0 {
		d.disabledSleep = defaultDisabledSleep
	}
	if d.backoffBase <= 0 {
		d.backoffBase = defaultWatchBackoffBase
	}
	if d.maxWatchFailure <= 0 {
		d.maxWatchFailure = defaultWatchMaxFailures
	}
	if d.stableThreshold <= 0 {
		d.stableThreshold = defaultWatchStableWindow
	}
	return d
}

func startSelectionHook(a *app) error {
	return startSelectionHookWith(defaultSelectionHookDeps(a))
}

func startSelectionHookWith(deps selectionHookDeps) error {
	deps = deps.withDefaults()

	selectionHookMu.Lock()
	defer selectionHookMu.Unlock()
	if selectionHookCancel != nil {
		return nil
	}
	hasWlPaste := false
	if _, err := deps.lookPath("wl-paste"); err == nil {
		hasWlPaste = true
	}
	hasX11Tool := false
	if _, err := deps.lookPath("xclip"); err == nil {
		hasX11Tool = true
	} else if _, err := deps.lookPath("xsel"); err == nil {
		hasX11Tool = true
	}
	if !hasWlPaste && !hasX11Tool {
		return fmt.Errorf("划词翻译需要 wl-paste（Wayland）或 xclip/xsel（X11）")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	selectionHookCancel = cancel
	selectionHookDone = done

	useWatch := hasWlPaste && (deps.getenv("WAYLAND_DISPLAY") != "" || !hasX11Tool)
	go func() {
		defer close(done)
		if useWatch {
			watchWaylandSelectionWith(ctx, deps)
			return
		}
		pollSelectionWith(ctx, deps)
	}()
	return nil
}

func stopSelectionHook() {
	selectionHookMu.Lock()
	defer selectionHookMu.Unlock()
	if selectionHookCancel != nil {
		selectionHookCancel()
		selectionHookCancel = nil
	}
	// Do not join here: a pending RunOnMain may be waiting on this UI thread.
}

func stopSelectionHookAndWait(timeout time.Duration) bool {
	selectionHookMu.Lock()
	cancel := selectionHookCancel
	done := selectionHookDone
	selectionHookCancel = nil
	selectionHookDone = nil
	selectionHookMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// watchWaylandSelection uses `wl-paste --primary --watch` so idle CPU wakeups
// stay at zero instead of spawning a subprocess every 300ms. If the watcher
// fails repeatedly, it falls back to polling.
func watchWaylandSelection(ctx context.Context, a *app) {
	watchWaylandSelectionWith(ctx, defaultSelectionHookDeps(a))
}

func watchWaylandSelectionWith(ctx context.Context, deps selectionHookDeps) {
	deps = deps.withDefaults()
	baseline, _ := deps.readSelection(ctx)
	failures := 0
	for ctx.Err() == nil {
		started := time.Now()
		err := runWaylandSelectionWatcherWith(ctx, deps, &baseline)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= deps.stableThreshold {
			failures = 0
		}
		failures++
		log.Printf("Wayland 选区监听退出（第 %d 次）: %v", failures, err)
		if failures >= deps.maxWatchFailure {
			log.Printf("Wayland 选区监听连续失败，回退到轮询模式")
			pollSelectionWith(ctx, deps)
			return
		}
		backoff := time.Duration(failures) * deps.backoffBase
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func runWaylandSelectionWatcher(ctx context.Context, a *app, baseline *string) error {
	return runWaylandSelectionWatcherWith(ctx, defaultSelectionHookDeps(a), baseline)
}

func runWaylandSelectionWatcherWith(ctx context.Context, deps selectionHookDeps, baseline *string) error {
	deps = deps.withDefaults()
	proc, err := deps.newWatchCmd(ctx)
	if err != nil {
		return err
	}
	stdout := proc.Stdout()

	events := make(chan struct{}, 1)
	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case events <- struct{}{}:
			default:
			}
		}
		scanDone <- scanner.Err()
	}()

	// Drain scanDone before calling proc.Wait() to satisfy os/exec.Cmd.StdoutPipe.
	waitAndFinish := func() error {
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-scanDone:
			timer.Stop()
		case <-timer.C:
			_ = stdout.Close()
			fallback := time.NewTimer(200 * time.Millisecond)
			select {
			case <-scanDone:
			case <-fallback.C:
			}
			fallback.Stop()
		}
		return proc.Wait()
	}

	timer := time.NewTimer(deps.debounceDelay)
	timer.Stop()
	defer timer.Stop()

	loggedReadError := false
	for {
		select {
		case <-ctx.Done():
			_ = waitAndFinish()
			return ctx.Err()
		case <-events:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(deps.debounceDelay)
		case <-timer.C:
			allowed := deps.isEnabled() && !deps.isBlocked()
			text, err := deps.readSelection(ctx)
			if ctx.Err() != nil {
				_ = waitAndFinish()
				return ctx.Err()
			}
			if err != nil {
				if !loggedReadError {
					log.Printf("Wayland 划词读取失败（本次监听仅记录一次）: %v", err)
					loggedReadError = true
				}
				continue
			}
			if !allowed || !deps.isEnabled() || deps.isBlocked() || text == "" {
				*baseline = text
				continue
			}
			if text == *baseline {
				continue
			}
			*baseline = text
			delivered := text
			deps.deliver(ctx, delivered)
		case scanErr := <-scanDone:
			waitErr := proc.Wait()
			if waitErr != nil {
				return waitErr
			}
			return scanErr
		}
	}
}

func pollSelection(ctx context.Context, a *app) {
	pollSelectionWith(ctx, defaultSelectionHookDeps(a))
}

func pollSelectionWith(ctx context.Context, deps selectionHookDeps) {
	deps = deps.withDefaults()
	ticker := time.NewTicker(deps.pollInterval)
	defer ticker.Stop()
	var debounce selectionDebouncer
	loggedReadError := false
	for ctx.Err() == nil {
		if !deps.isEnabled() {
			debounce = selectionDebouncer{}
			sleepTimer := time.NewTimer(deps.disabledSleep)
			select {
			case <-ctx.Done():
				sleepTimer.Stop()
				return
			case <-sleepTimer.C:
				continue
			}
		}

		// Reject samples started before focus changed, even if wl-paste finishes afterward.
		allowed := !deps.isBlocked()
		text, err := deps.readSelection(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !loggedReadError {
			log.Printf("划词读取失败（本次监控仅记录一次）: %v", err)
			loggedReadError = true
		}
		if debounce.sample(text, allowed && !deps.isBlocked() && deps.isEnabled(), err) {
			delivered := text
			deps.deliver(ctx, delivered)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
