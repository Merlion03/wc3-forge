package forge

import "fmt"

// Terrain BRUSH mutators — region edits over a circular/square footprint of
// corners, the engine behind the GUI Terrain Palette (and the matching
// terrain.*_brush MCP tools).
//
// Where terrain_mutate.go edits a single corner, this file edits every corner
// inside a brush footprint in ONE atomic command (one undo step, one
// entity-changed event). A click-drag "stroke" wraps many such dabs in a
// BeginUndoGroup/EndUndoGroup so the whole stroke collapses to a single Ctrl+Z.
//
// Four operations, mirroring WC3 World Editor / HiveWE:
//
//	PaintTileBrush  — set the ground tile (FourCC) of every footprint corner.
//	HeightBrush     — raise / lower / flatten / smooth the ground heightfield.
//	CliffBrush      — raise / lower / set the integer cliff LayerHeight (0..15),
//	                  setting CliffTexture so a cliff wall actually renders.
//	RampBrush       — toggle the ramp flag so a cliff step renders as a walkable
//	                  slope instead of a sheer wall.
//
// All four resolve the brush footprint Go-side from (centerCol, centerRow,
// radius, shape) so the wire stays tiny during a drag — the frontend streams
// one dab per corner the cursor crosses, not a corner list. radius is in corner
// units (0 = a single corner, 1 = a 3×3 neighborhood, …); shape is "circle"
// (euclidean) or "square" (chebyshev). col/row are 0-based corner grid indices,
// linear index row*Width+col, matching terrain_mutate.go and GetTerrain.
//
// Cliff geometry is fully derived from LayerHeight (+ ramp flags / CliffTexture)
// by GetTerrain's cliff-placement pass and the frontend cliff renderer, so these
// brushes only touch the packed .w3e fields — the walls recompute on re-render.

// Default per-application height step (game-space Z, WC3 world units) for the
// raise/lower height brush when the caller doesn't override strength. One WC3
// cliff step is 128 studs; a single dab nudges far less so repeated strokes
// build up gradually, matching the editor's feel.
const defaultHeightStep = 16.0

// maxLayerHeight is the inclusive ceiling for the packed 4-bit cliff layer.
const maxLayerHeight = 15

// terrainField identifies which packed Tilepoint field a single corner edit
// targets, so one compound command can carry mixed tile/height/layer/flag edits.
type terrainField uint8

const (
	fieldTile      terrainField = iota // GroundTexture (palette index)
	fieldHeightRaw                     // GroundHeight (raw int16)
	fieldLayer                         // LayerHeight (0..15)
	fieldCliffTex                      // CliffTexture (0..15)
	fieldFlags                         // Flags (ramp/blight/water/boundary nibble)
)

// terrainCornerEdit is one field write on one corner, with the prior value so
// Undo round-trips without re-deriving anything. Values are widened to int32 so
// a single slot covers uint8 indices and int16 heights uniformly.
type terrainCornerEdit struct {
	idx    int
	field  terrainField
	oldVal int32
	newVal int32
}

// terrainBatchCmd is one brush dab: a list of per-corner field writes applied
// (Redo) or reverted (Undo) as a unit. Mirrors the per-mutator command pattern
// in terrain_mutate.go / history.go, but compound so a footprint is one step.
type terrainBatchCmd struct {
	label string
	edits []terrainCornerEdit
}

func (c *terrainBatchCmd) Label() string { return c.label }
func (c *terrainBatchCmd) Apply(s *Session) error {
	return applyTerrainEditsLocked(s, c.edits, false)
}
func (c *terrainBatchCmd) Revert(s *Session) error {
	return applyTerrainEditsLocked(s, c.edits, true)
}
func (c *terrainBatchCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "terrain", ID: 0, Field: "brush"}}
}

// applyTerrainEditsLocked writes the new (or old, when revert) value of each
// edit into the live terrain. Caller MUST hold s.mu. Flips dirtyTerrain so a
// post-undo Save still persists the reverted state. Out-of-range indices are
// skipped defensively (terrain dims don't change under these brushes, so this
// never fires in practice).
func applyTerrainEditsLocked(s *Session, edits []terrainCornerEdit, revert bool) error {
	if s.terrain == nil {
		return fmt.Errorf("no terrain loaded")
	}
	n := len(s.terrain.Tiles)
	for _, e := range edits {
		if e.idx < 0 || e.idx >= n {
			continue
		}
		v := e.newVal
		if revert {
			v = e.oldVal
		}
		tp := &s.terrain.Tiles[e.idx]
		switch e.field {
		case fieldTile:
			tp.GroundTexture = uint8(v)
		case fieldHeightRaw:
			tp.GroundHeight = int16(v)
		case fieldLayer:
			tp.LayerHeight = uint8(v)
		case fieldCliffTex:
			tp.CliffTexture = uint8(v)
		case fieldFlags:
			tp.Flags = uint8(v)
		}
	}
	s.dirtyTerrain = true
	return nil
}

