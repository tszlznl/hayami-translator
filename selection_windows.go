//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"golang.org/x/sys/windows"
)

var errClipboardSnapshot = errors.New("无法保存当前剪贴板，已取消划词翻译")

const (
	vkShift   = 0x10
	vkControl = 0x11
	vkMenu    = 0x12
	vkLWin    = 0x5B
	vkRWin    = 0x5C
	vkS       = 0x53
	vkC       = 0x43

	keyeventfKeyup = 0x0002
)

type keybdInput struct {
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	extra uintptr
}

// winInput is the Windows INPUT structure for a keyboard event (40 bytes on
// 64-bit Windows): a type, four bytes of padding, the union, padded to MOUSEINPUT.
type winInput struct {
	typ uint32
	_   uint32
	ki  keybdInput
	_   [8]byte
}

// Fail compilation immediately if built for a non-64-bit pointer model where
// the Win32 INPUT union layout differs.
const (
	_ = uint(unsafe.Sizeof(winInput{}) - 40)
	_ = uint(40 - unsafe.Sizeof(winInput{}))
	_ = uint(unsafe.Sizeof(uintptr(0)) - 8)
)

var (
	user32                         = windows.NewLazySystemDLL("user32.dll")
	procSendInput                  = user32.NewProc("SendInput")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")

	// selectionReadMu serializes clipboard reads: concurrent calls would
	// save each other's in-progress clipboard state and restore it later.
	selectionReadMu sync.Mutex
)

func clipboardSeq() uint32 {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(r)
}

func sendKey(vk uint16, up bool) bool {
	flags := uint32(0)
	if up {
		flags = keyeventfKeyup
	}
	in := winInput{typ: 1, ki: keybdInput{vk: vk, flags: flags}}
	r, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	return r == 1
}

// releaseModifiers lets go of the keys of the shortcut that just fired, so
// that Ctrl+C is not sent with Alt or Shift still down.
func releaseModifiers() {
	for _, vk := range []uint16{vkMenu, vkShift, vkControl, vkLWin, vkRWin} {
		sendKey(vk, true)
	}
	time.Sleep(40 * time.Millisecond)
}

func sendCtrlC() bool {
	defer sendKey(vkControl, true)
	if !sendKey(vkControl, false) {
		return false
	}
	time.Sleep(10 * time.Millisecond)
	sendKey(vkC, false)
	time.Sleep(10 * time.Millisecond)
	sendKey(vkC, true)
	return true
}

// readSelectedText reads the foreground window's selection via a two-tier
// strategy: first querying UI Automation TextPattern.GetSelection (zero key
// injection, zero clipboard mutation), and falling back to Ctrl+C + lossless
// clipboard restore only when UIA is unavailable or returns empty text.
// It must run off the main thread: mygo's clipboard calls marshal to it.
func readSelectedText() (string, error) {
	return readSelectedTextTwoTier(readSelectedTextUIA, readClipboardFallback)
}

func readClipboardFallback() (string, error) {
	selectionReadMu.Lock()
	defer selectionReadMu.Unlock()
	return readSelectedTextWith(saveClipboard, copySelectedText)
}

// readSelectedTextWith refuses to touch the clipboard unless the previous
// contents can be restored afterwards.
func readSelectedTextWith(save func() (transfer.Data, bool), copy func(transfer.Data) (string, error)) (string, error) {
	saved, restorable := save()
	if !restorable {
		return "", errClipboardSnapshot
	}
	return copy(saved)
}

func copySelectedText(saved transfer.Data) (string, error) {
	copied := false
	defer func() {
		if !copied {
			return
		}
		if err := restoreClipboard(saved); err != nil {
			log.Printf("恢复剪贴板失败: %v", err)
		}
	}()

	before := clipboardSeq()

	releaseModifiers()
	if !sendCtrlC() {
		return "", fmt.Errorf("无法发送 Ctrl+C（目标窗口可能以管理员权限运行）")
	}

	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		if clipboardSeq() != before {
			copied = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !copied {
		return "", nil
	}
	time.Sleep(30 * time.Millisecond)

	return strings.TrimSpace(mygo.Clipboard.ReadText()), nil
}

// saveClipboard snapshots the clipboard for restoreClipboard. A clipboard
// that offers one unreadable format, such as the EnterpriseDataProtectionId
// Explorer writes, fails a whole Clipboard.Read. Skip only formats known to
// be safe to omit; an unreadable unknown format means the rest could not be
// restored faithfully, so selection translation aborts instead.
func saveClipboard() (transfer.Data, bool) {
	d, err := mygo.Clipboard.Read()
	if err == nil {
		return d, true
	}
	var reps []transfer.Representation
	for _, f := range mygo.Clipboard.Formats() {
		b, readErr := mygo.Clipboard.ReadFormat(f)
		if readErr != nil {
			if canDropClipboardFormat(f) {
				continue
			}
			log.Printf("保存剪贴板格式 %s 失败，已取消划词: %v", f, readErr)
			return transfer.Data{}, false
		}
		reps = append(reps, transfer.Bytes(f, b))
	}
	if len(reps) == 0 {
		log.Printf("保存剪贴板失败，已取消划词: %v", err)
		return transfer.Data{}, false
	}
	return transfer.New(transfer.NewItem(reps...)), true
}

func canDropClipboardFormat(f transfer.Format) bool {
	switch strings.ToLower(string(f)) {
	case "enterprisedataprotection", "enterprisedataprotectionid":
		return true
	default:
		return false
	}
}

func restoreClipboard(saved transfer.Data) error {
	if len(saved.Items()) == 0 {
		return mygo.Clipboard.Write(transfer.Data{})
	}
	return mygo.Clipboard.Write(saved)
}

// waitClipboardImage waits until the clipboard changes and holds an image.
func waitClipboardImage(ctx context.Context, before uint32, timeout time.Duration) []byte {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx != nil && ctx.Err() != nil {
			return nil
		}
		if clipboardSeq() != before {
			if img := mygo.Clipboard.ReadImage(); len(img) > 0 {
				return img
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil
}

// startSnipping opens the system snipping overlay with Win+Shift+S.
func startSnipping() {
	releaseModifiers()
	for _, vk := range []uint16{vkLWin, vkShift} {
		sendKey(vk, false)
	}
	sendKey(vkS, false)
	sendKey(vkS, true)
	sendKey(vkShift, true)
	sendKey(vkLWin, true)
}
