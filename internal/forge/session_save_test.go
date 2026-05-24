package forge

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
)

// TestMoveUnit_Save_RoundTrip is the end-to-end smoke test for the save slice:
// open a folder-backed map, mutate one unit's position via MoveUnit, save,
// reopen the map, and confirm the new position survived. Also verifies dirty
// listeners fire on the right transitions.
func TestMoveUnit_Save_RoundTrip(t *testing.T) {
	src := `C:\Users\4step\projects\wc3-survival-game\map\extracted`
	if _, err := os.Stat(src); err != nil {
		t.Skipf("fixture %q not available: %v", src, err)
	}

	// Copy the extracted map to a tempdir so we don't disturb the source.
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

	// Open via a fresh Session (avoid the global to keep this test isolated).
	s := &Session{}
	var dirtyHistory []bool
	s.OnDirtyChanged(func(d bool) { dirtyHistory = append(dirtyHistory, d) })
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.IsDirty() {
		t.Errorf("freshly-opened session is dirty")
	}

	// Capture original first unit's creation_number + position.
	units := s.Units()
	if units == nil || len(units.Entities) == 0 {
		t.Fatalf("expected entities in fixture, got none")
	}
	first := units.Entities[0]
	cn := first.CreationNumber
	origPos := first.Position

	// Move by +100 on X.
	newX := origPos[0] + 100
	if err := s.MoveUnit(cn, newX, origPos[1], origPos[2]); err != nil {
		t.Fatalf("MoveUnit: %v", err)
	}
	if !s.IsDirty() {
		t.Errorf("expected dirty after MoveUnit")
	}

	// Save and verify clean.
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if s.IsDirty() {
		t.Errorf("expected clean after Save")
	}

	// Dirty history should include a true transition then a false transition.
	if len(dirtyHistory) < 2 {
		t.Errorf("expected at least 2 dirty events, got %d: %v", len(dirtyHistory), dirtyHistory)
	} else {
		if !dirtyHistory[0] {
			t.Errorf("first dirty event should be true, got %v", dirtyHistory[0])
		}
		if dirtyHistory[len(dirtyHistory)-1] {
			t.Errorf("last dirty event should be false, got %v", dirtyHistory[len(dirtyHistory)-1])
		}
	}

	// Now reopen and confirm position survived on disk.
	s2 := &Session{}
	if err := s2.Open(tmp); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	var got *unitsdoo.Entity
	for i := range s2.Units().Entities {
		if s2.Units().Entities[i].CreationNumber == cn {
			got = &s2.Units().Entities[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("entity %d disappeared on re-open", cn)
	}
	if got.Position[0] != newX || got.Position[1] != origPos[1] || got.Position[2] != origPos[2] {
		t.Errorf("position after reopen = %v, want (%v, %v, %v)",
			got.Position, newX, origPos[1], origPos[2])
	}
}

// TestMoveUnit_NoOpDoesNotDirty asserts that MoveUnit to the unit's current
// position is a no-op — no dirty flip, no listener fire. The Properties panel
// commits position edits on blur/Enter even when the value didn't actually
// change (e.g. user clicked into the field and Escape-blurred without typing),
// so without this short-circuit the Save pill flips to amber for no real edit.
func TestMoveUnit_NoOpDoesNotDirty(t *testing.T) {
	src := `C:\Users\4step\projects\wc3-survival-game\map\extracted`
	if _, err := os.Stat(src); err != nil {
		t.Skipf("fixture %q not available: %v", src, err)
	}
	tmp := t.TempDir()
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(src, e.Name()))
		_ = os.WriteFile(filepath.Join(tmp, e.Name()), b, 0o644)
	}
	s := &Session{}
	var dirtyHistory []bool
	s.OnDirtyChanged(func(d bool) { dirtyHistory = append(dirtyHistory, d) })
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	units := s.Units()
	if units == nil || len(units.Entities) == 0 {
		t.Fatalf("expected entities in fixture, got none")
	}
	first := units.Entities[0]
	cn := first.CreationNumber
	pos := first.Position

	// Same-position move should not flip dirty.
	if err := s.MoveUnit(cn, pos[0], pos[1], pos[2]); err != nil {
		t.Fatalf("MoveUnit (no-op): %v", err)
	}
	if s.IsDirty() {
		t.Errorf("same-position MoveUnit flipped dirty")
	}
	if len(dirtyHistory) != 0 {
		t.Errorf("same-position MoveUnit fired %d dirty events, want 0: %v",
			len(dirtyHistory), dirtyHistory)
	}

	// Sanity: a real move still flips dirty.
	if err := s.MoveUnit(cn, pos[0]+1, pos[1], pos[2]); err != nil {
		t.Fatalf("MoveUnit (real): %v", err)
	}
	if !s.IsDirty() {
		t.Errorf("real MoveUnit failed to flip dirty")
	}
	if len(dirtyHistory) != 1 || !dirtyHistory[0] {
		t.Errorf("real MoveUnit dirty history = %v, want [true]", dirtyHistory)
	}
}

