// Package forge — Convert Map to Lua. Modernizes old JASS maps (FileVersion
// 25 / TFT-era) so they're editable on the current Reforged client and
// wc3-forge's Lua-only codegen.
//
// Two-step protocol:
//
//   1. CheckConvertToLua scans the loaded map's triggers for vJASS-specific
//      content that can't be auto-translated (the JassHelper preprocessor is
//      out of scope). Pure JASS — function/if/loop/set/call/return/globals —
//      is NOT a blocker; the jass2lua transpiler handles it.
//
//   2. ConvertToLua transpiles every pure-JASS section, concatenates with the
//      GUI codegen output, writes war3map.lua, deletes war3map.j, sets
//      info.Lua=true. Optionally backs up the map source first (folder copy
//      or .w3x file copy). Marks info + script dirty so Save persists.
//      Records ONE undo Command that restores the original war3map.j bytes +
//      info.Lua=false on Revert + drops the generated war3map.lua. The backup
//      file is NOT auto-deleted on Undo — it stays as the user's belt-and-
//      suspenders fallback.
//
// JASS-vs-vJASS triage is driven by jass2lua.FindVJASSKeyword (the keyword
// regex is centralized there alongside the transpiler so both stay in sync).
package forge

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/StephenSHorton/wc3-forge/internal/forge/jass2lua"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wtg"
)

// ConvertBlocker describes one trigger / synthetic node that blocks
// auto-conversion. Kind discriminates the source:
//
//	"script_vjass"      — is_script=true trigger whose body uses vJASS keywords
//	"custom_text_vjass" — GUI trigger whose CustomText overlay uses vJASS
//	"map_header_vjass"  — hand-rolled war3map.j containing vJASS
//	"global_jass_vjass" — war3map.wct GlobalJASS block containing vJASS
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
var ErrConvertBlocked = errors.New("map has unconvertable vJASS content")

// ErrAlreadyLua is returned by ConvertToLua when the loaded map is already
// flagged as Lua (info.Lua == true). The UI disables the menu item in this
// case, so this is mostly belt-and-suspenders for direct MCP callers.
var ErrAlreadyLua = errors.New("map is already Lua (info.Lua == true) — nothing to convert")

// ErrBackupFailed is returned when the backup step fails. We refuse to proceed
// to a conversion the user can't easily undo.
var ErrBackupFailed = errors.New("backup failed; refusing to convert without a backup the user asked for")

