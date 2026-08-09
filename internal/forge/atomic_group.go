package forge

import (
	"errors"
	"fmt"
)

// sessionDirtySnapshot captures every per-file dirty bit that contributes to
// Session.anyDirtyLocked. dirtySkinMods is copied because it is a mutable map.
// pendingSkyModel is intentionally not part of this snapshot: the command that
// mutates it restores the actual value on Revert, while this type is only about
// dirty bookkeeping.
type sessionDirtySnapshot struct {
	units, doodads, info, terrain, gameplay, strings bool
	unitMods, itemMods, abilityMods, buffMods        bool
	destructibleMods, doodadMods, upgradeMods        bool
	triggers, mapHeaderScript, imports, regions      bool
	skinMods                                         map[string]bool
}

func captureSessionDirtyLocked(s *Session) sessionDirtySnapshot {
	return sessionDirtySnapshot{
		units: s.dirtyUnits, doodads: s.dirtyDoodads, info: s.dirtyInfo,
		terrain: s.dirtyTerrain, gameplay: s.dirtyGameplay, strings: s.dirtyStrings,
		unitMods: s.dirtyUnitMods, itemMods: s.dirtyItemMods,
		abilityMods: s.dirtyAbilityMods, buffMods: s.dirtyBuffMods,
		destructibleMods: s.dirtyDestructibleMods, doodadMods: s.dirtyDoodadMods,
		upgradeMods: s.dirtyUpgradeMods, triggers: s.dirtyTriggers,
		mapHeaderScript: s.mapHeaderScriptDirty, imports: s.dirtyImports,
		regions: s.dirtyRegions, skinMods: cloneDirtyMap(s.dirtySkinMods),
	}
}

func (d sessionDirtySnapshot) restoreLocked(s *Session) {
	s.dirtyUnits = d.units
	s.dirtyDoodads = d.doodads
	s.dirtyInfo = d.info
	s.dirtyTerrain = d.terrain
	s.dirtyGameplay = d.gameplay
	s.dirtyStrings = d.strings
	s.dirtyUnitMods = d.unitMods
	s.dirtyItemMods = d.itemMods
	s.dirtyAbilityMods = d.abilityMods
	s.dirtyBuffMods = d.buffMods
	s.dirtyDestructibleMods = d.destructibleMods
	s.dirtyDoodadMods = d.doodadMods
	s.dirtyUpgradeMods = d.upgradeMods
	s.dirtyTriggers = d.triggers
	s.mapHeaderScriptDirty = d.mapHeaderScript
	s.dirtyImports = d.imports
	s.dirtyRegions = d.regions
	s.dirtySkinMods = cloneDirtyMap(d.skinMods)
}

func cloneDirtyMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type atomicGroupSnapshot struct {
	dirty        sessionDirtySnapshot
	dirtyVisible bool
	history      []Command
	redo         []Command
}

