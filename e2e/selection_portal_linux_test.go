//go:build e2e && linux

package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Local helpers for Linux selection, portal, and pixel inspection tests
// ---------------------------------------------------------------------------

func installSafeWlPasteShim(t *testing.T, s *sandbox) {
	t.Helper()
	t.Cleanup(func() {
		cleanupWlWatchPID(s)
	})
	s.writeShim("wl-paste", `#!/bin/sh
LOG_FILE="${HAYAMI_SHIM_LOG_DIR}/wl-paste.log"
STATE_DIR="${HAYAMI_SHIM_STATE_DIR}"
get_state() {
  if [ -f "$STATE_DIR/$1" ]; then
    /bin/cat "$STATE_DIR/$1"
  else
    eval "printf '%s' \"\${$1:-}\""
  fi
}
log_call() {
  printf '%s\t%s\n' "wl-paste" "$*" >> "$LOG_FILE"
}
log_call "$@"
if [ "${1:-}" = "--primary" ] && [ "${2:-}" = "--watch" ]; then
  shift 2
  crash="$(get_state wl_watch_crash)"
  if [ -n "$crash" ]; then
    exit 1
  fi
  printf '%s\n' "$$" > "$STATE_DIR/wl_watch.pid"
  trap 'exit 0' TERM INT
  last_seq="$(get_state wl_watch_seq)"
  while true; do
    if ! kill -0 "$PPID" 2>/dev/null; then
      exit 0
    fi
    cur_seq="$(get_state wl_watch_seq)"
    if [ "$cur_seq" != "$last_seq" ] && [ -n "$cur_seq" ]; then
      last_seq="$cur_seq"
      "$@"
    fi
    /bin/sleep 0.05
  done
fi
mode="$(get_state wl_paste_mode)"
[ -z "$mode" ] && mode="ok"
case "$mode" in
  ok)
    get_state wl_paste_text
    exit 0
    ;;
  slow)
    exec /bin/sleep 5
    ;;
  empty)
    exit 0
    ;;
  nothing)
    printf 'Nothing is copied\n' >&2
    exit 1
    ;;
  nontext)
    printf 'Clipboard content is not available as text\n' >&2
    exit 1
    ;;
  error)
    printf 'Failed to connect to a Wayland server\n' >&2
    exit 1
    ;;
  *)
    printf 'unknown wl-paste mode: %s\n' "$mode" >&2
    exit 1
    ;;
esac
`)
}

func cleanupWlWatchPID(s *sandbox) {
	pidPath := filepath.Join(s.ShimStateDir, "wl_watch.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return
	}
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 1 {
			continue
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func startGUIWithEnv(t *testing.T, s *sandbox, mutateEnv func([]string) []string, args ...string) *GUIProcess {
	t.Helper()
	s.requireDisplay()

	sockPath := filepath.Join(s.RuntimeDir, "auto.sock")
	_ = os.Remove(sockPath)

	env := s.buildEnv(false, "MYGO_AUTOMATION="+sockPath)
	if mutateEnv != nil {
		env = mutateEnv(env)
	}

	cmd := exec.Command(hayamiBin, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("startGUIWithEnv %v: %v", args, err)
	}

	done := make(chan error, 1)
	proc := &GUIProcess{
		Cmd:        cmd,
		SocketPath: sockPath,
		Stdout:     &stdout,
		Stderr:     &stderr,
		Done:       done,
	}
	go func() {
		err := cmd.Wait()
		proc.exited.Store(true)
		done <- err
	}()

	t.Cleanup(func() {
		proc.Stop(t)
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("GUI process exited early (%v)\nstdout:\n%s\nstderr:\n%s\ndebug.log:\n%s",
				err, stdout.String(), stderr.String(), s.readDebugLog())
		default:
		}
		conn, err := net.DialTimeout("unix", sockPath, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return proc
		}
		time.Sleep(25 * time.Millisecond)
	}

	proc.Stop(t)
	t.Fatalf("timed out waiting for MYGO_AUTOMATION socket %s", sockPath)
	return nil
}

func runForwardCLIWithEnv(t *testing.T, s *sandbox, mutateEnv func([]string) []string, timeout time.Duration, args ...string) CLIResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	env := s.buildEnv(false)
	if mutateEnv != nil {
		env = mutateEnv(env)
	}
	cmd := exec.CommandContext(ctx, hayamiBin, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	return CLIResult{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
}

func dialMainWindow(t *testing.T, proc *GUIProcess) (*AutomationClient, WindowInfo) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	client, err := DialAutomation(ctx, proc.SocketPath, proc.Done)
	if err != nil {
		t.Fatalf("DialAutomation: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	win, err := client.WaitWindow(ctx, func(w WindowInfo) bool {
		return w.Title == "Hayami Translator"
	})
	if err != nil {
		t.Fatalf("WaitWindow Hayami Translator: %v", err)
	}
	return client, win
}

func treeContainsText(tr TreeResult, substr string) bool {
	for _, n := range tr.Nodes {
		if strings.Contains(n.Label, substr) ||
			strings.Contains(n.Value, substr) ||
			strings.Contains(n.Placeholder, substr) ||
			strings.Contains(n.Description, substr) {
			return true
		}
	}
	return false
}

func waitTreeContains(t *testing.T, client *AutomationClient, winID int, timeout time.Duration, substr string) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	tr, err := client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		return treeContainsText(tr, substr)
	})
	if err != nil {
		t.Fatalf("WaitTree(%q): %v", substr, err)
	}
	return tr
}

func countShimLogLines(s *sandbox, tool, substr string) int {
	logText := s.readShimLog(tool)
	if logText == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(logText, "\n") {
		if line != "" && (substr == "" || strings.Contains(line, substr)) {
			count++
		}
	}
	return count
}

func writePNGFixture(t *testing.T, s *sandbox, name string, width, height int) string {
	t.Helper()
	p := filepath.Join(s.TmpDir, name)
	if err := os.WriteFile(p, makePNG(t, width, height), 0o644); err != nil {
		t.Fatalf("write png fixture %s: %v", p, err)
	}
	return p
}

func sampleNodeCornerColor(t *testing.T, img image.Image, win WindowInfo, b Rect) color.RGBA {
	t.Helper()
	if win.Bounds.Width <= 0 || win.Bounds.Height <= 0 {
		t.Fatalf("invalid window bounds: %+v", win.Bounds)
	}
	sx := float64(img.Bounds().Dx()) / win.Bounds.Width
	sy := float64(img.Bounds().Dy()) / win.Bounds.Height
	px := int((b.X + 4) * sx)
	py := int((b.Y + 4) * sy)
	return sampleImagePixel(t, img, px, py)
}

func sampleImagePixel(t *testing.T, img image.Image, px, py int) color.RGBA {
	t.Helper()
	bounds := img.Bounds()
	if px < bounds.Min.X {
		px = bounds.Min.X
	}
	if px >= bounds.Max.X {
		px = bounds.Max.X - 1
	}
	if py < bounds.Min.Y {
		py = bounds.Min.Y
	}
	if py >= bounds.Max.Y {
		py = bounds.Max.Y - 1
	}
	r, g, b, a := img.At(px, py).RGBA()
	return color.RGBA{
		R: uint8(r >> 8),
		G: uint8(g >> 8),
		B: uint8(b >> 8),
		A: uint8(a >> 8),
	}
}

