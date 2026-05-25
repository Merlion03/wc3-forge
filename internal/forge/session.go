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
	"strings"
	"sync"
	"time"

	"github.com/StephenSHorton/wc3-forge/internal/formats/doodadsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/mpq"
	"github.com/StephenSHorton/wc3-forge/internal/formats/shd"
	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3e"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wpm"
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
	// write replaces (or creates) the named file's bytes in this source.
	// Folder sources write to disk; MPQ sources currently return
	// ErrMPQWriteNotImplemented (extract to a folder first).
	write(name string, data []byte) error
	// close releases any open handles. Safe to call once at end of Open.
	close() error
}

// ErrMPQWriteNotImplemented is returned by Save when the loaded map is backed
// by an MPQ archive. MPQ writing is not yet supported — callers should extract
// the map to a folder first. errors.Is-checkable so the UI can surface a
// friendly toast rather than a stack trace.
var ErrMPQWriteNotImplemented = errors.New("MPQ archive writing is not yet implemented — extract the map to a folder first")

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

// write replaces (or creates) the named file under f.root. Path traversal is
// defended against via filepath.Clean — name comes from Session's own code
// today but plumb safely in case future callers route untrusted strings here.
func (f folderSource) write(name string, data []byte) error {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+"..") {
		return fmt.Errorf("write %q: unsafe path", name)
	}
	return os.WriteFile(filepath.Join(f.root, clean), data, 0o644)
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

// write on an MPQ source is intentionally unsupported. MPQ writing is a
// multi-thousand-line problem (rebuild block table, hash table, sector
// offsets, optional compression) that wc3-forge defers in favour of the
// folder-source path. Callers should extract the .w3x to a folder first.
func (m mpqSource) write(name string, data []byte) error {
	return fmt.Errorf("%w (file=%q)", ErrMPQWriteNotImplemented, name)
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
	pathingMap       *wpm.File      // war3map.wpm
	strings          wts.Strings    // war3map.wts, for TRIGSTR_<n> resolution

	selection      SelectionState
	listeners      []func(SelectionState)
	mapListeners   []func(bool) // fired after Open/Close; bool = loaded

	// Dirty tracking — per-file granularity. Save iterates these and writes
	// only the dirty files back through the source's write path. Open + Close
	// reset them. The boolean dirty-changed bus mirrors the map/selection
	// notification pattern (lock-free copy → invoke listeners).
	//
	// The PUBLIC dirty-changed event is a single boolean ("any file dirty");
	// per-file flags are the internal granularity that lets Save write only
	// the files that actually changed. Mutator fires dirty=true on the
	// 0→1 transition of (dirtyUnits || dirtyDoodads || dirtyInfo), Save
	// fires dirty=false on the 1→0 transition.
	dirtyUnits     bool
	dirtyDoodads   bool
	dirtyInfo      bool
	dirtyListeners []func(bool)

	// Entity-change bus — fired from any mutator (MoveUnit today; SetRotation,
	// SetType, etc. in the future). Subscribers refresh stale views of the
	// changed entity (Properties panel re-fetches, scene re-positions the
	// model). Mirrors the OnDirtyChanged pattern: slice of listeners, copied
	// under the read lock and invoked outside it so listeners may call back
	// into Session safely. Distinct from OnDirtyChanged because dirty fires
	// only on the first edit per save-cycle (boolean transition), whereas
	// every mutation needs to repaint UI even when the session was already
	// dirty.
	entityListeners []func(EntityChange)

	// Bridge-call observability bus — every MCP handler dispatch fires a
	// BridgeCallEvent through here, so subscribers (the Wails app forwards
	// to the in-page Agent Console) can stream a live log of agent activity.
	// Passive: listeners must not call back into bridge handlers from this
	// callback or they'll recurse. Held under the same lock+copy pattern as
	// the other listener slices.
	bridgeCallListeners []func(BridgeCallEvent)

	// UI-command bus — used by MCP handlers that need to drive JS-side state
	// (View menu visibility, terrain/doodad mode toggle, camera positioning).
	// The App layer subscribes and forwards each command string to the same
	// `wc3-forge:test-command` Wails event the existing test-driver hook
	// already handles. Keeps the bridge layer App-free (no Wails imports in
	// forge.*) while still letting bridge handlers reach UI state.
	uiCommandListeners []func(string)
}

