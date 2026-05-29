package forge

import (
	"fmt"
	"os"
	"strings"

	"github.com/StephenSHorton/wc3-forge/internal/formats/mpq"
)

// This file holds the MPQ-backed save path: how a *mpqSource (defined in
// session.go) turns its buffered pending writes + tombstones into a freshly
// repacked .w3x on disk. Kept out of session.go's body to limit churn in that
// hot file while several agents edit it in parallel.

// mpqNameKey produces the case/slash-insensitive comparison key the buffered
// pending/deleted maps are keyed by. Mirrors the reader's normalisation
// (forward slash -> backslash, ASCII upper-fold) so "war3map.w3i" and
// "WAR3MAP.W3I" address the same buffered entry. The original (un-folded) name
// is preserved separately for re-packing — see flush, which tracks display
// names alongside.
func mpqNameKey(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "\\")
	return strings.ToUpper(name)
}

// flush repacks the entire archive and atomically replaces the .w3x on disk.
//
// Why repack-the-world rather than splice individual files: the MPQ writer in
// internal/formats/mpq builds a fresh, self-consistent archive (header + hash
// table + block table + data) from a flat name->bytes set. Splicing one file
// into an existing archive in place would mean rewriting the hash/block tables
// AND every offset anyway, so a clean rebuild is both simpler and safer.
//
// Source of the file set:
//   - Every name the open archive declares (a.List() — the union of its
//     internal (listfile) and the standard WC3 name set, filtered to names
//     that actually resolve).
//   - Overlaid with this source's pending writes (new/changed bytes).
//   - Minus this source's tombstones (deleted files).
//
// The original HM3W/user-data preheader (the bytes before the MPQ header) is
// copied through verbatim so the lobby preview and any custom shunt survive.
//
// Atomicity + DO-NO-HARM: the repack writes to a sibling temp file and renames
// over the original only on full success (mpq.WriteFile handles this). If ANY
// step fails the original .w3x is left byte-for-byte untouched and the buffered
// changes stay pending so a later flush can retry. On success the buffers are
// cleared and the in-process archive handle is reopened so subsequent reads see
// the committed bytes.
//
// flush takes the source lock and delegates to flushLocked. forceRepackAll also
// holds the lock while it pre-populates the buffer, so the actual repack work
// lives in flushLocked (which assumes the lock is already held — sync.Mutex is
// not reentrant).
func (m *mpqSource) flush() error {
	if m == nil {
		return fmt.Errorf("mpq flush: nil source")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flushLocked()
}

// flushLocked is the repack body. Caller MUST hold m.mu.
func (m *mpqSource) flushLocked() error {
	// Fast path: nothing buffered means the on-disk archive already matches
	// the in-memory view; no repack needed.
	if len(m.pending) == 0 && len(m.deleted) == 0 {
		return nil
	}
	if m.path == "" {
		return fmt.Errorf("%w: mpq source has no path to write to", ErrMPQWriteNotImplemented)
	}
	if m.a == nil {
		return fmt.Errorf("%w: mpq source has no open archive to repack", ErrMPQWriteNotImplemented)
	}

	// Assemble the full file set, keyed by the normalised name so an edited
	// file (pending) replaces its archive copy exactly once.
	type fileBuf struct {
		name string // display (un-folded) name for the new archive + listfile
		data []byte
	}
	collected := make(map[string]fileBuf)

	addFromArchive := func(name string) error {
		key := mpqNameKey(name)
		if _, dup := collected[key]; dup {
			return nil
		}
		if m.deleted[key] {
			return nil // tombstoned — drop it
		}
		if b, ok := m.pending[key]; ok {
			collected[key] = fileBuf{name: name, data: append([]byte(nil), b...)}
			return nil
		}
		b, err := m.a.Read(name)
		if err != nil {
			return fmt.Errorf("read %q for repack: %w", name, err)
		}
		collected[key] = fileBuf{name: name, data: b}
		return nil
	}

	// 1) Every declared name from the open archive (skips (listfile)/
	//    (attributes) per the reader's List() contract — we regenerate the
	//    listfile in the writer).
	for _, name := range m.a.List() {
		if strings.EqualFold(name, "(listfile)") || strings.EqualFold(name, "(attributes)") {
			continue
		}
		if err := addFromArchive(name); err != nil {
			return err
		}
	}

	// 2) Any pending writes for files the archive didn't already declare
	//    (newly-created files, e.g. a war3map.lua added by Convert-to-Lua on
	//    a map that previously had none). We don't have the original display
	//    name for these, so the normalised key doubles as the name — it is a
	//    valid storage path (uppercase is fine; the engine folds case).
	//    Prefer a lower-cased rendering for the common war3map.* set so the
	//    listfile reads naturally; fall back to the key otherwise.
	for key, data := range m.pending {
		if _, already := collected[key]; already {
			continue
		}
		if m.deleted[key] {
			continue
		}
		collected[key] = fileBuf{name: displayNameForKey(key), data: append([]byte(nil), data...)}
	}

	// Flatten into the writer's entry slice.
	entries := make([]mpq.FileEntry, 0, len(collected))
	for _, fb := range collected {
		entries = append(entries, mpq.FileEntry{Name: fb.name, Data: fb.data})
	}
	if len(entries) == 0 {
		return fmt.Errorf("%w: refusing to write an empty archive (no files collected)", ErrMPQWriteNotImplemented)
	}

	// Preserve the original preheader (HM3W / user-data shunt) verbatim. We
	// read it from disk NOW, while the archive handle is still open and the
	// file definitely exists, and stash the bytes in memory.
	var preHeader []byte
	if off := m.a.ArchiveOffset(); off > 0 {
		raw, err := os.ReadFile(m.path)
		if err != nil {
			return fmt.Errorf("read original preheader from %q: %w", m.path, err)
		}
		if int64(len(raw)) < off {
			return fmt.Errorf("original file %q shorter (%d) than its archive offset (%d)", m.path, len(raw), off)
		}
		preHeader = append([]byte(nil), raw[:off]...)
	}

	// CRITICAL (Windows): the reader holds an open os.File handle on m.path.
	// Windows refuses to os.Rename a temp file ONTO a path that has an open
	// handle ("Access is denied"). All file bytes are already collected into
	// memory above, so we can safely close the archive here, releasing the
	// handle, before WriteFile's atomic rename. We reopen afterwards.
	//
	// DO NO HARM: WriteFile writes to a sibling temp file and only renames on
	// full success. If WriteFile fails, m.path is untouched; we reopen the
	// (unchanged) original below so the session stays usable and the buffered
	// edits remain pending for a retry.
	_ = m.a.Close()
	m.a = nil

	writeErr := mpq.WriteFile(m.path, entries, preHeader)

	// Reopen the archive (the new bytes on success, the untouched original on
	// failure) so subsequent reads have a live handle again.
	reopened, openErr := mpq.Open(m.path)
	if openErr == nil {
		m.a = reopened
	}

	if writeErr != nil {
		// Leave buffers in place for a retry; surface the write error.
		return fmt.Errorf("repack %q: %w", m.path, writeErr)
	}
	if openErr != nil {
		// The repack succeeded on disk but we couldn't reopen — surface it so
		// the caller knows the in-process handle is stale (the file itself is
		// correct and a fresh Open of the map will work).
		return fmt.Errorf("repack %q succeeded but reopen failed: %w", m.path, openErr)
	}

	m.pending = make(map[string][]byte)
	m.deleted = make(map[string]bool)
	return nil
}

// forceRepackAll rewrites the archive even when nothing is buffered. Used by
// the clean-save path so a "Save" on an unedited .w3x still produces a real,
// freshly-packed archive at the source path (rather than a misleading no-op).
//
// It works by reading every declared file into the pending buffer and then
// running the normal flush. Because flush reads pending-first, this guarantees
// a byte-faithful copy of the current contents into the new archive. If a read
// fails the whole operation aborts before any disk write (DO NO HARM).
func (m *mpqSource) forceRepackAll() error {
	if m == nil {
		return fmt.Errorf("%w: nil source", ErrMPQWriteNotImplemented)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.a == nil {
		return fmt.Errorf("%w: no archive to repack", ErrMPQWriteNotImplemented)
	}
	for _, name := range m.a.List() {
		if strings.EqualFold(name, "(listfile)") || strings.EqualFold(name, "(attributes)") {
			continue
		}
		key := mpqNameKey(name)
		if _, ok := m.pending[key]; ok {
			continue
		}
		if m.deleted[key] {
			continue
		}
		b, err := m.a.Read(name)
		if err != nil {
			return fmt.Errorf("read %q for full repack: %w", name, err)
		}
		m.pending[key] = b
	}
	return m.flushLocked()
}

// displayNameForKey renders a normalised (uppercase, backslash) name key back
// into a friendly storage path for files that originated as pending writes
// without a known original casing. We special-case the well-known war3map.*
// lowercase convention so a freshly-added script lists as "war3map.lua" rather
// than "WAR3MAP.LUA"; everything else keeps the key. Case is irrelevant to the
// engine (it folds on lookup) — this is purely cosmetic for the (listfile).
func displayNameForKey(key string) string {
	// The key uses backslash separators already.
	if strings.HasPrefix(key, "WAR3MAP") {
		return strings.ToLower(key)
	}
	return key
}
