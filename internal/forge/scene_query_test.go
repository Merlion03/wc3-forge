package forge

import (
	"math"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/formats/doodadsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3r"
)

func sceneQueryFixture() *Session {
	return &Session{
		loaded: true,
		units: &unitsdoo.File{Entities: []unitsdoo.Entity{
			{TypeID: "hfoo", Player: 0, Position: [3]float32{0, 0, 0}, Rotation: 1, Scale: [3]float32{1, 1, 1}, CreationNumber: 10},
			{TypeID: "hpea", Player: 1, Position: [3]float32{100, 0, 0}, Rotation: 2, Scale: [3]float32{1, 1, 1}, CreationNumber: 11},
			{TypeID: slocTypeID, Player: 3, Position: [3]float32{50, 50, 0}, Rotation: slocStartRotation, Scale: [3]float32{1, 1, 1}, CreationNumber: 99},
		}},
		doodads: &doodadsdoo.File{Doodads: []doodadsdoo.Doodad{
			{TypeID: "LTlt", Position: [3]float32{25, 0, 0}, Scale: [3]float32{1, 1, 1}, Variation: 2, Life: 100, Flags: 2, CreationNumber: 20},
			{TypeID: "B000", Position: [3]float32{300, 300, 0}, Scale: [3]float32{1, 1, 1}, Life: 80, Flags: 3, CreationNumber: 21},
		}},
		regions: &w3r.File{Version: 5, Regions: []w3r.Region{
			{Name: "Spawn Area", CreationNumber: 30, Left: -10, Bottom: -10, Right: 10, Top: 10},
			{Name: "Far Expansion", CreationNumber: 31, Left: 200, Bottom: 200, Right: 250, Top: 250},
		}},
	}
}

func strptr(v string) *string { return &v }
func u32ptr(v uint32) *uint32 { return &v }
func i32ptr(v int32) *int32   { return &v }

func TestSceneQuery_DefaultKindsNoSlocDuplicate(t *testing.T) {
	s := sceneQueryFixture()
	got, err := s.QueryScene(SceneQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Matched != 7 || got.Count != 7 {
		t.Fatalf("matched/count = %d/%d, want 7/7", got.Matched, got.Count)
	}
	counts := map[string]int{}
	for _, item := range got.Items {
		counts[item["kind"].(string)]++
	}
	if counts["unit"] != 2 || counts["doodad"] != 2 || counts["region"] != 2 || counts["start_location"] != 1 {
		t.Fatalf("unexpected kind counts: %#v", counts)
	}
}

func TestSceneQuery_AttributeFilters(t *testing.T) {
	s := sceneQueryFixture()
	got, err := s.QueryScene(SceneQuery{
		Kinds: []string{"unit"},
		Where: SceneWhere{TypeID: strptr("hpea"), Player: u32ptr(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Items[0]["id"] != int64(11) {
		t.Fatalf("unexpected result: %#v", got)
	}

	got, err = s.QueryScene(SceneQuery{
		Kinds: []string{"region"},
		Where: SceneWhere{NameContains: strptr("EXPANSION")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Items[0]["id"] != int64(31) {
		t.Fatalf("name filter: %#v", got)
	}
}

func TestSceneQuery_RadiusIntersectsRegion(t *testing.T) {
	s := sceneQueryFixture()
	got, err := s.QueryScene(SceneQuery{
		Kinds:   []string{"unit", "doodad", "region"},
		Spatial: SceneSpatial{Radius: &SceneRadius{X: 20, Y: 0, Radius: 10}},
		Sort:    "id",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Spawn Area touches the circle at x=10, the tree is at x=25, and the
	// hfoo unit is 20 units away (outside).
	if got.Count != 2 {
		t.Fatalf("count=%d items=%#v", got.Count, got.Items)
	}
	if got.Items[0]["id"] != int64(20) || got.Items[1]["id"] != int64(30) {
		t.Fatalf("unexpected ids: %#v", got.Items)
	}
}

func TestSceneQuery_RectAndWithinRegion(t *testing.T) {
	s := sceneQueryFixture()
	got, err := s.QueryScene(SceneQuery{
		Spatial: SceneSpatial{Rect: &SceneRect{MinX: -1, MinY: -1, MaxX: 30, MaxY: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Matched != 3 { // hfoo + LTlt + Spawn Area intersection.
		t.Fatalf("rect matched=%d items=%#v", got.Matched, got.Items)
	}

	got, err = s.QueryScene(SceneQuery{
		Kinds:   []string{"unit", "doodad", "start_location"},
		Spatial: SceneSpatial{WithinRegion: i32ptr(30)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Items[0]["id"] != int64(10) {
		t.Fatalf("within region: %#v", got)
	}
}

func TestSceneQuery_NearestUsesRegionEdgeDistance(t *testing.T) {
	s := sceneQueryFixture()
	got, err := s.QueryScene(SceneQuery{
		Kinds:     []string{"region", "doodad"},
		NearestTo: &ScenePoint{X: 190, Y: 225},
		Limit:     3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 3 {
		t.Fatalf("count=%d", got.Count)
	}
	if got.Items[0]["id"] != int64(31) {
		t.Fatalf("nearest should be Far Expansion by 10 units: %#v", got.Items)
	}
	d, ok := got.Items[0]["distance"].(float64)
	if !ok || math.Abs(d-10) > 1e-9 {
		t.Fatalf("distance=%v, want 10", got.Items[0]["distance"])
	}
}

func TestSceneQuery_Projection(t *testing.T) {
	s := sceneQueryFixture()
	got, err := s.QueryScene(SceneQuery{
		Kinds:  []string{"unit"},
		Where:  SceneWhere{IDs: []int64{10}},
		Fields: []string{"type_id", "position"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item := got.Items[0]
	if len(item) != 4 { // kind + id are always present.
		t.Fatalf("projection leaked fields: %#v", item)
	}
	if item["kind"] != "unit" || item["id"] != int64(10) || item["type_id"] != "hfoo" {
		t.Fatalf("projection result: %#v", item)
	}
	if _, ok := item["player"]; ok {
		t.Fatalf("player should have been projected out: %#v", item)
	}
}

func TestSceneQuery_Pagination(t *testing.T) {
	s := sceneQueryFixture()
	got, err := s.QueryScene(SceneQuery{Sort: "id", Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Matched != 7 || got.Count != 2 || !got.Truncated || got.NextOffset != 3 {
		t.Fatalf("pagination metadata: %#v", got)
	}
}

func TestSceneQuery_Validation(t *testing.T) {
	s := sceneQueryFixture()
	tests := []SceneQuery{
		{Kinds: []string{"wat"}},
		{Where: SceneWhere{TypeID: strptr("abc")}},
		{Spatial: SceneSpatial{Radius: &SceneRadius{Radius: -1}}},
		{Spatial: SceneSpatial{Rect: &SceneRect{MinX: 2, MaxX: 1}}},
		{Sort: "distance"},
		{Order: "sideways"},
		{Limit: sceneQueryMaxLimit + 1},
		{Offset: -1},
		{Fields: []string{"distance"}},
		{Fields: []string{"not_a_field"}},
		{Spatial: SceneSpatial{WithinRegion: i32ptr(999)}},
	}
	for i, q := range tests {
		if _, err := s.QueryScene(q); err == nil {
			t.Errorf("case %d: expected error for %#v", i, q)
		}
	}

	unloaded := &Session{}
	if _, err := unloaded.QueryScene(SceneQuery{}); err == nil {
		t.Error("unloaded session: expected error")
	}
}
