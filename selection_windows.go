//go:build windows

package main

import (
	"context"
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
// amd64): a type, four bytes of padding, the union, padded to MOUSEINPUT.
type winInput struct {
	typ uint32
	_   uint32
	ki  keybdInput
	_   [8]byte
}

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

// readSelectedText copies the selection of the foreground window by sending
// Ctrl+C and reading the clipboard. The previous clipboard contents are
// restored afterwards. It detects the copy through the clipboard sequence
// number, so it never writes a probe that clipboard history would record.
// It must run off the main thread: mygo's clipboard calls marshal to it.
func readSelectedText() (string, error) {
	selectionReadMu.Lock()
	defer selectionReadMu.Unlock()

	saved, saveErr := mygo.Clipboard.Read()
	if saveErr != nil {
		return "", fmt.Errorf("读取当前剪贴板失败：%w", saveErr)
	}
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
