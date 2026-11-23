//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Cross-OS local helpers for F6 (Dict), F7 (Vision), F8 (LRU Cache)
// ---------------------------------------------------------------------------

type tcvStreamGate struct {
	HoldBeforeFirstChunk bool
	FirstChunk           string
	RestChunks           []string

	Started  chan struct{}
	Release  chan struct{}
	Canceled atomic.Bool

	startOnce   sync.Once
	releaseOnce sync.Once
	mu          sync.Mutex
	reqs        []RecordedRequest
}

func (g *tcvStreamGate) Close() {
	g.releaseOnce.Do(func() {
		close(g.Release)
	})
}

func (g *tcvStreamGate) WaitStarted(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-g.Started:
	case <-time.After(timeout):
		t.Fatalf("timed out after %v waiting for gated SSE stream to start", timeout)
	}
}

func (g *tcvStreamGate) Requests() []RecordedRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]RecordedRequest, len(g.reqs))
	copy(out, g.reqs)
	return out
}

type tcvControlledLLM struct {
	Mock   *MockLLMServer
	Server *httptest.Server

	mu          sync.Mutex
	gate        *tcvStreamGate
	activeGates []*tcvStreamGate
}

func tcvNewControlledLLM(t *testing.T) *tcvControlledLLM {
	t.Helper()
	m := newMockLLMServer(t)
	c := &tcvControlledLLM{Mock: m}
	c.Server = httptest.NewServer(http.HandlerFunc(c.handle))
	t.Cleanup(func() {
		c.Disarm()
		c.Server.Close()
	})
	return c
}

func (c *tcvControlledLLM) V1URL() string {
	return c.Server.URL + "/v1"
}

func (c *tcvControlledLLM) ArmGate(holdBeforeFirst bool, firstChunk string, restChunks ...string) *tcvStreamGate {
	g := &tcvStreamGate{
		HoldBeforeFirstChunk: holdBeforeFirst,
		FirstChunk:           firstChunk,
		RestChunks:           restChunks,
		Started:              make(chan struct{}),
		Release:              make(chan struct{}),
	}
	c.mu.Lock()
	if c.gate != nil {
		c.gate.Close()
	}
	c.gate = g
	c.activeGates = append(c.activeGates, g)
	c.mu.Unlock()
	return g
}

func (c *tcvControlledLLM) Disarm() {
	c.mu.Lock()
	c.gate = nil
	gates := c.activeGates
	c.activeGates = nil
	c.mu.Unlock()
	for _, g := range gates {
		g.Close()
	}
}

func (c *tcvControlledLLM) handle(w http.ResponseWriter, r *http.Request) {
	route := classifyRoute(r.Method, r.URL.Path)
	c.mu.Lock()
	g := c.gate
	c.mu.Unlock()

	if g == nil || route != "chat" {
		c.Mock.Server.Config.Handler.ServeHTTP(w, r)
		return
	}

	// Consume gate once per gated request unless caller re-arms.
	c.mu.Lock()
	if c.gate == g {
		c.gate = nil
	}
	c.mu.Unlock()

	bodyBytes, _ := io.ReadAll(r.Body)
	rec := RecordedRequest{
		Method:    r.Method,
		Path:      r.URL.Path,
		RawQuery:  r.URL.RawQuery,
		Headers:   r.Header.Clone(),
		Body:      bodyBytes,
		Timestamp: time.Now(),
	}
	parseChatPayload(bodyBytes, &rec)

	g.mu.Lock()
	g.reqs = append(g.reqs, rec)
	g.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	writeDelta := func(delta string) {
		if delta == "" {
			return
		}
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{"content": delta},
				},
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}

	if !g.HoldBeforeFirstChunk && g.FirstChunk != "" {
		writeDelta(g.FirstChunk)
	}

	g.startOnce.Do(func() {
		close(g.Started)
	})

	select {
	case <-g.Release:
	case <-r.Context().Done():
		g.Canceled.Store(true)
		return
	}

	if g.HoldBeforeFirstChunk && g.FirstChunk != "" {
		writeDelta(g.FirstChunk)
	}
	for _, chunk := range g.RestChunks {
		if r.Context().Err() != nil {
			g.Canceled.Store(true)
			return
		}
		writeDelta(chunk)
	}

	_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func tcvPollUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for: %s", timeout, desc)
}

func tcvOpenMainPanel(t *testing.T, s *sandbox, args ...string) (*GUIProcess, *AutomationClient, WindowInfo) {
	t.Helper()
	proc := s.startGUI(args...)

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
	return proc, client, win
}

func tcvNodeMatchesText(n Node, substr string) bool {
	return strings.Contains(n.Label, substr) ||
		strings.Contains(n.Value, substr) ||
		strings.Contains(n.Placeholder, substr) ||
		strings.Contains(n.Description, substr)
}

func tcvTreeContains(tr TreeResult, substr string) bool {
	for _, n := range tr.Nodes {
		if tcvNodeMatchesText(n, substr) {
			return true
		}
	}
	return false
}

func tcvFindNode(tr TreeResult, pred func(Node) bool) (Node, bool) {
	for _, n := range tr.Nodes {
		if pred(n) {
			return n, true
		}
	}
	return Node{}, false
}

func tcvWaitTree(t *testing.T, client *AutomationClient, winID int, timeout time.Duration, desc string, pred func(TreeResult) bool) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	tr, err := client.WaitTree(ctx, winID, pred)
	if err != nil {
		t.Fatalf("WaitTree(%s): %v", desc, err)
	}
	return tr
}

func tcvWaitTreeFast(t *testing.T, client *AutomationClient, winID int, timeout time.Duration, desc string, pred func(TreeResult) bool) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	var last TreeResult
	var lastErr error
	for {
		tr, err := client.Tree(ctx, winID)
		if err == nil {
			last = tr
			if pred(tr) {
				return tr
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			t.Fatalf("WaitTreeFast(%s) timed out (%v, lastErr=%v)\n%s", desc, ctx.Err(), lastErr, formatTreeSummary(last))
		case <-ticker.C:
		}
	}
}

func tcvWaitText(t *testing.T, client *AutomationClient, winID int, timeout time.Duration, substr string) TreeResult {
	t.Helper()
	return tcvWaitTree(t, client, winID, timeout, fmt.Sprintf("text %q", substr), func(tr TreeResult) bool {
		return tcvTreeContains(tr, substr)
	})
}

func tcvSwitchTab(t *testing.T, client *AutomationClient, winID int, label string) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	btn, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "button" && n.Label == label
	})
	if err != nil {
		t.Fatalf("WaitNode tab %q: %v", label, err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press tab %q: %v", label, err)
	}
	if err := client.WaitIdle(ctx, winID); err != nil {
		t.Fatalf("WaitIdle tab %q: %v", label, err)
	}
	tr, err := client.Tree(ctx, winID)
	if err != nil {
		t.Fatalf("Tree tab %q: %v", label, err)
	}
	return tr
}

func tcvIsPopupSubmitBtn(n Node) bool {
	return n.Role == "button" &&
		(n.Label == "翻译" || n.Label == "重新翻译") &&
		n.Key == nil &&
		n.Description == ""
}

func tcvSetPopupSource(t *testing.T, client *AutomationClient, winID int, source string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tr, err := client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		if _, ok := tcvFindNode(tr, func(n Node) bool {
			return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
		}); ok {
			return true
		}
		_, ok := tcvFindNode(tr, func(n Node) bool {
			return n.Role == "button" && strings.Contains(n.Description, "点击编辑原文")
		})
		return ok
	})
	if err != nil {
		t.Fatalf("WaitTree popup editor/preview: %v", err)
	}
	editor, ok := tcvFindNode(tr, func(n Node) bool {
		return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
	})
	if !ok {
		preview, _ := tcvFindNode(tr, func(n Node) bool {
			return n.Role == "button" && strings.Contains(n.Description, "点击编辑原文")
		})
		if err := client.Act(ctx, ActParams{Window: winID, ID: preview.ID, Action: "press"}); err != nil {
			t.Fatalf("open popup editor: %v", err)
		}
		editor, _, err = client.WaitNode(ctx, winID, func(n Node) bool {
			return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
		})
		if err != nil {
			t.Fatalf("WaitNode popup editor: %v", err)
		}
	}

	if err := client.Act(ctx, ActParams{Window: winID, ID: editor.ID, Action: "setValue", Text: source}); err != nil {
		t.Fatalf("setValue popup source %q: %v", source, err)
	}
	if err := client.WaitIdle(ctx, winID); err != nil {
		t.Fatalf("WaitIdle after setValue: %v", err)
	}
}

