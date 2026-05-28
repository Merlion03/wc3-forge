// Trigger Editor write-side — Phase 2a structural editing.
//
// Mirrors the Object Editor's Phase 1b plumbing:
//   - Each public mutator (AddTriggerCategory, RenameTriggerNode, MoveTrigger,
//     SetTriggerEnabled, ...) acquires s.mu, validates, mutates, records a
//     Command via s.recordCommand, flips s.dirtyTriggers, releases the lock,
//     then fires notifyDirty(true) on the 0→1 transition + notifyEntityChanged
//     + notifyHistoryChanged.
//   - Apply/Revert under s.mu so Undo/Redo replays the same code path.
//   - EntityChange uses Kind="trigger" and the trigger/category id in ID.
//
// All mutators are idempotent: passing the same value the entity already
// holds is a no-op (no dirty flip, no history entry, no event).
//
// Hand-rolled-script maps: the synthetic Map Header script trigger is
// editable via SetTriggerCustomText. The Save path detects this via
// s.triggerIsHandRolled and writes war3map.lua / war3map.j directly
// instead of going through the wtg/wct encoders.

package forge

import (
	"fmt"

	"github.com/StephenSHorton/wc3-forge/internal/formats/wct"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wtg"
)

// ---------------------------------------------------------------------------
// Helpers — small, lock-held primitives the mutators + commands share.
// ---------------------------------------------------------------------------

// computeNextTriggerID scans every id used in t (categories, triggers,
// variables, and DeletedIDs) and returns max+1 so allocator never collides
// with anything the World Editor previously assigned (including soft-deleted
// ids the editor tracks via the deleted-id arrays).
func computeNextTriggerID(t *wtg.Triggers) int32 {
	if t == nil {
		return 1
	}
	maxID := int32(0)
	bump := func(id int32) {
		if id > maxID {
			maxID = id
		}
	}
	for _, c := range t.Categories {
		bump(c.ID)
	}
	for _, tr := range t.Triggers {
		bump(tr.ID)
	}
	for _, v := range t.Variables {
		bump(v.ID)
	}
	for _, ids := range t.DeletedIDs {
		for _, id := range ids {
			bump(id)
		}
	}
	return maxID + 1
}

// findCategoryIndex returns the index of the category with the given id, or -1.
// Caller MUST hold s.mu (read or write).
func findCategoryIndex(t *wtg.Triggers, id int32) int {
	if t == nil {
		return -1
	}
	for i := range t.Categories {
		if t.Categories[i].ID == id {
			return i
		}
	}
	return -1
}

// findTriggerIndex returns the index of the trigger with the given id, or -1.
func findTriggerIndex(t *wtg.Triggers, id int32) int {
	if t == nil {
		return -1
	}
	for i := range t.Triggers {
		if t.Triggers[i].ID == id {
			return i
		}
	}
	return -1
}

// findVariableIndex returns the index of the variable with the given id, or -1.
func findVariableIndex(t *wtg.Triggers, id int32) int {
	if t == nil {
		return -1
	}
	for i := range t.Variables {
		if t.Variables[i].ID == id {
			return i
		}
	}
	return -1
}

// triggerNodeExists reports whether any tree node carries the given id.
// Categories first (small), then triggers, then variables.
func triggerNodeExists(t *wtg.Triggers, id int32) bool {
	return findCategoryIndex(t, id) >= 0 || findTriggerIndex(t, id) >= 0 || findVariableIndex(t, id) >= 0
}

// validateParentForMove enforces the HiveWE drop-mime invariants: the new
// parent must be a category-like node (map / library / category) AND must
// not be a descendant of the node being moved (no self-parent, no cycles).
// id is the node being moved; newParentID is the proposed new parent.
//
// Caller MUST hold s.mu (read lock is sufficient).
func validateParentForMove(t *wtg.Triggers, id, newParentID int32) error {
	if id == newParentID {
		return fmt.Errorf("cannot move a node into itself")
	}
	// The new parent must exist + be category-like.
	pi := findCategoryIndex(t, newParentID)
	if pi < 0 {
		return fmt.Errorf("new parent id %d is not a category (must be map/library/category)", newParentID)
	}
	c := t.Categories[pi]
	switch c.Classifier {
	case wtg.ClassifierMap, wtg.ClassifierLibrary, wtg.ClassifierCategory:
		// ok
	default:
		return fmt.Errorf("new parent id %d has classifier %d, want category-like", newParentID, c.Classifier)
	}
	// If id is a category, check that newParentID isn't a descendant.
	if findCategoryIndex(t, id) < 0 {
		return nil
	}
	cur := newParentID
	for steps := 0; steps < 1000; steps++ {
		if cur == id {
			return fmt.Errorf("cannot move a category under one of its descendants")
		}
		idx := findCategoryIndex(t, cur)
		if idx < 0 {
			return nil
		}
		parent := t.Categories[idx].ParentID
		if parent == cur {
			return nil // self-loop already; let downstream catch
		}
		cur = parent
		if cur < 0 {
			return nil
		}
	}
	return fmt.Errorf("category parent chain exceeded 1000 hops (possible cycle)")
}

