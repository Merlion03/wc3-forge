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

// SetUnitObjectField writes `value` to the named field on the unit. column
// may be FourCC or column-name; the mutator normalizes. Returns the
// post-mutation full detail payload so the caller doesn't need a separate
// Get round-trip — the Overridden flag refreshes in-place.
func SetUnitObjectField(id, column, value string) (*UnitObjectDetail, error) {
	if id == "" {
		return nil, errors.New("id is required")
	}
	if column == "" {
		return nil, errors.New("column is required")
	}
	if err := Current.SetUnitField(id, column, value); err != nil {
		return nil, err
	}
	return GetUnitObject(id)
}

// CreateCustomUnitObject appends a new custom unit and returns the chosen ID
// + full detail payload. When id is empty the allocator picks the next free
// FourCC starting from the base's first character.
func CreateCustomUnitObject(baseID, id string) (string, *UnitObjectDetail, error) {
	if baseID == "" {
		return "", nil, errors.New("base_id is required")
	}
	chosenID, err := Current.AddCustomUnit(id, baseID)
	if err != nil {
		return "", nil, err
	}
	d, err := GetUnitObject(chosenID)
	if err != nil {
		return chosenID, nil, fmt.Errorf("get newly-created %q: %w", chosenID, err)
	}
	return chosenID, d, nil
}

// DeleteCustomUnitObject removes the custom unit with the given id. Errors
// if id isn't a custom (stock units aren't deletable).
func DeleteCustomUnitObject(id string) error {
	if id == "" {
		return errors.New("id is required")
	}
	return Current.DeleteCustomUnit(id)
}
