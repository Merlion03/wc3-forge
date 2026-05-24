package doodadsdoo

import (
	"encoding/json"
	"os"
	"testing"
)

// Fixture 1: wc3-survival's war3map.doo. It's a flat arena map with very
// few (likely zero) placed doodads — primarily exercises the header + empty
// trailing special-doodad section path.
const fixturePathV16 = `C:\Users\4step\projects\wc3-survival-game\map\extracted\war3map.doo`

// Fixture 2: enfos v2.64f's war3map.doo. JASS-era map packed with terrain
// decoration — should produce hundreds of doodad entries. Exercises the full
// per-doodad code path including item-drop sets and the subversion-9/11
// skin_id heuristic.
const fixturePathEnfo = `C:\Users\4step\maps-extracted\enfo-v2.64f\war3map.doo`

func TestParse_v1_6(t *testing.T) {
	data, err := os.ReadFile(fixturePathV16)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got, want := string(f.Format[:]), "W3do"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
	if f.Version != 7 && f.Version != 8 {
		t.Errorf("unexpected Version %d (expected 7 or 8)", f.Version)
	}
	if f.SubVersion != 9 && f.SubVersion != 11 {
		t.Errorf("unexpected SubVersion %d (expected 9 or 11)", f.SubVersion)
	}
	if len(f.Doodads) < 0 {
		// Tautological — the >= 0 check from the spec is to assert that
		// the file parses cleanly even when empty. The real assertion is
		// just "Parse didn't error".
		t.Errorf("negative doodad count? %d", len(f.Doodads))
	}

	t.Logf("Format=%q Version=%d SubVersion=%d Doodads=%d SpecialFormat=%d SpecialDoodads=%d",
		string(f.Format[:]), f.Version, f.SubVersion, len(f.Doodads), f.SpecialFormat, len(f.SpecialDoodads))

	// Per-doodad sanity (probably zero iterations on this fixture).
	for i, d := range f.Doodads {
		validateDoodad(t, i, d)
	}

	// Sample up to 5 doodads as JSON for visual sanity. (Empty fixture →
	// SpecialDoodads only, which is still informative.)
	logSample(t, f, 5)
}

func TestParse_enfo(t *testing.T) {
	data, err := os.ReadFile(fixturePathEnfo)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got, want := string(f.Format[:]), "W3do"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
	if f.Version != 7 && f.Version != 8 {
		t.Errorf("unexpected Version %d (expected 7 or 8)", f.Version)
	}
	if f.SubVersion != 9 && f.SubVersion != 11 {
		t.Errorf("unexpected SubVersion %d (expected 9 or 11)", f.SubVersion)
	}

	t.Logf("Format=%q Version=%d SubVersion=%d Doodads=%d SpecialFormat=%d SpecialDoodads=%d",
		string(f.Format[:]), f.Version, f.SubVersion, len(f.Doodads), f.SpecialFormat, len(f.SpecialDoodads))

	// Enfos is heavily decorated — should easily clear 50 (likely many hundreds).
	if len(f.Doodads) <= 50 {
		t.Errorf("expected >50 doodads in enfos fixture, got %d", len(f.Doodads))
	}

	for i, d := range f.Doodads {
		validateDoodad(t, i, d)
	}

	logSample(t, f, 5)
}

// validateDoodad asserts per-entity invariants used by both fixtures.
func validateDoodad(t *testing.T, i int, d Doodad) {
	t.Helper()

	if len(d.TypeID) != 4 {
		t.Errorf("doodad[%d].TypeID = %q (len %d), want exactly 4 bytes", i, d.TypeID, len(d.TypeID))
	}
	for j, c := range []byte(d.TypeID) {
		// FourCCs are printable ASCII. Random-set placeholders use letters+digits.
		if c < 0x20 || c > 0x7E {
			t.Errorf("doodad[%d].TypeID byte %d = 0x%02x (not printable ASCII)", i, j, c)
		}
	}

	// Game-coord sanity. Doodads can sit in the unplayable border so use a
	// looser bound than units: 16384 game units gives a couple maps of slack
	// either side and still catches scrambled scale=128 garbage.
	if abs32(d.Position[0]) >= 16384 || abs32(d.Position[1]) >= 16384 {
		t.Errorf("doodad[%d] position out of bounds: %v", i, d.Position)
	}

	if d.SkinID != "" && len(d.SkinID) != 4 {
		t.Errorf("doodad[%d].SkinID = %q (len %d), want 4 or empty", i, d.SkinID, len(d.SkinID))
	}
}

// logSample dumps up to n parsed doodads (and up to 3 special-doodads) as
// indented JSON for human eyeball inspection.
func logSample(t *testing.T, f *File, n int) {
	t.Helper()

	type sample struct {
		Format         string          `json:"format"`
		Version        uint32          `json:"version"`
		SubVersion     uint32          `json:"sub_version"`
		DoodadsTotal   int             `json:"doodads_total"`
		DoodadsSample  []Doodad        `json:"doodads_sample"`
		SpecialFormat  uint32          `json:"special_format"`
		SpecialsTotal  int             `json:"specials_total"`
		SpecialsSample []SpecialDoodad `json:"specials_sample"`
	}

	d := f.Doodads
	if len(d) > n {
		d = d[:n]
	}
	sd := f.SpecialDoodads
	if len(sd) > 3 {
		sd = sd[:3]
	}

	s := sample{
		Format:         string(f.Format[:]),
		Version:        f.Version,
		SubVersion:     f.SubVersion,
		DoodadsTotal:   len(f.Doodads),
		DoodadsSample:  d,
		SpecialFormat:  f.SpecialFormat,
		SpecialsTotal:  len(f.SpecialDoodads),
		SpecialsSample: sd,
	}
	pretty, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("Sample:\n%s", string(pretty))
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}