// EntityChange is the payload for OnEntityChanged. Kind/ID identify which
// entity changed; Field names the conceptual property that moved. Convention:
// one event per changed channel — "position" | "rotation" | "scale" | "transform".
// A single mutator that changes only one channel fires one event with that
// channel's Field tag; the other fields are zero-valued and ignored by
// subscribers that don't care. A future "transform" field may carry all three
// simultaneously if a mutator needs to atomically change position+rotation
// (e.g. orbit around centroid fires two separate events today — one MoveUnit
// + one RotateUnit — which is fine and keeps the event shape self-documenting).
//
// Forward-compatible by design: new mutators add new Field strings; existing
// subscribers branching on Field safely ignore unknown tags.
type EntityChange struct {
	Kind     string     `json:"kind"`     // "unit" | "doodad" | "info" | ...
	ID       uint32     `json:"id"`       // creation_number (per-kind in WC3)
	Field    string     `json:"field"`    // "position" | "rotation" | "scale" | "transform"
	Position [3]float32 `json:"position"` // valid when Field == "position" or "transform"
	Rotation float32    `json:"rotation"` // valid when Field == "rotation" or "transform" (radians, Z-axis only)
	Scale    [3]float32 `json:"scale"`    // valid when Field == "scale" or "transform"
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

	// war3map.wpm — OPTIONAL static pathing map. Independent of terrain
	// dimensions (file declares its own width/height) so we don't gate on
	// `terrain != nil`. Pathing exists for terrainless maps in principle,
	// though every real map ships terrain.
	pathingMap, err := readOpt(src, "war3map.wpm", wpm.Parse)
	if err != nil {
		return err
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
	s.pathingMap = pathingMap
	s.strings = wtsStrings
	s.selection = SelectionState{Items: nil, Primary: -1}
	wasDirty := s.dirtyUnits || s.dirtyDoodads || s.dirtyInfo
	s.dirtyUnits = false
	s.dirtyDoodads = false
	s.dirtyInfo = false
	s.mu.Unlock()
	if prevSource != nil {
		_ = prevSource.close()
	}
	s.notifySelection()
	s.notifyMapChanged(true)
	if wasDirty {
		s.notifyDirty(false)
	}
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
	s.pathingMap = nil
	s.strings = nil
	s.selection = SelectionState{Items: nil, Primary: -1}
	wasDirty := s.dirtyUnits || s.dirtyDoodads || s.dirtyInfo
	s.dirtyUnits = false
	s.dirtyDoodads = false
	s.dirtyInfo = false
	s.mu.Unlock()
	if prevSource != nil {
		_ = prevSource.close()
	}
	s.notifySelection()
	s.notifyMapChanged(false)
	if wasDirty {
		s.notifyDirty(false)
	}
}

// Strings returns the parsed war3map.wts trigger-strings table, or nil if
// the loaded map doesn't ship one. Used to resolve TRIGSTR_<n> references
// in per-map object modifications.
func (s *Session) Strings() wts.Strings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.strings
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

// UnitMods returns the parsed war3map.w3u (per-map unit modifications +
// new derived types), or nil if absent.
func (s *Session) UnitMods() *w3objmod.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unitMods
}

// ItemMods returns the parsed war3map.w3t, or nil if absent.
func (s *Session) ItemMods() *w3objmod.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.itemMods
}

// ShadowMap returns the parsed war3map.shd, or nil if absent.
func (s *Session) ShadowMap() *shd.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.shadowMap
}

// PathingMap returns the parsed war3map.wpm, or nil if absent.
func (s *Session) PathingMap() *wpm.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pathingMap
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