func colorLuma(c color.RGBA) int {
	return (int(c.R)*299 + int(c.G)*587 + int(c.B)*114) / 1000
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func decodeCapturedPNG(t *testing.T, res CaptureResult) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(res.PNG))
	if err != nil {
		t.Fatalf("decode captured PNG (%d bytes): %v", len(res.PNG), err)
	}
	return img
}

// ---------------------------------------------------------------------------
// F4 Tier 1: Core Selection Capture & Watchers (6 tests)
// ---------------------------------------------------------------------------

func TestE2E_F4_T1_WaylandOneShotPrimaryReadAndTranslate(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "你好主选区"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	installSafeWlPasteShim(t, s)
	s.setShimState("wl_paste_mode", "ok")
	s.setShimState("wl_paste_text", "  hello primary\n")

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	res := s.runForwardCLI(5*time.Second, "-s")
	if res.ExitCode != 0 {
		t.Fatalf("-s exit code = %d, stderr=%q", res.ExitCode, res.Stderr)
	}

	pollUntil(t, 4*time.Second, "MockLLM receives 1 request for primary selection", func() bool {
		return len(m.Requests()) == 1
	})

	shimLog := s.readShimLog("wl-paste")
	if !strings.Contains(shimLog, "--primary --no-newline --type text") {
		t.Fatalf("expected wl-paste PRIMARY invocation, got:\n%s", shimLog)
	}

	reqs := m.Requests()
	if !strings.Contains(reqs[0].Messages[len(reqs[0].Messages)-1].Text, "hello primary") {
		t.Fatalf("unexpected LLM user message: %#v", reqs[0].Messages)
	}

	tr := waitTreeContains(t, client, win.ID, 4*time.Second, "你好主选区")
	if !treeContainsText(tr, "hello primary") {
		t.Fatalf("expected popup tree to contain source text 'hello primary'")
	}
}

func TestE2E_F4_T1_WaylandWatchDeliverAndDebounce(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "已防抖翻译"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	installSafeWlPasteShim(t, s)
	s.setShimState("wl_paste_mode", "ok")
	s.setShimState("wl_paste_text", "initial")

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	pollUntil(t, 4*time.Second, "wl-paste --watch started", func() bool {
		return strings.Contains(s.readShimLog("wl-paste"), "--primary --watch echo 1")
	})

	// Rapidly burst watch notifications to test 250ms debounce coalescing.
	s.setShimState("wl_paste_text", "settled selection")
	for _, seq := range []string{"1", "2", "3"} {
		s.setShimState("wl_watch_seq", seq)
		time.Sleep(30 * time.Millisecond)
	}

	pollUntil(t, 4*time.Second, "coalesced LLM translation request", func() bool {
		return len(m.Requests()) >= 1
	})
	waitTreeContains(t, client, win.ID, 4*time.Second, "已防抖翻译")

	if got := len(m.Requests()); got != 1 {
		t.Fatalf("expected 1 coalesced LLM request after debounce, got %d", got)
	}
	reqs := m.Requests()
	if !strings.Contains(reqs[0].Messages[len(reqs[0].Messages)-1].Text, "settled selection") {
		t.Fatalf("expected settled selection in LLM request, got %#v", reqs[0].Messages)
	}

	// Hide popup (watcher stays active) and bump watch with identical text: must not re-trigger.
	if err := s.runForwardCLI(5*time.Second, "-t"); err.ExitCode != 0 {
		t.Fatalf("hide popup -t failed: %+v", err)
	}
	pollUntil(t, 3*time.Second, "popup hidden before identical-selection watch check", func() bool {
		wins, err := client.Windows(context.Background())
		if err != nil {
			return false
		}
		for _, w := range wins {
			if w.ID == win.ID {
				return !w.Visible
			}
		}
		return false
	})

	readsBefore := countShimLogLines(s, "wl-paste", "--primary --no-newline --type text")
	s.setShimState("wl_watch_seq", "4")
	pollUntil(t, 3*time.Second, "watcher reads identical selection", func() bool {
		return countShimLogLines(s, "wl-paste", "--primary --no-newline --type text") > readsBefore
	})
	if got := len(m.Requests()); got != 1 {
		t.Fatalf("identical selection re-triggered translation: got %d requests, want 1", got)
	}
}

func TestE2E_F4_T1_X11PrimaryReadXclip(t *testing.T) {
	if origDisplay == "" {
		t.Skip("skipping X11 selection test: DISPLAY is unset")
	}
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "X11翻译结果"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("xclip")
	s.setShimState("xclip_mode", "ok")
	s.setShimState("xclip_text", "  x11 primary  ")

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	if res := s.runForwardCLI(5*time.Second, "-s"); res.ExitCode != 0 {
		t.Fatalf("-s exit code = %d, stderr=%q", res.ExitCode, res.Stderr)
	}

	pollUntil(t, 4*time.Second, "MockLLM receives xclip translation", func() bool {
		return len(m.Requests()) == 1
	})
	waitTreeContains(t, client, win.ID, 4*time.Second, "X11翻译结果")

	if logText := s.readShimLog("xclip"); !strings.Contains(logText, "-o -selection primary") {
		t.Fatalf("expected xclip -o -selection primary in log, got %q", logText)
	}
	if !strings.Contains(m.Requests()[0].Messages[len(m.Requests()[0].Messages)-1].Text, "x11 primary") {
		t.Fatalf("expected trimmed 'x11 primary' in LLM request")
	}
}

func TestE2E_F4_T1_X11SessionPrefersXclipOverWlPaste(t *testing.T) {
	if origDisplay == "" {
		t.Skip("skipping X11 preference test: DISPLAY is unset")
	}
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "来自X11"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	installSafeWlPasteShim(t, s)
	s.setShimState("wl_paste_mode", "error")
	s.installShim("xclip")
	s.setShimState("xclip_mode", "ok")
	s.setShimState("xclip_text", "from-x11")

	clearWayland := func(env []string) []string {
		return setOrReplaceEnv(env, "WAYLAND_DISPLAY", "")
	}

	proc := startGUIWithEnv(t, s, clearWayland, "--silent")
	client, win := dialMainWindow(t, proc)

	res := runForwardCLIWithEnv(t, s, clearWayland, 5*time.Second, "-s")
	if res.ExitCode != 0 {
		t.Fatalf("-s under X11 failed: %+v", res)
	}

	pollUntil(t, 4*time.Second, "MockLLM receives xclip selection", func() bool {
		return len(m.Requests()) == 1
	})
	waitTreeContains(t, client, win.ID, 4*time.Second, "来自X11")

	if !strings.Contains(s.readShimLog("xclip"), "-o -selection primary") {
		t.Fatalf("expected xclip to be used, log=%q", s.readShimLog("xclip"))
	}
	if strings.Contains(s.readShimLog("wl-paste"), "--no-newline") {
		t.Fatalf("expected wl-paste not to be used for reading in X11 session, log=%q", s.readShimLog("wl-paste"))
	}
}

