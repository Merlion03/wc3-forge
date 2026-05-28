package forge

import (
	"os"
	"path/filepath"
	"testing"
)

// loadFixtureSession copies a folder-backed map fixture into a tempdir and
// opens it as a fresh Session. Skips when the fixture isn't present.
func loadFixtureSession(t *testing.T) (*Session, string) {
	t.Helper()
	src := `C:\Users\4step\projects\wc3-survival-game\map\extracted`
	if _, err := os.Stat(src); err != nil {
		t.Skipf("fixture %q not available: %v", src, err)
	}
	tmp := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(tmp, e.Name()), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, tmp
}

// TestAddTriggerCategory_SaveRoundTrip — add a category, save, reopen, confirm
// the category survived AND the wtg parses cleanly.
func TestAddTriggerCategory_SaveRoundTrip(t *testing.T) {
	s, tmp := loadFixtureSession(t)
	id, err := s.AddTriggerCategory("___wc3-forge-test-cat___", -1)
	if err != nil {
		t.Fatalf("AddTriggerCategory: %v", err)
	}
	if !s.IsDirty() {
		t.Errorf("expected dirty after AddTriggerCategory")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if s.IsDirty() {
		t.Errorf("expected clean after Save")
	}
	// Reopen and confirm.
	s2 := &Session{}
	if err := s2.Open(tmp); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	tt := s2.Triggers()
	if tt == nil {
		t.Fatalf("re-opened session has no triggers")
	}
	found := false
	for _, c := range tt.Categories {
		if c.ID == id && c.Name == "___wc3-forge-test-cat___" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("added category did not survive save/reopen")
	}
}

// TestAddGUITrigger_UndoRedo — add a trigger, undo, confirm gone, redo,
// confirm back.
func TestAddGUITrigger_UndoRedo(t *testing.T) {
	s, _ := loadFixtureSession(t)
	// Need a parent category. If the loaded map has none, skip.
	tt := s.Triggers()
	if tt == nil || len(tt.Categories) == 0 {
		t.Skip("no categories in fixture")
	}
	parent := tt.Categories[0].ID
	id, err := s.AddGUITrigger("___wc3-forge-test-trig___", parent)
	if err != nil {
		t.Fatalf("AddGUITrigger: %v", err)
	}
	if findTriggerIndex(s.Triggers(), id) < 0 {
		t.Fatalf("trigger not present after Add")
	}
	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if findTriggerIndex(s.Triggers(), id) >= 0 {
		t.Errorf("trigger still present after Undo")
	}
	if err := s.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if findTriggerIndex(s.Triggers(), id) < 0 {
		t.Errorf("trigger absent after Redo")
	}
}

// TestSetTriggerEnabled_Idempotent — calling SetTriggerEnabled with the
// current value must be a no-op (no dirty flip).
func TestSetTriggerEnabled_Idempotent(t *testing.T) {
	s, _ := loadFixtureSession(t)
	tt := s.Triggers()
	if tt == nil || len(tt.Triggers) == 0 {
		t.Skip("no triggers in fixture")
	}
	tr := &tt.Triggers[0]
	id := tr.ID
	enabled := tr.IsEnabled
	if err := s.SetTriggerEnabled(id, enabled); err != nil {
		t.Fatalf("SetTriggerEnabled: %v", err)
	}
	if s.IsDirty() {
		t.Errorf("same-value SetTriggerEnabled flipped dirty")
	}
	// Real toggle should flip dirty.
	if err := s.SetTriggerEnabled(id, !enabled); err != nil {
		t.Fatalf("SetTriggerEnabled toggle: %v", err)
	}
	if !s.IsDirty() {
		t.Errorf("real toggle didn't flip dirty")
	}
}

// TestMoveTriggerNode_RefusesSelfParent — confirm the validate gate fires.
func TestMoveTriggerNode_RefusesSelfParent(t *testing.T) {
	s, _ := loadFixtureSession(t)
	tt := s.Triggers()
	if tt == nil || len(tt.Categories) == 0 {
		t.Skip("no categories in fixture")
	}
	id := tt.Categories[0].ID
	err := s.MoveTriggerNode(id, id)
	if err == nil {
		t.Errorf("expected error moving node into itself")
	}
}

// TestMoveTriggerNode_RefusesNonCategoryParent — moving under a trigger or
// variable must error.
func TestMoveTriggerNode_RefusesNonCategoryParent(t *testing.T) {
	s, _ := loadFixtureSession(t)
	tt := s.Triggers()
	if tt == nil || len(tt.Triggers) == 0 || len(tt.Categories) == 0 {
		t.Skip("fixture missing triggers or categories")
	}
	srcCat := tt.Categories[0].ID
	trgTrig := tt.Triggers[0].ID
	// Try to move srcCat under trgTrig (a trigger).
	if err := s.MoveTriggerNode(srcCat, trgTrig); err == nil {
		t.Errorf("expected error moving category under a trigger")
	}
}

// TestDeleteTriggerCategory_ReparentsChildrenToRoot — children survive at
// parent_id=-1 (so they're not orphaned in the tree).
func TestDeleteTriggerCategory_ReparentsChildrenToRoot(t *testing.T) {
	s, _ := loadFixtureSession(t)
	tt := s.Triggers()
	if tt == nil {
		t.Skip("no triggers in fixture")
	}
	parent, _ := s.AddTriggerCategory("___wc3-forge-test-parent___", -1)
	child, _ := s.AddGUITrigger("___child___", parent)
	if err := s.DeleteTriggerNode(parent); err != nil {
		t.Fatalf("DeleteTriggerNode: %v", err)
	}
	tt = s.Triggers()
	idx := findTriggerIndex(tt, child)
	if idx < 0 {
		t.Errorf("child trigger gone after parent delete")
		return
	}
	if tt.Triggers[idx].ParentID != -1 {
		t.Errorf("child parent_id = %d, want -1", tt.Triggers[idx].ParentID)
	}
}

// TestRenameTriggerNode_RoundTrip — rename, undo, redo.
func TestRenameTriggerNode_RoundTrip(t *testing.T) {
	s, _ := loadFixtureSession(t)
	tt := s.Triggers()
	if tt == nil || len(tt.Triggers) == 0 {
		t.Skip("no triggers")
	}
	id := tt.Triggers[0].ID
	orig := tt.Triggers[0].Name
	if err := s.RenameTriggerNode(id, "___renamed___"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if tt := s.Triggers(); tt.Triggers[findTriggerIndex(tt, id)].Name != "___renamed___" {
		t.Errorf("rename did not take")
	}
	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if tt := s.Triggers(); tt.Triggers[findTriggerIndex(tt, id)].Name != orig {
		t.Errorf("undo did not restore name")
	}
}

// TestSetMapHeaderScript_HandRolledMap — synthesize the hand-rolled-script
// scenario in a tempdir + assert SetMapHeaderScript flips the dirty flag
// and Save writes the script back to disk.
func TestSetMapHeaderScript_HandRolledMap(t *testing.T) {
	tmp := t.TempDir()
	// Minimal w3i to make Session.Open happy. We can't easily build a real
	// w3i without the encoder + a fixture, so we copy the survival w3i and
	// strip the wtg + wct + scripts, leaving just a war3map.lua sidecar.
	src := `C:\Users\4step\projects\wc3-survival-game\map\extracted`
	if _, err := os.Stat(src); err != nil {
		t.Skipf("fixture not available")
	}
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "war3map.wtg" || name == "war3map.wct" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(src, name))
		_ = os.WriteFile(filepath.Join(tmp, name), b, 0o644)
	}
	// Ensure war3map.lua exists (synthesize a tiny stub if the source didn't
	// have one).
	if _, err := os.Stat(filepath.Join(tmp, "war3map.lua")); err != nil {
		_ = os.WriteFile(filepath.Join(tmp, "war3map.lua"), []byte("-- original\n"), 0o644)
	}
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !s.triggerIsHandRolled {
		t.Skipf("session didn't classify as hand-rolled-script (info.Lua=%v, mapHeaderScriptName=%q)", s.info != nil && s.info.Lua, s.mapHeaderScriptName)
	}
	const newContent = "-- updated by wc3-forge test\n"
	if err := s.SetMapHeaderScript(newContent); err != nil {
		t.Fatalf("SetMapHeaderScript: %v", err)
	}
	if !s.IsDirty() {
		t.Errorf("expected dirty after SetMapHeaderScript")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if s.IsDirty() {
		t.Errorf("expected clean after Save")
	}
	// Verify the file on disk.
	scriptName := "war3map.lua"
	if s.info != nil && !s.info.Lua {
		scriptName = "war3map.j"
	}
	got, err := os.ReadFile(filepath.Join(tmp, scriptName))
	if err != nil {
		t.Fatalf("read %s: %v", scriptName, err)
	}
	if string(got) != newContent {
		t.Errorf("script content = %q, want %q", string(got), newContent)
	}
}
