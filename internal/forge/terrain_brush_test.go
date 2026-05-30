package forge

import "testing"

// fixtureExtracted is the same Lordaeron survival map the history tests open.
const fixtureExtracted = `C:\Users\4step\projects\wc3-survival-game\map\extracted`

// centerCorner returns a corner index/coords comfortably inside the grid so a
// radius-1 brush footprint never clips an edge.
func centerCorner(t *testing.T, s *Session) (col, row int) {
	t.Helper()
	tr := s.Terrain()
	if tr == nil || tr.Width < 8 || tr.Height < 8 {
		t.Fatalf("fixture terrain too small: %+v", tr)
	}
	return int(tr.Width) / 2, int(tr.Height) / 2
}

// TestPaintTileBrush_FootprintAndUndo paints a radius-1 circle and checks every
// footprint corner flipped to the target tile, then that Undo/Redo round-trips
// the whole dab as one step.
func TestPaintTileBrush_FootprintAndUndo(t *testing.T) {
	tmp := copyFixtureToTemp(t, fixtureExtracted)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	tr := s.Terrain()
	if len(tr.GroundTilesets) < 2 {
		t.Skipf("fixture ground palette has <2 tiles (%v); can't paint a different tile", tr.GroundTilesets)
	}
	col, row := centerCorner(t, s)
	w := int(tr.Width)
	center := row*w + col

	// Pick a target FourCC different from the center corner's current tile.
	curIdx := tr.Tiles[center].GroundTexture
	target := tr.GroundTilesets[0]
	if uint8(0) == curIdx {
		target = tr.GroundTilesets[1]
	}

	// Snapshot the radius-1 circle footprint (center + 4 orthogonal neighbors).
	footprint := []int{center, center - 1, center + 1, center - w, center + w}
	before := make(map[int]uint8, len(footprint))
	for _, idx := range footprint {
		before[idx] = s.Terrain().Tiles[idx].GroundTexture
	}

	if err := s.PaintTileBrush(col, row, 1, "circle", target); err != nil {
		t.Fatalf("PaintTileBrush: %v", err)
	}
	// Expected palette index of the target FourCC (scan; the resolver helper
	// needs the lock held).
	var wantIdx uint8
	for i, id := range s.Terrain().GroundTilesets {
		if id == target {
			wantIdx = uint8(i)
			break
		}
	}
	for _, idx := range footprint {
		if got := s.Terrain().Tiles[idx].GroundTexture; got != wantIdx {
			t.Errorf("corner %d after paint = %d, want %d", idx, got, wantIdx)
		}
	}
	if !s.CanUndo() {
		t.Fatalf("expected an undo step after paint")
	}

	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	for _, idx := range footprint {
		if got := s.Terrain().Tiles[idx].GroundTexture; got != before[idx] {
			t.Errorf("corner %d after undo = %d, want %d (orig)", idx, got, before[idx])
		}
	}
	if err := s.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	for _, idx := range footprint {
		if got := s.Terrain().Tiles[idx].GroundTexture; got != wantIdx {
			t.Errorf("corner %d after redo = %d, want %d", idx, got, wantIdx)
		}
	}
}

// TestCliffBrush_RaiseAndUndo raises the cliff layer over a single corner and
// checks LayerHeight incremented + a renderable cliff texture got set, then that
// Undo restores both.
func TestCliffBrush_RaiseAndUndo(t *testing.T) {
	tmp := copyFixtureToTemp(t, fixtureExtracted)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	col, row := centerCorner(t, s)
	w := int(s.Terrain().Width)
	center := row*w + col

	origLayer := s.Terrain().Tiles[center].LayerHeight
	origTex := s.Terrain().Tiles[center].CliffTexture
	if origLayer >= maxLayerHeight {
		t.Skipf("center corner already at max layer %d", origLayer)
	}

	if err := s.CliffBrush(col, row, 0, "circle", "raise", 0, ""); err != nil {
		t.Fatalf("CliffBrush: %v", err)
	}
	if got := s.Terrain().Tiles[center].LayerHeight; got != origLayer+1 {
		t.Errorf("layer after raise = %d, want %d", got, origLayer+1)
	}
	if got := s.Terrain().Tiles[center].CliffTexture; got > maxLayerHeight {
		t.Errorf("cliff texture after raise = %d, want a valid palette slot", got)
	}

	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := s.Terrain().Tiles[center].LayerHeight; got != origLayer {
		t.Errorf("layer after undo = %d, want %d", got, origLayer)
	}
	if got := s.Terrain().Tiles[center].CliffTexture; got != origTex {
		t.Errorf("cliff texture after undo = %d, want %d", got, origTex)
	}
}

// TestBrushFootprint_CircleVsSquare checks the two shapes produce the expected
// corner counts for a radius-2 brush well inside the grid: a square is the full
// (2r+1)² block; a circle drops the four extreme corners.
func TestBrushFootprint_CircleVsSquare(t *testing.T) {
	tmp := copyFixtureToTemp(t, fixtureExtracted)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	col, row := centerCorner(t, s)

	s.mu.Lock()
	sq := s.terrainBrushCornersLocked(col, row, 2, "square")
	ci := s.terrainBrushCornersLocked(col, row, 2, "circle")
	s.mu.Unlock()

	if len(sq) != 25 {
		t.Errorf("square r=2 footprint = %d corners, want 25", len(sq))
	}
	// radius-2 euclidean: drops the 4 corners at distance √8 > 2 → 21.
	if len(ci) != 21 {
		t.Errorf("circle r=2 footprint = %d corners, want 21", len(ci))
	}
}
