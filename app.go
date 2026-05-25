package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"github.com/StephenSHorton/wc3-forge/internal/forge"
	"github.com/StephenSHorton/wc3-forge/internal/formats/doodadsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
	"github.com/StephenSHorton/wc3-forge/internal/wc3launch"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Wails event names emitted from Go → JS. Defined as constants so the
// frontend strings stay in sync with what's actually emitted.
const (
	eventSelectionChanged = "wc3-forge:selection-changed"
	eventMapChanged       = "wc3-forge:map-changed"
	eventDirtyChanged     = "wc3-forge:dirty-changed"
	eventEntityChanged    = "wc3-forge:entity-changed"
	eventDevSetAnim       = "wc3-forge:dev-set-anim"
)

// App is the Wails-bindable surface exposed to the frontend. Every method
// here becomes an awaitable JS function under wailsjs/go/main/App.
//
// App is intentionally thin: it shapes the Go-JS boundary (JSON-friendly
// types, no opaque pointers) and delegates to forge.Current. The MCP bridge
// handlers in internal/forge/handlers.go cover the same surface for AI
// clients — both routes converge on forge.Session.
type App struct {
	ctx context.Context
}

func NewApp() *App {
	return &App{}
}

// LogJS writes a JS-side message to wc3-forge.log next to the executable.
// Crude but reliable: GUI Wails apps have no console, so this is how
// frontend code surfaces diagnostics during early-bootstrap debugging.
func (a *App) LogJS(message string) {
	f, err := os.OpenFile("wc3-forge.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format("15:04:05.000"), message)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Forward the startup --camera spec (if any) to the frontend. App.svelte
	// listens for this event and applies the spec on the first map-load.
	if startupCameraSpec != "" {
		// Defer one tick so the frontend's EventsOn listener is registered by
		// the time we emit. runtime.EventsEmit doesn't queue for listeners
		// that haven't subscribed yet.
		go func() {
			// Wait until reasonable assumption that App.svelte's onMount has
			// registered EventsOn. Empirical: 500ms is comfortably enough
			// after Wails finishes JS bootstrapping.
			time.Sleep(500 * time.Millisecond)
			runtime.EventsEmit(a.ctx, "wc3-forge:startup-camera", map[string]any{
				"spec": startupCameraSpec,
			})
		}()
	}
	if startupPickSelfTest {
		// Fire after a longer delay than the camera spec so map-load + asset
		// streaming have had time to settle. The JS handler walks every
		// unit+doodad instance, projects its bound-center to canvas pixels,
		// calls rayPick at that pixel, and reports per-instance success/mismatch.
		go func() {
			time.Sleep(4 * time.Second)
			runtime.EventsEmit(a.ctx, "wc3-forge:pick-self-test", map[string]any{})
		}()
	}
	// Forward Go-side selection changes to the frontend.
	forge.Current.OnSelectionChanged(func(s forge.SelectionState) {
		runtime.EventsEmit(a.ctx, eventSelectionChanged, s)
	})
	// Forward map open/close from any source (App method OR bridge OR
	// --open flag at startup) to the frontend so it reloads.
	forge.Current.OnMapChanged(func(loaded bool) {
		runtime.EventsEmit(a.ctx, eventMapChanged, map[string]any{"loaded": loaded})
	})
	// Forward dirty-state changes (MoveUnit edits, Save flushes) so the UI
	// can keep its modified-dot + Save-button enable state in sync without
	// polling. Payload mirrors the map-changed shape: { dirty: bool }.
	//
	// Also reflect dirty state in the OS-visible Wails window title. The JS
	// side's document.title only updates the inner WebView2 child window,
	// which the user never sees; runtime.WindowSetTitle hits the outer Wails
	// window so the taskbar + window-list entry pick up the `* ` prefix too.
	forge.Current.OnDirtyChanged(func(dirty bool) {
		runtime.EventsEmit(a.ctx, eventDirtyChanged, map[string]any{"dirty": dirty})
		title := "wc3-forge"
		if dirty {
			title = "* wc3-forge"
		}
		runtime.WindowSetTitle(a.ctx, title)
	})
	// Forward entity-change notifications (MoveUnit, future SetRotation/etc.)
	// so two stale views can refresh in lockstep:
	//   - Properties panel re-fetches the primary entity to repaint inputs
	//   - Scene re-positions/re-rotates/etc. the rendered model
	// The payload rides the new position (for Field=="position") so the
	// scene subscriber doesn't need a follow-up fetch — see EntityChange.
	// Wails marshals the struct via its json tags; the JS side reads
	// camel-case-not (the tags are lowercase: kind/id/field/position).
	forge.Current.OnEntityChanged(func(c forge.EntityChange) {
		runtime.EventsEmit(a.ctx, eventEntityChanged, c)
	})
}

// OpenMapDialog presents an OS folder picker and returns the selected
// directory, or "" if the user cancelled.
func (a *App) OpenMapDialog() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select an extracted .w3x folder",
	})
}

// MapStatus mirrors the wire shape used by map.status / bridge.ping.
type MapStatus struct {
	Loaded    bool   `json:"loaded"`
	Path      string `json:"path,omitempty"`
	Name      string `json:"name,omitempty"`
	UnitCount int    `json:"unit_count"`
}

// OpenMap loads the map at the given path. Returns the post-open status so
// the UI doesn't need a separate Status() roundtrip. The map-changed event
// fires automatically via the Session listener registered in startup —
// no explicit emit here.
func (a *App) OpenMap(path string) (MapStatus, error) {
	if path == "" {
		return MapStatus{}, fmt.Errorf("path is required")
	}
	if err := forge.Current.Open(path); err != nil {
		return MapStatus{}, err
	}
	return a.Status(), nil
}

func (a *App) CloseMap() MapStatus {
	forge.Current.Close()
	return a.Status()
}

