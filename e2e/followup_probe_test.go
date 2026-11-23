//go:build e2e

package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Cross-OS local helpers for F9 (Multi-turn Follow-up) & F10 (Settings Probe)
// ---------------------------------------------------------------------------

type fpStreamGate struct {
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

func (g *fpStreamGate) Close() {
	g.releaseOnce.Do(func() {
		close(g.Release)
	})
}

func (g *fpStreamGate) WaitStarted(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-g.Started:
	case <-time.After(timeout):
		t.Fatalf("timed out after %v waiting for gated SSE stream to start", timeout)
	}
}

type fpControlledLLM struct {
	Mock   *MockLLMServer
	Server *httptest.Server

	mu          sync.Mutex
	gate        *fpStreamGate
	activeGates []*fpStreamGate
}

func fpNewControlledLLM(t *testing.T) *fpControlledLLM {
	t.Helper()
	m := newMockLLMServer(t)
	c := &fpControlledLLM{Mock: m}
	c.Server = httptest.NewServer(http.HandlerFunc(c.handle))
	t.Cleanup(func() {
		c.Disarm()
		c.Server.Close()
	})
	return c
}

func (c *fpControlledLLM) V1URL() string {
	return c.Server.URL + "/v1"
}

func (c *fpControlledLLM) ArmGate(holdBeforeFirst bool, firstChunk string, restChunks ...string) *fpStreamGate {
	g := &fpStreamGate{
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

func (c *fpControlledLLM) Disarm() {
	c.mu.Lock()
	c.gate = nil
	gates := c.activeGates
	c.activeGates = nil
	c.mu.Unlock()
	for _, g := range gates {
		g.Close()
	}
}

func (c *fpControlledLLM) handle(w http.ResponseWriter, r *http.Request) {
	route := classifyRoute(r.Method, r.URL.Path)
	c.mu.Lock()
	g := c.gate
	c.mu.Unlock()

	if g == nil || route != "chat" {
		c.Mock.Server.Config.Handler.ServeHTTP(w, r)
		return
	}

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

func fpPollUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
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

func fpOpenMainPanel(t *testing.T, s *sandbox, args ...string) (*GUIProcess, *AutomationClient, WindowInfo) {
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

func fpNodeHasText(n Node, substr string) bool {
	return strings.Contains(n.Label, substr) ||
		strings.Contains(n.Value, substr) ||
		strings.Contains(n.Placeholder, substr) ||
		strings.Contains(n.Description, substr)
}

func fpTreeContains(tr TreeResult, substr string) bool {
	for _, n := range tr.Nodes {
		if fpNodeHasText(n, substr) {
			return true
		}
	}
	return false
}

func fpNodesContain(nodes []Node, substr string) bool {
	for _, n := range nodes {
		if fpNodeHasText(n, substr) {
			return true
		}
	}
	return false
}

func fpFindNode(tr TreeResult, pred func(Node) bool) (Node, bool) {
	for _, n := range tr.Nodes {
		if pred(n) {
			return n, true
		}
	}
	return Node{}, false
}

func fpWaitTree(t *testing.T, client *AutomationClient, winID int, timeout time.Duration, desc string, pred func(TreeResult) bool) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	tr, err := client.WaitTree(ctx, winID, pred)
	if err != nil {
		t.Fatalf("WaitTree(%s): %v", desc, err)
	}
	return tr
}

func fpWaitText(t *testing.T, client *AutomationClient, winID int, timeout time.Duration, substr string) TreeResult {
	t.Helper()
	return fpWaitTree(t, client, winID, timeout, fmt.Sprintf("text %q", substr), func(tr TreeResult) bool {
		return fpTreeContains(tr, substr)
	})
}

func fpSwitchPanelTab(t *testing.T, client *AutomationClient, winID int, label string) TreeResult {
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

func fpOpenSettingsEnginesTab(t *testing.T, client *AutomationClient, winID int) TreeResult {
	t.Helper()
	fpSwitchPanelTab(t, client, winID, "设置")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	nav, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
		return n.Role == "button" && (n.Description == "翻译引擎" || n.Label == "翻译引擎" || keyEquals(n.Key, "nav-翻译引擎"))
	})
	if err != nil {
		t.Fatalf("WaitNode 翻译引擎 nav: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: nav.ID, Action: "press"}); err != nil {
		t.Fatalf("press 翻译引擎 nav: %v", err)
	}
	return fpWaitText(t, client, winID, 4*time.Second, "引擎管理与多引擎对比")
}

func fpIsPopupSubmitBtn(n Node) bool {
	return n.Role == "button" &&
		(n.Label == "翻译" || n.Label == "重新翻译") &&
		n.Key == nil &&
		n.Description == ""
}

func fpTranslateInPopup(t *testing.T, client *AutomationClient, winID int, source, wantResultSubstr string) TreeResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	tr, err := client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		if _, ok := fpFindNode(tr, func(n Node) bool {
			return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
		}); ok {
			return true
		}
		_, ok := fpFindNode(tr, func(n Node) bool {
			return n.Role == "button" && strings.Contains(n.Description, "点击编辑原文")
		})
		return ok
	})
	if err != nil {
		t.Fatalf("WaitTree popup editor/preview: %v", err)
	}
	editor, ok := fpFindNode(tr, func(n Node) bool {
		return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
	})
	if !ok {
		preview, _ := fpFindNode(tr, func(n Node) bool {
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
		t.Fatalf("setValue popup source: %v", err)
	}
	tr, err = client.WaitTree(ctx, winID, func(tr TreeResult) bool {
		_, ok := fpFindNode(tr, func(n Node) bool {
			return fpIsPopupSubmitBtn(n) && !n.HasState("disabled")
		})
		return ok
	})
	if err != nil {
		t.Fatalf("WaitTree enabled popup translate button: %v", err)
	}
	btn, _ := fpFindNode(tr, fpIsPopupSubmitBtn)
	if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press popup translate button: %v", err)
	}

	return fpWaitTree(t, client, winID, 6*time.Second, fmt.Sprintf("popup result %q", wantResultSubstr), func(tr TreeResult) bool {
		_, hasRetranslate := fpFindNode(tr, func(n Node) bool {
			return n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
		})
		return hasRetranslate && fpTreeContains(tr, wantResultSubstr)
	})
}

func fpIsFollowUpInput(n Node) bool {
	return n.Role == "textField" && (strings.Contains(n.Placeholder, "追问") || strings.Contains(n.Placeholder, "继续提问"))
}

func fpIsFollowUpSendBtn(n Node) bool {
	return n.Role == "button" && (n.Label == "追问" || n.Label == "发送")
}

func fpSetFollowUpInput(t *testing.T, client *AutomationClient, winID int, question string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	input, _, err := client.WaitNode(ctx, winID, fpIsFollowUpInput)
	if err != nil {
		t.Fatalf("WaitNode follow-up input: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: winID, ID: input.ID, Action: "setValue", Text: question}); err != nil {
		t.Fatalf("setValue follow-up input %q: %v", question, err)
	}
}

func fpSubmitFollowUp(t *testing.T, client *AutomationClient, winID int, question, wantAnswer string, useCtrlEnter bool) TreeResult {
	t.Helper()
	fpSetFollowUpInput(t, client, winID, question)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if useCtrlEnter {
		if input, _, err := client.WaitNode(ctx, winID, fpIsFollowUpInput); err == nil {
			_ = client.Act(ctx, ActParams{Window: winID, ID: input.ID, Action: "focus"})
			_ = client.WaitIdle(ctx, winID)
		}
		if err := client.Input(ctx, winID, InputEvent{Type: "key", Key: "Ctrl+Enter"}); err != nil {
			t.Fatalf("Ctrl+Enter follow-up: %v", err)
		}
	} else {
		btn, _, err := client.WaitNode(ctx, winID, func(n Node) bool {
			return fpIsFollowUpSendBtn(n) && !n.HasState("disabled")
		})
		if err != nil {
			t.Fatalf("WaitNode follow-up send button: %v", err)
		}
		if err := client.Act(ctx, ActParams{Window: winID, ID: btn.ID, Action: "press"}); err != nil {
			t.Fatalf("press follow-up send button: %v", err)
		}
	}

	return fpWaitTree(t, client, winID, 6*time.Second, fmt.Sprintf("follow-up answer %q", wantAnswer), func(tr TreeResult) bool {
		btn, ok := fpFindNode(tr, fpIsFollowUpSendBtn)
		return ok && !btn.HasState("disabled") && fpTreeContains(tr, wantAnswer)
	})
}

func fpFindFollowUpCopyButtons(tr TreeResult) []Node {
	var out []Node
	for _, n := range tr.Nodes {
		if n.Role != "button" {
			continue
		}
		if n.Description == "复制译文" || n.Label == "复制译文" {
			continue
		}
		if n.Label == "复制" || strings.Contains(n.Description, "复制") {
			out = append(out, n)
		}
	}
	return out
}

func fpProviderCardSlice(tr TreeResult, cardIndex int) []Node {
	wantBadge := fmt.Sprintf("优先级 %d", cardIndex+1)
	nextBadge := fmt.Sprintf("优先级 %d", cardIndex+2)

	startIdx := -1
	for i, n := range tr.Nodes {
		if strings.Contains(n.Label, wantBadge) {
			startIdx = i
			if i >= 2 && tr.Nodes[i-2].Role == "switch" {
				startIdx = i - 2
			} else if i >= 1 {
				startIdx = i - 1
			}
			break
		}
	}
	if startIdx < 0 {
		return nil
	}

	endIdx := len(tr.Nodes)
	for i := startIdx + 3; i < len(tr.Nodes); i++ {
		n := tr.Nodes[i]
		if strings.Contains(n.Label, nextBadge) {
			if i >= 2 && tr.Nodes[i-2].Role == "switch" {
				endIdx = i - 2
			} else {
				endIdx = i - 1
			}
			break
		}
		if n.Role == "button" && n.Label == "+ OpenAI Compatible" {
			endIdx = i
			break
		}
	}
	if endIdx <= startIdx {
		return nil
	}
	return tr.Nodes[startIdx:endIdx]
}

func fpPressProviderTest(t *testing.T, client *AutomationClient, winID, cardIndex int) {
	t.Helper()
	tr := fpWaitTree(t, client, winID, 4*time.Second, fmt.Sprintf("provider card %d test button", cardIndex), func(tr TreeResult) bool {
		card := fpProviderCardSlice(tr, cardIndex)
		for _, n := range card {
			if n.Role == "button" && (n.Label == "测试" || n.Label == "测试连接" || strings.Contains(n.Description, "测试")) && !n.HasState("disabled") {
				return true
			}
		}
		return false
	})
	card := fpProviderCardSlice(tr, cardIndex)
	for _, n := range card {
		if n.Role == "button" && (n.Label == "测试" || n.Label == "测试连接" || strings.Contains(n.Description, "测试")) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if err := client.Act(ctx, ActParams{Window: winID, ID: n.ID, Action: "press"}); err != nil {
				t.Fatalf("press 测试 on card %d: %v", cardIndex, err)
			}
			return
		}
	}
	t.Fatalf("测试 button not found in card %d:\n%s", cardIndex, formatTreeSummary(tr))
}

func fpWaitProviderCardPred(t *testing.T, client *AutomationClient, winID, cardIndex int, timeout time.Duration, desc string, pred func([]Node) bool) []Node {
	t.Helper()
	var matched []Node
	fpWaitTree(t, client, winID, timeout, fmt.Sprintf("card %d: %s", cardIndex, desc), func(tr TreeResult) bool {
		card := fpProviderCardSlice(tr, cardIndex)
		if len(card) > 0 && pred(card) {
			matched = card
			return true
		}
		return false
	})
	return matched
}

func fpSetCardField(t *testing.T, client *AutomationClient, winID, cardIndex int, fieldLabel, value string) {
	t.Helper()
	tr := fpWaitTree(t, client, winID, 4*time.Second, fmt.Sprintf("card %d field %q", cardIndex, fieldLabel), func(tr TreeResult) bool {
		for _, n := range fpProviderCardSlice(tr, cardIndex) {
			if n.Role == "textField" && n.Label == fieldLabel {
				return true
			}
		}
		return false
	})
	for _, n := range fpProviderCardSlice(tr, cardIndex) {
		if n.Role == "textField" && n.Label == fieldLabel {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if err := client.Act(ctx, ActParams{Window: winID, ID: n.ID, Action: "setValue", Text: value}); err != nil {
				t.Fatalf("setValue card %d field %q: %v", cardIndex, fieldLabel, err)
			}
			_ = client.WaitIdle(ctx, winID)
			return
		}
	}
	t.Fatalf("field %q not found in card %d", fieldLabel, cardIndex)
}

func fpFileSHA256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s for sha256: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fpReadClipboardLinux() (string, bool) {
	if runtime.GOOS == "windows" {
		return "", false
	}
	if origWaylandDisplay != "" {
		if p, err := exec.LookPath("wl-paste"); err == nil {
			out, err := exec.Command(p, "--no-newline").Output()
			if err == nil {
				return string(out), true
			}
		}
	}
	if origDisplay != "" {
		if p, err := exec.LookPath("xclip"); err == nil {
			out, err := exec.Command(p, "-o", "-selection", "clipboard").Output()
			if err == nil {
				return string(out), true
			}
		}
	}
	return "", false
}

func fpSnapshotAndRestoreClipboard(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	orig, ok := fpReadClipboardLinux()
	if !ok {
		return
	}
	t.Cleanup(func() {
		if origWaylandDisplay != "" {
			if p, err := exec.LookPath("wl-copy"); err == nil {
				cmd := exec.Command(p)
				cmd.Stdin = strings.NewReader(orig)
				_ = cmd.Run()
				return
			}
		}
		if origDisplay != "" {
			if p, err := exec.LookPath("xclip"); err == nil {
				cmd := exec.Command(p, "-i", "-selection", "clipboard")
				cmd.Stdin = strings.NewReader(orig)
				_ = cmd.Run()
			}
		}
	})
}

func fpAssertCardContainsAny(t *testing.T, card []Node, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if fpNodesContain(card, needle) {
			return
		}
	}
	t.Fatalf("expected card nodes to contain one of %v, got:\n%s", needles, formatTreeSummary(TreeResult{Nodes: card}))
}

