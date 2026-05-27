package forge

import (
	"log"
	"strings"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/formats/slk"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
)

// Object Editor data layer — kind-agnostic generic that hosts every object
// kind (units, items, abilities, buffs, destructables, doodads, upgrades)
// under one shared code path.
//
// Phase 2a ships the refactor with units-only as the lone registered kind;
// Phase 2b adds the other six by stamping out additional KindConfig instances
// and pointing them at the right SLK/INI families + per-kind metadata file.
//
// The original Phase-1b "units only" globals (unitsBaseOnce / unitsBase /
// unitsMeta) are replaced with a per-kind cache keyed by KindConfig.Kind.
// Each kind has its own sync.Once + cache slot, so loading items doesn't
// block units and a failing kind doesn't poison the others.

// KindConfig captures everything that varies per object kind. One instance
// per kind lives at package level (UnitsConfig, ItemsConfig in 2b, ...).
// Handlers + the Session mutators take a *KindConfig and dispatch through it.
//
// Closure-based session accessors (GetMods, SetMods, GetDirty, SetDirty)
// avoid the awkward "pointer-to-pointer struct field" pattern: the per-kind
// shadow lives in a Session field directly (s.unitMods, s.itemMods, ...);
// the closure plumbs the read/write so generic code doesn't need to know
// which field. Captures the *Session at call time (not config time) so the
// same config can serve any Session — important for tests that build their
// own Session rather than touching the global Current.
type KindConfig struct {
	// Kind is the wire-namespace tag: "units", "items", "abilities", "buffs",
	// "destructables", "doodads", "upgrades". MCP routes are
	// "objects." + Kind + ".(list|get|...)" — keep it lowercase + URL-safe.
	Kind string

	// MetaDataFile is the per-kind metadata SLK ("Units/UnitMetaData.slk").
	// Drives field display names, types, applicability flags, FourCC bindings.
	MetaDataFile string

	// BaseSLKs is the ordered list of SLK tables that contribute to the base
	// row set. Later loads overwrite earlier ones on collision (HiveWE merge
	// order). For units: UnitData → UnitBalance → UnitWeapons → UnitAbilities → unitUI.
	BaseSLKs []string

	// BaseINIs is the per-race / per-skin INI overlays. Reforged moved display
	// names and icon paths out of SLKs into these companion .txt files; without
	// them the `name`/`art` columns are empty.
	BaseINIs []string

	// ShadowFile is the per-map war3map.w3* file ("war3map.w3u" for units).
	// Open reads it; Save writes it.
	ShadowFile string

	// ShadowOpt is the `opt` flag for w3objmod.Parse/Encode — true for w3d and
	// w3a (variation/level/dataPointer fields per modification), false for the
	// rest (w3b, w3u, w3t).
	ShadowOpt bool

	// AppliesFn filters metadata rows so a field shared across kinds (the
	// units+items metadata table for instance) only surfaces in the editor
	// that should host it.
	AppliesFn func(UnitFieldMeta) bool

	// ClassifyFn returns the leaf tree-bucket for a row (e.g. "hero" |
	// "building" | "special" | "unit" for units). Wire-stable strings —
	// the JS Object Editor switches on them.
	ClassifyFn func(id string, fields map[string]string) string

	// FilterListRow returns true if a row should appear in the list response.
	// Units use "row has a `race` column" (skips item rows that share the
	// metadata family). When nil, every row from the merged base ships.
	FilterListRow func(id string, fields map[string]string) bool

	// CategoryFn composes the per-row "race + kind" label that appears in the
	// list response. For units it's `titleRace(race) + " " + kindSuffix`; for
	// items it'd be the item-class label. Returns the display string.
	CategoryFn func(race, kind string) string

	// GetMods returns the *w3objmod.File for this kind on the given session.
	// Returns nil when the session has no shadow for this kind (fresh map,
	// no edits yet). Caller MUST hold s.mu (read or write).
	GetMods func(s *Session) *w3objmod.File

	// SetMods replaces the shadow pointer on the session. Used by the
	// ensure-allocated helper and by tests that stub a fresh File in place.
	// Caller MUST hold s.mu (write lock).
	SetMods func(s *Session, f *w3objmod.File)

	// GetDirty returns the per-kind dirty flag value on the session.
	// Caller MUST hold s.mu (read or write).
	GetDirty func(s *Session) bool

	// SetDirty sets the per-kind dirty flag. Caller MUST hold s.mu (write).
	SetDirty func(s *Session, v bool)
}