func (a *App) Status() MapStatus {
	s := MapStatus{Loaded: forge.Current.IsLoaded()}
	if !s.Loaded {
		return s
	}
	s.Path = forge.Current.Path()
	if info := forge.Current.Info(); info != nil {
		s.Name = info.Name
	}
	if units := forge.Current.Units(); units != nil {
		s.UnitCount = len(units.Entities)
	}
	return s
}

// UnitDTO is the JS-facing unit shape. Subset of unitsdoo.Entity — the heavy
// fields (random data blobs, ability mods) get their own per-unit fetch later.
type UnitDTO struct {
	CreationNumber uint32     `json:"creation_number"`
	TypeID         string     `json:"type_id"`
	SkinID         string     `json:"skin_id"`
	Player         uint32     `json:"player"`
	Position       [3]float32 `json:"position"`
	Rotation       float32    `json:"rotation"`
	Scale          [3]float32 `json:"scale"`
	HitPointsPct   int32      `json:"hit_points_pct"`
	ManaPct        int32      `json:"mana_pct"`
	HeroLevel      uint32     `json:"hero_level"`
	GoldAmount     uint32     `json:"gold_amount"`
}

func (a *App) ListUnits() []UnitDTO {
	units := forge.Current.Units()
	if units == nil {
		return []UnitDTO{}
	}
	out := make([]UnitDTO, 0, len(units.Entities))
	for _, e := range units.Entities {
		out = append(out, UnitDTO{
			CreationNumber: e.CreationNumber,
			TypeID:         e.TypeID,
			SkinID:         e.SkinID,
			Player:         e.Player,
			Position:       e.Position,
			Rotation:       e.Rotation,
			Scale:          e.Scale,
			HitPointsPct:   e.HitPointsPct,
			ManaPct:        e.ManaPct,
			HeroLevel:      e.HeroLevel,
			GoldAmount:     e.GoldAmount,
		})
	}
	return out
}

// TerrainDTO is the JS-facing terrain shape. Heights + ground-texture indices
// as parallel slices for compact transfer (typed arrays on the JS side).
// Vertex (col, row) is at index row*Width + col, row 0 = bottom.
type TerrainDTO struct {
	Width        uint32     `json:"width"`         // vertex count along X
	Height       uint32     `json:"height"`        // vertex count along Y
	CenterOffset [2]float32 `json:"center_offset"` // game coords of vertex (0,0); bottom-left
	Heights      []float32  `json:"heights"`       // length = Width*Height, game-space Z
	// GroundTex is uint32 (not uint8/byte) so encoding/json emits a real
	// JSON array. Go's `encoding/json` special-cases []byte → base64 string,
	// which would silently turn each tile index into an ASCII character on
	// the JS side. Wire cost is tiny at these sizes.
	GroundTex []uint32 `json:"ground_tex"`  // length = Width*Height, palette idx 0..63
	Tileset   string   `json:"tileset"`     // single letter, e.g. "L" (Lordaeron)
	Palette   []string `json:"palette"`     // ground tileset FourCCs (palette key)
	// PaletteColors is one RGB triplet (0..255) per palette entry, sampled
	// from the actual WC3 tileset BLP/DDS via Terrain.slk. Lets the JS
	// renderer color tiles by their real texture average without having to
	// decode BLP/DDS itself. parallel-indexed with Palette.
	PaletteColors [][3]uint8 `json:"palette_colors"`
	// PaletteTextures is one `dir/file` asset path stem per palette FourCC
	// (e.g. `terrainart/icecrown/ice_dirt`). JS appends `.dds` (or `.blp`
	// fallback via the assetHandler's BLP↔DDS swap) and loads via
	// viewer.load. Empty string if Terrain.slk lookup failed for that entry.
	// parallel-indexed with Palette.
	PaletteTextures []string `json:"palette_textures"`
	// Per-corner cliff data. LayerHeight is the integer cliff step (0..15),
	// CliffTex is the index into CliffPalette (0..15; 15 commonly means
	// "no cliff" at this corner). CliffVar is the high 3 bits of TextureDetails
	// (per-corner variation), used in the cliff-mesh filename.
	// RampFlags packs: bit 0 = ramp, bit 1 = boundary.
	//
	// uint32 (not uint8) for the same reason as GroundTex above — Go's
	// encoding/json base64-encodes []byte/[]uint8.
	LayerHeight  []uint32 `json:"layer_height"`
	CliffTex     []uint32 `json:"cliff_tex"`
	CliffVar     []uint32 `json:"cliff_var"`
	// GroundVar is the per-corner ground variation index (0..31, low 5 bits
	// of TextureDetails). Picks the sub-tile within the palette texture's
	// 4×4 atlas; for "extended" textures (width = 2×height) the variation
	// indexes into the variation half (slots 16..31). HiveWE convention:
	//   non-extended: variation==0 → slot 0, else slot 15
	//   extended    : variation 0..15 → slot 16+v, 16 → 15, else 0
	GroundVar    []uint32 `json:"ground_var"`
	RampFlags    []uint32 `json:"ramp_flags"`
	CliffPalette []string `json:"cliff_palette"` // cliff tileset FourCCs
	// CliffPaletteTextures is one `texdir/texfile` asset path stem per cliff
	// palette FourCC, sourced from TerrainArt/CliffTypes.slk. JS appends
	// `.dds` (or `.blp` fallback) and loads via viewer.load. Empty string when
	// the SLK lookup failed. Parallel-indexed with CliffPalette. Used by the
	// custom cliff vertex/fragment shader (cliff-shader.ts), since HiveWE's
	// `vOffset.a = corner_cliff_texture` indexes into a 2D-array-texture of
	// these (one layer per palette); WebGL1 lacks sampler2DArray so we bind
	// them per-palette.
	CliffPaletteTextures []string `json:"cliff_palette_textures"`
	// Per-corner water data. WaterZ is the per-vertex water surface
	// elevation in studs (the per-tileset WaterInfo.Offset gets added
	// JS-side because Offset comes from Water.slk and lives with the
	// other rendering constants). HasWater is the per-vertex flag bit:
	// any cell whose 4 corners include at least one HasWater=1 gets a
	// water quad in the JS water mesh.
	WaterZ    []float32  `json:"water_z"`
	HasWater  []uint32   `json:"has_water"`
	Water     WaterInfo  `json:"water"`
	// Static shadow map (war3map.shd). 4× resolution per terrain tile —
	// dimensions are (Width-1)*4 × (Height-1)*4. Empty array when the map
	// doesn't ship a war3map.shd. Encoded as base64 by Wails since it's
	// []byte; JS decodes it (Uint8Array.from(atob(s), c => c.charCodeAt(0)))
	// before uploading to a GL texture. We deliberately keep the []byte
	// type — at 320KB for a typical map, the base64 wire is ~430KB which
	// is fine; expanding to []uint32 quadruples that for no benefit since
	// JS needs Uint8Array for gl.texImage2D anyway.
	ShadowMap       []byte `json:"shadow_map"`
	ShadowMapWidth  int    `json:"shadow_map_width"`
	ShadowMapHeight int    `json:"shadow_map_height"`
	// CellSkip is a per-cell flag (1 = skip terrain quad). Length =
	// (Width-1)*(Height-1), row-major (cell (i,j) at index j*(Width-1)+i).
	// Mirrors HiveWE's `gpu_ground_exists_data == 0` test — a cell is
	// "skipped" when it's a cliff cell (any 4-corner layer_height mismatch)
	// AND NOT a ramp entrance (all 4 corners ramp-flagged with non-
	// symmetric layer-height diagonals). On skipped cells, the cliff MDX
	// alone covers the vertical face; rendering the terrain quad there
	// produces diagonal slopes that poke through the cliff geometry —
	// the bug Problem 1 fixes. Stored as []uint32 for the same
	// base64-marshal-trap reason as GroundTex.
	CellSkip []uint32 `json:"cell_skip"`
	// CliffDisplacement is the per-corner SMOOTH height (just `Tilepoint.Z()`,
	// in studs) — i.e. the per-corner wobble WITHOUT the layer-step offset.
	// This is what the cliff vertex shader's per-vertex displacement consumes.
	//
	// CRITICAL: this is NOT the same as `Heights` (which is FinalZ = Z() +
	// (layer-2)*128). The cliff MDX geometry ALREADY encodes the full layer-step
	// height in its vertex Z range (a 1-step cliff MDX has vertices spanning
	// vPos.z = 0..128 studs). If we displaced cliffs by the full FinalZ, the
	// layer-step contribution would be applied TWICE (once via MDX geometry,
	// once via shader displacement) producing visibly 2×-tall cliff walls.
	//
	// This matches HiveWE's binding split (terrain.ixx:613 vs :652): the
	// terrain shader's SSBO binding 0 is `cliff_level_buffer` (full
	// FinalGroundHeights), but the cliff shader's SSBO binding 0 is
	// `ground_height_buffer` (CORNER_HEIGHT only — the wobble). HiveWE's cliff
	// shader pulls the layer-step contribution from `vOffset.z = min_layer - 2`
	// and from each MDX vertex's own Z; the per-corner displacement adds ONLY
	// the small wobble adjustment.
	//
	// Stored as parallel-indexed with `Heights` (length = Width*Height,
	// row-major); each entry is the corresponding Tilepoint's `Z()` in studs.
	CliffDisplacement []float32 `json:"cliff_displacement"`
}

