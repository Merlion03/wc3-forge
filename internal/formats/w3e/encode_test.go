package w3e

import (
	"bytes"
	"os"
	"testing"
)

// TestRoundTrip_v1_6 takes the real Reforged-era survival map fixture used by
// the Parse test, re-encodes it, and asserts byte-for-byte equality. A drift
// of even one bit here means a tileset swap would corrupt the map on save.
func TestRoundTrip_v1_6(t *testing.T) {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	encoded, err := Encode(f)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if !bytes.Equal(encoded, data) {
		// Locate the first divergence so the failure is debuggable.
		n := len(data)
		if len(encoded) < n {
			n = len(encoded)
		}
		var firstDiff = -1
		for i := 0; i < n; i++ {
			if encoded[i] != data[i] {
				firstDiff = i
				break
			}
		}
		t.Fatalf("round-trip drift: encoded %d bytes, original %d bytes, first diff at byte %d",
			len(encoded), len(data), firstDiff)
	}
}

// TestRoundTrip_AfterSwap exercises the path that matters for tileset swap:
// rewrite GroundTilesets / CliffTilesets to a different set, remap every
// Tilepoint.GroundTexture / CliffTexture, then assert Parse(Encode(f)) sees
// exactly the post-swap state.
func TestRoundTrip_AfterSwap(t *testing.T) {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Replace tileset palettes with a 2-entry set so the index space is tiny.
	f.Tileset = 'A'
	f.GroundTilesets = []string{"Agrs", "Adrt"}
	f.CliffTilesets = []string{"CAa1"}
	for i := range f.Tiles {
		f.Tiles[i].GroundTexture = uint8(i % 2)
		f.Tiles[i].CliffTexture = 0
	}

	encoded, err := Encode(f)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	round, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse(Encode): %v", err)
	}

	if round.Tileset != 'A' {
		t.Errorf("Tileset = %q, want %q", string(round.Tileset), "A")
	}
	if got, want := round.GroundTilesets, []string{"Agrs", "Adrt"}; !equalStrings(got, want) {
		t.Errorf("GroundTilesets = %v, want %v", got, want)
	}
	if got, want := round.CliffTilesets, []string{"CAa1"}; !equalStrings(got, want) {
		t.Errorf("CliffTilesets = %v, want %v", got, want)
	}
	for i := range round.Tiles {
		if got, want := round.Tiles[i].GroundTexture, uint8(i%2); got != want {
			t.Fatalf("Tiles[%d].GroundTexture = %d, want %d", i, got, want)
			break
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
