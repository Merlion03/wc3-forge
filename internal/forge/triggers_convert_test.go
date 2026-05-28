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

// Convert-to-Lua tests. The flow now distinguishes pure JASS (transpiled
// automatically) from vJASS (blocked). Tests cover both branches.

// writeConvertFixture creates a tiny extracted-map folder under t.TempDir().
// Returns the path. Includes:
//   - war3map.w3i with FileVersion=TFT (Lua=false initially)
//   - war3map.j with a small JASS body (used as the synthesized Map Header
//     for hand-rolled-script tests)
//   - optionally war3map.wtg + war3map.wct (built from the provided triggers
//     argument). When triggers is nil, no wtg/wct is written — the loader
//     synthesizes a hand-rolled-script "Map Header" trigger from war3map.j.
func writeConvertFixture(t *testing.T, triggers *wtg.Triggers) string {
	t.Helper()
	dir := t.TempDir()

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

	jSource := []byte("function main takes nothing returns nothing\n    call DoNothing()\nendfunction\n")
	if err := os.WriteFile(filepath.Join(dir, "war3map.j"), jSource, 0o644); err != nil {
		t.Fatalf("write j: %v", err)
	}

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

// TestCheckConvertToLua_PureJassScriptTriggerNotBlocked asserts an is_script
// trigger with only pure-JASS keywords is NOT a blocker — it gets transpiled.
func TestCheckConvertToLua_PureJassScriptTriggerNotBlocked(t *testing.T) {
	tr := guiOnlyTriggers()
	tr.Triggers = append(tr.Triggers, wtg.Trigger{
		Classifier: wtg.ClassifierScript, ID: 11, ParentID: 1,
		Name: "PureJassThing", IsEnabled: true, InitiallyOn: true, IsScript: true,
		CustomText: "function PureJassThing_Actions takes nothing returns nothing\n    call DoNothing()\nendfunction\n",
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
	if len(res.Blockers) != 0 {
		t.Fatalf("expected no blockers for pure-JASS, got %d: %+v", len(res.Blockers), res.Blockers)
	}
}

// TestCheckConvertToLua_VJassScriptTriggerBlocks asserts an is_script trigger
// with a vJASS `struct` keyword IS a blocker.
//
// Phase 2 deliberately stopped blocking `library` (it's preprocessed away).
// We pivoted this test to `struct`, which is unblocked by Phase 3 work.
func TestCheckConvertToLua_VJassScriptTriggerBlocks(t *testing.T) {
	tr := guiOnlyTriggers()
	tr.Triggers = append(tr.Triggers, wtg.Trigger{
		Classifier: wtg.ClassifierScript, ID: 11, ParentID: 1,
		Name: "VJassThing", IsEnabled: true, InitiallyOn: true, IsScript: true,
		CustomText: "struct Foo\n    integer x\nendstruct\n",
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
		t.Fatalf("expected 1 vJASS blocker, got %d: %+v", len(res.Blockers), res.Blockers)
	}
	if res.Blockers[0].Kind != "script_vjass" || res.Blockers[0].TriggerName != "VJassThing" {
		t.Errorf("unexpected blocker: %+v", res.Blockers[0])
	}

	convertRes, err := s.ConvertToLua()
	if !errors.Is(err, ErrConvertBlocked) {
		t.Fatalf("ConvertToLua should refuse with ErrConvertBlocked, got %v", err)
	}
	if convertRes == nil || len(convertRes.Blockers) != 1 {
		t.Errorf("ConvertToLua refused but did not return blocker list: %+v", convertRes)
	}
}

// TestCheckConvertToLua_LibraryScriptTriggerNoLongerBlocks asserts that
// after Phase 2 a script trigger whose body is a vJASS `library` (no
// struct/module/interface/define) is NOT a blocker — it's handled by
// PreprocessLibScope.
func TestCheckConvertToLua_LibraryScriptTriggerNoLongerBlocks(t *testing.T) {
	tr := guiOnlyTriggers()
	tr.Triggers = append(tr.Triggers, wtg.Trigger{
		Classifier: wtg.ClassifierScript, ID: 11, ParentID: 1,
		Name: "LibraryOnly", IsEnabled: true, InitiallyOn: true, IsScript: true,
		CustomText: "library Foo\n    private function bar takes nothing returns nothing\n        call DoNothing()\n    endfunction\nendlibrary\n",
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
	if len(res.Blockers) != 0 {
		t.Fatalf("expected no blockers for library-only script after Phase 2, got %d: %+v", len(res.Blockers), res.Blockers)
	}
}

// TestCheckConvertToLua_MixedTriggers asserts a map with one pure-JASS trigger
// and one vJASS trigger blocks (because of the vJASS one).
func TestCheckConvertToLua_MixedTriggers(t *testing.T) {
	tr := guiOnlyTriggers()
	tr.Triggers = append(tr.Triggers,
		wtg.Trigger{
			Classifier: wtg.ClassifierScript, ID: 11, ParentID: 1,
			Name: "Pure", IsEnabled: true, InitiallyOn: true, IsScript: true,
			CustomText: "function Pure_Actions takes nothing returns nothing\n    call DoNothing()\nendfunction\n",
		},
		wtg.Trigger{
			Classifier: wtg.ClassifierScript, ID: 12, ParentID: 1,
			Name: "WithStruct", IsEnabled: true, InitiallyOn: true, IsScript: true,
			CustomText: "struct S\n    integer x\nendstruct\n",
		},
	)
	tr.Elements = append(tr.Elements,
		wtg.ElementRef{Kind: wtg.ElementKindTrigger, Index: 1},
		wtg.ElementRef{Kind: wtg.ElementKindTrigger, Index: 2},
	)
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
		t.Fatalf("expected exactly 1 vJASS blocker (the struct trigger), got %d: %+v", len(res.Blockers), res.Blockers)
	}
	if res.Blockers[0].TriggerName != "WithStruct" {
		t.Errorf("expected blocker on the struct trigger, got %+v", res.Blockers[0])
	}
}

// TestCheckConvertToLua_CustomTextPureJassNotBlocked asserts a GUI trigger with
// pure-JASS custom_text overlay is NOT a blocker.
func TestCheckConvertToLua_CustomTextPureJassNotBlocked(t *testing.T) {
	tr := guiOnlyTriggers()
	tr.Triggers = append(tr.Triggers, wtg.Trigger{
		Classifier: wtg.ClassifierGUI, ID: 12, ParentID: 1,
		Name: "GuiPlusJass", IsEnabled: true, InitiallyOn: true,
		ECAs: []wtg.ECA{
			{Type: wtg.ECAEvent, Name: "MapInitializationEvent", Enabled: true},
		},
		CustomText: "call BJDebugMsg(\"hi\")\n",
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
	if len(res.Blockers) != 0 {
		t.Fatalf("expected no blockers for pure-JASS custom_text, got %d: %+v", len(res.Blockers), res.Blockers)
	}
}

// TestCheckConvertToLua_HandRolledPureJassNotBlocked asserts a map with no
// .wtg but a pure-JASS hand-rolled war3map.j is NOT a blocker (the transpiler
// handles the synthesized Map Header script).
func TestCheckConvertToLua_HandRolledPureJassNotBlocked(t *testing.T) {
	dir := writeConvertFixture(t, nil) // no wtg → loader synthesizes Map Header
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := s.CheckConvertToLua()
	if err != nil {
		t.Fatalf("CheckConvertToLua: %v", err)
	}
	if len(res.Blockers) != 0 {
		t.Fatalf("expected no blockers for hand-rolled pure-JASS map, got %d: %+v", len(res.Blockers), res.Blockers)
	}
}

// TestCheckConvertToLua_HandRolledVJassBlocks asserts a hand-rolled war3map.j
// containing struct (Phase 3+ vJASS surface) IS a blocker.
//
// Pre-Phase-2 this test used `library`; that's no longer a blocker.
func TestCheckConvertToLua_HandRolledVJassBlocks(t *testing.T) {
	dir := t.TempDir()
	info := &w3i.Info{
		FileVersion: w3i.FileVersionTFT,
		Name:        "vJassFixture",
		Tileset:     'L',
	}
	infoBytes, _ := w3i.Encode(info)
	_ = os.WriteFile(filepath.Join(dir, "war3map.w3i"), infoBytes, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "war3map.j"), []byte("struct Stuff\n    integer x\nendstruct\n"), 0o644)

	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := s.CheckConvertToLua()
	if err != nil {
		t.Fatalf("CheckConvertToLua: %v", err)
	}
	if len(res.Blockers) != 1 {
		t.Fatalf("expected 1 vJASS map_header blocker, got %d: %+v", len(res.Blockers), res.Blockers)
	}
	if res.Blockers[0].Kind != "map_header_vjass" {
		t.Errorf("expected kind=map_header_vjass, got %+v", res.Blockers[0])
	}
}

// TestCheckConvertToLua_HandRolledLibraryNoLongerBlocks asserts a hand-rolled
// war3map.j whose only vJASS surface is library/scope is NOT a blocker after
// Phase 2.
func TestCheckConvertToLua_HandRolledLibraryNoLongerBlocks(t *testing.T) {
	dir := t.TempDir()
	info := &w3i.Info{
		FileVersion: w3i.FileVersionTFT,
		Name:        "LibraryOnlyFixture",
		Tileset:     'L',
	}
	infoBytes, _ := w3i.Encode(info)
	_ = os.WriteFile(filepath.Join(dir, "war3map.w3i"), infoBytes, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "war3map.j"), []byte(
		"library Stuff\n"+
			"    private function helper takes nothing returns nothing\n"+
			"    endfunction\n"+
			"endlibrary\n"+
			"function main takes nothing returns nothing\n"+
			"    call DoNothing()\n"+
			"endfunction\n",
	), 0o644)

	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := s.CheckConvertToLua()
	if err != nil {
		t.Fatalf("CheckConvertToLua: %v", err)
	}
	if len(res.Blockers) != 0 {
		t.Fatalf("expected no blockers for library-only hand-rolled map, got %d: %+v", len(res.Blockers), res.Blockers)
	}
}

// TestConvertToLua_SuccessRoundTrip asserts the happy path (pure GUI map):
//   - GUI-only map opens
//   - ConvertToLua succeeds (no blockers)
//   - war3map.lua appears on disk with codegen output
//   - war3map.j is gone
//   - info.Lua is true
//   - Save persists the info change (re-open shows Lua=true)
//
// Backup defaults to true; the backup folder should exist on disk after
// conversion.
func TestConvertToLua_SuccessRoundTrip(t *testing.T) {
	dir := writeConvertFixture(t, guiOnlyTriggers())
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Info().Lua {
		t.Fatalf("fixture says Lua=true at open — bug in writeConvertFixture")
	}

	res, err := s.ConvertToLuaWithOptions(ConvertToLuaOptions{Backup: false})
	if err != nil {
		t.Fatalf("ConvertToLua: %v", err)
	}
	if res == nil || len(res.Blockers) != 0 {
		t.Fatalf("expected empty Blockers on success, got %+v", res)
	}

	luaPath := filepath.Join(dir, "war3map.lua")
	luaBytes, err := os.ReadFile(luaPath)
	if err != nil {
		t.Fatalf("read war3map.lua: %v", err)
	}
	if !strings.Contains(string(luaBytes), "-- generated by wc3-forge") {
		t.Errorf("war3map.lua missing codegen header — got first 200 chars: %q", string(luaBytes[:min(200, len(luaBytes))]))
	}

	if _, err := os.Stat(filepath.Join(dir, "war3map.j")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("war3map.j should be deleted, got err=%v", err)
	}

	if !s.Info().Lua {
		t.Errorf("info.Lua not flipped to true after ConvertToLua")
	}
	if !s.IsDirty() {
		t.Errorf("session should be dirty after ConvertToLua (info change pending)")
	}

	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s2 := &Session{}
	if err := s2.Open(dir); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if !s2.Info().Lua {
		t.Errorf("info.Lua did not survive Save+re-open")
	}
}

// TestConvertToLua_BackupCreatesFolderCopy exercises the backup path for a
// folder-source map.
func TestConvertToLua_BackupCreatesFolderCopy(t *testing.T) {
	dir := writeConvertFixture(t, guiOnlyTriggers())
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err := s.ConvertToLuaWithOptions(ConvertToLuaOptions{Backup: true})
	if err != nil {
		t.Fatalf("ConvertToLua: %v", err)
	}
	backup := dir + ".backup"
	if !pathExists(backup) {
		t.Fatalf("expected backup folder at %q", backup)
	}
	// Backup folder should still have war3map.j (pre-conversion).
	if _, err := os.Stat(filepath.Join(backup, "war3map.j")); err != nil {
		t.Errorf("backup folder missing war3map.j: %v", err)
	}
}

// TestConvertToLua_TranspilesPureJass asserts a map with a pure-JASS script
// trigger ends up with transpiled Lua in war3map.lua (not the original JASS).
func TestConvertToLua_TranspilesPureJass(t *testing.T) {
	tr := guiOnlyTriggers()
	tr.Triggers = append(tr.Triggers, wtg.Trigger{
		Classifier: wtg.ClassifierScript, ID: 11, ParentID: 1,
		Name: "Helper", IsEnabled: true, InitiallyOn: true, IsScript: true,
		CustomText: "function Helper_Init takes nothing returns nothing\n    call BJDebugMsg(\"hi from helper\")\nendfunction\n",
	})
	tr.Elements = append(tr.Elements, wtg.ElementRef{Kind: wtg.ElementKindTrigger, Index: 1})
	dir := writeConvertFixture(t, tr)
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.ConvertToLuaWithOptions(ConvertToLuaOptions{Backup: false}); err != nil {
		t.Fatalf("ConvertToLua: %v", err)
	}
	lua, err := os.ReadFile(filepath.Join(dir, "war3map.lua"))
	if err != nil {
		t.Fatalf("read war3map.lua: %v", err)
	}
	if !strings.Contains(string(lua), "function Helper_Init()") {
		t.Errorf("expected transpiled Lua function decl, got: ...%s...", string(lua[max(0, len(lua)-400):]))
	}
	if strings.Contains(string(lua), "function Helper_Init takes nothing returns nothing") {
		t.Errorf("expected JASS to be replaced; still found original signature")
	}
}

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
	if _, err := s.ConvertToLuaWithOptions(ConvertToLuaOptions{Backup: false}); err != nil {
		t.Fatalf("ConvertToLua: %v", err)
	}
	if !s.CanUndo() {
		t.Fatalf("CanUndo=false after ConvertToLua — Command not recorded")
	}

	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	restored, err := os.ReadFile(filepath.Join(dir, "war3map.j"))
	if err != nil {
		t.Fatalf("read restored war3map.j: %v", err)
	}
	if string(restored) != string(originalJASS) {
		t.Errorf("restored war3map.j differs from original\n  original: %q\n  restored: %q",
			string(originalJASS), string(restored))
	}

	if _, err := os.Stat(filepath.Join(dir, "war3map.lua")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("war3map.lua should be deleted on Undo, got err=%v", err)
	}

	if s.Info().Lua {
		t.Errorf("info.Lua did not flip back to false on Undo")
	}
}

func TestConvertToLua_AlreadyLuaRefuses(t *testing.T) {
	dir := writeConvertFixture(t, guiOnlyTriggers())
	infoBytes, err := os.ReadFile(filepath.Join(dir, "war3map.w3i"))
	if err != nil {
		t.Fatalf("read w3i: %v", err)
	}
	info, err := w3i.Parse(infoBytes)
	if err != nil {
		t.Fatalf("parse w3i: %v", err)
	}
	info.Lua = true
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
	_, err = s.ConvertToLuaWithOptions(ConvertToLuaOptions{Backup: false})
	if !errors.Is(err, ErrAlreadyLua) {
		t.Errorf("expected ErrAlreadyLua, got %v", err)
	}
}

// TestTranspilePreview_Smoke checks that TranspilePreview returns a non-empty
// section for a script trigger with pure-JASS body.
func TestTranspilePreview_Smoke(t *testing.T) {
	tr := guiOnlyTriggers()
	tr.Triggers = append(tr.Triggers, wtg.Trigger{
		Classifier: wtg.ClassifierScript, ID: 11, ParentID: 1,
		Name: "PreviewMe", IsEnabled: true, InitiallyOn: true, IsScript: true,
		CustomText: "function PreviewMe_Actions takes nothing returns nothing\n    call DoNothing()\nendfunction\n",
	})
	tr.Elements = append(tr.Elements, wtg.ElementRef{Kind: wtg.ElementKindTrigger, Index: 1})
	dir := writeConvertFixture(t, tr)
	s := &Session{}
	if err := s.Open(dir); err != nil {
		t.Fatalf("Open: %v", err)
	}
	preview, err := s.TranspilePreview()
	if err != nil {
		t.Fatalf("TranspilePreview: %v", err)
	}
	if len(preview.Sections) != 1 {
		t.Fatalf("expected 1 section, got %d: %+v", len(preview.Sections), preview.Sections)
	}
	if preview.Sections[0].Kind != "script" {
		t.Errorf("expected kind=script, got %+v", preview.Sections[0])
	}
	if !strings.Contains(preview.Sections[0].Transpiled, "function PreviewMe_Actions()") {
		t.Errorf("expected transpiled Lua, got: %s", preview.Sections[0].Transpiled)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
