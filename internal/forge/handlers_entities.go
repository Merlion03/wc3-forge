package forge

import (
	"encoding/json"
	"fmt"
)

// handlers_entities.go holds the MCP bridge handlers for placed-entity
// create/delete (units + doodads). The matching reg() lines live in
// RegisterAll (handlers.go). Kept separate from handlers.go's body so the
// parallel terrain/MCP agents don't merge-conflict on it.
//
// WIRE-METHOD CONTRACT (exact names + params):
//   units.create    {type_id, player, x, y, z?, rotation?, scale?} -> {creation_number}
//   units.delete    {creation_number} -> {ok:true}
//   doodads.create  {type_id, x, y, z?, rotation?, scale?, variation?} -> {creation_number}
//   doodads.delete  {creation_number} -> {ok:true}
//
// x/y/z are WC3 world units (origin = map center), the same convention as
// units.move / doodads.move. rotation is radians (Z-axis). scale defaults to
// 1.0 when omitted or zero.

// unitsCreateParams matches the units.create wire shape. Z/Rotation/Scale are
// optional — JSON-absent decodes to the Go zero value, and CreateUnit treats
// scale==0 as "default 1.0".
type unitsCreateParams struct {
	TypeID   string  `json:"type_id"`
	Player   uint32  `json:"player"`
	X        float32 `json:"x"`
	Y        float32 `json:"y"`
	Z        float32 `json:"z"`
	Rotation float32 `json:"rotation"`
	Scale    float32 `json:"scale"`
}

type entityCreateResponse struct {
	CreationNumber uint32 `json:"creation_number"`
}

func handleUnitsCreate(params json.RawMessage) (any, error) {
	var p unitsCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TypeID == "" {
		return nil, fmt.Errorf("type_id is required")
	}
	cn, err := Current.CreateUnit(p.TypeID, p.Player, [3]float32{p.X, p.Y, p.Z}, p.Rotation, p.Scale)
	if err != nil {
		return nil, err
	}
	return entityCreateResponse{CreationNumber: cn}, nil
}

// entityDeleteParams matches units.delete / doodads.delete (both take just a
// creation_number).
type entityDeleteParams struct {
	CreationNumber uint32 `json:"creation_number"`
}

func handleUnitsDelete(params json.RawMessage) (any, error) {
	var p entityDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.DeleteUnit(p.CreationNumber); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// doodadsCreateParams matches the doodads.create wire shape. Unlike units it
// has no player and adds an optional variation; Z/Rotation/Scale are optional.
type doodadsCreateParams struct {
	TypeID    string  `json:"type_id"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
	Z         float32 `json:"z"`
	Rotation  float32 `json:"rotation"`
	Scale     float32 `json:"scale"`
	Variation uint32  `json:"variation"`
}

func handleDoodadsCreate(params json.RawMessage) (any, error) {
	var p doodadsCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TypeID == "" {
		return nil, fmt.Errorf("type_id is required")
	}
	cn, err := Current.CreateDoodad(p.TypeID, [3]float32{p.X, p.Y, p.Z}, p.Rotation, p.Scale, p.Variation)
	if err != nil {
		return nil, err
	}
	return entityCreateResponse{CreationNumber: cn}, nil
}

func handleDoodadsDelete(params json.RawMessage) (any, error) {
	var p entityDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.DeleteDoodad(p.CreationNumber); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}
