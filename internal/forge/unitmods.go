package forge

import (
	"fmt"
	"strings"

	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
)

// Write-side helpers for the units object-modification table (war3map.w3u).
// Pairs with objectunits.go (read pipeline) — the Object Editor's right-pane
// edits, "Add Custom" button, and "Delete Custom" button funnel through here.
//
// Convention: Overrides keys on s.unitMods are FOURCC strings (Parse was
// called with nil FieldMap at Open time). Public callers may pass either
// FourCC or column-name; setUnitField normalizes via the metadata before
// writing. This lets the MCP wire accept either form (HiveWE compatibility)
// without forcing the JS caller to know the binding.

// fieldKeyForUnitMods resolves caller-supplied field identifier (FourCC OR
// column-name) to the FourCC the on-disk shadow expects. Returns "" when
// the identifier doesn't resolve to a known field (caller should error out).
//
// Lookup order:
//  1. Direct match in ByID (caller passed a FourCC like "unam").
//  2. Lowercase column-name match in the metadata's Field column
//     (caller passed "name").
//
// Lowercased input on the column-match path — slk.Mapped columns are
// lowercase-keyed and UnitMetaData.Field is stored as-typed; we normalize
// both sides to lowercase for the comparison.
func fieldKeyForUnitMods(meta *UnitMetadata, ident string) string {
	if meta == nil {
		return ""
	}
	if _, ok := meta.ByID[ident]; ok {
		return ident
	}
	lc := strings.ToLower(ident)
	for i := range meta.Fields {
		if strings.ToLower(meta.Fields[i].Field) == lc {
			return meta.Fields[i].ID
		}
	}
	return ""
}

// findCustomIndex returns the index of the custom with the given ID in the
// unit-mods table, or -1 if not present. Caller MUST hold s.mu (read or
// write — both work).
func findCustomIndex(mods *w3objmod.File, id string) int {
	if mods == nil {
		return -1
	}
	for i := range mods.Customs {
		if mods.Customs[i].ID == id {
			return i
		}
	}
	return -1
}

// findOriginalEditIndex returns the index of the original-edit row for the
// given base ID, or -1 if not present.
func findOriginalEditIndex(mods *w3objmod.File, baseID string) int {
	if mods == nil {
		return -1
	}
	for i := range mods.OriginalEdits {
		if mods.OriginalEdits[i].BaseID == baseID {
			return i
		}
	}
	return -1
}

// ensureUnitMods returns s.unitMods, allocating a fresh empty File when nil.
// Caller MUST hold s.mu (write lock).
func ensureUnitMods(s *Session) *w3objmod.File {
	if s.unitMods == nil {
		s.unitMods = &w3objmod.File{Version: 3}
	}
	return s.unitMods
}

// setUnitField is the lock-held mutator for "edit a single field on a stock
// or custom unit". Used by the public SetUnitField AND by the command's
// Apply/Revert so undo/redo replays the same code path.
//
// Caller MUST hold s.mu (write lock). Returns the prior override value +
// "had override" flag so the caller can build the undo command.
//
// For STOCK units (id matches a SLK row), the override lands in the
// OriginalEdits table — creates a new edit-row entry if none exists yet.
// For CUSTOM units (id matches a custom row), the override lands on the
// custom's own Overrides map.
//
// Returns an error if id isn't a known unit (neither stock nor custom) or
// the field key doesn't translate to a metadata-known FourCC.
func setUnitField(s *Session, id, field, value string) (prevVal string, hadOverride bool, err error) {
	mods := ensureUnitMods(s)
	_, meta, _ := loadUnitsBase()
	fourCC := fieldKeyForUnitMods(meta, field)
	if fourCC == "" {
		return "", false, fmt.Errorf("unknown field %q (not in UnitMetaData)", field)
	}

	// Is `id` a custom? Update that custom's overrides directly.
	if ci := findCustomIndex(mods, id); ci >= 0 {
		c := &mods.Customs[ci]
		if c.Overrides == nil {
			c.Overrides = w3objmod.Overrides{}
		}
		prev, had := c.Overrides[fourCC]
		c.Overrides[fourCC] = value
		return prev, had, nil
	}

	// Is `id` a known stock unit? Update the OriginalEdits table.
	base, _, _ := loadUnitsBase()
	if base == nil || base.Rows[id] == nil {
		return "", false, fmt.Errorf("no unit with id %q", id)
	}
	if ei := findOriginalEditIndex(mods, id); ei >= 0 {
		e := &mods.OriginalEdits[ei]
		if e.Overrides == nil {
			e.Overrides = w3objmod.Overrides{}
		}
		prev, had := e.Overrides[fourCC]
		e.Overrides[fourCC] = value
		return prev, had, nil
	}
	// First edit on this stock unit — create the OriginalEdit row.
	mods.OriginalEdits = append(mods.OriginalEdits, w3objmod.OriginalEdit{
		BaseID:    id,
		Overrides: w3objmod.Overrides{fourCC: value},
	})
	return "", false, nil
}