// CheckConvertToLua scans the loaded map for vJASS-specific script content
// (the JassHelper preprocessor is out of scope; pure JASS is transpiled
// automatically). Returns an empty Blockers slice when the map is safe to
// convert.
//
// Blockers (we refuse on these):
//   - is_script triggers using vJASS keywords (library, struct, scope, etc.)
//   - GUI triggers whose CustomText overlay uses vJASS keywords
//   - Hand-rolled war3map.j containing vJASS
//   - war3map.wct GlobalJASS header containing vJASS
//
// NOT blockers (handled by the transpiler):
//   - is_script triggers with pure JASS (function/if/loop/set/call/etc.)
//   - GUI triggers with pure-JASS CustomText overlays
//   - Pure-JASS hand-rolled war3map.j
//   - Pure-JASS GlobalJASS
//   - Pure-GUI triggers with no script content
//   - Comment triggers
func (s *Session) CheckConvertToLua() (*ConvertToLuaCheckResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return nil, fmt.Errorf("no map loaded")
	}
	res := &ConvertToLuaCheckResult{Blockers: []ConvertBlocker{}}

	// Hand-rolled war3map.j case: scan the synthesized Map Header trigger's
	// custom_text for vJASS. Pure-JASS hand-rolled maps are now convertible
	// (the transpiler handles them). The textmacro preprocessor runs FIRST
	// so a textmacro-only header expands to pure JASS and stops blocking.
	if s.triggerIsHandRolled {
		if s.triggers != nil && len(s.triggers.Triggers) > 0 {
			tr := &s.triggers.Triggers[0]
			if kw, found := scanVJASSAfterPreprocess(tr.CustomText); found {
				name := s.mapHeaderScriptName
				if name == "" {
					name = "war3map.j"
				}
				res.Blockers = append(res.Blockers, ConvertBlocker{
					TriggerID:   tr.ID,
					TriggerName: "Map Header (" + name + ")",
					Kind:        "map_header_vjass",
					Reason:      "war3map.j uses the vJASS keyword `" + kw + "`. vJASS requires the JassHelper preprocessor; auto-transpile to Lua isn't supported. Rewrite the script as pure JASS or Lua first.",
				})
			}
		}
		return res, nil
	}

	if s.triggers == nil {
		// No triggers and no hand-rolled script → trivially safe.
		return res, nil
	}

	// Per-trigger vJASS scan. We preprocess each section before scanning so
	// textmacro-only triggers no longer block the convert flow.
	for i := range s.triggers.Triggers {
		tr := &s.triggers.Triggers[i]
		if tr.IsComment {
			continue
		}
		if (tr.IsScript || tr.Classifier == wtg.ClassifierScript) && tr.CustomText != "" {
			if kw, found := scanVJASSAfterPreprocess(tr.CustomText); found {
				res.Blockers = append(res.Blockers, ConvertBlocker{
					TriggerID:   tr.ID,
					TriggerName: tr.Name,
					Kind:        "script_vjass",
					Reason:      "Custom-script trigger uses the vJASS keyword `" + kw + "`. vJASS needs the JassHelper preprocessor; rewrite as pure JASS or Lua, or delete this trigger before retrying.",
				})
			}
			continue
		}
		if tr.CustomText != "" {
			if kw, found := scanVJASSAfterPreprocess(tr.CustomText); found {
				res.Blockers = append(res.Blockers, ConvertBlocker{
					TriggerID:   tr.ID,
					TriggerName: tr.Name,
					Kind:        "custom_text_vjass",
					Reason:      "GUI trigger has a custom-script overlay using the vJASS keyword `" + kw + "`. Clear the overlay or rewrite it as pure JASS before retrying.",
				})
			}
		}
	}

	// GlobalJASS in war3map.wct — also subject to the vJASS gate.
	if s.triggersWct != nil && strings.TrimSpace(s.triggersWct.GlobalJASS) != "" {
		if kw, found := scanVJASSAfterPreprocess(s.triggersWct.GlobalJASS); found {
			res.Blockers = append(res.Blockers, ConvertBlocker{
				TriggerID:   -1,
				TriggerName: "war3map.wct GlobalJASS",
				Kind:        "global_jass_vjass",
				Reason:      "war3map.wct's global JASS header uses the vJASS keyword `" + kw + "`. Rewrite as pure JASS first.",
			})
		}
	}

	return res, nil
}

// scanVJASSAfterPreprocess runs every vJASS preprocessor pass in order
// (textmacro expansion → library/scope wrapping → struct expansion) and then
// scans the final expanded source for vJASS keywords the transpiler still
// can't handle. This is what makes textmacro-only AND library/scope-only AND
// struct-only sections stop blocking — they vanish during their respective
// preprocess passes, leaving pure JASS behind.
//
// We deliberately ignore preprocess errors here: an unknown `//!` directive,
// an unknown runtextmacro call, an unresolved `requires`, a struct parse
// error — none should itself cause the blocker check to fail (the user sees
// those as warnings in the diff UI). What matters for the blocker gate is
// whether the triply-expanded source still contains vJASS keywords from
// Phase 4 (module, interface, define).
func scanVJASSAfterPreprocess(src string) (string, bool) {
	if src == "" {
		return "", false
	}
	pp := jass2lua.Preprocess(src)
	ls := jass2lua.PreprocessLibScope(pp.Expanded)
	st := jass2lua.PreprocessStructs(ls.Expanded)
	return jass2lua.FindVJASSKeyword(st.Expanded)
}

// TranspilePreview is the read-only DTO returned by TranspilePreview() — every
// pure-JASS section the convert flow would translate, with both sides of the
// diff prepared. ConvertToLua's actual write path re-runs this internally to
// stay deterministic.
type TranspilePreview struct {
	Sections []TranspileSection `json:"sections"`
}