func TestE2E_F4_T1_X11FallbackToXselWhenXclipAbsent(t *testing.T) {
	if origDisplay == "" {
		t.Skip("skipping X11 selection test: DISPLAY is unset")
	}
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "来自xsel"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("xsel")
	s.setShimState("xsel_mode", "ok")
	s.setShimState("xsel_text", "from-xsel")

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	if res := s.runForwardCLI(5*time.Second, "-s"); res.ExitCode != 0 {
		t.Fatalf("-s failed: %+v", res)
	}

	pollUntil(t, 4*time.Second, "MockLLM receives xsel selection", func() bool {
		return len(m.Requests()) == 1
	})
	waitTreeContains(t, client, win.ID, 4*time.Second, "来自xsel")

	if !strings.Contains(s.readShimLog("xsel"), "-o -p") {
		t.Fatalf("expected xsel -o -p in log, got %q", s.readShimLog("xsel"))
	}
}

func TestE2E_F4_T1_WatcherSuppressedWhilePopupFocused(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "新文本译文"})

	// popupPinned must be false so showPopup() focuses the window and sets selectionBlocked=true.
	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = false
	s.writeConfigJSON(cfg)

	installSafeWlPasteShim(t, s)
	s.setShimState("wl_paste_mode", "ok")
	s.setShimState("wl_paste_text", "")

	proc := s.startGUI()
	client, win := dialMainWindow(t, proc)

	pollUntil(t, 4*time.Second, "popup focused and wl-paste watcher running", func() bool {
		return strings.Contains(s.readDebugLog(), "[debug] panel focused") &&
			strings.Contains(s.readShimLog("wl-paste"), "--primary --watch echo 1")
	})

	// Trigger watcher while popup is focused: selection must be suppressed & update baseline.
	readsBefore := countShimLogLines(s, "wl-paste", "--primary --no-newline --type text")
	s.setShimState("wl_paste_text", "blocked text")
	s.setShimState("wl_watch_seq", "1")
	pollUntil(t, 3*time.Second, "watcher samples blocked text while popup focused", func() bool {
		return countShimLogLines(s, "wl-paste", "--primary --no-newline --type text") > readsBefore
	})
	if got := len(m.Requests()); got != 0 {
		t.Fatalf("expected 0 LLM requests while popup focused, got %d", got)
	}

	// Hide popup; re-trigger with identical "blocked text" -> baseline suppresses replay.
	if res := s.runForwardCLI(5*time.Second, "-t"); res.ExitCode != 0 {
		t.Fatalf("hide popup -t failed: %+v", res)
	}
	pollUntil(t, 3*time.Second, "popup hidden after -t", func() bool {
		wins, err := client.Windows(context.Background())
		if err != nil {
			return false
		}
		for _, w := range wins {
			if w.ID == win.ID {
				return !w.Visible
			}
		}
		return false
	})

	readsBefore = countShimLogLines(s, "wl-paste", "--primary --no-newline --type text")
	s.setShimState("wl_watch_seq", "2")
	pollUntil(t, 3*time.Second, "watcher samples blocked text after hide", func() bool {
		return countShimLogLines(s, "wl-paste", "--primary --no-newline --type text") > readsBefore
	})
	if got := len(m.Requests()); got != 0 {
		t.Fatalf("suppressed selection replayed after hide: got %d requests, want 0", got)
	}

	// Genuine new selection delivers and translates.
	s.setShimState("wl_paste_text", "fresh text")
	s.setShimState("wl_watch_seq", "3")
	pollUntil(t, 4*time.Second, "fresh selection triggers translation", func() bool {
		return len(m.Requests()) == 1
	})
	waitTreeContains(t, client, win.ID, 4*time.Second, "新文本译文")
	if !strings.Contains(m.Requests()[0].Messages[len(m.Requests()[0].Messages)-1].Text, "fresh text") {
		t.Fatalf("unexpected translated text: %#v", m.Requests()[0].Messages)
	}
}

// ---------------------------------------------------------------------------
// F4 Tier 2: Error, Boundary, Crash-Fallback & Timeout Paths (6 tests)
// ---------------------------------------------------------------------------

func TestE2E_F4_T2_WaylandWatchThreeCrashesFallbackToPolling(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "轮询回退译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	installSafeWlPasteShim(t, s)
	s.setShimState("wl_watch_crash", "1")
	s.setShimState("wl_paste_mode", "ok")
	s.setShimState("wl_paste_text", "baseline")

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	pollUntil(t, 8*time.Second, "debug.log records fallback to polling after 3 watch crashes", func() bool {
		return strings.Contains(s.readDebugLog(), "Wayland 选区监听连续失败，回退到轮询模式")
	})
	// Wait for pollSelectionWith's first sample so selectionDebouncer initializes its baseline
	// with "baseline" before we switch wl_paste_text to "polled after fallback".
	readsAtFallback := countShimLogLines(s, "wl-paste", "--primary --no-newline --type text")
	pollUntil(t, 3*time.Second, "pollSelectionWith records initial baseline read", func() bool {
		return countShimLogLines(s, "wl-paste", "--primary --no-newline --type text") > readsAtFallback
	})

	s.setShimState("wl_paste_text", "polled after fallback")
	pollUntil(t, 5*time.Second, "polling picks up updated selection", func() bool {
		return len(m.Requests()) >= 1
	})
	waitTreeContains(t, client, win.ID, 4*time.Second, "轮询回退译文")
	if !strings.Contains(m.Requests()[0].Messages[len(m.Requests()[0].Messages)-1].Text, "polled after fallback") {
		t.Fatalf("expected polled text in LLM request, got %#v", m.Requests()[0].Messages)
	}
}

func TestE2E_F4_T2_EmptyAndNonTextSelectionHandledGracefully(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		wantArgs string
		stateKey string
		stateVal string
	}{
		{name: "empty", tool: "wl-paste", wantArgs: "--primary --no-newline --type text", stateKey: "wl_paste_mode", stateVal: "empty"},
		{name: "nothing", tool: "wl-paste", wantArgs: "--primary --no-newline --type text", stateKey: "wl_paste_mode", stateVal: "nothing"},
		{name: "nontext", tool: "wl-paste", wantArgs: "--primary --no-newline --type text", stateKey: "wl_paste_mode", stateVal: "nontext"},
		{name: "xclip_empty", tool: "xclip", wantArgs: "-o -selection primary", stateKey: "xclip_mode", stateVal: "empty"},
		{name: "xsel_empty", tool: "xsel", wantArgs: "-o -p", stateKey: "xsel_mode", stateVal: "empty"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.tool == "xclip" || tc.tool == "xsel") && origDisplay == "" {
				t.Skip("skipping X11 selection subtest: DISPLAY is unset")
			}
			s := newSandbox(t)
			m := newMockLLMServer(t)

			cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
			cfg["disableSelectionToolbar"] = true
			cfg["popupPinned"] = true
			s.writeConfigJSON(cfg)

			s.IsolatePATH = true
			if tc.tool == "wl-paste" {
				installSafeWlPasteShim(t, s)
			} else {
				s.installShim(tc.tool)
			}
			s.setShimState(tc.stateKey, tc.stateVal)

			proc := s.startGUI("--silent")
			client, win := dialMainWindow(t, proc)

			if res := s.runForwardCLI(5*time.Second, "-s"); res.ExitCode != 0 {
				t.Fatalf("-s failed: %+v", res)
			}

			// Wait for the post-readSelectedText status ("输入文本后按 Ctrl+Enter 翻译")
			// rather than the pre-existing startup placeholder ("输入文字后按 Ctrl+Enter 翻译…").
			tr := waitTreeContains(t, client, win.ID, 4*time.Second, "输入文本后按 Ctrl+Enter 翻译")
			if shimLog := s.readShimLog(tc.tool); !strings.Contains(shimLog, tc.wantArgs) {
				t.Fatalf("expected %s shim to be invoked with %q for %s, got %q", tc.tool, tc.wantArgs, tc.name, shimLog)
			}
			if treeContainsText(tr, "失败") {
				t.Fatalf("unexpected error status for %s: %s", tc.name, formatTreeSummary(tr))
			}
			if got := len(m.Requests()); got != 0 {
				t.Fatalf("expected 0 LLM requests for %s, got %d", tc.name, got)
			}
		})
	}
}

