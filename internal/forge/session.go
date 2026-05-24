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

	"github.com/StephenSHorton/wc3-forge/internal/formats/doodadsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/mpq"
	"github.com/StephenSHorton/wc3-forge/internal/formats/shd"
	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3e"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wts"
)

// fileSource abstracts "where does a file's bytes come from" so the same
// Open code path covers both folder-based extracted maps and MPQ-backed
// .w3x / .w3m / .mpq files.
type fileSource interface {
	// read returns the file's bytes. ok=false + nil error means "the file
	// isn't present" (the caller decides whether that's fatal). A non-nil
	// error means a real I/O / format problem.
	read(name string) (data []byte, ok bool, err error)
	// close releases any open handles. Safe to call once at end of Open.
	close() error
}

type folderSource struct{ root string }

func (f folderSource) read(name string) ([]byte, bool, error) {
	b, err := os.ReadFile(filepath.Join(f.root, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return b, true, nil
}
func (f folderSource) close() error { return nil }

type mpqSource struct{ a *mpq.Archive }

func (m mpqSource) read(name string) ([]byte, bool, error) {
	if !m.a.Has(name) {
		return nil, false, nil
	}
	b, err := m.a.Read(name)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}
func (m mpqSource) close() error { return m.a.Close() }

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
	source  fileSource // kept open for ReadFile (mdx-m3-viewer pathSolver asks for arbitrary files inside the map MPQ)
	rawMap  []byte     // the raw .w3x bytes if opened from an archive; nil for folder-based opens
	info    *w3i.Info
	units   *unitsdoo.File
	doodads *doodadsdoo.File
	terrain *w3e.File
	// Per-map object-modification tables. The renderer merges these on top
	// of the stock SLK type indices so customs ("D006") and stock-row edits
	// both resolve to a usable MDX. nil if the map doesn't include them.
	doodadMods       *w3objmod.File // war3map.w3d
	destructibleMods *w3objmod.File // war3map.w3b
	unitMods         *w3objmod.File // war3map.w3u (parsed for future use)
	itemMods         *w3objmod.File // war3map.w3t
	shadowMap        *shd.File      // war3map.shd

	selection      SelectionState
	listeners      []func(SelectionState)
	mapListeners   []func(bool) // fired after Open/Close; bool = loaded
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

// Open replaces the loaded map with the one at path. path may be:
//   - a directory containing extracted map files (war3map.w3i, etc.), or
//   - an .w3x / .w3m / .mpq archive (HM3W shunt auto-detected).
//
// war3map.w3i is required; everything else is best-effort.
func (s *Session) Open(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat %q: %w", abs, err)
	}

	var src fileSource
	var rawMapBytes []byte
	if fi.IsDir() {
		src = folderSource{root: abs}
	} else {
		// Read the whole .w3x into memory once. mdx-m3-viewer's
		// War3MapViewer.loadMap wants the raw bytes, and we also want
		// the archive open for per-file asset reads via pathSolver.
		rawMapBytes, err = os.ReadFile(abs)
		if err != nil {
			return fmt.Errorf("read %q: %w", abs, err)
		}
		archive, err := mpq.Open(abs)
		if err != nil {
			return fmt.Errorf("open MPQ %q: %w", abs, err)
		}
		src = mpqSource{a: archive}
	}

	// war3map.w3i — REQUIRED.
	w3iBytes, ok, err := src.read("war3map.w3i")
	if err != nil {
		return fmt.Errorf("read war3map.w3i: %w", err)
	}
	if !ok {
		return fmt.Errorf("%q has no war3map.w3i", abs)
	}
	info, err := w3i.Parse(w3iBytes)
	if err != nil {
		return fmt.Errorf("parse war3map.w3i: %w", err)
	}

	// war3map.wts — OPTIONAL trigger strings. Resolve TRIGSTR_<n> on Info.
	wtsStrings, err := readOpt(src, "war3map.wts", wts.Parse)
	if err != nil {
		return err
	}
	if wtsStrings != nil {
		info.ResolveStrings(wtsStrings.Display)
	}

	// war3mapUnits.doo — OPTIONAL placed units/items.
	units, err := readOpt(src, "war3mapUnits.doo", unitsdoo.Parse)
	if err != nil {
		return err
	}
	if units == nil {
		units = &unitsdoo.File{}
	}

	// war3map.doo — OPTIONAL placed doodads/destructibles.
	doodads, err := readOpt(src, "war3map.doo", doodadsdoo.Parse)
	if err != nil {
		return err
	}
	if doodads == nil {
		doodads = &doodadsdoo.File{}
	}

	// war3map.w3e — OPTIONAL terrain. nil downstream means "can't render".
	terrain, err := readOpt(src, "war3map.w3e", w3e.Parse)
	if err != nil {
		return err
	}

	// war3map.shd — OPTIONAL static shadow map. Dimensions are derived
	// from the terrain we just parsed; depends on `terrain` being non-nil.
	var shadowMap *shd.File
	if terrain != nil {
		shdBytes, ok, err := src.read("war3map.shd")
		if err != nil {
			return fmt.Errorf("read war3map.shd: %w", err)
		}
		if ok {
			sm, perr := shd.Parse(shdBytes, int(terrain.Width), int(terrain.Height))
			if perr != nil {
				// Recoverable: shd.Parse returns a usable zero-fill File along
				// with the warning. Log and proceed.
				if sm != nil {
					shadowMap = sm
				}
			} else {
				shadowMap = sm
			}
		}
	}

	// war3map.w3{d,b,u,t} — OPTIONAL object-modification tables. Custom
	// type IDs ("D006") + stock-row edits ("ATtr scale = 1.5") live here.
	// The renderer's type indices apply these on top of the base SLK.
	parseDood := func(b []byte) (*w3objmod.File, error) { return w3objmod.Parse(b, true, nil) }
	parseDest := func(b []byte) (*w3objmod.File, error) { return w3objmod.Parse(b, false, nil) }
	parseUnit := func(b []byte) (*w3objmod.File, error) { return w3objmod.Parse(b, false, nil) }
	parseItem := func(b []byte) (*w3objmod.File, error) { return w3objmod.Parse(b, false, nil) }
	doodadMods, err := readOpt(src, "war3map.w3d", parseDood)
	if err != nil {
		return err
	}
	destructibleMods, err := readOpt(src, "war3map.w3b", parseDest)
	if err != nil {
		return err
	}
	unitMods, err := readOpt(src, "war3map.w3u", parseUnit)
	if err != nil {
		return err
	}
	itemMods, err := readOpt(src, "war3map.w3t", parseItem)
	if err != nil {
		return err
	}

	// Atomically swap state; close any previously-held source before stomping it.
	s.mu.Lock()
	prevSource := s.source
	s.loaded = true
	s.path = abs
	s.source = src
	s.rawMap = rawMapBytes
	s.info = info
	s.units = units
	s.doodads = doodads
	s.terrain = terrain
	s.doodadMods = doodadMods
	s.destructibleMods = destructibleMods
	s.unitMods = unitMods
	s.itemMods = itemMods
	s.shadowMap = shadowMap
	s.selection = SelectionState{Items: nil, Primary: -1}
	s.mu.Unlock()
	if prevSource != nil {
		_ = prevSource.close()
	}
	s.notifySelection()
	s.notifyMapChanged(true)
	return nil
}

// readOpt fetches one optional file via src and runs its parser. Returns
// (nil, nil) if the file is absent. Wraps both I/O and parse errors so the
// caller gets a clear "while loading <name>" trail.
func readOpt[T any](src fileSource, name string, parse func([]byte) (T, error)) (T, error) {
	var zero T
	b, ok, err := src.read(name)
	if err != nil {
		return zero, fmt.Errorf("read %s: %w", name, err)
	}
	if !ok {
		return zero, nil
	}
	v, err := parse(b)
	if err != nil {
		return zero, fmt.Errorf("parse %s: %w", name, err)
	}
	return v, nil
}

// Close discards the loaded map. Safe to call when nothing is loaded.
func (s *Session) Close() {
	s.mu.Lock()
	prevSource := s.source
	s.loaded = false
	s.path = ""
	s.source = nil
	s.rawMap = nil
	s.info = nil
	s.units = nil
	s.doodads = nil
	s.terrain = nil
	s.doodadMods = nil
	s.destructibleMods = nil
	s.unitMods = nil
	s.itemMods = nil
	s.shadowMap = nil
	s.selection = SelectionState{Items: nil, Primary: -1}
	s.mu.Unlock()
	if prevSource != nil {
		_ = prevSource.close()
	}
	s.notifySelection()
	s.notifyMapChanged(false)
}

// RawMapBytes returns the raw .w3x file bytes if the current map was opened
// from an archive (suitable for War3MapViewer.loadMap). Returns nil for
// folder-based opens or when no map is loaded.
func (s *Session) RawMapBytes() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rawMap
}

