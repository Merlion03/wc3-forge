package forge

import (
	"encoding/json"
	"testing"
)

// Generic-pipeline tests. The Phase-1b unitmods/handlers tests stay
// units-specific (they're the regression bar for byte-faithful Phase-1b
// behavior); this file exercises the generic surface directly so Phase 2b
// has a copy-paste pattern when adding ItemsConfig + the other kinds.

// TestUnitsConfig_Registered confirms init wired UnitsConfig() into the
// kindConfigs lookup so undo/redo commands referencing Kind="units" can
// resolve their config.
func TestUnitsConfig_Registered(t *testing.T) {
	cfg := kindConfigFor("units")
	if cfg == nil {
		t.Fatal("UnitsConfig not registered")
	}
	if cfg.Kind != "units" {
		t.Errorf("Kind = %q, want units", cfg.Kind)
	}
	if cfg.ShadowFile != "war3map.w3u" {
		t.Errorf("ShadowFile = %q, want war3map.w3u", cfg.ShadowFile)
	}
	if cfg.ShadowOpt {
		t.Error("units ShadowOpt should be false (w3u is non-variant)")
	}
}

// TestKindConfigFor_UnknownPanics: an undo command for an unregistered kind
// is a programmer error (the kind should have been registered in init) —
// the lookup must panic so the bug surfaces immediately, not silently no-op.
func TestKindConfigFor_UnknownPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on unknown kind")
		}
	}()
	_ = kindConfigFor("not-a-real-kind")
}

// TestSetObjectField_GenericRoundTrip exercises the kind-agnostic mutator
// against the units config. The behavior is identical to TestSetUnitField_*
// in objects_mods_test.go — this version just calls SetObjectField directly
// instead of going through the legacy SetUnitField alias, proving the new
// surface is the source of truth (the alias being a thin wrapper).
func TestSetObjectField_GenericRoundTrip(t *testing.T) {
	stubUnitsBase(t)
	tmp := minimalFolderMap(t)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}

	cfg := UnitsConfig()
	if err := s.SetObjectField(cfg, "hpea", "name", "Generic"); err != nil {
		t.Fatalf("SetObjectField: %v", err)
	}
	mods := cfg.GetMods(s)
	if mods == nil || len(mods.OriginalEdits) != 1 {
		t.Fatalf("expected one OriginalEdit, got %+v", mods)
	}
	if mods.OriginalEdits[0].Overrides["unam"] != "Generic" {
		t.Errorf("override = %q, want Generic", mods.OriginalEdits[0].Overrides["unam"])
	}
	if !cfg.GetDirty(s) {
		t.Error("expected per-kind dirty flag set after SetObjectField")
	}
}

// TestAddCustomObject_Generic: create a custom via the generic path, verify
// the auto-allocated ID lands in the right slot.
func TestAddCustomObject_Generic(t *testing.T) {
	stubUnitsBase(t)
	tmp := minimalFolderMap(t)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cfg := UnitsConfig()
	id, err := s.AddCustomObject(cfg, "", "hpea")
	if err != nil {
		t.Fatalf("AddCustomObject: %v", err)
	}
	if id == "" || id[0] != 'h' {
		t.Errorf("allocator picked %q, want h-prefixed", id)
	}
	mods := cfg.GetMods(s)
	if mods == nil || len(mods.Customs) != 1 || mods.Customs[0].ID != id {
		t.Errorf("custom not appended: %+v", mods)
	}
}

// TestDeleteCustomObject_GenericUndo: delete-then-undo through the generic
// surface restores the row + its overrides.
func TestDeleteCustomObject_GenericUndo(t *testing.T) {
	stubUnitsBase(t)
	tmp := minimalFolderMap(t)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	cfg := UnitsConfig()
	id, err := s.AddCustomObject(cfg, "", "hpea")
	if err != nil {
		t.Fatalf("AddCustomObject: %v", err)
	}
	if err := s.SetObjectField(cfg, id, "name", "ToBeRestored"); err != nil {
		t.Fatalf("SetObjectField: %v", err)
	}
	if err := s.DeleteCustomObject(cfg, id); err != nil {
		t.Fatalf("DeleteCustomObject: %v", err)
	}
	if mods := cfg.GetMods(s); len(mods.Customs) != 0 {
		t.Fatalf("expected 0 customs post-delete, got %d", len(mods.Customs))
	}
	if err := s.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	mods := cfg.GetMods(s)
	if len(mods.Customs) != 1 || mods.Customs[0].Overrides["unam"] != "ToBeRestored" {
		t.Errorf("Undo did not restore overrides: %+v", mods.Customs)
	}
}

// TestMergedObjects_Generic confirms the generic merged-view returns the
// same shape as Phase-1b's MergedUnits when fed UnitsConfig.
//
// MergedObjects reads the shadow from the package-global `Current` session
// (mirroring Phase-1b MergedUnits behavior — handlers run against Current,
// not an arbitrary Session). This test drives Current accordingly + restores
// it on cleanup so concurrent tests aren't disturbed.
func TestMergedObjects_Generic(t *testing.T) {
	stubUnitsBase(t)
	tmp := minimalFolderMap(t)
	if err := Current.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { Current.Close() })
	if err := Current.SetObjectField(UnitsConfig(), "hpea", "name", "Merged"); err != nil {
		t.Fatalf("SetObjectField: %v", err)
	}
	merged, _, err := MergedObjects(UnitsConfig())
	if err != nil {
		t.Fatalf("MergedObjects: %v", err)
	}
	u, ok := merged["hpea"]
	if !ok {
		t.Fatal("hpea missing from merged view")
	}
	if u.Fields["name"] != "Merged" {
		t.Errorf("merged name = %q, want Merged", u.Fields["name"])
	}
	if !u.IsEdited {
		t.Error("IsEdited not set after override")
	}
}

// TestListObjects_Generic_WireShape locks in the JSON wire shape so a
// Phase-2b refactor or kind addition that accidentally renames a JSON tag
// fails this test (and the JS frontend silently breaks). Marshal a synthetic
// row and verify every Phase-1b key is present + spelled the same.
func TestListObjects_Generic_WireShape(t *testing.T) {
	row := objectsUnitsListEntity{
		ID: "X", Name: "Y", Race: "human", RaceLabel: "Human",
		Kind: "unit", Category: "Human", IsCustom: true, IsEdited: true,
		BaseID: "B", Campaign: true, IconArt: "ic.blp",
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"X","name":"Y","race":"human","race_label":"Human","kind":"unit","category":"Human","is_custom":true,"is_edited":true,"base_id":"B","campaign":true,"icon_art":"ic.blp"}`
	if string(b) != want {
		t.Errorf("wire shape drift:\n got=%s\nwant=%s", b, want)
	}
}