func fpHasClickableAction(tr TreeResult, labelSubstr string) (Node, bool) {
	byID := make(map[string]Node, len(tr.Nodes))
	for _, n := range tr.Nodes {
		byID[n.ID] = n
	}
	for _, n := range tr.Nodes {
		if !fpNodeHasText(n, labelSubstr) {
			continue
		}
		cur := n
		for {
			if cur.HasAction("press") {
				return cur, true
			}
			if cur.Parent == "" {
				break
			}
			p, ok := byID[cur.Parent]
			if !ok {
				break
			}
			cur = p
		}
	}
	return Node{}, false
}

// ---------------------------------------------------------------------------
// F9 Tier 1: Multi-Turn Follow-up, Copy & History Persistence (5 tests)
// ---------------------------------------------------------------------------

func TestE2E_F9_T1_MultiTurnFollowUpContextAccumulation(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "初始译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpTranslateInPopup(t, client, win.ID, "resilience", "初始译文")

	// Turn 1 -> 4 messages: system, user(source), assistant(result), user(q1).
	m.SetDefault("chat", MockResponse{Body: "第一轮追问回答"})
	fpSubmitFollowUp(t, client, win.ID, "第一轮问题", "第一轮追问回答", false)

	// Turn 2 -> 6 messages: + assistant(a1), user(q2).
	m.SetDefault("chat", MockResponse{Body: "第二轮追问回答"})
	fpSubmitFollowUp(t, client, win.ID, "第二轮问题", "第二轮追问回答", false)

	// Turn 3 -> 8 messages: + assistant(a2), user(q3).
	m.SetDefault("chat", MockResponse{Body: "第三轮追问回答"})
	tr := fpSubmitFollowUp(t, client, win.ID, "第三轮问题", "第三轮追问回答", false)

	for _, want := range []string{"第一轮问题", "第一轮追问回答", "第二轮问题", "第二轮追问回答", "第三轮问题", "第三轮追问回答"} {
		if !fpTreeContains(tr, want) {
			t.Fatalf("expected multi-turn text %q retained in UI:\n%s", want, formatTreeSummary(tr))
		}
	}

	reqs := m.Requests()
	if len(reqs) != 4 {
		t.Fatalf("expected 4 requests (1 initial + 3 follow-ups), got %d", len(reqs))
	}
	wantLens := []int{2, 4, 6, 8}
	for i, wantLen := range wantLens {
		if len(reqs[i].Messages) != wantLen {
			t.Fatalf("request %d: expected %d messages, got %d (%+v)", i, wantLen, len(reqs[i].Messages), reqs[i].Messages)
		}
	}
	lastMsgs := reqs[3].Messages
	if lastMsgs[1].Role != "user" || !strings.Contains(lastMsgs[1].Text, "resilience") ||
		lastMsgs[2].Role != "assistant" || lastMsgs[2].Text != "初始译文" ||
		lastMsgs[3].Role != "user" || !strings.Contains(lastMsgs[3].Text, "第一轮问题") ||
		lastMsgs[4].Role != "assistant" || lastMsgs[4].Text != "第一轮追问回答" ||
		lastMsgs[5].Role != "user" || !strings.Contains(lastMsgs[5].Text, "第二轮问题") ||
		lastMsgs[6].Role != "assistant" || lastMsgs[6].Text != "第二轮追问回答" ||
		lastMsgs[7].Role != "user" || !strings.Contains(lastMsgs[7].Text, "第三轮问题") {
		t.Fatalf("unexpected Turn 3 multi-turn message sequence: %+v", lastMsgs)
	}
}

