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
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
)

// Session holds the currently-loaded map. Phase 1 only supports folder-based
// maps (an extracted .w3x). MPQ-backed opening is deferred to a follow-up.
type Session struct {
	mu     sync.RWMutex
	loaded bool
	path   string
	info   *w3i.Info
	units  *unitsdoo.File
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

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = true
	s.path = abs
	s.info = info
	s.units = units
	return nil
}

// Close discards the loaded map. Safe to call when nothing is loaded.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = false
	s.path = ""
	s.info = nil
	s.units = nil
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