// RunAtomicGroup executes fn as one all-or-nothing undo group.
//
// Unlike BeginUndoGroup/AbortUndoGroup, a failed RunAtomicGroup is a real
// rollback: every command recorded while fn runs is reverted in reverse order,
// the pre-call dirty flags are restored exactly, and neither undo nor redo
// history changes. On success the commands become one normal undo entry.
//
// The transaction is session-wide. Normal undo-aware mutators that run
// concurrently while fn is active join the same pending group and are therefore
// committed or rolled back with it. The callback MUST use only mutators that
// record a Command; filesystem/import/test-map paths and other non-undoable
// mutation APIs are outside this primitive's contract. Nested history groups
// are intentionally rejected for the v1.1 agent-patch surface; fn must not call
// Begin/End/AbortUndoGroup itself.
func (s *Session) RunAtomicGroup(label string, fn func() error) error {
	if fn == nil {
		return fmt.Errorf("RunAtomicGroup: nil fn")
	}

	s.mu.Lock()
	if s.groupDepth != 0 {
		depth := s.groupDepth
		s.mu.Unlock()
		return fmt.Errorf("cannot start atomic group while undo group depth is %d", depth)
	}
	grp := &groupCmd{label: label}
	snap := atomicGroupSnapshot{
		dirty:        captureSessionDirtyLocked(s),
		dirtyVisible: s.anyDirtyLocked() || s.pendingSkyModel != nil,
		history:      append([]Command(nil), s.history...),
		redo:         append([]Command(nil), s.redoStack...),
	}
	s.pendingGroup = grp
	s.groupDepth = 1
	s.mu.Unlock()

	fnErr := callAtomicFn(fn)
	if fnErr != nil {
		return s.rollbackAtomicGroup(grp, snap, fnErr)
	}

	s.mu.Lock()
	if s.groupDepth != 1 || s.pendingGroup != grp {
		s.mu.Unlock()
		return s.rollbackAtomicGroup(grp, snap, fmt.Errorf("atomic group history state was modified by callback"))
	}
	s.groupDepth = 0
	s.pendingGroup = nil

	historyChanged := len(grp.cmds) > 0
	if historyChanged {
		finalizeGroupLabel(grp)
		s.history = append(s.history, grp)
		s.redoStack = nil
	}
	s.mu.Unlock()

	if historyChanged {
		s.notifyHistoryChanged()
	}
	return nil
}

func callAtomicFn(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("atomic group panic: %v", r)
		}
	}()
	return fn()
}

func finalizeGroupLabel(grp *groupCmd) {
	if grp == nil || len(grp.cmds) == 0 {
		return
	}
	if grp.label == "" {
		grp.label = grp.cmds[0].Label()
	}
	if len(grp.cmds) > 1 && grp.label == grp.cmds[0].Label() {
		grp.label = fmt.Sprintf("%s ×%d", grp.label, len(grp.cmds))
	}
}

func (s *Session) rollbackAtomicGroup(grp *groupCmd, snap atomicGroupSnapshot, cause error) error {
	s.mu.Lock()

	// Snapshot the visible dirty state before compensation: mutators may already
	// have emitted dirty=true while the group was executing.
	dirtyBeforeRollback := s.anyDirtyLocked() || s.pendingSkyModel != nil

	var rollbackErrs []error
	for i := len(grp.cmds) - 1; i >= 0; i-- {
		if err := grp.cmds[i].Revert(s); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("revert %q: %w", grp.cmds[i].Label(), err))
		}
	}

	// Always unblock history and restore the pre-call stacks. Commands recorded
	// during an atomic group never belong in history on failure.
	s.groupDepth = 0
	s.pendingGroup = nil
	s.history = append([]Command(nil), snap.history...)
	s.redoStack = append([]Command(nil), snap.redo...)

	if len(rollbackErrs) == 0 {
		// Command.Revert marks the touched files dirty as part of normal undo.
		// A successful transaction rollback must instead reproduce the exact
		// dirty bookkeeping from before the call.
		snap.dirty.restoreLocked(s)
	}

	notifs := grp.Affected(s)
	dirtyAfterRollback := s.anyDirtyLocked() || s.pendingSkyModel != nil
	s.mu.Unlock()

	for _, n := range notifs {
		s.notifyEntityChanged(n)
	}
	if dirtyBeforeRollback != dirtyAfterRollback {
		s.notifyDirty(dirtyAfterRollback)
	}
	// Even though the visible undo/redo stacks are restored, the open-group
	// state changed 1→0. Wake listeners so any control that observed the open
	// transaction re-enables itself.
	s.notifyHistoryChanged()

	if len(rollbackErrs) > 0 {
		// Do not pretend the original dirty snapshot is trustworthy after a
		// failed compensation. Surface both errors; the current dirty flags from
		// the partial Revert attempts are deliberately left intact.
		return errors.Join(append([]error{cause}, rollbackErrs...)...)
	}
	if snap.dirtyVisible != dirtyAfterRollback {
		return fmt.Errorf("atomic rollback dirty-state invariant failed (before=%t after=%t): %w", snap.dirtyVisible, dirtyAfterRollback, cause)
	}
	return cause
}