// TranspileSection is one piece of the preview. ID:
//
//	>=0 — trigger id (per-trigger script trigger or GUI custom_text overlay)
//	-1  — war3map.wct GlobalJASS header
//	-2  — hand-rolled war3map.j (entire file)
//
// Errors is non-nil only when the transpiler flagged trouble; UI uses it to
// warn the user before they hit Convert.
//
// PreprocessWarnings carries non-fatal diagnostics from the textmacro
// preprocessor (unknown macro, arg-count mismatch, unrecognized `//!`
// directive, recursion limit). Empty when the section didn't use macros
// (or used them cleanly). The UI surfaces these as a small warning bar
// above the diff editor; they do NOT block conversion.
//
// LibScopeWarnings is the analogous bucket for the Phase-2 library/scope
// preprocessor (unresolved requires, dep cycles, missing endlibrary,
// stray closer at top level). Independent from PreprocessWarnings so the
// UI can render them as separate amber bars and the user can tell which
// pass produced which diagnostic.
//
// StructWarnings is the analogous bucket for the Phase-3 struct preprocessor
// (unsupported `extends array`, `delegate` member, missing `endstruct`,
// unrecognized struct body line). Independent so the UI can show a third
// amber bar.
//
// InitOrder lists the libraries detected in this section in dep-respecting
// order. The aggregate across all sections drives the synthetic init
// function the codegen emits — see emit.go's
// emitLibScopeInitFunction call.
//
// StructInits lists the structs that defined `static method onInit`; the
// aggregate is threaded through to the codegen so each Foo.onInit() fires
// after library inits (and AFTER the struct snippet is itself defined,
// since the snippet creates the static method).
type TranspileSection struct {
	ID                 int32                  `json:"id"`
	Label              string                 `json:"label"`
	Kind               string                 `json:"kind"` // "script", "custom_text", "global_jass", "map_header"
	Original           string                 `json:"original"`
	Transpiled         string                 `json:"transpiled"`
	Errors             []string               `json:"errors,omitempty"`
	PreprocessWarnings []string               `json:"preprocess_warnings,omitempty"`
	LibScopeWarnings   []string               `json:"libscope_warnings,omitempty"`
	StructWarnings     []string               `json:"struct_warnings,omitempty"`
	InitOrder          []jass2lua.LibraryInit `json:"init_order,omitempty"`
	StructInits        []jass2lua.StructInit  `json:"struct_inits,omitempty"`
}

// TranspilePreview returns a pure-read diff payload for the convert dialog.
// Empty Sections when the map has no transpilable script content (a pure-GUI
// map). The preview never writes to disk.
func (s *Session) TranspilePreview() (*TranspilePreview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return nil, fmt.Errorf("no map loaded")
	}
	out := &TranspilePreview{Sections: []TranspileSection{}}

	// Hand-rolled map_header path.
	if s.triggerIsHandRolled {
		if s.triggers != nil && len(s.triggers.Triggers) > 0 {
			tr := &s.triggers.Triggers[0]
			if strings.TrimSpace(tr.CustomText) != "" {
				sec := transpileSectionScript(tr.ID, "Map Header (war3map.j)", "map_header", tr.CustomText)
				out.Sections = append(out.Sections, sec)
			}
		}
		return out, nil
	}

	// Global JASS header (war3map.wct) — top of the diff so the user sees it first.
	if s.triggersWct != nil && strings.TrimSpace(s.triggersWct.GlobalJASS) != "" {
		sec := transpileSectionScript(-1, "war3map.wct GlobalJASS", "global_jass", s.triggersWct.GlobalJASS)
		out.Sections = append(out.Sections, sec)
	}

	if s.triggers == nil {
		return out, nil
	}

	// Per-trigger sections.
	for i := range s.triggers.Triggers {
		tr := &s.triggers.Triggers[i]
		if tr.IsComment {
			continue
		}
		if (tr.IsScript || tr.Classifier == wtg.ClassifierScript) && strings.TrimSpace(tr.CustomText) != "" {
			sec := transpileSectionScript(tr.ID, "Custom Script: "+tr.Name, "script", tr.CustomText)
			out.Sections = append(out.Sections, sec)
			continue
		}
		if strings.TrimSpace(tr.CustomText) != "" {
			// GUI trigger with custom_text overlay — auto-wrap as a function.
			sec := transpileSectionCustomText(tr.ID, tr.Name, tr.CustomText)
			out.Sections = append(out.Sections, sec)
		}
	}
	return out, nil
}