func tcvStartPopupTranslation(t *testing.T, client *AutomationClient, winID int, source string) {
	t.Helper()
	tcvSetPopupSource(t, client, winID, source)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tr, err := client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		_, ok := tcvFindNode(tr, func(n Node) bool {
			return tcvIsPopupSubmitBtn(n) && !n.HasState("disabled")
		})
		return ok
	})
	if err != nil {
		t.Fatalf("WaitTree enabled popup translate button: %v", err)
	}
	btn, _ := tcvFindNode(tr, tcvIsPopupSubmitBtn)
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press popup translate button: %v", err)
	}
}

func tcvTranslateInPopup(t *testing.T, client *AutomationClient, winID int, source, wantResultSubstr string) TreeResult {
	t.Helper()
	tcvStartPopupTranslation(t, client, winID, source)
	return tcvWaitTree(t, client, winID, 6*time.Second, fmt.Sprintf("popup result %q", wantResultSubstr), func(tr TreeResult) bool {
		_, hasRetranslate := tcvFindNode(tr, func(n Node) bool {
			return n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
		})
		return hasRetranslate && tcvTreeContains(tr, wantResultSubstr)
	})
}

func tcvSendPopupCtrlEnter(t *testing.T, client *AutomationClient, winID int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	tr, err := client.Tree(ctx, winID)
	if err != nil {
		t.Fatalf("Tree before Ctrl+Enter: %v", err)
	}
	if focusTarget, ok := tcvFindNode(tr, func(n Node) bool {
		return n.HasState("focusable") && (strings.Contains(n.Description, "点击编辑原文") ||
			(n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "") ||
			(n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")))
	}); ok {
		_ = client.Act(ctx, ActParams{Window: winID, ID: focusTarget.ID, Action: "focus"})
		_ = client.WaitIdle(ctx, winID)
	}
	if err := client.Input(ctx, winID, InputEvent{Type: "key", Key: "Ctrl+Enter"}); err != nil {
		t.Fatalf("send Ctrl+Enter: %v", err)
	}
}

func tcvClickRetranslate(t *testing.T, client *AutomationClient, winID int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	btn, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
	})
	if err != nil {
		t.Fatalf("WaitNode enabled 重新翻译: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press 重新翻译: %v", err)
	}
}

func tcvSelectOption(t *testing.T, client *AutomationClient, winID int, selectLabel, optionLabel string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	trigger, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "popUpButton" && n.Label == selectLabel
	})
	if err != nil {
		t.Fatalf("WaitNode select %q: %v", selectLabel, err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: trigger.ID, Action: "press"}); err != nil {
		t.Fatalf("open select %q: %v", selectLabel, err)
	}

	var optNode Node
	_, err = client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		for _, n := range tr.Nodes {
			if n.Role != "listItem" {
				continue
			}
			if n.Label == optionLabel {
				optNode = n
				return true
			}
			for _, child := range tr.Nodes {
				if child.Parent == n.ID && child.Label == optionLabel {
					optNode = n
					return true
				}
			}
		}
		return false
	})
	if err != nil {
		t.Fatalf("WaitTree option %q in %q: %v", optionLabel, selectLabel, err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: optNode.ID, Action: "press"}); err != nil {
		t.Fatalf("select option %q: %v", optionLabel, err)
	}
	if err := client.WaitIdle(ctx, winID); err != nil {
		t.Fatalf("WaitIdle after select %q: %v", optionLabel, err)
	}
}

func tcvCyclePopupMode(t *testing.T, client *AutomationClient, winID int, wantMode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	for i := 0; i < 8; i++ {
		tr, err := client.Tree(ctx, winID)
		if err != nil {
			t.Fatalf("Tree cycling mode: %v", err)
		}
		btn, ok := tcvFindNode(tr, func(n Node) bool {
			return n.Role == "button" && n.Description == "切换模式"
		})
		if !ok {
			t.Fatalf("mode button not found in tree:\n%s", formatTreeSummary(tr))
		}
		hasModeLabel := strings.Contains(btn.Label, wantMode)
		if !hasModeLabel {
			for _, ch := range tr.Nodes {
				if ch.Parent == btn.ID && ch.Label == wantMode {
					hasModeLabel = true
					break
				}
			}
		}
		if hasModeLabel {
			return
		}
		if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
			t.Fatalf("press mode button: %v", err)
		}
		_ = client.WaitIdle(ctx, winID)
	}
	t.Fatalf("mode %q not reached after 8 cycles", wantMode)
}

func tcvSetInputTabText(t *testing.T, client *AutomationClient, winID int, source string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	editor, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "textField" && strings.Contains(n.Placeholder, "在此输入要翻译的内容")
	})
	if err != nil {
		t.Fatalf("WaitNode input tab editor: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: editor.ID, Action: "setValue", Text: source}); err != nil {
		t.Fatalf("setValue input tab source: %v", err)
	}
}

func tcvPressInputTabTranslate(t *testing.T, client *AutomationClient, winID int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	btn, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "button" && n.Label == "翻译 (Ctrl+Enter)" && !n.HasState("disabled")
	})
	if err != nil {
		t.Fatalf("WaitNode input tab translate button: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press input tab translate button: %v", err)
	}
}

func tcvTranslateInInputTab(t *testing.T, client *AutomationClient, winID int, source, wantResultSubstr string) TreeResult {
	t.Helper()
	tcvSetInputTabText(t, client, winID, source)
	tcvPressInputTabTranslate(t, client, winID)
	return tcvWaitText(t, client, winID, 6*time.Second, wantResultSubstr)
}

func tcvClearInputTab(t *testing.T, client *AutomationClient, winID int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	btn, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "button" && n.Label == "清空"
	})
	if err != nil {
		t.Fatalf("WaitNode input tab 清空: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press 清空: %v", err)
	}
	if err := client.WaitIdle(ctx, winID); err != nil {
		t.Fatalf("WaitIdle after 清空: %v", err)
	}
}

func tcvWritePNGFile(t *testing.T, s *sandbox, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(s.TmpDir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", p, err)
	}
	return p
}

func tcvMakeTransparentPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x < 16 || y < 16 || x >= width-16 || y >= height-16 {
				img.SetNRGBA(x, y, color.NRGBA{R: 20, G: 30, B: 40, A: 255})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 0})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode transparent PNG: %v", err)
	}
	return buf.Bytes()
}

func tcvMakePixelBombPNG(w, h uint32) []byte {
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA

	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	writeChunk := func(typ string, data []byte) {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
		buf.Write(lenBuf[:])
		buf.WriteString(typ)
		buf.Write(data)
		crc := crc32.NewIEEE()
		_, _ = crc.Write([]byte(typ))
		_, _ = crc.Write(data)
		var crcBuf [4]byte
		binary.BigEndian.PutUint32(crcBuf[:], crc.Sum32())
		buf.Write(crcBuf[:])
	}
	writeChunk("IHDR", ihdr[:])
	writeChunk("IEND", nil)
	return buf.Bytes()
}

func tcvSetupSpectacleFixture(t *testing.T, s *sandbox, pngBytes []byte) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Linux spectacle screenshot shim only runs on non-Windows hosts")
	}
	s.DBusAddr = "unix:path=" + filepath.Join(s.RuntimeDir, "no-dbus")
	s.IsolatePATH = true
	s.installShim("spectacle")
	s.setShimState("spectacle_mode", "ok")
	s.setShimState("fixture_png", tcvWritePNGFile(t, s, "shot.png", pngBytes))
}

func tcvDecodeImageDataURI(t *testing.T, dataURI string) image.Image {
	t.Helper()
	_, b64, ok := strings.Cut(dataURI, ",")
	if !ok {
		t.Fatalf("invalid data URI: %q", dataURI)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 decode image: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("jpeg decode: %v", err)
	}
	return img
}

// ---------------------------------------------------------------------------
// F6 Tier 1: Core Tiered Word Lookup & Embedded Offline Dictionary (6 tests)
// ---------------------------------------------------------------------------

func TestE2E_F6_T1_SingleWordShowsOfflineDictBeforeLLMFinishes(t *testing.T) {
	s := newSandbox(t)
	c := tcvNewControlledLLM(t)
	gate := c.ArmGate(true, "算法语境释义")

	cfg := defaultLoopbackConfig(c.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvStartPopupTranslation(t, client, win.ID, "algorithm")

	gate.WaitStarted(t, 5*time.Second)

	// While LLM SSE stream is still held before chunk 1, offline dict card
	// AND streaming indicator must already be visible simultaneously.
	tr := tcvWaitTree(t, client, win.ID, 4*time.Second, "offline dict during active LLM stream", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "离线词典") &&
			tcvTreeContains(tr, "algorithm") &&
			tcvTreeContains(tr, "n.") &&
			tcvTreeContains(tr, "/") &&
			tcvTreeContains(tr, "正在翻译…")
	})
	if len(gate.Requests()) != 1 {
		t.Fatalf("expected 1 in-flight LLM request, got %d", len(gate.Requests()))
	}
	if tcvTreeContains(tr, "算法语境释义") {
		t.Fatalf("LLM result appeared before gate released:\n%s", formatTreeSummary(tr))
	}

	gate.Close()
	tr = tcvWaitTree(t, client, win.ID, 5*time.Second, "completed LLM result alongside offline dict", func(tr TreeResult) bool {
		_, hasRetranslate := tcvFindNode(tr, func(n Node) bool {
			return n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
		})
		return hasRetranslate && tcvTreeContains(tr, "算法语境释义")
	})
	if !tcvTreeContains(tr, "离线词典") || !tcvTreeContains(tr, "algorithm") {
		t.Fatalf("offline dictionary card disappeared after LLM completion:\n%s", formatTreeSummary(tr))
	}
}