func TestE2E_F4_T2_WaylandConnectionErrorShowsStatusInPopup(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	installSafeWlPasteShim(t, s)
	s.setShimState("wl_paste_mode", "error")

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	if res := s.runForwardCLI(5*time.Second, "-s"); res.ExitCode != 0 {
		t.Fatalf("-s failed: %+v", res)
	}

	tr := waitTreeContains(t, client, win.ID, 4*time.Second, "读取 Wayland PRIMARY 选区失败")
	if !treeContainsText(tr, "输入文字后按 Ctrl+Enter 翻译…") {
		t.Fatalf("expected popup in edit mode on error: %s", formatTreeSummary(tr))
	}
	if got := len(m.Requests()); got != 0 {
		t.Fatalf("expected 0 LLM requests on selection error, got %d", got)
	}
}

func TestE2E_F4_T2_AllSelectionToolsMissingActionableError(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	pollUntil(t, 3*time.Second, "startup selection hook logs missing tools", func() bool {
		return strings.Contains(s.readDebugLog(), "划词翻译需要 wl-paste（Wayland）或 xclip/xsel（X11）")
	})

	if res := s.runForwardCLI(5*time.Second, "-s"); res.ExitCode != 0 {
		t.Fatalf("-s failed: %+v", res)
	}

	tr := waitTreeContains(t, client, win.ID, 4*time.Second, "wl-paste")
	if !treeContainsText(tr, "xclip/xsel") {
		t.Fatalf("expected missing-tool guidance mentioning xclip/xsel: %s", formatTreeSummary(tr))
	}
	if got := len(m.Requests()); got != 0 {
		t.Fatalf("expected 0 LLM requests when tools missing, got %d", got)
	}
}

func TestE2E_F4_T2_SlowWlPasteTimesOutCleanlyAndRecovers(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "超时恢复译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	installSafeWlPasteShim(t, s)
	s.setShimState("wl_paste_mode", "slow")

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	start := time.Now()
	if res := s.runForwardCLI(5*time.Second, "-s"); res.ExitCode != 0 {
		t.Fatalf("-s slow failed: %+v", res)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	tr, err := client.WaitTree(ctx, win.ID, func(tr TreeResult) bool {
		return treeContainsText(tr, "deadline exceeded") ||
			treeContainsText(tr, "读取 Wayland PRIMARY 选区失败")
	})
	if err != nil {
		t.Fatalf("WaitTree after slow wl-paste timeout: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 4*time.Second {
		t.Fatalf("slow wl-paste took %v, expected <= ~1s timeout", elapsed)
	}
	if !treeContainsText(tr, "deadline exceeded") && !treeContainsText(tr, "读取 Wayland PRIMARY 选区失败") {
		t.Fatalf("expected timeout error status in popup tree, got:\n%s", formatTreeSummary(tr))
	}
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle after timeout failed: %v", err)
	}

	// Recover with working wl-paste on next -s.
	s.setShimState("wl_paste_mode", "ok")
	s.setShimState("wl_paste_text", "recovered selection")
	if res := s.runForwardCLI(5*time.Second, "-s"); res.ExitCode != 0 {
		t.Fatalf("-s recovery failed: %+v", res)
	}

	pollUntil(t, 4*time.Second, "recovered -s calls MockLLM", func() bool {
		return len(m.Requests()) == 1
	})
	waitTreeContains(t, client, win.ID, 4*time.Second, "超时恢复译文")
}

func TestE2E_F4_T2_DisableSelectionToolbarConfigPreventsWatcher(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "显式划词译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	installSafeWlPasteShim(t, s)
	s.setShimState("wl_paste_mode", "ok")
	s.setShimState("wl_paste_text", "ignored")

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	pollUntil(t, 4*time.Second, "ready() finished in debug.log", func() bool {
		return strings.Contains(s.readDebugLog(), "ready() finished")
	})

	// Even if watch fires, selectionToolbarEnabled == false suppresses background delivery.
	s.setShimState("wl_watch_seq", "1")
	pollUntil(t, 2*time.Second, "WaitIdle after watch bump", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return client.WaitIdle(ctx, win.ID) == nil
	})
	// Wait briefly beyond 250ms debounce to confirm background watcher never fires LLM.
	time.Sleep(400 * time.Millisecond)
	if got := len(m.Requests()); got != 0 {
		t.Fatalf("expected 0 LLM requests when disableSelectionToolbar=true, got %d", got)
	}

	// Explicit -s still reads and translates.
	s.setShimState("wl_paste_text", "explicit selection")
	if res := s.runForwardCLI(5*time.Second, "-s"); res.ExitCode != 0 {
		t.Fatalf("explicit -s failed: %+v", res)
	}
	pollUntil(t, 4*time.Second, "explicit -s translates", func() bool {
		return len(m.Requests()) == 1
	})
	waitTreeContains(t, client, win.ID, 4*time.Second, "显式划词译文")
}

// ---------------------------------------------------------------------------
// F5 Tier 1: Linux D-Bus Portals (Screenshot & Appearance) + KDE (6 tests)
// ---------------------------------------------------------------------------