// transpileSectionScript runs the full preprocessor pipeline for a section
// whose body is a full JASS script (function decls + globals). The textmacro
// + libscope + struct passes run first, then TranspileScript on the
// triply-expanded output. The struct-Lua snippets are spliced into the
// transpiled output at the marker positions PreprocessStructs left behind.
// Diagnostics from each pass surface separately on the section.
func transpileSectionScript(id int32, label, kind, src string) TranspileSection {
	pp := jass2lua.Preprocess(src)
	ls := jass2lua.PreprocessLibScope(pp.Expanded)
	st := jass2lua.PreprocessStructs(ls.Expanded)
	lua, err := jass2lua.TranspileScript(st.Expanded)
	if err == nil || lua != "" {
		lua = jass2lua.SpliceStructLua(lua, st)
	}
	sec := TranspileSection{
		ID:                 id,
		Label:              label,
		Kind:               kind,
		Original:           src,
		Transpiled:         lua,
		PreprocessWarnings: preprocessWarningsToStrings(pp.Errors),
		LibScopeWarnings:   libScopeWarningsToStrings(ls.Errors),
		StructWarnings:     structWarningsToStrings(st.Errors),
		InitOrder:          ls.InitOrder,
		StructInits:        st.Inits,
	}
	if err != nil {
		sec.Errors = append(sec.Errors, err.Error())
	}
	return sec
}

// transpileSectionCustomText is the equivalent for GUI-overlay custom_text
// sections, which are bare statement lists rather than full scripts. The
// library/scope + struct preprocessors still run (an overlay can contain a
// library or struct, though it's rare) before falling through to
// TranspileFunction. Splice struct Lua afterwards.
func transpileSectionCustomText(id int32, triggerName, src string) TranspileSection {
	pp := jass2lua.Preprocess(src)
	ls := jass2lua.PreprocessLibScope(pp.Expanded)
	st := jass2lua.PreprocessStructs(ls.Expanded)
	lua, err := jass2lua.TranspileFunction(triggerName+"_CustomText", st.Expanded)
	if err == nil || lua != "" {
		lua = jass2lua.SpliceStructLua(lua, st)
	}
	sec := TranspileSection{
		ID:                 id,
		Label:              "Custom Text: " + triggerName,
		Kind:               "custom_text",
		Original:           src,
		Transpiled:         lua,
		PreprocessWarnings: preprocessWarningsToStrings(pp.Errors),
		LibScopeWarnings:   libScopeWarningsToStrings(ls.Errors),
		StructWarnings:     structWarningsToStrings(st.Errors),
		InitOrder:          ls.InitOrder,
		StructInits:        st.Inits,
	}
	if err != nil {
		sec.Errors = append(sec.Errors, err.Error())
	}
	return sec
}

// preprocessWarningsToStrings flattens PreprocessError diagnostics into the
// "line N: message" strings the UI surfaces. Returns nil for the no-warning
// case so the JSON omits the field entirely (json:"...,omitempty").
func preprocessWarningsToStrings(errs []jass2lua.PreprocessError) []string {
	if len(errs) == 0 {
		return nil
	}
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Line > 0 {
			out = append(out, fmt.Sprintf("line %d: %s", e.Line, e.Message))
		} else {
			out = append(out, e.Message)
		}
	}
	return out
}

// libScopeWarningsToStrings flattens LibScopeError diagnostics into the
// "line N: message" strings the UI surfaces. Same shape as
// preprocessWarningsToStrings; kept as a separate function so changes to
// either pass's diagnostic shape don't ripple across both.
func libScopeWarningsToStrings(errs []jass2lua.LibScopeError) []string {
	if len(errs) == 0 {
		return nil
	}
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Line > 0 {
			out = append(out, fmt.Sprintf("line %d: %s", e.Line, e.Message))
		} else {
			out = append(out, e.Message)
		}
	}
	return out
}

// structWarningsToStrings flattens StructError diagnostics into the
// "line N: message" strings the UI surfaces. Same shape as the other
// per-pass helpers.
func structWarningsToStrings(errs []jass2lua.StructError) []string {
	if len(errs) == 0 {
		return nil
	}
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Line > 0 {
			out = append(out, fmt.Sprintf("line %d: %s", e.Line, e.Message))
		} else {
			out = append(out, e.Message)
		}
	}
	return out
}

// ConvertToLuaOptions is the new params shape for ConvertToLua. Backup=true
// duplicates the map's source (folder or .w3x file) before any conversion
// happens; on backup failure the operation refuses (no half-baked state).
type ConvertToLuaOptions struct {
	Backup bool `json:"backup"`
}

// ConvertToLua is the legacy wrapper (backup defaults to true). New callers
// should prefer ConvertToLuaWithOptions.
func (s *Session) ConvertToLua() (*ConvertToLuaCheckResult, error) {
	return s.ConvertToLuaWithOptions(ConvertToLuaOptions{Backup: true})
}

