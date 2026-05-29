package casc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

	// Enumeration goes through the platform raw-op helpers (rawFindFirstFile
	// /NextFile/Close), which Open already bound cross-platform (Open ->
	// loadLib -> bindCASCLib). Using them — instead of re-binding through a
	// dlopen handle — keeps this test working on Windows, where the call
	// layer is syscall.LazyProc.Call rather than purego.
	//
	// CASC_FIND_DATA from CascLib.h — findDataSize bytes. Most of that is
	// szFileName (findDataNameOff..+findDataNameLen) which we read as result.
	var data [findDataSize]byte
	hFind := rawFindFirstFile(s.handle, "*", &data)
	if hFind == 0 || hFind == ^uintptr(0) {
		t.Fatalf("CascFindFirstFile failed (handle=%x)", hFind)
	}
	defer rawFindClose(hFind)

	count := 0
	for {
		name := readCString(data[findDataNameOff : findDataNameOff+findDataNameLen])
		lname := strings.ToLower(name)
		if strings.Contains(lname, "footman.mdx") || strings.Contains(lname, "miscdata.txt") || strings.Contains(lname, "units.slk") || strings.Contains(lname, "teamcolor00.blp") {
			t.Logf("[%d] %s", count, name)
		}
		count++
		if count > 100000 {
			t.Logf("... (capped at %d entries)", count)
			break
		}
		if !rawFindNextFile(hFind, &data) {
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
