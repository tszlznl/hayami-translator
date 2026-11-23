//go:build windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// SpeechVoiceSpeakFlags from sapi.h:
	// SVSFlagsAsync (1) | SVSFPurgeBeforeSpeak (2) = 3.
	SVSFlagsAsync        = 1
	SVSFPurgeBeforeSpeak = 2

	// SpeechRunState from sapi.h: SRSEDone = 1, SRSIsSpeaking = 2.
	SRSEDone      = 1
	SRSIsSpeaking = 2

	dispatchMethod      = 0x1
	dispatchPropertyGet = 0x2

	vtEmpty = 0
	vtI4    = 3
	vtBSTR  = 8

	vtIDispatchGetIDsOfNames = 5
	vtIDispatchInvoke        = 6
)

var (
	procCLSIDFromProgID = ole32.NewProc("CLSIDFromProgID")
	procSysAllocString  = oleaut32.NewProc("SysAllocString")

	iidIDispatch = windows.GUID{
		Data1: 0x00020400,
		Data2: 0x0000,
		Data3: 0x0000,
		Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
	}
	iidNull = windows.GUID{}
)

// comVariant is the 24-byte Windows VARIANT layout on 64-bit Windows.
type comVariant struct {
	vt         uint16
	wReserved1 uint16
	wReserved2 uint16
	wReserved3 uint16
	val1       uintptr
	val2       uintptr
}

type comDispParams struct {
	rgvarg            uintptr
	rgdispidNamedArgs uintptr
	cArgs             uint32
	cNamedArgs        uint32
}

const (
	_ = uint(unsafe.Sizeof(comVariant{}) - 24)
	_ = uint(24 - unsafe.Sizeof(comVariant{}))
)

type sapiController struct {
	mu sync.Mutex
}

var winSAPI sapiController

func allocBSTR(s string) (uintptr, error) {
	u16, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return 0, err
	}
	bstr, _, _ := procSysAllocString.Call(uintptr(unsafe.Pointer(u16)))
	if bstr == 0 {
		return 0, fmt.Errorf("SysAllocString failed")
	}
	return bstr, nil
}

func dispGetID(disp uintptr, name string) (int32, error) {
	u16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	names := [1]*uint16{u16}
	var dispid int32
	hr := comCall(
		disp,
		vtIDispatchGetIDsOfNames,
		uintptr(unsafe.Pointer(&iidNull)),
		uintptr(unsafe.Pointer(&names[0])),
		1,
		0x0400, // LOCALE_USER_DEFAULT
		uintptr(unsafe.Pointer(&dispid)),
	)
	if comFailed(hr) {
		return 0, fmt.Errorf("IDispatch.GetIDsOfNames(%s) failed: 0x%08x", name, uint32(hr))
	}
	return dispid, nil
}

func createSAPIVoice() (uintptr, error) {
	progID, err := windows.UTF16PtrFromString("SAPI.SpVoice")
	if err != nil {
		return 0, err
	}
	var clsid windows.GUID
	hr, _, _ := procCLSIDFromProgID.Call(
		uintptr(unsafe.Pointer(progID)),
		uintptr(unsafe.Pointer(&clsid)),
	)
	if comFailed(hr) {
		return 0, fmt.Errorf("CLSIDFromProgID(SAPI.SpVoice) failed: 0x%08x", uint32(hr))
	}
	var disp uintptr
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsid)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIDispatch)),
		uintptr(unsafe.Pointer(&disp)),
	)
	if comFailed(hr) || disp == 0 {
		return 0, fmt.Errorf("CoCreateInstance(SAPI.SpVoice) failed: 0x%08x", uint32(hr))
	}
	return disp, nil
}

// invokeSAPISpeak invokes SpVoice.Speak(text, flags) via IDispatch::Invoke.
// Note that IDispatch arguments in rgvarg are ordered in reverse (right-to-left).
func invokeSAPISpeak(disp uintptr, speakID int32, text string, flags uint32) error {
	bstr, err := allocBSTR(text)
	if err != nil {
		return err
	}
	defer procSysFreeString.Call(bstr)

	args := [2]comVariant{
		{vt: vtI4, val1: uintptr(flags)}, // 2nd parameter: Flags
		{vt: vtBSTR, val1: bstr},         // 1st parameter: Text
	}
	params := comDispParams{
		rgvarg: uintptr(unsafe.Pointer(&args[0])),
		cArgs:  2,
	}
	var res comVariant
	hr := comCall(
		disp,
		vtIDispatchInvoke,
		uintptr(uint32(speakID)),
		uintptr(unsafe.Pointer(&iidNull)),
		0x0400,
		dispatchMethod,
		uintptr(unsafe.Pointer(&params)),
		uintptr(unsafe.Pointer(&res)),
		0,
		0,
	)
	if comFailed(hr) {
		return fmt.Errorf("SpVoice.Speak failed: 0x%08x", uint32(hr))
	}
	return nil
}

// invokeSAPIWaitUntilDone calls SpVoice.WaitUntilDone(timeoutMs) and returns
// true when speech has completed.
func invokeSAPIWaitUntilDone(disp uintptr, waitID int32, timeoutMs int32) bool {
	arg := comVariant{vt: vtI4, val1: uintptr(uint32(timeoutMs))}
	params := comDispParams{
		rgvarg: uintptr(unsafe.Pointer(&arg)),
		cArgs:  1,
	}
	var res comVariant
	hr := comCall(
		disp,
		vtIDispatchInvoke,
		uintptr(uint32(waitID)),
		uintptr(unsafe.Pointer(&iidNull)),
		0x0400,
		dispatchMethod,
		uintptr(unsafe.Pointer(&params)),
		uintptr(unsafe.Pointer(&res)),
		0,
		0,
	)
	if comFailed(hr) {
		return true
	}
	// VARIANT_BOOL: 0 is false (timed out, still speaking), non-zero is true (done).
	return int16(res.val1) != 0
}

// speakPlatformSystem speaks text in-process via Windows SAPI.SpVoice COM
// with SVSFlagsAsync | SVSFPurgeBeforeSpeak for instant async cancellation.
func speakPlatformSystem(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	switch uint32(hr) {
	case comSOK, comSFalse:
		defer procCoUninitialize.Call()
	case comRPCChangedMode:
	default:
		if comFailed(hr) {
			return fmt.Errorf("CoInitializeEx(SAPI) failed: 0x%08x", uint32(hr))
		}
	}

	disp, err := createSAPIVoice()
	if err != nil {
		return err
	}
	defer comRelease(disp)

	speakID, err := dispGetID(disp, "Speak")
	if err != nil {
		return err
	}
	waitID, waitErr := dispGetID(disp, "WaitUntilDone")

	const asyncPurgeFlags = uint32(SVSFlagsAsync | SVSFPurgeBeforeSpeak)
	if err := invokeSAPISpeak(disp, speakID, text, asyncPurgeFlags); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			// Immediately purge and stop active SAPI output on cancellation.
			_ = invokeSAPISpeak(disp, speakID, "", asyncPurgeFlags)
			return ctx.Err()
		default:
		}
		if waitErr == nil {
			if invokeSAPIWaitUntilDone(disp, waitID, 40) {
				return nil
			}
		} else {
			time.Sleep(40 * time.Millisecond)
			return nil
		}
	}
}

// Stop immediately purges any active SAPI speech stream.
func (s *sapiController) Stop() {
	stopSpeaking()
}

func hideCommandWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
