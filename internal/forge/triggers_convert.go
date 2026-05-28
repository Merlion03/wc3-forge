// Package forge — Convert Map to Lua. Modernizes old JASS maps (FileVersion
// 25 / TFT-era) so they're editable on the current Reforged client and
// wc3-forge's Lua-only codegen.
//
// Two-step protocol:
//
//   1. CheckConvertToLua scans the loaded map's triggers for content that
//      can't be auto-translated (whole-trigger JASS in is_script triggers,
//      per-trigger custom_text overlays on GUI triggers, hand-rolled JASS
//      in war3map.j when there's no .wtg). Returns a Blockers list — empty
//      means safe to convert.
//
//   2. ConvertToLua re-emits the script as Lua via the Phase 3 codegen,
//      writes war3map.lua, deletes war3map.j, sets info.Lua=true. Marks
//      info + script dirty so Save persists. Records ONE undo Command
//      that restores the original war3map.j bytes + info.Lua=false on
//      Revert + drops the generated war3map.lua.
//
// JASS conversion is intentionally out of scope: the trigger-editor surface
// is GUI + Lua only, and translating arbitrary JASS to Lua is a multi-pass
// compiler problem we don't attempt. The blocker dialog asks the user to
// either delete the offending triggers (most are usually third-party
// debugging hooks) or rewrite them by hand before retrying.
package forge

import (
	"errors"
	"fmt"

	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wtg"
)

// ConvertBlocker describes one trigger / synthetic node that blocks
// auto-conversion. Kind discriminates the source:
//
//	"script"       — is_script=true trigger (whole-trigger custom JASS)
//	"custom_text"  — GUI trigger with non-empty CustomText (overlay JASS)
//	"map_header"   — synthetic Map Header script (hand-rolled war3map.j)
//
// Reason is a short human-readable explanation the UI surfaces verbatim.
type ConvertBlocker struct {
	TriggerID   int32  `json:"trigger_id"`
	TriggerName string `json:"trigger_name"`
	Kind        string `json:"kind"`
	Reason      string `json:"reason"`
}

// ConvertToLuaCheckResult is the wire shape both CheckConvertToLua and
// ConvertToLua return. Empty Blockers means the map is safe to convert
// (or, for ConvertToLua, that the conversion succeeded).
type ConvertToLuaCheckResult struct {
	Blockers []ConvertBlocker `json:"blockers"`
}

// ErrConvertBlocked is the sentinel error ConvertToLua returns when blockers
// are present. The Wails wrapper translates this to a non-error response
// carrying the blocker list (the UI prefers a typed payload over an opaque
// error string for the blocker-list dialog).
var ErrConvertBlocked = errors.New("map has unconvertable JASS content")

// ErrAlreadyLua is returned by ConvertToLua when the loaded map is already
// flagged as Lua (info.Lua == true). The UI disables the menu item in this
// case, so this is mostly belt-and-suspenders for direct MCP callers.
var ErrAlreadyLua = errors.New("map is already Lua (info.Lua == true) — nothing to convert")