func TestE2E_F9_T1_PerTurnCopyButtonsAndClipboard(t *testing.T) {
	fpSnapshotAndRestoreClipboard(t)
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "主译文内容"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpTranslateInPopup(t, client, win.ID, "copyable text", "主译文内容")

	m.SetDefault("chat", MockResponse{Body: "追问回答Alpha"})
	fpSubmitFollowUp(t, client, win.ID, "问题1", "追问回答Alpha", false)

	m.SetDefault("chat", MockResponse{Body: "追问回答Beta"})
	tr := fpSubmitFollowUp(t, client, win.ID, "问题2", "追问回答Beta", false)

	copyBtns := fpFindFollowUpCopyButtons(tr)
	if len(copyBtns) < 2 {
		t.Fatalf("expected at least 2 per-turn copy buttons, found %d:\n%s", len(copyBtns), formatTreeSummary(tr))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Click first turn's copy button.
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: copyBtns[0].ID, Action: "press"}); err != nil {
		t.Fatalf("press turn 1 copy button: %v", err)
	}
	fpWaitText(t, client, win.ID, 4*time.Second, "已复制")
	if got, ok := fpReadClipboardLinux(); ok && got != "" && got != "追问回答Alpha" {
		t.Fatalf("expected clipboard '追问回答Alpha', got %q", got)
	}

	// Click second turn's copy button.
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: copyBtns[1].ID, Action: "press"}); err != nil {
		t.Fatalf("press turn 2 copy button: %v", err)
	}
	fpWaitText(t, client, win.ID, 4*time.Second, "已复制")
	if got, ok := fpReadClipboardLinux(); ok && got != "" && got != "追问回答Beta" {
		t.Fatalf("expected clipboard '追问回答Beta', got %q", got)
	}

	// Turns remain intact after copying.
	tr, err := client.Tree(ctx, win.ID)
	if err != nil {
		t.Fatalf("Tree after copy: %v", err)
	}
	if !fpTreeContains(tr, "追问回答Alpha") || !fpTreeContains(tr, "追问回答Beta") {
		t.Fatalf("follow-up turns altered after copying:\n%s", formatTreeSummary(tr))
	}
}

func TestE2E_F9_T1_FollowUpSavedToHistoryAndRestoredAfterRestart(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "初始历史译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	proc, client, win := fpOpenMainPanel(t, s)
	fpTranslateInPopup(t, client, win.ID, "history-persist-source", "初始历史译文")

	m.SetDefault("chat", MockResponse{Body: "历史追问回答一"})
	fpSubmitFollowUp(t, client, win.ID, "历史追问一", "历史追问回答一", false)

	m.SetDefault("chat", MockResponse{Body: "历史追问回答二"})
	fpSubmitFollowUp(t, client, win.ID, "历史追问二", "历史追问回答二", false)

	fpPollUntil(t, 4*time.Second, "single history row persisted", func() bool {
		return s.countHistoryRows() == 1
	})

	// Restart GUI in the same sandbox to prove on-disk persistence in history.db.
	_ = client.Close()
	proc.Stop(t)

	_, client2, win2 := fpOpenMainPanel(t, s)
	fpSwitchPanelTab(t, client2, win2.ID, "历史")

	var rowBtn Node
	tr := fpWaitTree(t, client2, win2.ID, 5*time.Second, "persisted history row", func(tr TreeResult) bool {
		btn, ok := fpHasClickableAction(tr, "history-persist-source")
		if ok {
			rowBtn = btn
			return true
		}
		return false
	})
	_ = tr

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client2.Act(ctx, ActParams{Window: win2.ID, ID: rowBtn.ID, Action: "press"}); err != nil {
		t.Fatalf("open history entry: %v", err)
	}

	tr = fpWaitTree(t, client2, win2.ID, 5*time.Second, "restored follow-up turns in popup", func(tr TreeResult) bool {
		return fpTreeContains(tr, "history-persist-source") &&
			fpTreeContains(tr, "初始历史译文") &&
			fpTreeContains(tr, "历史追问一") &&
			fpTreeContains(tr, "历史追问回答一") &&
			fpTreeContains(tr, "历史追问二") &&
			fpTreeContains(tr, "历史追问回答二")
	})
	_ = tr

	// Continue with Turn 3 after restart -> must send 8 messages and update existing row (still 1 row).
	m.ClearRequests()
	m.SetDefault("chat", MockResponse{Body: "重启后第三轮回答"})
	fpSubmitFollowUp(t, client2, win2.ID, "重启后第三轮问题", "重启后第三轮回答", false)

	reqs := m.Requests()
	if len(reqs) != 1 || len(reqs[0].Messages) != 8 {
		t.Fatalf("expected 1 request with 8 messages on restored turn 3, got %+v", reqs)
	}
	if err := client2.WaitIdle(ctx, win2.ID); err != nil {
		t.Fatalf("WaitIdle: %v", err)
	}
	if cnt := s.countHistoryRows(); cnt != 1 {
		t.Fatalf("expected follow-up update to preserve single history row, got count=%d", cnt)
	}
}

