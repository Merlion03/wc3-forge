// Package casc reads files from a local Warcraft III install's CASC storage.
//
// The Blizzard CASC format is non-trivial — multi-stage indirection from
// game-relative paths → content keys → encoding keys → archive offsets,
// plus the BLTE block-level encoding inside each archive. Rather than
// porting all of it to Go (a 2-3k LOC project), we dlopen Ladislav
// Zezula's battle-tested CascLib (same library HiveWE's scripts use for
// StormLib).
//
// We load via purego, not cgo: no toolchain needed at build time, and the
// same code works on Windows (CascLib.dll) and macOS (libcasc.dylib). The
// library must sit next to the executable, or anywhere on the OS's
// dynamic loader search path. The project's scripts/casclib/ holds the
// vendored Windows binaries; the macOS .dylib is built on demand by
// scripts/build-casclib-macos.sh and is .gitignored.
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
	"unsafe"

	"github.com/ebitengine/purego"
)

// Tiny indirection so locateLib stays readable without importing
// "os"+"path/filepath" in three places.
var (
	osExecutable = os.Executable
	osGetwd      = os.Getwd
	osStat       = os.Stat
	filepathDir  = filepath.Dir
)

// DLLPath, if non-empty, overrides the auto-locate. Set this before any
// Open call to point at a specific CascLib library (e.g. for tests).
// Despite the name, this is the path to the shared library on any OS —
// CascLib.dll on Windows, libcasc.dylib on macOS.
var DLLPath string

// CascLib symbols are loaded once at process startup via purego. Function
// vars are assigned by RegisterLibFunc in loadLib; subsequent calls dispatch
// through them. All path arguments are uintptr — the caller does the
// platform-appropriate string conversion (UTF-16 on Windows, NUL-terminated
// UTF-8/ASCII on macOS) before passing the pointer in.
var (
	libOnce   sync.Once
	libErr    error
	libHandle uintptr // exposed for in-package tests that need to look up rare CascLib symbols (e.g. enumeration) we don't surface as part of the public API.

	// IMPORTANT: every parameter through which CascLib writes back into Go
	// memory (handles, sizes, byte counts, the read buffer, the find-data
	// struct) MUST be a typed pointer (*T / unsafe.Pointer), NOT uintptr.
	// purego only keeps pointer-kind args alive across the foreign call
	// (func.go addValue stores v.Pointer() in the live args slice); a
	// `uintptr(unsafe.Pointer(&x))` arg is treated as a plain integer, so the
	// compiler may keep `x` in a register and never reload CascLib's
	// write-back — yielding garbage handles/sizes that fail flakily depending
	// on optimization + GC timing. (This was the macOS "storage closed"/
	// "CascGetFileSize64 failed" flake and the Windows empty-texture
	// regression.) Opaque handles we only pass IN (hStorage/hFile/hFind) stay
	// uintptr.
	cascOpenStorage  func(szParams uintptr, dwLocaleMask uint32, phStorage *uintptr) bool
	cascCloseStorage func(hStorage uintptr) bool
	cascOpenFile     func(hStorage uintptr, pvFileName unsafe.Pointer, dwLocaleFlags uint32, dwOpenFlags uint32, phFile *uintptr) bool
	cascCloseFile    func(hFile uintptr) bool
	cascReadFile     func(hFile uintptr, lpBuffer unsafe.Pointer, dwToRead uint32, pdwRead *uint32) bool
	cascGetFileSize  func(hFile uintptr, pSize *uint64) bool

	// CASC enumeration (CascFindFirstFile/NextFile/Close), backing
	// ListByPrefix. Returns an opaque find HANDLE; pFindData is an out-param
	// CASC_FIND_DATA struct CascLib fills, so it's unsafe.Pointer (pinned).
	cascFindFirstFile func(hStorage uintptr, szMask unsafe.Pointer, pFindData unsafe.Pointer, szListFile unsafe.Pointer) uintptr
	cascFindNextFile  func(hFind uintptr, pFindData unsafe.Pointer) bool
	cascFindClose     func(hFind uintptr) bool
)