// CheckConvertToLua scans the loaded map's triggers for content that can't
// be automatically translated from JASS to Lua. Returns an empty Blockers
// slice when the map is safe to convert.
//
// Blockers (we refuse on these):
//   - is_script triggers (whole-trigger JASS): the trigger's custom_text
//     IS the JASS. We can't translate arbitrary JASS to Lua.
//   - GUI triggers with non-empty custom_text: per-trigger custom JASS
//     overlays the GUI code. Same reason.
//   - The synthetic Map Header script (when info.Lua==false and a hand-
//     rolled war3map.j exists): the whole script is hand-written JASS.
//
// NOT blockers:
//   - Comment triggers (CommentString ECAs) — no script content.
//   - Pure GUI triggers (is_script==false, custom_text=="") — the
//     codegen handles every GUI ECA.
//   - Disabled triggers (is_enabled==false) — the codegen skips them, so
//     a disabled is_script trigger doesn't actually appear in output.
//     We still flag it though: the user might re-enable later and the
//     content would re-introduce JASS.
func (s *Session) CheckConvertToLua() (*ConvertToLuaCheckResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return nil, fmt.Errorf("no map loaded")
	}
	res := &ConvertToLuaCheckResult{Blockers: []ConvertBlocker{}}
	// Hand-rolled war3map.j case: no .wtg in the map, the loader synthesized
	// a "Map Header" script trigger holding the raw JASS source. The whole
	// thing is hand-written JASS so the only "convert" we could offer is
	// "drop the script" — which would destroy gameplay. Surface as a single
	// blocker with a clear explanation.
	if s.triggerIsHandRolled {
		name := s.mapHeaderScriptName
		if name == "" {
			name = "war3map.j"
		}
		var tid int32
		if s.triggers != nil && len(s.triggers.Triggers) > 0 {
			tid = s.triggers.Triggers[0].ID
		}
		res.Blockers = append(res.Blockers, ConvertBlocker{
			TriggerID:   tid,
			TriggerName: "Map Header (" + name + ")",
			Kind:        "map_header",
			Reason:      "The whole map script is hand-rolled JASS in " + name + ". Auto-conversion would destroy gameplay logic. Convert the script manually first, or open this map in WC3 World Editor and use Map → Convert to Lua.",
		})
		return res, nil
	}
	if s.triggers == nil {
		// No triggers + no hand-rolled script → conversion is a no-op
		// (the codegen will still emit the scaffolding). Safe.
		return res, nil
	}
	for i := range s.triggers.Triggers {
		tr := &s.triggers.Triggers[i]
		// Comment triggers don't have script content. Skip.
		if tr.IsComment {
			continue
		}
		// is_script trigger — the trigger's custom_text IS the trigger's
		// body (in JASS for a JASS map). We can't translate arbitrary
		// JASS to Lua.
		if tr.IsScript || tr.Classifier == wtg.ClassifierScript {
			res.Blockers = append(res.Blockers, ConvertBlocker{
				TriggerID:   tr.ID,
				TriggerName: tr.Name,
				Kind:        "script",
				Reason:      "Custom-script trigger (whole-trigger JASS). Rewrite it as a GUI trigger, convert it to Lua by hand, or delete it before retrying.",
			})
			continue
		}
		// GUI trigger with non-empty custom_text — the user pasted a JASS
		// snippet on top of the GUI body. Same translation problem.
		if tr.CustomText != "" {
			res.Blockers = append(res.Blockers, ConvertBlocker{
				TriggerID:   tr.ID,
				TriggerName: tr.Name,
				Kind:        "custom_text",
				Reason:      "GUI trigger with a custom-script overlay (JASS in custom_text). Clear the custom_text or rewrite it as Lua by hand before retrying.",
			})
		}
	}
	return res, nil
}