func TestE2E_F9_T1_FollowUpWorksInNonTranslateModeAndCtrlEnter(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "初始润色译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["activeMode"] = "polish"
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpTranslateInPopup(t, client, win.ID, "draft sentence", "初始润色译文")

	m.SetDefault("chat", MockResponse{Body: "更精简的润色版本"})
	tr := fpSubmitFollowUp(t, client, win.ID, "再短一点", "更精简的润色版本", true)
	if !fpTreeContains(tr, "再短一点") {
		t.Fatalf("expected Ctrl+Enter follow-up question rendered:\n%s", formatTreeSummary(tr))
	}

	reqs := m.Requests()
	if len(reqs) != 2 || len(reqs[1].Messages) != 4 {
		t.Fatalf("expected 2 requests with 4 messages on Ctrl+Enter follow-up, got %+v", reqs)
	}
}

func TestE2E_F9_T1_FollowUpStreamingChunksAndDisabledWhileInFlight(t *testing.T) {
	s := newSandbox(t)
	c := fpNewControlledLLM(t)
	c.Mock.SetDefault("chat", MockResponse{Body: "基础译文"})

	cfg := defaultLoopbackConfig(c.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpTranslateInPopup(t, client, win.ID, "streaming follow up", "基础译文")

	gate := c.ArmGate(false, "CHUNK_ONE_", "CHUNK_TWO")
	fpSetFollowUpInput(t, client, win.ID, "流式追问")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sendBtn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return fpIsFollowUpSendBtn(n) && !n.HasState("disabled")
	})
	if err != nil {
		t.Fatalf("WaitNode enabled follow-up button: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: sendBtn.ID, Action: "press"}); err != nil {
		t.Fatalf("press follow-up button: %v", err)
	}

	gate.WaitStarted(t, 5*time.Second)

	// Mid-stream: partial chunk visible AND send button disabled.
	fpWaitTree(t, client, win.ID, 4*time.Second, "mid-stream follow-up chunk with disabled button", func(tr TreeResult) bool {
		btn, ok := fpFindNode(tr, fpIsFollowUpSendBtn)
		return ok && btn.HasState("disabled") && fpTreeContains(tr, "CHUNK_ONE_")
	})

	gate.Close()

	// Complete: full text visible AND send button re-enabled.
	fpWaitTree(t, client, win.ID, 5*time.Second, "completed follow-up chunks with re-enabled button", func(tr TreeResult) bool {
		btn, ok := fpFindNode(tr, fpIsFollowUpSendBtn)
		return ok && !btn.HasState("disabled") && fpTreeContains(tr, "CHUNK_ONE_CHUNK_TWO")
	})
}

// ---------------------------------------------------------------------------
// F9 Tier 2: Boundaries, Error Isolation & Mode Restrictions (5 tests)
// ---------------------------------------------------------------------------

func TestE2E_F9_T2_FollowUpHiddenDuringTranslationAndClearedOnRetranslate(t *testing.T) {
	s := newSandbox(t)
	c := fpNewControlledLLM(t)
	gate := c.ArmGate(true, "初始翻译完成")

	cfg := defaultLoopbackConfig(c.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)

	// Start initial translation while held before first chunk.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	editor, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
	})
	if err != nil {
		t.Fatalf("WaitNode popup editor: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: editor.ID, Action: "setValue", Text: "retranslate clears"}); err != nil {
		t.Fatalf("setValue: %v", err)
	}
	tBtn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return fpIsPopupSubmitBtn(n) && !n.HasState("disabled")
	})
	if err != nil {
		t.Fatalf("WaitNode translate button: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: tBtn.ID, Action: "press"}); err != nil {
		t.Fatalf("press translate: %v", err)
	}

	gate.WaitStarted(t, 5*time.Second)
	tr := fpWaitText(t, client, win.ID, 4*time.Second, "正在翻译…")
	if _, hasInput := fpFindNode(tr, fpIsFollowUpInput); hasInput {
		t.Fatalf("follow-up input must be hidden while initial translation streams:\n%s", formatTreeSummary(tr))
	}

	gate.Close()
	fpWaitTree(t, client, win.ID, 5*time.Second, "initial translation finished with follow-up visible", func(tr TreeResult) bool {
		_, hasInput := fpFindNode(tr, fpIsFollowUpInput)
		return hasInput && fpTreeContains(tr, "初始翻译完成")
	})

	c.Mock.SetDefault("chat", MockResponse{Body: "旧追问回答"})
	fpSubmitFollowUp(t, client, win.ID, "旧追问问题", "旧追问回答", false)

	// Click "重新翻译" -> must clear prior follow-up turns.
	c.Mock.SetDefault("chat", MockResponse{Body: "全新译文"})
	reBtn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "button" && n.Label == "重新翻译" && n.Key == nil && n.Description == "" && !n.HasState("disabled")
	})
	if err != nil {
		t.Fatalf("WaitNode 重新翻译: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: reBtn.ID, Action: "press"}); err != nil {
		t.Fatalf("press 重新翻译: %v", err)
	}
	tr = fpWaitText(t, client, win.ID, 5*time.Second, "全新译文")
	if fpTreeContains(tr, "旧追问问题") || fpTreeContains(tr, "旧追问回答") {
		t.Fatalf("prior follow-up turns were not cleared on retranslate:\n%s", formatTreeSummary(tr))
	}
}

