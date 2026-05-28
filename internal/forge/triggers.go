package forge

import (
	"log"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/formats/wct"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wtg"
	wtgdata "github.com/StephenSHorton/wc3-forge/internal/formats/wtg/data"
)

// Trigger Editor data layer — Phase 1a (read-only).
//
// The Session owns one *wtg.Triggers and one *wct.File per loaded map; this
// file wires the parse → resolve → expose flow. Phase 2a will add mutation +
// encode + dirty tracking; Phase 3 wires Lua codegen.
//
// Three integration patterns matter:
//
//  1. Embedded TriggerData snapshot. The wtg parser needs argc per function
//     name. Live CASC could provide it but we ship our own snapshot under
//     internal/formats/wtg/data/ so Phase-1a parses are independent of the
//     user's WC3 install. The CASC mount IS consulted on Open as a soft
//     check: if its TriggerData.txt size disagrees with our snapshot, we
//     log a warning so future agents know to refresh.
//
//  2. Hand-rolled-script maps. Many real maps (the Enfo family, almost every
//     LTS project) have no .wtg at all — every trigger lives in war3map.lua
//     or war3map.j. The Trigger Editor still needs SOMETHING to display, so
//     Open synthesizes a "Map Header" Classifier::script entry whose
//     CustomText is the raw script source. Editable CodeMirror lands in 2a;
//     in 1a we render it in a <pre> block.
//
//  3. wct binding. The .wct stores per-trigger script blobs in a flat list;
//     the .wtg's category-then-trigger walk determines the order. Per HiveWE
//     triggers.ixx L924-938, that order is "outer loop categories, inner
//     loop triggers whose parent_id matches, skip is_comment in 1.31+".
//     bindCustomTexts replicates that walk so each Trigger.CustomText points
//     at the right blob.

// triggerDataOnce + triggerDataValue cache the parsed embedded snapshot so
// every Session.Open doesn't re-parse the same 689k of TriggerData.txt.
// Process-lifetime singleton — refresh requires a binary rebuild (the embed
// reads at compile time).
//
// The error is captured and stuck in triggerDataErr so callers can branch on
// "the snapshot is broken" without re-running ParseTriggerData on every Open.
var (
	triggerDataOnce  sync.Once
	triggerDataValue *wtg.TriggerData
	triggerDataErr   error
)

// TriggerDataSnapshot returns the process-wide *wtg.TriggerData built from
// the embedded snapshot under internal/formats/wtg/data/. Idempotent + cheap
// after first call. Returns nil if the embedded snapshot failed to parse —
// which would be a build-time error (the snapshot ships with the binary), so
// real-world callers can assume non-nil.
func TriggerDataSnapshot() *wtg.TriggerData {
	triggerDataOnce.Do(func() {
		td, err := wtg.ParseTriggerData(wtgdata.TriggerDataTXT)
		if err != nil {
			triggerDataErr = err
			log.Printf("triggers: parse embedded TriggerData.txt: %v", err)
			return
		}
		if err := td.LoadTriggerStrings(wtgdata.TriggerStringsTXT); err != nil {
			// Soft-fail: missing tooltips don't break the editor.
			log.Printf("triggers: parse embedded TriggerStrings.txt: %v", err)
		}
		triggerDataValue = td
	})
	return triggerDataValue
}

// Triggers returns the loaded map's parsed war3map.wtg, or nil if no map is
// loaded / the map has no triggers. The returned pointer is shared — callers
// must not mutate (no mutation API in 1a, so this is more about future-proof
// discipline than current safety).
func (s *Session) Triggers() *wtg.Triggers {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.triggers
}

// TriggersScripts returns the loaded map's parsed war3map.wct, or nil if no
// map is loaded / the map has no .wct file. The returned pointer is shared.
func (s *Session) TriggersScripts() *wct.File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.triggersWct
}

// loadTriggersForOpenV2 is the Phase 2a version of loadTriggersForOpen with
// the extra return fields the Session needs to discriminate hand-rolled-script
// maps at Save time (the boolean) and to know which script file to write
// (the string). The legacy 2-return shape is preserved as a wrapper for any
// caller still using it.
func loadTriggersForOpenV2(src fileSource, isLuaMap bool) (*wtg.Triggers, *wct.File, bool, string) {
	td := TriggerDataSnapshot()
	if td == nil {
		log.Printf("triggers: embedded snapshot unusable; trigger editor will be empty")
		return nil, nil, false, ""
	}
	wtgBytes, hasWTG, err := src.read("war3map.wtg")
	if err != nil {
		log.Printf("triggers: read war3map.wtg: %v", err)
	}
	wctBytes, hasWCT, err := src.read("war3map.wct")
	if err != nil {
		log.Printf("triggers: read war3map.wct: %v", err)
	}
	var triggers *wtg.Triggers
	var wctFile *wct.File
	if hasWTG {
		parsed, err := wtg.Parse(wtgBytes, td.ArgumentCounts)
		if err != nil {
			log.Printf("triggers: parse war3map.wtg: %v", err)
		} else {
			triggers = parsed
		}
	}
	if hasWCT {
		parsed, err := wct.Parse(wctBytes)
		if err != nil {
			log.Printf("triggers: parse war3map.wct: %v", err)
		} else {
			wctFile = parsed
		}
	}
	if triggers != nil && wctFile != nil {
		bindCustomTexts(triggers, wctFile)
		return triggers, wctFile, false, ""
	}
	if triggers != nil {
		return triggers, nil, false, ""
	}
	// No .wtg. If the map has a script source, synthesize a hand-rolled
	// "Map Header" entry so the user can at least read it (and now: edit it).
	if scriptPath, scriptBytes := readScriptSource(src, isLuaMap); scriptPath != "" {
		return synthesizeHandRolledScriptTriggers(scriptPath, string(scriptBytes)), nil, true, scriptPath
	}
	return nil, nil, false, ""
}