func TestE2E_F5_T1_ScreenshotPortalSuccessSendsToVisionLLM(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "截图识别译文"})

	portal := startMockPortal(t, mockPortalOptions{
		ScreenshotPNG:  makePNG(t, 200, 120),
		ScreenshotCode: 0,
	})
	s.DBusAddr = portal.Addr

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("spectacle")

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	pollUntil(t, 5*time.Second, "Vision LLM receives screenshot", func() bool {
		return len(m.Requests()) == 1
	})
	tr := waitTreeContains(t, client, win.ID, 4*time.Second, "视觉模型截图识别")
	if !treeContainsText(tr, "截图识别译文") {
		t.Fatalf("expected vision translation result in popup: %s", formatTreeSummary(tr))
	}

	var sawScreenshot bool
	for _, call := range portal.Calls() {
		if call.Interface == "org.freedesktop.portal.Screenshot" && call.Method == "Screenshot" {
			sawScreenshot = true
			if call.Options["interactive"] != "true" {
				t.Fatalf("expected interactive=true in portal Screenshot options, got %+v", call.Options)
			}
		}
	}
	if !sawScreenshot {
		t.Fatalf("expected org.freedesktop.portal.Screenshot call, got %+v", portal.Calls())
	}
	if shimLog := s.readShimLog("spectacle"); shimLog != "" {
		t.Fatalf("expected spectacle not to be invoked on portal success, got %q", shimLog)
	}

	reqs := m.Requests()
	if len(reqs[0].Images) != 1 {
		t.Fatalf("expected 1 image in vision LLM request, got %d", len(reqs[0].Images))
	}
	img := reqs[0].Images[0]
	if img.Width != 200 || img.Height != 120 {
		t.Fatalf("unexpected image dimensions %dx%d, want 200x120", img.Width, img.Height)
	}
}

func TestE2E_F5_T1_ScreenshotPortalFallbackToSpectacle(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "Spectacle截图译文"})

	portal := startMockPortal(t, mockPortalOptions{
		ScreenshotCode: 2,
	})
	s.DBusAddr = portal.Addr

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("spectacle")
	s.setShimState("spectacle_mode", "ok")
	s.setShimState("fixture_png", writePNGFixture(t, s, "spectacle.png", 160, 90))

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	pollUntil(t, 5*time.Second, "Vision LLM receives spectacle fallback screenshot", func() bool {
		return len(m.Requests()) == 1
	})
	waitTreeContains(t, client, win.ID, 4*time.Second, "Spectacle截图译文")

	var sawPortal bool
	for _, call := range portal.Calls() {
		if call.Interface == "org.freedesktop.portal.Screenshot" && call.Method == "Screenshot" {
			sawPortal = true
		}
	}
	if !sawPortal {
		t.Fatalf("expected portal attempt before fallback, calls=%+v", portal.Calls())
	}
	if logText := s.readShimLog("spectacle"); !strings.Contains(logText, "--region --background --nonotify --new-instance --output") {
		t.Fatalf("unexpected spectacle args: %q", logText)
	}
	reqs := m.Requests()
	if len(reqs[0].Images) != 1 || reqs[0].Images[0].Width != 160 || reqs[0].Images[0].Height != 90 {
		t.Fatalf("unexpected spectacle vision images: %+v", reqs[0].Images)
	}
}

func TestE2E_F5_T1_ScreenshotFallbackChainGrimSlurpAndFlameshot(t *testing.T) {
	t.Run("grim_slurp", func(t *testing.T) {
		s := newSandbox(t)
		s.DBusAddr = "unix:path=" + filepath.Join(s.RuntimeDir, "no-dbus")
		m := newMockLLMServer(t)
		m.SetDefault("chat", MockResponse{Body: "Grim截图译文"})

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["disableSelectionToolbar"] = true
		cfg["popupPinned"] = true
		s.writeConfigJSON(cfg)

		s.IsolatePATH = true
		s.installShim("slurp")
		s.installShim("grim")
		s.setShimState("slurp_mode", "ok")
		s.setShimState("slurp_region", "10,20 100x80")
		s.setShimState("grim_mode", "ok")
		s.setShimState("fixture_png", writePNGFixture(t, s, "grim.png", 100, 80))

		proc := s.startGUI("--silent")
		client, win := dialMainWindow(t, proc)

		if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
			t.Fatalf("-c failed: %+v", res)
		}
		pollUntil(t, 5*time.Second, "Vision LLM receives grim screenshot", func() bool {
			return len(m.Requests()) == 1
		})
		waitTreeContains(t, client, win.ID, 4*time.Second, "Grim截图译文")

		if s.readShimLog("slurp") == "" {
			t.Fatalf("expected slurp invocation")
		}
		if glog := s.readShimLog("grim"); !strings.Contains(glog, "-g 10,20 100x80") {
			t.Fatalf("expected grim -g 10,20 100x80, got %q", glog)
		}
		if imgs := m.Requests()[0].Images; len(imgs) != 1 || imgs[0].Width != 100 || imgs[0].Height != 80 {
			t.Fatalf("unexpected grim vision images: %+v", imgs)
		}
	})

	t.Run("flameshot", func(t *testing.T) {
		s := newSandbox(t)
		s.DBusAddr = "unix:path=" + filepath.Join(s.RuntimeDir, "no-dbus")
		m := newMockLLMServer(t)
		m.SetDefault("chat", MockResponse{Body: "Flameshot截图译文"})

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["disableSelectionToolbar"] = true
		cfg["popupPinned"] = true
		s.writeConfigJSON(cfg)

		s.IsolatePATH = true
		s.installShim("flameshot")
		s.setShimState("flameshot_mode", "ok")
		s.setShimState("fixture_png", writePNGFixture(t, s, "flameshot.png", 110, 70))

		proc := s.startGUI("--silent")
		client, win := dialMainWindow(t, proc)

		if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
			t.Fatalf("-c failed: %+v", res)
		}
		pollUntil(t, 5*time.Second, "Vision LLM receives flameshot screenshot", func() bool {
			return len(m.Requests()) == 1
		})
		waitTreeContains(t, client, win.ID, 4*time.Second, "Flameshot截图译文")

		if flog := s.readShimLog("flameshot"); !strings.Contains(flog, "gui --raw") {
			t.Fatalf("expected flameshot gui --raw, got %q", flog)
		}
		if imgs := m.Requests()[0].Images; len(imgs) != 1 || imgs[0].Width != 110 || imgs[0].Height != 70 {
			t.Fatalf("unexpected flameshot vision images: %+v", imgs)
		}
	})
}

