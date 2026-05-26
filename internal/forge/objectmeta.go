package forge

import (
	"strconv"
	"strings"

	"github.com/StephenSHorton/wc3-forge/internal/formats/slk"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
)

// UnitFieldMeta is one row of UnitMetaData.slk — the description of a single
// editable field on units (or items, depending on the use* flags). Units and
// items share this metadata table; the editor filters by the use* flags.
//
// The applicability flags (UseUnit/UseHero/UseBuilding/UseItem) double as the
// filter that decides which fields show up in the unit editor vs. item
// editor. A field with UseUnit=1 AND UseItem=1 appears in both.
type UnitFieldMeta struct {
	ID          string // FourCC of the field (the w3u modification_id)
	Field       string // SLK column name; matches slk.Mapped column-key after lowercasing
	SLK         string // source SLK family: "Profile" | "UnitData" | "UnitBalance" | "UnitWeapons" | "UnitAbilities" | "UnitUI" | "ItemData"
	Index       int    // display order within its category; -1 means "unindexed" (variant subfields)
	Category    string // "abil" | "art" | "combat" | "editor" | "move" | "path" | "sound" | "stats" | "tech" | "text"
	DisplayName string // WESTRING_* key; resolved at render time, not parse time
	Type        string // value type: "int" | "real" | "bool" | "string" | "abilityList" | "icon" | "model" | ...
	Data        int    // variant repeat count; >0 means the field is split into N synthetic variants (e.g. missileart→missileart,missileart2)
	MinVal      string // numeric constraints kept as strings — consumer parses on demand (and the column is often blank)
	MaxVal      string
	UseUnit     bool
	UseHero     bool
	UseBuilding bool
	UseItem     bool
}

// AppliesToUnits reports whether this field is editable on any of the
// unit-family object kinds (regular units, heroes, buildings). Items use the
// same metadata table but a separate flag — caller filters with this for the
// unit editor.
func (f UnitFieldMeta) AppliesToUnits() bool {
	return f.UseUnit || f.UseHero || f.UseBuilding
}

// UnitMetadata is the parsed UnitMetaData.slk: every editable unit/item field,
// indexed for both ordered display (Fields, sorted by category then Index)
// and FourCC lookup (ByID).
type UnitMetadata struct {
	Fields []UnitFieldMeta
	ByID   map[string]*UnitFieldMeta
}

// FieldMap builds the FourCC → column-name map w3objmod.Parse needs to
// translate war3map.w3u modification IDs into SLK column-name space, so
// shadow overrides can be merged column-for-column with the base table.
//
// Lowercased to match slk.Mapped's column convention.
func (m *UnitMetadata) FieldMap() w3objmod.FieldMap {
	fm := w3objmod.FieldMap{}
	for i := range m.Fields {
		f := &m.Fields[i]
		fm[f.ID] = strings.ToLower(f.Field)
	}
	return fm
}

// ParseUnitMetadata parses UnitMetaData.slk bytes into UnitMetadata.
//
// One row per editable field. Row key is the field FourCC (also stored as
// the `id` column). Empty rows + the SLK header are handled by slk.Mapped;
// we just transform rows into UnitFieldMeta records.
func ParseUnitMetadata(data []byte) (*UnitMetadata, error) {
	m := slk.New()
	if err := m.Load(data); err != nil {
		return nil, err
	}
	out := &UnitMetadata{
		Fields: make([]UnitFieldMeta, 0, len(m.Rows)),
		ByID:   make(map[string]*UnitFieldMeta, len(m.Rows)),
	}
	for key, row := range m.Rows {
		// Some SLK rows carry only an `id` column (no `field`) — skip those
		// as they can't be merged into a base table.
		field := row.String("field")
		if field == "" {
			continue
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(row.String("index")))
		data, _ := strconv.Atoi(strings.TrimSpace(row.String("data")))
		out.Fields = append(out.Fields, UnitFieldMeta{
			ID:          key,
			Field:       field,
			SLK:         row.String("slk"),
			Index:       idx,
			Category:    row.String("category"),
			DisplayName: row.String("displayname"),
			Type:        row.String("type"),
			Data:        data,
			MinVal:      row.String("minval"),
			MaxVal:      row.String("maxval"),
			UseUnit:     row.String("useunit") == "1",
			UseHero:     row.String("usehero") == "1",
			UseBuilding: row.String("usebuilding") == "1",
			UseItem:     row.String("useitem") == "1",
		})
	}
	// Build the FourCC → *UnitFieldMeta index. Pointers into the slice are
	// stable for the lifetime of this UnitMetadata (we never mutate Fields
	// after construction).
	for i := range out.Fields {
		out.ByID[out.Fields[i].ID] = &out.Fields[i]
	}
	return out, nil
}
