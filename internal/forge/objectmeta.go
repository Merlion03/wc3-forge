package forge

import (
	"strconv"
	"strings"

	"github.com/StephenSHorton/wc3-forge/internal/formats/slk"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
)

// ObjectFieldMeta is one row of a per-kind MetaData.slk — the description of
// a single editable field on units, items, doodads, destructables, abilities,
// buffs, or upgrades. Each kind's MetaData.slk is laid out the same way (the
// SLK schema is identical across kinds); the use* columns determine which
// kinds the field applies to.
//
// Phase 2b: a single MetaData family (the units+items shared one) is no
// longer the only consumer — Doodad/Destructable/Ability/Buff/Upgrade
// metadata each have their own use* columns. We carry all of them on this
// struct; ParseObjectMetadata fills whatever's present in the SLK and zero-
// defaults the rest. Filter via the Applies* methods or directly through
// KindConfig.AppliesFn.
type ObjectFieldMeta struct {
	ID          string // FourCC of the field (the w3{u,t,b,d,h,a,q} modification_id)
	Field       string // SLK column name; matches slk.Mapped column-key after lowercasing
	SLK         string // source SLK family (Profile / UnitData / ItemData / DoodadData / DestructableData / AbilityData / BuffData / UpgradeData)
	Index       int    // display order within its category; -1 means "unindexed" (variant subfields)
	Category    string // "abil" | "art" | "combat" | "editor" | "move" | "path" | "sound" | "stats" | "tech" | "text"
	DisplayName string // WESTRING_* key; resolved at render time, not parse time
	Type        string // value type: "int" | "real" | "bool" | "string" | "abilityList" | "icon" | "model" | ...
	Data        int    // variant repeat count; >0 means the field is split into N synthetic variants (e.g. missileart→missileart,missileart2)
	MinVal      string // numeric constraints kept as strings — consumer parses on demand (and the column is often blank)
	MaxVal      string

	// Per-kind applicability flags. A single field row may carry several
	// (e.g. a font-color field could be shared across units AND items).
	// Each Applies* method ORs the relevant set so a kind's filter is one
	// boolean lookup.
	UseUnit        bool
	UseHero        bool
	UseBuilding    bool
	UseItem        bool
	UseAbility     bool
	UseBuff        bool
	UseDestructable bool
	UseDoodad      bool
	UseUpgrade     bool
}

// AppliesToUnits reports whether this field is editable on any unit-family
// kind (regular units, heroes, buildings). The units editor filters with this.
func (f ObjectFieldMeta) AppliesToUnits() bool {
	return f.UseUnit || f.UseHero || f.UseBuilding
}

// AppliesToItems reports whether this field is editable on items. Distinct
// from AppliesToUnits because the same field-metadata SLK feeds both, and the
// item editor wants only `UseItem == true` rows.
func (f ObjectFieldMeta) AppliesToItems() bool { return f.UseItem }

// AppliesToAbilities reports whether the field belongs in the ability editor.
func (f ObjectFieldMeta) AppliesToAbilities() bool { return f.UseAbility }

// AppliesToBuffs reports whether the field belongs in the buff editor.
func (f ObjectFieldMeta) AppliesToBuffs() bool { return f.UseBuff }

// AppliesToDestructables reports whether the field belongs in the
// destructable editor.
func (f ObjectFieldMeta) AppliesToDestructables() bool { return f.UseDestructable }

// AppliesToDoodads reports whether the field belongs in the doodad editor.
func (f ObjectFieldMeta) AppliesToDoodads() bool { return f.UseDoodad }

// AppliesToUpgrades reports whether the field belongs in the upgrade editor.
func (f ObjectFieldMeta) AppliesToUpgrades() bool { return f.UseUpgrade }

// UnitFieldMeta is the Phase-1b name retained as an alias of ObjectFieldMeta.
// Old call sites that reference the type by name keep compiling unchanged.
// New code should target ObjectFieldMeta directly.
type UnitFieldMeta = ObjectFieldMeta

// ObjectMetadata is a parsed per-kind MetaData.slk: every editable field for
// that kind, indexed for both ordered display (Fields, sorted by category
// then Index) and FourCC lookup (ByID).
type ObjectMetadata struct {
	Fields []ObjectFieldMeta
	ByID   map[string]*ObjectFieldMeta
}

// UnitMetadata is the Phase-1b name retained as an alias of ObjectMetadata.
type UnitMetadata = ObjectMetadata

// FieldMap builds the FourCC → column-name map w3objmod.Parse needs to
// translate a war3map.w3* modification ID into SLK column-name space, so
// shadow overrides can be merged column-for-column with the base table.
//
// Lowercased to match slk.Mapped's column convention.
func (m *ObjectMetadata) FieldMap() w3objmod.FieldMap {
	fm := w3objmod.FieldMap{}
	for i := range m.Fields {
		f := &m.Fields[i]
		fm[f.ID] = strings.ToLower(f.Field)
	}
	return fm
}

// ParseObjectMetadata parses a per-kind MetaData.slk into ObjectMetadata.
// Kind-agnostic: it reads all known use* columns; columns the file doesn't
// have are zero-defaulted (slk.MappedRow returns "" for missing keys, which
// the "1" comparison rejects). This means UnitMetaData.slk (which has
// useUnit/useHero/useBuilding/useItem but NOT useAbility/useBuff/etc.)
// produces UseAbility=false naturally, with no per-kind branching here.
//
// One row per editable field. Row key is the field FourCC (also stored as
// the `id` column). Empty rows + the SLK header are handled by slk.Mapped;
// we just transform rows into ObjectFieldMeta records.
func ParseObjectMetadata(data []byte) (*ObjectMetadata, error) {
	m := slk.New()
	if err := m.Load(data); err != nil {
		return nil, err
	}
	out := &ObjectMetadata{
		Fields: make([]ObjectFieldMeta, 0, len(m.Rows)),
		ByID:   make(map[string]*ObjectFieldMeta, len(m.Rows)),
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
		out.Fields = append(out.Fields, ObjectFieldMeta{
			ID:              key,
			Field:           field,
			SLK:             row.String("slk"),
			Index:           idx,
			Category:        row.String("category"),
			DisplayName:     row.String("displayname"),
			Type:            row.String("type"),
			Data:            data,
			MinVal:          row.String("minval"),
			MaxVal:          row.String("maxval"),
			UseUnit:         row.String("useunit") == "1",
			UseHero:         row.String("usehero") == "1",
			UseBuilding:     row.String("usebuilding") == "1",
			UseItem:         row.String("useitem") == "1",
			UseAbility:      row.String("useability") == "1",
			UseBuff:         row.String("usespecific") == "1" || row.String("usebuff") == "1",
			UseDestructable: row.String("usedestructable") == "1" || row.String("usedest") == "1",
			UseDoodad:       row.String("usedoodad") == "1" || row.String("usedood") == "1",
			UseUpgrade:      row.String("useupgrade") == "1" || row.String("useupgr") == "1",
		})
	}
	// Build the FourCC → *ObjectFieldMeta index. Pointers into the slice are
	// stable for the lifetime of this ObjectMetadata (we never mutate Fields
	// after construction).
	for i := range out.Fields {
		out.ByID[out.Fields[i].ID] = &out.Fields[i]
	}
	return out, nil
}

// ParseUnitMetadata is the Phase-1b name retained as a thin wrapper over the
// generic ParseObjectMetadata. Same behavior, same return shape — kept so
// existing call sites in handlers/tests keep compiling.
func ParseUnitMetadata(data []byte) (*ObjectMetadata, error) {
	return ParseObjectMetadata(data)
}