// terrainBrushCornersLocked returns the linear tile indices of every corner
// within `radius` of (centerCol,centerRow), clipped to the grid. shape "square"
// uses chebyshev distance (a (2r+1)² block); anything else (default "circle")
// uses euclidean distance. Caller MUST hold s.mu. Returns nil if no terrain is
// loaded or the center is off-grid.
func (s *Session) terrainBrushCornersLocked(centerCol, centerRow, radius int, shape string) []int {
	if s.terrain == nil {
		return nil
	}
	w := int(s.terrain.Width)
	h := int(s.terrain.Height)
	if centerCol < 0 || centerCol >= w || centerRow < 0 || centerRow >= h {
		return nil
	}
	if radius < 0 {
		radius = 0
	}
	square := shape == "square"
	// Circle threshold uses (radius+0.5)² so small brushes are round rather
	// than star-shaped: 4·(dx²+dy²) ≤ (2·radius+1)² is the integer-exact form.
	// radius 0 → just the center, radius 1 → a full 3×3, radius 2 → 21 corners.
	diam := 2*radius + 1
	threshold := diam * diam
	out := make([]int, 0, diam*diam)
	for dy := -radius; dy <= radius; dy++ {
		row := centerRow + dy
		if row < 0 || row >= h {
			continue
		}
		for dx := -radius; dx <= radius; dx++ {
			col := centerCol + dx
			if col < 0 || col >= w {
				continue
			}
			if !square && 4*(dx*dx+dy*dy) > threshold {
				continue
			}
			out = append(out, row*w+col)
		}
	}
	return out
}

// commitTerrainBrushLocked records a compound command for the given edits and
// returns whether history changed (so the caller can fire notifications after
// unlocking). No-op (returns the command nil) when edits is empty so a dab that
// changed nothing doesn't flip the Save pill or pollute history. Caller MUST
// hold s.mu and is responsible for the post-unlock notifications.
func (s *Session) commitTerrainBrushLocked(label string, edits []terrainCornerEdit) (historyChanged bool) {
	if len(edits) == 0 {
		return false
	}
	s.dirtyTerrain = true
	s.recordCommand(&terrainBatchCmd{label: label, edits: edits})
	return s.groupDepth == 0
}

// finishTerrainBrush fires the post-unlock notifications shared by every brush
// mutator: dirty pill (if it just flipped), one terrain entity-changed event,
// and history-changed when the edit landed outside a group. Call AFTER s.mu is
// released.
func (s *Session) finishTerrainBrush(wasDirty, changed, historyChanged bool) {
	if !changed {
		return
	}
	if !wasDirty {
		s.notifyDirty(true)
	}
	s.notifyEntityChanged(EntityChange{Kind: "terrain", ID: 0, Field: "brush"})
	if historyChanged {
		s.notifyHistoryChanged()
	}
}

// PaintTileBrush sets the ground tile of every corner in the brush footprint to
// groundTileID (a FourCC already in the map's ground palette). One undo step,
// one re-render. No-op corners (already on that tile) are skipped.
func (s *Session) PaintTileBrush(centerCol, centerRow, radius int, shape, groundTileID string) error {
	s.mu.Lock()
	corners := s.terrainBrushCornersLocked(centerCol, centerRow, radius, shape)
	if corners == nil {
		s.mu.Unlock()
		return fmt.Errorf("brush center (%d,%d) out of range or no terrain loaded", centerCol, centerRow)
	}
	newIdx, err := s.resolveGroundPaletteIndexLocked(groundTileID)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	wasDirty := s.anyDirtyLocked()
	edits := make([]terrainCornerEdit, 0, len(corners))
	for _, idx := range corners {
		old := s.terrain.Tiles[idx].GroundTexture
		if old == newIdx {
			continue
		}
		s.terrain.Tiles[idx].GroundTexture = newIdx
		edits = append(edits, terrainCornerEdit{idx: idx, field: fieldTile, oldVal: int32(old), newVal: int32(newIdx)})
	}
	historyChanged := s.commitTerrainBrushLocked("Paint terrain", edits)
	s.mu.Unlock()
	s.finishTerrainBrush(wasDirty, len(edits) > 0, historyChanged)
	return nil
}