func TestE2E_F9_T2_FollowUpDisabledInMultiEngineAndImageMode(t *testing.T) {
	t.Run("multi_engine_mode", func(t *testing.T) {
		s := newSandbox(t)
		m1 := newMockLLMServer(t)
		m1.SetDefault("chat", MockResponse{Body: "多引擎A"})
		m2 := newMockLLMServer(t)
		m2.SetDefault("chat", MockResponse{Body: "多引擎B"})

		cfg := defaultLoopbackConfig(m1.V1URL(), "m1")
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		cfg["multiEngine"] = true
		cfg["providers"] = []map[string]any{
			{"id": "p1", "name": "P1", "kind": "openai", "enabled": true, "baseURL": m1.V1URL(), "model": "m1"},
			{"id": "p2", "name": "P2", "kind": "openai", "enabled": true, "baseURL": m2.V1URL(), "model": "m2"},
		}
		s.writeConfigJSON(cfg)

		_, client, win := fpOpenMainPanel(t, s)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		editor, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
			return n.Role == "textField" && strings.Contains(n.Placeholder, "输入文字")
		})
		if err != nil {
			t.Fatalf("WaitNode editor: %v", err)
		}
		if err := client.Act(ctx, ActParams{Window: win.ID, ID: editor.ID, Action: "setValue", Text: "multi engine test"}); err != nil {
			t.Fatalf("setValue: %v", err)
		}
		tBtn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
			return fpIsPopupSubmitBtn(n) && !n.HasState("disabled")
		})
		if err != nil {
			t.Fatalf("WaitNode translate: %v", err)
		}
		if err := client.Act(ctx, ActParams{Window: win.ID, ID: tBtn.ID, Action: "press"}); err != nil {
			t.Fatalf("press translate: %v", err)
		}

		tr := fpWaitText(t, client, win.ID, 6*time.Second, "全部完成 (2/2 成功)")
		if _, ok := fpFindNode(tr, fpIsFollowUpInput); ok {
			t.Fatalf("follow-up input must not render in multiEngine mode:\n%s", formatTreeSummary(tr))
		}
		if _, ok := fpFindNode(tr, fpIsFollowUpSendBtn); ok {
			t.Fatalf("follow-up button must not render in multiEngine mode:\n%s", formatTreeSummary(tr))
		}
	})

	t.Run("image_mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Linux spectacle screenshot shim only runs on non-Windows hosts")
		}
		s := newSandbox(t)
		s.DBusAddr = "unix:path=" + filepath.Join(s.RuntimeDir, "no-dbus")
		s.IsolatePATH = true
		s.installShim("spectacle")
		s.setShimState("spectacle_mode", "ok")
		shotPath := filepath.Join(s.TmpDir, "shot.png")
		if err := os.WriteFile(shotPath, makePNG(t, 200, 120), 0o600); err != nil {
			t.Fatalf("write shot.png: %v", err)
		}
		s.setShimState("fixture_png", shotPath)

		m := newMockLLMServer(t)
		m.SetDefault("chat", MockResponse{Body: "截图模式结果"})

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		_, client, win := fpOpenMainPanel(t, s, "--silent")
		if res := s.runForwardCLI(5*time.Second, "-c"); res.ExitCode != 0 {
			t.Fatalf("-c failed: %+v", res)
		}

		tr := fpWaitTree(t, client, win.ID, 6*time.Second, "image result without follow-up", func(tr TreeResult) bool {
			return fpTreeContains(tr, "视觉模型截图识别") && fpTreeContains(tr, "截图模式结果")
		})
		if _, ok := fpFindNode(tr, fpIsFollowUpInput); ok {
			t.Fatalf("follow-up input must not render in image mode:\n%s", formatTreeSummary(tr))
		}
	})
}

func TestE2E_F9_T2_EmptyAndWhitespaceFollowUpIgnored(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "基础结果"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpTranslateInPopup(t, client, win.ID, "whitespace followup", "基础结果")
	fpPollUntil(t, 4*time.Second, "history row saved", func() bool {
		return s.countHistoryRows() == 1
	})
	m.ClearRequests()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, ws := range []string{"", "   \n\t  "} {
		fpSetFollowUpInput(t, client, win.ID, ws)
		if btn, ok := fpFindNode(fpWaitTree(t, client, win.ID, 3*time.Second, "follow-up btn", func(tr TreeResult) bool {
			_, ok := fpFindNode(tr, fpIsFollowUpSendBtn)
			return ok
		}), fpIsFollowUpSendBtn); ok && !btn.HasState("disabled") {
			_ = client.Act(ctx, ActParams{Window: win.ID, ID: btn.ID, Action: "press"})
		}
		if err := client.WaitIdle(ctx, win.ID); err != nil {
			t.Fatalf("WaitIdle: %v", err)
		}
	}

	if len(m.Requests()) != 0 {
		t.Fatalf("expected 0 HTTP requests for empty/whitespace follow-up, got %d", len(m.Requests()))
	}
	if cnt := s.countHistoryRows(); cnt != 1 {
		t.Fatalf("expected history row count unchanged (1), got %d", cnt)
	}
}

func TestE2E_F9_T2_FollowUpErrorTurnDoesNotCorruptPriorTurnsOrHistory(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "基准译文"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpTranslateInPopup(t, client, win.ID, "error isolation", "基准译文")

	// Turn 1 succeeds.
	m.SetDefault("chat", MockResponse{Body: "首轮有效回答"})
	fpSubmitFollowUp(t, client, win.ID, "首轮有效问题", "首轮有效回答", false)

	// Turn 2 fails (HTTP 500).
	m.SetDefault("chat", MockResponse{Status: http.StatusInternalServerError, Body: "boom 500"})
	fpSetFollowUpInput(t, client, win.ID, "失败问题")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	btn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return fpIsFollowUpSendBtn(n) && !n.HasState("disabled")
	})
	if err != nil {
		t.Fatalf("WaitNode follow-up button: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press follow-up button: %v", err)
	}

	tr := fpWaitTree(t, client, win.ID, 5*time.Second, "follow-up error with Turn 1 preserved", func(tr TreeResult) bool {
		return fpTreeContains(tr, "翻译服务暂时不可用") &&
			fpTreeContains(tr, "首轮有效问题") &&
			fpTreeContains(tr, "首轮有效回答")
	})
	_ = tr

	// Turn 3 succeeds -> must send 6 messages (excluding failed Turn 2).
	m.ClearRequests()
	m.SetDefault("chat", MockResponse{Body: "恢复后回答"})
	fpSubmitFollowUp(t, client, win.ID, "恢复后问题", "恢复后回答", false)

	reqs := m.Requests()
	if len(reqs) != 1 || len(reqs[0].Messages) != 6 {
		t.Fatalf("expected 6 messages (excluding failed turn 2), got %+v", reqs)
	}
	for _, msg := range reqs[0].Messages {
		if strings.Contains(msg.Text, "失败问题") {
			t.Fatalf("failed follow-up question leaked into subsequent turn context: %+v", reqs[0].Messages)
		}
	}
}

func TestE2E_F9_T2_FollowUpWithBaiduOrAzureOnlyShowsGuidanceNoHTTP(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("chat", MockResponse{Body: "初始内容"})
	m.SetDefault("baidu", MockResponse{Body: "百度译文"})
	m.SetExpectedBaiduSecret("baidu-secret")

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":       "baidu",
			"name":     "百度翻译",
			"kind":     "baidu",
			"enabled":  true,
			"endpoint": m.URL() + "/api/trans/vip/translate",
			"appID":    "appid-123",
		},
	}
	s.writeConfigJSON(cfg)

	// Seed translation result via UI? Actually Baidu requires secret from keyring or settings,
	// so start with OpenAI for initial translation, then hot-reload config to Baidu-only (with Azure/Baidu)
	// OR configure OpenAI disabled and seed via settings? Even simpler: start with OpenAI enabled,
	// translate "initial", then hot-reload config.json to have ONLY Baidu enabled (with no OpenAI)!
	// Wait: Baidu needs a key in keyring; instead, let's check what happens if we hot-reload to
	// disabled OpenAI + enabled Azure/Baidu (no OpenAI Compatible enabled)!
	// Wait: even better, start with OpenAI enabled, translate "guidance test", then hot-reload
	// config.json to set OpenAI enabled=false and Baidu enabled=true -> footer changes to "百度翻译",
	// then submit follow-up -> sendFollowUp checks EnabledPromptProviders() -> 0 OpenAI -> shows
	// "追问需要启用一个配置好的 OpenAI Compatible 服务" and makes 0 HTTP requests!
	cfg["providers"] = []map[string]any{
		{
			"id":      "openai",
			"name":    "OpenAI-Init",
			"kind":    "openai",
			"enabled": true,
			"baseURL": m.V1URL(),
			"model":   "gpt-4o-mini",
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpTranslateInPopup(t, client, win.ID, "guidance test", "初始内容")
	fpPollUntil(t, 4*time.Second, "history row saved", func() bool {
		return s.countHistoryRows() == 1
	})

	// Hot-reload to disable OpenAI and leave only Azure/Baidu in config.
	cfg["providers"] = []map[string]any{
		{
			"id":      "openai",
			"name":    "OpenAI-Init",
			"kind":    "openai",
			"enabled": false,
			"baseURL": m.V1URL(),
			"model":   "gpt-4o-mini",
		},
		{
			"id":       "azure",
			"name":     "AzureOnly",
			"kind":     "azure",
			"enabled":  true,
			"endpoint": m.URL() + "/translate",
		},
	}
	s.writeConfigJSON(cfg)
	fpWaitTree(t, client, win.ID, 6*time.Second, "OpenAI disabled after hot-reload", func(tr TreeResult) bool {
		return !fpTreeContains(tr, "OpenAI-Init")
	})
	m.ClearRequests()

	fpSetFollowUpInput(t, client, win.ID, "尝试追问")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	btn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return fpIsFollowUpSendBtn(n) && !n.HasState("disabled")
	})
	if err != nil {
		t.Fatalf("WaitNode follow-up send button: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: btn.ID, Action: "press"}); err != nil {
		t.Fatalf("press follow-up send: %v", err)
	}

	fpWaitText(t, client, win.ID, 4*time.Second, "OpenAI Compatible")
	if len(m.Requests()) != 0 {
		t.Fatalf("expected 0 HTTP requests when no OpenAI Compatible provider is enabled, got %d", len(m.Requests()))
	}
	if cnt := s.countHistoryRows(); cnt != 1 {
		t.Fatalf("expected history row count unchanged (1), got %d", cnt)
	}
}

