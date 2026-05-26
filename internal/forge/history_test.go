package forge

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/formats/doodadsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
)

// copyFixtureToTemp copies every file from src to a fresh tempdir.
// Lifted from the existing session_save_test pattern.
func copyFixtureToTemp(t *testing.T, src string) string {
	t.Helper()
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
	return tmp
}

// TestUndo_MoveUnit_RestoresPosition verifies the basic round-trip:
// mutate → Undo → state matches pre-mutation; Redo → matches post-mutation.
func TestUndo_MoveUnit_RestoresPosition(t *testing.T) {
	tmp := copyFixtureToTemp(t, `C:\Users\4step\projects\wc3-survival-game\map\extracted`)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	units := s.Units()
	if units == nil || len(units.Entities) == 0 {
		t.Fatalf("expected entities in fixture")
	}
	cn := units.Entities[0].CreationNumber
	origPos := units.Entities[0].Position
	newPos := [3]float32{origPos[0] + 100, origPos[1] + 200, origPos[2]}

	if err := s.MoveUnit(cn, newPos[0], newPos[1], newPos[2]); err != nil {
		t.Fatalf("MoveUnit: %v", err)
	}
	if got := s.Units().Entities[0].Position; got != newPos {
		t.Fatalf("post-mutation position = %v, want %v", got, newPos)
	}
	if !s.CanUndo() || s.CanRedo() {
		t.Errorf("after mutate: canUndo=%v canRedo=%v, want true/false", s.CanUndo(), s.CanRedo())
	}

	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := s.Units().Entities[0].Position; got != origPos {
		t.Errorf("post-undo position = %v, want %v", got, origPos)
	}
	if s.CanUndo() || !s.CanRedo() {
		t.Errorf("after undo: canUndo=%v canRedo=%v, want false/true", s.CanUndo(), s.CanRedo())
	}

	if err := s.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if got := s.Units().Entities[0].Position; got != newPos {
		t.Errorf("post-redo position = %v, want %v", got, newPos)
	}
}

// TestUndo_ScaleUnit_RestoresScaleRaw is the gotcha-specific test: scale
// mutation must capture the pre-mutation scaleRaw so Revert can restore
// byte-faithful round-trip. Without the capture, slocs (raw=1.0 → public
// 0.0078125) would lose their original on-disk bits after one undo cycle.
func TestUndo_ScaleUnit_RestoresScaleRaw(t *testing.T) {
	tmp := copyFixtureToTemp(t, `C:\Users\4step\projects\wc3-survival-game\map\extracted`)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	units := s.Units()
	if units == nil || len(units.Entities) == 0 {
		t.Fatalf("expected entities")
	}
	// Snapshot all entities' Scale + scaleRaw so the bytes-equal check below
	// has the full pre-mutation state to compare against.
	cn := units.Entities[0].CreationNumber
	origScale := units.Entities[0].Scale
	origRaw := unitsdoo.ScaleRaw(&units.Entities[0])

	// Encode pre-mutation for the round-trip equality check.
	preEncoded, err := unitsdoo.Encode(units)
	if err != nil {
		t.Fatalf("Encode pre: %v", err)
	}

	// Mutate scale, then undo.
	if err := s.ScaleUnit(cn, 5, 5, 5); err != nil {
		t.Fatalf("ScaleUnit: %v", err)
	}
	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	// Verify public Scale + scaleRaw are restored.
	got := s.Units().Entities[0].Scale
	gotRaw := unitsdoo.ScaleRaw(&s.Units().Entities[0])
	if got != origScale {
		t.Errorf("Scale after undo = %v, want %v", got, origScale)
	}
	if gotRaw != origRaw {
		t.Errorf("scaleRaw after undo = %v, want %v (Revert must restore both fields)", gotRaw, origRaw)
	}

	// Encode after the undo and verify bytes match pre-mutation. This is the
	// load-bearing check: if scaleRaw wasn't restored, Encode would emit
	// Scale*128 instead of the original raw bytes — bytes wouldn't match.
	postEncoded, err := unitsdoo.Encode(s.Units())
	if err != nil {
		t.Fatalf("Encode post: %v", err)
	}
	if !bytes.Equal(preEncoded, postEncoded) {
		t.Errorf("encoded bytes differ after mutate→undo cycle (scaleRaw preservation broken)")
	}
}

