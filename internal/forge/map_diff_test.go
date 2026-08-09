package forge

import (
	"encoding/json"
	"testing"
)

func findMapDiffChange(t *testing.T, changes []MapDiffChange, entity string) MapDiffChange {
	t.Helper()
	for _, c := range changes {
		if c.Entity == entity {
			return c
		}
	}
	t.Fatalf("missing diff change %q in %#v", entity, changes)
	return MapDiffChange{}
}

func TestPreviewMapPatchDoesNotMutateLiveSession(t *testing.T) {
	s := patchTestSession()
	result, err := s.PreviewMapPatch(MapDiffRequest{Operations: []json.RawMessage{
		patchRaw(`{"op":"units.move","creation_number":10,"x":100,"y":200,"z":0}`),
		patchRaw(`{"op":"doodads.create","type_id":"B000","x":32,"y":64,"z":0}`),
		patchRaw(`{"op":"regions.rename","creation_number":30,"name":"Expansion"}`),
		patchRaw(`{"op":"terrain.set_tile","col":1,"row":1,"ground_tile_id":"Ldro"}`),
		patchRaw(`{"op":"map.info_set","updates":{"name":"Previewed"}}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.OperationCount != 5 || result.TotalChanges < 5 {
		t.Fatalf("result=%#v", result)
	}
	if result.Bounds == nil || result.Bounds.MinX > 1 || result.Bounds.MaxX < 100 || result.Bounds.MaxY < 200 {
		t.Fatalf("bounds=%#v", result.Bounds)
	}
	if s.IsDirty() || len(s.history) != 0 {
		t.Fatal("preview changed live dirty/history state")
	}
	if got := s.units.Entities[0].Position; got != [3]float32{1, 2, 3} {
		t.Fatalf("live unit moved during preview: %v", got)
	}
	if len(s.doodads.Doodads) != 1 || s.regions.Regions[0].Name != "Spawn" || s.info.Name != "Old" {
		t.Fatal("preview mutated live map data")
	}

	unit := findMapDiffChange(t, result.Changes, "unit:10")
	if unit.Action != "modify" || unit.Before == nil || unit.After == nil || unit.Bounds == nil {
		t.Fatalf("unit diff=%#v", unit)
	}
	created := findMapDiffChange(t, result.Changes, "doodad:21")
	if created.Action != "create" || created.Before != nil || created.After == nil {
		t.Fatalf("create diff=%#v", created)
	}
	info := findMapDiffChange(t, result.Changes, "map_info")
	if info.Action != "modify" || info.Before == nil || info.After == nil {
		t.Fatalf("map info diff=%#v", info)
	}
}

func TestPreviewMapPatchSequentialCreateThenMove(t *testing.T) {
	s := patchTestSession()
	result, err := s.PreviewMapPatch(MapDiffRequest{Operations: []json.RawMessage{
		patchRaw(`{"op":"units.create","type_id":"hpea","player":0,"x":0,"y":0,"z":0}`),
		patchRaw(`{"op":"units.move","creation_number":11,"x":100,"y":100,"z":0}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations[0].PredictedCreationID != 11 {
		t.Fatalf("predicted=%d", result.Operations[0].PredictedCreationID)
	}
	change := findMapDiffChange(t, result.Changes, "unit:11")
	if change.Action != "create" || change.Bounds == nil || change.Bounds.MinX != 100 || change.Bounds.MinY != 100 {
		t.Fatalf("created change=%#v", change)
	}
	if len(s.units.Entities) != 1 {
		t.Fatalf("preview created live unit: %d", len(s.units.Entities))
	}
}

func TestPreviewMapPatchLimitTruncatesDetailsButKeepsTotals(t *testing.T) {
	s := patchTestSession()
	result, err := s.PreviewMapPatch(MapDiffRequest{Limit: 1, Operations: []json.RawMessage{
		patchRaw(`{"op":"terrain.paint_tile","col":1,"row":1,"radius":2,"shape":"square","ground_tile_id":"Ldro"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalChanges <= 1 || result.Count != 1 || !result.Truncated {
		t.Fatalf("result=%#v", result)
	}
	if result.Bounds == nil {
		t.Fatal("truncated preview lost global bounds")
	}
}

func TestPreviewMapPatchRejectsInvalidPatchWithoutMutation(t *testing.T) {
	s := patchTestSession()
	_, err := s.PreviewMapPatch(MapDiffRequest{Operations: []json.RawMessage{
		patchRaw(`{"op":"units.move","creation_number":999,"x":1,"y":2,"z":3}`),
	}})
	if err == nil {
		t.Fatal("expected invalid target error")
	}
	if s.IsDirty() || len(s.history) != 0 || s.units.Entities[0].Position != [3]float32{1, 2, 3} {
		t.Fatal("invalid preview mutated live session")
	}
}