// collectOrderedCustomTexts returns the per-trigger custom_text array in
// the wct's category-then-trigger order. Used by Save to keep wct.File's
// in-memory CustomTexts in sync with the live wtg state.
func collectOrderedCustomTexts(t *wtg.Triggers, isPre131 bool) []string {
	if t == nil {
		return nil
	}
	skipComments := !isPre131
	out := make([]string, 0, len(t.Triggers))
	for _, cat := range t.Categories {
		for i := range t.Triggers {
			tr := &t.Triggers[i]
			if tr.ParentID != cat.ID {
				continue
			}
			if skipComments && tr.IsComment {
				continue
			}
			out = append(out, tr.CustomText)
		}
	}
	return out
}

// allocTriggerID is the lock-held id allocator. Increments triggerNextID
// past any existing collision (shouldn't happen but defensive).
func allocTriggerID(s *Session) int32 {
	for {
		id := s.triggerNextID
		s.triggerNextID++
		if id <= 0 {
			continue
		}
		if !triggerNodeExists(s.triggers, id) {
			return id
		}
	}
}

// ensureTriggers allocates an empty in-memory tree if the loaded map didn't
// ship a wtg. Lets mutators succeed on melee-template maps. Caller MUST hold
// s.mu (write lock).
func ensureTriggers(s *Session) *wtg.Triggers {
	if s.triggers != nil {
		return s.triggers
	}
	s.triggers = &wtg.Triggers{
		Version:    0x80000004,
		SubVersion: 7,
		TrigDefVer: 2,
	}
	if s.triggerNextID < 1 {
		s.triggerNextID = 1
	}
	return s.triggers
}

// markDirtyAndNotify is the post-mutation tail every mutator runs: flips
// dirty (fires bus on 0→1), notifies entity-changed, notifies history-changed
// if the command was added to history (not a group member).
//
// Caller MUST NOT hold s.mu at the time of the call. wasDirty is the value
// of anyDirtyLocked() captured BEFORE the mutator's dirty flip.
func markDirtyAndNotify(s *Session, wasDirty, historyChanged bool, ev EntityChange) {
	if !wasDirty {
		s.notifyDirty(true)
	}
	s.notifyEntityChanged(ev)
	if historyChanged {
		s.notifyHistoryChanged()
	}
}

// ---------------------------------------------------------------------------
// Public mutators — one per wire operation.
// ---------------------------------------------------------------------------

// AddTriggerCategory appends a new category under parentID. Returns the new
// category's id. parentID may be -1 (root) or any existing category id.
//
// New categories default to open_state=true and is_comment=false.
func (s *Session) AddTriggerCategory(name string, parentID int32) (int32, error) {
	if name == "" {
		return 0, fmt.Errorf("name is required")
	}
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return 0, fmt.Errorf("no map loaded")
	}
	t := ensureTriggers(s)
	if parentID >= 0 && findCategoryIndex(t, parentID) < 0 {
		s.mu.Unlock()
		return 0, fmt.Errorf("no parent category with id %d", parentID)
	}
	id := allocTriggerID(s)
	cat := wtg.Category{
		Classifier: wtg.ClassifierCategory,
		ID:         id,
		ParentID:   parentID,
		Name:       name,
		OpenState:  true,
	}
	t.Categories = append(t.Categories, cat)
	t.Elements = append(t.Elements, wtg.ElementRef{Kind: wtg.ElementKindCategory, Index: len(t.Categories) - 1})
	wasDirty := s.anyDirtyLocked()
	s.dirtyTriggers = true
	s.recordCommand(&addTriggerNodeCmd{kind: triggerNodeKindCategory, id: id, parentID: parentID, name: name, openState: true})
	historyChanged := s.groupDepth == 0
	s.mu.Unlock()
	markDirtyAndNotify(s, wasDirty, historyChanged, EntityChange{Kind: "trigger", ID: uint32(id), Field: "add_category"})
	return id, nil
}

// AddGUITrigger appends a new GUI trigger under parentID. The trigger starts
// with empty ECAs (Phase 2b1 adds the authoring UI). is_enabled=true and
// initially_on=true by default.
func (s *Session) AddGUITrigger(name string, parentID int32) (int32, error) {
	return s.addTrigger(name, parentID, wtg.ClassifierGUI, false, false)
}

// AddScriptTrigger appends a new custom-script trigger under parentID. The
// trigger starts with empty CustomText; mutate via SetTriggerCustomText.
func (s *Session) AddScriptTrigger(name string, parentID int32) (int32, error) {
	return s.addTrigger(name, parentID, wtg.ClassifierGUI, true, false)
}

// AddCommentTrigger appends a comment-only trigger under parentID. The
// trigger starts with empty Description; mutate via SetTriggerDescription.
func (s *Session) AddCommentTrigger(name string, parentID int32) (int32, error) {
	return s.addTrigger(name, parentID, wtg.ClassifierGUI, false, true)
}