// ConvertToLuaWithOptions runs the full convert flow:
//   - If opts.Backup, snapshot the map source (folder copy or .w3x file copy
//     to "<name>.backup<.w3x>"). Refuse on backup failure.
//   - Transpile each pure-JASS section (per-trigger custom_text + script
//     triggers + war3map.wct GlobalJASS).
//   - Concatenate the transpiled Lua with the GUI codegen output.
//   - Write war3map.lua, delete war3map.j, set info.Lua=true.
//
// Returns ErrConvertBlocked + the blocker list embedded in the result when
// CheckConvertToLua finds vJASS content. Returns ErrAlreadyLua if the map is
// already flagged as Lua. Returns ErrBackupFailed if the requested backup
// couldn't be created.
func (s *Session) ConvertToLuaWithOptions(opts ConvertToLuaOptions) (*ConvertToLuaCheckResult, error) {
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
	mapPath := s.path
	s.mu.Unlock()

	// Backup BEFORE any other work — the whole point is to give the user a
	// fallback if anything later goes wrong.
	if opts.Backup {
		if err := backupMapSource(mapPath); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBackupFailed, err)
		}
	}

	// Re-acquire lock for the rest of the flow.
	s.mu.Lock()
	originalJASS, hadJASS, readErr := src.read("war3map.j")
	if readErr != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("read war3map.j: %w", readErr)
	}
	if !hadJASS {
		originalJASS = nil
	}

	// Honor preserve-marker on existing war3map.lua.
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

	// Build the per-trigger transpiled overlay (custom_texts that need
	// translation feed into CustomTexts as Lua, so the codegen treats them
	// as already-Lua and concatenates them verbatim). Each section runs
	// through every vJASS preprocessor pass (textmacro + library/scope +
	// struct) BEFORE the transpiler so a macro-only / library-only /
	// struct-only section produces real Lua, not error placeholders.
	//
	// Per-pass aggregates:
	//   - libInits     : vJASS library InitOrder across sections; codegen
	//                    threads these into _vjass_init_libs() in dep order.
	//   - structInits  : structs that defined `static method onInit`; codegen
	//                    appends Foo.onInit() calls AFTER the library inits.
	//
	// Each section also gets its struct-Lua snippets spliced into the
	// transpiled output at the marker positions PreprocessStructs left.
	customTextsLua := map[int32]string{}
	var libInits []jass2lua.LibraryInit
	var structInits []jass2lua.StructInit
	if s.triggers != nil {
		for i := range s.triggers.Triggers {
			tr := &s.triggers.Triggers[i]
			if tr.IsComment {
				continue
			}
			if strings.TrimSpace(tr.CustomText) == "" {
				continue
			}
			pp := jass2lua.Preprocess(tr.CustomText)
			ls := jass2lua.PreprocessLibScope(pp.Expanded)
			st := jass2lua.PreprocessStructs(ls.Expanded)
			libInits = append(libInits, ls.InitOrder...)
			structInits = append(structInits, st.Inits...)
			var lua string
			if tr.IsScript || tr.Classifier == wtg.ClassifierScript {
				lua, _ = jass2lua.TranspileScript(st.Expanded)
			} else {
				lua, _ = jass2lua.TranspileFunction(tr.Name+"_CustomText", st.Expanded)
			}
			lua = jass2lua.SpliceStructLua(lua, st)
			customTextsLua[tr.ID] = lua
		}
	}

	in := CodegenInputs{
		Triggers:    s.triggers,
		Units:       s.units,
		Doodads:     s.doodads,
		Terrain:     s.terrain,
		Info:        s.info,
		Regions:     s.regions,
		Cameras:     s.cameras,
		TD:          TriggerDataSnapshot(),
		CustomTexts: customTextsLua,
		LibInits:    libInits,
		StructInits: structInits,
	}
	if s.triggersWct != nil && strings.TrimSpace(s.triggersWct.GlobalJASS) != "" {
		pp := jass2lua.Preprocess(s.triggersWct.GlobalJASS)
		ls := jass2lua.PreprocessLibScope(pp.Expanded)
		st := jass2lua.PreprocessStructs(ls.Expanded)
		in.LibInits = append(in.LibInits, ls.InitOrder...)
		in.StructInits = append(in.StructInits, st.Inits...)
		lua, _ := jass2lua.TranspileScript(st.Expanded)
		lua = jass2lua.SpliceStructLua(lua, st)
		in.GlobalJASS = lua
	}

	infoCopy := *s.info
	infoCopy.Lua = true
	in.Info = &infoCopy
	s.mu.Unlock()

	luaText, err := GenerateLuaScript(in)
	if err != nil {
		return nil, fmt.Errorf("generate war3map.lua: %w", err)
	}

	s.mu.Lock()
	if !s.loaded || s.info == nil || s.source == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("map closed during conversion")
	}
	cmd := &convertToLuaCmd{
		originalJASS:    originalJASS,
		hadOriginalJASS: hadJASS,
		generatedLua:    []byte(luaText),
		wasLua:          s.info.Lua,
		oldFileVersion:  s.info.FileVersion,
	}
	if err := cmd.Apply(s); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	wasDirty := s.anyDirtyLocked()
	s.dirtyInfo = true
	s.mapHeaderScriptDirty = false
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

