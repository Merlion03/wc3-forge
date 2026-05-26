package forge

import (
	"log"
	"strings"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/formats/slk"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
)

// The Object Editor reads the same stock SLK / INI families HiveWE merges
// to build its `units_slk` global. Three layers are stacked:
//
//	1. Base SLKs   — Units/UnitData.slk + UnitBalance + UnitWeapons +
//	                 UnitAbilities + unitUI. The shared schema is keyed by
//	                 the 4-char unit FourCC ("hpea", "Hpal", …).
//	2. INI overlays — per-race UnitStrings/UnitFunc + unitSkin.txt. Reforged
//	                 moved display names + icon paths out of the SLKs into
//	                 these companion files; without them every unit's `name`
//	                 column is empty.
//	3. Metadata    — Units/UnitMetaData.slk. Describes each field's display
//	                 name, type, category, applicability flags, FourCC ↔
//	                 column-name binding. Drives the editor's column-name
//	                 vocabulary AND the FourCC translation for w3u shadows.
//
// Cache lifecycle: base + meta are CASC-derived (process-global, don't
// depend on the loaded map) and live in package-level sync.Once-guarded
// globals. The merged view is per-Session because the w3u shadow is per-map.

var (
	unitsBaseOnce sync.Once
	unitsBase     *slk.Mapped
	unitsMeta     *UnitMetadata
	unitsBaseErr  error
)

// SLK / INI files that contribute to the units base. Order matters for
// columns that exist in multiple sources — later loads overwrite earlier
// ones on collision. HiveWE merges in the same order; we mirror it so
// cross-checks line up byte-for-byte.
var (
	unitBaseSLKs = []string{
		"Units/UnitData.slk",
		"Units/UnitBalance.slk",
		"Units/UnitWeapons.slk",
		"Units/UnitAbilities.slk",
		"Units/unitUI.slk",
	}
	unitBaseINIs = []string{
		// Reforged splits per-asset paths + display names into companion
		// .txt files. Without these, the `name` and `file` columns are
		// blank for every stock unit.
		"Units/unitSkin.txt",
		"Units/HumanUnitFunc.txt",
		"Units/HumanUnitStrings.txt",
		"Units/OrcUnitFunc.txt",
		"Units/OrcUnitStrings.txt",
		"Units/UndeadUnitFunc.txt",
		"Units/UndeadUnitStrings.txt",
		"Units/NightElfUnitFunc.txt",
		"Units/NightElfUnitStrings.txt",
		"Units/NeutralUnitFunc.txt",
		"Units/NeutralUnitStrings.txt",
		"Units/CampaignUnitFunc.txt",
		"Units/CampaignUnitStrings.txt",
	}
)

// loadUnitsBase performs the lazy one-time load of stock units data. Safe
// to call concurrently; subsequent calls are cheap (sync.Once + read-only
// access to the cached tables).
//
// Missing files log a warning but don't abort — a partial mount still
// produces a usable (if incomplete) editor. The renderer side of the
// codebase tolerates the same incomplete-base condition, so the editor
// matches that bar.
func loadUnitsBase() (*slk.Mapped, *UnitMetadata, error) {
	unitsBaseOnce.Do(func() {
		base := slk.New()
		for _, name := range unitBaseSLKs {
			data, ok, err := readBaseAsset(name)
			if err != nil || !ok {
				log.Printf("objectunits: skip SLK %s: ok=%v err=%v", name, ok, err)
				continue
			}
			if err := base.Load(data); err != nil {
				log.Printf("objectunits: parse SLK %s: %v", name, err)
			}
		}
		for _, name := range unitBaseINIs {
			data, ok, err := readBaseAsset(name)
			if err != nil || !ok {
				log.Printf("objectunits: skip INI %s: ok=%v err=%v", name, ok, err)
				continue
			}
			if err := base.LoadINI(data); err != nil {
				log.Printf("objectunits: parse INI %s: %v", name, err)
			}
		}
		unitsBase = base

		metaData, ok, err := readBaseAsset("Units/UnitMetaData.slk")
		if err != nil || !ok {
			log.Printf("objectunits: UnitMetaData.slk missing: ok=%v err=%v", ok, err)
			// Empty metadata still allows the editor to function in a
			// reduced mode (no field display names, w3u shadows won't merge
			// into column-name space). Keep going.
			unitsMeta = &UnitMetadata{ByID: map[string]*UnitFieldMeta{}}
			return
		}
		meta, err := ParseUnitMetadata(metaData)
		if err != nil {
			log.Printf("objectunits: parse UnitMetaData: %v", err)
			unitsBaseErr = err
			unitsMeta = &UnitMetadata{ByID: map[string]*UnitFieldMeta{}}
			return
		}
		unitsMeta = meta
		log.Printf("objectunits: base loaded — %d unit rows, %d field metas",
			len(unitsBase.Rows), len(unitsMeta.Fields))
	})
	return unitsBase, unitsMeta, unitsBaseErr
}