// MoveUnit relocates the unit with the given creation_number to the supplied
// game coordinates. Fires the dirty-changed event when this is the first
// pending edit. Returns an error if no entity with that creation_number is
// loaded.
//
// Game-coords contract: x/y/z are already in WC3 game coordinates (centered
// at 0,0), matching the wire format used everywhere in this package. The
// unitsdoo parser stores Position verbatim — no conversion needed here.
func (s *Session) MoveUnit(creationNumber uint32, x, y, z float32) error {
	s.mu.Lock()
	if s.units == nil {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	found := -1
	for i := range s.units.Entities {
		if s.units.Entities[i].CreationNumber == creationNumber {
			found = i
			break
		}
	}
	if found < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no unit with creation_number %d", creationNumber)
	}
	// No-op when the new position matches the current one. The Properties
	// panel commits on blur/Enter even when the user only inspected the
	// input (or pressed Escape, which blurs without a real change), so
	// without this guard the Save pill flips to amber on every focus-out
	// for no actual edit. Bit-exact float compare is fine: parseFloat of
	// the same stringified value round-trips, and the JS side always
	// formats current truth into the input before the user touches it.
	cur := s.units.Entities[found].Position
	if cur[0] == x && cur[1] == y && cur[2] == z {
		s.mu.Unlock()
		return nil
	}
	s.units.Entities[found].Position = [3]float32{x, y, z}
	wasDirty := s.dirtyUnits
	s.dirtyUnits = true
	s.mu.Unlock()
	if !wasDirty {
		s.notifyDirty(true)
	}
	// Notify entity-change subscribers AFTER dirty so the Save pill flips
	// before Properties + scene repaint — keeps the visible-state ordering
	// consistent with the older UI-driven path (which fired dirty via the
	// MoveUnit call, then refreshed the scene via the explicit JS-side
	// scene.updateUnitPosition immediately after).
	s.notifyEntityChanged(EntityChange{
		Kind:     "unit",
		ID:       creationNumber,
		Field:    "position",
		Position: [3]float32{x, y, z},
	})
	return nil
}

// MoveDoodad relocates the doodad with the given creation_number to the
// supplied game coordinates. Fires the dirty-changed event when this is the
// first pending edit (across all per-file dirty flags). Returns an error if
// no doodad with that creation_number is loaded.
//
// Game-coords contract: x/y/z are already in WC3 game coordinates (centered
// at 0,0). doodadsdoo stores Position verbatim — no conversion needed here.
// Mirrors MoveUnit's shape; the only differences are:
//   - we look up creation_number in s.doodads.Doodads (per-kind id namespace —
//     a unit and a doodad can share the same numeric creation_number)
//   - the dirty flag flipped is dirtyDoodads (so Save writes war3map.doo, not
//     war3mapUnits.doo)
//   - the EntityChange kind is "doodad" so scene/Properties subscribers can
//     branch correctly
func (s *Session) MoveDoodad(creationNumber uint32, x, y, z float32) error {
	s.mu.Lock()
	if s.doodads == nil {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	found := -1
	for i := range s.doodads.Doodads {
		if s.doodads.Doodads[i].CreationNumber == creationNumber {
			found = i
			break
		}
	}
	if found < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no doodad with creation_number %d", creationNumber)
	}
	// No-op short-circuit: same rationale as MoveUnit — Properties commits on
	// blur even when nothing changed, and we don't want every focus-out to
	// flip the Save pill to amber.
	cur := s.doodads.Doodads[found].Position
	if cur[0] == x && cur[1] == y && cur[2] == z {
		s.mu.Unlock()
		return nil
	}
	s.doodads.Doodads[found].Position = [3]float32{x, y, z}
	// 0→1 transition of the combined dirty flag (any per-file flag). If the
	// session was already dirty for another reason (e.g. dirtyUnits), this
	// edit doesn't re-fire the public dirty event.
	wasDirty := s.dirtyUnits || s.dirtyDoodads || s.dirtyInfo
	s.dirtyDoodads = true
	s.mu.Unlock()
	if !wasDirty {
		s.notifyDirty(true)
	}
	s.notifyEntityChanged(EntityChange{
		Kind:     "doodad",
		ID:       creationNumber,
		Field:    "position",
		Position: [3]float32{x, y, z},
	})
	return nil
}