func TestE2E_F6_T1_MultiWordAndParagraphBypassOfflineDict(t *testing.T) {
	s := newSandbox(t)
	c := tcvNewControlledLLM(t)

	cfg := defaultLoopbackConfig(c.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)

	// 1. Short multi-word phrase held mid-stream: must NOT show offline dict at any stage.
	gate := c.ArmGate(true, "你好吗")
	tcvStartPopupTranslation(t, client, win.ID, "how are you")
	gate.WaitStarted(t, 5*time.Second)

	tr := tcvWaitText(t, client, win.ID, 4*time.Second, "正在翻译…")
	if tcvTreeContains(tr, "离线词典") {
		t.Fatalf("unexpected offline dictionary card on multi-word phrase during stream:\n%s", formatTreeSummary(tr))
	}
	gate.Close()
	tr = tcvWaitText(t, client, win.ID, 5*time.Second, "你好吗")
	if tcvTreeContains(tr, "离线词典") {
		t.Fatalf("unexpected offline dictionary card on multi-word phrase after completion:\n%s", formatTreeSummary(tr))
	}

	// 2. Multi-sentence paragraph.
	paragraph := "Distributed systems require careful consensus. Leader election prevents split brain."
	c.Mock.SetDefault("chat", MockResponse{Body: "分布式系统需要谨慎的共识机制。"})
	tr = tcvTranslateInPopup(t, client, win.ID, paragraph, "分布式系统需要谨慎的共识机制。")
	if tcvTreeContains(tr, "离线词典") {
		t.Fatalf("unexpected offline dictionary card on paragraph:\n%s", formatTreeSummary(tr))
	}
	reqs := c.Mock.Requests()
	if len(reqs) == 0 || !strings.Contains(reqs[len(reqs)-1].Messages[len(reqs[len(reqs)-1].Messages)-1].Text, paragraph) {
		t.Fatalf("expected paragraph text sent verbatim to LLM, got %+v", reqs)
	}
}

func TestE2E_F6_T1_PunctuationHyphenContractionAndCaseNormalization(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	cases := []struct {
		input      string
		wantWord   string
		wantResult string
		wantIPA    bool
	}{
		{input: "hello!", wantWord: "hello", wantResult: "你好译文", wantIPA: true},
		{input: "(test)", wantWord: "test", wantResult: "测试译文", wantIPA: true},
		{input: "well-known", wantWord: "well-known", wantResult: "知名译文", wantIPA: false},
		{input: "don't", wantWord: "don't", wantResult: "不要译文", wantIPA: true},
		{input: "Serendipity", wantWord: "serendipity", wantResult: "意外发现译文", wantIPA: true},
	}

	for _, tc := range cases {
		m.ClearRequests()
		m.SetDefault("chat", MockResponse{Body: tc.wantResult})

		tr := tcvTranslateInInputTab(t, client, win.ID, tc.input, tc.wantResult)
		if !tcvTreeContains(tr, "离线词典") {
			t.Fatalf("input %q: expected 离线词典 card, got:\n%s", tc.input, formatTreeSummary(tr))
		}
		if !tcvTreeContains(tr, tc.wantWord) {
			t.Fatalf("input %q: expected normalized word %q in dict card, got:\n%s", tc.input, tc.wantWord, formatTreeSummary(tr))
		}
		if tc.wantIPA && !tcvTreeContains(tr, "/") {
			t.Fatalf("input %q: expected IPA slashes in dict card, got:\n%s", tc.input, formatTreeSummary(tr))
		}
		reqs := m.Requests()
		if len(reqs) != 1 || len(reqs[0].Messages) == 0 {
			t.Fatalf("input %q: expected 1 LLM request with messages, got %+v", tc.input, reqs)
		}
		userMsg := reqs[0].Messages[len(reqs[0].Messages)-1].Text
		if !strings.Contains(userMsg, tc.input) {
			t.Fatalf("input %q: expected original input sent to LLM, got user message %q", tc.input, userMsg)
		}

		tcvClearInputTab(t, client, win.ID)
	}
}

func TestE2E_F6_T1_InflectionAndPossessiveStemming(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	cases := []struct {
		input      string
		wantLemma  string
		wantResult string
	}{
		{input: "algorithms", wantLemma: "algorithm", wantResult: "复数算法译文"},
		{input: "debugging", wantLemma: "debug", wantResult: "调试中译文"},
		{input: "went", wantLemma: "go", wantResult: "去过译文"},
		{input: "algorithm's", wantLemma: "algorithm", wantResult: "算法的译文"},
	}

	for _, tc := range cases {
		m.ClearRequests()
		m.SetDefault("chat", MockResponse{Body: tc.wantResult})

		tr := tcvTranslateInInputTab(t, client, win.ID, tc.input, tc.wantResult)
		if !tcvTreeContains(tr, "离线词典") {
			t.Fatalf("input %q: expected 离线词典 card, got:\n%s", tc.input, formatTreeSummary(tr))
		}
		if !tcvTreeContains(tr, tc.wantLemma) {
			t.Fatalf("input %q: expected base lemma %q in dict card, got:\n%s", tc.input, tc.wantLemma, formatTreeSummary(tr))
		}
		reqs := m.Requests()
		if len(reqs) != 1 || !strings.Contains(reqs[0].Messages[len(reqs[0].Messages)-1].Text, tc.input) {
			t.Fatalf("input %q: expected original inflected input sent to LLM, got %+v", tc.input, reqs)
		}

		tcvClearInputTab(t, client, win.ID)
	}
}

func TestE2E_F6_T1_InputTabDisplaysDictCardAndClearsCleanly(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "短暂的译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	tr := tcvTranslateInInputTab(t, client, win.ID, "ephemeral", "短暂的译文")
	if !tcvTreeContains(tr, "离线词典") || !tcvTreeContains(tr, "ephemeral") {
		t.Fatalf("expected offline dict card in input tab:\n%s", formatTreeSummary(tr))
	}

	tcvClearInputTab(t, client, win.ID)
	tr = tcvWaitTree(t, client, win.ID, 4*time.Second, "cleared input tab", func(tr TreeResult) bool {
		return !tcvTreeContains(tr, "离线词典") && !tcvTreeContains(tr, "短暂的译文")
	})

	m.SetDefault("chat", MockResponse{Body: "你好世界"})
	tr = tcvTranslateInInputTab(t, client, win.ID, "hello world", "你好世界")
	if tcvTreeContains(tr, "离线词典") {
		t.Fatalf("expected no offline dict card after clearing and translating phrase:\n%s", formatTreeSummary(tr))
	}
}

func TestE2E_F6_T1_NonTranslateModesBypassOfflineDict(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "模式输出-翻译"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tr := tcvTranslateInPopup(t, client, win.ID, "algorithm", "模式输出-翻译")
	if !tcvTreeContains(tr, "离线词典") {
		t.Fatalf("expected 离线词典 in 翻译 mode:\n%s", formatTreeSummary(tr))
	}

	// Cycling mode auto-retranslates when a result exists.
	m.SetDefault("chat", MockResponse{Body: "模式输出-润色"})
	tcvCyclePopupMode(t, client, win.ID, "润色")
	tr = tcvWaitTree(t, client, win.ID, 5*time.Second, "润色 mode result", func(tr TreeResult) bool {
		_, hasRetranslate := tcvFindNode(tr, func(n Node) bool {
			return n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
		})
		return hasRetranslate && tcvTreeContains(tr, "模式输出-润色")
	})
	if tcvTreeContains(tr, "离线词典") {
		t.Fatalf("expected 离线词典 to be suppressed in 润色 mode:\n%s", formatTreeSummary(tr))
	}

	// Cycle back to 翻译 and confirm 离线词典 returns (cache hit restores prior result).
	tcvCyclePopupMode(t, client, win.ID, "翻译")
	tr = tcvWaitTree(t, client, win.ID, 5*time.Second, "restored 离线词典 in 翻译 mode", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "模式输出-翻译") && tcvTreeContains(tr, "离线词典")
	})
	if !tcvTreeContains(tr, "algorithm") {
		t.Fatalf("expected algorithm dict card when returning to 翻译 mode:\n%s", formatTreeSummary(tr))
	}
}

// ---------------------------------------------------------------------------
// F6 Tier 2: Boundaries, Misses & Concurrent Cancellation (5 tests)
// ---------------------------------------------------------------------------

