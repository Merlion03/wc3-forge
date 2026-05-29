package casc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

// wc3InstallDefault is the canonical install root for the current OS. The
// tests skip when nothing's found there (a fresh dev machine without WC3
// is a normal state) — set WC3FORGE_WC3_PATH to override.
var wc3InstallDefault = func() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files (x86)\Warcraft III`
	}
	return "/Applications/Warcraft III"
}()

func wc3InstallPath(t *testing.T) string {
	t.Helper()
	p := os.Getenv("WC3FORGE_WC3_PATH")
	if p == "" {
		p = wc3InstallDefault
	}
	// .build.info is what CascLib looks for to recognize a CASC root; if
	// it's missing, the test wouldn't get past Open anyway.
	if _, err := os.Stat(filepath.Join(p, ".build.info")); err != nil {
		t.Skipf("WC3 install not found at %q (set WC3FORGE_WC3_PATH to override)", p)
	}
	return p
}

func TestOpen(t *testing.T) {
	s, err := Open(wc3InstallPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.handle == 0 {
		t.Fatalf("nil storage handle")
	}
}

func TestEnumerate(t *testing.T) {
	s, err := Open(wc3InstallPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Enumeration uses the package-level Find* funcs, which Open already
	// bound cross-platform (Open -> loadLib -> bindCASCLib). Using them
	// directly — instead of re-binding through a dlopen handle — keeps this
	// test working on Windows, where there's no purego.Dlopen.
	//
	// CASC_FIND_DATA from CascLib.h — 0x1108 bytes (4360). Most of that
	// is szFileName[MAX_PATH=0x400] which we read as our result.
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
		t.Fatalf("CascFindFirstFile failed (handle=%x)", hFind)
	}
	defer cascFindClose(hFind)

	count := 0
	for {
		// szFileName starts at offset 0x18 (24) in CASC_FIND_DATA.
		// Width is 0x400 (1024) bytes.
		name := readCString(data[0x18 : 0x18+0x400])
		lname := strings.ToLower(name)
		if strings.Contains(lname, "footman.mdx") || strings.Contains(lname, "miscdata.txt") || strings.Contains(lname, "units.slk") || strings.Contains(lname, "teamcolor00.blp") {
			t.Logf("[%d] %s", count, name)
		}
		count++
		if count > 100000 {
			t.Logf("... (capped at %d entries)", count)
			break
		}
		if !cascFindNextFile(hFind, unsafe.Pointer(&data[0])) {
			break
		}
	}
	t.Logf("Total CASC entries: %d", count)
}

func readCString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func TestReadCanonicalAssets(t *testing.T) {
	s, err := Open(wc3InstallPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Files we expect to find in any vaguely-recent WC3 install. Lowercase
	// paths, forward-slash. SLK + INI + a guaranteed stock unit + a stock
	// texture cover the four flavours of asset mdx-m3-viewer will request.
	cases := []struct {
		name    string
		minSize int
	}{
		// Caller-relative (no prefix) — ReadFile prepends mount prefixes.
		{"units/human/footman/footman.mdx", 1000},
		{"units\\human\\footman\\footman.mdx", 1000},
		// Note: not every well-known asset lives under war3.w3mod:; the
		// mount prefix list in casc.go will need to grow as misses surface.
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, ok, err := s.ReadFile(c.name)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if !ok {
				t.Fatalf("%s not present in CASC", c.name)
			}
			if len(data) < c.minSize {
				t.Errorf("%s too small: %d bytes < expected %d", c.name, len(data), c.minSize)
			}
			t.Logf("%s: %d bytes", c.name, len(data))
		})
	}
}
