// Package casc reads files from a local Warcraft III install's CASC storage.
//
// The Blizzard CASC format is non-trivial — multi-stage indirection from
// game-relative paths → content keys → encoding keys → archive offsets,
// plus the BLTE block-level encoding inside each archive. Rather than
// porting all of it to Go (a 2-3k LOC project), we call Ladislav Zezula's
// battle-tested CascLib via Windows' LoadLibrary, the same trick HiveWE's
// scripts use for StormLib. No cgo, no toolchain — just the DLL next to
// the binary.
//
// CascLib.dll must be present in the executable's directory (or anywhere
// on the DLL search path). The official vcpkg-built copy lives in this
// project's scripts/casclib/ for convenience.
//
// Thread-safety: a single Storage's operations are serialised by a mutex.
// CascLib's storage handle is shared across reads internally; we keep
// concurrent callers single-file rather than reason about its internals.
package casc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// Tiny indirection so locateDLL stays readable without importing
// "os"+"path/filepath" in three places.
var (
	osExecutable = os.Executable
	osGetwd      = os.Getwd
	osStat       = os.Stat
	filepathDir  = filepath.Dir
)

var (
	dllOnce          sync.Once
	dllErr           error
	dll              *syscall.LazyDLL
	procOpenStorage  *syscall.LazyProc
	procCloseStorage *syscall.LazyProc
	procOpenFile     *syscall.LazyProc
	procCloseFile    *syscall.LazyProc
	procReadFile     *syscall.LazyProc
	procGetFileSize  *syscall.LazyProc
)

// DLLPath, if non-empty, overrides the auto-locate. Set this before any
// Open call to point at a specific CascLib.dll (e.g. for tests).
var DLLPath string

func loadDLL() {
	dllPath := DLLPath
	if dllPath == "" {
		dllPath = locateDLL()
	}
	dll = syscall.NewLazyDLL(dllPath)
	procOpenStorage = dll.NewProc("CascOpenStorage")
	procCloseStorage = dll.NewProc("CascCloseStorage")
	// CascLib was built with _UNICODE on Windows. The lowercase symbol
	// CascOpenFile is the ASCII variant (CascOpenFileW would be wide).
	// File names within CASC are conventionally lowercase ASCII so we use
	// the narrow flavour to avoid UTF-16 conversion per call.
	procOpenFile = dll.NewProc("CascOpenFile")
	procCloseFile = dll.NewProc("CascCloseFile")
	procReadFile = dll.NewProc("CascReadFile")
	// Use the 64-bit size variant; the 32-bit CascGetFileSize is the
	// legacy entry point and on this vcpkg build it returns
	// INVALID_FILE_SIZE for valid files (no Win32 error set).
	procGetFileSize = dll.NewProc("CascGetFileSize64")
	// Probe Load to surface a clear error before any Open call.
	dllErr = dll.Load()
}

// locateDLL searches a few standard places for CascLib.dll. Production
// wc3-forge ships it alongside the executable; this helper also covers
// `go test` (where the test binary lives in a temp dir) by walking up to
// the repo's scripts/casclib/ for development convenience.
func locateDLL() string {
	const name = "CascLib.dll"
	candidates := []string{name} // current dir / Windows DLL search path

	if exe, err := osExecutable(); err == nil {
		candidates = append(candidates, filepathDir(exe)+"\\"+name)
	}
	if cwd, err := osGetwd(); err == nil {
		// scripts/casclib/CascLib.dll relative to the project root.
		// Walk up a few levels because `go test` runs from the package dir.
		for i, p := 0, cwd; i < 5; i, p = i+1, filepathDir(p) {
			candidates = append(candidates, p+"\\scripts\\casclib\\"+name)
		}
	}

	for _, c := range candidates {
		if _, err := osStat(c); err == nil {
			return c
		}
	}
	// Fall through to the bare name and let Windows DLL search find it
	// (or fail with a clear "not found" at first .Call()).
	return name
}

// Storage is an open handle to a CASC install (Warcraft III, in our case).
type Storage struct {
	handle uintptr
	mu     sync.Mutex
}

// Open returns a Storage for the install at the given path. The path
// should be the install ROOT (the directory containing .build.info),
// e.g. "C:\\Program Files (x86)\\Warcraft III". A CascLib product
// suffix is appended automatically — `:w3` for the retail WC3 product.
// Without the suffix, multi-product installs can open a storage handle
// that returns 0 bytes for every file. CascLib's storage open is
// expensive (scans the archive index files) — call once at startup,
// reuse for the process lifetime, Close on shutdown.
func Open(installPath string) (*Storage, error) {
	dllOnce.Do(loadDLL)
	if dllErr != nil {
		return nil, fmt.Errorf("load CascLib.dll: %w", dllErr)
	}

	// CascOpenStorage(LPCTSTR) — TCHAR is wchar_t with _UNICODE.
	// HiveWE passes the path WITH a trailing :w3 product specifier (e.g.
	// "C:\\Program Files (x86)\\Warcraft III\\:w3"). The backslash is
	// what their std::filesystem path-append produced; CascLib's parser
	// understands the :<product> tail either way.
	pathPtr, err := syscall.UTF16PtrFromString(installPath + `\:w3`)
	if err != nil {
		return nil, fmt.Errorf("convert path: %w", err)
	}
	var handle uintptr
	// IMPORTANT: dwLocaleMask = 0 means "NO locales accessible"; storage
	// opens fine but every file read returns 0 bytes. CASC_LOCALE_ALL
	// (0xFFFFFFFF) opens every locale. HiveWE's casc.ixx uses the same.
	const cascLocaleAll = 0xFFFFFFFF
	r1, _, _ := procOpenStorage.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(cascLocaleAll),
		uintptr(unsafe.Pointer(&handle)),
	)
	if r1 == 0 {
		// CascLib reports the cause through GetLastError. The Win32 error
		// codes are CASC-specific in some cases (e.g. ERROR_FILE_CORRUPT
		// when .build.info parsing fails); we surface the raw number plus
		// path so the caller can diagnose.
		return nil, fmt.Errorf("CascOpenStorage failed for %q (GetLastError=%d)", installPath, lastError())
	}
	return &Storage{handle: handle}, nil
}