// RotateUnit sets the facing angle (radians, Z-axis only) of the unit with the
// given creation_number. Mirrors MoveUnit's shape: no-op short-circuit when
// unchanged, dirty-flip on first edit, entity-changed event after unlock.
//
// WC3 units store a single float32 Rotation (radians around Z). X/Y rotation
// has no on-disk storage — this mutator only accepts Z-axis values.
func (s *Session) RotateUnit(creationNumber uint32, rotation float32) error {
	s.mu.Lock()
	if s.units == nil {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	found := -1
	for i := range s.units.Entities {
		if s.units.Entities[i].CreationNumber == creationNumber {
			found = i
			break
		}
	}
	if found < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no unit with creation_number %d", creationNumber)
	}
	if s.units.Entities[found].Rotation == rotation {
		s.mu.Unlock()
		return nil
	}
	s.units.Entities[found].Rotation = rotation
	wasDirty := s.dirtyUnits
	s.dirtyUnits = true
	pos := s.units.Entities[found].Position
	s.mu.Unlock()
	if !wasDirty {
		s.notifyDirty(true)
	}
	s.notifyEntityChanged(EntityChange{
		Kind:     "unit",
		ID:       creationNumber,
		Field:    "rotation",
		Position: pos,
		Rotation: rotation,
	})
	return nil
}

// RotateDoodad sets the facing angle (radians, Z-axis only) of the doodad with
// the given creation_number. Mirrors MoveDoodad's shape.
//
// NOTE: Some doodad types carry a fixed_rot flag in the SLK type index, which
// restricts valid rotations to a small set of angles (e.g. quarter-turns for
// bridges). The backend stores whatever value it's given — snapping to
// acceptable angles per fixed_rot is the responsibility of the gizmo's commit
// path (Phase C). This comment is the contract notice for Phase C agents.
func (s *Session) RotateDoodad(creationNumber uint32, rotation float32) error {
	s.mu.Lock()
	if s.doodads == nil {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	found := -1
	for i := range s.doodads.Doodads {
		if s.doodads.Doodads[i].CreationNumber == creationNumber {
			found = i
			break
		}
	}
	if found < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no doodad with creation_number %d", creationNumber)
	}
	if s.doodads.Doodads[found].Rotation == rotation {
		s.mu.Unlock()
		return nil
	}
	s.doodads.Doodads[found].Rotation = rotation
	wasDirty := s.dirtyUnits || s.dirtyDoodads || s.dirtyInfo
	s.dirtyDoodads = true
	pos := s.doodads.Doodads[found].Position
	s.mu.Unlock()
	if !wasDirty {
		s.notifyDirty(true)
	}
	s.notifyEntityChanged(EntityChange{
		Kind:     "doodad",
		ID:       creationNumber,
		Field:    "rotation",
		Position: pos,
		Rotation: rotation,
	})
	return nil
}

// ScaleUnit sets the per-axis scale of the unit with the given creation_number.
// Mirrors MoveUnit's shape.
//
// CRITICAL: This mutator clears the entity's scaleRaw preservation field so
// the next Encode emits the new Scale value rather than the original on-disk
// bits. Without this, unitsdoo.Encode would preserve the OLD bytes (scaleRaw
// takes precedence over Scale*128 when non-zero), silently discarding the edit.
// See feedback_unitsdoo_scale_raw.md for the gotcha background.
//
// Doodads do NOT have this issue (their Scale is stored raw, no /128 divide at
// Parse). Only units need scaleRaw invalidation.
func (s *Session) ScaleUnit(creationNumber uint32, sx, sy, sz float32) error {
	s.mu.Lock()
	if s.units == nil {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	found := -1
	for i := range s.units.Entities {
		if s.units.Entities[i].CreationNumber == creationNumber {
			found = i
			break
		}
	}
	if found < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no unit with creation_number %d", creationNumber)
	}
	cur := s.units.Entities[found].Scale
	if cur[0] == sx && cur[1] == sy && cur[2] == sz {
		s.mu.Unlock()
		return nil
	}
	s.units.Entities[found].Scale = [3]float32{sx, sy, sz}
	// Invalidate scaleRaw so Encode derives from the new Scale value.
	// scaleRaw is unexported; we call the package-level helper to set it.
	unitsdoo.ClearScaleRaw(&s.units.Entities[found])
	wasDirty := s.dirtyUnits
	s.dirtyUnits = true
	pos := s.units.Entities[found].Position
	s.mu.Unlock()
	if !wasDirty {
		s.notifyDirty(true)
	}
	s.notifyEntityChanged(EntityChange{
		Kind:  "unit",
		ID:    creationNumber,
		Field: "scale",
		Scale: [3]float32{sx, sy, sz},
		Position: pos,
	})
	return nil
}

