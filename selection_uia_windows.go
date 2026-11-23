//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
	uiaTextPatternID        = int32(10014)

	comSOK             = uint32(0x00000000)
	comSFalse          = uint32(0x00000001)
	comRPCChangedMode  = uint32(0x80010106)
	comEPointerHRESULT = uintptr(0x80004003)

	// COM vtable slots verified against UIAutomationClient.h / UIAutomationClient.idl.
	vtUnknownRelease             = 2
	vtUIAGetFocusedElement       = 8
	vtUIAElemGetCurrentPatternAs = 14
	vtTextPatternGetSelection    = 5
	vtTextRangeArrayGetLength    = 3
	vtTextRangeArrayGetElement   = 4
	vtTextRangeGetText           = 12
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")

	oleaut32          = windows.NewLazySystemDLL("oleaut32.dll")
	procSysStringLen  = oleaut32.NewProc("SysStringLen")
	procSysFreeString = oleaut32.NewProc("SysFreeString")

	clsidCUIAutomation8 = windows.GUID{
		Data1: 0xe22ad333,
		Data2: 0xb25f,
		Data3: 0x460c,
		Data4: [8]byte{0x83, 0xd0, 0x05, 0x81, 0x10, 0x73, 0x95, 0xc9},
	}
	clsidCUIAutomation = windows.GUID{
		Data1: 0xff48dba4,
		Data2: 0x60ef,
		Data3: 0x4201,
		Data4: [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e},
	}
	iidIUIAutomation = windows.GUID{
		Data1: 0x30cbe57d,
		Data2: 0xd9d0,
		Data3: 0x452a,
		Data4: [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee},
	}
	iidIUIAutomationTextPattern = windows.GUID{
		Data1: 0x32eba289,
		Data2: 0x3583,
		Data3: 0x42c9,
		Data4: [8]byte{0x9c, 0x59, 0x3b, 0x6d, 0x9a, 0x1e, 0x9b, 0x6a},
	}
)

//go:uintptrescapes
func comCall(obj uintptr, slot int, args ...uintptr) uintptr {
	if obj == 0 {
		return comEPointerHRESULT
	}
	vtbl := *(*uintptr)(pointerFromUintptr(obj))
	if vtbl == 0 {
		return comEPointerHRESULT
	}
	fn := *(*uintptr)(unsafe.Add(pointerFromUintptr(vtbl), uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	var buf [8]uintptr
	callArgs := buf[:0]
	if 1+len(args) > len(buf) {
		callArgs = make([]uintptr, 0, 1+len(args))
	}
	callArgs = append(callArgs, obj)
	callArgs = append(callArgs, args...)
	r, _, _ := syscall.SyscallN(fn, callArgs...)
	return r
}

func comFailed(hr uintptr) bool {
	return int32(uint32(hr)) < 0
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, vtUnknownRelease)
	}
}

func bstrToString(bstr uintptr) string {
	if bstr == 0 {
		return ""
	}
	n, _, _ := procSysStringLen.Call(bstr)
	if n == 0 {
		return ""
	}
	utf16Slice := unsafe.Slice((*uint16)(pointerFromUintptr(bstr)), int(n))
	return string(utf16.Decode(utf16Slice))
}

func createUIAutomation() (uintptr, error) {
	var automation uintptr
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCUIAutomation8)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)),
		uintptr(unsafe.Pointer(&automation)),
	)
	if !comFailed(hr) && automation != 0 {
		return automation, nil
	}
	automation = 0
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCUIAutomation)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)),
		uintptr(unsafe.Pointer(&automation)),
	)
	if comFailed(hr) || automation == 0 {
		return 0, fmt.Errorf("CoCreateInstance(CUIAutomation) failed: 0x%08x", uint32(hr))
	}
	return automation, nil
}

// readSelectedTextUIA queries the foreground focused control via Windows UI
// Automation TextPattern.GetSelection without synthesizing keystrokes or
// modifying the clipboard.
func readSelectedTextUIA() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	switch uint32(hr) {
	case comSOK, comSFalse:
		defer procCoUninitialize.Call()
	case comRPCChangedMode:
		// Thread already belongs to another COM apartment (e.g., MTA); proceed
		// without calling CoUninitialize.
	default:
		if comFailed(hr) {
			return "", fmt.Errorf("CoInitializeEx failed: 0x%08x", uint32(hr))
		}
	}

	return readSelectedTextUIACOM()
}

func readSelectedTextUIACOM() (string, error) {
	automation, err := createUIAutomation()
	if err != nil {
		return "", err
	}
	defer comRelease(automation)

	var focused uintptr
	hr := comCall(automation, vtUIAGetFocusedElement, uintptr(unsafe.Pointer(&focused)))
	if comFailed(hr) || focused == 0 {
		return "", fmt.Errorf("IUIAutomation.GetFocusedElement failed: 0x%08x", uint32(hr))
	}
	defer comRelease(focused)

	var textPattern uintptr
	hr = comCall(
		focused,
		vtUIAElemGetCurrentPatternAs,
		uintptr(uiaTextPatternID),
		uintptr(unsafe.Pointer(&iidIUIAutomationTextPattern)),
		uintptr(unsafe.Pointer(&textPattern)),
	)
	if comFailed(hr) || textPattern == 0 {
		return "", fmt.Errorf("IUIAutomationElement.GetCurrentPatternAs(TextPattern) unavailable: 0x%08x", uint32(hr))
	}
	defer comRelease(textPattern)

	var ranges uintptr
	hr = comCall(textPattern, vtTextPatternGetSelection, uintptr(unsafe.Pointer(&ranges)))
	if comFailed(hr) || ranges == 0 {
		return "", fmt.Errorf("IUIAutomationTextPattern.GetSelection failed: 0x%08x", uint32(hr))
	}
	defer comRelease(ranges)

	var count int32
	hr = comCall(ranges, vtTextRangeArrayGetLength, uintptr(unsafe.Pointer(&count)))
	if comFailed(hr) {
		return "", fmt.Errorf("IUIAutomationTextRangeArray.get_Length failed: 0x%08x", uint32(hr))
	}
	if count <= 0 {
		return "", nil
	}

	parts := make([]string, 0, count)
	for i := int32(0); i < count; i++ {
		part, err := readUIATextRangeAt(ranges, i)
		if err != nil {
			return "", err
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func readUIATextRangeAt(ranges uintptr, index int32) (string, error) {
	var rng uintptr
	hr := comCall(ranges, vtTextRangeArrayGetElement, uintptr(index), uintptr(unsafe.Pointer(&rng)))
	if comFailed(hr) || rng == 0 {
		return "", fmt.Errorf("IUIAutomationTextRangeArray.GetElement(%d) failed: 0x%08x", index, uint32(hr))
	}
	defer comRelease(rng)

	var bstr uintptr
	maxLen := int32(-1)
	hr = comCall(rng, vtTextRangeGetText, uintptr(maxLen), uintptr(unsafe.Pointer(&bstr)))
	if bstr != 0 {
		defer procSysFreeString.Call(bstr)
	}
	if comFailed(hr) {
		return "", fmt.Errorf("IUIAutomationTextRange.GetText failed: 0x%08x", uint32(hr))
	}
	return bstrToString(bstr), nil
}
