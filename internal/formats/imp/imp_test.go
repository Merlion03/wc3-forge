package imp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// fixturePath is a real war3map.imp lifted from HiveWE's bundled "test map".
// It is 140 bytes: version 1, count 8, and exercises BOTH flag-byte
// conventions — 0x1D for the bare "Crystal.mdx" and 0x0D for seven
// "war3mapSkin.w3*" object-shadow paths. See testdata/README.md for the
// provenance and the flag-byte survey that pinned the semantics.
const fixtureName = "war3map.imp"

func loadFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// TestRoundTripByteIdentical is THE format-proving test: parse the real fixture
// and re-encode it, asserting the output is byte-for-byte identical to the
// input. This proves the version/count header, the per-entry flag byte, and the
// NUL-terminated path encoding are all modeled correctly.
func TestRoundTripByteIdentical(t *testing.T) {
	orig := loadFixture(t)

	f, err := Parse(orig)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	got := f.Encode()
	if !bytes.Equal(got, orig) {
		t.Fatalf("round-trip not byte-identical\n  orig len=%d\n   got len=%d\n  orig=% x\n   got=% x",
			len(orig), len(got), orig, got)
	}
}

// TestParseFixtureContents asserts the decoded structure matches the known
// bytes of the fixture, including the two distinct flag values.
func TestParseFixtureContents(t *testing.T) {
	f, err := Parse(loadFixture(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if f.Version != 1 {
		t.Errorf("Version = %d, want 1", f.Version)
	}
	if len(f.Entries) != 8 {
		t.Fatalf("len(Entries) = %d, want 8", len(f.Entries))
	}

	// First entry: bare filename under the 0x1D custom variant.
	if got := f.Entries[0]; got.Flag != 0x1D || got.Path != "Crystal.mdx" {
		t.Errorf("Entries[0] = {flag:0x%02X path:%q}, want {flag:0x1D path:%q}", got.Flag, got.Path, "Crystal.mdx")
	}

	// Remaining seven: war3mapSkin.* shadows under the 0x0D standard flag.
	wantPaths := []string{
		"war3mapSkin.w3a", "war3mapSkin.w3b", "war3mapSkin.w3d",
		"war3mapSkin.w3h", "war3mapSkin.w3q", "war3mapSkin.w3t",
		"war3mapSkin.w3u",
	}
	for i, want := range wantPaths {
		got := f.Entries[i+1]
		if got.Flag != StandardFlag || got.Path != want {
			t.Errorf("Entries[%d] = {flag:0x%02X path:%q}, want {flag:0x%02X path:%q}",
				i+1, got.Flag, got.Path, StandardFlag, want)
		}
	}
}

// TestAddAndHas covers the mutation surface: Add increments the count, uses the
// standard flag, normalizes slashes, and is idempotent; Has matches
// case-insensitively and slash-insensitively.
func TestAddAndHas(t *testing.T) {
	f, err := Parse(loadFixture(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	before := len(f.Entries)

	// Add a new path using a forward slash to confirm normalization.
	f.Add("war3mapImported/cube.mdx")

	if got := len(f.Entries); got != before+1 {
		t.Fatalf("after Add, len(Entries) = %d, want %d", got, before+1)
	}

	added := f.Entries[len(f.Entries)-1]
	if added.Flag != StandardFlag {
		t.Errorf("added entry flag = 0x%02X, want 0x%02X (StandardFlag)", added.Flag, StandardFlag)
	}
	if added.Path != "war3mapImported\\cube.mdx" {
		t.Errorf("added entry path = %q, want %q (slashes normalized to backslash)", added.Path, "war3mapImported\\cube.mdx")
	}

	// Has must find it regardless of slash style or case.
	for _, q := range []string{
		"war3mapImported\\cube.mdx",
		"war3mapImported/cube.mdx",
		"WAR3MAPIMPORTED\\CUBE.MDX",
		"war3mapimported/Cube.MDX",
	} {
		if !f.Has(q) {
			t.Errorf("Has(%q) = false, want true", q)
		}
	}

	if f.Has("war3mapImported\\notthere.mdx") {
		t.Errorf("Has(notthere) = true, want false")
	}

	// Add must be idempotent: re-adding the same path (different slash/case)
	// does not grow the table.
	countAfterFirst := len(f.Entries)
	f.Add("war3mapImported\\cube.mdx")
	f.Add("WAR3MAPIMPORTED/CUBE.MDX")
	if got := len(f.Entries); got != countAfterFirst {
		t.Errorf("Add not idempotent: len(Entries) = %d, want %d", got, countAfterFirst)
	}
}

// TestAddRoundTripsThroughParse confirms an Add'd entry survives Encode→Parse
// (the table stays well-formed after mutation).
func TestAddRoundTripsThroughParse(t *testing.T) {
	f := &File{Version: 1}
	f.Add("war3mapImported\\cube.mdx")
	f.Add("war3mapImported\\cube.blp")

	reparsed, err := Parse(f.Encode())
	if err != nil {
		t.Fatalf("Parse(Encode): %v", err)
	}
	if !reparsed.Has("war3mapImported/cube.mdx") || !reparsed.Has("war3mapImported/cube.blp") {
		t.Errorf("reparsed table missing added entries: %+v", reparsed.Entries)
	}
	if reparsed.Version != 1 || len(reparsed.Entries) != 2 {
		t.Errorf("reparsed = {version:%d entries:%d}, want {1 2}", reparsed.Version, len(reparsed.Entries))
	}
}

// TestParseEmpty handles the common "no imports" table (version 1, count 0).
func TestParseEmpty(t *testing.T) {
	empty := []byte{0x01, 0, 0, 0, 0, 0, 0, 0}
	f, err := Parse(empty)
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	if f.Version != 1 || len(f.Entries) != 0 {
		t.Fatalf("empty parse = {version:%d entries:%d}, want {1 0}", f.Version, len(f.Entries))
	}
	if got := f.Encode(); !bytes.Equal(got, empty) {
		t.Errorf("empty round-trip = % x, want % x", got, empty)
	}
}

// TestParseTruncated asserts truncation is reported, not silently tolerated.
func TestParseTruncated(t *testing.T) {
	cases := map[string][]byte{
		"no header":         {0x01, 0, 0},
		"count without body": {0x01, 0, 0, 0, 0x01, 0, 0, 0},                  // declares 1 entry, no data
		"unterminated path": {0x01, 0, 0, 0, 0x01, 0, 0, 0, 0x0D, 'a', 'b'}, // flag + path, no NUL
	}
	for name, data := range cases {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: Parse succeeded, want error", name)
		}
	}
}