func loadLib() {
	path := DLLPath
	if path == "" {
		path = locateLib()
	}
	handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		libErr = fmt.Errorf("dlopen %s: %w", path, err)
		return
	}
	libHandle = handle
	// CascLib was built with _UNICODE on Windows. The lowercase symbol
	// CascOpenFile is the ASCII variant (CascOpenFileW would be wide).
	// File names within CASC are conventionally lowercase ASCII so we use
	// the narrow flavour to avoid UTF-16 conversion per call.
	//
	// On macOS, names are not decorated (no W/A suffix); the symbols come
	// out as plain CascOpenFile etc., matching what RegisterLibFunc looks
	// up below.
	purego.RegisterLibFunc(&cascOpenStorage, handle, "CascOpenStorage")
	purego.RegisterLibFunc(&cascCloseStorage, handle, "CascCloseStorage")
	purego.RegisterLibFunc(&cascOpenFile, handle, "CascOpenFile")
	purego.RegisterLibFunc(&cascCloseFile, handle, "CascCloseFile")
	purego.RegisterLibFunc(&cascReadFile, handle, "CascReadFile")
	// Use the 64-bit size variant; the 32-bit CascGetFileSize is the
	// legacy entry point and on this vcpkg build it returns
	// INVALID_FILE_SIZE for valid files (no Win32 error set).
	purego.RegisterLibFunc(&cascGetFileSize, handle, "CascGetFileSize64")
	// Enumeration symbols for ListByPrefix. Core CascLib API, always present
	// in our vendored builds; registered up front (cheap) rather than lazily.
	purego.RegisterLibFunc(&cascFindFirstFile, handle, "CascFindFirstFile")
	purego.RegisterLibFunc(&cascFindNextFile, handle, "CascFindNextFile")
	purego.RegisterLibFunc(&cascFindClose, handle, "CascFindClose")
}

// locateLib searches a few standard places for the CASC shared library.
// Production wc3-forge ships it alongside the executable; this helper also
// covers `go test` (where the test binary lives in a temp dir) by walking
// up to the repo's scripts/casclib/ for development convenience.
//
// libBaseName + the OS-specific filepath separator come from libname_*.go.
func locateLib() string {
	name := libFileName
	sep := string(filepath.Separator)
	candidates := []string{name} // current dir / OS loader search path

	if exe, err := osExecutable(); err == nil {
		candidates = append(candidates, filepathDir(exe)+sep+name)
	}
	if cwd, err := osGetwd(); err == nil {
		// scripts/casclib/<libFileName> relative to the project root.
		// Walk up a few levels because `go test` runs from the package dir.
		for i, p := 0, cwd; i < 5; i, p = i+1, filepathDir(p) {
			candidates = append(candidates, filepath.Join(p, "scripts", "casclib", name))
		}
	}

	for _, c := range candidates {
		if _, err := osStat(c); err == nil {
			return c
		}
	}
	// Fall through to the bare name and let the OS loader find it
	// (or fail with a clear "not found" at first call).
	return name
}

// Storage is an open handle to a CASC install (Warcraft III, in our case).
type Storage struct {
	handle uintptr
	mu     sync.Mutex
	// reforged, when true, reorders the CASC mount-prefix search list so
	// `_hd.w3mod:` is checked BEFORE `war3.w3mod:`. This matters because the
	// same logical path (e.g. "units/human/footman/footman.mdx") can resolve
	// to either the SD or HD asset depending on which mount we hit first.
	// Default (false) prefers SD assets. Use SetReforged to flip.
	reforged bool
}

// SetReforged sets the HD/SD preference. Concurrent-safe.
func (s *Storage) SetReforged(b bool) {
	s.mu.Lock()
	s.reforged = b
	s.mu.Unlock()
}

// Reforged returns the current HD/SD preference.
func (s *Storage) Reforged() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reforged
}

