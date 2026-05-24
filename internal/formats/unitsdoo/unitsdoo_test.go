package unitsdoo

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Fixture 1: wc3-survival's war3mapUnits.doo. Small (1166 bytes) — version 8,
// subversion 11, 10 entities including a sloc + Goblin Merchant + gym units.
var fixturePathV16 = filepath.Join("testdata", "wc3_survival_v1_6.doo")

// Fixture 2: enfos v2.64f's war3mapUnits.doo. Larger (1396 bytes) — exercises
// more of the per-entity code paths than the survival fixture.
var fixturePathEnfo = filepath.Join("testdata", "enfo_ffb_v2_64f.doo")

func TestParse_v1_6(t *testing.T) {
	data, err := os.ReadFile(fixturePathV16)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Header sanity.
	if got, want := string(f.Format[:]), "W3do"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
	if f.Version != 7 && f.Version != 8 && f.Version != 9 {
		t.Errorf("unexpected Version %d (expected 7, 8, or 9)", f.Version)
	}
	if f.SubVersion != 9 && f.SubVersion != 11 {
		t.Errorf("unexpected SubVersion %d (expected 9 or 11)", f.SubVersion)
	}

	t.Logf("Format=%q Version=%d SubVersion=%d Entities=%d",
		string(f.Format[:]), f.Version, f.SubVersion, len(f.Entities))

	// At least 5 entities — wc3-survival has the placed Goblin Merchant, hero
	// hologram previews, and a gym of training/preview units.
	if len(f.Entities) < 5 {
		t.Errorf("expected >=5 entities, got %d", len(f.Entities))
	}

	// Per-entity sanity.
	for i, e := range f.Entities {
		if len(e.TypeID) != 4 {
			t.Errorf("entity[%d].TypeID = %q (len %d), want exactly 4 bytes", i, e.TypeID, len(e.TypeID))
		}
		for j, c := range []byte(e.TypeID) {
			// FourCCs are printable ASCII (alphanumeric + sometimes digits/spaces).
			if c < 0x20 || c > 0x7E {
				t.Errorf("entity[%d].TypeID byte %d = 0x%02x (not printable ASCII)", i, j, c)
			}
		}

		// Game-coord sanity. Standard WC3 maps cap out around 480 playable tiles
		// (~7680 game units per side from center). 8192 gives comfortable margin.
		if abs32(e.Position[0]) >= 8192 || abs32(e.Position[1]) >= 8192 {
			t.Errorf("entity[%d] position out of game-coord bounds: %v", i, e.Position)
		}

		// SkinID, when present, must also be 4-byte FourCC.
		if e.SkinID != "" && len(e.SkinID) != 4 {
			t.Errorf("entity[%d].SkinID = %q (len %d), want 4 or empty", i, e.SkinID, len(e.SkinID))
		}
	}

	// Dump everything as pretty JSON for human eyeball check.
	pretty, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("Parsed file:\n%s", string(pretty))
}

// TestRoundTrip asserts that Encode is the byte-faithful inverse of Parse for
// real-world fixtures. This is the contract that lets Session.Save write the
// file back through a folder source without corrupting unmodified entities.
func TestRoundTrip(t *testing.T) {
	cases := []string{fixturePathV16, fixturePathEnfo}
	for _, path := range cases {
		t.Run(filepath.Base(path), func(t *testing.T) {
			orig, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			f, err := Parse(orig)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			out, err := Encode(f)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(orig, out) {
				// Find first divergence to make the failure actionable.
				div := firstDiff(orig, out)
				t.Fatalf("Encode output != original (sizes orig=%d encoded=%d, first diff at offset %d)",
					len(orig), len(out), div)
			}
		})
	}
}

// firstDiff returns the offset of the first differing byte between a and b,
// or min(len(a), len(b)) if one is a prefix of the other.
func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}