// kindConfigs is the lookup map used by undo/redo commands to dispatch back
// into the right config from a stored Kind string. Populated by package init
// calling registerKindConfig for every kind we ship.
//
// Read-only after init — no mutex needed. Tests that want to stub a kind
// can use the test-only override by calling registerKindConfig from a Test*
// function before the test body runs (each test ends with the same call
// restoring the production config).
var kindConfigs = map[string]*KindConfig{}

// registerKindConfig wires a KindConfig into the lookup table. Called once
// per kind during package init (see UnitsConfig below). Idempotent — calling
// twice with the same Kind overwrites the prior entry (test-friendly).
func registerKindConfig(cfg *KindConfig) {
	kindConfigs[cfg.Kind] = cfg
}

// kindConfigFor returns the KindConfig with the given Kind, or panics if
// none is registered. Internal use only — undo/redo commands resolve their
// stored Kind string this way, and a missing entry means somebody minted a
// command for an unregistered kind (a programmer error, not a runtime
// condition that should silently no-op).
func kindConfigFor(kind string) *KindConfig {
	cfg, ok := kindConfigs[kind]
	if !ok {
		panic("forge: no KindConfig registered for kind " + kind)
	}
	return cfg
}

// objectBaseCache is the per-kind lazy-load cache. One slot per kind, each
// guarded by its own sync.Once so kinds load independently. Indexed by
// KindConfig.Kind.
type objectBaseCache struct {
	once sync.Once
	base *slk.Mapped
	meta *UnitMetadata
	err  error
}

var (
	objectBaseMu  sync.Mutex
	objectBaseMap = map[string]*objectBaseCache{}
)

// getObjectBaseCache returns (creating if needed) the per-kind cache slot.
// The cache instance survives across calls so the sync.Once stays load-bearing.
func getObjectBaseCache(kind string) *objectBaseCache {
	objectBaseMu.Lock()
	defer objectBaseMu.Unlock()
	if c, ok := objectBaseMap[kind]; ok {
		return c
	}
	c := &objectBaseCache{}
	objectBaseMap[kind] = c
	return c
}

// loadObjectBase performs the lazy one-time load of stock object data for
// the given kind. Replaces Phase-1b's loadUnitsBase — same shape, but the
// SLK/INI/metadata file list comes from cfg, and the cache is per-kind.
//
// Missing files log a warning but don't abort; a partial mount still yields
// a usable (if incomplete) editor. UnitMetaData missing collapses to an
// empty metadata table so the editor renders an empty field list rather
// than crashing.
func loadObjectBase(cfg *KindConfig) (*slk.Mapped, *UnitMetadata, error) {
	cache := getObjectBaseCache(cfg.Kind)
	cache.once.Do(func() {
		base := slk.New()
		for _, name := range cfg.BaseSLKs {
			data, ok, err := readBaseAsset(name)
			if err != nil || !ok {
				log.Printf("objects[%s]: skip SLK %s: ok=%v err=%v", cfg.Kind, name, ok, err)
				continue
			}
			if err := base.Load(data); err != nil {
				log.Printf("objects[%s]: parse SLK %s: %v", cfg.Kind, name, err)
			}
		}
		for _, name := range cfg.BaseINIs {
			data, ok, err := readBaseAsset(name)
			if err != nil || !ok {
				log.Printf("objects[%s]: skip INI %s: ok=%v err=%v", cfg.Kind, name, ok, err)
				continue
			}
			if err := base.LoadINI(data); err != nil {
				log.Printf("objects[%s]: parse INI %s: %v", cfg.Kind, name, err)
			}
		}
		cache.base = base

		metaData, ok, err := readBaseAsset(cfg.MetaDataFile)
		if err != nil || !ok {
			log.Printf("objects[%s]: %s missing: ok=%v err=%v", cfg.Kind, cfg.MetaDataFile, ok, err)
			cache.meta = &UnitMetadata{ByID: map[string]*UnitFieldMeta{}}
			return
		}
		meta, err := ParseUnitMetadata(metaData)
		if err != nil {
			log.Printf("objects[%s]: parse %s: %v", cfg.Kind, cfg.MetaDataFile, err)
			cache.err = err
			cache.meta = &UnitMetadata{ByID: map[string]*UnitFieldMeta{}}
			return
		}
		cache.meta = meta
		log.Printf("objects[%s]: base loaded — %d rows, %d field metas",
			cfg.Kind, len(cache.base.Rows), len(cache.meta.Fields))
	})
	return cache.base, cache.meta, cache.err
}

