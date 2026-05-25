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

// TestHandleDoodadsMove_InvalidParams mirrors the units variant: malformed
// JSON should surface a wrapped unmarshal error, not a panic.
func TestHandleDoodadsMove_InvalidParams(t *testing.T) {
	if _, err := handleDoodadsMove(json.RawMessage(`{"creation_number":"not-a-number"}`)); err == nil {
		t.Fatal("expected error for malformed creation_number")
	}
}

// TestHandleDoodadsMove_NoMap asserts the "no map loaded" path returns an
// error rather than silently no-op-ing — keeps the bridge's contract loud
// about the precondition.
func TestHandleDoodadsMove_NoMap(t *testing.T) {
	prev := Current
	t.Cleanup(func() { Current = prev })
	Current = &Session{}
	_, err := handleDoodadsMove(json.RawMessage(`{"creation_number":1,"x":0,"y":0,"z":0}`))
	if err == nil {
		t.Fatal("expected error when no map loaded")
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

// TestHandleUnitsRotate_InvalidParams asserts the param-unmarshal path surfaces
// a clean error for malformed input.
func TestHandleUnitsRotate_InvalidParams(t *testing.T) {
	if _, err := handleUnitsRotate(json.RawMessage(`{"creation_number":"bad"}`)); err == nil {
		t.Fatal("expected error for malformed creation_number")
	}
}

// TestHandleUnitsRotate_NoMap asserts the "no map loaded" path returns an error.
func TestHandleUnitsRotate_NoMap(t *testing.T) {
	prev := Current
	t.Cleanup(func() { Current = prev })
	Current = &Session{}
	_, err := handleUnitsRotate(json.RawMessage(`{"creation_number":1,"rotation":1.57}`))
	if err == nil {
		t.Fatal("expected error when no map loaded")
	}
}

// TestHandleDoodadsRotate_InvalidParams mirrors the units variant.
func TestHandleDoodadsRotate_InvalidParams(t *testing.T) {
	if _, err := handleDoodadsRotate(json.RawMessage(`{"creation_number":"bad"}`)); err == nil {
		t.Fatal("expected error for malformed creation_number")
	}
}

// TestHandleDoodadsRotate_NoMap asserts "no map loaded" returns an error.
func TestHandleDoodadsRotate_NoMap(t *testing.T) {
	prev := Current
	t.Cleanup(func() { Current = prev })
	Current = &Session{}
	_, err := handleDoodadsRotate(json.RawMessage(`{"creation_number":1,"rotation":1.57}`))
	if err == nil {
		t.Fatal("expected error when no map loaded")
	}
}

// TestHandleUnitsScale_InvalidParams asserts malformed input returns an error.
func TestHandleUnitsScale_InvalidParams(t *testing.T) {
	if _, err := handleUnitsScale(json.RawMessage(`{"creation_number":"bad"}`)); err == nil {
		t.Fatal("expected error for malformed creation_number")
	}
}

// TestHandleUnitsScale_NoMap asserts "no map loaded" returns an error.
func TestHandleUnitsScale_NoMap(t *testing.T) {
	prev := Current
	t.Cleanup(func() { Current = prev })
	Current = &Session{}
	_, err := handleUnitsScale(json.RawMessage(`{"creation_number":1,"sx":1.0,"sy":1.0,"sz":1.0}`))
	if err == nil {
		t.Fatal("expected error when no map loaded")
	}
}

// TestHandleDoodadsScale_InvalidParams mirrors the units variant.
func TestHandleDoodadsScale_InvalidParams(t *testing.T) {
	if _, err := handleDoodadsScale(json.RawMessage(`{"creation_number":"bad"}`)); err == nil {
		t.Fatal("expected error for malformed creation_number")
	}
}

// TestHandleDoodadsScale_NoMap asserts "no map loaded" returns an error.
func TestHandleDoodadsScale_NoMap(t *testing.T) {
	prev := Current
	t.Cleanup(func() { Current = prev })
	Current = &Session{}
	_, err := handleDoodadsScale(json.RawMessage(`{"creation_number":1,"sx":1.0,"sy":1.0,"sz":1.0}`))
	if err == nil {
		t.Fatal("expected error when no map loaded")
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
