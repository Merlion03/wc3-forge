package forge

import (
	"encoding/json"
	"testing"
)

// TestAgentIntelligenceWorkflow exercises the intended v1.1 agent loop on one
// in-memory map: discover -> dry-run -> apply -> validate -> one-step undo.
// It deliberately stays above the individual mutator layer so regressions in
// the composition between scene_query, map_apply_patch, validation, and history
// are caught even when each feature's unit tests still pass independently.
func TestAgentIntelligenceWorkflow(t *testing.T) {
	s := patchTestSession()

	before, err := s.QueryScene(SceneQuery{
		Kinds: []string{"unit", "region"},
		Sort:  "id",
	})
	if err != nil {
		t.Fatal(err)
	}
	if before.Matched != 2 {
		t.Fatalf("initial scene matched=%d, want 2", before.Matched)
	}

	ops := []json.RawMessage{
		patchRaw(`{"op":"units.move","creation_number":10,"x":64,"y":64,"z":0}`),
		patchRaw(`{"op":"regions.create","name":"Expansion","min_x":32,"min_y":32,"max_x":96,"max_y":96}`),
	}
	dry, err := s.ApplyMapPatch(MapApplyPatchRequest{Label: "Expansion", DryRun: true, Operations: ops})
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.Applied || dry.Operations[1].PredictedCreationID != 31 {
		t.Fatalf("dry-run=%#v", dry)
	}
	if s.IsDirty() || len(s.history) != 0 {
		t.Fatal("dry-run changed dirty/history state")
	}

	applied, err := s.ApplyMapPatch(MapApplyPatchRequest{Label: "Expansion", Operations: ops})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || len(s.history) != 1 {
		t.Fatalf("apply=%#v history=%d", applied, len(s.history))
	}

	validation, err := s.ValidateMap()
	if err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || validation.Errors != 0 {
		t.Fatalf("validation=%#v", validation)
	}

	after, err := s.QueryScene(SceneQuery{
		Kinds: []string{"unit", "region"},
		Where: SceneWhere{IDs: []int64{10, 31}},
		Sort:  "id",
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Matched != 2 {
		t.Fatalf("post-apply scene=%#v", after)
	}

	if err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := s.units.Entities[0].Position; got != [3]float32{1, 2, 3} {
		t.Fatalf("unit after undo=%v", got)
	}
	if len(s.regions.Regions) != 1 || s.regions.Regions[0].CreationNumber != 30 {
		t.Fatalf("regions after undo=%#v", s.regions.Regions)
	}
}