func (s *Session) addTrigger(name string, parentID int32, classifier wtg.Classifier, isScript, isComment bool) (int32, error) {
	if name == "" {
		return 0, fmt.Errorf("name is required")
	}
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return 0, fmt.Errorf("no map loaded")
	}
	t := ensureTriggers(s)
	if findCategoryIndex(t, parentID) < 0 {
		s.mu.Unlock()
		return 0, fmt.Errorf("no parent category with id %d", parentID)
	}
	id := allocTriggerID(s)
	tr := wtg.Trigger{
		Classifier:  classifier,
		ID:          id,
		ParentID:    parentID,
		Name:        name,
		IsEnabled:   true,
		IsScript:    isScript,
		IsComment:   isComment,
		InitiallyOn: true,
	}
	t.Triggers = append(t.Triggers, tr)
	t.Elements = append(t.Elements, wtg.ElementRef{Kind: wtg.ElementKindTrigger, Index: len(t.Triggers) - 1})
	wasDirty := s.anyDirtyLocked()
	s.dirtyTriggers = true
	s.recordCommand(&addTriggerNodeCmd{
		kind: triggerNodeKindTrigger, id: id, parentID: parentID, name: name,
		classifier: classifier, isScript: isScript, isComment: isComment,
	})
	historyChanged := s.groupDepth == 0
	s.mu.Unlock()
	markDirtyAndNotify(s, wasDirty, historyChanged, EntityChange{Kind: "trigger", ID: uint32(id), Field: "add_trigger"})
	return id, nil
}

// AddTriggerVariable appends a new global trigger variable (udg_*). varType
// is a TriggerTypes name ("integer", "unit", ...). arraySize is ignored when
// isArray is false. parentID is currently unused at the on-disk level but
// kept in the signature for future "variable in a category" support.
func (s *Session) AddTriggerVariable(name, varType string, isArray bool, arraySize int32, initValue string) (int32, error) {
	if name == "" {
		return 0, fmt.Errorf("name is required")
	}
	if varType == "" {
		return 0, fmt.Errorf("type is required")
	}
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return 0, fmt.Errorf("no map loaded")
	}
	t := ensureTriggers(s)
	for _, v := range t.Variables {
		if v.Name == name {
			s.mu.Unlock()
			return 0, fmt.Errorf("variable %q already exists", name)
		}
	}
	id := allocTriggerID(s)
	v := wtg.Variable{
		Name:          name,
		Type:          varType,
		IsArray:       isArray,
		ArraySize:     arraySize,
		IsInitialized: initValue != "",
		InitialValue:  initValue,
		ID:            id,
	}
	t.Variables = append(t.Variables, v)
	wasDirty := s.anyDirtyLocked()
	s.dirtyTriggers = true
	s.recordCommand(&addTriggerVariableCmd{saved: v})
	historyChanged := s.groupDepth == 0
	s.mu.Unlock()
	markDirtyAndNotify(s, wasDirty, historyChanged, EntityChange{Kind: "trigger", ID: uint32(id), Field: "add_variable"})
	return id, nil
}

// DeleteTriggerNode removes the node with the given id. Behavior:
//   - Category: deletes the category AND recursively re-parents children
//     to ROOT (parent_id = -1). HiveWE deletes children too; we re-parent
//     to root to minimize data loss for the user. The UI can offer a
//     follow-up "delete with children" if needed.
//   - Trigger: deletes the trigger + its custom_text binding.
//   - Variable: deletes the variable.
//
// Refuses to delete the synthetic Map Header (id=0 with hand-rolled-script
// maps) since that's the only handle on the script source.
func (s *Session) DeleteTriggerNode(id int32) error {
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	t := s.triggers
	if t == nil {
		s.mu.Unlock()
		return fmt.Errorf("no trigger tree loaded")
	}
	if s.triggerIsHandRolled {
		s.mu.Unlock()
		return fmt.Errorf("cannot delete nodes in hand-rolled-script maps")
	}
	// Category delete?
	if ci := findCategoryIndex(t, id); ci >= 0 {
		saved := t.Categories[ci]
		// Re-parent children (categories + triggers) to root.
		var reparented []reparentedNode
		for i := range t.Categories {
			if t.Categories[i].ParentID == id {
				reparented = append(reparented, reparentedNode{kind: triggerNodeKindCategory, id: t.Categories[i].ID, oldParent: id})
				t.Categories[i].ParentID = -1
			}
		}
		for i := range t.Triggers {
			if t.Triggers[i].ParentID == id {
				reparented = append(reparented, reparentedNode{kind: triggerNodeKindTrigger, id: t.Triggers[i].ID, oldParent: id})
				t.Triggers[i].ParentID = -1
			}
		}
		t.Categories = append(t.Categories[:ci], t.Categories[ci+1:]...)
		t.Elements = removeElementRef(t.Elements, wtg.ElementKindCategory, ci)
		wasDirty := s.anyDirtyLocked()
		s.dirtyTriggers = true
		s.recordCommand(&deleteTriggerNodeCmd{kind: triggerNodeKindCategory, savedCat: saved, reparented: reparented})
		historyChanged := s.groupDepth == 0
		s.mu.Unlock()
		markDirtyAndNotify(s, wasDirty, historyChanged, EntityChange{Kind: "trigger", ID: uint32(id), Field: "delete"})
		return nil
	}
	if ti := findTriggerIndex(t, id); ti >= 0 {
		saved := t.Triggers[ti]
		t.Triggers = append(t.Triggers[:ti], t.Triggers[ti+1:]...)
		t.Elements = removeElementRef(t.Elements, wtg.ElementKindTrigger, ti)
		wasDirty := s.anyDirtyLocked()
		s.dirtyTriggers = true
		s.recordCommand(&deleteTriggerNodeCmd{kind: triggerNodeKindTrigger, savedTrig: saved})
		historyChanged := s.groupDepth == 0
		s.mu.Unlock()
		markDirtyAndNotify(s, wasDirty, historyChanged, EntityChange{Kind: "trigger", ID: uint32(id), Field: "delete"})
		return nil
	}
	if vi := findVariableIndex(t, id); vi >= 0 {
		saved := t.Variables[vi]
		t.Variables = append(t.Variables[:vi], t.Variables[vi+1:]...)
		wasDirty := s.anyDirtyLocked()
		s.dirtyTriggers = true
		s.recordCommand(&deleteTriggerNodeCmd{kind: triggerNodeKindVariable, savedVar: saved})
		historyChanged := s.groupDepth == 0
		s.mu.Unlock()
		markDirtyAndNotify(s, wasDirty, historyChanged, EntityChange{Kind: "trigger", ID: uint32(id), Field: "delete"})
		return nil
	}
	s.mu.Unlock()
	return fmt.Errorf("no trigger node with id %d", id)
}