// resetObjectBaseCacheForTest clears the lazy-load globals for one kind so
// a test can re-run loadObjectBase under different conditions (different
// asset reader, missing CASC, etc.). Production code never calls this.
//
// Pass "" to clear ALL kinds — useful for tests that touch the global state
// without binding to a specific kind.
func resetObjectBaseCacheForTest(kind string) {
	objectBaseMu.Lock()
	defer objectBaseMu.Unlock()
	if kind == "" {
		objectBaseMap = map[string]*objectBaseCache{}
		return
	}
	delete(objectBaseMap, kind)
}

// setObjectBaseForTest installs a pre-built base + metadata into the cache
// for the given kind, bypassing the sync.Once-guarded loader. Test-only
// helper used by stubUnitsBase + friends in objects_mods_test.go so tests
// don't have to drive the real CASC mount.
func setObjectBaseForTest(kind string, base *slk.Mapped, meta *UnitMetadata) {
	cache := getObjectBaseCache(kind)
	// Consume the sync.Once with a no-op load so the public loadObjectBase
	// path returns our stub on every subsequent call (and so subsequent test
	// resets land in a known state).
	cache.once.Do(func() {})
	cache.base = base
	cache.meta = meta
	cache.err = nil
}

// MergedObject is one object after stacking base SLK + INI overlays + the
// loaded map's per-kind shadow. Kind-agnostic; the same struct serves units,
// items, doodads, etc. Phase 1b's MergedUnit type is now an alias of this
// so existing references compile unchanged.
type MergedObject struct {
	ID         string
	BaseID     string
	IsCustom   bool
	IsEdited   bool
	Fields     map[string]string
	Overridden map[string]bool
}

// MergedUnit is the legacy alias kept for backward compatibility with code
// that imports the type by name. Going forward, prefer MergedObject.
type MergedUnit = MergedObject