func TestE2E_F6_T2_UnknownEnglishWordFallsBackGracefullyToLLM(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "未知词条兜底译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tr := tcvTranslateInPopup(t, client, win.ID, "xyzzyplugh123", "未知词条兜底译文")
	if tcvTreeContains(tr, "离线词典") {
		t.Fatalf("unexpected 离线词典 card for unknown token:\n%s", formatTreeSummary(tr))
	}
	if len(m.Requests()) != 1 {
		t.Fatalf("expected 1 LLM request, got %d", len(m.Requests()))
	}
}

func TestE2E_F6_T2_CJKCodeIdentifiersNumbersAndURLsBypassDict(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	inputs := []struct {
		src  string
		want string
	}{
		{src: "你好", want: "Hello"},
		{src: "こんにちは", want: "你好日语"},
		{src: "camelCase", want: "驼峰命名"},
		{src: "snake_case", want: "蛇形命名"},
		{src: "12345", want: "一二三四五"},
		{src: "https://example.com", want: "示例网址"},
	}

	for _, tc := range inputs {
		m.SetDefault("chat", MockResponse{Body: tc.want})
		tr := tcvTranslateInInputTab(t, client, win.ID, tc.src, tc.want)
		if tcvTreeContains(tr, "离线词典") {
			t.Fatalf("input %q should bypass 离线词典, got:\n%s", tc.src, formatTreeSummary(tr))
		}
		tcvClearInputTab(t, client, win.ID)
	}
}

func TestE2E_F6_T2_EmptyAndWhitespaceInputBlockedNoHTTP(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)

	// 1. Popup tab: empty and whitespace keep submit button disabled.
	for _, ws := range []string{"", "   \n\t  "} {
		tcvSetPopupSource(t, client, win.ID, ws)
		tr := tcvWaitTree(t, client, win.ID, 3*time.Second, "disabled popup translate button", func(tr TreeResult) bool {
			btn, ok := tcvFindNode(tr, tcvIsPopupSubmitBtn)
			return ok && btn.HasState("disabled")
		})
		if tcvTreeContains(tr, "离线词典") {
			t.Fatalf("unexpected 离线词典 on whitespace %q", ws)
		}
	}

	// 2. Input tab: pressing translate on whitespace sends 0 HTTP requests.
	tcvSwitchTab(t, client, win.ID, "输入")
	tcvSetInputTabText(t, client, win.ID, "   \n\t  ")
	tcvPressInputTabTranslate(t, client, win.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle: %v", err)
	}
	if len(m.Requests()) != 0 {
		t.Fatalf("expected 0 HTTP requests on whitespace input, got %d", len(m.Requests()))
	}
}

func TestE2E_F6_T2_OfflineDictSurvivesLLMError(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Status: http.StatusInternalServerError, Body: "upstream 500"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvStartPopupTranslation(t, client, win.ID, "serendipity")

	tr := tcvWaitTree(t, client, win.ID, 5*time.Second, "error alongside offline dict", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "翻译服务暂时不可用") &&
			tcvTreeContains(tr, "离线词典") &&
			tcvTreeContains(tr, "serendipity")
	})
	if !tcvTreeContains(tr, "/") {
		t.Fatalf("expected phonetic transcription preserved on error:\n%s", formatTreeSummary(tr))
	}
}

func TestE2E_F6_T2_ConcurrentStreamCancellationReplacesDictAndDiscardsStaleDeltas(t *testing.T) {
	s := newSandbox(t)
	c := tcvNewControlledLLM(t)
	gate := c.ArmGate(false, "STALE_PART", "STALE_END")

	cfg := defaultLoopbackConfig(c.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	tcvSetInputTabText(t, client, win.ID, "algorithm")
	tcvPressInputTabTranslate(t, client, win.ID)
	gate.WaitStarted(t, 5*time.Second)

	tcvWaitTree(t, client, win.ID, 4*time.Second, "stale first chunk and algorithm dict", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "STALE_PART") &&
			tcvTreeContains(tr, "离线词典") &&
			tcvTreeContains(tr, "algorithm")
	})

	// Cancel in-flight stream by immediately starting a new translation for "apple".
	c.Mock.SetDefault("chat", MockResponse{Body: "FRESH_APPLE"})
	tcvSetInputTabText(t, client, win.ID, "apple")
	tcvPressInputTabTranslate(t, client, win.ID)

	tcvPollUntil(t, 5*time.Second, "in-flight algorithm SSE context canceled", func() bool {
		return gate.Canceled.Load()
	})
	gate.Close()

	tr := tcvWaitTree(t, client, win.ID, 5*time.Second, "fresh apple dict and result", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "FRESH_APPLE") &&
			tcvTreeContains(tr, "apple") &&
			tcvTreeContains(tr, "离线词典")
	})
	if tcvTreeContains(tr, "STALE_PART") || tcvTreeContains(tr, "STALE_END") || tcvTreeContains(tr, "algorithm") {
		t.Fatalf("stale stream delta or previous dict leaked into UI:\n%s", formatTreeSummary(tr))
	}
}

// ---------------------------------------------------------------------------
// F7 Tier 1: Adaptive Screenshot Pre-Compression & Vision LLM (6 tests)
// ---------------------------------------------------------------------------

func TestE2E_F7_T1_LandscapeHighDPIResizedTo1600JPEG(t *testing.T) {
	s := newSandbox(t)
	tcvSetupSpectacleFixture(t, s, makePNG(t, 3200, 2000))

	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "横屏高分截图译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s, "--silent")
	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	tcvPollUntil(t, 6*time.Second, "vision LLM receives landscape JPEG", func() bool {
		return len(m.Requests()) == 1
	})
	tr := tcvWaitText(t, client, win.ID, 5*time.Second, "横屏高分截图译文")
	if !tcvTreeContains(tr, "视觉模型截图识别") {
		t.Fatalf("expected 视觉模型截图识别 badge:\n%s", formatTreeSummary(tr))
	}
	if tcvTreeContains(tr, "离线词典") {
		t.Fatalf("unexpected 离线词典 on screenshot:\n%s", formatTreeSummary(tr))
	}

	imgs := m.Requests()[0].Images
	if len(imgs) != 1 {
		t.Fatalf("expected 1 image, got %d", len(imgs))
	}
	if imgs[0].DeclaredMIME != "image/jpeg" || imgs[0].ActualMIME != "image/jpeg" {
		t.Fatalf("expected image/jpeg declared & actual MIME, got declared=%q actual=%q", imgs[0].DeclaredMIME, imgs[0].ActualMIME)
	}
	if imgs[0].Width != 1600 || imgs[0].Height != 1000 {
		t.Fatalf("expected resized 1600x1000, got %dx%d", imgs[0].Width, imgs[0].Height)
	}
}

func TestE2E_F7_T1_PortraitTallScreenshotResizedTo1600LongEdge(t *testing.T) {
	s := newSandbox(t)
	tcvSetupSpectacleFixture(t, s, makePNG(t, 1080, 3840))

	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "竖屏长截图译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s, "--silent")
	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	tcvPollUntil(t, 6*time.Second, "vision LLM receives portrait JPEG", func() bool {
		return len(m.Requests()) == 1
	})
	tcvWaitText(t, client, win.ID, 5*time.Second, "竖屏长截图译文")

	imgs := m.Requests()[0].Images
	if len(imgs) != 1 {
		t.Fatalf("expected 1 image, got %d", len(imgs))
	}
	if imgs[0].ActualMIME != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %q", imgs[0].ActualMIME)
	}
	if imgs[0].Width != 450 || imgs[0].Height != 1600 {
		t.Fatalf("expected resized 450x1600, got %dx%d", imgs[0].Width, imgs[0].Height)
	}
}

func TestE2E_F7_T1_SmallPNGNotUpscaledAndEncodedAsJPEG(t *testing.T) {
	s := newSandbox(t)
	tcvSetupSpectacleFixture(t, s, makePNG(t, 800, 600))

	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "小图不放大译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s, "--silent")
	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	tcvPollUntil(t, 6*time.Second, "vision LLM receives small PNG as JPEG", func() bool {
		return len(m.Requests()) == 1
	})
	tcvWaitText(t, client, win.ID, 5*time.Second, "小图不放大译文")

	imgs := m.Requests()[0].Images
	if len(imgs) != 1 {
		t.Fatalf("expected 1 image, got %d", len(imgs))
	}
	if imgs[0].ActualMIME != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %q", imgs[0].ActualMIME)
	}
	if imgs[0].Width != 800 || imgs[0].Height != 600 {
		t.Fatalf("expected preserved 800x600 without upscaling, got %dx%d", imgs[0].Width, imgs[0].Height)
	}
}

