//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows"
)

const (
	whMouseLL = 14

	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmRButtonDown = 0x0204
	wmMButtonDown = 0x0207
	wmMouseWheel  = 0x020A
	wmQuit        = 0x0012

	gaRoot = 2
)

type mousePoint struct {
	X int32
	Y int32
}

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

// msllHookStruct is MSLLHOOKSTRUCT from WinUser.h.
type msllHookStruct struct {
	Pt          mousePoint
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// winMSG is MSG. It is only used to pump the hook thread's message queue.
type winMSG struct {
	HWnd     uintptr
	Message  uint32
	_        uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       mousePoint
	LPrivate uint32
}

type mouseEvent struct {
	message uintptr
	pt      mousePoint
}

type hookStart struct {
	threadID uint32
	err      error
}

type dragStart struct {
	pt   mousePoint
	at   time.Time
	root uintptr
}

var (
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procGetAncestor         = user32.NewProc("GetAncestor")
	procGetParent           = user32.NewProc("GetParent")
	procGetClassNameW       = user32.NewProc("GetClassNameW")
	procGetWindowTextW      = user32.NewProc("GetWindowTextW")
	procWindowFromPoint     = user32.NewProc("WindowFromPoint")
	procGetClientRect       = user32.NewProc("GetClientRect")
	procClientToScreen      = user32.NewProc("ClientToScreen")
	procGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procPeekMessageW        = user32.NewProc("PeekMessageW")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
)

var (
	hookMu       sync.Mutex
	hookEvents   chan mouseEvent
	hookThreadID uint32
	hookDoneCh   chan struct{}

	hookProc uintptr

	captureMu sync.Mutex
	dragSeq   atomic.Uint64
)

// startSelectionHook installs one WH_MOUSE_LL hook. The hook stays installed
// while the app runs; the settings checkbox only flips selectionToolbarEnabled,
// which keeps starting and stopping the hook out of the settings path.
func startSelectionHook(a *app) error {
	hookMu.Lock()
	if hookEvents != nil {
		hookMu.Unlock()
		return nil
	}
	events := make(chan mouseEvent, 64)
	ready := make(chan hookStart, 1)
	done := make(chan struct{})
	hookEvents = events
	hookDoneCh = done
	hookMu.Unlock()

	go runMouseHook(events, ready, done)
	st := <-ready
	if st.err != nil {
		hookMu.Lock()
		hookEvents = nil
		hookDoneCh = nil
		hookMu.Unlock()
		return st.err
	}

	hookMu.Lock()
	hookThreadID = st.threadID
	hookMu.Unlock()
	go processMouseEvents(a, events)
	return nil
}

func stopSelectionHook() {
	hookMu.Lock()
	tid := hookThreadID
	events := hookEvents
	done := hookDoneCh
	hookThreadID = 0
	hookEvents = nil
	hookDoneCh = nil
	hookMu.Unlock()
	if events == nil {
		return
	}
	if tid != 0 {
		procPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
	}
	if done == nil {
		return
	}
	select {
	case <-done:
		close(events)
	case <-time.After(2 * time.Second):
	}
}

func runMouseHook(events chan<- mouseEvent, ready chan<- hookStart, done chan<- struct{}) {
	runtime.LockOSThread()
	defer close(done)

	hookProc = windows.NewCallback(func(nCode, wParam, lParam uintptr) uintptr {
		if int32(nCode) >= 0 {
			switch uint32(wParam) {
			case wmLButtonDown, wmLButtonUp, wmRButtonDown, wmMButtonDown, wmMouseWheel:
				info := (*msllHookStruct)(pointerFromUintptr(lParam))
				select {
				case events <- mouseEvent{message: wParam, pt: info.Pt}:
				default:
				}
			}
		}
		r, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
		return r
	})

	hmod, _, _ := procGetModuleHandleW.Call(0)
	h, _, callErr := procSetWindowsHookExW.Call(whMouseLL, hookProc, hmod, 0)
	if h == 0 {
		ready <- hookStart{err: fmt.Errorf("SetWindowsHookExW: %v", callErr)}
		return
	}

	// Create the message queue before announcing the thread id, so an early
	// stop can post WM_QUIT safely.
	var msg winMSG
	procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 0)
	ready <- hookStart{threadID: windows.GetCurrentThreadId()}

	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
	}
	procUnhookWindowsHookEx.Call(h)
}

func processMouseEvents(a *app, events <-chan mouseEvent) {
	var down *dragStart
	for ev := range events {
		switch uint32(ev.message) {
		case wmLButtonDown:
			// Any new press invalidates a read that is still in flight.
			dragSeq.Add(1)
			down = nil
			if pointInToolbar(ev.pt) {
				// The press has to reach the button; hiding the panel now
				// would cancel the click.
				continue
			}
			a.hideSelectionToolbar()
			if pointInOurApp(ev.pt) || !selectionToolbarEnabled.Load() {
				continue
			}
			root := rootWindowAt(ev.pt)
			if root == 0 {
				continue
			}
			down = &dragStart{pt: ev.pt, at: time.Now(), root: root}

		case wmLButtonUp:
			start := down
			down = nil
			if start == nil || !selectionToolbarEnabled.Load() {
				continue
			}
			if time.Since(start.at) < 40*time.Millisecond || !dragged(start.pt, ev.pt) {
				continue
			}
			if !clientAreaAt(start.pt) || !sameRootAt(start.root, ev.pt) {
				continue
			}
			if pointInOurApp(ev.pt) || foregroundIsOurs() || ctrlOrAltDown() {
				continue
			}
			if blockedWindowAt(start.pt) {
				continue
			}
			hwnd := foregroundWindow()
			if hwnd == 0 {
				continue
			}
			gen := dragSeq.Load()
			p := mygo.Screen.CursorScreenPoint()
			go captureToolbarSelection(a, gen, p, hwnd)

		case wmRButtonDown, wmMButtonDown, wmMouseWheel:
			dragSeq.Add(1)
			down = nil
			a.hideSelectionToolbar()
		}
	}
}

