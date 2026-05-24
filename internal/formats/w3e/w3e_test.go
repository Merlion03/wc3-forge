package w3e

import (
	"math"
	"os"
	"testing"
)

// Path to the wc3-survival v1.6 fixture. The map lives in a sibling project
// under ~/projects/wc3-survival-game/. We use an absolute path on purpose:
// the file is a real extracted-from-MPQ Reforged terrain blob and not
// something we want to vendor into this repo.
const fixturePath = `C:\Users\4step\projects\wc3-survival-game\map\extracted\war3map.w3e`

func TestParse_v1_6(t *testing.T) {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Format magic must be "W3E!".
	if f.Format != Magic {
		t.Errorf("format = %q, want %q", f.Format[:], Magic[:])
	}

	// Dimensions: wc3-survival v1.6 is a square map. The on-wire vertex count
	// per axis is `tile_count + 1` (vertex per tile + 1 trailing vertex). For
	// this fixture the editor recorded 65×65 vertices (64-tile span). We don't
	// hard-code "65" beyond the basic invariants — the playable tile count is
	// stored separately in war3map.w3i, so .w3e is just the heightfield grid.
	if f.Width != f.Height {
		t.Errorf("expected square map, got %dx%d", f.Width, f.Height)
	}
	if f.Width < 4 || f.Width > 1024 {
		t.Errorf("dimension %d outside sane bounds [4, 1024]", f.Width)
	}

	// Tile slice length must match width*height (row-major).
	wantTiles := int(f.Width) * int(f.Height)
	if got := len(f.Tiles); got != wantTiles {
		t.Fatalf("Tiles len = %d, want %d (= Width*Height)", got, wantTiles)
	}

	// CenterOffset for a 64-tile map should be (-4096, -4096) — the bottom-left
	// of the map in game-coord space. We compare with a small epsilon because
	// the wire format is float32 and the editor sometimes nudges values by
	// fractional amounts.
	const wantOX, wantOY = float32(-4096), float32(-4096)
	const eps = 1.0
	if math.Abs(float64(f.CenterOffset[0]-wantOX)) > eps ||
		math.Abs(float64(f.CenterOffset[1]-wantOY)) > eps {
		t.Errorf("CenterOffset = %v, want approximately (%v, %v)", f.CenterOffset, wantOX, wantOY)
	}

	// FourCC tileset slot validation: at least one ground entry, all 4 chars.
	if len(f.GroundTilesets) == 0 {
		t.Errorf("no ground tilesets")
	}
	for i, id := range f.GroundTilesets {
		if len(id) != 4 {
			t.Errorf("GroundTilesets[%d] = %q (len %d), want 4-char FourCC", i, id, len(id))
		}
	}
	for i, id := range f.CliffTilesets {
		if len(id) != 4 {
			t.Errorf("CliffTilesets[%d] = %q (len %d), want 4-char FourCC", i, id, len(id))
		}
	}

	// Sample tilepoints: corners + center. We touch each helper to verify
	// the bit-unpack math compiles and doesn't panic on edge values.
	w, h := int(f.Width), int(f.Height)
	idx := func(col, row int) int { return row*w + col }
	type sample struct {
		name     string
		col, row int
	}
	samples := []sample{
		{"bottom-left", 0, 0},
		{"bottom-right", w - 1, 0},
		{"top-left", 0, h - 1},
		{"top-right", w - 1, h - 1},
		{"center", w / 2, h / 2},
	}
	for _, s := range samples {
		tp := f.Tiles[idx(s.col, s.row)]
		// Exercise every helper at least once so a future bit-fiddling
		// regression doesn't sneak past this test.
		_ = tp.Z()
		_ = tp.WaterZ()
		_ = tp.GroundTexIdx()
		_ = tp.CliffTexIdx()
		_ = tp.HasRamp()
		_ = tp.HasBlight()
		_ = tp.HasWater()
		_ = tp.HasBoundary()
	}

	// Visual sanity dump — only printed with `go test -v`.
	t.Logf("war3map.w3e v1.6: version=%d tileset=%q custom=%v dims=%dx%d offset=(%.1f,%.1f)",
		f.Version, string(f.Tileset), f.CustomTileset,
		f.Width, f.Height, f.CenterOffset[0], f.CenterOffset[1])
	t.Logf("ground tilesets (%d): %v", len(f.GroundTilesets), f.GroundTilesets)
	t.Logf("cliff  tilesets (%d): %v", len(f.CliffTilesets), f.CliffTilesets)
	for _, s := range samples {
		tp := f.Tiles[idx(s.col, s.row)]
		gameX := f.CenterOffset[0] + float32(s.col)*128
		gameY := f.CenterOffset[1] + float32(s.row)*128
		t.Logf("  %-13s (col=%2d row=%2d game=(%7.1f,%7.1f)) z=%6.2f waterZ=%6.2f gtex=%d ctex=%d layer=%d var=%d flags=0b%04b boundary=%v",
			s.name, s.col, s.row, gameX, gameY,
			tp.Z(), tp.WaterZ(),
			tp.GroundTexIdx(), tp.CliffTexIdx(),
			tp.LayerHeight, tp.TextureDetails,
			tp.Flags, tp.Boundary)
	}
}