// removeElementRef rebuilds the Elements slice without the given (kind, index)
// entry, fixing up subsequent same-kind indices that referenced positions
// past the deleted slot. After a slice deletion at index `oldIdx`, every
// subsequent same-kind ref now points one slot earlier in the underlying
// Categories/Triggers slice, so we shift them.
func removeElementRef(elements []wtg.ElementRef, kind wtg.ElementKind, oldIdx int) []wtg.ElementRef {
	out := make([]wtg.ElementRef, 0, len(elements))
	for _, e := range elements {
		if e.Kind == kind && e.Index == oldIdx {
			continue // the deleted slot
		}
		if e.Kind == kind && e.Index > oldIdx {
			e.Index--
		}
		out = append(out, e)
	}
	return out
}

// RenameTriggerNode updates the Name (categories / triggers) or Variable.Name.
// Refuses empty names. Idempotent.
func (s *Session) RenameTriggerNode(id int32, newName string) error {
	if newName == "" {
		return fmt.Errorf("name cannot be empty")
	}
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	t := s.triggers
	if t == nil {
		s.mu.Unlock()
		return fmt.Errorf("no trigger tree loaded")
	}
	if ci := findCategoryIndex(t, id); ci >= 0 {
		old := t.Categories[ci].Name
		if old == newName {
			s.mu.Unlock()
			return nil
		}
		t.Categories[ci].Name = newName
		return s.finishTriggerMutation(EntityChange{Kind: "trigger", ID: uint32(id), Field: "rename"}, &renameTriggerNodeCmd{id: id, oldName: old, newName: newName})
	}
	if ti := findTriggerIndex(t, id); ti >= 0 {
		old := t.Triggers[ti].Name
		if old == newName {
			s.mu.Unlock()
			return nil
		}
		t.Triggers[ti].Name = newName
		return s.finishTriggerMutation(EntityChange{Kind: "trigger", ID: uint32(id), Field: "rename"}, &renameTriggerNodeCmd{id: id, oldName: old, newName: newName})
	}
	if vi := findVariableIndex(t, id); vi >= 0 {
		old := t.Variables[vi].Name
		if old == newName {
			s.mu.Unlock()
			return nil
		}
		t.Variables[vi].Name = newName
		return s.finishTriggerMutation(EntityChange{Kind: "trigger", ID: uint32(id), Field: "rename"}, &renameTriggerNodeCmd{id: id, oldName: old, newName: newName})
	}
	s.mu.Unlock()
	return fmt.Errorf("no trigger node with id %d", id)
}

// finishTriggerMutation is the post-mutate tail used by simple-field mutators
// (rename, enabled toggle, etc.). Records the command, flips dirty, releases
// the lock, fires notifications. Caller MUST hold s.mu when called.
func (s *Session) finishTriggerMutation(ev EntityChange, cmd Command) error {
	wasDirty := s.anyDirtyLocked()
	s.dirtyTriggers = true
	s.recordCommand(cmd)
	historyChanged := s.groupDepth == 0
	s.mu.Unlock()
	markDirtyAndNotify(s, wasDirty, historyChanged, ev)
	return nil
}

// SetTriggerEnabled toggles is_enabled on a trigger. No-op if unchanged.
func (s *Session) SetTriggerEnabled(id int32, enabled bool) error {
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	ti := findTriggerIndex(s.triggers, id)
	if ti < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no trigger with id %d", id)
	}
	tr := &s.triggers.Triggers[ti]
	if tr.IsEnabled == enabled {
		s.mu.Unlock()
		return nil
	}
	old := tr.IsEnabled
	tr.IsEnabled = enabled
	return s.finishTriggerMutation(
		EntityChange{Kind: "trigger", ID: uint32(id), Field: "is_enabled"},
		&setTriggerBoolCmd{id: id, field: "is_enabled", oldVal: old, newVal: enabled},
	)
}

// SetTriggerInitiallyOn toggles initially_on. No-op if unchanged.
// Note: stored INVERTED on disk; the encoder handles the flip — this
// mutator operates on the human-facing value.
func (s *Session) SetTriggerInitiallyOn(id int32, initiallyOn bool) error {
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	ti := findTriggerIndex(s.triggers, id)
	if ti < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no trigger with id %d", id)
	}
	tr := &s.triggers.Triggers[ti]
	if tr.InitiallyOn == initiallyOn {
		s.mu.Unlock()
		return nil
	}
	old := tr.InitiallyOn
	tr.InitiallyOn = initiallyOn
	return s.finishTriggerMutation(
		EntityChange{Kind: "trigger", ID: uint32(id), Field: "initially_on"},
		&setTriggerBoolCmd{id: id, field: "initially_on", oldVal: old, newVal: initiallyOn},
	)
}