// GetTerrain returns the terrain grid for rendering. Empty if no map loaded or
// the map has no .w3e file (older maps sometimes lack one).
func (a *App) GetTerrain() (TerrainDTO, error) {
	t := forge.Current.Terrain()
	if t == nil {
		return TerrainDTO{}, fmt.Errorf("no terrain loaded")
	}
	W := int(t.Width)
	H := int(t.Height)
	n := W * H
	heights := make([]float32, n)
	cliffDisp := make([]float32, n) // per-corner SMOOTH height (Z() only), in studs
	ground := make([]uint32, n)
	layer := make([]uint32, n)
	cliffTex := make([]uint32, n)
	cliffVar := make([]uint32, n)
	groundVar := make([]uint32, n)
	rampFlags := make([]uint32, n)
	waterZ := make([]float32, n)
	hasWater := make([]uint32, n)
	for i := 0; i < n; i++ {
		// FinalZ includes the layer_height contribution; the terrain mesh
		// renders corners at this world Z directly.
		heights[i] = t.Tiles[i].FinalZ()
		// CliffDisplacement is the per-corner SMOOTH height (just the wobble,
		// no layer-step), in studs. Used by the cliff vertex shader's
		// per-vertex displacement. HiveWE binds CORNER_HEIGHT (smooth) to the
		// cliff shader (terrain.ixx:652), NOT FinalGroundHeights — because the
		// cliff MDX geometry already encodes the layer-step rise in its
		// vertex Z values, so adding (layer-2)*128 again would double it.
		cliffDisp[i] = t.Tiles[i].Z()
		ground[i] = uint32(t.Tiles[i].GroundTexIdx())
		layer[i] = uint32(t.Tiles[i].LayerHeight)
		cliffTex[i] = uint32(t.Tiles[i].CliffTexIdx())
		cliffVar[i] = uint32((t.Tiles[i].TextureDetails >> 5) & 0x07)
		groundVar[i] = uint32(t.Tiles[i].TextureDetails & 0x1F)
		var rf uint32
		if t.Tiles[i].HasRamp() {
			rf |= 0x01
		}
		if t.Tiles[i].Boundary {
			rf |= 0x02
		}
		rampFlags[i] = rf
		waterZ[i] = t.Tiles[i].WaterZ()
		if t.Tiles[i].HasWater() {
			hasWater[i] = 1
		}
	}

	// HiveWE-compat: replace per-corner ground texture for cliff/ramp corners.
	//
	// .w3e stores `ground_texture` per corner verbatim — usually a sensible
	// "what's underneath the cliff" value, but on mixed-tileset maps (where
	// cliffs and floors come from different tilesets) it's often a stale or
	// editor-default value. HiveWE's `real_tile_texture` therefore overrides
	// the stored value with `cliff_to_ground_texture[cliff_index]` whenever
	// the corner sits on a cliff or ramp. The mapping comes from
	// TerrainArt/CliffTypes.slk's `groundtile` column (e.g. CIrb → Irbk).
	//
	// Symptom this fixes (verified vs HiveWE on Enfo's FFB v2.64f): shield
	// borders and tower edges stored as Cityscape `Ybtl` (tan brick) but
	// adjacent to Icecrown cliffs render with `Irbk` (Ice_RuneBricks blue-gray
	// stone) in HiveWE. Without the override wc3-forge showed the tan, which
	// looked wildly out of place vs the Icecrown floor.
	//
	// Single-tileset maps (the Lordaeron-Summer-only survival test fixture)
	// were unaffected because the cliff's groundtile FourCC is already the
	// floor's FourCC there — the override is a no-op.
	//
	// We compute corner_cliff per HiveWE: a cell's BL corner is "cliff" when
	// any of its 4 corners has a different layer_height than BL. Then a corner
	// is "on a cliff" when ANY of the cells it touches (at most 4 — itself
	// plus left, bottom, and bottom-left neighbors) is a cliff cell. Same
	// 4-cell window HiveWE walks in real_tile_texture.
	c2g := CliffToGroundTexture(t.CliffTilesets, t.GroundTilesets)
	cellCliff := make([]bool, n) // per-cell flag indexed by BL corner; valid for i<W-1 && j<H-1
	for j := 0; j < H-1; j++ {
		for i := 0; i < W-1; i++ {
			bl := j*W + i
			lhBL := t.Tiles[bl].LayerHeight
			if t.Tiles[bl+1].LayerHeight != lhBL ||
				t.Tiles[bl+W].LayerHeight != lhBL ||
				t.Tiles[bl+W+1].LayerHeight != lhBL {
				cellCliff[bl] = true
			}
		}
	}

	// Compute per-corner `corner_romp` flag — strict HiveWE port from
	// terrain.ixx::update_cliff_meshes (lines 1083-1187). A corner is "romp"
	// iff it sits on a ramp clifftrans piece that wc3-forge will actually
	// render. The strict rule is needed for the cell-skip pass below to match
	// HiveWE's update_ground_exists exactly.
	//
	// Two passes: vertical and horizontal ramps. Each spans 2 cells; HiveWE
	// detects them by checking 3-corner-tall (vertical) or 3-corner-wide
	// (horizontal) ramp-flag patterns with the middle-row corners at the
	// min of the endpoints' layer_height.
	//
	// We don't have access to the clifftrans MDX file existence at this stage
	// — Blizzard ships all the standard variants, so we conservatively assume
	// every spanning ramp gets a clifftrans. If the rare missing variant
	// shows up, that single ramp endpoint will show a small visible void;
	// acceptable tradeoff vs replicating the asset-existence check on the
	// Go side.
	cornerRomp := make([]bool, n)
	// Vertical ramps — span (i, j) and (i, j+1)
	for j := 0; j < H-2; j++ {
		for i := 0; i < W-1; i++ {
			bl := j*W + i
			br := bl + 1
			tl := bl + W
			tr := bl + W + 1
			ttl := bl + 2*W
			ttr := bl + 2*W + 1
			lhBL := int(t.Tiles[bl].LayerHeight)
			lhBR := int(t.Tiles[br].LayerHeight)
			lhTL := int(t.Tiles[tl].LayerHeight)
			lhTR := int(t.Tiles[tr].LayerHeight)
			lhTTL := int(t.Tiles[ttl].LayerHeight)
			lhTTR := int(t.Tiles[ttr].LayerHeight)
			ae := lhBL
			if lhTTL < ae {
				ae = lhTTL
			}
			cf := lhBR
			if lhTTR < cf {
				cf = lhTTR
			}
			if lhTL != ae || lhTR != cf {
				continue
			}
			rBL := t.Tiles[bl].HasRamp()
			rTL := t.Tiles[tl].HasRamp()
			rTTL := t.Tiles[ttl].HasRamp()
			rBR := t.Tiles[br].HasRamp()
			rTR := t.Tiles[tr].HasRamp()
			rTTR := t.Tiles[ttr].HasRamp()
			if rBL != rTL || rBL != rTTL || rBR != rTR || rBR != rTTR || rBL == rBR {
				continue
			}
			// Flat-ramp filter: all four spanning corners at same layer ⇒
			// degenerate AAAA-shape clifftrans that Blizzard doesn't ship.
			if lhBL == lhTTL && lhBL == lhBR && lhBL == lhTTR {
				continue
			}
			cornerRomp[bl] = true
			cornerRomp[tl] = true
		}
	}
	// Horizontal ramps — span (i, j) and (i+1, j)
	for j := 0; j < H-1; j++ {
		for i := 0; i < W-2; i++ {
			bl := j*W + i
			br := bl + 1
			brr := bl + 2
			tl := bl + W
			tr := bl + W + 1
			trr := bl + W + 2
			lhBL := int(t.Tiles[bl].LayerHeight)
			lhBR := int(t.Tiles[br].LayerHeight)
			lhBRR := int(t.Tiles[brr].LayerHeight)
			lhTL := int(t.Tiles[tl].LayerHeight)
			lhTR := int(t.Tiles[tr].LayerHeight)
			lhTRR := int(t.Tiles[trr].LayerHeight)
			ae := lhBL
			if lhBRR < ae {
				ae = lhBRR
			}
			bf := lhTL
			if lhTRR < bf {
				bf = lhTRR
			}
			if lhBR != ae || lhTR != bf {
				continue
			}
			rBL := t.Tiles[bl].HasRamp()
			rBR := t.Tiles[br].HasRamp()
			rBRR := t.Tiles[brr].HasRamp()
			rTL := t.Tiles[tl].HasRamp()
			rTR := t.Tiles[tr].HasRamp()
			rTRR := t.Tiles[trr].HasRamp()
			if rBL != rBR || rBL != rBRR || rTL != rTR || rTL != rTRR || rBL == rTL {
				continue
			}
			if lhBL == lhBRR && lhBL == lhTL && lhBL == lhTRR {
				continue
			}
			cornerRomp[bl] = true
			cornerRomp[br] = true
		}
	}

	// Compute per-cell "skip terrain quad" mask — STRICT HiveWE port of
	// update_ground_exists (terrain.ixx line 1043-1055):
	//   gpu_ground_exists[idx] = !(((corner_cliff[idx] || corner_romp[idx])
	//                                && !is_corner_ramp_entrance(i, j))
	//                              || corner_special_doodad[idx])
	//
	// (We don't track corner_special_doodad — its consumers are rare and the
	// data isn't surfaced in our doodadsdoo parser yet.)
	//
	// Now safe to use the strict rule because the new cliff-shader.ts
	// per-vertex displaces the cliff MDX to match `final_heights[corner]`,
	// matching HiveWE's cliff.vert line 35. The cliff/ramp meshes fully
	// cover the cells we skip; no pragmatic "keep any ramp corner" widening
	// needed (that was a978b4b's workaround for missing displacement).
	numCells := (W - 1) * (H - 1)
	cellSkip := make([]uint32, numCells)
	for j := 0; j < H-1; j++ {
		for i := 0; i < W-1; i++ {
			bl := j*W + i
			isCliff := cellCliff[bl]
			isRomp := cornerRomp[bl]
			if !isCliff && !isRomp {
				continue
			}
			br := bl + 1
			tl := bl + W
			tr := tl + 1
			rBL := t.Tiles[bl].HasRamp()
			rBR := t.Tiles[br].HasRamp()
			rTL := t.Tiles[tl].HasRamp()
			rTR := t.Tiles[tr].HasRamp()
			allRamp := rBL && rBR && rTL && rTR
			isRampEntrance := allRamp && !(t.Tiles[bl].LayerHeight == t.Tiles[tr].LayerHeight && t.Tiles[tl].LayerHeight == t.Tiles[br].LayerHeight)
			if !isRampEntrance {
				cellSkip[j*(W-1)+i] = 1
			}
		}
	}

	// Apply ramp-base +0.5-layer bump to heights, faithful port of
	// terrain.ixx::update_ground_heights lines 952-988. In HiveWE units the
	// bump is +0.5 layer-step; in our stud space one layer = 128 studs, so
	// the bump is +64 studs.
	//
	// For each cell whose all-four-corners are ramp AND non-symmetric, the
	// ramp-base corners (at the lower endpoint of the ramp's layer transition)
	// get their FinalZ lifted by +64 so the cliff vertex shader's lookup
	// returns a height that matches the actual ramp surface. Without this,
	// the cliff displacement assumes the corner sits at the lower layer
	// (which is geometrically the floor of the ramp, NOT the ramp surface),
	// and the ramp clifftrans MDX slope ends in mid-air at the ramp's base.
	//
	// The bump must be applied IDEMPOTENTLY (HiveWE iterates over multiple
	// tile-cells and writes the same corner index multiple times) — we use
	// direct assignment matching HiveWE's pattern.
	for j := 0; j < H-1; j++ {
		for i := 0; i < W-1; i++ {
			bl := j*W + i
			br := bl + 1
			tl := bl + W
			tr := bl + W + 1
			if !(t.Tiles[bl].HasRamp() && t.Tiles[tl].HasRamp() && t.Tiles[br].HasRamp() && t.Tiles[tr].HasRamp()) {
				continue
			}
			lhBL := t.Tiles[bl].LayerHeight
			lhBR := t.Tiles[br].LayerHeight
			lhTL := t.Tiles[tl].LayerHeight
			lhTR := t.Tiles[tr].LayerHeight
			if lhBL == lhTR && lhTL == lhBR {
				continue
			}
			base := lhBL
			if lhBR < base {
				base = lhBR
			}
			if lhTL < base {
				base = lhTL
			}
			if lhTR < base {
				base = lhTR
			}
			if lhBL == base {
				heights[bl] = t.Tiles[bl].Z() + (float32(lhBL)-2.0)*128.0 + 64.0
			}
			if lhBR == base {
				heights[br] = t.Tiles[br].Z() + (float32(lhBR)-2.0)*128.0 + 64.0
			}
			if lhTL == base {
				heights[tl] = t.Tiles[tl].Z() + (float32(lhTL)-2.0)*128.0 + 64.0
			}
			if lhTR == base {
				heights[tr] = t.Tiles[tr].Z() + (float32(lhTR)-2.0)*128.0 + 64.0
			}
		}
	}
	for j := 0; j < H; j++ {
		for i := 0; i < W; i++ {
			idx := j*W + i
			// Corner-touched cells: (i, j), (i-1, j), (i, j-1), (i-1, j-1).
			// Each is keyed by its BL corner index.
			anyCliff := false
			if i < W-1 && j < H-1 && cellCliff[idx] {
				anyCliff = true
			} else if i > 0 && j < H-1 && cellCliff[idx-1] {
				anyCliff = true
			} else if i < W-1 && j > 0 && cellCliff[idx-W] {
				anyCliff = true
			} else if i > 0 && j > 0 && cellCliff[idx-W-1] {
				anyCliff = true
			}
			// HiveWE's `a_romp` ORs `corner_romp[]`, NOT the raw per-corner
			// `corner_ramp` flag from war3map.w3e. `corner_romp` is set TRUE
			// only when a corner is part of an ACTUALLY-RENDERED ramp
			// clifftrans piece — much narrower than "any corner with the
			// ramp flag set."
			//
			// We previously used `t.Tiles[idx-N].HasRamp()` here (corner_ramp),
			// which over-fired the remap on plateau-interior corners adjacent
			// to ramp-flagged corners that are NOT part of a renderable ramp
			// span (e.g. the spear-tip plateau on Enfo's FFB at cells
			// (11..13, 67) — all flagged ramp=true but never matched by
			// HiveWE's vertical/horizontal ramp pre-pass because of the
			// `rBL != rBR` asymmetry requirement). The result: every plateau
			// corner touching one of those false-positive ramp corners got
			// remapped to Irbk, hiding the Itbk overlay that's supposed to
			// frame the cliff border with rune-brick. Symptom: cells (11,68)
			// and (12,68) rendered as flat Irbk in wc3-forge but as
			// Irbk-frame-with-Itbk-interior in HiveWE.
			//
			// Mirrors terrain.ixx::real_tile_texture line 755-768.
			anyRomp := cornerRomp[idx]
			if i > 0 && cornerRomp[idx-1] {
				anyRomp = true
			}
			if j > 0 && cornerRomp[idx-W] {
				anyRomp = true
			}
			if i > 0 && j > 0 && cornerRomp[idx-W-1] {
				anyRomp = true
			}
			if !anyRomp && !(anyCliff && !t.Tiles[idx].HasRamp()) {
				continue
			}
			// Ramp continuity fix: when THIS corner is itself a ramp corner,
			// leave its stored ground_texture intact instead of remapping to
			// the cliff palette's groundtile. This makes the ramp's top
			// surface inherit the surrounding floor's tile pattern.
			//
			// HiveWE's `real_tile_texture` (terrain.ixx line 770) does remap
			// every ramp-affected corner to `cliff_to_ground_texture[texture]`
			// — but in stock Blizzard maps the cliff palette's groundtile
			// happens to match the adjacent base floor (e.g. CIrb→Irbk, which
			// IS the typical Icecrown base tile), so the discontinuity is
			// invisible. Custom-tileset maps (like Enfo's FFB which mixes
			// Icecrown cliffs over a Cityscape brick floor) make the gap
			// obvious — the ramp top renders as a uniform tile of the cliff
			// palette's stand-in instead of continuing the surrounding
			// patterned brick.
			//
			// We preserve HiveWE's behavior for PURE-CLIFF corners (the
			// `anyCliff && !corner_ramp` branch entered this block — the
			// next-line `continue` keeps that path firing). We only skip the
			// remap when the corner is itself a ramp-flagged vertex, which
			// means the rendered cell here IS the ramp's top surface and
			// the user's expectation is that floor tiles continue across.
			if t.Tiles[idx].HasRamp() {
				continue
			}
			// Resolve the cliff palette index for this corner.
			// HiveWE comment: "Number 15 seems to be something" — when the
			// stored cliff_texture is 15, remap to (15-14) = 1. Mirrors the
			// `if (texture == 15) texture -= 14` in src/base/terrain.ixx.
			cti := t.Tiles[idx].CliffTexIdx()
			if cti == 15 {
				cti = 1
			}
			if int(cti) >= len(c2g) {
				continue
			}
			mapped := c2g[cti]
			if mapped < 0 {
				continue
			}
			ground[idx] = uint32(mapped)
		}
	}

	// Shadow map: optional. Empty + 0 dims when absent. The byte slice
	// rides as base64 (Go encoding/json's []byte default) — JS knows to
	// decode it before uploading as a GL texture.
	var shadowBytes []byte
	var shadowW, shadowH int
	if sm := forge.Current.ShadowMap(); sm != nil {
		shadowBytes = sm.Cells
		shadowW = sm.Width
		shadowH = sm.Height
	}
	return TerrainDTO{
		Width:         t.Width,
		Height:        t.Height,
		CenterOffset:  t.CenterOffset,
		Heights:       heights,
		GroundTex:     ground,
		Tileset:       string([]byte{t.Tileset}),
		Palette:         t.GroundTilesets,
		PaletteColors:   PaletteColors(t.GroundTilesets),
		PaletteTextures: PaletteTexturePaths(t.GroundTilesets),
		LayerHeight:     layer,
		CliffTex:        cliffTex,
		CliffVar:        cliffVar,
		GroundVar:       groundVar,
		RampFlags:       rampFlags,
		CliffPalette:         t.CliffTilesets,
		CliffPaletteTextures: CliffPaletteTexturePaths(t.CliffTilesets),
		WaterZ:        waterZ,
		HasWater:      hasWater,
		Water:         WaterColors(t.Tileset),
		ShadowMap:       shadowBytes,
		ShadowMapWidth:  shadowW,
		ShadowMapHeight: shadowH,
		CellSkip:          cellSkip,
		CliffDisplacement: cliffDisp,
	}, nil
}