func TestE2E_F5_T1_SettingsPortalColorSchemeLightVsDark(t *testing.T) {
	runScheme := func(scheme uint32) color.RGBA {
		s := newSandbox(t)
		m := newMockLLMServer(t)
		portal := startMockPortal(t, mockPortalOptions{
			ColorScheme: &scheme,
		})
		s.DBusAddr = portal.Addr
		s.ExtraEnv["XDG_CURRENT_DESKTOP"] = "GNOME"
		s.ExtraEnv["KDE_FULL_SESSION"] = ""

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["theme"] = "system"
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		proc := s.startGUI("-p")
		client, win := dialMainWindow(t, proc)

		tr := waitTreeContains(t, client, win.ID, 4*time.Second, "保存设置")
		var saveBtn Node
		for _, n := range tr.Nodes {
			if n.Role == "button" && n.Label == "保存设置" {
				saveBtn = n
				break
			}
		}
		if saveBtn.ID == "" {
			t.Fatalf("missing 保存设置 button in tree")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.WaitIdle(ctx, win.ID)
		capRes, err := client.Capture(ctx, win.ID)
		if err != nil {
			t.Fatalf("Capture(scheme=%d): %v", scheme, err)
		}

		var sawReadAll bool
		for _, call := range portal.Calls() {
			if call.Interface == "org.freedesktop.portal.Settings" && call.Method == "ReadAll" {
				sawReadAll = true
			}
		}
		if !sawReadAll {
			t.Fatalf("expected Settings.ReadAll portal call for scheme=%d, calls=%+v", scheme, portal.Calls())
		}

		img := decodeCapturedPNG(t, capRes)
		// Sample inside the opaque footer row left of 保存设置 button.
		sx := float64(img.Bounds().Dx()) / win.Bounds.Width
		sy := float64(img.Bounds().Dy()) / win.Bounds.Height
		fx := int((saveBtn.Bounds.X - 40) * sx)
		fy := int((saveBtn.Bounds.Y + saveBtn.Bounds.Height/2) * sy)
		return sampleImagePixel(t, img, fx, fy)
	}

	darkPx := runScheme(1)
	lightPx := runScheme(2)

	if luma := colorLuma(darkPx); luma >= 80 {
		t.Fatalf("color-scheme=1 footer pixel %+v luminance = %d, want < 80 (dark)", darkPx, luma)
	}
	if luma := colorLuma(lightPx); luma <= 200 {
		t.Fatalf("color-scheme=2 footer pixel %+v luminance = %d, want > 200 (light)", lightPx, luma)
	}
}

func TestE2E_F5_T1_SettingsPortalAccentColorAppliedToPrimaryUI(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	scheme := uint32(2)
	accent := [3]float64{0.0, 0.85, 0.25}
	portal := startMockPortal(t, mockPortalOptions{
		ColorScheme: &scheme,
		AccentColor: &accent,
	})
	s.DBusAddr = portal.Addr
	s.ExtraEnv["XDG_CURRENT_DESKTOP"] = "GNOME"
	s.ExtraEnv["KDE_FULL_SESSION"] = ""

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["theme"] = "system"
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	proc := s.startGUI("-p")
	client, win := dialMainWindow(t, proc)

	tr := waitTreeContains(t, client, win.ID, 4*time.Second, "保存设置")
	var saveBtn Node
	for _, n := range tr.Nodes {
		if n.Role == "button" && n.Label == "保存设置" {
			saveBtn = n
			break
		}
	}
	if saveBtn.ID == "" {
		t.Fatalf("missing 保存设置 button")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = client.WaitIdle(ctx, win.ID)
	capRes, err := client.Capture(ctx, win.ID)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	img := decodeCapturedPNG(t, capRes)
	px := sampleNodeCornerColor(t, img, win, saveBtn.Bounds)
	if px.G <= 180 || px.R >= 40 || px.B >= 100 {
		t.Fatalf("expected green-dominant portal accent on 保存设置 button, got RGBA=%+v", px)
	}
}

func TestE2E_F5_T1_KDEGlobalsPaletteAndAccentParsed(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	// Use portal ColorScheme=1 (no AccentColor) so theme="system" deterministically
	// evaluates dark polarity matching [Colors:Window] BackgroundNormal=24,48,72
	// while [Colors:Selection] supplies the custom accent color.
	scheme := uint32(1)
	portal := startMockPortal(t, mockPortalOptions{
		ColorScheme: &scheme,
	})
	s.DBusAddr = portal.Addr
	s.ExtraEnv["XDG_CURRENT_DESKTOP"] = "KDE"
	s.ExtraEnv["KDE_FULL_SESSION"] = "true"

	kdeCfg := `[Colors:Window]
BackgroundNormal=24,48,72
ForegroundNormal=240,240,240

[Colors:Header]
BackgroundNormal=24,48,72

[Colors:Selection]
BackgroundNormal=220,60,30
ForegroundNormal=255,255,255
`
	if err := os.WriteFile(filepath.Join(s.ConfigHome, "kdeglobals"), []byte(kdeCfg), 0o644); err != nil {
		t.Fatalf("write kdeglobals: %v", err)
	}

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["theme"] = "system"
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	proc := s.startGUI("-p")
	client, win := dialMainWindow(t, proc)

	tr := waitTreeContains(t, client, win.ID, 4*time.Second, "保存设置")
	var saveBtn Node
	for _, n := range tr.Nodes {
		if n.Role == "button" && n.Label == "保存设置" {
			saveBtn = n
			break
		}
	}
	if saveBtn.ID == "" {
		t.Fatalf("missing 保存设置 button")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = client.WaitIdle(ctx, win.ID)
	capRes, err := client.Capture(ctx, win.ID)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	img := decodeCapturedPNG(t, capRes)
	sx := float64(img.Bounds().Dx()) / win.Bounds.Width
	sy := float64(img.Bounds().Dy()) / win.Bounds.Height
	bgPx := sampleImagePixel(t, img, int((saveBtn.Bounds.X-40)*sx), int((saveBtn.Bounds.Y+saveBtn.Bounds.Height/2)*sy))
	if absDiff(bgPx.R, 24) > 8 || absDiff(bgPx.G, 48) > 8 || absDiff(bgPx.B, 72) > 8 {
		t.Fatalf("expected KDE palette dark blue ~(24,48,72), got %+v", bgPx)
	}

	accentPx := sampleNodeCornerColor(t, img, win, saveBtn.Bounds)
	if absDiff(accentPx.R, 220) > 12 || absDiff(accentPx.G, 60) > 12 || absDiff(accentPx.B, 30) > 12 {
		t.Fatalf("expected KDE selection accent ~(220,60,30) on 保存设置, got %+v", accentPx)
	}
}

// ---------------------------------------------------------------------------
// F5 Tier 2: Cancellation, Out-of-Range, Cleanup, Missing Tools & Signals
// ---------------------------------------------------------------------------

func TestE2E_F5_T2_ScreenshotPortalUserCancelCode1SkipsFallback(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	portal := startMockPortal(t, mockPortalOptions{
		ScreenshotCode: 1,
	})
	s.DBusAddr = portal.Addr

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	s.IsolatePATH = true
	s.installShim("spectacle")
	s.setShimState("spectacle_mode", "ok")
	s.setShimState("fixture_png", writePNGFixture(t, s, "unused.png", 64, 64))

	proc := s.startGUI("--silent")
	client, win := dialMainWindow(t, proc)

	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	pollUntil(t, 4*time.Second, "portal Screenshot called and cancelled", func() bool {
		for _, call := range portal.Calls() {
			if call.Interface == "org.freedesktop.portal.Screenshot" && call.Method == "Screenshot" {
				return true
			}
		}
		return false
	})

	// Wait for UI idle so any erroneous fallback/popup update would have settled.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = client.WaitIdle(ctx, win.ID)
	time.Sleep(150 * time.Millisecond)

	if shimLog := s.readShimLog("spectacle"); shimLog != "" {
		t.Fatalf("expected spectacle NOT to run on portal user cancel (code=1), got %q", shimLog)
	}
	if got := len(m.Requests()); got != 0 {
		t.Fatalf("expected 0 LLM requests on user cancel, got %d", got)
	}
	wins, err := client.Windows(ctx)
	if err != nil {
		t.Fatalf("Windows(): %v", err)
	}
	for _, w := range wins {
		if w.ID == win.ID && w.Visible {
			t.Fatalf("expected popup to remain hidden after portal cancel")
		}
	}
}

func TestE2E_F5_T2_PendingPortalScreenshotCanceledCallsRequestClose(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	portal := startMockPortal(t, mockPortalOptions{
		ScreenshotPNG: makePNG(t, 120, 80),
		HoldRequest:   true,
	})
	s.DBusAddr = portal.Addr

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["disableSelectionToolbar"] = true
	cfg["popupPinned"] = true
	s.writeConfigJSON(cfg)

	proc := s.startGUI("--silent")
	_, _ = dialMainWindow(t, proc)

	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	pollUntil(t, 4*time.Second, "portal Screenshot request active", func() bool {
		for _, call := range portal.Calls() {
			if call.Interface == "org.freedesktop.portal.Screenshot" && call.Method == "Screenshot" {
				return true
			}
		}
		return false
	})

	// Opening settings (-p) cancels pending shotCancel, which must call Request.Close on D-Bus.
	if res := s.runForwardCLI(5*time.Second, "-p"); res.ExitCode != 0 {
		t.Fatalf("-p failed: %+v", res)
	}

	pollUntil(t, 4*time.Second, "org.freedesktop.portal.Request.Close called", func() bool {
		for _, call := range portal.Calls() {
			if call.Interface == "org.freedesktop.portal.Request" && call.Method == "Close" {
				return true
			}
		}
		return false
	})

	if got := len(m.Requests()); got != 0 {
		t.Fatalf("expected 0 LLM requests for cancelled screenshot, got %d", got)
	}
}

func TestE2E_F5_T2_ScreenshotTempURIRemovedPersistentURIPreserved(t *testing.T) {
	tempShotPath := func(t *testing.T, s *sandbox) string {
		p := filepath.Join(s.TmpDir, "temp_shot.png")
		if err := os.WriteFile(p, makePNG(t, 90, 60), 0o644); err != nil {
			t.Fatalf("write temp_shot.png: %v", err)
		}
		return p
	}
	keepShotPath := func(t *testing.T, s *sandbox) string {
		p := filepath.Join(s.DataHome, "keep_shot.png")
		if err := os.WriteFile(p, makePNG(t, 90, 60), 0o644); err != nil {
			t.Fatalf("write keep_shot.png: %v", err)
		}
		return p
	}

	t.Run("temp_uri_removed", func(t *testing.T) {
		s := newSandbox(t)
		m := newMockLLMServer(t)
		p := tempShotPath(t, s)
		portal := startMockPortal(t, mockPortalOptions{
			ScreenshotURI:  "file://" + p,
			ScreenshotCode: 0,
		})
		s.DBusAddr = portal.Addr

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["disableSelectionToolbar"] = true
		cfg["popupPinned"] = true
		s.writeConfigJSON(cfg)

		proc := s.startGUI("--silent")
		_, _ = dialMainWindow(t, proc)

		if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
			t.Fatalf("-c failed: %+v", res)
		}
		pollUntil(t, 5*time.Second, "Vision LLM receives temp screenshot", func() bool {
			return len(m.Requests()) == 1
		})
		pollUntil(t, 2*time.Second, "temp_shot.png removed", func() bool {
			_, err := os.Stat(p)
			return os.IsNotExist(err)
		})
	})

	t.Run("persistent_uri_preserved", func(t *testing.T) {
		s := newSandbox(t)
		m := newMockLLMServer(t)
		p := keepShotPath(t, s)
		portal := startMockPortal(t, mockPortalOptions{
			ScreenshotURI:  "file://" + p,
			ScreenshotCode: 0,
		})
		s.DBusAddr = portal.Addr

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["disableSelectionToolbar"] = true
		cfg["popupPinned"] = true
		s.writeConfigJSON(cfg)

		proc := s.startGUI("--silent")
		_, _ = dialMainWindow(t, proc)

		if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
			t.Fatalf("-c failed: %+v", res)
		}
		pollUntil(t, 5*time.Second, "Vision LLM receives persistent screenshot", func() bool {
			return len(m.Requests()) == 1
		})
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected persistent screenshot %s to be preserved, stat err=%v", p, err)
		}
	})
}