// backupMapSource snapshots the map source so the user can roll back if the
// converted Lua misbehaves. The backup is NOT touched by Undo — it sits next
// to the original until the user manually deletes it.
//
// Naming:
//   - Folder source (e.g. `C:/path/to/MyMap`)        → `C:/path/to/MyMap.backup` (folder copy)
//   - .w3x file source (`C:/path/to/MyMap.w3x`)      → `C:/path/to/MyMap.backup.w3x` (file copy)
//
// If a backup with the chosen name already exists, we append `-N` until we
// find a free path. This costs a stat per attempt but is bounded — users
// rarely have more than a handful of historical backups around.
func backupMapSource(mapPath string) error {
	if mapPath == "" {
		return fmt.Errorf("no map path available for backup")
	}
	st, err := os.Stat(mapPath)
	if err != nil {
		return fmt.Errorf("stat map %q: %w", mapPath, err)
	}
	dest := pickBackupPath(mapPath, st.IsDir())
	if st.IsDir() {
		return copyDir(mapPath, dest)
	}
	return copyFile(mapPath, dest)
}

// pickBackupPath finds a non-colliding backup path for mapPath. For folders
// the suffix is `.backup`; for files we splice `.backup` before the original
// extension (`.backup.w3x` reads naturally and keeps the extension's MIME
// hint intact). Collisions append `-1`, `-2`, ... to the .backup token.
func pickBackupPath(mapPath string, isDir bool) string {
	if isDir {
		base := mapPath + ".backup"
		try := base
		for i := 1; pathExists(try); i++ {
			try = fmt.Sprintf("%s-%d", base, i)
		}
		return try
	}
	ext := filepath.Ext(mapPath)
	noExt := strings.TrimSuffix(mapPath, ext)
	base := noExt + ".backup" + ext
	try := base
	for i := 1; pathExists(try); i++ {
		try = fmt.Sprintf("%s.backup-%d%s", noExt, i, ext)
	}
	return try
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		// Make sure parent exists (WalkDir visits files before symlinks
		// sometimes; defensive).
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target)
	})
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
// IMPORTANT: backup is NOT tracked by this command — undo restores the
// pre-conversion state in-place; the backup file is the user's belt-and-
// suspenders fallback and we never auto-delete it.
type convertToLuaCmd struct {
	originalJASS    []byte
	hadOriginalJASS bool
	generatedLua    []byte
	wasLua          bool
	oldFileVersion  int32
}

func (c *convertToLuaCmd) Label() string { return "Convert to Lua" }

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
	if s.info.FileVersion < w3i.FileVersionRefV28 {
		s.info.FileVersion = w3i.FileVersionRefV28
	}
	s.dirtyInfo = true
	return nil
}

func (c *convertToLuaCmd) Revert(s *Session) error {
	if s.source == nil || s.info == nil {
		return fmt.Errorf("convert revert: no map loaded")
	}
	if c.hadOriginalJASS {
		if err := s.source.write("war3map.j", c.originalJASS); err != nil {
			return fmt.Errorf("restore war3map.j: %w", err)
		}
	} else {
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

func (c *convertToLuaCmd) Affected(s *Session) []EntityChange {
	return []EntityChange{{Kind: "info", ID: 0, Field: "lua"}}
}

var _ Command = (*convertToLuaCmd)(nil)