// ---------------------------------------------------------------------------
// F10 Tier 1: Settings "测试" Connection Probe (5 tests)
// ---------------------------------------------------------------------------

func TestE2E_F10_T1_OpenAIProbeSuccessLatencyVisionAndCleanState(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("models", MockResponse{Status: http.StatusOK, Models: []string{"gpt-4o-mini", "gpt-4o"}})
	m.SetDefault("chat", MockResponse{Status: http.StatusOK, Body: "ok"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)
	beforeHash := fpFileSHA256(t, s.configPath())

	_, client, win := fpOpenMainPanel(t, s)
	fpOpenSettingsEnginesTab(t, client, win.ID)

	fpPressProviderTest(t, client, win.ID, 0)

	card := fpWaitProviderCardPred(t, client, win.ID, 0, 6*time.Second, "OpenAI probe success with latency and vision", func(nodes []Node) bool {
		return (fpNodesContain(nodes, "ms") || fpNodesContain(nodes, "µs") || fpNodesContain(nodes, "s")) &&
			(fpNodesContain(nodes, "成功") || fpNodesContain(nodes, "正常") || fpNodesContain(nodes, "可用") || fpNodesContain(nodes, "通过"))
	})
	fpAssertCardContainsAny(t, card, "视觉", "Vision", "vision")

	if len(m.Requests()) == 0 {
		t.Fatalf("expected probe to send HTTP request(s) to MockLLMServer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	tr, err := client.Tree(ctx, win.ID)
	if err != nil {
		t.Fatalf("Tree after probe: %v", err)
	}
	if fpTreeContains(tr, "放弃修改") {
		t.Fatalf("probing must not mark settingsDirty()=true:\n%s", formatTreeSummary(tr))
	}

	// Closing panel must succeed without "有未保存的修改" confirmation dialog.
	closeBtn, ok := fpFindNode(tr, func(n Node) bool {
		return n.Role == "button" && n.Label == "收起面板"
	})
	if !ok {
		t.Fatalf("expected 收起面板 button in panel header")
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: closeBtn.ID, Action: "press"}); err != nil {
		t.Fatalf("press 收起面板: %v", err)
	}
	fpPollUntil(t, 4*time.Second, "panel closes cleanly without dirty dialog", func() bool {
		wins, err := client.Windows(ctx)
		if err != nil {
			return false
		}
		for _, w := range wins {
			if w.ID == win.ID && !w.Visible {
				return true
			}
		}
		return false
	})

	if afterHash := fpFileSHA256(t, s.configPath()); afterHash != beforeHash {
		t.Fatalf("config.json mutated by probe: before=%s after=%s", beforeHash, afterHash)
	}
}

func TestE2E_F10_T1_OpenAIVisionDisabledReportsStatus(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("models", MockResponse{Status: http.StatusOK, Models: []string{"gpt-4o-mini"}})
	m.SetDefault("chat", MockResponse{Status: http.StatusOK, Body: "ok"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":      "openai",
			"name":    "TextOnlyOpenAI",
			"kind":    "openai",
			"enabled": true,
			"baseURL": m.V1URL(),
			"model":   "gpt-4o-mini",
			"vision":  false,
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpOpenSettingsEnginesTab(t, client, win.ID)
	fpPressProviderTest(t, client, win.ID, 0)

	card := fpWaitProviderCardPred(t, client, win.ID, 0, 6*time.Second, "vision disabled probe result", func(nodes []Node) bool {
		return (fpNodesContain(nodes, "ms") || fpNodesContain(nodes, "µs")) &&
			(fpNodesContain(nodes, "未开启视觉") || fpNodesContain(nodes, "未启用视觉") || fpNodesContain(nodes, "不支持视觉") || fpNodesContain(nodes, "仅文本") || fpNodesContain(nodes, "关闭"))
	})
	fpAssertCardContainsAny(t, card, "成功", "正常", "可用", "通过")
}

func TestE2E_F10_T1_ProbeUsesUnsavedDraftBeforeSave(t *testing.T) {
	s := newSandbox(t)
	mBad := newMockLLMServer(t)
	mBad.SetDefault("models", MockResponse{Status: http.StatusUnauthorized, Body: "bad old endpoint"})
	mBad.SetDefault("chat", MockResponse{Status: http.StatusUnauthorized, Body: "bad old endpoint"})

	mDraft := newMockLLMServer(t)
	mDraft.SetDefault("models", MockResponse{Status: http.StatusOK, Models: []string{"gpt-4o-draft"}})
	mDraft.SetDefault("chat", MockResponse{Status: http.StatusOK, Body: "ok"})

	cfg := defaultLoopbackConfig(mBad.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)
	beforeHash := fpFileSHA256(t, s.configPath())

	_, client, win := fpOpenMainPanel(t, s)
	fpOpenSettingsEnginesTab(t, client, win.ID)

	// Edit Base URL and Model in unsaved draft without clicking "保存设置".
	fpSetCardField(t, client, win.ID, 0, "接口地址 Base URL", mDraft.V1URL())
	fpSetCardField(t, client, win.ID, 0, "模型", "gpt-4o-draft")

	fpPressProviderTest(t, client, win.ID, 0)
	fpWaitProviderCardPred(t, client, win.ID, 0, 6*time.Second, "unsaved draft endpoint probed successfully", func(nodes []Node) bool {
		return (fpNodesContain(nodes, "ms") || fpNodesContain(nodes, "µs")) &&
			(fpNodesContain(nodes, "成功") || fpNodesContain(nodes, "正常") || fpNodesContain(nodes, "可用") || fpNodesContain(nodes, "通过"))
	})

	if len(mDraft.Requests()) == 0 {
		t.Fatalf("expected probe to target unsaved draft BaseURL %s", mDraft.V1URL())
	}
	if len(mBad.Requests()) != 0 {
		t.Fatalf("expected saved BaseURL %s not to be probed when draft was edited, got %d reqs", mBad.V1URL(), len(mBad.Requests()))
	}

	// Discard changes and verify config.json remains untouched.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	discardBtn, _, err := client.WaitNode(ctx, win.ID, func(n Node) bool {
		return n.Role == "button" && n.Label == "放弃修改"
	})
	if err != nil {
		t.Fatalf("WaitNode 放弃修改: %v", err)
	}
	if err := client.Act(ctx, ActParams{Window: win.ID, ID: discardBtn.ID, Action: "press"}); err != nil {
		t.Fatalf("press 放弃修改: %v", err)
	}
	if afterHash := fpFileSHA256(t, s.configPath()); afterHash != beforeHash {
		t.Fatalf("config.json mutated: before=%s after=%s", beforeHash, afterHash)
	}
}

func TestE2E_F10_T1_AzureAndBaiduProbeSuccess(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("azure", MockResponse{Status: http.StatusOK, Body: "azure ok"})
	m.SetDefault("baidu", MockResponse{Status: http.StatusOK, Body: "baidu ok"})
	m.SetExpectedBaiduSecret("baidu-secret-ok")

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":       "azure",
			"name":     "AzureTest",
			"kind":     "azure",
			"enabled":  true,
			"endpoint": m.URL() + "/translate",
			"region":   "eastasia",
		},
		{
			"id":       "baidu",
			"name":     "BaiduTest",
			"kind":     "baidu",
			"enabled":  true,
			"endpoint": m.URL() + "/api/trans/vip/translate",
			"appID":    "20260001",
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpOpenSettingsEnginesTab(t, client, win.ID)

	// Enter secret keys in the card password fields (draft only).
	fpSetCardField(t, client, win.ID, 0, "订阅 Key", "azure-sub-key")
	fpSetCardField(t, client, win.ID, 1, "密钥 Secret", "baidu-secret-ok")

	fpPressProviderTest(t, client, win.ID, 0)
	fpWaitProviderCardPred(t, client, win.ID, 0, 6*time.Second, "Azure probe success", func(nodes []Node) bool {
		return (fpNodesContain(nodes, "ms") || fpNodesContain(nodes, "µs")) &&
			(fpNodesContain(nodes, "成功") || fpNodesContain(nodes, "正常") || fpNodesContain(nodes, "可用") || fpNodesContain(nodes, "通过"))
	})

	fpPressProviderTest(t, client, win.ID, 1)
	fpWaitProviderCardPred(t, client, win.ID, 1, 6*time.Second, "Baidu probe success", func(nodes []Node) bool {
		return (fpNodesContain(nodes, "ms") || fpNodesContain(nodes, "µs")) &&
			(fpNodesContain(nodes, "成功") || fpNodesContain(nodes, "正常") || fpNodesContain(nodes, "可用") || fpNodesContain(nodes, "通过"))
	})

	var sawAzure, sawBaidu bool
	for _, req := range m.Requests() {
		if req.Path == "/translate" {
			sawAzure = true
		}
		if req.Path == "/api/trans/vip/translate" {
			sawBaidu = true
		}
	}
	if !sawAzure || !sawBaidu {
		t.Fatalf("expected both Azure (/translate) and Baidu (/api/trans/vip/translate) requests, got %+v", m.Requests())
	}
}

func TestE2E_F10_T1_MultipleProvidersIndependentInlineResults(t *testing.T) {
	s := newSandbox(t)
	mOK := newMockLLMServer(t)
	mOK.SetDefault("models", MockResponse{Status: http.StatusOK, Models: []string{"m-ok"}})
	mOK.SetDefault("chat", MockResponse{Status: http.StatusOK, Body: "ok"})

	mFail := newMockLLMServer(t)
	mFail.SetDefault("models", MockResponse{Status: http.StatusUnauthorized, Body: "unauthorized"})
	mFail.SetDefault("chat", MockResponse{Status: http.StatusUnauthorized, Body: "unauthorized"})

	cfg := defaultLoopbackConfig(mOK.V1URL(), "m-ok")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":      "p1",
			"name":    "HealthyCard",
			"kind":    "openai",
			"enabled": true,
			"baseURL": mOK.V1URL(),
			"model":   "m-ok",
			"vision":  true,
		},
		{
			"id":      "p2",
			"name":    "UnauthorizedCard",
			"kind":    "openai",
			"enabled": true,
			"baseURL": mFail.V1URL(),
			"model":   "m-fail",
			"vision":  true,
		},
	}
	s.writeConfigJSON(cfg)
	beforeHash := fpFileSHA256(t, s.configPath())

	_, client, win := fpOpenMainPanel(t, s)
	fpOpenSettingsEnginesTab(t, client, win.ID)

	fpPressProviderTest(t, client, win.ID, 0)
	fpWaitProviderCardPred(t, client, win.ID, 0, 6*time.Second, "card 0 success", func(nodes []Node) bool {
		return fpNodesContain(nodes, "成功") || fpNodesContain(nodes, "正常") || fpNodesContain(nodes, "可用") || fpNodesContain(nodes, "通过")
	})

	fpPressProviderTest(t, client, win.ID, 1)
	fpWaitProviderCardPred(t, client, win.ID, 1, 6*time.Second, "card 1 auth error", func(nodes []Node) bool {
		return fpNodesContain(nodes, "鉴权") || fpNodesContain(nodes, "401") || fpNodesContain(nodes, "Key") || fpNodesContain(nodes, "失败")
	})

	// Card 0's inline success must still remain intact.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	tr, err := client.Tree(ctx, win.ID)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	card0 := fpProviderCardSlice(tr, 0)
	fpAssertCardContainsAny(t, card0, "成功", "正常", "可用", "通过")
	if fpTreeContains(tr, "放弃修改") {
		t.Fatalf("multi-card probe must not mark settingsDirty()=true")
	}
	if afterHash := fpFileSHA256(t, s.configPath()); afterHash != beforeHash {
		t.Fatalf("config.json mutated: before=%s after=%s", beforeHash, afterHash)
	}
}

