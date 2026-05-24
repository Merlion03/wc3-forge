package main

import (
	"context"
	"fmt"

	"github.com/StephenSHorton/wc3-forge/internal/forge"
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

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Forward Go-side selection changes to the frontend.
	forge.Current.OnSelectionChanged(func(s forge.SelectionState) {
		runtime.EventsEmit(a.ctx, eventSelectionChanged, s)
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
// the UI doesn't need a separate Status() roundtrip.
func (a *App) OpenMap(path string) (MapStatus, error) {
	if path == "" {
		return MapStatus{}, fmt.Errorf("path is required")
	}
	if err := forge.Current.Open(path); err != nil {
		return MapStatus{}, err
	}
	runtime.EventsEmit(a.ctx, eventMapChanged, map[string]any{"loaded": true})
	return a.Status(), nil
}

func (a *App) CloseMap() MapStatus {
	forge.Current.Close()
	runtime.EventsEmit(a.ctx, eventMapChanged, map[string]any{"loaded": false})
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
	GroundTex    []uint8    `json:"ground_tex"`    // length = Width*Height, palette idx 0..15
	Tileset      string     `json:"tileset"`       // single letter, e.g. "L" (Lordaeron)
	Palette      []string   `json:"palette"`       // ground tileset FourCCs (palette key)
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
	ground := make([]uint8, n)
	for i := 0; i < n; i++ {
		heights[i] = t.Tiles[i].Z()
		ground[i] = t.Tiles[i].GroundTexIdx()
	}
	return TerrainDTO{
		Width:        t.Width,
		Height:       t.Height,
		CenterOffset: t.CenterOffset,
		Heights:      heights,
		GroundTex:    ground,
		Tileset:      string([]byte{t.Tileset}),
		Palette:      t.GroundTilesets,
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