// captureToolbarSelection runs off the hook thread. Reading the selection
// takes a few hundred milliseconds, so every check is repeated after it: a
// new click or a foreground change cancels this particular toolbar.
func captureToolbarSelection(a *app, gen uint64, p mygo.Point, hwnd uintptr) {
	time.Sleep(140 * time.Millisecond)
	if !selectionToolbarEnabled.Load() || dragSeq.Load() != gen {
		return
	}
	if foregroundWindow() != hwnd {
		return
	}
	captureMu.Lock()
	defer captureMu.Unlock()
	if !selectionToolbarEnabled.Load() || dragSeq.Load() != gen {
		return
	}
	text, err := readSelectedText()
	if err != nil || strings.TrimSpace(text) == "" {
		return
	}
	if !selectionToolbarEnabled.Load() || dragSeq.Load() != gen {
		return
	}
	a.showSelectionToolbar(text, p)
}

// pointerFromUintptr turns the pointer the hook callback received back into
// an unsafe.Pointer. lParam is valid for the duration of the callback.
func pointerFromUintptr(v uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&v))
}

func dragged(a, b mousePoint) bool {
	dx, dy := a.X-b.X, a.Y-b.Y
	return dx >= 8 || dx <= -8 || dy >= 8 || dy <= -8
}

func sameRootAt(root uintptr, pt mousePoint) bool {
	return root != 0 && rootWindowAt(pt) == root
}

func rootWindowAt(pt mousePoint) uintptr {
	h := windowFromPoint(pt)
	if h == 0 {
		return 0
	}
	r, _, _ := procGetAncestor.Call(h, gaRoot)
	if r == 0 {
		return h
	}
	return r
}

// pointInToolbar identifies the panel by its window title. Comparing window
// bounds would mix DIPs from mygo with the physical points from the hook.
func pointInToolbar(pt mousePoint) bool {
	h := windowFromPoint(pt)
	if h == 0 || windowProcessID(h) != uint32(os.Getpid()) {
		return false
	}
	root, _, _ := procGetAncestor.Call(h, gaRoot)
	if root == 0 {
		root = h
	}
	var buf [64]uint16
	n, _, _ := procGetWindowTextW.Call(root, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return n > 0 && windows.UTF16ToString(buf[:int(n)]) == "划词助手"
}

func pointInOurApp(pt mousePoint) bool {
	return windowProcessID(windowFromPoint(pt)) == uint32(os.Getpid())
}

func foregroundIsOurs() bool {
	return windowProcessID(foregroundWindow()) == uint32(os.Getpid())
}

func foregroundWindow() uintptr {
	return uintptr(windows.GetForegroundWindow())
}

func windowProcessID(hwnd uintptr) uint32 {
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	windows.GetWindowThreadProcessId(windows.HWND(hwnd), &pid)
	return pid
}

func ctrlOrAltDown() bool {
	for _, vk := range []uint16{vkControl, vkMenu, vkLWin, vkRWin} {
		r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
		if r&0x8000 != 0 {
			return true
		}
	}
	return false
}

// clientAreaAt checks the point against the client rectangle of the window
// under the press. A title bar, a border and a classic scrollbar live outside
// it, so dragging those does not send Ctrl+C into the application.
func clientAreaAt(pt mousePoint) bool {
	h := windowFromPoint(pt)
	if h == 0 {
		return false
	}
	var rc winRect
	r, _, _ := procGetClientRect.Call(h, uintptr(unsafe.Pointer(&rc)))
	if r == 0 {
		return false
	}
	origin := mousePoint{}
	r, _, _ = procClientToScreen.Call(h, uintptr(unsafe.Pointer(&origin)))
	if r == 0 {
		return false
	}
	right := origin.X + rc.Right - rc.Left
	bottom := origin.Y + rc.Bottom - rc.Top
	return pt.X >= origin.X && pt.X < right && pt.Y >= origin.Y && pt.Y < bottom
}

func blockedWindowAt(pt mousePoint) bool {
	h := windowFromPoint(pt)
	for i := 0; h != 0 && i < 8; i++ {
		var buf [256]uint16
		n, _, _ := procGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 {
			class := strings.ToLower(windows.UTF16ToString(buf[:int(n)]))
			if blockedWindowClass(class) {
				return true
			}
		}
		parent, _, _ := procGetParent.Call(h)
		h = parent
	}
	return false
}

// blockedWindowClass lists windows where a drag is not a text selection, or
// where Ctrl+C has a destructive meaning. Terminals are the important case:
// sending Ctrl+C to one interrupts the running command.
func blockedWindowClass(class string) bool {
	switch class {
	case "consolewindowclass", "cascadia_hosting_window_class",
		"virtualconsoleclass", "mintty", "putty",
		"syslistview32", "systreeview32", "listbox",
		"shell_traywnd", "shell_secondarytraywnd", "mstasklistwclass",
		"progman", "workerw", "scrollbar", "toolbarwindow32":
		return true
	}
	return false
}

func windowFromPoint(pt mousePoint) uintptr {
	h, _, _ := procWindowFromPoint.Call(packPoint(pt))
	return h
}

// packPoint passes a POINT by value to WindowFromPoint and WM_NCHITTEST.
// POINT is two 32-bit fields in one 64-bit register on windows/amd64.
func packPoint(pt mousePoint) uintptr {
	return uintptr(uint32(pt.X)) | uintptr(uint32(pt.Y))<<32
}
