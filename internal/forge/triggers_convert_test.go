package forge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wct"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wtg"
)

// Phase: Convert Map to Lua. Each test builds a minimal extracted-map folder
// (war3map.w3i + war3map.j + optional wtg/wct), opens it via Session.Open,
// runs the convert flow, and asserts the file shuffle + dirty/undo behavior.
//
// We don't rely on any user-side fixture (no Enfo's FFB on disk); the test
// builds its own .w3i via w3i.Encode and writes a stub war3map.j.

// writeConvertFixture creates a tiny extracted-map folder under t.TempDir().
// Returns the path. Includes:
//   - war3map.w3i with FileVersion=TFT (Lua=false initially)
//   - war3map.j with `function main() end` (the bytes that conversion must
//     drop)
//   - optionally war3map.wtg + war3map.wct (built from the provided triggers
//     argument). When triggers is nil, no wtg/wct is written — the loader
//     synthesizes a hand-rolled-script "Map Header" trigger from war3map.j.
func writeConvertFixture(t *testing.T, triggers *wtg.Triggers) string {
	t.Helper()
	dir := t.TempDir()

	// Minimal war3map.w3i. FileVersion=25 (TFT) is the canonical "old JASS map"
	// shape — exactly what the Convert flow targets. Lua starts false.
	info := &w3i.Info{
		FileVersion: w3i.FileVersionTFT,
		MapVersion:  1,
		Name:        "ConvertFixture",
		Author:      "tests",
		Tileset:     'L',
		Lua:         false,
	}
	infoBytes, err := w3i.Encode(info)
	if err != nil {
		t.Fatalf("w3i.Encode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "war3map.w3i"), infoBytes, 0o644); err != nil {
		t.Fatalf("write w3i: %v", err)
	}

	// war3map.j stub. Recognizable bytes so the round-trip test can byte-
	// compare the restored file against the original.
	jSource := []byte("function main()\n  -- original jass body\nend\n")
	if err := os.WriteFile(filepath.Join(dir, "war3map.j"), jSource, 0o644); err != nil {
		t.Fatalf("write j: %v", err)
	}

	// Optional wtg/wct. When triggers is nil, the loader will synthesize a
	// hand-rolled "Map Header" script trigger from war3map.j — which is
	// itself a blocker, so we exercise both paths.
	if triggers != nil {
		td := TriggerDataSnapshot()
		if td == nil {
			t.Fatalf("TriggerDataSnapshot returned nil — embedded data broken")
		}
		wtgBytes, err := wtg.Encode(triggers, td.ArgumentCounts)
		if err != nil {
			t.Fatalf("wtg.Encode: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "war3map.wtg"), wtgBytes, 0o644); err != nil {
			t.Fatalf("write wtg: %v", err)
		}
		wctFile := &wct.File{Version: 0x80000004, SubVersion: 1}
		wctFile.CustomTexts = collectOrderedCustomTexts(triggers, wctFile.IsPre131)
		wctBytes, err := wct.Encode(wctFile, triggers)
		if err != nil {
			t.Fatalf("wct.Encode: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "war3map.wct"), wctBytes, 0o644); err != nil {
			t.Fatalf("write wct: %v", err)
		}
	}

	return dir
}

// guiOnlyTriggers builds a *wtg.Triggers with one MapInitializationEvent-
// driven GUI trigger that calls DoNothing. No custom_text, no is_script —
// this is the canonical "safe to convert" shape.
func guiOnlyTriggers() *wtg.Triggers {
	return &wtg.Triggers{
		Version:    0x80000004,
		SubVersion: 7,
		TrigDefVer: 2,
		Categories: []wtg.Category{
			{Classifier: wtg.ClassifierMap, ID: 0, ParentID: -1, Name: "Map Header"},
			{Classifier: wtg.ClassifierCategory, ID: 1, ParentID: 0, Name: "Main"},
		},
		Triggers: []wtg.Trigger{
			{
				Classifier: wtg.ClassifierGUI, ID: 10, ParentID: 1,
				Name: "OnInit", IsEnabled: true, InitiallyOn: true,
				ECAs: []wtg.ECA{
					{Type: wtg.ECAEvent, Name: "MapInitializationEvent", Enabled: true},
					{Type: wtg.ECAAction, Name: "DoNothing", Enabled: true},
				},
			},
		},
		Elements: []wtg.ElementRef{
			{Kind: wtg.ElementKindCategory, Index: 0},
			{Kind: wtg.ElementKindCategory, Index: 1},
			{Kind: wtg.ElementKindTrigger, Index: 0},
		},
	}
}

// TestCheckConvertToLua_GuiOnlyClean asserts a GUI-only map (no script
// triggers, no custom_text) returns an empty Blockers list.
func TestCheckConvertToLua_GuiOnlyClean(t *testing.T) {
	dir := writeConvertFixture(t, guiOnlyTriggers())
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := s.CheckConvertToLua()
	if err != nil {
		t.Fatalf("CheckConvertToLua: %v", err)
	}
	if len(res.Blockers) != 0 {
		t.Fatalf("expected no blockers, got %d: %+v", len(res.Blockers), res.Blockers)
	}
}

// TestCheckConvertToLua_ScriptTriggerBlocks asserts an is_script trigger
// surfaces as a blocker.
func TestCheckConvertToLua_ScriptTriggerBlocks(t *testing.T) {
	tr := guiOnlyTriggers()
	tr.Triggers = append(tr.Triggers, wtg.Trigger{
		Classifier: wtg.ClassifierScript, ID: 11, ParentID: 1,
		Name: "RawJassThing", IsEnabled: true, InitiallyOn: true, IsScript: true,
		CustomText: "function RawJassThing_Actions takes nothing returns nothing\nendfunction\n",
	})
	tr.Elements = append(tr.Elements, wtg.ElementRef{Kind: wtg.ElementKindTrigger, Index: 1})
	dir := writeConvertFixture(t, tr)
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := s.CheckConvertToLua()
	if err != nil {
		t.Fatalf("CheckConvertToLua: %v", err)
	}
	if len(res.Blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d: %+v", len(res.Blockers), res.Blockers)
	}
	if res.Blockers[0].Kind != "script" || res.Blockers[0].TriggerName != "RawJassThing" {
		t.Errorf("unexpected blocker: %+v", res.Blockers[0])
	}

	// ConvertToLua should refuse with the same blocker list embedded.
	convertRes, err := s.ConvertToLua()
	if !errors.Is(err, ErrConvertBlocked) {
		t.Fatalf("ConvertToLua should refuse with ErrConvertBlocked, got %v", err)
	}
	if convertRes == nil || len(convertRes.Blockers) != 1 {
		t.Errorf("ConvertToLua refused but did not return blocker list: %+v", convertRes)
	}
}

// TestCheckConvertToLua_CustomTextOverlayBlocks asserts a GUI trigger with
// non-empty custom_text surfaces as a blocker.
func TestCheckConvertToLua_CustomTextOverlayBlocks(t *testing.T) {
	tr := guiOnlyTriggers()
	// Add a GUI trigger that ALSO has a JASS custom_text overlay.
	tr.Triggers = append(tr.Triggers, wtg.Trigger{
		Classifier: wtg.ClassifierGUI, ID: 12, ParentID: 1,
		Name: "GuiPlusJass", IsEnabled: true, InitiallyOn: true,
		ECAs: []wtg.ECA{
			{Type: wtg.ECAEvent, Name: "MapInitializationEvent", Enabled: true},
		},
		CustomText: "// hand-written JASS overlay\ncall BJDebugMsg(\"hi\")\n",
	})
	tr.Elements = append(tr.Elements, wtg.ElementRef{Kind: wtg.ElementKindTrigger, Index: 1})
	dir := writeConvertFixture(t, tr)
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := s.CheckConvertToLua()
	if err != nil {
		t.Fatalf("CheckConvertToLua: %v", err)
	}
	if len(res.Blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d: %+v", len(res.Blockers), res.Blockers)
	}
	if res.Blockers[0].Kind != "custom_text" || res.Blockers[0].TriggerName != "GuiPlusJass" {
		t.Errorf("unexpected blocker: %+v", res.Blockers[0])
	}
}

// TestCheckConvertToLua_HandRolledScriptBlocks asserts a map with no .wtg but
// a hand-rolled war3map.j surfaces a "map_header" blocker — auto-conversion
// of arbitrary JASS would destroy gameplay.
func TestCheckConvertToLua_HandRolledScriptBlocks(t *testing.T) {
	dir := writeConvertFixture(t, nil) // no wtg → loader synthesizes Map Header
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := s.CheckConvertToLua()
	if err != nil {
		t.Fatalf("CheckConvertToLua: %v", err)
	}
	if len(res.Blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d: %+v", len(res.Blockers), res.Blockers)
	}
	if res.Blockers[0].Kind != "map_header" {
		t.Errorf("expected kind=map_header, got %+v", res.Blockers[0])
	}
}

// TestConvertToLua_SuccessRoundTrip asserts the happy path:
//   - GUI-only map opens
//   - ConvertToLua succeeds (no blockers)
//   - war3map.lua appears on disk with codegen output
//   - war3map.j is gone
//   - info.Lua is true
//   - Save persists the info change (re-open shows Lua=true)
func TestConvertToLua_SuccessRoundTrip(t *testing.T) {
	dir := writeConvertFixture(t, guiOnlyTriggers())
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Info().Lua {
		t.Fatalf("fixture says Lua=true at open — bug in writeConvertFixture")
	}

	res, err := s.ConvertToLua()
	if err != nil {
		t.Fatalf("ConvertToLua: %v", err)
	}
	if res == nil || len(res.Blockers) != 0 {
		t.Fatalf("expected empty Blockers on success, got %+v", res)
	}

	// war3map.lua should exist and look like codegen output.
	luaPath := filepath.Join(dir, "war3map.lua")
	luaBytes, err := os.ReadFile(luaPath)
	if err != nil {
		t.Fatalf("read war3map.lua: %v", err)
	}
	if !strings.Contains(string(luaBytes), "-- generated by wc3-forge") {
		t.Errorf("war3map.lua missing codegen header — got first 200 chars: %q", string(luaBytes[:min(200, len(luaBytes))]))
	}

	// war3map.j should be gone.
	if _, err := os.Stat(filepath.Join(dir, "war3map.j")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("war3map.j should be deleted, got err=%v", err)
	}

	// info.Lua should be true in memory.
	if !s.Info().Lua {
		t.Errorf("info.Lua not flipped to true after ConvertToLua")
	}
	if !s.IsDirty() {
		t.Errorf("session should be dirty after ConvertToLua (info change pending)")
	}

	// Save persists the info change.
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Re-open and confirm Lua=true survived.
	s2 := &Session{}
	if err := s2.Open(dir); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if !s2.Info().Lua {
		t.Errorf("info.Lua did not survive Save+re-open")
	}
}

// TestConvertToLua_Undo asserts Ctrl+Z after a successful conversion
// restores war3map.j (byte-equal), info.Lua=false, and drops war3map.lua.
func TestConvertToLua_Undo(t *testing.T) {
	dir := writeConvertFixture(t, guiOnlyTriggers())
	originalJASS, err := os.ReadFile(filepath.Join(dir, "war3map.j"))
	if err != nil {
		t.Fatalf("read original war3map.j: %v", err)
	}

	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.ConvertToLua(); err != nil {
		t.Fatalf("ConvertToLua: %v", err)
	}
	if !s.CanUndo() {
		t.Fatalf("CanUndo=false after ConvertToLua — Command not recorded")
	}

	// Undo.
	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	// war3map.j should be back, byte-equal.
	restored, err := os.ReadFile(filepath.Join(dir, "war3map.j"))
	if err != nil {
		t.Fatalf("read restored war3map.j: %v", err)
	}
	if string(restored) != string(originalJASS) {
		t.Errorf("restored war3map.j differs from original\n  original: %q\n  restored: %q",
			string(originalJASS), string(restored))
	}

	// war3map.lua should be gone.
	if _, err := os.Stat(filepath.Join(dir, "war3map.lua")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("war3map.lua should be deleted on Undo, got err=%v", err)
	}

	// info.Lua should be false again.
	if s.Info().Lua {
		t.Errorf("info.Lua did not flip back to false on Undo")
	}
}

// TestConvertToLua_AlreadyLuaRefuses asserts a map that's already Lua-flagged
// refuses conversion (the menu item is also disabled in this case, but the
// backend guards for direct MCP/test callers).
func TestConvertToLua_AlreadyLuaRefuses(t *testing.T) {
	dir := writeConvertFixture(t, guiOnlyTriggers())
	// Patch info.Lua=true before opening.
	infoBytes, err := os.ReadFile(filepath.Join(dir, "war3map.w3i"))
	if err != nil {
		t.Fatalf("read w3i: %v", err)
	}
	info, err := w3i.Parse(infoBytes)
	if err != nil {
		t.Fatalf("parse w3i: %v", err)
	}
	info.Lua = true
	// Need to bump FileVersion to one that supports Lua flag.
	info.FileVersion = w3i.FileVersionRefV28
	infoBytes2, err := w3i.Encode(info)
	if err != nil {
		t.Fatalf("re-encode w3i: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "war3map.w3i"), infoBytes2, 0o644); err != nil {
		t.Fatalf("rewrite w3i: %v", err)
	}

	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !s.Info().Lua {
		t.Fatalf("expected Lua=true after patching")
	}
	_, err = s.ConvertToLua()
	if !errors.Is(err, ErrAlreadyLua) {
		t.Errorf("expected ErrAlreadyLua, got %v", err)
	}
}

// min is a tiny helper since this file targets a Go version that may not
// ship the builtin (it's a generic in 1.21+; we're at 1.22+ but a local
// definition is safer for one-off use).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