// ---------------------------------------------------------------------------
// F10 Tier 2: Error Paths, Timeouts, Credentials & Provider Failures (5 tests)
// ---------------------------------------------------------------------------

func TestE2E_F10_T2_OpenAIAuthFailure401(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("models", MockResponse{Status: http.StatusUnauthorized, Body: "invalid api key"})
	m.SetDefault("chat", MockResponse{Status: http.StatusUnauthorized, Body: "invalid api key"})

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)
	beforeHash := fpFileSHA256(t, s.configPath())

	_, client, win := fpOpenMainPanel(t, s)
	fpOpenSettingsEnginesTab(t, client, win.ID)
	fpPressProviderTest(t, client, win.ID, 0)

	fpWaitProviderCardPred(t, client, win.ID, 0, 6*time.Second, "401 auth failure inline", func(nodes []Node) bool {
		return fpNodesContain(nodes, "鉴权") || fpNodesContain(nodes, "401") || fpNodesContain(nodes, "API Key")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	tr, err := client.Tree(ctx, win.ID)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if fpTreeContains(tr, "放弃修改") {
		t.Fatalf("401 probe error must not set settingsDirty()=true")
	}
	if afterHash := fpFileSHA256(t, s.configPath()); afterHash != beforeHash {
		t.Fatalf("config.json mutated on 401 probe")
	}
}

func TestE2E_F10_T2_OpenAIModelMissing404(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("models", MockResponse{Status: http.StatusOK, Models: []string{"only-other-model"}})
	m.SetDefault("chat", MockResponse{Status: http.StatusNotFound, Body: "model not found: nonexistent-model"})

	cfg := defaultLoopbackConfig(m.V1URL(), "nonexistent-model")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpOpenSettingsEnginesTab(t, client, win.ID)
	fpPressProviderTest(t, client, win.ID, 0)

	card := fpWaitProviderCardPred(t, client, win.ID, 0, 6*time.Second, "model unavailable error", func(nodes []Node) bool {
		return fpNodesContain(nodes, "模型") || fpNodesContain(nodes, "404") || fpNodesContain(nodes, "不存在") || fpNodesContain(nodes, "不可用")
	})
	if fpNodesContain(card, "鉴权失败") {
		t.Fatalf("model 404 should not report auth failure:\n%s", formatTreeSummary(TreeResult{Nodes: card}))
	}
}

func TestE2E_F10_T2_UnreachableEndpointAndMidProbeCloseSafety(t *testing.T) {
	t.Run("unreachable_closed_port", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen ephemeral: %v", err)
		}
		closedURL := "http://" + ln.Addr().String() + "/v1"
		_ = ln.Close()

		s := newSandbox(t)
		cfg := defaultLoopbackConfig(closedURL, "gpt-4o-mini")
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		_, client, win := fpOpenMainPanel(t, s)
		fpOpenSettingsEnginesTab(t, client, win.ID)
		fpPressProviderTest(t, client, win.ID, 0)

		fpWaitProviderCardPred(t, client, win.ID, 0, 6*time.Second, "connection refused inline error", func(nodes []Node) bool {
			return fpNodesContain(nodes, "失败") || fpNodesContain(nodes, "连接") || fpNodesContain(nodes, "无法") || fpNodesContain(nodes, "不可用")
		})
	})

	t.Run("close_panel_during_slow_probe", func(t *testing.T) {
		s := newSandbox(t)
		m := newMockLLMServer(t)
		m.SetDefault("models", MockResponse{Delay: 2 * time.Second, Models: []string{"gpt-4o-mini"}})
		m.SetDefault("chat", MockResponse{Delay: 2 * time.Second, Body: "ok"})

		cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
		cfg["popupPinned"] = true
		cfg["disableSelectionToolbar"] = true
		s.writeConfigJSON(cfg)

		proc, client, win := fpOpenMainPanel(t, s)
		fpOpenSettingsEnginesTab(t, client, win.ID)
		fpPressProviderTest(t, client, win.ID, 0)

		fpPollUntil(t, 4*time.Second, "slow probe HTTP request in flight", func() bool {
			return len(m.Requests()) >= 1
		})

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		tr, err := client.Tree(ctx, win.ID)
		if err != nil {
			t.Fatalf("Tree during slow probe: %v", err)
		}
		closeBtn, ok := fpFindNode(tr, func(n Node) bool {
			return n.Role == "button" && n.Label == "收起面板"
		})
		if !ok {
			t.Fatalf("expected 收起面板 button")
		}
		if err := client.Act(ctx, ActParams{Window: win.ID, ID: closeBtn.ID, Action: "press"}); err != nil {
			t.Fatalf("press 收起面板 mid-probe: %v", err)
		}
		if err := client.WaitIdle(ctx, win.ID); err != nil {
			t.Fatalf("WaitIdle after mid-probe close: %v", err)
		}
		if proc.exited.Load() {
			t.Fatalf("GUI process crashed on mid-probe close")
		}
	})
}

