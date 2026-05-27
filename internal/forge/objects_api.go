package forge

import (
	"errors"
	"fmt"
)

// Public re-exports of the Object Editor read/write surface for the Wails
// App layer. The generic backed surface (listObjects/getObject/...) lives
// in handlers_objects.go; this file gives package main typed entry points
// that don't go through json.RawMessage.
//
// Each kind's surface is a thin wrapper around the generic that captures
// the right *KindConfig. The Wails ListUnitObjects/GetUnitObject/etc.
// methods in app.go call the units-named wrappers so the existing
// frontend keeps working byte-for-byte. Phase 2b adds parallel
// ListItemObjects/etc. by copying the units wrappers below — the bodies
// are mechanical (swap UnitsConfig() for the kind's config).

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
	return listObjects(UnitsConfig())
}

// GetUnitObject returns one merged unit's full field table by FourCC id.
// Returns (nil, nil) when the id isn't known (so callers can show an
// inline empty state without branching on error vs. missing).
func GetUnitObject(id string) (*UnitObjectDetail, error) {
	if id == "" {
		return nil, errors.New("id is required")
	}
	return getObject(UnitsConfig(), id)
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
	if err := Current.SetObjectField(UnitsConfig(), id, column, value); err != nil {
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
	chosenID, err := Current.AddCustomObject(UnitsConfig(), id, baseID)
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
	return Current.DeleteCustomObject(UnitsConfig(), id)
}
