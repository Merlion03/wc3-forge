package forge

import (
	"encoding/json"
	"fmt"
)

// Terrain-brush wire handlers. These expose the per-corner terrain mutators
// (terrain_mutate.go) over the MCP bridge. Registered in RegisterAll
// (handlers.go) via reg(). Wire contract (col/row are 0-based corner grid
// indices; height is game-space Z in WC3 world units):
//
//   terrain.set_tile   {col:int, row:int, ground_tile_id:string} -> {ok:true}
//   terrain.set_height {col:int, row:int, height:number}         -> {ok:true}
//   terrain.get_tile   {col:int, row:int}
//        -> {col, row, ground_tile_id:string, height:number}
//
// All mutating handlers route through the undo-aware Session methods, which
// flip dirtyTerrain and fire the terrain entity-changed event. get_tile is a
// pure read-back so an agent can confirm an edit landed without a Save.

// terrainSetTileParams carries the corner coordinates + the target ground tile
// FourCC for terrain.set_tile. The FourCC must already be present in the map's
// ground palette (the brush resolves it to a palette index, it does not grow
// the palette).
type terrainSetTileParams struct {
	Col          int    `json:"col"`
	Row          int    `json:"row"`
	GroundTileID string `json:"ground_tile_id"`
}

func handleTerrainSetTile(params json.RawMessage) (any, error) {
	var p terrainSetTileParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.GroundTileID == "" {
		return nil, fmt.Errorf("ground_tile_id is required")
	}
	if err := Current.SetTerrainTile(p.Col, p.Row, p.GroundTileID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// terrainSetHeightParams carries the corner coordinates + the new ground height
// (game-space Z) for terrain.set_height.
type terrainSetHeightParams struct {
	Col    int     `json:"col"`
	Row    int     `json:"row"`
	Height float32 `json:"height"`
}

func handleTerrainSetHeight(params json.RawMessage) (any, error) {
	var p terrainSetHeightParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.SetTerrainHeight(p.Col, p.Row, p.Height); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// terrainGetTileParams carries just the corner coordinates for the read-back.
type terrainGetTileParams struct {
	Col int `json:"col"`
	Row int `json:"row"`
}

func handleTerrainGetTile(params json.RawMessage) (any, error) {
	var p terrainGetTileParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	info, err := Current.GetTerrainTile(p.Col, p.Row)
	if err != nil {
		return nil, err
	}
	return info, nil
}