// SetTriggerRunOnInit toggles run_on_initialization. No-op if unchanged.
func (s *Session) SetTriggerRunOnInit(id int32, runOnInit bool) error {
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	ti := findTriggerIndex(s.triggers, id)
	if ti < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no trigger with id %d", id)
	}
	tr := &s.triggers.Triggers[ti]
	if tr.RunOnInitialization == runOnInit {
		s.mu.Unlock()
		return nil
	}
	old := tr.RunOnInitialization
	tr.RunOnInitialization = runOnInit
	return s.finishTriggerMutation(
		EntityChange{Kind: "trigger", ID: uint32(id), Field: "run_on_initialization"},
		&setTriggerBoolCmd{id: id, field: "run_on_initialization", oldVal: old, newVal: runOnInit},
	)
}

// MoveTriggerNode re-parents the node with the given id under newParentID.
// Refuses self-parent + descent-into-self for categories. newParentID must
// point at a category-like node (map / library / category).
func (s *Session) MoveTriggerNode(id int32, newParentID int32) error {
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	t := s.triggers
	if t == nil {
		s.mu.Unlock()
		return fmt.Errorf("no trigger tree loaded")
	}
	if err := validateParentForMove(t, id, newParentID); err != nil {
		s.mu.Unlock()
		return err
	}
	if ci := findCategoryIndex(t, id); ci >= 0 {
		old := t.Categories[ci].ParentID
		if old == newParentID {
			s.mu.Unlock()
			return nil
		}
		t.Categories[ci].ParentID = newParentID
		return s.finishTriggerMutation(
			EntityChange{Kind: "trigger", ID: uint32(id), Field: "parent_id"},
			&moveTriggerNodeCmd{id: id, kind: triggerNodeKindCategory, oldParent: old, newParent: newParentID},
		)
	}
	if ti := findTriggerIndex(t, id); ti >= 0 {
		old := t.Triggers[ti].ParentID
		if old == newParentID {
			s.mu.Unlock()
			return nil
		}
		t.Triggers[ti].ParentID = newParentID
		return s.finishTriggerMutation(
			EntityChange{Kind: "trigger", ID: uint32(id), Field: "parent_id"},
			&moveTriggerNodeCmd{id: id, kind: triggerNodeKindTrigger, oldParent: old, newParent: newParentID},
		)
	}
	if vi := findVariableIndex(t, id); vi >= 0 {
		old := t.Variables[vi].ParentID
		if old == newParentID {
			s.mu.Unlock()
			return nil
		}
		t.Variables[vi].ParentID = newParentID
		return s.finishTriggerMutation(
			EntityChange{Kind: "trigger", ID: uint32(id), Field: "parent_id"},
			&moveTriggerNodeCmd{id: id, kind: triggerNodeKindVariable, oldParent: old, newParent: newParentID},
		)
	}
	s.mu.Unlock()
	return fmt.Errorf("no trigger node with id %d", id)
}

// SetTriggerCustomText updates a script trigger's custom_text. Also handles
// the synthetic Map Header script trigger in hand-rolled-script maps —
// flipping mapHeaderScriptDirty for the Save path.
func (s *Session) SetTriggerCustomText(id int32, customText string) error {
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	ti := findTriggerIndex(s.triggers, id)
	if ti < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no trigger with id %d", id)
	}
	tr := &s.triggers.Triggers[ti]
	if tr.CustomText == customText {
		s.mu.Unlock()
		return nil
	}
	old := tr.CustomText
	tr.CustomText = customText
	wasDirty := s.anyDirtyLocked()
	if s.triggerIsHandRolled && ti == 0 {
		s.mapHeaderScriptDirty = true
	} else {
		s.dirtyTriggers = true
	}
	s.recordCommand(&setTriggerCustomTextCmd{id: id, oldText: old, newText: customText, mapHeader: s.triggerIsHandRolled && ti == 0})
	historyChanged := s.groupDepth == 0
	s.mu.Unlock()
	markDirtyAndNotify(s, wasDirty, historyChanged, EntityChange{Kind: "trigger", ID: uint32(id), Field: "custom_text"})
	return nil
}

// SetMapHeaderScript replaces the synthesized Map Header script trigger's
// custom_text for hand-rolled-script maps. Convenience over
// SetTriggerCustomText that finds the right id internally.
func (s *Session) SetMapHeaderScript(content string) error {
	s.mu.RLock()
	if !s.triggerIsHandRolled || s.triggers == nil || len(s.triggers.Triggers) == 0 {
		s.mu.RUnlock()
		return fmt.Errorf("no hand-rolled-script Map Header to edit")
	}
	id := s.triggers.Triggers[0].ID
	s.mu.RUnlock()
	return s.SetTriggerCustomText(id, content)
}

