package forge

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Public re-exports of the Object Editor (units) read surface so package
// main can bind them through Wails without re-implementing the wire shapes.
// The MCP handlers in handlers_objects.go remain the source of truth for
// JSON contract; these wrappers just unwrap the JSON-RawMessage signature
// the bridge uses so callers can pass typed Go values instead.

// UnitObjectListEntity is the public alias of the bridge's list-row shape.
// Same JSON tags so a Wails-bound []UnitObjectListEntity and an MCP
// objects.units.list response decode identically on the frontend.
type UnitObjectListEntity = objectsUnitsListEntity

// UnitObjectField is the public alias of the bridge's per-field row shape.
type UnitObjectField = objectsUnitsField

// UnitObjectDetail is the public alias of the bridge's get-result shape.
type UnitObjectDetail = objectsUnitsGetResult

// ListUnitObjects returns the merged-units tree. Empty slice when no CASC
// is reachable — callers should treat that as "empty state", not an error.
func ListUnitObjects() ([]UnitObjectListEntity, error) {
	raw, err := handleObjectsUnitsList(nil)
	if err != nil {
		return nil, err
	}
	out, ok := raw.([]objectsUnitsListEntity)
	if !ok {
		return nil, errors.New("internal: unexpected list shape")
	}
	return out, nil
}

// GetUnitObject returns one merged unit's full field table by FourCC id.
// Returns (nil, nil) when the id isn't known (so callers can show an
// inline empty state without branching on error vs. missing).
func GetUnitObject(id string) (*UnitObjectDetail, error) {
	if id == "" {
		return nil, errors.New("id is required")
	}
	params, _ := json.Marshal(objectsUnitsGetParams{ID: id})
	raw, err := handleObjectsUnitsGet(params)
	if err != nil {
		return nil, err
	}
	d, ok := raw.(*objectsUnitsGetResult)
	if !ok {
		return nil, fmt.Errorf("internal: unexpected get shape %T", raw)
	}
	return d, nil
}
