//go:build !windows

package casc

import (
	"runtime"
	"unsafe"
)

// libFileName is the CascLib shared-library filename we look for on this OS.
//
// CascLib's CMake produces a "casc.framework" bundle on macOS, but
// scripts/build-casclib-macos.sh extracts the inner Mach-O and re-names
// it to libcasc.dylib (with its install_name rewritten to
// @loader_path/libcasc.dylib so dlopen-by-name works once it sits next
// to the executable).
const libFileName = "libcasc.dylib"

// encodeLibPath converts a Go string to the form CascLib's
// CascOpenStorage expects on this platform. On macOS/Linux CascLib is
// compiled with TCHAR == char, so szParams is plain UTF-8 — same
// encoding as a Go string in memory.
//
// Returns a uintptr suitable for passing as a function argument, plus a
// "free" callback the caller must defer. The free function holds a
// reference to the byte buffer so the GC can't reclaim it before
// CascOpenStorage returns.
func encodeLibPath(s string) (uintptr, func(), error) {
	// NUL-terminated for C.
	b := append([]byte(s), 0)
	ptr := uintptr(unsafe.Pointer(&b[0]))
	return ptr, func() { runtime.KeepAlive(b) }, nil
}

// lastError on non-Windows returns 2 (ENOENT) unconditionally — a
// deliberate degradation. CascLib's POSIX build sets errno on failure,
// but purego.RegisterLibFunc dispatches through goroutine machinery
// that doesn't preserve errno across the foreign call, so we can't
// distinguish "file not in this CASC mount" from "real I/O error" the
// way the Windows GetLastError check does.
//
// The downstream consumer (openOne in casc.go) treats lastError() == 2
// as "not found — move on to the next mount prefix", which is the right
// behavior for the overwhelmingly common case. Genuine storage errors
// surface at CascOpenStorage time (a separate code path that just
// reports failure without an errno classification anyway), so the lost
// fidelity here is acceptable. If asset-load debugging on macOS ever
// needs real errno, swap purego.RegisterLibFunc for purego.SyscallN
// in casc.go and capture the returned err — or move that one helper
// behind a cgo file.
func lastError() uint32 {
	return 2
}