// Open returns a Storage for the install at the given path. The path
// should be the install ROOT (the directory containing .build.info),
// e.g. "C:\\Program Files (x86)\\Warcraft III" or
// "/Applications/Warcraft III". A CascLib product suffix is appended
// automatically — `:w3` for the retail WC3 product. Without the suffix,
// multi-product installs can open a storage handle that returns 0 bytes
// for every file. CascLib's storage open is expensive (scans the archive
// index files) — call once at startup, reuse for the process lifetime,
// Close on shutdown.
func Open(installPath string) (*Storage, error) {
	libOnce.Do(loadLib)
	if libErr != nil {
		return nil, fmt.Errorf("load CASC library: %w", libErr)
	}

	// HiveWE passes the path WITH a trailing :w3 product specifier (e.g.
	// "C:\\Program Files (x86)\\Warcraft III\\:w3"). The separator before
	// :w3 is what their std::filesystem path-append produced; CascLib's
	// parser understands the :<product> tail either way. We use the
	// platform-native separator so the path looks natural to the user
	// in error messages.
	fullPath := installPath + string(filepath.Separator) + ":w3"
	pathPtr, free, err := encodeLibPath(fullPath)
	if err != nil {
		return nil, fmt.Errorf("convert path: %w", err)
	}
	defer free()

	var handle uintptr
	// IMPORTANT: dwLocaleMask = 0 means "NO locales accessible"; storage
	// opens fine but every file read returns 0 bytes. CASC_LOCALE_ALL
	// (0xFFFFFFFF) opens every locale. HiveWE's casc.ixx uses the same.
	const cascLocaleAll uint32 = 0xFFFFFFFF
	if !cascOpenStorage(pathPtr, cascLocaleAll, &handle) {
		// CascLib reports the cause through GetLastError (Windows) or
		// errno (POSIX). We surface whichever the platform-specific
		// lastError() returns plus the path so the caller can diagnose.
		return nil, fmt.Errorf("CascOpenStorage failed for %q (err=%d)", installPath, lastError())
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
	ok := cascCloseStorage(s.handle)
	s.handle = 0
	if !ok {
		return fmt.Errorf("CascCloseStorage failed (err=%d)", lastError())
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
	prefixes := sdCascPrefixes
	if s.Reforged() {
		prefixes = hdCascPrefixes
	}
	for _, prefix := range prefixes {
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

// sdCascPrefixes are the CASC mount paths we try in SD (Classic) mode.
// `war3.w3mod:` (the main shared SD-asset mount) is tried first; HD is
// available as a fallback for assets that only exist HD-side (e.g. some
// Reforged-only TeamColor textures).
var sdCascPrefixes = []string{
	"war3.w3mod:",
	"war3.w3mod:_hd.w3mod:",
	"war3.w3mod:_locales\\enus.w3mod:",
	"war3.w3mod:_deprecated.w3mod:",
}

// hdCascPrefixes are the CASC mount paths we try in HD (Reforged) mode.
// `_hd.w3mod:` first so HD models, materials and textures are preferred
// over their SD siblings when both exist under the same logical path.
var hdCascPrefixes = []string{
	"war3.w3mod:_hd.w3mod:",
	"war3.w3mod:",
	"war3.w3mod:_locales\\enus.w3mod:",
	"war3.w3mod:_deprecated.w3mod:",
}

// cascPrefixes is retained as the SD default for back-compat with callers
// (and the test suite) that referenced the old name. New code should use
// SetReforged + ReadFile instead of consulting this slice directly.
//
// Deprecated: use the Storage's reforged-aware ReadFile.
var cascPrefixes = sdCascPrefixes

// ListByPrefix returns every CASC entry whose lowercased name starts with
// prefix (forward-slash form). prefix MUST end with a "/" — e.g.
// "replaceabletextures/commandbuttons/". The returned names are lowercased +
// forward-slash. Backed by CascFindFirstFile/CascFindNextFile; expensive
// (~10-15ms for a full enumeration on a modern CASC install) so callers
// should cache the result.
//
// Returns an empty slice + nil error when no entries match; a real error
// only when CASC enumeration fails outright (storage closed, library load
// failure).
func (s *Storage) ListByPrefix(prefix string) ([]string, error) {
	libOnce.Do(loadLib)
	if libErr != nil {
		return nil, fmt.Errorf("load CASC library: %w", libErr)
	}
	if cascFindFirstFile == nil || cascFindNextFile == nil || cascFindClose == nil {
		return nil, fmt.Errorf("CASC enumeration symbols unavailable on this CascLib build")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return nil, fmt.Errorf("storage closed")
	}

	// CASC_FIND_DATA layout from CascLib.h: 0x1108 bytes total. szFileName
	// is at offset 0x18, MAX_PATH = 0x400 bytes wide. CascLib writes into
	// this buffer, so it's passed as unsafe.Pointer (purego keeps it pinned;
	// see the binding comment above).
	var data [0x1108]byte
	mask := append([]byte("*"), 0)
	listfileName := append([]byte(""), 0)
	hFind := cascFindFirstFile(
		s.handle,
		unsafe.Pointer(&mask[0]),
		unsafe.Pointer(&data[0]),
		unsafe.Pointer(&listfileName[0]),
	)
	if hFind == 0 || hFind == ^uintptr(0) {
		// Empty storage or no listfile entries — treat as "no matches" rather
		// than an error. CascLib doesn't distinguish these.
		return nil, nil
	}
	defer cascFindClose(hFind)

	prefixLC := strings.ToLower(prefix)
	out := make([]string, 0, 256)
	// Hard cap to avoid runaway loops on a corrupt storage. Real CASC
	// installs have ~200k entries; we'll never legitimately need more.
	for n := 0; n < 500_000; n++ {
		name := readCStringFromBuf(data[0x18 : 0x18+0x400])
		// CASC stores paths with backslashes; normalize before the prefix
		// check.
		lname := strings.ToLower(strings.ReplaceAll(name, `\`, "/"))
		if strings.HasPrefix(lname, prefixLC) {
			out = append(out, lname)
		}
		if !cascFindNextFile(hFind, unsafe.Pointer(&data[0])) {
			break
		}
	}
	return out, nil
}

// readCStringFromBuf returns the bytes up to (but excluding) the first NUL
// in b. Used by ListByPrefix to extract the szFileName field from a
// CASC_FIND_DATA struct.
func readCStringFromBuf(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// openOne does the raw open-read-close for a single fully-qualified
// CASC path. Caller assembles the path.
//
// pvFileName is `const void *` in CascLib's signature; for string lookups
// it's a null-terminated ASCII (or UTF-8) C-string regardless of OS —
// even on Windows where the storage path uses TCHAR, file names are
// narrow. So we always pass a UTF-8 byte slice here, not the platform-
// specific encoded form.
func (s *Storage) openOne(name string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return nil, false, fmt.Errorf("storage closed")
	}

	nameBytes := append([]byte(name), 0)

	var fileHandle uintptr
	ok := cascOpenFile(
		s.handle,
		unsafe.Pointer(&nameBytes[0]),
		0, // dwLocaleFlags
		0, // dwOpenFlags
		&fileHandle,
	)
	if !ok {
		// ERROR_FILE_NOT_FOUND (2) and ERROR_PATH_NOT_FOUND (3) are the
		// common "not in CASC" cases on Windows; ENOENT (2) on POSIX
		// collapses to the same numeric value. Anything else is
		// unexpected and worth surfacing.
		errno := lastError()
		if errno == 2 || errno == 3 {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("CascOpenFile(%q) failed (err=%d)", name, errno)
	}
	defer cascCloseFile(fileHandle)

	// CascGetFileSize64 takes an out-param uint64*. Bool return.
	var size64 uint64
	ok = cascGetFileSize(fileHandle, &size64)
	if !ok {
		return nil, false, fmt.Errorf("CascGetFileSize64 failed for %q (err=%d)", name, lastError())
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
	ok = cascReadFile(
		fileHandle,
		unsafe.Pointer(&buf[0]),
		size,
		&bytesRead,
	)
	if !ok {
		return nil, false, fmt.Errorf("CascReadFile(%q) failed (err=%d)", name, lastError())
	}
	return buf[:bytesRead], true, nil
}
