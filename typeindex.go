package main

import (
	"log"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/forge"
	"github.com/StephenSHorton/wc3-forge/internal/formats/slk"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
)

// TypeIndex resolves a FourCC type ID ("Hpal", "ATtr", …) to the data the
// renderer needs: MDX path, vertex tint, model scale, variation count, etc.
// Backed by the WC3 base SLK files (UnitData, unitUI, ItemData, Doodads).
//
// One unit/doodad-shaped lookup per Wails session. Built lazily on first use
// so cold start stays cheap (~100ms saved when running headless). Cached for
// the process lifetime — base SLKs don't change between map opens.

// UnitTypeInfo carries everything the JS scene needs to instantiate a unit
// or item MDX. Fields mirror the SLK column names from War3MapViewer's
// handlers/w3x/unit.js so the JS side can mostly forget about SLK semantics.
type UnitTypeInfo struct {
	File       string  `json:"file"`        // MDX path stem (no .mdx suffix); empty if absent
	ModelScale float64 `json:"model_scale"` // baseline scale multiplier; default 1
	MoveHeight float64 `json:"move_height"` // Z offset above terrain
	Red        int     `json:"red"`         // 0..255 vertex tint
	Green      int     `json:"green"`       // 0..255 vertex tint
	Blue       int     `json:"blue"`        // 0..255 vertex tint
}

// DoodadTypeInfo carries everything the JS scene needs to instantiate a
// doodad or destructible MDX. NumVar selects which fileN.mdx variant.
type DoodadTypeInfo struct {
	File       string  `json:"file"`        // MDX path stem (no .mdx suffix)
	NumVar     int     `json:"num_var"`     // variation count; 1 means no suffix
	FixedRot   float64 `json:"fixed_rot"`   // terrain-doodad rotation override (degrees)
	ModelScale float64 `json:"model_scale"` // baseline scale; default 1
}

var (
	indexOnce sync.Once
	indexErr  error
	unitIndex map[string]UnitTypeInfo
	doodIndex map[string]DoodadTypeInfo
)

func loadSLKs(m *slk.Mapped, names []string) {
	for _, name := range names {
		data, ok, err := readBaseAsset(name)
		if err != nil || !ok {
			log.Printf("typeindex: skip %s: ok=%v err=%v", name, ok, err)
			continue
		}
		if err := m.Load(data); err != nil {
			log.Printf("typeindex: parse %s: %v", name, err)
		}
	}
}

func loadINIs(m *slk.Mapped, names []string) {
	for _, name := range names {
		data, ok, err := readBaseAsset(name)
		if err != nil || !ok {
			log.Printf("typeindex: skip %s: ok=%v err=%v", name, ok, err)
			continue
		}
		if err := m.LoadINI(data); err != nil {
			log.Printf("typeindex: parse %s: %v", name, err)
		}
	}
}

// readBaseAsset is the same map-first-then-CASC lookup the assetHandler uses,
// minus the HTTP plumbing. We need it here so SLK lookups work regardless of
// whether a map is currently loaded.
func readBaseAsset(name string) ([]byte, bool, error) {
	clean := strings.ToLower(path.Clean(strings.ReplaceAll(name, "\\", "/")))

	if data, ok, err := forge.Current.ReadFile(clean); err != nil {
		return nil, false, err
	} else if ok {
		return data, true, nil
	}
	c, err := getCASC()
	if err != nil || c == nil {
		return nil, false, err
	}
	return c.ReadFile(clean)
}

// buildTypeIndex parses every base SLK we care about. Failures on individual
// files log a warning but don't abort — partial coverage beats no coverage,
// and the asset handler will 404 missing models cleanly.
//
// **The skin .txt files are NOT optional.** Reforged CASC stripped per-asset
// paths out of the base SLKs (Doodads.slk, UnitData.slk, …) and moved them
// into companion `*Skin.txt` files. Without merging these, every doodad/
// unit lookup returns a row with no `file` column → nothing renders. The
// lib only loads them when isReforged=true; we have to load them regardless,
// because our CASC source IS a Reforged install.
func buildTypeIndex() {
	units := slk.New()
	loadSLKs(units, []string{
		"Units/UnitData.slk",
		"Units/unitUI.slk",
		"Units/ItemData.slk",
	})
	loadINIs(units, []string{
		"Units/unitSkin.txt",
		"Units/itemSkin.txt",
	})

	doodads := slk.New()
	loadSLKs(doodads, []string{
		"Doodads/Doodads.slk",
		"Units/DestructableData.slk",
	})
	loadINIs(doodads, []string{
		"Doodads/doodadSkins.txt",
		"Units/destructableSkin.txt",
	})

	unitIndex = make(map[string]UnitTypeInfo, len(units.Rows))
	for id, row := range units.Rows {
		info := UnitTypeInfo{
			File:       row.String("file"),
			ModelScale: row.Number("modelScale"),
			MoveHeight: row.Number("moveHeight"),
			Red:        int(row.Number("red")),
			Green:      int(row.Number("green")),
			Blue:       int(row.Number("blue")),
		}
		// SLKs default to scale 1 when the column is absent; Number returns
		// 0 in that case which would render the model invisibly small.
		if info.ModelScale == 0 {
			info.ModelScale = 1
		}
		unitIndex[id] = info
	}

	doodIndex = make(map[string]DoodadTypeInfo, len(doodads.Rows))
	for id, row := range doodads.Rows {
		info := DoodadTypeInfo{
			File:     row.String("file"),
			NumVar:   int(row.Number("numVar")),
			FixedRot: row.Number("fixedRot"),
			// Doodads use defScale; modelScale is the unit-side name. The
			// Reforged skin .txt also carries defScale:hd for HD-only sizing
			// which we ignore (SD-only rendering for now).
			ModelScale: firstNonZero(row.Number("defScale"), row.Number("modelScale")),
		}
		if info.NumVar <= 0 {
			info.NumVar = 1
		}
		if info.ModelScale == 0 {
			info.ModelScale = 1
		}
		doodIndex[id] = info
	}

	log.Printf("typeindex: %d units/items, %d doodads/destructibles indexed",
		len(unitIndex), len(doodIndex))
}