// TestMoveUnit_UnknownCN asserts MoveUnit surfaces a clear error for a
// creation_number that doesn't exist (and doesn't mark the session dirty).
func TestMoveUnit_UnknownCN(t *testing.T) {
	src := `C:\Users\4step\projects\wc3-survival-game\map\extracted`
	if _, err := os.Stat(src); err != nil {
		t.Skipf("fixture %q not available: %v", src, err)
	}
	tmp := t.TempDir()
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(src, e.Name()))
		_ = os.WriteFile(filepath.Join(tmp, e.Name()), b, 0o644)
	}
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	err := s.MoveUnit(99999, 0, 0, 0)
	if err == nil {
		t.Fatal("expected error for unknown creation_number")
	}
	if s.IsDirty() {
		t.Errorf("session went dirty on failed MoveUnit")
	}
}

// TestMoveUnit_FiresEntityChanged asserts MoveUnit notifies OnEntityChanged
// subscribers with a "position" Field and the new coordinates baked into the
// payload. Bridge-driven and UI-driven move paths both flow through this
// event so the Properties panel + 3D scene can repaint without polling.
//
// Also verifies the no-op short-circuit does NOT fire entity-changed (no
// real mutation, no need to repaint).
func TestMoveUnit_FiresEntityChanged(t *testing.T) {
	src := `C:\Users\4step\projects\wc3-survival-game\map\extracted`
	if _, err := os.Stat(src); err != nil {
		t.Skipf("fixture %q not available: %v", src, err)
	}
	tmp := t.TempDir()
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(src, e.Name()))
		_ = os.WriteFile(filepath.Join(tmp, e.Name()), b, 0o644)
	}
	s := &Session{}
	var changes []EntityChange
	s.OnEntityChanged(func(c EntityChange) { changes = append(changes, c) })
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	units := s.Units()
	if units == nil || len(units.Entities) == 0 {
		t.Fatalf("expected entities in fixture, got none")
	}
	first := units.Entities[0]
	cn := first.CreationNumber
	pos := first.Position

	// Real move → expect exactly one entity-changed event with the new
	// position and the right kind/id/field tags.
	newPos := [3]float32{pos[0] + 50, pos[1] - 25, pos[2]}
	if err := s.MoveUnit(cn, newPos[0], newPos[1], newPos[2]); err != nil {
		t.Fatalf("MoveUnit: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 entity-change event, got %d: %+v", len(changes), changes)
	}
	got := changes[0]
	if got.Kind != "unit" {
		t.Errorf("Kind = %q, want %q", got.Kind, "unit")
	}
	if got.ID != cn {
		t.Errorf("ID = %d, want %d", got.ID, cn)
	}
	if got.Field != "position" {
		t.Errorf("Field = %q, want %q", got.Field, "position")
	}
	if got.Position != newPos {
		t.Errorf("Position = %v, want %v", got.Position, newPos)
	}

	// Same-position move (no-op short-circuit) → no additional event.
	if err := s.MoveUnit(cn, newPos[0], newPos[1], newPos[2]); err != nil {
		t.Fatalf("MoveUnit (no-op): %v", err)
	}
	if len(changes) != 1 {
		t.Errorf("no-op MoveUnit fired extra entity-change event(s): %+v", changes)
	}

	// Different move → second event.
	if err := s.MoveUnit(cn, newPos[0]+10, newPos[1], newPos[2]); err != nil {
		t.Fatalf("MoveUnit (second real): %v", err)
	}
	if len(changes) != 2 {
		t.Errorf("expected 2 entity-change events after second real move, got %d", len(changes))
	}
}

// TestMPQ_SaveReturnsSentinel asserts MPQ-backed sessions reject Save with
// the documented sentinel error so the UI can show a friendly toast.
func TestMPQ_SaveReturnsSentinel(t *testing.T) {
	// Smoke-build the smallest plausible mpqSource scenario via direct
	// construction (avoid needing a real .w3x in the test fixture set).
	// We don't actually need a working archive — we just need an mpqSource
	// whose write() returns ErrMPQWriteNotImplemented when called.
	src := mpqSource{a: nil}
	err := src.write("war3mapUnits.doo", []byte("garbage"))
	if !errors.Is(err, ErrMPQWriteNotImplemented) {
		t.Errorf("expected ErrMPQWriteNotImplemented, got %v", err)
	}
}

// TestFolderSource_WriteSafety asserts the folderSource path-traversal guard
// rejects names that try to escape the map root.
func TestFolderSource_WriteSafety(t *testing.T) {
	tmp := t.TempDir()
	fs := folderSource{root: tmp}
	cases := []string{
		`..\war3map.w3i`,
		`..`,
		`C:\Windows\System32\evil.dll`,
		`subdir\..\..\evil`,
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fs.write(name, []byte("x")); err == nil {
				t.Errorf("expected error for path %q, got nil", name)
			}
		})
	}
	// Sanity: a normal name works.
	if err := fs.write("war3mapUnits.doo", []byte("x")); err != nil {
		t.Errorf("unexpected error for normal name: %v", err)
	}
	if !bytes.Equal(mustRead(t, filepath.Join(tmp, "war3mapUnits.doo")), []byte("x")) {
		t.Errorf("write did not produce expected contents")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