func TestE2E_F10_T2_MissingCredentialsFailsLocallyNoHTTP(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":      "openai-remote",
			"name":    "RemoteOpenAI",
			"kind":    "openai",
			"enabled": true,
			"baseURL": "https://api.example.invalid/v1",
			"model":   "gpt-4o-mini",
		},
		{
			"id":       "azure-nokey",
			"name":     "AzureNoKey",
			"kind":     "azure",
			"enabled":  true,
			"endpoint": m.URL() + "/translate",
		},
		{
			"id":       "baidu-nokey",
			"name":     "BaiduNoKey",
			"kind":     "baidu",
			"enabled":  true,
			"endpoint": m.URL() + "/api/trans/vip/translate",
			"appID":    "",
		},
	}
	s.writeConfigJSON(cfg)

	_, client, win := fpOpenMainPanel(t, s)
	fpOpenSettingsEnginesTab(t, client, win.ID)

	for cardIdx := 0; cardIdx < 3; cardIdx++ {
		fpPressProviderTest(t, client, win.ID, cardIdx)
		fpWaitProviderCardPred(t, client, win.ID, cardIdx, 4*time.Second, "missing credential inline error", func(nodes []Node) bool {
			return fpNodesContain(nodes, "Key") || fpNodesContain(nodes, "密钥") || fpNodesContain(nodes, "APP ID") || fpNodesContain(nodes, "配置") || fpNodesContain(nodes, "填写")
		})
	}

	if len(m.Requests()) != 0 {
		t.Fatalf("expected 0 HTTP requests when credentials missing, got %d", len(m.Requests()))
	}
}

func TestE2E_F10_T2_BaiduInvalidSign54001AndAzure401(t *testing.T) {
	s := newSandbox(t)
	m := newMockLLMServer(t)
	m.SetDefault("azure", MockResponse{Status: http.StatusUnauthorized, Body: "invalid subscription key"})
	m.SetDefault("baidu", MockResponse{Status: http.StatusOK, Body: "ignored"})
	m.SetExpectedBaiduSecret("expected-correct-secret")

	cfg := defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini")
	cfg["popupPinned"] = true
	cfg["disableSelectionToolbar"] = true
	cfg["providers"] = []map[string]any{
		{
			"id":       "azure",
			"name":     "AzureBad",
			"kind":     "azure",
			"enabled":  true,
			"endpoint": m.URL() + "/translate",
			"region":   "eastasia",
		},
		{
			"id":       "baidu",
			"name":     "BaiduBadSign",
			"kind":     "baidu",
			"enabled":  true,
			"endpoint": m.URL() + "/api/trans/vip/translate",
			"appID":    "20260002",
		},
	}
	s.writeConfigJSON(cfg)
	beforeHash := fpFileSHA256(t, s.configPath())

	_, client, win := fpOpenMainPanel(t, s)
	fpOpenSettingsEnginesTab(t, client, win.ID)

	fpSetCardField(t, client, win.ID, 0, "订阅 Key", "wrong-azure-key")
	fpSetCardField(t, client, win.ID, 1, "密钥 Secret", "wrong-baidu-secret")

	fpPressProviderTest(t, client, win.ID, 0)
	fpWaitProviderCardPred(t, client, win.ID, 0, 6*time.Second, "Azure 401 auth error", func(nodes []Node) bool {
		return fpNodesContain(nodes, "鉴权") || fpNodesContain(nodes, "401") || fpNodesContain(nodes, "Key") || fpNodesContain(nodes, "密钥")
	})

	fpPressProviderTest(t, client, win.ID, 1)
	fpWaitProviderCardPred(t, client, win.ID, 1, 6*time.Second, "Baidu 54001 sign error", func(nodes []Node) bool {
		return fpNodesContain(nodes, "54001") || fpNodesContain(nodes, "签名") || fpNodesContain(nodes, "密钥") || fpNodesContain(nodes, "鉴权")
	})

	if afterHash := fpFileSHA256(t, s.configPath()); afterHash != beforeHash {
		t.Fatalf("config.json mutated by failed probes: before=%s after=%s", beforeHash, afterHash)
	}
}