// Close releases the storage. Idempotent.
func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return nil
	}
	r1, _, _ := procCloseStorage.Call(s.handle)
	s.handle = 0
	if r1 == 0 {
		return fmt.Errorf("CascCloseStorage failed (GetLastError=%d)", lastError())
	}
	return nil
}

// ReadFile fetches one file by its WC3-relative path (e.g.
// "units/human/footman/footman.mdx" or its backslash equivalent).
// We try the standard set of CASC mount prefixes in order: `war3.w3mod:`
// (the main stock-asset mount), then localized + HD variants. Returns
// (nil, false, nil) if the name isn't found in any mount.
//
// Why prefixes: WC3's CASC organizes assets via TVFS (TACT virtual
// file system). The same logical path can live under multiple .w3mod
// "mounts" (e.g. `war3.w3mod:` for shared SD, `en.w3mod:` for English
// localized strings/textures, `_hd.w3mod:` for the Reforged HD pack).
// CascOpenFile of a bare path returns a fake-success 0-byte handle
// instead of an error — so we explicitly try each prefix and accept
// the first non-zero response.
func (s *Storage) ReadFile(name string) ([]byte, bool, error) {
	// Normalize to backslash-style; CASC stores paths with `\`.
	bs := strings.ReplaceAll(name, "/", "\\")
	for _, prefix := range cascPrefixes {
		full := prefix + bs
		data, ok, err := s.openOne(full)
		if err != nil {
			return nil, false, err
		}
		if ok && len(data) > 0 {
			return data, true, nil
		}
	}
	return nil, false, nil
}

// cascPrefixes are the CASC mount paths we try in order. SD assets first
// (most stock models live here), then HD, then localized. Add more as
// needed once we see real misses.
var cascPrefixes = []string{
	"war3.w3mod:",
	"war3.w3mod:_hd.w3mod:",
	"war3.w3mod:_locales\\enus.w3mod:",
	"war3.w3mod:_deprecated.w3mod:",
}

// openOne does the raw open-read-close for a single fully-qualified
// CASC path. Caller assembles the path.
func (s *Storage) openOne(name string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return nil, false, fmt.Errorf("storage closed")
	}

	// CascOpenFile takes a const void* file name. For string lookups it's
	// a null-terminated ASCII (or UTF-8) C-string. Empty + null terminator.
	nameBytes := append([]byte(name), 0)

	var fileHandle uintptr
	r1, _, _ := procOpenFile.Call(
		s.handle,
		uintptr(unsafe.Pointer(&nameBytes[0])),
		uintptr(0), // dwLocaleFlags
		uintptr(0), // dwOpenFlags
		uintptr(unsafe.Pointer(&fileHandle)),
	)
	if r1 == 0 {
		// ERROR_FILE_NOT_FOUND (2) and ERROR_PATH_NOT_FOUND (3) are the
		// common "not in CASC" cases. Anything else is unexpected and
		// worth surfacing.
		errno := lastError()
		if errno == 2 || errno == 3 {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("CascOpenFile(%q) failed (GetLastError=%d)", name, errno)
	}
	defer procCloseFile.Call(fileHandle)

	// CascGetFileSize64 takes an out-param uint64*. Bool return.
	var size64 uint64
	r1, _, _ = procGetFileSize.Call(fileHandle, uintptr(unsafe.Pointer(&size64)))
	if r1 == 0 {
		return nil, false, fmt.Errorf("CascGetFileSize64 failed for %q (GetLastError=%d)", name, lastError())
	}
	if size64 == 0 {
		return []byte{}, true, nil
	}
	if size64 > 1<<32 {
		// We do single-shot reads with uint32 length parameter to
		// CascReadFile; >4GiB game assets aren't a thing, but guard
		// anyway so we don't truncate silently.
		return nil, false, fmt.Errorf("CASC file %q too large (%d bytes)", name, size64)
	}
	size := uint32(size64)

	buf := make([]byte, size)
	var bytesRead uint32
	r1, _, _ = procReadFile.Call(
		fileHandle,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(size),
		uintptr(unsafe.Pointer(&bytesRead)),
	)
	if r1 == 0 {
		return nil, false, fmt.Errorf("CascReadFile(%q) failed (GetLastError=%d)", name, lastError())
	}
	return buf[:bytesRead], true, nil
}

func lastError() uint32 {
	// syscall.GetLastError returns a Go error; we want the raw Win32 code.
	// On Windows, errno is the LastError DWORD.
	if e := syscall.GetLastError(); e != nil {
		if errno, ok := e.(syscall.Errno); ok {
			return uint32(errno)
		}
	}
	return 0
}
