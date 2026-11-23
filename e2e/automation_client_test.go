//go:build e2e

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type AutomationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *AutomationError) Error() string {
	return fmt.Sprintf("automation %s: %s", e.Code, e.Message)
}

type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type WindowInfo struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	URL           string `json:"url,omitempty"`
	Bounds        Rect   `json:"bounds"`
	ContentBounds Rect   `json:"contentBounds"`
	Focused       bool   `json:"focused"`
	Visible       bool   `json:"visible"`
}

type Node struct {
	ID              string   `json:"id"`
	Parent          string   `json:"parent,omitempty"`
	Role            string   `json:"role"`
	Key             any      `json:"key,omitempty"`
	Label           string   `json:"label,omitempty"`
	Value           string   `json:"value,omitempty"`
	Placeholder     string   `json:"placeholder,omitempty"`
	Description     string   `json:"description,omitempty"`
	Bounds          Rect     `json:"bounds"`
	States          []string `json:"states,omitempty"`
	Actions         []string `json:"actions,omitempty"`
	Min             *float64 `json:"min,omitempty"`
	Max             *float64 `json:"max,omitempty"`
	Now             *float64 `json:"now,omitempty"`
	Step            *float64 `json:"step,omitempty"`
	KeyShortcuts    string   `json:"keyShortcuts,omitempty"`
	Selection       *[2]int  `json:"selection,omitempty"`
	Level           int      `json:"level,omitempty"`
	PosInSet        int      `json:"posInSet,omitempty"`
	SetSize         int      `json:"setSize,omitempty"`
	RowCount        int      `json:"rowCount,omitempty"`
	ColCount        int      `json:"colCount,omitempty"`
	RowIndex        int      `json:"rowIndex,omitempty"`
	ColIndex        int      `json:"colIndex,omitempty"`
	RowSpan         int      `json:"rowSpan,omitempty"`
	ColSpan         int      `json:"colSpan,omitempty"`
	Current         string   `json:"current,omitempty"`
	AutoComplete    string   `json:"autoComplete,omitempty"`
	RoleDescription string   `json:"roleDescription,omitempty"`
	HasPopup        string   `json:"hasPopup,omitempty"`
	Controls        []string `json:"controls,omitempty"`
	DescribedBy     []string `json:"describedBy,omitempty"`
	ErrorMessage    []string `json:"errorMessage,omitempty"`
	WebView         bool     `json:"webView,omitempty"`
}

func (n Node) HasState(state string) bool {
	for _, s := range n.States {
		if s == state {
			return true
		}
	}
	return false
}

func (n Node) HasAction(action string) bool {
	for _, a := range n.Actions {
		if a == action {
			return true
		}
	}
	return false
}

type TreeResult struct {
	Window        int      `json:"window"`
	Focus         string   `json:"focus,omitempty"`
	Nodes         []Node   `json:"nodes"`
	Announcements []string `json:"announcements,omitempty"`
}

type ActParams struct {
	Window int    `json:"window,omitempty"`
	ID     string `json:"id,omitempty"`
	Key    any    `json:"key,omitempty"`
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
	From   int    `json:"from,omitempty"`
	To     int    `json:"to,omitempty"`
}

type InputEvent struct {
	Type    string   `json:"type"`
	X       float64  `json:"x,omitempty"`
	Y       float64  `json:"y,omitempty"`
	Button  string   `json:"button,omitempty"`
	Clicks  int      `json:"clicks,omitempty"`
	DX      float64  `json:"dx,omitempty"`
	DY      float64  `json:"dy,omitempty"`
	Precise bool     `json:"precise,omitempty"`
	Key     string   `json:"key,omitempty"`
	Mods    []string `json:"mods,omitempty"`
	Text    string   `json:"text,omitempty"`
}

