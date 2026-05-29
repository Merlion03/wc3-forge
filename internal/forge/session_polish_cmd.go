package forge

import (
	"encoding/json"
	"fmt"

	"github.com/StephenSHorton/wc3-forge/internal/bridge"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
)

// This file collects small correctness/polish additions kept OUT of
// session.go's body so parallel agents editing the shared session.go don't
// collide. Everything here is package forge; methods on *Session are valid in
// any file.
//
// Contents:
//   1. setMapInfoCmd + Session.SetMapInfo — routes map-info edits through the
//      undo/redo history (previously map.info_set mutated Info via MutateInfo
//      directly and left the history stack empty, contradicting the
//      "all mutations go through recordCommand" architecture claim).
//   2. selection.set "primary" support — handled in the handler below, which
//      resolves an optional client-supplied primary (index OR kind:id) and
//      passes it to the existing Session.SetSelection(items, primary).
//   3. view.get_mode read-back + a minimal session-side view-mode field.

// ---------------------------------------------------------------------------
// 1) Undoable map.info_set
// ---------------------------------------------------------------------------

// setMapInfoCmd captures a full BEFORE/AFTER snapshot of the war3map.w3i Info
// struct so Undo restores the prior field values and Redo re-applies the edit.
//
// Why a whole-struct snapshot rather than per-field deltas: ApplyInfoUpdates
// (the shared partial-update walker) only ever overwrites VALUE-typed top-level
// fields + nested value structs (Name/Author/Description, Flags, Fog,
// WaterColor, LoadingScreen, CamDistance, CameraComplements, …). It never
// appends to or mutates the contents of Info's slices (Players, Forces,
// RandomUnitTables, …). So a shallow struct copy (`old := *info`) is a complete
// and correct snapshot for every change the walker can make: restoring the
// whole struct value restores exactly the scalar/struct fields the walker
// touched while leaving the (untouched, shared) slice backing arrays intact.
//
// Cost is one Info struct value per edit (~a few hundred bytes plus shared
// slice headers) — negligible for an editor where each Map Info edit is a
// deliberate user action.
type setMapInfoCmd struct {
	old w3i.Info
	new w3i.Info
}

func (c *setMapInfoCmd) Label() string { return "Edit map info" }

func (c *setMapInfoCmd) Apply(s *Session) error {
	if s.info == nil {
		return fmt.Errorf("no map loaded")
	}
	*s.info = c.new
	s.dirtyInfo = true
	return nil
}

func (c *setMapInfoCmd) Revert(s *Session) error {
	if s.info == nil {
		return fmt.Errorf("no map loaded")
	}
	*s.info = c.old
	s.dirtyInfo = true
	return nil
}

func (c *setMapInfoCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "info", ID: 0, Field: "info"}}
}

// SetMapInfo applies the partial-update DTO `updates` to the loaded map's
// war3map.w3i and records the change on the undo/redo history stack. It is the
// undoable replacement for the old map.info_set path (which called MutateInfo
// directly and never reached recordCommand, so changes never appeared on the
// history stack and Undo could not restore the prior values).
//
// Returns the number of fields successfully applied (same semantics as
// ApplyInfoUpdates) and an error if no map is loaded. A call that changes
// nothing (empty updates, or only unknown/type-mismatched keys) is a no-op: it
// records no history entry, flips no dirty flag, and fires no event.
//
// Mirrors the MutateInfo notification ordering (dirty first, then the
// entity-changed "info" event, then history-changed) so existing UI
// subscribers (Properties panel, Map Info Editor, header pill, history panel)
// all refresh exactly as they did before.
func (s *Session) SetMapInfo(updates map[string]any) (int, error) {
	s.mu.Lock()
	if s.info == nil {
		s.mu.Unlock()
		return 0, fmt.Errorf("no map loaded")
	}
	if len(updates) == 0 {
		s.mu.Unlock()
		return 0, nil
	}
	// Snapshot BEFORE, apply, snapshot AFTER. See setMapInfoCmd's doc for why a
	// shallow struct copy is a complete snapshot of everything the walker can
	// change.
	before := *s.info
	changed := ApplyInfoUpdates(s.info, updates)
	if changed == 0 {
		// Nothing the walker recognised actually applied — leave history,
		// dirty, and events untouched so a no-op call doesn't pollute the
		// undo stack. (ApplyInfoUpdates may have written identical values for
		// matched-but-unchanged keys, but `changed == 0` means no key matched
		// at all, so before == *s.info; restore defensively to be safe.)
		*s.info = before
		s.mu.Unlock()
		return 0, nil
	}
	after := *s.info
	wasDirty := s.anyDirtyLocked()
	s.dirtyInfo = true
	s.recordCommand(&setMapInfoCmd{old: before, new: after})
	historyChanged := s.groupDepth == 0
	s.mu.Unlock()
	if !wasDirty {
		s.notifyDirty(true)
	}
	s.notifyEntityChanged(EntityChange{Kind: "info", ID: 0, Field: "info"})
	if historyChanged {
		s.notifyHistoryChanged()
	}
	return changed, nil
}

// ---------------------------------------------------------------------------
// 3) Minimal session-side view-mode read-back
// ---------------------------------------------------------------------------

