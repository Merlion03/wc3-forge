package forge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/formats/doodadsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3e"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3r"
)

func patchRaw(s string) json.RawMessage { return json.RawMessage(s) }

func patchTestSession() *Session {
	terrain := &w3e.File{
		Width: 3, Height: 3, CenterOffset: [2]float32{-128, -128},
		GroundTilesets: []string{"Ldrt", "Ldro"},
		CliffTilesets:  []string{"CLdi"},
		Tiles:          make([]w3e.Tilepoint, 9),
	}
	units := &unitsdoo.File{Entities: []unitsdoo.Entity{
		{TypeID: "hfoo", CreationNumber: 10, Position: [3]float32{1, 2, 3}, Scale: [3]float32{1, 1, 1}},
	}}
	doodads := &doodadsdoo.File{Doodads: []doodadsdoo.Doodad{
		{TypeID: "LTlt", CreationNumber: 20, Position: [3]float32{4, 5, 6}, Scale: [3]float32{1, 1, 1}},
	}}
	regions := &w3r.File{Version: 5, Regions: []w3r.Region{
		{Name: "Spawn", CreationNumber: 30, Left: -10, Bottom: -10, Right: 10, Top: 10},
	}}
	return &Session{
		loaded:  true,
		info:    &w3i.Info{Name: "Old"},
		units:   units,
		doodads: doodads,
		regions: regions,
		terrain: terrain,
	}
}

