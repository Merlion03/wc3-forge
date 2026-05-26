//go:build windows

package casc

import (
	"runtime"
	"syscall"
	"unsafe"
)

// libFileName is the CascLib shared-library filename we look for on this OS.
const libFileName = "CascLib.dll"

// encodeLibPath converts a Go string to the form CascLib's
// CascOpenStorage expects on this platform. The DLL is compiled with
// _UNICODE so szParams is LPCTSTR == const wchar_t* (UTF-16LE).
//
// Returns a uintptr suitable for passing as a function argument, plus a
// "free" callback the caller must defer. The free function holds a
// reference to the UTF-16 buffer so the GC can't reclaim it before
// CascOpenStorage returns.
func encodeLibPath(s string) (uintptr, func(), error) {
	wide, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, func() {}, err
	}
	ptr := uintptr(unsafe.Pointer(&wide[0]))
	// Keep `wide` reachable until free() runs.
	return ptr, func() { runtime.KeepAlive(wide) }, nil
}

// lastError returns CascLib's reported error from the most recent failing
// call. CascLib uses Win32 SetLastError on Windows.
func lastError() uint32 {
	if e := syscall.GetLastError(); e != nil {
		if errno, ok := e.(syscall.Errno); ok {
			return uint32(errno)
		}
	}
	return 0
}