// SetTriggerDescription updates a trigger's description (used for comment
// triggers but valid on any trigger). Idempotent.
func (s *Session) SetTriggerDescription(id int32, description string) error {
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	ti := findTriggerIndex(s.triggers, id)
	if ti < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no trigger with id %d", id)
	}
	tr := &s.triggers.Triggers[ti]
	if tr.Description == description {
		s.mu.Unlock()
		return nil
	}
	old := tr.Description
	tr.Description = description
	return s.finishTriggerMutation(
		EntityChange{Kind: "trigger", ID: uint32(id), Field: "description"},
		&setTriggerDescriptionCmd{id: id, oldText: old, newText: description},
	)
}

// SetTriggerVariable updates an existing variable's full field set (name,
// type, isArray, arraySize, initValue). The combined mutator matches the
// Variable Editor's Save button shape — one round-trip per commit.
func (s *Session) SetTriggerVariable(id int32, name, varType string, isArray bool, arraySize int32, initValue string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if varType == "" {
		return fmt.Errorf("type is required")
	}
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	vi := findVariableIndex(s.triggers, id)
	if vi < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no variable with id %d", id)
	}
	v := &s.triggers.Variables[vi]
	if v.Name == name && v.Type == varType && v.IsArray == isArray && v.ArraySize == arraySize && v.InitialValue == initValue {
		s.mu.Unlock()
		return nil
	}
	// Name collision check (skip self).
	for i := range s.triggers.Variables {
		if i == vi {
			continue
		}
		if s.triggers.Variables[i].Name == name {
			s.mu.Unlock()
			return fmt.Errorf("variable name %q is already in use", name)
		}
	}
	old := *v
	v.Name = name
	v.Type = varType
	v.IsArray = isArray
	v.ArraySize = arraySize
	v.InitialValue = initValue
	v.IsInitialized = initValue != ""
	return s.finishTriggerMutation(
		EntityChange{Kind: "trigger", ID: uint32(id), Field: "variable"},
		&setTriggerVariableCmd{id: id, oldV: old, newV: *v},
	)
}

// ---------------------------------------------------------------------------
// Command types. One per mutator; Apply/Revert under s.mu.
// ---------------------------------------------------------------------------

type triggerNodeKind uint8

const (
	triggerNodeKindCategory triggerNodeKind = iota
	triggerNodeKindTrigger
	triggerNodeKindVariable
)

type reparentedNode struct {
	kind      triggerNodeKind
	id        int32
	oldParent int32
}

// addTriggerNodeCmd captures the inputs needed to recreate a category or
// trigger that was added via AddTriggerCategory / AddGUITrigger / etc.
// Variables use addTriggerVariableCmd which preserves the full struct.
type addTriggerNodeCmd struct {
	kind       triggerNodeKind
	id         int32
	parentID   int32
	name       string
	openState  bool
	classifier wtg.Classifier // triggers only
	isScript   bool
	isComment  bool
}

func (c *addTriggerNodeCmd) Label() string {
	switch c.kind {
	case triggerNodeKindCategory:
		return "Add trigger category"
	default:
		return "Add trigger"
	}
}

func (c *addTriggerNodeCmd) Apply(s *Session) error {
	t := ensureTriggers(s)
	if c.kind == triggerNodeKindCategory {
		t.Categories = append(t.Categories, wtg.Category{
			Classifier: wtg.ClassifierCategory, ID: c.id, ParentID: c.parentID, Name: c.name, OpenState: c.openState,
		})
		t.Elements = append(t.Elements, wtg.ElementRef{Kind: wtg.ElementKindCategory, Index: len(t.Categories) - 1})
	} else {
		t.Triggers = append(t.Triggers, wtg.Trigger{
			Classifier: c.classifier, ID: c.id, ParentID: c.parentID, Name: c.name,
			IsEnabled: true, IsScript: c.isScript, IsComment: c.isComment, InitiallyOn: true,
		})
		t.Elements = append(t.Elements, wtg.ElementRef{Kind: wtg.ElementKindTrigger, Index: len(t.Triggers) - 1})
	}
	s.dirtyTriggers = true
	return nil
}

func (c *addTriggerNodeCmd) Revert(s *Session) error {
	t := s.triggers
	if t == nil {
		return nil
	}
	if c.kind == triggerNodeKindCategory {
		if i := findCategoryIndex(t, c.id); i >= 0 {
			t.Categories = append(t.Categories[:i], t.Categories[i+1:]...)
			t.Elements = removeElementRef(t.Elements, wtg.ElementKindCategory, i)
		}
	} else {
		if i := findTriggerIndex(t, c.id); i >= 0 {
			t.Triggers = append(t.Triggers[:i], t.Triggers[i+1:]...)
			t.Elements = removeElementRef(t.Elements, wtg.ElementKindTrigger, i)
		}
	}
	s.dirtyTriggers = true
	return nil
}

func (c *addTriggerNodeCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "trigger", ID: uint32(c.id), Field: "structure"}}
}

type addTriggerVariableCmd struct {
	saved wtg.Variable
}

func (c *addTriggerVariableCmd) Label() string { return "Add trigger variable" }
func (c *addTriggerVariableCmd) Apply(s *Session) error {
	t := ensureTriggers(s)
	t.Variables = append(t.Variables, c.saved)
	s.dirtyTriggers = true
	return nil
}
func (c *addTriggerVariableCmd) Revert(s *Session) error {
	if s.triggers == nil {
		return nil
	}
	if i := findVariableIndex(s.triggers, c.saved.ID); i >= 0 {
		s.triggers.Variables = append(s.triggers.Variables[:i], s.triggers.Variables[i+1:]...)
		s.dirtyTriggers = true
	}
	return nil
}
func (c *addTriggerVariableCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "trigger", ID: uint32(c.saved.ID), Field: "structure"}}
}