// SelectionDTO mirrors forge.SelectionState for JS consumption.
type SelectionDTO struct {
	Items   []SelectionItemDTO `json:"items"`
	Primary int                `json:"primary"`
}

type SelectionItemDTO struct {
	Kind string `json:"kind"`
	ID   uint32 `json:"id"`
}

func (a *App) GetSelection() SelectionDTO {
	s := forge.Current.Selection()
	items := make([]SelectionItemDTO, len(s.Items))
	for i, it := range s.Items {
		items[i] = SelectionItemDTO{Kind: it.Kind, ID: it.ID}
	}
	return SelectionDTO{Items: items, Primary: s.Primary}
}

// SetSelection replaces the selection. Empty items clears.
func (a *App) SetSelection(items []SelectionItemDTO) SelectionDTO {
	converted := make([]forge.SelectionItem, len(items))
	for i, it := range items {
		converted[i] = forge.SelectionItem{Kind: it.Kind, ID: it.ID}
	}
	forge.Current.SetSelection(converted, len(converted)-1)
	return a.GetSelection()
}

// SelectUnit is a convenience for the common case: select a single unit by
// creation_number. Replaces any existing selection.
func (a *App) SelectUnit(creationNumber uint32) SelectionDTO {
	forge.Current.SetSelection([]forge.SelectionItem{{Kind: "unit", ID: creationNumber}}, 0)
	return a.GetSelection()
}