// HeightBrush adjusts the ground heightfield over the footprint. mode:
//
//	"raise"   — add strength (game-Z units) to each corner.
//	"lower"   — subtract strength.
//	"flatten" — set every corner to `target` (game-Z); WC3's plateau tool.
//	"smooth"  — move each corner toward the average of its 4-neighbors by
//	            strength fraction (strength clamped to [0,1]; default 0.5).
//
// strength<=0 falls back to defaultHeightStep for raise/lower and 0.5 for
// smooth. Heights are quantized to the .w3e int16 grid; corners that don't
// actually move are skipped. One undo step, one re-render.
func (s *Session) HeightBrush(centerCol, centerRow, radius int, shape, mode string, strength, target float32) error {
	s.mu.Lock()
	corners := s.terrainBrushCornersLocked(centerCol, centerRow, radius, shape)
	if corners == nil {
		s.mu.Unlock()
		return fmt.Errorf("brush center (%d,%d) out of range or no terrain loaded", centerCol, centerRow)
	}
	w := int(s.terrain.Width)
	h := int(s.terrain.Height)
	wasDirty := s.anyDirtyLocked()
	edits := make([]terrainCornerEdit, 0, len(corners))
	for _, idx := range corners {
		oldRaw := s.terrain.Tiles[idx].GroundHeight
		var newZ float32
		switch mode {
		case "lower":
			step := strength
			if step <= 0 {
				step = defaultHeightStep
			}
			newZ = s.terrain.Tiles[idx].Z() - step
		case "flatten":
			newZ = target
		case "smooth":
			frac := strength
			if frac <= 0 {
				frac = 0.5
			}
			if frac > 1 {
				frac = 1
			}
			avg := neighborAvgZLocked(s, idx, w, h)
			cur := s.terrain.Tiles[idx].Z()
			newZ = cur + (avg-cur)*frac
		default: // "raise"
			step := strength
			if step <= 0 {
				step = defaultHeightStep
			}
			newZ = s.terrain.Tiles[idx].Z() + step
		}
		newRaw := gameZToRawHeight(newZ)
		if newRaw == oldRaw {
			continue
		}
		s.terrain.Tiles[idx].GroundHeight = newRaw
		edits = append(edits, terrainCornerEdit{idx: idx, field: fieldHeightRaw, oldVal: int32(oldRaw), newVal: int32(newRaw)})
	}
	historyChanged := s.commitTerrainBrushLocked("Edit terrain height", edits)
	s.mu.Unlock()
	s.finishTerrainBrush(wasDirty, len(edits) > 0, historyChanged)
	return nil
}

// neighborAvgZLocked returns the average game-Z of the up-to-4 orthogonal
// neighbors of corner idx (falling back to the corner itself at the grid edge),
// used by the smooth brush. Caller MUST hold s.mu.
func neighborAvgZLocked(s *Session, idx, w, h int) float32 {
	col := idx % w
	row := idx / w
	var sum float32
	var cnt int
	add := func(c, r int) {
		if c < 0 || c >= w || r < 0 || r >= h {
			return
		}
		sum += s.terrain.Tiles[r*w+c].Z()
		cnt++
	}
	add(col-1, row)
	add(col+1, row)
	add(col, row-1)
	add(col, row+1)
	if cnt == 0 {
		return s.terrain.Tiles[idx].Z()
	}
	return sum / float32(cnt)
}