// ScaleDoodad sets the per-axis scale of the doodad with the given
// creation_number. Mirrors MoveDoodad's shape.
//
// Doodad Scale is stored RAW on disk (no /128 divide at Parse — the opposite
// convention from unitsdoo). Encode writes Scale verbatim, so no scaleRaw
// invalidation is needed here. The mutator simply sets the public Scale field.
func (s *Session) ScaleDoodad(creationNumber uint32, sx, sy, sz float32) error {
	s.mu.Lock()
	if s.doodads == nil {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	found := -1
	for i := range s.doodads.Doodads {
		if s.doodads.Doodads[i].CreationNumber == creationNumber {
			found = i
			break
		}
	}
	if found < 0 {
		s.mu.Unlock()
		return fmt.Errorf("no doodad with creation_number %d", creationNumber)
	}
	cur := s.doodads.Doodads[found].Scale
	if cur[0] == sx && cur[1] == sy && cur[2] == sz {
		s.mu.Unlock()
		return nil
	}
	s.doodads.Doodads[found].Scale = [3]float32{sx, sy, sz}
	wasDirty := s.dirtyUnits || s.dirtyDoodads || s.dirtyInfo
	s.dirtyDoodads = true
	pos := s.doodads.Doodads[found].Position
	s.mu.Unlock()
	if !wasDirty {
		s.notifyDirty(true)
	}
	s.notifyEntityChanged(EntityChange{
		Kind:  "doodad",
		ID:    creationNumber,
		Field: "scale",
		Scale: [3]float32{sx, sy, sz},
		Position: pos,
	})
	return nil
}

// MutateInfo applies fn to the in-memory war3map.w3i Info under the session
// write lock, flips dirtyInfo, and fires the standard entity-changed event
// so subscribers (Properties panel, header pill, the new Map Info Editor
// dialog) refresh. Mirrors the MoveUnit/MoveDoodad pattern but at the file
// level — the Info struct is a single document, so there's no creation_number
// to disambiguate; the EntityChange payload carries Kind="info", ID=0,
// Field="info".
//
// Returns an error if no map is loaded. The fn callback should be small and
// non-blocking — it runs under the write lock, which serializes every other
// reader/writer in the session.
//
// MUST be the only path that mutates Info — bypassing MutateInfo leaves the
// session in a state where IsDirty() is wrong, the Save button stays
// disabled, and the changes are silently dropped on next Open. Wails app
// methods + bridge handlers + future undo/redo all funnel through here.
//
// No no-op short-circuit: unlike MoveUnit, MutateInfo can't cheaply compare
// "before" and "after" because Info is a deep struct with slices of slices.
// Callers that want no-op detection should diff their inputs themselves
// before calling MutateInfo.
func (s *Session) MutateInfo(fn func(*w3i.Info)) error {
	if fn == nil {
		return fmt.Errorf("MutateInfo: nil fn")
	}
	s.mu.Lock()
	if s.info == nil {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	fn(s.info)
	wasDirty := s.dirtyUnits || s.dirtyDoodads || s.dirtyInfo
	s.dirtyInfo = true
	s.mu.Unlock()
	if !wasDirty {
		s.notifyDirty(true)
	}
	// Map Info changes don't fit the position-only EntityChange shape today,
	// but firing the event lets the same UI subscribers (Properties panel,
	// future Map Info Editor) repaint without inventing a new event channel.
	// Kind="info" / Field="info" keeps the discriminator unambiguous; consumers
	// branch on Kind before reading Position.
	s.notifyEntityChanged(EntityChange{
		Kind:  "info",
		ID:    0,
		Field: "info",
	})
	return nil
}

// IsDirty reports whether the session holds unsaved edits to any in-memory
// map file. The flag is the OR of every per-file dirty flag — the UI cares
// only about "anything to save" granularity. Save itself reads each per-file
// flag and writes only what's dirty.
func (s *Session) IsDirty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dirtyUnits || s.dirtyDoodads || s.dirtyInfo
}

// Save flushes every dirty in-memory file back through the source's write
// path. On success the dirty flag clears and a dirty=false event fires.
//
// Partial-write semantics: if one file writes and another fails, the dirty
// flags for the written files clear (they're now in sync with disk) but the
// failed file stays dirty so the user can retry. The first error is returned;
// successive failures are surfaced via wrapped messages.
//
// MPQ-backed sessions short-circuit with ErrMPQWriteNotImplemented — extract
// the map to a folder first.
func (s *Session) Save() error {
	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return fmt.Errorf("no map loaded")
	}
	if !s.dirtyUnits && !s.dirtyDoodads && !s.dirtyInfo {
		s.mu.Unlock()
		return nil // nothing to do
	}
	src := s.source
	units := s.units
	doodads := s.doodads
	info := s.info
	saveUnits := s.dirtyUnits
	saveDoodads := s.dirtyDoodads
	saveInfo := s.dirtyInfo
	s.mu.Unlock()

	if src == nil {
		return fmt.Errorf("no source for writing")
	}

	// Encode dirty files OUTSIDE the lock so the (potentially-slow) write
	// doesn't block UI reads. Encode is pure over its inputs.
	//
	// Partial-write semantics: each file's dirty flag clears independently
	// only after its successful write. If one file errors, its flag stays
	// set and the user can retry; previously-written files don't get re-
	// written on the retry. First error wins.
	if saveUnits {
		data, err := unitsdoo.Encode(units)
		if err != nil {
			return fmt.Errorf("encode war3mapUnits.doo: %w", err)
		}
		if err := src.write("war3mapUnits.doo", data); err != nil {
			return fmt.Errorf("write war3mapUnits.doo: %w", err)
		}
		s.mu.Lock()
		s.dirtyUnits = false
		s.mu.Unlock()
	}
	if saveDoodads {
		data, err := doodadsdoo.Encode(doodads)
		if err != nil {
			return fmt.Errorf("encode war3map.doo: %w", err)
		}
		if err := src.write("war3map.doo", data); err != nil {
			return fmt.Errorf("write war3map.doo: %w", err)
		}
		s.mu.Lock()
		s.dirtyDoodads = false
		s.mu.Unlock()
	}
	if saveInfo {
		data, err := w3i.Encode(info)
		if err != nil {
			return fmt.Errorf("encode war3map.w3i: %w", err)
		}
		if err := src.write("war3map.w3i", data); err != nil {
			return fmt.Errorf("write war3map.w3i: %w", err)
		}
		s.mu.Lock()
		s.dirtyInfo = false
		s.mu.Unlock()
	}

	// Fire the public dirty=false event only when everything cleared. (If a
	// later per-file write fails we'll have returned above with the partial
	// clear in place; the next Save call clears the rest.)
	s.notifyDirty(false)
	return nil
}