// ViewMode returns the session's record of the editor's pick mode
// ("terrain" | "doodad"). This tracks the most recent view.set_mode REQUEST,
// not the live renderer state — the authoritative pick-mode toggle lives in the
// frontend (App.svelte's terrainPickModeOn). The session field exists so
// view.get_mode can report a value (useful for verifying view.set_mode landed
// and for idempotency checks); a Go-side field can't observe a frontend toggle
// the user flips by clicking the toolbar button, so callers that need the true
// live state must read it from the UI.
//
// Defaults to "doodad" before any view.set_mode call, matching the frontend's
// initial terrainPickModeOn=false (which renders "Doodad Mode").
func (s *Session) ViewMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.viewMode == "" {
		return "doodad"
	}
	return s.viewMode
}

// SetViewMode records the requested pick mode on the session. Accepts only
// "terrain" or "doodad"; any other value (including "") is ignored and leaves
// the current record unchanged. Pure bookkeeping — does NOT emit a UI command
// (the caller, handleViewSetMode, owns the existing terrain.toggle emission so
// the wire behaviour is unchanged).
func (s *Session) SetViewMode(mode string) {
	if mode != "terrain" && mode != "doodad" {
		return
	}
	s.mu.Lock()
	s.viewMode = mode
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Handlers — new file so RegisterAll's reg() lines (additive) are the only
// touch to handlers.go.
// ---------------------------------------------------------------------------

// handleMapInfoSetUndoable is the undoable replacement for handleMapInfoSet.
// Same wire contract — params {updates: {...}}, response {changed_fields: N} —
// but routes through Session.SetMapInfo so the edit lands on the history stack
// and Undo/Redo restore the prior info. RegisterAll points map.info_set here.
func handleMapInfoSetUndoable(params json.RawMessage) (any, error) {
	var p mapInfoSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if len(p.Updates) == 0 {
		return mapInfoSetResponse{ChangedFields: 0}, nil
	}
	changed, err := Current.SetMapInfo(p.Updates)
	if err != nil {
		return nil, err
	}
	return mapInfoSetResponse{ChangedFields: changed}, nil
}

// selectionSetParamsV2 extends the original selection.set params with an
// optional `primary` field. primary may be:
//   - an index into items (JSON number), or
//   - a "kind:id" string identifying which item is primary (e.g. "unit:42"),
//     or the bare id string when items are homogeneous.
//
// When omitted (or nil), behaviour is unchanged: the last item becomes primary
// (matching the legacy hardcoded len-1 default).
type selectionSetParamsV2 struct {
	Items   []SelectionItem `json:"items"`
	Primary json.RawMessage `json:"primary,omitempty"`
}

// handleSelectionSetWithPrimary honors an optional client-supplied `primary`
// (index or id) on selection.set, defaulting to the last item when omitted.
// Replaces handleSelectionSet (which hardcoded primary = len-1 and had no
// Primary field on its params struct).
func handleSelectionSetWithPrimary(params json.RawMessage) (any, error) {
	var p selectionSetParamsV2
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	primary := resolveSelectionPrimary(p.Items, p.Primary)
	Current.SetSelection(p.Items, primary)
	return Current.Selection(), nil
}

// resolveSelectionPrimary maps an optional client `primary` value to an index
// into items. Returns len(items)-1 (the legacy "last item" default) when raw is
// absent/nil, malformed, or doesn't match any item. Empty selections return -1.
//
// Accepted forms for raw:
//   - JSON number → used directly as an index (clamped/validated by
//     Session.SetSelection, which falls back to last when out of range).
//   - JSON string "kind:id" → matches the item whose Kind+ID stringify to that
//     pair (id parsed as the entity's decimal creation_number).
//   - JSON string "id" (no colon) → matches the first item whose ID stringifies
//     to that value, regardless of kind (convenient for homogeneous selections).
func resolveSelectionPrimary(items []SelectionItem, raw json.RawMessage) int {
	last := len(items) - 1 // -1 when empty; SetSelection treats that as "clear"
	if last < 0 {
		// Empty selection — no primary to resolve; SetSelection clears anyway.
		return -1
	}
	if len(raw) == 0 || string(raw) == "null" {
		return last
	}
	// Try a numeric index first.
	var idx int
	if err := json.Unmarshal(raw, &idx); err == nil {
		// Defer range validation to SetSelection (it clamps out-of-range to
		// last), so a deliberate index passes straight through.
		return idx
	}
	// Try a string selector ("kind:id" or bare "id").
	var sel string
	if err := json.Unmarshal(raw, &sel); err != nil {
		return last
	}
	wantKind, wantID, hasKind := splitSelectionSelector(sel)
	for i, it := range items {
		if hasKind && it.Kind != wantKind {
			continue
		}
		if fmt.Sprintf("%d", it.ID) == wantID {
			return i
		}
	}
	return last
}

// splitSelectionSelector parses "kind:id" → (kind, id, true) or bare "id" →
// ("", id, false). Only the FIRST colon is treated as the separator so ids
// stay intact even if a future kind ever contained one (none do today).
func splitSelectionSelector(sel string) (kind, id string, hasKind bool) {
	for i := 0; i < len(sel); i++ {
		if sel[i] == ':' {
			return sel[:i], sel[i+1:], true
		}
	}
	return "", sel, false
}

// handleViewGetMode reports the session's record of the editor pick mode. See
// Session.ViewMode for the caveat about this tracking the last view.set_mode
// REQUEST rather than the live frontend toggle state.
func handleViewGetMode(_ json.RawMessage) (any, error) {
	return map[string]any{"mode": Current.ViewMode()}, nil
}

// registerSessionPolishHandlers wires this file's handlers into the bridge.
// Called from RegisterAll. Kept here (not appended inline in RegisterAll's
// body) so the only edit to handlers.go is one additive call line.
func registerSessionPolishHandlers(reg func(string, bridge.Handler)) {
	reg("view.get_mode", handleViewGetMode)
}
