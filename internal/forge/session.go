// Package forge is the editor core: the currently-loaded map (Session) and
// the MCP handlers that read/write it. The Session is a singleton protected
// by an RWMutex; bridge handlers run on per-connection goroutines and may
// touch the session concurrently.
package forge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3e"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
)

// Session holds the currently-loaded map. Phase 1 only supports folder-based
// maps (an extracted .w3x). MPQ-backed opening is deferred to a follow-up.
//
// The Session also owns Selection — the editor's first-class selection state.
// Tools (brushes, UI panels, MCP handlers) READ selection; they never own it.
// Mutations are funneled through SetSelection so listeners get notified.
type Session struct {
	mu      sync.RWMutex
	loaded  bool
	path    string
	info    *w3i.Info
	units   *unitsdoo.File
	terrain *w3e.File

	selection SelectionState
	listeners []func(SelectionState)
}

// SelectionState is the editor's current selection. Items are entity IDs in
// a kind-agnostic shape — kind+id pairs that resolve through the document.
type SelectionState struct {
	Items   []SelectionItem `json:"items"`
	Primary int             `json:"primary"` // index into Items, or -1 if empty
}

type SelectionItem struct {
	Kind string `json:"kind"` // "unit" | "item" | "doodad" | "region" | "trigger" | ...
	ID   uint32 `json:"id"`   // creation_number for unit/item, opaque per kind
}

// Current is the process-wide singleton session.
var Current = &Session{}

// Open replaces the loaded map with the one at path. The path MUST be a
// directory containing the extracted map files (war3map.w3i etc.). A trailing
// slash is fine; we resolve to absolute internally.
//
// war3map.w3i is required. war3mapUnits.doo is optional — a map with no
// preplaced units is valid (units.list returns an empty array).
func (s *Session) Open(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat %q: %w", abs, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%q is not a directory (MPQ-backed .w3x opening is not yet implemented; pass the extracted/ folder instead)", abs)
	}

	w3iBytes, err := os.ReadFile(filepath.Join(abs, "war3map.w3i"))
	if err != nil {
		return fmt.Errorf("read war3map.w3i: %w", err)
	}
	info, err := w3i.Parse(w3iBytes)
	if err != nil {
		return fmt.Errorf("parse war3map.w3i: %w", err)
	}

	var units *unitsdoo.File
	udPath := filepath.Join(abs, "war3mapUnits.doo")
	udBytes, err := os.ReadFile(udPath)
	switch {
	case err == nil:
		units, err = unitsdoo.Parse(udBytes)
		if err != nil {
			return fmt.Errorf("parse war3mapUnits.doo: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		units = &unitsdoo.File{} // empty but valid
	default:
		return fmt.Errorf("read war3mapUnits.doo: %w", err)
	}

	// war3map.w3e — required for 3D viewport rendering, but a map without one
	// is still openable (we just can't render terrain). Treat missing as
	// non-fatal; downstream code checks for nil.
	var terrain *w3e.File
	w3ePath := filepath.Join(abs, "war3map.w3e")
	w3eBytes, err := os.ReadFile(w3ePath)
	switch {
	case err == nil:
		terrain, err = w3e.Parse(w3eBytes)
		if err != nil {
			return fmt.Errorf("parse war3map.w3e: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		terrain = nil
	default:
		return fmt.Errorf("read war3map.w3e: %w", err)
	}

	s.mu.Lock()
	s.loaded = true
	s.path = abs
	s.info = info
	s.units = units
	s.terrain = terrain
	s.selection = SelectionState{Items: nil, Primary: -1}
	s.mu.Unlock()
	s.notifySelection()
	return nil
}

// Close discards the loaded map. Safe to call when nothing is loaded.
func (s *Session) Close() {
	s.mu.Lock()
	s.loaded = false
	s.path = ""
	s.info = nil
	s.units = nil
	s.terrain = nil
	s.selection = SelectionState{Items: nil, Primary: -1}
	s.mu.Unlock()
	s.notifySelection()
}

// IsLoaded reports whether a map is currently open.
func (s *Session) IsLoaded() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loaded
}

// Path returns the absolute path of the loaded map (or "" if none).
func (s *Session) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

// Info returns the parsed war3map.w3i, or nil if no map is loaded.
// The returned pointer is shared — callers must not mutate.
func (s *Session) Info() *w3i.Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.info
}

// Units returns the parsed war3mapUnits.doo, or nil if no map is loaded.
func (s *Session) Units() *unitsdoo.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.units
}

// Terrain returns the parsed war3map.w3e, or nil if no map is loaded or the
// map has no terrain file. Phase 3 viewport needs this.
func (s *Session) Terrain() *w3e.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.terrain
}

// Selection returns the current selection. Safe to call before a map is loaded
// (returns an empty selection).
func (s *Session) Selection() SelectionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Return a copy so callers can't mutate Items in place.
	items := make([]SelectionItem, len(s.selection.Items))
	copy(items, s.selection.Items)
	return SelectionState{Items: items, Primary: s.selection.Primary}
}

// SetSelection replaces the selection. If items is nil/empty, selection clears.
// Primary defaults to len(items)-1 (the most recently added) if out of range.
// Fires SelectionListeners after release of the write lock.
func (s *Session) SetSelection(items []SelectionItem, primary int) {
	s.mu.Lock()
	if len(items) == 0 {
		s.selection = SelectionState{Items: nil, Primary: -1}
	} else {
		if primary < 0 || primary >= len(items) {
			primary = len(items) - 1
		}
		copied := make([]SelectionItem, len(items))
		copy(copied, items)
		s.selection = SelectionState{Items: copied, Primary: primary}
	}
	s.mu.Unlock()
	s.notifySelection()
}

// OnSelectionChanged subscribes a listener. Listeners are called synchronously
// from SetSelection after the lock is released, so they may call back into
// other Session methods safely. There is no unsubscribe — Session is a process
// singleton and listeners live for the process.
func (s *Session) OnSelectionChanged(fn func(SelectionState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

func (s *Session) notifySelection() {
	s.mu.RLock()
	state := s.selection
	listeners := make([]func(SelectionState), len(s.listeners))
	copy(listeners, s.listeners)
	s.mu.RUnlock()
	for _, fn := range listeners {
		fn(state)
	}
}