// OnDirtyChanged subscribes to dirty-state-change notifications. Fired
// AFTER the lock is released, so listeners may safely call back into Session.
// Bool argument is the new dirty value (true = pending edits, false = clean).
//
// No-op when the dirty state doesn't actually change (e.g. a second MoveUnit
// when the session is already dirty does not re-fire).
func (s *Session) OnDirtyChanged(fn func(dirty bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirtyListeners = append(s.dirtyListeners, fn)
}

func (s *Session) notifyDirty(dirty bool) {
	s.mu.RLock()
	listeners := make([]func(bool), len(s.dirtyListeners))
	copy(listeners, s.dirtyListeners)
	s.mu.RUnlock()
	for _, fn := range listeners {
		fn(dirty)
	}
}

// OnEntityChanged subscribes to entity-mutation notifications. Fires AFTER the
// session lock is released, so listeners may safely call back into Session.
// Payload carries the (kind, id) of the changed entity, the conceptual field
// that moved, and (for position-field changes) the new coordinates — so
// scene-side subscribers don't need a round-trip fetch to repaint.
//
// Today only Field=="position" is emitted (from MoveUnit). Future mutators
// (rotation, type swap) emit their own Field tags and either populate fields
// added here, or expose their own per-event payload type.
func (s *Session) OnEntityChanged(fn func(EntityChange)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entityListeners = append(s.entityListeners, fn)
}

func (s *Session) notifyEntityChanged(c EntityChange) {
	s.mu.RLock()
	listeners := make([]func(EntityChange), len(s.entityListeners))
	copy(listeners, s.entityListeners)
	s.mu.RUnlock()
	for _, fn := range listeners {
		fn(c)
	}
}

// BridgeCallEvent is the payload for OnBridgeCall — one event per dispatched
// MCP bridge handler. Powers the in-page Agent Console (BridgeConsole.svelte)
// so the user can see, live, what every connected agent is doing.
//
// Field shape kept intentionally flat + JSON-friendly so it rides cleanly
// across the Wails event bus.
//
// ParamsSummary / Result are pre-truncated (~120 chars) — the console is a
// streaming live view, not a full audit log. Heavy payloads (terrain DTOs,
// full unit lists) shouldn't dominate the row width.
type BridgeCallEvent struct {
	Timestamp      time.Time `json:"timestamp"`
	Method         string    `json:"method"`
	ParamsSummary  string    `json:"params_summary"` // "" when the handler took no params
	Result         string    `json:"result"`         // "ok" when the handler returned a void-ish payload
	Error          string    `json:"error"`          // "" on success
	DurationMicros int64     `json:"duration_micros"`
}

// OnBridgeCall subscribes a listener to MCP-handler-dispatch events. Fires
// AFTER the handler completes (success or error), so the listener sees the
// final outcome. Lock+copy pattern matches the other listener buses; listeners
// must not call back into bridge handlers from this callback.
func (s *Session) OnBridgeCall(fn func(BridgeCallEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bridgeCallListeners = append(s.bridgeCallListeners, fn)
}

// notifyBridgeCall fires every BridgeCall listener with the given event.
// Exported (capitalized) so the bridge dispatch wrapper in handlers.go can
// invoke it from the bridge.Handler shim. Lock+copy then invoke outside the
// lock — same shape as notifySelection/notifyDirty/notifyEntityChanged.
func (s *Session) notifyBridgeCall(c BridgeCallEvent) {
	s.mu.RLock()
	listeners := make([]func(BridgeCallEvent), len(s.bridgeCallListeners))
	copy(listeners, s.bridgeCallListeners)
	s.mu.RUnlock()
	for _, fn := range listeners {
		fn(c)
	}
}

// NotifyBridgeCall is the exported wrapper used by the bridge-dispatch shim in
// handlers.go (which lives in the same package but uses this name for
// readability + grep-ability when scanning for "what fires the agent console").
func (s *Session) NotifyBridgeCall(c BridgeCallEvent) {
	s.notifyBridgeCall(c)
}

// OnUICommand subscribes to UI-command events from MCP handlers. Used by App
// to forward `view.*` / `camera.*` MCP calls into the JS-side test-command
// bus. Lock+copy pattern matches the other listener buses.
func (s *Session) OnUICommand(fn func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uiCommandListeners = append(s.uiCommandListeners, fn)
}

// EmitUICommand fires a UI-command string at every subscriber. MCP handlers
// invoke this for surfaces (View menu visibility, terrain/doodad mode, camera
// positioning) whose authoritative state lives in JS.
func (s *Session) EmitUICommand(cmd string) {
	s.mu.RLock()
	listeners := make([]func(string), len(s.uiCommandListeners))
	copy(listeners, s.uiCommandListeners)
	s.mu.RUnlock()
	for _, fn := range listeners {
		fn(cmd)
	}
}