// ReadFile fetches one named file from the currently-loaded map's source
// (the open MPQ or the folder). Returns (nil, false, nil) if the file
// isn't present. Used by the pathSolver bridge so mdx-m3-viewer can pull
// custom-imported assets out of the map archive.
func (s *Session) ReadFile(name string) ([]byte, bool, error) {
	s.mu.RLock()
	src := s.source
	s.mu.RUnlock()
	if src == nil {
		return nil, false, nil
	}
	return src.read(name)
}

// OnMapChanged subscribes to load/unload notifications. Called after the
// Session lock is released, so listeners may safely call back into Session.
// Fires with loaded=true after Open succeeds, loaded=false after Close.
func (s *Session) OnMapChanged(fn func(loaded bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mapListeners = append(s.mapListeners, fn)
}

func (s *Session) notifyMapChanged(loaded bool) {
	s.mu.RLock()
	listeners := make([]func(bool), len(s.mapListeners))
	copy(listeners, s.mapListeners)
	s.mu.RUnlock()
	for _, fn := range listeners {
		fn(loaded)
	}
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

// Doodads returns the parsed war3map.doo, or nil if no map is loaded.
func (s *Session) Doodads() *doodadsdoo.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doodads
}

// Terrain returns the parsed war3map.w3e, or nil if no map is loaded or the
// map has no terrain file. Phase 3 viewport needs this.
func (s *Session) Terrain() *w3e.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.terrain
}

// DoodadMods returns the parsed war3map.w3d (per-map doodad modifications +
// new derived types), or nil if absent.
func (s *Session) DoodadMods() *w3objmod.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doodadMods
}

// DestructibleMods returns the parsed war3map.w3b, or nil if absent.
func (s *Session) DestructibleMods() *w3objmod.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.destructibleMods
}

// ShadowMap returns the parsed war3map.shd, or nil if absent.
func (s *Session) ShadowMap() *shd.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.shadowMap
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