type deleteTriggerNodeCmd struct {
	kind       triggerNodeKind
	savedCat   wtg.Category
	savedTrig  wtg.Trigger
	savedVar   wtg.Variable
	reparented []reparentedNode // for category delete: who got reparented to root
}

func (c *deleteTriggerNodeCmd) Label() string { return "Delete trigger node" }

func (c *deleteTriggerNodeCmd) Apply(s *Session) error {
	t := s.triggers
	if t == nil {
		return nil
	}
	switch c.kind {
	case triggerNodeKindCategory:
		for _, r := range c.reparented {
			switch r.kind {
			case triggerNodeKindCategory:
				if i := findCategoryIndex(t, r.id); i >= 0 {
					t.Categories[i].ParentID = -1
				}
			case triggerNodeKindTrigger:
				if i := findTriggerIndex(t, r.id); i >= 0 {
					t.Triggers[i].ParentID = -1
				}
			}
		}
		if i := findCategoryIndex(t, c.savedCat.ID); i >= 0 {
			t.Categories = append(t.Categories[:i], t.Categories[i+1:]...)
			t.Elements = removeElementRef(t.Elements, wtg.ElementKindCategory, i)
		}
	case triggerNodeKindTrigger:
		if i := findTriggerIndex(t, c.savedTrig.ID); i >= 0 {
			t.Triggers = append(t.Triggers[:i], t.Triggers[i+1:]...)
			t.Elements = removeElementRef(t.Elements, wtg.ElementKindTrigger, i)
		}
	case triggerNodeKindVariable:
		if i := findVariableIndex(t, c.savedVar.ID); i >= 0 {
			t.Variables = append(t.Variables[:i], t.Variables[i+1:]...)
		}
	}
	s.dirtyTriggers = true
	return nil
}

func (c *deleteTriggerNodeCmd) Revert(s *Session) error {
	t := ensureTriggers(s)
	switch c.kind {
	case triggerNodeKindCategory:
		t.Categories = append(t.Categories, c.savedCat)
		t.Elements = append(t.Elements, wtg.ElementRef{Kind: wtg.ElementKindCategory, Index: len(t.Categories) - 1})
		for _, r := range c.reparented {
			switch r.kind {
			case triggerNodeKindCategory:
				if i := findCategoryIndex(t, r.id); i >= 0 {
					t.Categories[i].ParentID = r.oldParent
				}
			case triggerNodeKindTrigger:
				if i := findTriggerIndex(t, r.id); i >= 0 {
					t.Triggers[i].ParentID = r.oldParent
				}
			}
		}
	case triggerNodeKindTrigger:
		t.Triggers = append(t.Triggers, c.savedTrig)
		t.Elements = append(t.Elements, wtg.ElementRef{Kind: wtg.ElementKindTrigger, Index: len(t.Triggers) - 1})
	case triggerNodeKindVariable:
		t.Variables = append(t.Variables, c.savedVar)
	}
	s.dirtyTriggers = true
	return nil
}

func (c *deleteTriggerNodeCmd) Affected(s *Session) []EntityChange {
	var id int32
	switch c.kind {
	case triggerNodeKindCategory:
		id = c.savedCat.ID
	case triggerNodeKindTrigger:
		id = c.savedTrig.ID
	case triggerNodeKindVariable:
		id = c.savedVar.ID
	}
	return []EntityChange{{Kind: "trigger", ID: uint32(id), Field: "structure"}}
}

type renameTriggerNodeCmd struct {
	id              int32
	oldName, newName string
}

func (c *renameTriggerNodeCmd) Label() string { return "Rename trigger node" }
func (c *renameTriggerNodeCmd) Apply(s *Session) error { return renameTriggerNode(s, c.id, c.newName) }
func (c *renameTriggerNodeCmd) Revert(s *Session) error { return renameTriggerNode(s, c.id, c.oldName) }
func (c *renameTriggerNodeCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "trigger", ID: uint32(c.id), Field: "name"}}
}

func renameTriggerNode(s *Session, id int32, name string) error {
	t := s.triggers
	if t == nil {
		return fmt.Errorf("no triggers loaded")
	}
	if i := findCategoryIndex(t, id); i >= 0 {
		t.Categories[i].Name = name
		s.dirtyTriggers = true
		return nil
	}
	if i := findTriggerIndex(t, id); i >= 0 {
		t.Triggers[i].Name = name
		s.dirtyTriggers = true
		return nil
	}
	if i := findVariableIndex(t, id); i >= 0 {
		t.Variables[i].Name = name
		s.dirtyTriggers = true
		return nil
	}
	return fmt.Errorf("no trigger node with id %d", id)
}

type setTriggerBoolCmd struct {
	id     int32
	field  string // "is_enabled" | "initially_on" | "run_on_initialization"
	oldVal bool
	newVal bool
}

func (c *setTriggerBoolCmd) Label() string { return "Toggle " + c.field }
func (c *setTriggerBoolCmd) Apply(s *Session) error  { return setTriggerBool(s, c.id, c.field, c.newVal) }
func (c *setTriggerBoolCmd) Revert(s *Session) error { return setTriggerBool(s, c.id, c.field, c.oldVal) }
func (c *setTriggerBoolCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "trigger", ID: uint32(c.id), Field: c.field}}
}