func TestE2E_F7_T1_TransparentPNGAlphaCompositedOverWhite(t *testing.T) {
	s := newSandbox(t)
	tcvSetupSpectacleFixture(t, s, tcvMakeTransparentPNG(t, 400, 300))

	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "透明底图译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s, "--silent")
	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	tcvPollUntil(t, 6*time.Second, "vision LLM receives alpha-composited JPEG", func() bool {
		return len(m.Requests()) == 1
	})
	tcvWaitText(t, client, win.ID, 5*time.Second, "透明底图译文")

	imgs := m.Requests()[0].Images
	if len(imgs) != 1 || imgs[0].ActualMIME != "image/jpeg" {
		t.Fatalf("expected 1 image/jpeg, got %+v", imgs)
	}
	decoded := tcvDecodeImageDataURI(t, imgs[0].DataURI)
	r, g, b, _ := decoded.At(200, 150).RGBA()
	r8, g8, b8 := uint8(r>>8), uint8(g>>8), uint8(b>>8)
	if r8 < 245 || g8 < 245 || b8 < 245 {
		t.Fatalf("expected transparent center composited over white (>=245), got RGB(%d,%d,%d)", r8, g8, b8)
	}
}

func TestE2E_F7_T1_NonVisionAndNonOpenAIProvidersSkippedInPriorityOrder(t *testing.T) {
	s := newSandbox(t)
	tcvSetupSpectacleFixture(t, s, makePNG(t, 200, 100))

	mBaidu := newMockLLMServer(t)
	mTextOnly := newMockLLMServer(t)
	mVision := newMockLLMServer(t)
	mVision.SetDefault("chat", MockResponse{Body: "视觉模型命中译文"})

	cfg := defaultLoopbackConfig(mVision.V1URL(), "gpt-4o")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":       "baidu",
			"name":     "百度翻译",
			"kind":     "baidu",
			"enabled":  true,
			"endpoint": mBaidu.URL() + "/api/trans/vip/translate",
			"appID":    "test-appid",
		},
		{
			"id":      "openai-text",
			"name":    "TextOnly",
			"kind":    "openai",
			"enabled": true,
			"baseURL": mTextOnly.V1URL(),
			"model":   "gpt-text-only",
			"vision":  false,
		},
		{
			"id":      "openai-vision",
			"name":    "VisionModel",
			"kind":    "openai",
			"enabled": true,
			"baseURL": mVision.V1URL(),
			"model":   "gpt-4o",
			"vision":  true,
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s, "--silent")
	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	tcvPollUntil(t, 6*time.Second, "vision provider receives image request", func() bool {
		return len(mVision.Requests()) == 1
	})
	tcvWaitText(t, client, win.ID, 5*time.Second, "视觉模型命中译文")

	if len(mBaidu.Requests()) != 0 {
		t.Fatalf("expected 0 requests to Baidu provider in image mode, got %d", len(mBaidu.Requests()))
	}
	if len(mTextOnly.Requests()) != 0 {
		t.Fatalf("expected 0 requests to vision:false OpenAI provider, got %d", len(mTextOnly.Requests()))
	}
	if imgs := mVision.Requests()[0].Images; len(imgs) != 1 || imgs[0].ActualMIME != "image/jpeg" {
		t.Fatalf("expected compressed JPEG sent to vision provider, got %+v", imgs)
	}
}

func TestE2E_F7_T1_GrimSlurpFallbackPathAlsoCompressesBeforeVisionLLM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux grim/slurp screenshot fallback only runs on non-Windows hosts")
	}
	s := newSandbox(t)
	s.DBusAddr = "unix:path=" + filepath.Join(s.RuntimeDir, "no-dbus")
	s.IsolatePATH = true
	s.installShim("slurp")
	s.installShim("grim")
	s.setShimState("slurp_mode", "ok")
	s.setShimState("slurp_region", "0,0 2400x1200")
	s.setShimState("grim_mode", "ok")
	s.setShimState("fixture_png", tcvWritePNGFile(t, s, "grim-2400x1200.png", makePNG(t, 2400, 1200)))

	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "Grim高分压缩译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s, "--silent")
	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	tcvPollUntil(t, 6*time.Second, "vision LLM receives compressed grim JPEG", func() bool {
		return len(m.Requests()) == 1
	})
	tcvWaitText(t, client, win.ID, 5*time.Second, "Grim高分压缩译文")

	imgs := m.Requests()[0].Images
	if len(imgs) != 1 || imgs[0].ActualMIME != "image/jpeg" || imgs[0].Width != 1600 || imgs[0].Height != 800 {
		t.Fatalf("expected grim screenshot resized to 1600x800 JPEG, got %+v", imgs)
	}
}

// ---------------------------------------------------------------------------
// F7 Tier 2: Vision Error Paths, Multi-Engine Filtering & Edge Dimensions (5 tests)
// ---------------------------------------------------------------------------

func TestE2E_F7_T2_NoVisionProviderConfiguredShowsActionableError(t *testing.T) {
	s := newSandbox(t)
	tcvSetupSpectacleFixture(t, s, makePNG(t, 320, 240))

	m := newMockLLMServer(t)
	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":      "openai-novision",
			"name":    "TextOnly",
			"kind":    "openai",
			"enabled": true,
			"baseURL": m.V1URL(),
			"model":   "gpt-4o-mini",
			"vision":  false,
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s, "--silent")
	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	tcvWaitText(t, client, win.ID, 5*time.Second, "截图翻译需要启用一个配置好的 OpenAI Compatible 视觉模型")
	if len(m.Requests()) != 0 {
		t.Fatalf("expected 0 HTTP requests when no vision provider enabled, got %d", len(m.Requests()))
	}
}

func TestE2E_F7_T2_VisionProviderFallbackAndHTTPErrorSurfacing(t *testing.T) {
	t.Run("fallback_to_second_vision_provider", func(t *testing.T) {
		s := newSandbox(t)
		tcvSetupSpectacleFixture(t, s, makePNG(t, 400, 200))

		m1 := newMockLLMServer(t)
		m1.SetDefault("chat", MockResponse{Status: http.StatusInternalServerError, Body: "v1 failed"})
		m2 := newMockLLMServer(t)
		m2.SetDefault("chat", MockResponse{Body: "备用视觉模型成功"})

		cfg := defaultLoopbackConfig(m1.V1URL(), "gpt-4o-mini")
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		cfg["providers"] = []map[string]any{
			{
				"id":      "vis-1",
				"name":    "Vision1",
				"kind":    "openai",
				"enabled": true,
				"baseURL": m1.V1URL(),
				"model":   "vis-1",
				"vision":  true,
			},
			{
				"id":      "vis-2",
				"name":    "Vision2",
				"kind":    "openai",
				"enabled": true,
				"baseURL": m2.V1URL(),
				"model":   "vis-2",
				"vision":  true,
			},
		}
		s.writeConfigJSON(cfg)

		_, client, win := tcvOpenMainPanel(t, s, "--silent")
		if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
			t.Fatalf("-c failed: %+v", res)
		}

		tcvWaitText(t, client, win.ID, 6*time.Second, "备用视觉模型成功")
		if len(m1.Requests()) != 1 || len(m2.Requests()) != 1 {
			t.Fatalf("expected fallback from Vision1(1) to Vision2(1), got %d and %d", len(m1.Requests()), len(m2.Requests()))
		}
	})

	t.Run("all_fail_401_surfaces_auth_error", func(t *testing.T) {
		s := newSandbox(t)
		tcvSetupSpectacleFixture(t, s, makePNG(t, 400, 200))

		m := newMockLLMServer(t)
		m.SetDefault("chat", MockResponse{Status: http.StatusUnauthorized, Body: "invalid api key"})

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		_, client, win := tcvOpenMainPanel(t, s, "--silent")
		if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
			t.Fatalf("-c failed: %+v", res)
		}
		tcvWaitText(t, client, win.ID, 5*time.Second, "鉴权失败：请检查 API Key")
	})
}

func TestE2E_F7_T2_CorruptAndPixelBombScreenshotFailWithoutHTTP(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
	}{
		{name: "corrupt_bytes", bytes: []byte("not-a-valid-png-image")},
		{name: "pixel_bomb_20000x20000", bytes: tcvMakePixelBombPNG(20000, 20000)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			tcvSetupSpectacleFixture(t, s, tc.bytes)

			m := newMockLLMServer(t)
			cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
			cfg["popupPinned"] = true
			cfg["disableSelectionToolbar"] = true
			s.writeConfigJSON(cfg)

			_, client, win := tcvOpenMainPanel(t, s, "--silent")
			if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
				t.Fatalf("-c failed: %+v", res)
			}

			tcvWaitText(t, client, win.ID, 5*time.Second, "截图预处理失败")
			if len(m.Requests()) != 0 {
				t.Fatalf("expected 0 HTTP requests for %s, got %d", tc.name, len(m.Requests()))
			}
		})
	}
}