// MergedUnit is one unit type after stacking base SLK + INI overlays + the
// loaded map's w3u shadow. `Fields` is the merged column-name → value map
// (lowercased column keys, matching slk.Mapped). `Overridden` is the set
// of column names whose value came from a map-level shadow (vs. base);
// the UI uses this to render modified-fields in a different color.
//
// `BaseID` is set for custom-derived units (war3map.w3u custom table) —
// the FourCC of the stock unit the custom inherits from. Empty for stock
// units. `IsCustom` is the orthogonal "this id doesn't exist in the
// stock base" flag; we don't conflate it with "has any overrides" because
// HiveWE distinguishes the two visually (custom = green, edited = violet).
type MergedUnit struct {
	ID         string
	BaseID     string
	IsCustom   bool
	IsEdited   bool // true if any field comes from the w3u shadow
	Fields     map[string]string
	Overridden map[string]bool
}

// MergedUnits returns the merged base+shadow view for every unit known to
// the editor — stock units plus map-level custom units. The returned map
// is freshly built; callers can mutate it without affecting the cache.
//
// Phase 1a contract: read-only. No save-back path yet.
func MergedUnits() (map[string]*MergedUnit, *UnitMetadata, error) {
	base, meta, err := loadUnitsBase()
	if err != nil {
		return nil, meta, err
	}
	out := make(map[string]*MergedUnit, len(base.Rows))

	// Layer 1: every stock unit from the base.
	for id, row := range base.Rows {
		fields := make(map[string]string, len(row))
		for col, val := range row {
			fields[col] = val
		}
		out[id] = &MergedUnit{
			ID:         id,
			Fields:     fields,
			Overridden: map[string]bool{},
		}
	}

	// Layer 2: per-map shadow from war3map.w3u.
	Current.mu.RLock()
	mods := Current.unitMods
	Current.mu.RUnlock()
	if mods != nil {
		// Original-edits: changes to stock units. The override map currently
		// holds FourCC-keyed entries (parsed with nil FieldMap); translate
		// each FourCC to its SLK column name via metadata.
		for _, edit := range mods.OriginalEdits {
			u, ok := out[edit.BaseID]
			if !ok {
				// Original-table entry pointing at a base ID we don't know.
				// Can happen if the map references a stock unit our CASC
				// mount doesn't expose — fabricate a stub so the editor can
				// at least show the override.
				u = &MergedUnit{
					ID:         edit.BaseID,
					Fields:     map[string]string{},
					Overridden: map[string]bool{},
				}
				out[edit.BaseID] = u
			}
			applyOverrides(u, edit.Overrides, meta)
		}
		// Customs: new derived units that DON'T exist in the base. The
		// override map covers only explicitly-set fields; the rest inherit
		// from BaseID at lookup time.
		for _, c := range mods.Customs {
			u := &MergedUnit{
				ID:         c.ID,
				BaseID:     c.BaseID,
				IsCustom:   true,
				Fields:     map[string]string{},
				Overridden: map[string]bool{},
			}
			// Seed inherited fields from BaseID before applying the custom's
			// own overrides. Without this seed, every custom would appear
			// empty in the editor's field table.
			if baseRow := base.Rows[c.BaseID]; baseRow != nil {
				for col, val := range baseRow {
					u.Fields[col] = val
				}
			}
			applyOverrides(u, c.Overrides, meta)
			out[c.ID] = u
		}
	}
	return out, meta, nil
}

// applyOverrides walks the FourCC-keyed Overrides map from w3objmod and
// writes the values into the merged unit's column-name-keyed Fields map.
// Each FourCC is resolved through the metadata to find its SLK column
// name; if the FourCC isn't in the metadata, the override is dropped with
// a debug log (rather than silently inserting an opaque 4-char key).
func applyOverrides(u *MergedUnit, ov w3objmod.Overrides, meta *UnitMetadata) {
	if len(ov) == 0 {
		return
	}
	u.IsEdited = true
	for fourCC, val := range ov {
		// w3objmod with nil FieldMap stores the FourCC directly as the map
		// key. If a real FieldMap was used at parse time, the keys are
		// already column-name space and the metadata lookup is a no-op fall-
		// through. Handle both shapes.
		col := fourCC
		if m, ok := meta.ByID[fourCC]; ok {
			col = strings.ToLower(m.Field)
		} else if !looksLikeFourCC(fourCC) {
			// Already a column-name key (FieldMap was used at parse time).
			col = strings.ToLower(fourCC)
		}
		u.Fields[col] = val
		u.Overridden[col] = true
	}
}

// resetUnitsBaseCacheForTest clears the lazy-load globals so a test can
// re-run loadUnitsBase under different conditions (different asset reader,
// missing CASC, etc.). Production code never calls this — sync.Once is the
// intended one-shot, and after wc3-forge is up the base doesn't change.
func resetUnitsBaseCacheForTest() {
	unitsBaseOnce = sync.Once{}
	unitsBase = nil
	unitsMeta = nil
	unitsBaseErr = nil
}

// looksLikeFourCC reports whether s is exactly 4 bytes of ASCII — a
// best-effort heuristic to distinguish raw modification-IDs from already-
// translated column names. Real SLK columns longer than 4 chars are
// trivially distinguishable; the ambiguous case is a 4-letter column name
// like "name", which is fine to look up either way (the FourCC "name"
// isn't a registered field id so the meta lookup will miss and we fall
// through to the lowercase identity).
func looksLikeFourCC(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		c := s[i]
		if c < 0x21 || c > 0x7E {
			return false
		}
	}
	return true
}