func (a *App) ClearSelection() SelectionDTO {
	forge.Current.SetSelection(nil, -1)
	return a.GetSelection()
}

// DoodadDTO is the JS-facing doodad shape (trimmed for list view, like UnitDTO).
type DoodadDTO struct {
	CreationNumber uint32     `json:"creation_number"`
	TypeID         string     `json:"type_id"`
	SkinID         string     `json:"skin_id"`
	Position       [3]float32 `json:"position"`
	Rotation       float32    `json:"rotation"`
	Scale          [3]float32 `json:"scale"`
	Variation      uint32     `json:"variation"`
	Life           uint8      `json:"life"` // 0..100 for destructibles; 0xFF = N/A
	Flags          uint8      `json:"flags"`
}

func (a *App) ListDoodads() []DoodadDTO {
	dd := forge.Current.Doodads()
	if dd == nil {
		return []DoodadDTO{}
	}
	out := make([]DoodadDTO, 0, len(dd.Doodads))
	for _, d := range dd.Doodads {
		out = append(out, DoodadDTO{
			CreationNumber: d.CreationNumber,
			TypeID:         d.TypeID,
			SkinID:         d.SkinID,
			Position:       d.Position,
			Rotation:       d.Rotation,
			Scale:          d.Scale,
			Variation:      d.Variation,
			Life:           d.Life,
			Flags:          d.Flags,
		})
	}
	return out
}