func TestE2E_F5_T2_ScreenshotAllToolsMissingAndSpectacleHang(t *testing.T) {
	t.Run("all_tools_missing", func(t *testing.T) {
		s := newSandbox(t)
		s.DBusAddr = "unix:path=" + filepath.Join(s.RuntimeDir, "no-dbus")
		m := newMockLLMServer(t)

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["disableSelectionToolbar"] = true
		cfg["popupPinned"] = true
		s.writeConfigJSON(cfg)
		s.IsolatePATH = true

		proc := s.startGUI("--silent")
		client, win := dialMainWindow(t, proc)

		if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
			t.Fatalf("-c failed: %+v", res)
		}
		tr := waitTreeContains(t, client, win.ID, 4*time.Second, "截图失败")
		if !treeContainsText(tr, "未找到可用的区域截图工具") {
			t.Fatalf("expected missing screenshot tools guidance, got:\n%s", formatTreeSummary(tr))
		}
		if got := len(m.Requests()); got != 0 {
			t.Fatalf("expected 0 LLM requests when tools missing, got %d", got)
		}
	})

	t.Run("spectacle_hang_cancel", func(t *testing.T) {
		s := newSandbox(t)
		s.DBusAddr = "unix:path=" + filepath.Join(s.RuntimeDir, "no-dbus")
		m := newMockLLMServer(t)

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["disableSelectionToolbar"] = true
		cfg["popupPinned"] = true
		s.writeConfigJSON(cfg)

		s.IsolatePATH = true
		s.installShim("spectacle")
		s.setShimState("spectacle_mode", "hang")

		proc := s.startGUI("--silent")
		client, win := dialMainWindow(t, proc)

		if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
			t.Fatalf("-c failed: %+v", res)
		}
		pollUntil(t, 4*time.Second, "hanging spectacle started", func() bool {
			return s.readShimLog("spectacle") != ""
		})

		if res := s.runForwardCLI(5*time.Second, "-p"); res.ExitCode != 0 {
			t.Fatalf("-p cancel failed: %+v", res)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := client.WaitIdle(ctx, win.ID); err != nil {
			t.Fatalf("GUI failed to stay responsive after cancelling hanging spectacle: %v", err)
		}
		start := time.Now()
		proc.Stop(t)
		if elapsed := time.Since(start); elapsed >= 3*time.Second {
			t.Fatalf("Stop took %v, hanging spectacle leaked", elapsed)
		}
	})
}