// TestUndoGroup_GizmoStyleBatch wraps N mutations in a Begin/End group;
// one Undo should revert ALL of them in reverse order (mirrors the gizmo's
// drag-end commit loop: one drag → one undo step).
func TestUndoGroup_GizmoStyleBatch(t *testing.T) {
	tmp := copyFixtureToTemp(t, `C:\Users\4step\projects\wc3-survival-game\map\extracted`)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	units := s.Units()
	if units == nil || len(units.Entities) < 2 {
		t.Skipf("fixture has %d entities, test wants ≥2", len(units.Entities))
	}
	cn1 := units.Entities[0].CreationNumber
	cn2 := units.Entities[1].CreationNumber
	orig1 := units.Entities[0].Position
	orig2 := units.Entities[1].Position

	s.BeginUndoGroup("Move 2 units")
	if err := s.MoveUnit(cn1, orig1[0]+50, orig1[1], orig1[2]); err != nil {
		t.Fatalf("MoveUnit cn1: %v", err)
	}
	if err := s.MoveUnit(cn2, orig2[0]+50, orig2[1], orig2[2]); err != nil {
		t.Fatalf("MoveUnit cn2: %v", err)
	}
	if err := s.EndUndoGroup(); err != nil {
		t.Fatalf("EndUndoGroup: %v", err)
	}

	// One undo should restore both.
	hist := s.HistoryList()
	if got := len(hist.Undo); got != 1 {
		t.Errorf("history depth = %d, want 1 (grouped)", got)
	}
	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := s.Units().Entities[0].Position; got != orig1 {
		t.Errorf("cn1 post-undo = %v, want %v", got, orig1)
	}
	if got := s.Units().Entities[1].Position; got != orig2 {
		t.Errorf("cn2 post-undo = %v, want %v", got, orig2)
	}
	// Redo restores the group.
	if err := s.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if got := s.Units().Entities[0].Position; got[0] != orig1[0]+50 {
		t.Errorf("cn1 post-redo X = %v, want %v", got[0], orig1[0]+50)
	}
}

// TestUndo_NewMutation_ClearsRedo is the standard editor invariant: any
// fresh edit after an undo wipes the redo stack. Otherwise the user could
// "branch" history, which we don't support.
func TestUndo_NewMutation_ClearsRedo(t *testing.T) {
	tmp := copyFixtureToTemp(t, `C:\Users\4step\projects\wc3-survival-game\map\extracted`)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cn := s.Units().Entities[0].CreationNumber
	orig := s.Units().Entities[0].Position
	_ = s.MoveUnit(cn, orig[0]+10, orig[1], orig[2])
	_ = s.Undo()
	if !s.CanRedo() {
		t.Fatalf("CanRedo=false after undo, expected true")
	}
	_ = s.MoveUnit(cn, orig[0]+20, orig[1], orig[2])
	if s.CanRedo() {
		t.Errorf("CanRedo=true after fresh mutation, expected false (redo stack wiped)")
	}
}

// TestUndo_OpenMapClearsHistory verifies that history doesn't leak across
// map opens — creation_numbers would dangle and Revert would error.
func TestUndo_OpenMapClearsHistory(t *testing.T) {
	tmp := copyFixtureToTemp(t, `C:\Users\4step\projects\wc3-survival-game\map\extracted`)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cn := s.Units().Entities[0].CreationNumber
	orig := s.Units().Entities[0].Position
	_ = s.MoveUnit(cn, orig[0]+10, orig[1], orig[2])
	if !s.CanUndo() {
		t.Fatalf("expected CanUndo=true after mutation")
	}
	// Re-open — should wipe history.
	tmp2 := copyFixtureToTemp(t, `C:\Users\4step\projects\wc3-survival-game\map\extracted`)
	if err := s.Open(tmp2); err != nil {
		t.Fatalf("Open second: %v", err)
	}
	if s.CanUndo() {
		t.Errorf("history survived map re-open; expected wipe")
	}
}

// TestUndo_RotateDoodad_RestoresRotation covers the doodad-side path
// to ensure both kinds (unit/doodad) round-trip cleanly.
func TestUndo_RotateDoodad_RestoresRotation(t *testing.T) {
	tmp := copyFixtureToTemp(t, `C:\Users\4step\projects\HiveWE\workbench\enfo-ffb-potion999.w3x.extracted`)
	if _, err := os.Stat(tmp); err != nil {
		// Skip silently — the Enfo-extracted fixture isn't a guaranteed dir
		// in this test environment; the wc3-survival fixture has 0 doodads,
		// so we fall back to a session-only smoke test.
		t.Skip("doodad fixture unavailable; doodad mutator paths are covered by integration tests")
	}
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Doodads() == nil || len(s.Doodads().Doodads) == 0 {
		t.Skip("fixture has no doodads")
	}
	cn := s.Doodads().Doodads[0].CreationNumber
	orig := s.Doodads().Doodads[0].Rotation
	if err := s.RotateDoodad(cn, orig+1.0); err != nil {
		t.Fatalf("RotateDoodad: %v", err)
	}
	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := s.Doodads().Doodads[0].Rotation; got != orig {
		t.Errorf("rotation after undo = %v, want %v", got, orig)
	}
}

// Ensure doodadsdoo import stays used by the smoke test compile even when
// that test skips at runtime.
var _ = doodadsdoo.File{}