func ensureTypeIndex() error {
	indexOnce.Do(buildTypeIndex)
	return indexErr
}

func firstNonZero(vs ...float64) float64 {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

// --- Per-map overlay (custom + modified types) ---
//
// war3map.w3{d,b,u,t} carry the per-map object-modification tables. They
// hold two kinds of edits:
//   - "Original" edits: column overrides applied to a stock type (e.g.,
//     "ATtr" with maxScale changed to 1.5 for this map).
//   - "Custom" derived types: new FourCCs ("D006") that inherit a base
//     type's data, then apply their own overrides.
//
// We merge these on top of the cached stock indices when callers ask for
// the index. Stock cache stays immutable; the merge is per-call. At the
// scale we operate on (a few hundred customs at most) this is fine.
//
// Field-FourCC ↔ column-name mapping comes from {Doodad,Destructable,…}
// MetaData.slk; for the rendering-critical handful we hardcode it here so
// we don't have to ship another lazy SLK loader. Adding a column to the
// hardcoded set is a one-line change.

// doodadFieldMap maps the field-FourCCs used in war3map.w3d (and w3b for
// destructibles where they overlap) to their SLK column names. Add fields
// here as the renderer grows to consume more.
var doodadFieldMap = map[string]string{
	// Doodads (DoodadMetaData.slk)
	"dfil": "file",
	"dvar": "numVar",
	"ddes": "defScale",
	"dmis": "minScale",
	"dmas": "maxScale",
	"dfxr": "fixedRot",
	"dvr1": "red",
	"dvg1": "green",
	"dvb1": "blue",
	// Destructibles (DestructableMetaData.slk)
	"bfil": "file",
	"bvar": "numVar",
	"bmis": "minScale",
	"bmas": "maxScale",
	"bfxr": "fixedRot",
}

func resolveColumn(modID string, fields map[string]string) string {
	if c, ok := fields[modID]; ok {
		return c
	}
	return modID
}

func applyDoodadOverrides(base DoodadTypeInfo, ov w3objmod.Overrides) DoodadTypeInfo {
	out := base
	for rawCol, val := range ov {
		col := resolveColumn(rawCol, doodadFieldMap)
		switch strings.ToLower(col) {
		case "file":
			out.File = val
		case "numvar":
			out.NumVar = parseInt(val, base.NumVar)
		case "defscale", "modelscale":
			out.ModelScale = parseFloat(val, base.ModelScale)
		case "fixedrot":
			out.FixedRot = parseFloat(val, base.FixedRot)
		}
	}
	return out
}

func parseInt(s string, fallback int) int {
	// w3objmod serialises float modifications as "1.5" even when the SLK
	// column is conceptually an int; ParseFloat-then-truncate covers both.
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return int(v)
	}
	return fallback
}

func parseFloat(s string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return fallback
}

// mergeDoodadIndex returns a copy of the stock doodad index with the per-map
// w3d (doodads) + w3b (destructibles) modifications applied.
//
//   - OriginalEdits modify the row for the stock FourCC in place.
//   - Customs add a new row keyed by the derived FourCC, starting from the
//     base type's data and applying the custom's overrides.
//
// If the base type is missing from the stock index, the custom skips
// (logged). That can happen for very old maps referencing pre-Reforged
// types that have been retired.
func mergeDoodadIndex() map[string]DoodadTypeInfo {
	out := make(map[string]DoodadTypeInfo, len(doodIndex))
	for k, v := range doodIndex {
		out[k] = v
	}
	for _, mods := range []*w3objmod.File{forge.Current.DoodadMods(), forge.Current.DestructibleMods()} {
		if mods == nil {
			continue
		}
		for _, edit := range mods.OriginalEdits {
			base, ok := out[edit.BaseID]
			if !ok {
				continue
			}
			out[edit.BaseID] = applyDoodadOverrides(base, edit.Overrides)
		}
		for _, c := range mods.Customs {
			base, ok := out[c.BaseID]
			if !ok {
				log.Printf("typeindex: custom %s missing base %s; skipping", c.ID, c.BaseID)
				continue
			}
			out[c.ID] = applyDoodadOverrides(base, c.Overrides)
		}
	}
	return out
}

// GetUnitTypeIndex returns the full FourCC → UnitTypeInfo map. The frontend
// caches this on first call; subsequent calls hit the in-memory cache.
func (a *App) GetUnitTypeIndex() (map[string]UnitTypeInfo, error) {
	if err := ensureTypeIndex(); err != nil {
		return nil, err
	}
	// TODO(units): apply per-map w3u/w3t modifications when units render
	// path exists end-to-end (Phase 1F).
	return unitIndex, nil
}

// GetDoodadTypeIndex returns the full FourCC → DoodadTypeInfo map, with
// per-map w3d (doodads) + w3b (destructibles) modifications layered on
// top of the stock SLK. Must be re-fetched after each map open since the
// overlay changes — JS-side caches this per-map, not per-process.
func (a *App) GetDoodadTypeIndex() (map[string]DoodadTypeInfo, error) {
	if err := ensureTypeIndex(); err != nil {
		return nil, err
	}
	return mergeDoodadIndex(), nil
}