func TestE2E_F7_T2_ExtremeAspectRatioClampedToMinimum1Px(t *testing.T) {
	s := newSandbox(t)
	tcvSetupSpectacleFixture(t, s, makePNG(t, 4000, 1))

	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "极端比例截图译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s, "--silent")
	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	tcvPollUntil(t, 6*time.Second, "vision LLM receives clamped 1600x1 JPEG", func() bool {
		return len(m.Requests()) == 1
	})
	tcvWaitText(t, client, win.ID, 5*time.Second, "极端比例截图译文")

	imgs := m.Requests()[0].Images
	if len(imgs) != 1 || imgs[0].ActualMIME != "image/jpeg" || imgs[0].Width != 1600 || imgs[0].Height != 1 {
		t.Fatalf("expected 1600x1 JPEG, got %+v", imgs)
	}
}

func TestE2E_F7_T2_MultiEngineVisionFiltersOutNonVisionAndDoesNotCache(t *testing.T) {
	s := newSandbox(t)
	tcvSetupSpectacleFixture(t, s, makePNG(t, 640, 480))

	mVis1 := newMockLLMServer(t)
	mVis1.SetDefault("chat", MockResponse{Body: "视觉引擎1译文"})
	mText := newMockLLMServer(t)
	mVis2 := newMockLLMServer(t)
	mVis2.SetDefault("chat", MockResponse{Body: "视觉引擎2译文"})

	cfg := defaultLoopbackConfig(mVis1.V1URL(), "vis-1")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["multiEngine"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":      "vis1",
			"name":    "Vision1",
			"kind":    "openai",
			"enabled": true,
			"baseURL": mVis1.V1URL(),
			"model":   "vis-1",
			"vision":  true,
		},
		{
			"id":      "txt",
			"name":    "TextOnly",
			"kind":    "openai",
			"enabled": true,
			"baseURL": mText.V1URL(),
			"model":   "txt-only",
			"vision":  false,
		},
		{
			"id":      "vis2",
			"name":    "Vision2",
			"kind":    "openai",
			"enabled": true,
			"baseURL": mVis2.V1URL(),
			"model":   "vis-2",
			"vision":  true,
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s, "--silent")
	if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
		t.Fatalf("-c failed: %+v", res)
	}

	tr := tcvWaitTree(t, client, win.ID, 6*time.Second, "multi-engine vision completion", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "全部完成 (2/2 成功)") &&
			tcvTreeContains(tr, "视觉引擎1译文") &&
			tcvTreeContains(tr, "视觉引擎2译文")
	})
	if tcvTreeContains(tr, "TextOnly") {
		t.Fatalf("non-vision provider card should not appear in multi-engine image mode:\n%s", formatTreeSummary(tr))
	}
	if len(mVis1.Requests()) != 1 || len(mVis2.Requests()) != 1 || len(mText.Requests()) != 0 {
		t.Fatalf("unexpected request counts vis1=%d text=%d vis2=%d", len(mVis1.Requests()), len(mText.Requests()), len(mVis2.Requests()))
	}

	// Re-translate screenshot: must hit both vision providers again (screenshots are not cached).
	mVis1.SetDefault("chat", MockResponse{Body: "视觉引擎1第二次"})
	mVis2.SetDefault("chat", MockResponse{Body: "视觉引擎2第二次"})
	tcvClickRetranslate(t, client, win.ID)

	tr = tcvWaitTree(t, client, win.ID, 6*time.Second, "second vision re-translate", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "视觉引擎1第二次") && tcvTreeContains(tr, "视觉引擎2第二次")
	})
	if tcvTreeContains(tr, "缓存") {
		t.Fatalf("screenshots must not report cache hit:\n%s", formatTreeSummary(tr))
	}
	if len(mVis1.Requests()) != 2 || len(mVis2.Requests()) != 2 || len(mText.Requests()) != 0 {
		t.Fatalf("unexpected request counts after retranslate vis1=%d text=%d vis2=%d", len(mVis1.Requests()), len(mText.Requests()), len(mVis2.Requests()))
	}
}

// ---------------------------------------------------------------------------
// F8 Tier 1: Short-Term LRU Cache Hit, Key Tuple & Re-translate Bypass (6 tests)
// ---------------------------------------------------------------------------

func TestE2E_F8_T1_IdenticalRepeatHitsCacheWithoutHTTPAndPreservesDict(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "算法缓存译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tr := tcvTranslateInPopup(t, client, win.ID, "algorithm", "算法缓存译文")
	if len(m.Requests()) != 1 {
		t.Fatalf("expected 1 HTTP request, got %d", len(m.Requests()))
	}
	if !tcvTreeContains(tr, "离线词典") {
		t.Fatalf("expected 离线词典 on first translate")
	}

	tcvPollUntil(t, 4*time.Second, "history row persisted after first translation", func() bool {
		return s.countHistoryRows() == 1
	})

	// Trigger identical popup translation via Ctrl+Enter (calls runTranslation(false) -> checks cache).
	tcvSendPopupCtrlEnter(t, client, win.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	tr = tcvWaitTree(t, client, win.ID, 5*time.Second, "popup cache hit", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "已命中缓存") && tcvTreeContains(tr, "算法缓存译文")
	})
	if !tcvTreeContains(tr, "离线词典") || !tcvTreeContains(tr, "algorithm") {
		t.Fatalf("offline dict card missing on cache hit:\n%s", formatTreeSummary(tr))
	}
	if len(m.Requests()) != 1 {
		t.Fatalf("expected 0 extra HTTP requests on cache hit, got total %d", len(m.Requests()))
	}
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle: %v", err)
	}
	if cnt := s.countHistoryRows(); cnt != 1 {
		t.Fatalf("expected cache hit not to insert duplicate history row, got count=%d", cnt)
	}
}

func TestE2E_F8_T1_RetranslateButtonBypassesAndUpdatesCache(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.QueueResponse(
		MockResponse{Body: "结果-1"},
		MockResponse{Body: "结果-2"},
	)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvTranslateInPopup(t, client, win.ID, "ephemeral", "结果-1")
	if len(m.Requests()) != 1 {
		t.Fatalf("expected 1 request, got %d", len(m.Requests()))
	}

	// Repeat via Ctrl+Enter -> hits cache ("结果-1").
	tcvSendPopupCtrlEnter(t, client, win.ID)
	tcvWaitText(t, client, win.ID, 4*time.Second, "已命中缓存")
	if len(m.Requests()) != 1 {
		t.Fatalf("expected cache hit before retranslate, got %d requests", len(m.Requests()))
	}

	// Click "重新翻译" -> bypasses cache and updates entry to "结果-2".
	tcvClickRetranslate(t, client, win.ID)
	tr := tcvWaitTree(t, client, win.ID, 5*time.Second, "retranslate updated result", func(tr TreeResult) bool {
		_, hasRetranslate := tcvFindNode(tr, func(n Node) bool {
			return n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
		})
		return hasRetranslate && tcvTreeContains(tr, "结果-2")
	})
	if tcvTreeContains(tr, "已命中缓存") {
		t.Fatalf("retranslate should clear 已命中缓存 status:\n%s", formatTreeSummary(tr))
	}
	if len(m.Requests()) != 2 {
		t.Fatalf("expected 2 requests after retranslate, got %d", len(m.Requests()))
	}

	// Repeat via Ctrl+Enter -> cache now serves updated "结果-2" with 0 new requests.
	tcvSendPopupCtrlEnter(t, client, win.ID)
	tr = tcvWaitText(t, client, win.ID, 4*time.Second, "已命中缓存")
	if !tcvTreeContains(tr, "结果-2") {
		t.Fatalf("expected updated cached result 结果-2, got:\n%s", formatTreeSummary(tr))
	}
	if len(m.Requests()) != 2 {
		t.Fatalf("expected still 2 HTTP requests, got %d", len(m.Requests()))
	}
}

func TestE2E_F8_T1_CacheKeySensitiveToTargetLangAndMode(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "中文译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["targetLang"] = "简体中文"
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvTranslateInPopup(t, client, win.ID, "hello", "中文译文")
	if len(m.Requests()) != 1 {
		t.Fatalf("expected 1 request, got %d", len(m.Requests()))
	}

	// Change target language to 日本語 -> misses cache (2nd request).
	m.SetDefault("chat", MockResponse{Body: "こんにちは訳"})
	tcvSelectOption(t, client, win.ID, "目标语言", "日本語")
	tcvWaitText(t, client, win.ID, 5*time.Second, "こんにちは訳")
	if len(m.Requests()) != 2 {
		t.Fatalf("expected cache miss on target language change, got %d requests", len(m.Requests()))
	}

	// Switch back to 简体中文 -> hits cache (still 2 requests).
	tr := func() TreeResult {
		tcvSelectOption(t, client, win.ID, "目标语言", "简体中文")
		return tcvWaitTree(t, client, win.ID, 5*time.Second, "cache hit returning to 简体中文", func(tr TreeResult) bool {
			return tcvTreeContains(tr, "已命中缓存") && tcvTreeContains(tr, "中文译文")
		})
	}()
	_ = tr
	if len(m.Requests()) != 2 {
		t.Fatalf("expected cache hit when returning to 简体中文, got %d requests", len(m.Requests()))
	}

	// Switch mode to 润色 -> misses cache (3rd request).
	m.SetDefault("chat", MockResponse{Body: "润色版本"})
	tcvCyclePopupMode(t, client, win.ID, "润色")
	tcvWaitText(t, client, win.ID, 5*time.Second, "润色版本")
	if len(m.Requests()) != 3 {
		t.Fatalf("expected cache miss on mode change, got %d requests", len(m.Requests()))
	}
}