// GetDoodad returns the full Doodad for one creation_number.
func (a *App) GetDoodad(creationNumber uint32) (*doodadsdoo.Doodad, error) {
	dd := forge.Current.Doodads()
	if dd == nil {
		return nil, fmt.Errorf("no map loaded")
	}
	for i := range dd.Doodads {
		if dd.Doodads[i].CreationNumber == creationNumber {
			return &dd.Doodads[i], nil
		}
	}
	return nil, fmt.Errorf("no doodad with creation_number %d", creationNumber)
}

// GetMapBytes returns the raw .w3x bytes for the current map, base64-encoded
// for safe JSON transport. JS callers decode then pass to War3MapViewer's
// loadMap. Returns "" if the current map was opened from a folder (no
// archive bytes available) or no map is loaded.
//
// (Wails serializes []byte as base64 strings; the JS side does atob+Uint8Array
// to recover the buffer. That's intentional — base64 over the JS bridge
// performs fine at single-megabyte map sizes and avoids exposing raw
// bytes through the URL space.)
func (a *App) GetMapBytes() string {
	b := forge.Current.RawMapBytes()
	if len(b) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// SetUnitAnimation is a dev-only hook for poking unit animations from the JS
// devtools console (or any MCP client). Forwards (creationNumber, animName)
// to the frontend via Wails event; the scene-instances renderer matches the
// name against the unit's MDX sequences using the same rarity-weighted picker
// used for idle stand reroll. animName of "" or "stand" returns the unit to
// the idle reroll loop. Not exposed in the UI.
func (a *App) SetUnitAnimation(creationNumber uint32, animName string) {
	runtime.EventsEmit(a.ctx, eventDevSetAnim, map[string]any{
		"creation_number": creationNumber,
		"anim_name":       animName,
	})
}

// EmitTestCommand sends a free-form test command to the JS layer via a Wails
// event. Used by verification automation to drive UI state changes that
// would otherwise require synthetic input — which WebView2 silently drops in
// some cases (see project memory). The JS side subscribes to the event and
// dispatches per command. No-op if no subscriber is wired (event just goes
// nowhere; harmless).
//
// Commands are free-form strings parsed JS-side; conventional shape:
//   "terrain.toggle"               toggle terrain-pick mode
//   "terrain.set <col> <row>"      set terrain cell (auto-fills info)
//   "doodad.toggle <cat>"          toggle doodad category visibility
//   "section.toggle <id>"          toggle accordion section open state
//   "splitter.set <pct>"           set right-column explorer pct
func (a *App) EmitTestCommand(cmd string) {
	runtime.EventsEmit(a.ctx, "wc3-forge:test-command", map[string]any{
		"cmd": cmd,
	})
}


// PathingMapDTO is the JS-facing pathing-map shape. Cells carry one byte
// per quarter-cell (4× terrain tile resolution); each byte's bits flag
// movement constraints (unwalkable / unflyable / unbuildable / …).
//
// Cells is []uint32 (not []uint8) because Go's encoding/json base64-encodes
// []byte fields. The same trap caught us with TerrainDTO.GroundTex — see
// project memory `project-wc3-forge`. Wire cost at typical sizes (~256²) is
// 256 KB which is fine for a JSON array; the JS side packs it back into a
// Uint8Array before uploading to a GL texture.
type PathingMapDTO struct {
	Width  int      `json:"width"`
	Height int      `json:"height"`
	Cells  []uint32 `json:"cells"`
}

// GetPathingMap returns the parsed war3map.wpm for the loaded map, or an
// empty DTO (width=height=0, nil cells) when the map didn't ship a .wpm
// — the renderer treats that as "no overlay to draw".
func (a *App) GetPathingMap() PathingMapDTO {
	p := forge.Current.PathingMap()
	if p == nil {
		return PathingMapDTO{}
	}
	cells := make([]uint32, len(p.Cells))
	for i, b := range p.Cells {
		cells[i] = uint32(b)
	}
	return PathingMapDTO{Width: p.Width, Height: p.Height, Cells: cells}
}

// GetUnit returns the full Entity for one creation_number. Properties panel
// uses this to render the heavier fields (inventory, ability mods, hero
// stats, item drops) that aren't included in the lighter ListUnits payload.
func (a *App) GetUnit(creationNumber uint32) (*unitsdoo.Entity, error) {
	units := forge.Current.Units()
	if units == nil {
		return nil, fmt.Errorf("no map loaded")
	}
	for i := range units.Entities {
		if units.Entities[i].CreationNumber == creationNumber {
			return &units.Entities[i], nil
		}
	}
	return nil, fmt.Errorf("no entity with creation_number %d", creationNumber)
}

// MoveUnit relocates the unit with the given creation_number to the supplied
// game coordinates. The Properties panel calls this on X/Y/Z edit-commit.
//
// Wire contract: incoming x/y/z are GAME coords (centered at 0,0); the
// unitsdoo parser stores Position verbatim so we pass them straight through.
// No conversion happens at this boundary.
func (a *App) MoveUnit(creationNumber uint32, x, y, z float32) error {
	return forge.Current.MoveUnit(creationNumber, x, y, z)
}

// MoveDoodad relocates the doodad with the given creation_number to the
// supplied game coordinates. Mirrors MoveUnit's wire contract and shape;
// the kind disambiguation lives in the Properties panel (which branches on
// the primary selection's kind before calling Move{Unit,Doodad}). The
// existing dirty-changed + entity-changed startup subscriptions fire for
// either kind, so no additional Wails wiring is needed here.
func (a *App) MoveDoodad(creationNumber uint32, x, y, z float32) error {
	return forge.Current.MoveDoodad(creationNumber, x, y, z)
}

// IsDirty reports whether the session has unsaved edits. The header Save
// button's modified-dot indicator polls this on mount; subsequent updates
// arrive via the dirty-changed Wails event.
func (a *App) IsDirty() bool {
	return forge.Current.IsDirty()
}

// SaveMap flushes all pending edits back through the source's write path.
// Returns ErrMPQWriteNotImplemented (wrapped) when the loaded map is backed
// by an MPQ archive — the UI should surface that as a friendly toast and
// suggest extracting to a folder.
func (a *App) SaveMap() error {
	return forge.Current.Save()
}

// LaunchInWC3 spawns Warcraft III with the currently-loaded map preloaded
// via the `-loadfile` CLI flag. Mirrors HiveWE's "Test Map" ribbon button.
// Returns immediately after spawning — WC3 runs as a detached peer process.
//
// v1 limitations (documented in detail in internal/wc3launch/launch.go):
//   - We do NOT save the session before launching. The JS caller MUST disable
//     the Test Map button when forge.Current.IsDirty() to avoid running a
//     stale .w3x. (Folder-backed maps could save first, but WC3 only runs
//     packaged .w3x files — wc3-forge has no MPQ writer yet, so the folder
//     contents can't be re-bundled.)
//   - Only file-backed sessions can be tested. Folder-backed sessions return
//     wc3launch.ErrMapNotFound (path is a folder). The JS caller can detect
//     this from the Status().path heuristic and disable the button.
//
// Returns:
//   - wc3launch.ErrBinaryNotFound when Warcraft III.exe isn't at the expected
//     install path (env WC3FORGE_WC3_PATH overrides; else
//     `C:\Program Files (x86)\Warcraft III\_retail_\x86_64\Warcraft III.exe`).
//   - wc3launch.ErrMapNotFound when the session path doesn't resolve to a
//     real .w3x file.
//   - a fmt.Errorf for "no map loaded" / exec failures.
func (a *App) LaunchInWC3() error {
	if !forge.Current.IsLoaded() {
		return fmt.Errorf("no map loaded")
	}
	mapPath := forge.Current.Path()
	if mapPath == "" {
		return fmt.Errorf("no map path available")
	}
	return wc3launch.LaunchWithMap(mapPath)
}

// MapInfoGet returns the full parsed war3map.w3i for the loaded map. The
// Map Info Editor dialog calls this on open to pre-populate every field;
// it's heavier than Status() (which intentionally exposes only the
// name/path/unit_count needed for the header chrome) and is shaped to be
// fed directly into form inputs.
//
// Returns an error when no map is loaded — the dialog gates open on
// IsLoaded() so this is a programmer-error path, not a normal flow.
func (a *App) MapInfoGet() (*w3i.Info, error) {
	info := forge.Current.Info()
	if info == nil {
		return nil, fmt.Errorf("no map loaded")
	}
	return info, nil
}

// MapInfoApply applies a partial update DTO to the in-memory war3map.w3i.
// The Map Info Editor dialog calls this on commit; it walks `updates` and
// applies each known key to the Info struct via Session.MutateInfo so
// dirty-tracking + entity-changed events fire normally.
//
// Subset coverage for v1: the dialog's Description/General tab fields.
// See forge.ApplyInfoUpdates for the wire-key contract + how to extend.
// Unknown keys are silently ignored; type-mismatches are silently skipped.
//
// Returns the MutateInfo error (e.g. no map loaded). Successful application
// of a 0-length updates map is a no-op (returns nil without flipping dirty).
func (a *App) MapInfoApply(updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	return forge.Current.MutateInfo(func(info *w3i.Info) {
		forge.ApplyInfoUpdates(info, updates)
	})
}