// CliffBrush adjusts the integer cliff LayerHeight over the footprint. mode:
//
//	"raise" — LayerHeight += 1 (clamped to 15).
//	"lower" — LayerHeight -= 1 (clamped to 0).
//	"set"   — LayerHeight = level (clamped to [0,15]).
//
// When a corner ends up on a cliff (its layer differs from a neighbor the
// renderer will wall off) its CliffTexture is set to the palette slot matching
// cliffTileID (or slot 0 when cliffTileID is empty / not found) so a wall
// actually renders — a corner left at CliffTexture 15 ("no cliff") renders a
// void. One undo step, one re-render.
func (s *Session) CliffBrush(centerCol, centerRow, radius int, shape, mode string, level int, cliffTileID string) error {
	s.mu.Lock()
	corners := s.terrainBrushCornersLocked(centerCol, centerRow, radius, shape)
	if corners == nil {
		s.mu.Unlock()
		return fmt.Errorf("brush center (%d,%d) out of range or no terrain loaded", centerCol, centerRow)
	}
	cliffTex := s.resolveCliffPaletteIndexLocked(cliffTileID)
	wasDirty := s.anyDirtyLocked()
	edits := make([]terrainCornerEdit, 0, len(corners)*2)
	for _, idx := range corners {
		oldLayer := int(s.terrain.Tiles[idx].LayerHeight)
		var newLayer int
		switch mode {
		case "lower":
			newLayer = oldLayer - 1
		case "set":
			newLayer = level
		default: // "raise"
			newLayer = oldLayer + 1
		}
		if newLayer < 0 {
			newLayer = 0
		}
		if newLayer > maxLayerHeight {
			newLayer = maxLayerHeight
		}
		if newLayer != oldLayer {
			s.terrain.Tiles[idx].LayerHeight = uint8(newLayer)
			edits = append(edits, terrainCornerEdit{idx: idx, field: fieldLayer, oldVal: int32(oldLayer), newVal: int32(newLayer)})
		}
		// Give every painted corner a valid cliff texture so a wall renders
		// where layers differ. Skipping corners whose texture is already set
		// keeps the edit list lean.
		oldTex := s.terrain.Tiles[idx].CliffTexture
		if oldTex != cliffTex {
			s.terrain.Tiles[idx].CliffTexture = cliffTex
			edits = append(edits, terrainCornerEdit{idx: idx, field: fieldCliffTex, oldVal: int32(oldTex), newVal: int32(cliffTex)})
		}
	}
	historyChanged := s.commitTerrainBrushLocked("Edit cliff", edits)
	s.mu.Unlock()
	s.finishTerrainBrush(wasDirty, len(edits) > 0, historyChanged)
	return nil
}

// RampBrush toggles the ramp flag (Flags bit 0x1) over the footprint. With
// on=true the cliff step under the footprint renders as a walkable slope; with
// on=false it reverts to a sheer wall. One undo step, one re-render.
func (s *Session) RampBrush(centerCol, centerRow, radius int, shape string, on bool) error {
	s.mu.Lock()
	corners := s.terrainBrushCornersLocked(centerCol, centerRow, radius, shape)
	if corners == nil {
		s.mu.Unlock()
		return fmt.Errorf("brush center (%d,%d) out of range or no terrain loaded", centerCol, centerRow)
	}
	wasDirty := s.anyDirtyLocked()
	edits := make([]terrainCornerEdit, 0, len(corners))
	for _, idx := range corners {
		oldFlags := s.terrain.Tiles[idx].Flags
		newFlags := oldFlags
		if on {
			newFlags |= 0x1
		} else {
			newFlags &^= 0x1
		}
		if newFlags == oldFlags {
			continue
		}
		s.terrain.Tiles[idx].Flags = newFlags
		edits = append(edits, terrainCornerEdit{idx: idx, field: fieldFlags, oldVal: int32(oldFlags), newVal: int32(newFlags)})
	}
	historyChanged := s.commitTerrainBrushLocked("Edit ramp", edits)
	s.mu.Unlock()
	s.finishTerrainBrush(wasDirty, len(edits) > 0, historyChanged)
	return nil
}

// resolveCliffPaletteIndexLocked maps a 4-char cliff FourCC to its slot index in
// the map's cliff palette, returning 0 when the id is empty or not found (so the
// cliff brush always has a renderable texture rather than the 15 "no cliff"
// sentinel). Caller MUST hold s.mu. Distinct from the ground-palette resolver:
// cliff edits should degrade gracefully to slot 0 instead of erroring, since the
// brush's job is to raise terrain, not to validate a tileset choice.
func (s *Session) resolveCliffPaletteIndexLocked(cliffTileID string) uint8 {
	if s.terrain == nil {
		return 0
	}
	if len(cliffTileID) == 4 {
		for i, id := range s.terrain.CliffTilesets {
			if id == cliffTileID && i <= maxLayerHeight {
				return uint8(i)
			}
		}
	}
	return 0
}
