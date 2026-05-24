package forge

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/StephenSHorton/wc3-forge/internal/bridge"
)

// RegisterAll wires every wc3-forge MCP handler into the bridge. Called once
// from main before Bridge.Start.
func RegisterAll(b *bridge.Bridge) {
	b.Register("bridge.ping", handlePing)
	b.Register("map.open", handleMapOpen)
	b.Register("map.close", handleMapClose)
	b.Register("map.status", handleMapStatus)
	b.Register("map.info_get", handleMapInfoGet)
	b.Register("units.list", handleUnitsList)
	b.Register("units.move", handleUnitsMove)
	b.Register("doodads.move", handleDoodadsMove)
	b.Register("map.save", handleMapSave)
	b.Register("selection.get", handleSelectionGet)
	b.Register("selection.set", handleSelectionSet)
	b.Register("selection.clear", handleSelectionClear)
}

type pingResponse struct {
	OK        bool   `json:"ok"`
	Version   string `json:"version"`
	MapLoaded bool   `json:"map_loaded"`
	MapName   string `json:"map_name,omitempty"`
	MapPath   string `json:"map_path,omitempty"`
}

func handlePing(_ json.RawMessage) (any, error) {
	r := pingResponse{
		OK:        true,
		Version:   bridge.Version,
		MapLoaded: Current.IsLoaded(),
	}
	if r.MapLoaded {
		r.MapPath = Current.Path()
		if info := Current.Info(); info != nil {
			r.MapName = info.Name
		}
	}
	return r, nil
}

type mapOpenParams struct {
	Path string `json:"path"`
}

func handleMapOpen(params json.RawMessage) (any, error) {
	var p mapOpenParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Path == "" {
		return nil, errors.New("path is required")
	}
	if err := Current.Open(p.Path); err != nil {
		return nil, err
	}
	return mapStatusResult(), nil
}

func handleMapClose(_ json.RawMessage) (any, error) {
	Current.Close()
	return map[string]any{"ok": true}, nil
}

func handleMapStatus(_ json.RawMessage) (any, error) {
	return mapStatusResult(), nil
}

type mapStatusResponse struct {
	Loaded   bool   `json:"loaded"`
	Path     string `json:"path,omitempty"`
	Name     string `json:"name,omitempty"`
	UnitCount int   `json:"unit_count,omitempty"`
}

func mapStatusResult() mapStatusResponse {
	r := mapStatusResponse{Loaded: Current.IsLoaded()}
	if !r.Loaded {
		return r
	}
	r.Path = Current.Path()
	if info := Current.Info(); info != nil {
		r.Name = info.Name
	}
	if units := Current.Units(); units != nil {
		r.UnitCount = len(units.Entities)
	}
	return r
}

func handleMapInfoGet(_ json.RawMessage) (any, error) {
	info := Current.Info()
	if info == nil {
		return nil, errors.New("no map loaded")
	}
	return info, nil
}

// unitsListResponse mirrors the C++ fork's shape: each entity is rendered with
// game-coordinate position + the core identity fields a tool needs. Skin/level/
// HP/mana/inventory included; the heavier ability-modifications + random-data
// blobs are returned only on units.get (TODO).
type unitsListEntity struct {
	CreationNumber uint32     `json:"creation_number"`
	TypeID         string     `json:"type_id"`
	SkinID         string     `json:"skin_id,omitempty"`
	Player         uint32     `json:"player"`
	Position       [3]float32 `json:"position"`
	Rotation       float32    `json:"rotation"`
	Scale          [3]float32 `json:"scale"`
	HitPointsPct   int32      `json:"hit_points_pct"`
	ManaPct        int32      `json:"mana_pct"`
	HeroLevel      uint32     `json:"hero_level,omitempty"`
	GoldAmount     uint32     `json:"gold_amount,omitempty"`
	Inventory      []invItem  `json:"inventory,omitempty"`
}

type invItem struct {
	Slot   uint32 `json:"slot"`
	ItemID string `json:"item_id"`
}

func handleSelectionGet(_ json.RawMessage) (any, error) {
	return Current.Selection(), nil
}

type selectionSetParams struct {
	Items []SelectionItem `json:"items"`
}

func handleSelectionSet(params json.RawMessage) (any, error) {
	var p selectionSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	primary := len(p.Items) - 1 // most recently added becomes primary
	Current.SetSelection(p.Items, primary)
	return Current.Selection(), nil
}

func handleSelectionClear(_ json.RawMessage) (any, error) {
	Current.SetSelection(nil, -1)
	return Current.Selection(), nil
}

// unitsMoveParams carries game-coordinate position for a single unit. x/y/z
// are WC3 game coords (centered at 0,0) — the SAME wire contract as the Wails
// App.MoveUnit method. No conversion happens here; Session.MoveUnit stores
// Position verbatim.
type unitsMoveParams struct {
	CreationNumber uint32  `json:"creation_number"`
	X              float32 `json:"x"`
	Y              float32 `json:"y"`
	Z              float32 `json:"z"`
}

type unitsMoveResponse struct {
	OK             bool       `json:"ok"`
	CreationNumber uint32     `json:"creation_number"`
	Position       [3]float32 `json:"position"`
}

func handleUnitsMove(params json.RawMessage) (any, error) {
	var p unitsMoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.MoveUnit(p.CreationNumber, p.X, p.Y, p.Z); err != nil {
		return nil, err
	}
	return unitsMoveResponse{
		OK:             true,
		CreationNumber: p.CreationNumber,
		Position:       [3]float32{p.X, p.Y, p.Z},
	}, nil
}

// handleDoodadsMove is the doodad parallel to handleUnitsMove. The wire
// shape is identical (creation_number + x/y/z game coords); the difference
// is purely the dispatch target — Session.MoveDoodad mutates war3map.doo
// instead of war3mapUnits.doo. Reuses the unitsMoveParams/unitsMoveResponse
// structs because the shape is byte-for-byte the same; only the method name
// on the wire (`doodads.move` vs `units.move`) disambiguates kind.
func handleDoodadsMove(params json.RawMessage) (any, error) {
	var p unitsMoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.MoveDoodad(p.CreationNumber, p.X, p.Y, p.Z); err != nil {
		return nil, err
	}
	return unitsMoveResponse{
		OK:             true,
		CreationNumber: p.CreationNumber,
		Position:       [3]float32{p.X, p.Y, p.Z},
	}, nil
}

// handleMapSave flushes pending edits to disk via the session's source. For
// MPQ-backed sessions, returns a user-visible message instructing the caller
// to extract the map to a folder first; programmatic clients can also check
// the wrapped sentinel through the standard JSON-RPC error structure.
func handleMapSave(_ json.RawMessage) (any, error) {
	if err := Current.Save(); err != nil {
		if errors.Is(err, ErrMPQWriteNotImplemented) {
			return nil, errors.New("MPQ archive saving is not yet implemented — extract the map to a folder first.")
		}
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func handleUnitsList(_ json.RawMessage) (any, error) {
	units := Current.Units()
	if units == nil {
		return nil, errors.New("no map loaded")
	}
	out := make([]unitsListEntity, 0, len(units.Entities))
	for _, e := range units.Entities {
		inv := make([]invItem, 0, len(e.Inventory))
		for _, slot := range e.Inventory {
			inv = append(inv, invItem{Slot: slot.Slot, ItemID: slot.ItemID})
		}
		out = append(out, unitsListEntity{
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
			Inventory:      inv,
		})
	}
	return map[string]any{"entities": out}, nil
}
