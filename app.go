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
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Wails event names emitted from Go → JS. Defined as constants so the
// frontend strings stay in sync with what's actually emitted.
const (
	eventSelectionChanged = "wc3-forge:selection-changed"
	eventMapChanged       = "wc3-forge:map-changed"
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
	// Forward Go-side selection changes to the frontend.
	forge.Current.OnSelectionChanged(func(s forge.SelectionState) {
		runtime.EventsEmit(a.ctx, eventSelectionChanged, s)
	})
	// Forward map open/close from any source (App method OR bridge OR
	// --open flag at startup) to the frontend so it reloads.
	forge.Current.OnMapChanged(func(loaded bool) {
		runtime.EventsEmit(a.ctx, eventMapChanged, map[string]any{"loaded": loaded})
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
	RampFlags    []uint32 `json:"ramp_flags"`
	CliffPalette []string `json:"cliff_palette"` // cliff tileset FourCCs
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
}

// GetTerrain returns the terrain grid for rendering. Empty if no map loaded or
// the map has no .w3e file (older maps sometimes lack one).
func (a *App) GetTerrain() (TerrainDTO, error) {
	t := forge.Current.Terrain()
	if t == nil {
		return TerrainDTO{}, fmt.Errorf("no terrain loaded")
	}
	n := int(t.Width * t.Height)
	heights := make([]float32, n)
	ground := make([]uint32, n)
	layer := make([]uint32, n)
	cliffTex := make([]uint32, n)
	cliffVar := make([]uint32, n)
	rampFlags := make([]uint32, n)
	waterZ := make([]float32, n)
	hasWater := make([]uint32, n)
	for i := 0; i < n; i++ {
		// FinalZ includes the layer_height contribution; cliffs would otherwise
		// render as a flat slab. The JS terrain mesh + cliff transition logic
		// both depend on this combined Z.
		heights[i] = t.Tiles[i].FinalZ()
		ground[i] = uint32(t.Tiles[i].GroundTexIdx())
		layer[i] = uint32(t.Tiles[i].LayerHeight)
		cliffTex[i] = uint32(t.Tiles[i].CliffTexIdx())
		cliffVar[i] = uint32((t.Tiles[i].TextureDetails >> 5) & 0x07)
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
		Palette:       t.GroundTilesets,
		PaletteColors: PaletteColors(t.GroundTilesets),
		LayerHeight:   layer,
		CliffTex:      cliffTex,
		CliffVar:      cliffVar,
		RampFlags:     rampFlags,
		CliffPalette:  t.CliffTilesets,
		WaterZ:        waterZ,
		HasWater:      hasWater,
		Water:         WaterColors(t.Tileset),
		ShadowMap:       shadowBytes,
		ShadowMapWidth:  shadowW,
		ShadowMapHeight: shadowH,
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