func TestApplyMapPatchDryRunDoesNotMutate(t *testing.T) {
	s := patchTestSession()
	result, err := s.ApplyMapPatch(MapApplyPatchRequest{DryRun: true, Operations: []json.RawMessage{
		patchRaw(`{"op":"units.move","creation_number":10,"x":9,"y":8,"z":7}`),
		patchRaw(`{"op":"doodads.create","type_id":"B000","x":1,"y":2,"z":0}`),
		patchRaw(`{"op":"regions.create","name":"Expansion","min_x":20,"min_y":20,"max_x":30,"max_y":30}`),
		patchRaw(`{"op":"terrain.paint_tile","col":1,"row":1,"radius":1,"ground_tile_id":"Ldro"}`),
		patchRaw(`{"op":"map.info_set","updates":{"name":"New"}}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || !result.DryRun || s.IsDirty() || len(s.history) != 0 {
		t.Fatalf("dry-run mutated: %#v", result)
	}
	if s.units.Entities[0].Position != [3]float32{1, 2, 3} || s.info.Name != "Old" {
		t.Fatal("dry-run changed map state")
	}
	if result.Operations[1].PredictedCreationID != 21 || result.Operations[2].PredictedCreationID != 31 {
		t.Fatalf("predictions=%#v", result.Operations)
	}
}

func TestApplyMapPatchSequentialPreflight(t *testing.T) {
	s := patchTestSession()
	_, err := s.ApplyMapPatch(MapApplyPatchRequest{DryRun: true, Operations: []json.RawMessage{
		patchRaw(`{"op":"units.delete","creation_number":10}`),
		patchRaw(`{"op":"units.move","creation_number":10,"x":1,"y":1,"z":1}`),
	}})
	if err == nil || !strings.Contains(err.Error(), "operation 1") {
		t.Fatalf("err=%v", err)
	}

	result, err := s.ApplyMapPatch(MapApplyPatchRequest{DryRun: true, Operations: []json.RawMessage{
		patchRaw(`{"op":"units.create","type_id":"hpea","player":0,"x":0,"y":0,"z":0}`),
		patchRaw(`{"op":"units.move","creation_number":11,"x":100,"y":100,"z":0}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations[0].PredictedCreationID != 11 {
		t.Fatalf("predicted=%d", result.Operations[0].PredictedCreationID)
	}
}

func TestApplyMapPatchSuccessIsOneUndo(t *testing.T) {
	s := patchTestSession()
	result, err := s.ApplyMapPatch(MapApplyPatchRequest{Label: "Agent batch", Operations: []json.RawMessage{
		patchRaw(`{"op":"units.move","creation_number":10,"x":100,"y":200,"z":0}`),
		patchRaw(`{"op":"doodads.move","creation_number":20,"x":-100,"y":-200,"z":0}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || len(s.history) != 1 {
		t.Fatalf("result=%#v history=%d", result, len(s.history))
	}
	if err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if s.units.Entities[0].Position != [3]float32{1, 2, 3} || s.doodads.Doodads[0].Position != [3]float32{4, 5, 6} {
		t.Fatal("one Undo did not revert the complete patch")
	}
}

func TestApplyMapPatchCreateThenReferencePredictedID(t *testing.T) {
	s := patchTestSession()
	result, err := s.ApplyMapPatch(MapApplyPatchRequest{Operations: []json.RawMessage{
		patchRaw(`{"op":"units.create","type_id":"hpea","player":0,"x":0,"y":0,"z":0}`),
		patchRaw(`{"op":"units.move","creation_number":11,"x":100,"y":100,"z":0}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations[0].PredictedCreationID != 11 || len(s.units.Entities) != 2 {
		t.Fatalf("result=%#v units=%d", result, len(s.units.Entities))
	}
	if got := s.units.Entities[1].Position; got != [3]float32{100, 100, 0} {
		t.Fatalf("created unit pos=%v", got)
	}
	if err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if len(s.units.Entities) != 1 {
		t.Fatalf("created unit survived undo: %d units", len(s.units.Entities))
	}
}

func TestApplyMapPatchRejectsUnknownOperationFields(t *testing.T) {
	s := patchTestSession()
	_, err := s.ApplyMapPatch(MapApplyPatchRequest{DryRun: true, Operations: []json.RawMessage{
		patchRaw(`{"op":"units.move","creation_number":10,"x":1,"y":2,"z":3,"surprise":true}`),
	}})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err=%v", err)
	}
}

func TestApplyMapPatchRequiresOperationFields(t *testing.T) {
	s := patchTestSession()
	tests := []json.RawMessage{
		patchRaw(`{"op":"units.move","creation_number":10}`),
		patchRaw(`{"op":"units.move","creation_number":10,"x":null,"y":2,"z":3}`),
		patchRaw(`{"op":"regions.create","name":"R","min_x":0,"min_y":0,"max_x":10}`),
		patchRaw(`{"op":"terrain.brush_height","col":1,"row":1,"radius":1,"mode":"flatten"}`),
	}
	for i, raw := range tests {
		_, err := s.ApplyMapPatch(MapApplyPatchRequest{DryRun: true, Operations: []json.RawMessage{raw}})
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("case %d: err=%v", i, err)
		}
	}
}

func TestApplyMapPatchKeepsStartLocationsOutOfUnitsSurface(t *testing.T) {
	s := patchTestSession()
	s.units.Entities = append(s.units.Entities, unitsdoo.Entity{
		TypeID: slocTypeID, Player: 3, CreationNumber: 99, Position: [3]float32{50, 50, 0},
	})
	_, err := s.ApplyMapPatch(MapApplyPatchRequest{DryRun: true, Operations: []json.RawMessage{
		patchRaw(`{"op":"units.move","creation_number":99,"x":1,"y":2,"z":3}`),
	}})
	if err == nil || !strings.Contains(err.Error(), "no unit") {
		t.Fatalf("sloc units.move err=%v", err)
	}
	result, err := s.ApplyMapPatch(MapApplyPatchRequest{DryRun: true, Operations: []json.RawMessage{
		patchRaw(`{"op":"units.create","type_id":"hpea","player":0,"x":1,"y":2}`),
	}})
	if err != nil {
		t.Fatalf("ordinary create with sloc present: %v", err)
	}
	if result.Operations[0].PredictedCreationID != 100 {
		t.Fatalf("predicted creation id=%d, want 100 (sloc CN participates in allocator)", result.Operations[0].PredictedCreationID)
	}
	_, err = s.ApplyMapPatch(MapApplyPatchRequest{DryRun: true, Operations: []json.RawMessage{
		patchRaw(`{"op":"units.create","type_id":"sloc","player":3,"x":1,"y":2}`),
	}})
	if err == nil || !strings.Contains(err.Error(), "start location") {
		t.Fatalf("sloc units.create err=%v", err)
	}
}