// loadTriggersForOpen is the original 2-return wrapper, retained for any
// test or external caller that wasn't migrated to V2. Discards the extra
// hand-rolled-script metadata.
func loadTriggersForOpen(src fileSource, isLuaMap bool) (*wtg.Triggers, *wct.File) {
	td := TriggerDataSnapshot()
	if td == nil {
		log.Printf("triggers: embedded snapshot unusable; trigger editor will be empty")
		return nil, nil
	}
	wtgBytes, hasWTG, err := src.read("war3map.wtg")
	if err != nil {
		log.Printf("triggers: read war3map.wtg: %v", err)
	}
	wctBytes, hasWCT, err := src.read("war3map.wct")
	if err != nil {
		log.Printf("triggers: read war3map.wct: %v", err)
	}

	var triggers *wtg.Triggers
	var wctFile *wct.File

	if hasWTG {
		parsed, err := wtg.Parse(wtgBytes, td.ArgumentCounts)
		if err != nil {
			log.Printf("triggers: parse war3map.wtg: %v", err)
		} else {
			triggers = parsed
		}
	}
	if hasWCT {
		parsed, err := wct.Parse(wctBytes)
		if err != nil {
			log.Printf("triggers: parse war3map.wct: %v", err)
		} else {
			wctFile = parsed
		}
	}

	if triggers != nil && wctFile != nil {
		bindCustomTexts(triggers, wctFile)
		return triggers, wctFile
	}

	if triggers != nil {
		// .wtg without .wct — the per-trigger custom_text columns stay empty.
		// This is unusual but legal; the UI just shows blank script bodies for
		// any custom-script triggers.
		return triggers, nil
	}

	// No .wtg. If the map has a script source, synthesize a hand-rolled
	// "Map Header" entry so the user can at least read it.
	if scriptPath, scriptBytes := readScriptSource(src, isLuaMap); scriptPath != "" {
		return synthesizeHandRolledScriptTriggers(scriptPath, string(scriptBytes)), nil
	}

	return nil, nil
}

// readScriptSource returns the map's primary script file (war3map.lua when
// isLuaMap, war3map.j otherwise). Returns ("", nil) when neither is present
// — that's a legal "no script" map (rare; usually melee templates).
//
// Prefers the declared script kind first but falls back to the other if the
// declared one is absent — Reforged occasionally mislabels a JASS map as Lua
// in w3i (and vice versa) when authors hand-edit.
func readScriptSource(src fileSource, isLuaMap bool) (path string, data []byte) {
	primary := "war3map.j"
	fallback := "war3map.lua"
	if isLuaMap {
		primary, fallback = fallback, primary
	}
	if b, ok, _ := src.read(primary); ok {
		return primary, b
	}
	if b, ok, _ := src.read(fallback); ok {
		return fallback, b
	}
	return "", nil
}

// synthesizeHandRolledScriptTriggers builds a *wtg.Triggers from raw script
// source bytes. The resulting structure has:
//   - A Map Header category (id=0)
//   - One script trigger inside it whose CustomText is the raw script
//
// Editable CodeMirror lands in Phase 2a; in 1a the UI renders this in a
// monospace <pre> block.
func synthesizeHandRolledScriptTriggers(scriptName, scriptText string) *wtg.Triggers {
	return &wtg.Triggers{
		Version:    0x80000004,
		SubVersion: 7,
		TrigDefVer: 2,
		Categories: []wtg.Category{
			{
				Classifier: wtg.ClassifierMap,
				ID:         0,
				ParentID:   -1,
				Name:       "Map Header",
				OpenState:  true,
			},
		},
		Triggers: []wtg.Trigger{
			{
				Classifier:  wtg.ClassifierScript,
				ID:          1,
				ParentID:    0,
				Name:        scriptName,
				Description: "Hand-rolled script (no war3map.wtg in map)",
				CustomText:  scriptText,
				IsEnabled:   true,
				IsScript:    true,
				InitiallyOn: true,
			},
		},
	}
}

// bindCustomTexts walks the categories-then-triggers order HiveWE writes to
// .wct (triggers.ixx L924-938) and stitches each .wct entry onto its
// corresponding Trigger.CustomText.
//
// 1.31+ format SKIPS is_comment triggers in the wct, so the bind walk does
// too. Pre-1.31 includes every trigger.
//
// If counts don't match (a corrupt or out-of-sync pair), we bind as many as
// we can and log the remainder — better to render partial scripts than to
// drop them all.
func bindCustomTexts(t *wtg.Triggers, w *wct.File) {
	skipComments := !w.IsPre131
	i := 0
	for _, cat := range t.Categories {
		for ti := range t.Triggers {
			tr := &t.Triggers[ti]
			if tr.ParentID != cat.ID {
				continue
			}
			if skipComments && tr.IsComment {
				continue
			}
			if i >= len(w.CustomTexts) {
				log.Printf("triggers: wct ran out at entry %d (have %d, need more) — leaving remaining triggers' custom_text empty",
					i, len(w.CustomTexts))
				return
			}
			tr.CustomText = w.CustomTexts[i]
			i++
		}
	}
	if i < len(w.CustomTexts) {
		log.Printf("triggers: wct had %d entries but only %d bound — %d unused", len(w.CustomTexts), i, len(w.CustomTexts)-i)
	}
}