func setTriggerBool(s *Session, id int32, field string, val bool) error {
	ti := findTriggerIndex(s.triggers, id)
	if ti < 0 {
		return fmt.Errorf("no trigger with id %d", id)
	}
	tr := &s.triggers.Triggers[ti]
	switch field {
	case "is_enabled":
		tr.IsEnabled = val
	case "initially_on":
		tr.InitiallyOn = val
	case "run_on_initialization":
		tr.RunOnInitialization = val
	default:
		return fmt.Errorf("unknown trigger bool field %q", field)
	}
	s.dirtyTriggers = true
	return nil
}

type moveTriggerNodeCmd struct {
	id        int32
	kind      triggerNodeKind
	oldParent int32
	newParent int32
}

func (c *moveTriggerNodeCmd) Label() string { return "Move trigger node" }
func (c *moveTriggerNodeCmd) Apply(s *Session) error  { return moveTriggerNode(s, c.id, c.kind, c.newParent) }
func (c *moveTriggerNodeCmd) Revert(s *Session) error { return moveTriggerNode(s, c.id, c.kind, c.oldParent) }
func (c *moveTriggerNodeCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "trigger", ID: uint32(c.id), Field: "parent_id"}}
}

func moveTriggerNode(s *Session, id int32, kind triggerNodeKind, newParent int32) error {
	t := s.triggers
	if t == nil {
		return fmt.Errorf("no triggers loaded")
	}
	switch kind {
	case triggerNodeKindCategory:
		i := findCategoryIndex(t, id)
		if i < 0 {
			return fmt.Errorf("no category with id %d", id)
		}
		t.Categories[i].ParentID = newParent
	case triggerNodeKindTrigger:
		i := findTriggerIndex(t, id)
		if i < 0 {
			return fmt.Errorf("no trigger with id %d", id)
		}
		t.Triggers[i].ParentID = newParent
	case triggerNodeKindVariable:
		i := findVariableIndex(t, id)
		if i < 0 {
			return fmt.Errorf("no variable with id %d", id)
		}
		t.Variables[i].ParentID = newParent
	}
	s.dirtyTriggers = true
	return nil
}

type setTriggerCustomTextCmd struct {
	id        int32
	oldText   string
	newText   string
	mapHeader bool // true when this edits the synthetic Map Header script trigger
}

func (c *setTriggerCustomTextCmd) Label() string { return "Edit custom text" }
func (c *setTriggerCustomTextCmd) Apply(s *Session) error {
	return setTriggerCustomText(s, c.id, c.newText, c.mapHeader)
}
func (c *setTriggerCustomTextCmd) Revert(s *Session) error {
	return setTriggerCustomText(s, c.id, c.oldText, c.mapHeader)
}
func (c *setTriggerCustomTextCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "trigger", ID: uint32(c.id), Field: "custom_text"}}
}

func setTriggerCustomText(s *Session, id int32, text string, mapHeader bool) error {
	ti := findTriggerIndex(s.triggers, id)
	if ti < 0 {
		return fmt.Errorf("no trigger with id %d", id)
	}
	s.triggers.Triggers[ti].CustomText = text
	if mapHeader {
		s.mapHeaderScriptDirty = true
	} else {
		s.dirtyTriggers = true
	}
	return nil
}

type setTriggerDescriptionCmd struct {
	id      int32
	oldText string
	newText string
}

func (c *setTriggerDescriptionCmd) Label() string { return "Edit description" }
func (c *setTriggerDescriptionCmd) Apply(s *Session) error  { return setTriggerDescription(s, c.id, c.newText) }
func (c *setTriggerDescriptionCmd) Revert(s *Session) error { return setTriggerDescription(s, c.id, c.oldText) }
func (c *setTriggerDescriptionCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "trigger", ID: uint32(c.id), Field: "description"}}
}

func setTriggerDescription(s *Session, id int32, text string) error {
	ti := findTriggerIndex(s.triggers, id)
	if ti < 0 {
		return fmt.Errorf("no trigger with id %d", id)
	}
	s.triggers.Triggers[ti].Description = text
	s.dirtyTriggers = true
	return nil
}

type setTriggerVariableCmd struct {
	id         int32
	oldV, newV wtg.Variable
}

func (c *setTriggerVariableCmd) Label() string { return "Edit trigger variable" }
func (c *setTriggerVariableCmd) Apply(s *Session) error  { return setTriggerVariable(s, c.id, c.newV) }
func (c *setTriggerVariableCmd) Revert(s *Session) error { return setTriggerVariable(s, c.id, c.oldV) }
func (c *setTriggerVariableCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "trigger", ID: uint32(c.id), Field: "variable"}}
}

func setTriggerVariable(s *Session, id int32, v wtg.Variable) error {
	vi := findVariableIndex(s.triggers, id)
	if vi < 0 {
		return fmt.Errorf("no variable with id %d", id)
	}
	s.triggers.Variables[vi] = v
	s.dirtyTriggers = true
	return nil
}

// Compile-time guard that wct stays imported for package callers even if
// goimports decides we don't need it (Save uses it).
var _ = wct.File{}
