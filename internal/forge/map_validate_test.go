package forge

import (
	"strings"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/formats/doodadsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3e"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3r"
)

func validateTestSession() *Session {
	terrain := &w3e.File{
		Width: 3, Height: 3, CenterOffset: [2]float32{-128, -128},
		GroundTilesets: []string{"Ldrt"},
		CliffTilesets:  []string{"CLdi"},
		Tiles:          make([]w3e.Tilepoint, 9),
	}
	for i := range terrain.Tiles {
		terrain.Tiles[i].CliffTexture = 15
	}
	units := &unitsdoo.File{Entities: []unitsdoo.Entity{
		{TypeID: "hfoo", CreationNumber: 1, Position: [3]float32{0, 0, 0}},
		{TypeID: slocTypeID, Player: 0, CreationNumber: 2, Position: [3]float32{64, 64, 0}},
	}}
	doodads := &doodadsdoo.File{Doodads: []doodadsdoo.Doodad{
		{TypeID: "LTlt", CreationNumber: 1, Position: [3]float32{0, 0, 0}},
	}}
	regions := &w3r.File{Version: 5, Regions: []w3r.Region{
		{Name: "R", CreationNumber: 1, Left: -10, Bottom: -10, Right: 10, Top: 10},
	}}
	return &Session{loaded: true, terrain: terrain, units: units, doodads: doodads, regions: regions}
}

func diagnosticCodes(r MapValidationResult) string {
	var b strings.Builder
	for _, d := range r.Diagnostics {
		b.WriteString(d.Code)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestValidateMapClean(t *testing.T) {
	s := validateTestSession()
	r, err := s.ValidateMap()
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid || r.Errors != 0 || r.Warnings != 0 {
		t.Fatalf("%#v", r)
	}
}

func TestValidateMapStructuralDiagnostics(t *testing.T) {
	s := validateTestSession()
	s.units.Entities = append(s.units.Entities,
		unitsdoo.Entity{TypeID: "bad", CreationNumber: 1, Position: [3]float32{0, 0, 0}},
		unitsdoo.Entity{TypeID: slocTypeID, Player: 0, CreationNumber: 3, Position: [3]float32{0, 0, 0}},
	)
	s.regions.Regions[0].Left, s.regions.Regions[0].Right = 20, 10
	s.unitMods = &w3objmod.File{Version: 3, Customs: []w3objmod.CustomObject{
		{ID: "u001", BaseID: "hfoo"}, {ID: "u001", BaseID: "hfoo"}, {ID: "xxx", BaseID: "hfoo"},
	}}
	r, err := s.ValidateMap()
	if err != nil {
		t.Fatal(err)
	}
	codes := diagnosticCodes(r)
	for _, want := range []string{"entity.duplicate_creation_number", "start_location.duplicate_index", "region.invalid_bounds", "object.invalid_fourcc", "object.duplicate_custom_id"} {
		if !strings.Contains(codes, want) {
			t.Errorf("missing %s in %s", want, codes)
		}
	}
	if r.Valid || r.Errors < 5 {
		t.Fatalf("%#v", r)
	}
}

func TestValidateMapOutsidePlacementIsWarning(t *testing.T) {
	s := validateTestSession()
	s.units.Entities[0].Position = [3]float32{999, 999, 0}
	r, err := s.ValidateMap()
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid || r.Errors != 0 || r.Warnings != 1 || r.Diagnostics[0].Code != "entity.outside_map" {
		t.Fatalf("%#v", r)
	}
}

func TestValidateMapTerrainPaletteIndex(t *testing.T) {
	s := validateTestSession()
	s.terrain.Tiles[3].GroundTexture = 4
	r, err := s.ValidateMap()
	if err != nil {
		t.Fatal(err)
	}
	if r.Valid || !strings.Contains(diagnosticCodes(r), "terrain.invalid_ground_texture_index") {
		t.Fatalf("%#v", r)
	}
}