// ConvertToLua re-emits the loaded map's script as Lua:
//   - Generates war3map.lua via the existing Phase 3 codegen.
//   - Writes war3map.lua to the map source.
//   - Deletes war3map.j from the map source.
//   - Sets info.Lua = true.
//
// Marks info + script dirty so Save persists. Records a single Command for
// undo (which restores the original war3map.j + reverts info.Lua + drops
// the generated war3map.lua).
//
// Returns ErrConvertBlocked + the blocker list embedded in the result when
// CheckConvertToLua finds unconvertable content. The Wails wrapper unpacks
// this into a non-error response so the UI can show the dedicated blocker
// dialog rather than a generic toast.
//
// Returns ErrAlreadyLua if the map is already flagged as Lua.
func (s *Session) ConvertToLua() (*ConvertToLuaCheckResult, error) {
	// CheckConvertToLua takes a read lock; release before re-acquiring under
	// a write lock for the mutation. The check is racy if a concurrent
	// trigger mutation lands between Check and Convert, but the Trigger
	// Editor is modal in practice + this is a one-shot user action.
	check, err := s.CheckConvertToLua()
	if err != nil {
		return nil, err
	}
	if len(check.Blockers) > 0 {
		return check, ErrConvertBlocked
	}

	s.mu.Lock()
	if !s.loaded {
		s.mu.Unlock()
		return nil, fmt.Errorf("no map loaded")
	}
	if s.info == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("no map info loaded")
	}
	if s.info.Lua {
		s.mu.Unlock()
		return nil, ErrAlreadyLua
	}
	src := s.source
	if src == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("no source for writing")
	}
	// Snapshot the on-disk war3map.j so Revert can restore byte-for-byte.
	// Use the source's read path so MPQ-backed sessions go through the
	// same abstraction (they'd fail at write/delete time anyway). When
	// the map has no war3map.j on disk (extremely rare for a JASS-flagged
	// map, but defensively handled), originalJASS stays nil — Revert
	// will delete the synthesized war3map.lua and not write anything.
	originalJASS, hadJASS, readErr := src.read("war3map.j")
	if readErr != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("read war3map.j: %w", readErr)
	}
	if !hadJASS {
		originalJASS = nil
	}
	// Build codegen inputs identically to GenerateTriggerScript, except we
	// temporarily flip info.Lua=true so the codegen accepts the call (it
	// refuses with ErrLuaOnly when info.Lua is false). We don't mutate
	// s.info under the lock yet — that flip is part of the convert step
	// below and needs to be on the Command for undo.
	in := CodegenInputs{
		Triggers: s.triggers,
		Units:    s.units,
		Doodads:  s.doodads,
		Terrain:  s.terrain,
		Info:     s.info,
		Regions:  s.regions,
		Cameras:  s.cameras,
		TD:       TriggerDataSnapshot(),
	}
	if s.triggersWct != nil {
		in.GlobalJASS = s.triggersWct.GlobalJASS
	}
	// Honor the same preserve-marker peek that GenerateTriggerScript does.
	// A map with a hand-edited war3map.lua marked with PreserveScriptMarker
	// should not be silently overwritten — even when we're converting from
	// JASS, the marker means "don't touch the .lua file". Surface a clear
	// error so the user removes the marker first.
	if existing, ok, _ := src.read("war3map.lua"); ok && len(existing) > 0 {
		first := existing
		for i, b := range existing {
			if b == '\n' {
				first = existing[:i]
				break
			}
		}
		if string(first) == PreserveScriptMarker {
			s.mu.Unlock()
			return nil, ErrPreserveScript
		}
	}
	// Spoof Info.Lua=true for the codegen call. GenerateLuaScript refuses
	// (with ErrLuaOnly) when Info.Lua is false, but the whole point of
	// this method is to flip JASS maps to Lua — we need to run the codegen
	// before flipping the in-memory flag. Hand the codegen a shallow copy
	// of Info with Lua=true; the codegen only reads from Info, so a shallow
	// copy is safe (it shares the players/forces slices with the original).
	infoCopy := *s.info
	infoCopy.Lua = true
	in.Info = &infoCopy
	s.mu.Unlock()

	luaText, err := GenerateLuaScript(in)
	if err != nil {
		return nil, fmt.Errorf("generate war3map.lua: %w", err)
	}

	// Re-acquire the write lock for the mutation + Command record. Apply
	// the changes (lua write, j delete, info flag flip) inside the command's
	// Apply method so undo/redo flows through identical code.
	s.mu.Lock()
	if !s.loaded || s.info == nil || s.source == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("map closed during conversion")
	}
	cmd := &convertToLuaCmd{
		originalJASS:    originalJASS,
		hadOriginalJASS: hadJASS,
		generatedLua:    []byte(luaText),
		wasLua:          s.info.Lua, // false by construction (checked above)
		oldFileVersion:  s.info.FileVersion,
	}
	if err := cmd.Apply(s); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	wasDirty := s.anyDirtyLocked()
	s.dirtyInfo = true
	s.mapHeaderScriptDirty = false // conversion isn't a hand-rolled-script edit
	s.recordCommand(cmd)
	historyChanged := s.groupDepth == 0
	s.mu.Unlock()
	if !wasDirty {
		s.notifyDirty(true)
	}
	s.notifyEntityChanged(EntityChange{Kind: "info", ID: 0, Field: "lua"})
	if historyChanged {
		s.notifyHistoryChanged()
	}
	return &ConvertToLuaCheckResult{Blockers: []ConvertBlocker{}}, nil
}