func TestE2E_F8_T1_CacheKeySensitiveToProviderConfigHotReload(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "模型A结果"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":      "openai",
			"name":    "OpenAI-A",
			"kind":    "openai",
			"enabled": true,
			"baseURL": m.V1URL(),
			"model":   "gpt-4o-mini",
			"vision":  true,
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	tcvTranslateInInputTab(t, client, win.ID, "world", "模型A结果")
	if len(m.Requests()) != 1 || m.Requests()[0].Model != "gpt-4o-mini" {
		t.Fatalf("expected 1 request for gpt-4o-mini, got %+v", m.Requests())
	}

	// Confirm cache hit before reload.
	tcvPressInputTabTranslate(t, client, win.ID)
	tcvWaitText(t, client, win.ID, 4*time.Second, "已命中缓存")
	if len(m.Requests()) != 1 {
		t.Fatalf("expected cache hit before reload, got %d requests", len(m.Requests()))
	}

	// Hot-reload config.json changing both provider name ("OpenAI-B") and model ("gpt-4o").
	m.SetDefault("chat", MockResponse{Body: "模型B结果"})
	cfg["model"] = "gpt-4o"
	cfg["providers"] = []map[string]any{
		{
			"id":      "openai",
			"name":    "OpenAI-B",
			"kind":    "openai",
			"enabled": true,
			"baseURL": m.V1URL(),
			"model":   "gpt-4o",
			"vision":  true,
		},
	}
	s.writeConfigJSON(cfg)

	// Wait deterministically for the footer engine label to reflect the reloaded provider name.
	tcvWaitText(t, client, win.ID, 6*time.Second, "OpenAI-B")

	// Translate "world" again via cache-aware button -> must miss cache and send model "gpt-4o".
	tcvPressInputTabTranslate(t, client, win.ID)
	tr := tcvWaitText(t, client, win.ID, 5*time.Second, "模型B结果")
	if tcvTreeContains(tr, "已命中缓存") {
		t.Fatalf("expected cache miss after provider/model hot-reload, got:\n%s", formatTreeSummary(tr))
	}
	reqs := m.Requests()
	if len(reqs) != 2 || reqs[1].Model != "gpt-4o" {
		t.Fatalf("expected 2nd request with model gpt-4o, got %+v", reqs)
	}
}

func TestE2E_F8_T1_PopupAndInputTabShareCache(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "跨页共享缓存译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["targetLang"] = "简体中文"
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvTranslateInPopup(t, client, win.ID, "serendipity", "跨页共享缓存译文")
	if len(m.Requests()) != 1 {
		t.Fatalf("expected 1 HTTP request from popup, got %d", len(m.Requests()))
	}

	tcvSwitchTab(t, client, win.ID, "输入")
	tr := tcvTranslateInInputTab(t, client, win.ID, "serendipity", "跨页共享缓存译文")
	if !tcvTreeContains(tr, "已命中缓存") {
		t.Fatalf("expected cache hit in 输入 tab from popup translation:\n%s", formatTreeSummary(tr))
	}
	if len(m.Requests()) != 1 {
		t.Fatalf("expected 0 extra HTTP requests when hitting shared cache, got %d", len(m.Requests()))
	}
}

func TestE2E_F8_T1_MultiEngineFullSuccessCachedAndRestoresCards(t *testing.T) {
	s := newSandbox(t)
	m1 := newMockLLMServer(t)
	m1.SetDefault("chat", MockResponse{Body: "引擎甲-苹果"})
	m2 := newMockLLMServer(t)
	m2.SetDefault("chat", MockResponse{Body: "引擎乙-苹果"})

	cfg := defaultLoopbackConfig(m1.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["multiEngine"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":      "p1",
			"name":    "EngineA",
			"kind":    "openai",
			"enabled": true,
			"baseURL": m1.V1URL(),
			"model":   "m1",
		},
		{
			"id":      "p2",
			"name":    "EngineB",
			"kind":    "openai",
			"enabled": true,
			"baseURL": m2.V1URL(),
			"model":   "m2",
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvStartPopupTranslation(t, client, win.ID, "apple")
	tr := tcvWaitTree(t, client, win.ID, 6*time.Second, "multi-engine initial completion", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "全部完成 (2/2 成功)") &&
			tcvTreeContains(tr, "引擎甲-苹果") &&
			tcvTreeContains(tr, "引擎乙-苹果")
	})
	_ = tr
	if len(m1.Requests()) != 1 || len(m2.Requests()) != 1 {
		t.Fatalf("expected 1 req per engine, got %d and %d", len(m1.Requests()), len(m2.Requests()))
	}

	// Repeat via Ctrl+Enter -> must hit multi-engine cache and restore both cards.
	tcvSendPopupCtrlEnter(t, client, win.ID)
	tr = tcvWaitTree(t, client, win.ID, 5*time.Second, "multi-engine cache hit", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "全部完成 (2/2 成功 · 缓存)") &&
			tcvTreeContains(tr, "EngineA") &&
			tcvTreeContains(tr, "EngineB") &&
			tcvTreeContains(tr, "引擎甲-苹果") &&
			tcvTreeContains(tr, "引擎乙-苹果")
	})
	if len(m1.Requests()) != 1 || len(m2.Requests()) != 1 {
		t.Fatalf("expected 0 extra requests on multi-engine cache hit, got %d and %d", len(m1.Requests()), len(m2.Requests()))
	}
}

// ---------------------------------------------------------------------------
// F8 Tier 2: Error/Cancel Isolation, Boundary Keys & LRU Eviction (6 tests)
// ---------------------------------------------------------------------------

func TestE2E_F8_T2_HTTPAndTruncatedSSEErrorsAreNotCached(t *testing.T) {
	cases := []struct {
		name string
		resp MockResponse
	}{
		{name: "http_500", resp: MockResponse{Status: http.StatusInternalServerError, Body: "err 500"}},
		{name: "http_429", resp: MockResponse{Status: http.StatusTooManyRequests, Body: "rate limit"}},
		{name: "truncated_sse", resp: MockResponse{Chunks: []string{"断流前缀"}, TruncateSSE: true}},
		{name: "sse_error", resp: MockResponse{SSEError: "midstream failure"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t)
			m := newMockLLMServer(t)
			m.QueueResponse(tc.resp, MockResponse{Body: "错误恢复成功译文"})

			cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
			cfg["popupPinned"] = true
			cfg["disableSelectionToolbar"] = true
			s.writeConfigJSON(cfg)

			_, client, win := tcvOpenMainPanel(t, s)
			tcvSwitchTab(t, client, win.ID, "输入")

			tcvSetInputTabText(t, client, win.ID, "ephemeral")
			tcvPressInputTabTranslate(t, client, win.ID)

			tcvPollUntil(t, 5*time.Second, "first error request completed", func() bool {
				return len(m.Requests()) == 1
			})
			tcvWaitTree(t, client, win.ID, 5*time.Second, "error surfaced", func(tr TreeResult) bool {
				return tcvTreeContains(tr, "暂时不可用") ||
					tcvTreeContains(tr, "频繁") ||
					tcvTreeContains(tr, "翻译失败") ||
					tcvTreeContains(tr, "中断") ||
					tcvTreeContains(tr, "未收到结束标记")
			})

			// Retry using cache-reading button -> must NOT serve failed result from cache.
			tcvPressInputTabTranslate(t, client, win.ID)
			tr := tcvWaitText(t, client, win.ID, 5*time.Second, "错误恢复成功译文")
			if tcvTreeContains(tr, "已命中缓存") {
				t.Fatalf("failed translation must not be cached:\n%s", formatTreeSummary(tr))
			}
			if len(m.Requests()) != 2 {
				t.Fatalf("expected 2 requests (no cache on error), got %d", len(m.Requests()))
			}
		})
	}
}

