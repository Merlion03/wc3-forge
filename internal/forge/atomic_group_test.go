package forge

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/formats/doodadsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
)

type atomicTestNoopCmd struct{ label string }

func (c *atomicTestNoopCmd) Label() string                    { return c.label }
func (c *atomicTestNoopCmd) Apply(*Session) error             { return nil }
func (c *atomicTestNoopCmd) Revert(*Session) error            { return nil }
func (c *atomicTestNoopCmd) Affected(*Session) []EntityChange { return nil }

func atomicTestSession() *Session {
	return &Session{
		loaded: true,
		units: &unitsdoo.File{Entities: []unitsdoo.Entity{{
			TypeID: "hfoo", CreationNumber: 10, Position: [3]float32{1, 2, 3}, Scale: [3]float32{1, 1, 1},
		}}},
		doodads: &doodadsdoo.File{Doodads: []doodadsdoo.Doodad{{
			TypeID: "LTlt", CreationNumber: 20, Position: [3]float32{4, 5, 6}, Scale: [3]float32{1, 1, 1},
		}}},
	}
}

func TestRunAtomicGroupSuccessIsOneUndo(t *testing.T) {
	s := atomicTestSession()
	if err := s.RunAtomicGroup("Two moves", func() error {
		if err := s.MoveUnit(10, 100, 200, 0); err != nil {
			return err
		}
		return s.MoveDoodad(20, -100, -200, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if len(s.history) != 1 {
		t.Fatalf("history len=%d, want 1", len(s.history))
	}
	grp, ok := s.history[0].(*groupCmd)
	if !ok || grp.Label() != "Two moves" || len(grp.cmds) != 2 {
		t.Fatalf("unexpected group: %#v", s.history[0])
	}
	if err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := s.units.Entities[0].Position; got != [3]float32{1, 2, 3} {
		t.Fatalf("unit after undo=%v", got)
	}
	if got := s.doodads.Doodads[0].Position; got != [3]float32{4, 5, 6} {
		t.Fatalf("doodad after undo=%v", got)
	}
	if err := s.Redo(); err != nil {
		t.Fatal(err)
	}
	if got := s.units.Entities[0].Position; got != [3]float32{100, 200, 0} {
		t.Fatalf("unit after redo=%v", got)
	}
}

func TestRunAtomicGroupFailureRestoresStateDirtyAndStacks(t *testing.T) {
	s := atomicTestSession()
	h := &atomicTestNoopCmd{label: "old history"}
	r := &atomicTestNoopCmd{label: "old redo"}
	s.history = []Command{h}
	s.redoStack = []Command{r}
	s.dirtyInfo = true
	s.dirtyTerrain = true
	s.dirtySkinMods = map[string]bool{"units": true, "items": false}
	beforeDirty := captureSessionDirtyLocked(s)

	err := s.RunAtomicGroup("Failing patch", func() error {
		if err := s.MoveUnit(10, 100, 200, 0); err != nil {
			return err
		}
		if err := s.MoveDoodad(20, -100, -200, 0); err != nil {
			return err
		}
		return errors.New("third operation failed")
	})
	if err == nil || !strings.Contains(err.Error(), "third operation failed") {
		t.Fatalf("err=%v", err)
	}
	if got := s.units.Entities[0].Position; got != [3]float32{1, 2, 3} {
		t.Fatalf("unit leaked=%v", got)
	}
	if got := s.doodads.Doodads[0].Position; got != [3]float32{4, 5, 6} {
		t.Fatalf("doodad leaked=%v", got)
	}
	if !reflect.DeepEqual(beforeDirty, captureSessionDirtyLocked(s)) {
		t.Fatal("dirty snapshot changed after rollback")
	}
	if len(s.history) != 1 || s.history[0] != h {
		t.Fatal("history changed after rollback")
	}
	if len(s.redoStack) != 1 || s.redoStack[0] != r {
		t.Fatal("redo changed after rollback")
	}
	if s.groupDepth != 0 || s.pendingGroup != nil {
		t.Fatal("atomic group leaked open")
	}
}

func TestRunAtomicGroupRejectsNestedAndRollsBackPanics(t *testing.T) {
	s := atomicTestSession()
	s.groupDepth = 1
	s.pendingGroup = &groupCmd{}
	called := false
	if err := s.RunAtomicGroup("nested", func() error { called = true; return nil }); err == nil || called {
		t.Fatalf("nested: err=%v called=%v", err, called)
	}
	s.groupDepth, s.pendingGroup = 0, nil
	if err := s.RunAtomicGroup("panic", func() error {
		if err := s.MoveUnit(10, 7, 8, 9); err != nil {
			return err
		}
		panic("kaboom")
	}); err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("panic err=%v", err)
	}
	if got := s.units.Entities[0].Position; got != [3]float32{1, 2, 3} {
		t.Fatalf("panic leaked state=%v", got)
	}
}