type CaptureResult struct {
	PNG    []byte `json:"png"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type AutomationClient struct {
	conn   net.Conn
	r      *bufio.Reader
	mu     sync.Mutex
	nextID int64
}

func DialAutomation(ctx context.Context, sockPath string, procDone chan error) (*AutomationClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		if procDone != nil {
			select {
			case err := <-procDone:
				select {
				case procDone <- err:
				default:
				}
				return nil, fmt.Errorf("process exited before automation socket %s was ready: %w", sockPath, err)
			default:
			}
		}

		conn, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
		if err == nil {
			client := &AutomationClient{
				conn: conn,
				r:    bufio.NewReaderSize(conn, 64*1024),
			}
			// Calling Windows blocks on <-a.ready in mygo/plugins/automation until
			// App.WhenReady and createWindows() finish.
			if _, err := client.Windows(ctx); err != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("automation readiness handshake: %w", err)
			}
			return client, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dial automation %s: %w (last dial err: %v)", sockPath, ctx.Err(), lastErr)
		case err := <-procDone:
			select {
			case procDone <- err:
			default:
			}
			return nil, fmt.Errorf("process exited before automation socket %s was ready: %w", sockPath, err)
		case <-ticker.C:
		}
	}
}

func (c *AutomationClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *AutomationClient) Call(ctx context.Context, method string, params any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	req := struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{
		ID:     c.nextID,
		Method: method,
		Params: params,
	}
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", method, err)
	}
	line = append(line, '\n')

	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(10 * time.Second)
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return err
	}
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

	if _, err := c.conn.Write(line); err != nil {
		return fmt.Errorf("write %s: %w", method, err)
	}
	respLine, err := c.r.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read %s response: %w", method, err)
	}

	var resp struct {
		ID     json.RawMessage  `json:"id"`
		Result json.RawMessage  `json:"result"`
		Error  *AutomationError `json:"error"`
	}
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return fmt.Errorf("unmarshal %s response: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

func (c *AutomationClient) Windows(ctx context.Context) ([]WindowInfo, error) {
	var res struct {
		Windows []WindowInfo `json:"windows"`
	}
	if err := c.Call(ctx, "windows", nil, &res); err != nil {
		return nil, err
	}
	return res.Windows, nil
}

func (c *AutomationClient) Tree(ctx context.Context, window int) (TreeResult, error) {
	var params any
	if window != 0 {
		params = struct {
			Window int `json:"window"`
		}{Window: window}
	}
	var res TreeResult
	if err := c.Call(ctx, "tree", params, &res); err != nil {
		return TreeResult{}, err
	}
	return res, nil
}

func (c *AutomationClient) Act(ctx context.Context, p ActParams) error {
	return c.Call(ctx, "act", p, nil)
}

func (c *AutomationClient) Input(ctx context.Context, window int, events ...InputEvent) error {
	params := struct {
		Window int          `json:"window,omitempty"`
		Events []InputEvent `json:"events"`
	}{
		Window: window,
		Events: events,
	}
	return c.Call(ctx, "input", params, nil)
}

func (c *AutomationClient) Capture(ctx context.Context, window int) (CaptureResult, error) {
	var params any
	if window != 0 {
		params = struct {
			Window int `json:"window"`
		}{Window: window}
	}
	var res CaptureResult
	if err := c.Call(ctx, "capture", params, &res); err != nil {
		return CaptureResult{}, err
	}
	return res, nil
}

func (c *AutomationClient) Idle(ctx context.Context, window int, timeout time.Duration) error {
	params := struct {
		Window  int `json:"window,omitempty"`
		Timeout int `json:"timeout,omitempty"`
	}{
		Window:  window,
		Timeout: int(timeout.Milliseconds()),
	}
	return c.Call(ctx, "idle", params, nil)
}

func (c *AutomationClient) WaitIdle(ctx context.Context, window int) error {
	timeout := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem > 0 {
			timeout = rem
		}
	}
	return c.Idle(ctx, window, timeout)
}

func (c *AutomationClient) WaitWindow(ctx context.Context, pred func(WindowInfo) bool) (WindowInfo, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	var lastWindows []WindowInfo
	var lastErr error
	for {
		wins, err := c.Windows(ctx)
		if err == nil {
			lastWindows = wins
			for _, w := range wins {
				if pred(w) {
					return w, nil
				}
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return WindowInfo{}, fmt.Errorf("WaitWindow timed out (%w); lastErr=%v, windows=%+v", ctx.Err(), lastErr, lastWindows)
		case <-ticker.C:
		}
	}
}

func (c *AutomationClient) WaitTree(ctx context.Context, window int, pred func(TreeResult) bool) (TreeResult, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	var lastTree TreeResult
	var lastErr error
	for {
		// Best-effort short idle wait to settle pending frames.
		idleCtx, cancelIdle := context.WithTimeout(ctx, 200*time.Millisecond)
		_ = c.Idle(idleCtx, window, 150*time.Millisecond)
		cancelIdle()

		tree, err := c.Tree(ctx, window)
		if err == nil {
			lastTree = tree
			if pred(tree) {
				return tree, nil
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return lastTree, fmt.Errorf("WaitTree timed out (%w); lastErr=%v\n%s",
				ctx.Err(), lastErr, formatTreeSummary(lastTree))
		case <-ticker.C:
		}
	}
}

func (c *AutomationClient) WaitNode(ctx context.Context, window int, pred func(Node) bool) (Node, TreeResult, error) {
	var matched Node
	tree, err := c.WaitTree(ctx, window, func(tr TreeResult) bool {
		for _, n := range tr.Nodes {
			if pred(n) {
				matched = n
				return true
			}
		}
		return false
	})
	return matched, tree, err
}

func (c *AutomationClient) WaitNodeByKey(ctx context.Context, window int, key any) (Node, TreeResult, error) {
	return c.WaitNode(ctx, window, func(n Node) bool {
		return keyEquals(n.Key, key)
	})
}

func FindNodeByKey(tree TreeResult, key any) (Node, bool) {
	for _, n := range tree.Nodes {
		if keyEquals(n.Key, key) {
			return n, true
		}
	}
	return Node{}, false
}

func FindNodes(tree TreeResult, pred func(Node) bool) []Node {
	var out []Node
	for _, n := range tree.Nodes {
		if pred(n) {
			out = append(out, n)
		}
	}
	return out
}

func keyEquals(nodeKey any, wantKey any) bool {
	if nodeKey == nil || wantKey == nil {
		return nodeKey == wantKey
	}
	if a, ok := nodeKey.(string); ok {
		if b, ok := wantKey.(string); ok {
			return a == b
		}
	}
	if af, aok := asFloat64(nodeKey); aok {
		if bf, bok := asFloat64(wantKey); bok {
			return af == bf
		}
	}
	return fmt.Sprint(nodeKey) == fmt.Sprint(wantKey)
}

func asFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	case int32:
		return float64(x), true
	case uint32:
		return float64(x), true
	default:
		return 0, false
	}
}

func formatTreeSummary(tr TreeResult) string {
	if len(tr.Nodes) == 0 {
		return "tree: <empty>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "tree(window=%d, focus=%q, nodes=%d):", tr.Window, tr.Focus, len(tr.Nodes))
	limit := min(len(tr.Nodes), 40)
	for i := 0; i < limit; i++ {
		n := tr.Nodes[i]
		fmt.Fprintf(&b, "\n  [%s] role=%s key=%v label=%q val=%q states=%v",
			n.ID, n.Role, n.Key, n.Label, n.Value, n.States)
	}
	if len(tr.Nodes) > limit {
		fmt.Fprintf(&b, "\n  ... (%d more nodes)", len(tr.Nodes)-limit)
	}
	return b.String()
}

func TestHarness_AutomationClient(t *testing.T) {
	// 1. Verify ActParams and InputEvent omit zero/empty fields cleanly.
	actBytes, err := json.Marshal(ActParams{Key: "btn-test", Action: "press"})
	if err != nil {
		t.Fatalf("marshal ActParams: %v", err)
	}
	if strings.Contains(string(actBytes), `"id"`) || strings.Contains(string(actBytes), `"window"`) {
		t.Fatalf("ActParams did not omit empty id/window: %s", string(actBytes))
	}

	evBytes, err := json.Marshal(InputEvent{Type: "text", Text: "hello"})
	if err != nil {
		t.Fatalf("marshal InputEvent: %v", err)
	}
	if strings.Contains(string(evBytes), `"clicks"`) || strings.Contains(string(evBytes), `"button"`) {
		t.Fatalf("InputEvent did not omit zero fields: %s", string(evBytes))
	}

	if !keyEquals(float64(2), 2) || !keyEquals("panel", "panel") || keyEquals("a", "b") {
		t.Fatalf("keyEquals assertions failed")
	}

	// 2. Live handshake against hayami --silent when a display is available.
	s := newSandbox(t)
	s.requireDisplay()
	m := newMockLLMServer(t)
	s.writeConfigJSON(defaultLoopbackConfig(m.V1URL(), "gpt-4o-mini"))

	proc := s.startGUI("--silent")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := DialAutomation(ctx, proc.SocketPath, proc.Done)
	if err != nil {
		t.Fatalf("DialAutomation: %v", err)
	}
	defer client.Close()

	win, err := client.WaitWindow(ctx, func(w WindowInfo) bool {
		return strings.Contains(w.Title, "Hayami")
	})
	if err != nil {
		t.Fatalf("WaitWindow: %v", err)
	}
	if err := client.WaitIdle(ctx, win.ID); err != nil {
		t.Fatalf("WaitIdle: %v", err)
	}
}