func TestE2E_F8_T2_CancelledInFlightStreamIsNotCached(t *testing.T) {
	s := newSandbox(t)
	c := tcvNewControlledLLM(t)
	gate := c.ArmGate(false, "PARTIAL_STREAM", "NEVER_SENT")

	cfg := defaultLoopbackConfig(c.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	tcvSetInputTabText(t, client, win.ID, "algorithm")
	tcvPressInputTabTranslate(t, client, win.ID)
	gate.WaitStarted(t, 5*time.Second)

	tcvWaitText(t, client, win.ID, 4*time.Second, "PARTIAL_STREAM")
	tcvClearInputTab(t, client, win.ID)

	tcvPollUntil(t, 5*time.Second, "cancelled stream context done", func() bool {
		return gate.Canceled.Load()
	})
	gate.Close()

	c.Mock.SetDefault("chat", MockResponse{Body: "COMPLETE_ALG"})
	tr := tcvTranslateInInputTab(t, client, win.ID, "algorithm", "COMPLETE_ALG")
	if tcvTreeContains(tr, "已命中缓存") || tcvTreeContains(tr, "PARTIAL_STREAM") {
		t.Fatalf("cancelled partial stream must not be cached:\n%s", formatTreeSummary(tr))
	}
	if len(c.Mock.Requests()) != 1 {
		t.Fatalf("expected fresh HTTP request on Mock after cancelled gated request, got %d", len(c.Mock.Requests()))
	}
}

func TestE2E_F8_T2_MultiEnginePartialFailureNotCached(t *testing.T) {
	s := newSandbox(t)
	m1 := newMockLLMServer(t)
	m1.SetDefault("chat", MockResponse{Body: "部分成功-P1"})
	m2 := newMockLLMServer(t)
	m2.SetDefault("chat", MockResponse{Status: http.StatusInternalServerError, Body: "p2 down"})

	cfg := defaultLoopbackConfig(m1.V1URL(), "m1")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["multiEngine"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":      "p1",
			"name":    "P1",
			"kind":    "openai",
			"enabled": true,
			"baseURL": m1.V1URL(),
			"model":   "m1",
		},
		{
			"id":      "p2",
			"name":    "P2",
			"kind":    "openai",
			"enabled": true,
			"baseURL": m2.V1URL(),
			"model":   "m2",
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvStartPopupTranslation(t, client, win.ID, "hello")
	tcvWaitText(t, client, win.ID, 6*time.Second, "部分完成 (1/2 成功)")

	m1.SetDefault("chat", MockResponse{Body: "全部恢复-P1"})
	m2.SetDefault("chat", MockResponse{Body: "全部恢复-P2"})

	tcvSendPopupCtrlEnter(t, client, win.ID)
	tr := tcvWaitTree(t, client, win.ID, 6*time.Second, "fresh full multi-engine success", func(tr TreeResult) bool {
		return tcvTreeContains(tr, "全部完成 (2/2 成功)") &&
			tcvTreeContains(tr, "全部恢复-P1") &&
			tcvTreeContains(tr, "全部恢复-P2")
	})
	if tcvTreeContains(tr, "缓存") {
		t.Fatalf("partial failure must not be cached:\n%s", formatTreeSummary(tr))
	}
	if len(m1.Requests()) != 2 || len(m2.Requests()) != 2 {
		t.Fatalf("expected 2 requests on both P1 and P2, got %d and %d", len(m1.Requests()), len(m2.Requests()))
	}
}

func TestE2E_F8_T2_WhitespaceTrimmedAndCaseSensitivity(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.QueueResponse(
		MockResponse{Body: "小写结果"},
		MockResponse{Body: "大写首字母结果"},
	)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	tcvTranslateInInputTab(t, client, win.ID, "hello", "小写结果")
	if len(m.Requests()) != 1 {
		t.Fatalf("expected 1 request, got %d", len(m.Requests()))
	}

	// Whitespace around "hello" hits trimmed cache key.
	tr := tcvTranslateInInputTab(t, client, win.ID, "  hello  ", "已命中缓存")
	if !tcvTreeContains(tr, "小写结果") {
		t.Fatalf("expected cached 小写结果 on trimmed whitespace:\n%s", formatTreeSummary(tr))
	}
	if len(m.Requests()) != 1 {
		t.Fatalf("expected cache hit on whitespace-padded hello, got %d requests", len(m.Requests()))
	}

	// Different casing "Hello" must miss cache.
	tr = tcvTranslateInInputTab(t, client, win.ID, "Hello", "大写首字母结果")
	if tcvTreeContains(tr, "已命中缓存") {
		t.Fatalf("expected case-sensitive cache miss for Hello:\n%s", formatTreeSummary(tr))
	}
	if len(m.Requests()) != 2 {
		t.Fatalf("expected 2 requests after case change, got %d", len(m.Requests()))
	}
}

func TestE2E_F8_T2_InputTabSourceLangChangeMissesCache(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.QueueResponse(
		MockResponse{Body: "自动检测译文"},
		MockResponse{Body: "法语指定译文"},
	)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	tcvTranslateInInputTab(t, client, win.ID, "chat", "自动检测译文")
	if len(m.Requests()) != 1 {
		t.Fatalf("expected 1 request, got %d", len(m.Requests()))
	}

	tcvSelectOption(t, client, win.ID, "源语言", "Français")
	tr := tcvTranslateInInputTab(t, client, win.ID, "chat", "法语指定译文")
	if tcvTreeContains(tr, "已命中缓存") {
		t.Fatalf("expected cache miss after changing source language to Français:\n%s", formatTreeSummary(tr))
	}
	if len(m.Requests()) != 2 {
		t.Fatalf("expected 2 requests after source language change, got %d", len(m.Requests()))
	}
}

func TestE2E_F8_T2_LRUEvictionAtCapacity256(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := tcvOpenMainPanel(t, s)
	tcvSwitchTab(t, client, win.ID, "输入")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	editor, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "textField" && strings.Contains(n.Placeholder, "在此输入要翻译的内容")
	})
	if err != nil {
		t.Fatalf("WaitNode input editor: %v", err)
	}
	btn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "button" && n.Label == "翻译 (Ctrl+Enter)"
	})
	if err != nil {
		t.Fatalf("WaitNode input translate button: %v", err)
	}

	translateFast := func(src, wantRes string) TreeResult {
		m.SetDefault("chat", MockResponse{Body: wantRes})
		if err := client.Act(ctx, ActParams{Window: win.ID, ID: editor.ID, Action: "setValue", Text: src}); err != nil {
			t.Fatalf("setValue %q: %v", src, err)
		}
		if err := client.WaitIdle(ctx, win.ID); err != nil {
			t.Fatalf("WaitIdle after setValue %q: %v", src, err)
		}
		trCur, err := client.Tree(ctx, win.ID)
		if err != nil {
			t.Fatalf("Tree before press %q: %v", src, err)
		}
		curBtn, ok := tcvFindNode(trCur, func(n Node) bool {
			return n.Role == "button" && n.Label == "翻译 (Ctrl+Enter)"
		})
		if !ok {
			curBtn = btn
		}
		if err := client.Act(ctx, ActParams{Window: win.ID, ID: curBtn.ID, Action: "press"}); err != nil {
			t.Fatalf("press translate for %q: %v", src, err)
		}
		return tcvWaitTreeFast(t, client, win.ID, 5*time.Second, fmt.Sprintf("%s -> %s", src, wantRes), func(tr TreeResult) bool {
			return tcvTreeContains(tr, wantRes)
		})
	}

	// Fill capacity 256: word000 .. word255 -> 256 HTTP requests.
	for i := 0; i < 256; i++ {
		translateFast(fmt.Sprintf("word%03d", i), fmt.Sprintf("译文_%03d", i))
	}
	if len(m.Requests()) != 256 {
		t.Fatalf("expected 256 requests filling LRU cache, got %d", len(m.Requests()))
	}

	// Promote word000 to MRU via cache hit (still 256 HTTP requests).
	tr := translateFast("word000", "译文_000")
	if !tcvTreeContains(tr, "已命中缓存") || len(m.Requests()) != 256 {
		t.Fatalf("expected cache hit promoting word000, reqs=%d\n%s", len(m.Requests()), formatTreeSummary(tr))
	}

	// Insert 257th entry word256 -> evicts LRU word001 (257th HTTP request).
	tr = translateFast("word256", "译文_256")
	if tcvTreeContains(tr, "已命中缓存") || len(m.Requests()) != 257 {
		t.Fatalf("expected cache miss for word256, reqs=%d", len(m.Requests()))
	}

	// word000 must still be cached (LRU promoted).
	tr = translateFast("word000", "译文_000")
	if !tcvTreeContains(tr, "已命中缓存") || len(m.Requests()) != 257 {
		t.Fatalf("expected word000 retained after LRU promotion, reqs=%d\n%s", len(m.Requests()), formatTreeSummary(tr))
	}

	// word001 must have been evicted -> cache miss (258th HTTP request).
	tr = translateFast("word001", "译文_001_EVICTED")
	if tcvTreeContains(tr, "已命中缓存") || len(m.Requests()) != 258 {
		t.Fatalf("expected word001 evicted from LRU cache, reqs=%d\n%s", len(m.Requests()), formatTreeSummary(tr))
	}
}