// convertToLuaCmd captures everything needed to undo a Convert-to-Lua step:
//   - the original war3map.j bytes (nil + hadOriginalJASS=false when the map
//     had no .j file on disk to begin with)
//   - the generated war3map.lua bytes (so Apply/Redo regenerates byte-for-byte
//     instead of re-running the codegen, which is expensive and may produce
//     different output if other state has changed since the original convert)
//   - the wasLua flag (always false in practice — ConvertToLua refuses when
//     info.Lua is already true — but stored for symmetry)
//
// Apply (initial run + Redo): writes war3map.lua, deletes war3map.j, flips
// info.Lua=true, sets dirtyInfo + clears the script file from dirty state
// (we just wrote it directly).
//
// Revert: writes war3map.j back (if hadOriginalJASS), deletes war3map.lua,
// flips info.Lua=false. Marks dirtyInfo so Save re-writes the w3i with the
// reverted flag.
//
// IMPORTANT: This Command writes files DIRECTLY rather than going through
// the Save loop. The rationale: ConvertToLua is a coarse "modernize the
// map" operation — leaving war3map.j on disk until next Save would produce
// a transient invalid state where both .j and .lua exist + info.Lua=true,
// which WC3 itself doesn't reliably handle. We commit the file shuffle
// atomically with the in-memory flag flip; only the info file's encode-to-
// disk happens on Save (it's a small struct and matches the other dirty
// patterns in session.go).
type convertToLuaCmd struct {
	originalJASS    []byte
	hadOriginalJASS bool
	generatedLua    []byte
	wasLua          bool
	// oldFileVersion captures the .w3i FileVersion BEFORE conversion. The
	// Lua flag is only written when FileVersion >= FileVersionRefV28, so a
	// TFT-era (v25) map must be bumped to v28 before Save flushes Lua=true.
	// Undo restores the original version so the round-trip is faithful.
	oldFileVersion int32
}

func (c *convertToLuaCmd) Label() string { return "Convert to Lua" }

// Apply writes the .lua, deletes the .j, flips Lua=true. Caller holds s.mu.
// Errors abort early — partial state is possible (e.g. wrote .lua but
// failed to delete .j) but the caller is responsible for retrying via
// Save / re-running the convert flow. Each step is idempotent (writes
// overwrite; delete tolerates absent file).
func (c *convertToLuaCmd) Apply(s *Session) error {
	if s.source == nil || s.info == nil {
		return fmt.Errorf("convert: no map loaded")
	}
	if err := s.source.write("war3map.lua", c.generatedLua); err != nil {
		return fmt.Errorf("write war3map.lua: %w", err)
	}
	if err := s.source.delete("war3map.j"); err != nil {
		return fmt.Errorf("delete war3map.j: %w", err)
	}
	s.info.Lua = true
	// The Lua flag is only serialized at FileVersion >= 28 (encode.go L119).
	// Bump older maps so the persisted .w3i reflects Lua=true on next Save.
	// Most TFT-era maps are FileVersion=25; modern Reforged maps are >=28.
	if s.info.FileVersion < w3i.FileVersionRefV28 {
		s.info.FileVersion = w3i.FileVersionRefV28
	}
	s.dirtyInfo = true
	return nil
}

// Revert restores war3map.j, deletes war3map.lua, flips Lua=false. Caller
// holds s.mu. Symmetric to Apply: each step is idempotent, errors abort
// early (the user can re-run Undo after fixing the underlying disk issue).
func (c *convertToLuaCmd) Revert(s *Session) error {
	if s.source == nil || s.info == nil {
		return fmt.Errorf("convert revert: no map loaded")
	}
	if c.hadOriginalJASS {
		if err := s.source.write("war3map.j", c.originalJASS); err != nil {
			return fmt.Errorf("restore war3map.j: %w", err)
		}
	} else {
		// Belt-and-suspenders: defensively drop any war3map.j that might
		// exist on disk now (e.g. a sibling process wrote one between
		// Apply and Revert). Tolerates absent file.
		if err := s.source.delete("war3map.j"); err != nil {
			return fmt.Errorf("clear war3map.j: %w", err)
		}
	}
	if err := s.source.delete("war3map.lua"); err != nil {
		return fmt.Errorf("delete war3map.lua: %w", err)
	}
	s.info.Lua = c.wasLua
	s.info.FileVersion = c.oldFileVersion
	s.dirtyInfo = true
	return nil
}

// Affected returns the entity-change event the UI subscribes to for the
// Lua-flag flip. Kind="info" + Field="lua" is a new tag specific to this
// command; existing info-event subscribers (Properties panel, Map Info
// dialog) don't branch on Field beyond "info" so they'll repaint anyway.
func (c *convertToLuaCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "info", ID: 0, Field: "lua"}}
}

// Compile-time assertion that convertToLuaCmd satisfies the Command
// interface. Without this a typo in any of the four method names would
// silently break the history machinery.
var _ Command = (*convertToLuaCmd)(nil)