// MergedObjects returns the merged base+shadow view for every object of
// this kind. Generic over kind — pass UnitsConfig() to recover the old
// MergedUnits behavior bit-for-bit.
//
// The returned map is freshly built; callers can mutate it without affecting
// the cache.
func MergedObjects(cfg *KindConfig) (map[string]*MergedObject, *UnitMetadata, error) {
	base, meta, err := loadObjectBase(cfg)
	if err != nil {
		return nil, meta, err
	}
	out := make(map[string]*MergedObject, len(base.Rows))

	// Layer 1: every stock row from the base.
	for id, row := range base.Rows {
		fields := make(map[string]string, len(row))
		for col, val := range row {
			fields[col] = val
		}
		out[id] = &MergedObject{
			ID:         id,
			Fields:     fields,
			Overridden: map[string]bool{},
		}
	}

	// Layer 2: per-map shadow from the kind's war3map.w3* file.
	Current.mu.RLock()
	mods := cfg.GetMods(Current)
	Current.mu.RUnlock()
	if mods != nil {
		for _, edit := range mods.OriginalEdits {
			u, ok := out[edit.BaseID]
			if !ok {
				// Original-table entry pointing at a base ID we don't know.
				// Fabricate a stub so the override is still visible.
				u = &MergedObject{
					ID:         edit.BaseID,
					Fields:     map[string]string{},
					Overridden: map[string]bool{},
				}
				out[edit.BaseID] = u
			}
			applyOverrides(u, edit.Overrides, meta)
		}
		for _, c := range mods.Customs {
			u := &MergedObject{
				ID:         c.ID,
				BaseID:     c.BaseID,
				IsCustom:   true,
				Fields:     map[string]string{},
				Overridden: map[string]bool{},
			}
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

// MergedUnits is the Phase-1b read entry point, preserved verbatim as a thin
// wrapper around the generic. Existing callers (the units-API + handlers)
// keep working.
func MergedUnits() (map[string]*MergedObject, *UnitMetadata, error) {
	return MergedObjects(UnitsConfig())
}

// applyOverrides walks the FourCC-keyed Overrides map from w3objmod and
// writes the values into the merged object's column-name-keyed Fields map.
// Identical to Phase 1b — kept on the generic since every kind merges the
// same way (FourCC → metadata column lookup → write into Fields).
func applyOverrides(u *MergedObject, ov w3objmod.Overrides, meta *UnitMetadata) {
	if len(ov) == 0 {
		return
	}
	u.IsEdited = true
	for fourCC, val := range ov {
		col := fourCC
		if m, ok := meta.ByID[fourCC]; ok {
			col = strings.ToLower(m.Field)
		} else if !looksLikeFourCC(fourCC) {
			col = strings.ToLower(fourCC)
		}
		u.Fields[col] = val
		u.Overridden[col] = true
	}
}

// looksLikeFourCC reports whether s is exactly 4 bytes of printable ASCII —
// a best-effort heuristic to distinguish raw modification-IDs from already-
// translated column names.
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

// ---------------------------------------------------------------------------
// Units kind — the Phase-1b behavior, expressed as a KindConfig.
// ---------------------------------------------------------------------------

var (
	unitsConfigOnce sync.Once
	unitsConfigCfg  *KindConfig
)

// UnitsConfig returns the KindConfig for units. Lazy-built singleton so the
// closures only allocate once. Phase 1b's hard-coded UnitData/unitSkin/etc.
// paths live inside this builder; the rest of the generic doesn't know
// they're units-specific.
func UnitsConfig() *KindConfig {
	unitsConfigOnce.Do(func() {
		unitsConfigCfg = &KindConfig{
			Kind:         "units",
			MetaDataFile: "Units/UnitMetaData.slk",
			BaseSLKs: []string{
				"Units/UnitData.slk",
				"Units/UnitBalance.slk",
				"Units/UnitWeapons.slk",
				"Units/UnitAbilities.slk",
				"Units/unitUI.slk",
			},
			BaseINIs: []string{
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
			},
			ShadowFile: "war3map.w3u",
			ShadowOpt:  false,
			AppliesFn: func(f UnitFieldMeta) bool {
				return f.AppliesToUnits()
			},
			ClassifyFn: classifyUnitKind,
			FilterListRow: func(id string, fields map[string]string) bool {
				// HiveWE's filter for the unit editor: row must have a `race`
				// column. Items share the metadata family but have no race,
				// so this cheaply separates them.
				return strings.TrimSpace(fields["race"]) != ""
			},
			CategoryFn: func(race, kind string) string {
				cat := titleRace(race)
				switch kind {
				case "hero":
					cat += " Hero"
				case "building":
					cat += " Building"
				case "special":
					cat += " Special"
				}
				return cat
			},
			GetMods:  func(s *Session) *w3objmod.File { return s.unitMods },
			SetMods:  func(s *Session, f *w3objmod.File) { s.unitMods = f },
			GetDirty: func(s *Session) bool { return s.dirtyUnitMods },
			SetDirty: func(s *Session, v bool) { s.dirtyUnitMods = v },
		}
		registerKindConfig(unitsConfigCfg)
	})
	return unitsConfigCfg
}

// classifyUnitKind mirrors HiveWE's UnitTreeModel::getFolderParent — order
// is special > building > hero > unit. Hero detection uses the WC3 FourCC
// convention: heroes have an uppercase first character (Hpal Paladin, Orkn
// Shadow Hunter, E000 custom hero), non-heroes start lowercase (hpea Peasant).
func classifyUnitKind(id string, fields map[string]string) string {
	if fields["special"] == "1" {
		return "special"
	}
	if fields["isbldg"] == "1" {
		return "building"
	}
	if id != "" && id[0] >= 'A' && id[0] <= 'Z' {
		return "hero"
	}
	return "unit"
}

// init prebuilds the units KindConfig + registers it so commands that
// reference Kind="units" can resolve without an explicit UnitsConfig() call.
func init() {
	_ = UnitsConfig()
}