// clearUnitField removes a field override (used by Revert of setUnitFieldCmd
// when the prior state had no override). If the resulting OriginalEdit row
// has no remaining overrides, the row itself is dropped — keeps the on-disk
// table clean so a Save after a full undo doesn't ship empty edit rows.
//
// Caller MUST hold s.mu (write lock). Returns error if id isn't found.
func clearUnitField(s *Session, id, field string) error {
	if s.unitMods == nil {
		return nil // nothing to clear
	}
	_, meta, _ := loadUnitsBase()
	fourCC := fieldKeyForUnitMods(meta, field)
	if fourCC == "" {
		return fmt.Errorf("unknown field %q", field)
	}
	if ci := findCustomIndex(s.unitMods, id); ci >= 0 {
		delete(s.unitMods.Customs[ci].Overrides, fourCC)
		return nil
	}
	if ei := findOriginalEditIndex(s.unitMods, id); ei >= 0 {
		e := &s.unitMods.OriginalEdits[ei]
		delete(e.Overrides, fourCC)
		if len(e.Overrides) == 0 {
			// Drop the empty edit-row so the saved file doesn't list a
			// no-op original entry.
			s.unitMods.OriginalEdits = append(
				s.unitMods.OriginalEdits[:ei],
				s.unitMods.OriginalEdits[ei+1:]...,
			)
		}
		return nil
	}
	return nil // not present; idempotent revert
}

// addCustomUnit appends a new custom unit row with empty Overrides. Caller
// MUST hold s.mu (write lock). Returns error if newID already collides
// with another custom OR with a stock unit (which would shadow it).
func addCustomUnit(s *Session, newID, baseID string) error {
	if len(newID) != 4 {
		return fmt.Errorf("custom id %q must be 4 chars", newID)
	}
	if len(baseID) != 4 {
		return fmt.Errorf("base id %q must be 4 chars", baseID)
	}
	mods := ensureUnitMods(s)
	if findCustomIndex(mods, newID) >= 0 {
		return fmt.Errorf("custom %q already exists", newID)
	}
	base, _, _ := loadUnitsBase()
	if base != nil && base.Rows[newID] != nil {
		// Allowing the new ID to shadow a stock row is technically valid
		// in the wire format, but it's almost always a mistake — the user
		// would lose access to the stock unit. Block it; the allocator
		// already skips occupied IDs.
		return fmt.Errorf("custom id %q would shadow a stock unit", newID)
	}
	mods.Customs = append(mods.Customs, w3objmod.CustomObject{
		ID:        newID,
		BaseID:    baseID,
		Overrides: w3objmod.Overrides{},
	})
	return nil
}

// removeCustomUnit deletes the custom with the given ID. Caller MUST hold
// s.mu (write lock). Returns the deleted CustomObject (for undo restore)
// and a flag indicating whether the deletion happened (false = not found).
func removeCustomUnit(s *Session, id string) (w3objmod.CustomObject, bool) {
	if s.unitMods == nil {
		return w3objmod.CustomObject{}, false
	}
	ci := findCustomIndex(s.unitMods, id)
	if ci < 0 {
		return w3objmod.CustomObject{}, false
	}
	saved := s.unitMods.Customs[ci]
	s.unitMods.Customs = append(s.unitMods.Customs[:ci], s.unitMods.Customs[ci+1:]...)
	return saved, true
}

// reinsertCustomUnit re-appends a previously-deleted custom (used by Revert
// of deleteCustomUnitCmd). Caller MUST hold s.mu (write lock). The original
// position in the slice isn't preserved — Customs is an unordered set in
// practice (HiveWE writes them in iteration order), so appending is fine.
func reinsertCustomUnit(s *Session, saved w3objmod.CustomObject) {
	mods := ensureUnitMods(s)
	mods.Customs = append(mods.Customs, saved)
}

// allocateCustomID picks the next-free custom unit FourCC, modeled on
// HiveWE's allocator: take the base FourCC's first character (lowercased
// for the common case, preserving uppercase for hero bases), then scan
// "<char>000".."<char>ZZZ" using hex-style 3-character suffixes ("000",
// "001", …, "00A", …) and return the first unused slot.
//
// Skips IDs that collide with stock unit FourCCs (shadowing a stock unit
// would lose access to it in the editor). Also skips existing customs of
// course.
//
// Returns "" if no free slot exists in the prefix space (would require
// ~4096 customs starting with the same letter — practically unreachable).
func allocateCustomID(s *Session, baseID string) string {
	if baseID == "" {
		return ""
	}
	prefix := byte(baseID[0])
	base, _, _ := loadUnitsBase()

	// Build a quick lookup of occupied IDs at this prefix. Iterating the
	// full base.Rows once is cheaper than 4096 map lookups in the loop.
	occupied := map[string]bool{}
	if s.unitMods != nil {
		for _, c := range s.unitMods.Customs {
			if c.ID != "" && c.ID[0] == prefix {
				occupied[c.ID] = true
			}
		}
	}
	if base != nil {
		for id := range base.Rows {
			if len(id) == 4 && id[0] == prefix {
				occupied[id] = true
			}
		}
	}

	const hex = "0123456789ABCDEF"
	for a := 0; a < 16; a++ {
		for b := 0; b < 16; b++ {
			for c := 0; c < 16; c++ {
				id := string([]byte{prefix, hex[a], hex[b], hex[c]})
				if !occupied[id] {
					return id
				}
			}
		}
	}
	return ""
}
