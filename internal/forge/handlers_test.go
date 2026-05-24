package forge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
)

// TestHandleUnitsMove_InvalidParams asserts the param-unmarshal path surfaces
// a clean error for malformed input. Same shape as the existing handlers.
func TestHandleUnitsMove_InvalidParams(t *testing.T) {
	if _, err := handleUnitsMove(json.RawMessage(`{"creation_number":"not-a-number"}`)); err == nil {
		t.Fatal("expected error for malformed creation_number")
	}
}

// TestHandleMapSave_MPQSentinelMessage exercises the error-mapping branch:
// when Session.Save returns ErrMPQWriteNotImplemented (which it does for an
// MPQ-backed session OR — as covered here — when invoked with no map loaded
// in a way that returns the sentinel), the handler turns it into the
// user-visible message instead of letting the wrapped sentinel leak.
//
// We construct the mapping directly off the sentinel to keep the test
// hermetic (no real .w3x fixture needed).
func TestHandleMapSave_MPQSentinelMessage(t *testing.T) {
	// Build a temp session that's MPQ-backed and dirty, swap it into Current,
	// invoke the handler, then restore. mpqSource{a: nil}.write() returns
	// the wrapped sentinel which Session.Save propagates.
	prev := Current
	t.Cleanup(func() { Current = prev })

	s := &Session{}
	s.loaded = true
	s.path = "fake.w3x"
	s.source = mpqSource{a: nil}
	// Set dirtyUnits + a units file so Save reaches the write path. Empty
	// Entities slice is fine — Encode handles it.
	s.units = &unitsdoo.File{}
	s.dirtyUnits = true
	Current = s

	_, err := handleMapSave(nil)
	if err == nil {
		t.Fatal("expected error from MPQ-backed save")
	}
	if !strings.Contains(err.Error(), "extract the map to a folder first") {
		t.Errorf("expected user-visible MPQ message, got %q", err.Error())
	}
}

// TestHandleMapSave_OK confirms the happy path: a folder-backed session with
// no dirty edits returns ok:true.
func TestHandleMapSave_OK(t *testing.T) {
	prev := Current
	t.Cleanup(func() { Current = prev })

	tmp := t.TempDir()
	s := &Session{}
	s.loaded = true
	s.path = tmp
	s.source = folderSource{root: tmp}
	// dirtyUnits=false so Save short-circuits without needing a real units file.
	Current = s

	res, err := handleMapSave(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("expected map response, got %T", res)
	}
	if m["ok"] != true {
		t.Errorf("expected ok:true, got %v", m["ok"])
	}
}