func TestE2E_F5_T2_SettingsPortalOutOfRangeValuesAndExplicitOverride(t *testing.T) {
	t.Run("out_of_range_under_system", func(t *testing.T) {
		s := newSandbox(t)
		m := newMockLLMServer(t)

		badScheme := uint32(3)
		badAccent := [3]float64{-1, -1, -1}
		portal := startMockPortal(t, mockPortalOptions{
			ColorScheme: &badScheme,
			AccentColor: &badAccent,
		})
		s.DBusAddr = portal.Addr
		s.ExtraEnv["XDG_CURRENT_DESKTOP"] = "GNOME"
		s.ExtraEnv["KDE_FULL_SESSION"] = ""

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["theme"] = "system"
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		proc := s.startGUI("-p")
		client, win := dialMainWindow(t, proc)

		tr := waitTreeContains(t, client, win.ID, 4*time.Second, "保存设置")
		var saveBtn Node
		for _, n := range tr.Nodes {
			if n.Role == "button" && n.Label == "保存设置" {
				saveBtn = n
				break
			}
		}
		if saveBtn.ID == "" {
			t.Fatalf("missing 保存设置 button")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.WaitIdle(ctx, win.ID)
		capRes, err := client.Capture(ctx, win.ID)
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}

		var sawReadAll bool
		for _, call := range portal.Calls() {
			if call.Interface == "org.freedesktop.portal.Settings" && call.Method == "ReadAll" {
				sawReadAll = true
			}
		}
		if !sawReadAll {
			t.Fatalf("expected Settings.ReadAll portal call under theme='system', calls=%+v", portal.Calls())
		}

		img := decodeCapturedPNG(t, capRes)
		sx := float64(img.Bounds().Dx()) / win.Bounds.Width
		sy := float64(img.Bounds().Dy()) / win.Bounds.Height
		footerPx := sampleImagePixel(t, img, int((saveBtn.Bounds.X-40)*sx), int((saveBtn.Bounds.Y+saveBtn.Bounds.Height/2)*sy))
		if luma := colorLuma(footerPx); luma <= 200 {
			t.Fatalf("expected light/default footer when out-of-range color-scheme=3 is ignored under theme='system', got %+v (luma=%d)", footerPx, luma)
		}
		accentPx := sampleNodeCornerColor(t, img, win, saveBtn.Bounds)
		// Invalid (-1,-1,-1) accent must be rejected so default Win11 blue (#0067c0) remains.
		if accentPx.B <= accentPx.R || accentPx.B < 140 || accentPx.R >= 50 {
			t.Fatalf("expected default #0067c0 blue accent when portal accent (-1,-1,-1) rejected, got %+v", accentPx)
		}
	})

	t.Run("explicit_light_overrides_dark_portal", func(t *testing.T) {
		s := newSandbox(t)
		m := newMockLLMServer(t)

		darkScheme := uint32(1)
		portal := startMockPortal(t, mockPortalOptions{
			ColorScheme: &darkScheme,
		})
		s.DBusAddr = portal.Addr
		s.ExtraEnv["XDG_CURRENT_DESKTOP"] = "GNOME"
		s.ExtraEnv["KDE_FULL_SESSION"] = ""

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["theme"] = "light"
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		proc := s.startGUI("-p")
		client, win := dialMainWindow(t, proc)

		tr := waitTreeContains(t, client, win.ID, 4*time.Second, "保存设置")
		var saveBtn Node
		for _, n := range tr.Nodes {
			if n.Role == "button" && n.Label == "保存设置" {
				saveBtn = n
				break
			}
		}
		if saveBtn.ID == "" {
			t.Fatalf("missing 保存设置 button")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.WaitIdle(ctx, win.ID)
		capRes, err := client.Capture(ctx, win.ID)
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}

		var sawReadAll bool
		for _, call := range portal.Calls() {
			if call.Interface == "org.freedesktop.portal.Settings" && call.Method == "ReadAll" {
				sawReadAll = true
			}
		}
		if !sawReadAll {
			t.Fatalf("expected Settings.ReadAll portal call with ColorScheme=1, calls=%+v", portal.Calls())
		}

		img := decodeCapturedPNG(t, capRes)
		sx := float64(img.Bounds().Dx()) / win.Bounds.Width
		sy := float64(img.Bounds().Dy()) / win.Bounds.Height
		footerPx := sampleImagePixel(t, img, int((saveBtn.Bounds.X-40)*sx), int((saveBtn.Bounds.Y+saveBtn.Bounds.Height/2)*sy))
		if luma := colorLuma(footerPx); luma <= 200 {
			t.Fatalf("explicit theme='light' overridden by dark portal scheme=1: %+v (luma=%d)", footerPx, luma)
		}
	})
}

func TestE2E_F5_T2_SettingsPortalDynamicSettingChanged(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	light := uint32(2)
	portal := startMockPortal(t, mockPortalOptions{
		ColorScheme: &light,
	})
	s.DBusAddr = portal.Addr
	s.ExtraEnv["XDG_CURRENT_DESKTOP"] = "GNOME"
	s.ExtraEnv["KDE_FULL_SESSION"] = ""

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["theme"] = "system"
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	proc := s.startGUI("-p")
	client, win := dialMainWindow(t, proc)

	tr := waitTreeContains(t, client, win.ID, 4*time.Second, "保存设置")
	var saveBtn Node
	for _, n := range tr.Nodes {
		if n.Role == "button" && n.Label == "保存设置" {
			saveBtn = n
			break
		}
	}
	if saveBtn.ID == "" {
		t.Fatalf("missing 保存设置 button")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = client.WaitIdle(ctx, win.ID)
	capInit, err := client.Capture(ctx, win.ID)
	if err != nil {
		t.Fatalf("initial Capture: %v", err)
	}
	imgInit := decodeCapturedPNG(t, capInit)
	sx := float64(imgInit.Bounds().Dx()) / win.Bounds.Width
	sy := float64(imgInit.Bounds().Dy()) / win.Bounds.Height
	initFooter := sampleImagePixel(t, imgInit, int((saveBtn.Bounds.X-40)*sx), int((saveBtn.Bounds.Y+saveBtn.Bounds.Height/2)*sy))
	if colorLuma(initFooter) <= 200 {
		t.Fatalf("expected initial light theme footer (>200 luma), got %+v", initFooter)
	}

	// Emit SettingChanged signals dynamically while app is running without restarting.
	// Keep the polling window strictly below portalAppearanceCacheTTL (2s) so expired-TTL
	// ReadAll polling cannot mask a missing SettingChanged signal listener.
	portal.SetColorScheme(1)
	portal.SetAccentColor(0.9, 0.2, 0.5)

	deadline := time.Now().Add(1000 * time.Millisecond)
	var flippedDark, updatedAccent bool
	var lastFooter, lastAccent color.RGBA
	for time.Now().Before(deadline) {
		capCur, err := client.Capture(ctx, win.ID)
		if err == nil {
			imgCur := decodeCapturedPNG(t, capCur)
			lastFooter = sampleImagePixel(t, imgCur, int((saveBtn.Bounds.X-40)*sx), int((saveBtn.Bounds.Y+saveBtn.Bounds.Height/2)*sy))
			lastAccent = sampleNodeCornerColor(t, imgCur, win, saveBtn.Bounds)
			flippedDark = colorLuma(lastFooter) < 80
			updatedAccent = lastAccent.R > 180 && lastAccent.G < 90 && lastAccent.B > 90
			if flippedDark && updatedAccent {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("dynamic SettingChanged signal did not live-update UI within 1s (footer=%+v dark=%v, accent=%+v updated=%v)",
		lastFooter, flippedDark, lastAccent, updatedAccent)
}

var _ = fmt.Sprintf
