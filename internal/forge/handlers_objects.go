package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/formats/wesstrings"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wts"
)

// MCP handlers for the Object Editor. Phase 1a scope: units only, read-only.
//
// Wire format choices:
//   - One handler per kind ("objects.units.*") rather than a single
//     polymorphic endpoint. Different object kinds have different metadata
//     surfaces (items don't have a race column; doodads use a single-letter
//     category; abilities have a level dimension) — a unified endpoint
//     would either lose those distinctions or force the wire format to
//     carry every union field. Separate handlers are clearer.
//   - List endpoint returns flat rows + per-row category/race tags. The
//     frontend builds the tree by grouping client-side — same shape HiveWE
//     uses for its tree models. Keeps the MCP wire small (no nested JSON)
//     and lets the UI re-pivot without a round-trip.

// Lazy WES-strings table — Reforged ships UI/WorldEditStrings.txt and
// UI/WorldEditGameStrings.txt; merged into a single in-memory table.
var (
	wesOnce sync.Once
	wesTab  *wesstrings.Table
)

func loadWES() *wesstrings.Table {
	wesOnce.Do(func() {
		t := wesstrings.New()
		for _, name := range []string{"UI/WorldEditStrings.txt", "UI/WorldEditGameStrings.txt"} {
			data, ok, err := readBaseAsset(name)
			if err != nil || !ok {
				log.Printf("handlers_objects: wes skip %s: ok=%v err=%v", name, ok, err)
				continue
			}
			if err := t.Load(data); err != nil {
				log.Printf("handlers_objects: wes parse %s: %v", name, err)
			}
		}
		wesTab = t
	})
	return wesTab
}

// resolveDisplay turns a raw SLK/INI cell value into something a UI can show.
// Dereferences TRIGSTR_<n> (map-local strings) and WESTRING_FOO (WC3 stock
// strings), strips WC3 color codes (|cAARRGGBB ... |r), and trims. A no-op
// on plain text. Identical contract to package main's resolveDisplay but
// keeps the forge package self-contained (no cross-package import needed).
func resolveDisplay(raw string, mapStrings wts.Strings) string {
	if raw == "" {
		return ""
	}
	v := raw
	if strings.HasPrefix(v, "TRIGSTR_") {
		v = mapStrings.Display(v)
	} else if strings.HasPrefix(v, "WESTRING_") {
		v = loadWES().Resolve(v)
	}
	return strings.TrimSpace(wts.StripColorCodes(v))
}

// titleRace turns the lowercase race tag in SLK ("human", "nightelf",
// "creeps") into a UI-friendly label. Duplicates package main's table —
// small and stable enough that the duplication beats a cross-package call.
func titleRace(r string) string {
	switch strings.ToLower(r) {
	case "human":
		return "Human"
	case "orc":
		return "Orc"
	case "undead":
		return "Undead"
	case "nightelf":
		return "Night Elf"
	case "naga":
		return "Naga"
	case "creeps":
		return "Creep"
	case "critters":
		return "Critter"
	case "commoner":
		return "Commoner"
	case "demon":
		return "Demon"
	case "other":
		return "Special"
	case "":
		return ""
	default:
		return strings.Title(r)
	}
}

// normalizeIconPath converts an SLK/INI icon-path cell into the form the
// asset HTTP handler expects: forward slashes, lowercased, with .blp
// appended when no recognized texture extension is declared. Duplicated
// from typeindex.go's same-named helper — same contract, kept local to
// avoid pulling package main into internal/forge.
func normalizeIconPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.ToLower(p)
	ext := strings.ToLower(path.Ext(p))
	if ext != ".blp" && ext != ".dds" && ext != ".tga" {
		p += ".blp"
	}
	return p
}

// unitModelPath resolves the per-unit MDX path from the merged row's
// `file` column. Reforged splits some entries into `file:hd` / `file:sd`
// per-mode variants the same way the icon path does (see unitIconArt);
// the same fallback chain applies. Returns (primary, fallbacks) where
// primary is the .mdx form and the fallbacks list carries the .mdl
// sibling — the asset handler's mdx↔mdl swap already covers most cases,
// but the explicit fallback gives mdx-m3-viewer a second attempt before
// giving up on the load.
//
// Custom maps frequently ship .mdl exports of imported models (e.g.
// "war3mapImported/foo.mdl") so the .mdl fallback is load-bearing for
// custom-content maps even when the asset handler's sibling swap fires.
func unitModelPath(fields map[string]string) (string, []string) {
	for _, k := range []string{"file", "file:hd", "file:sd"} {
		v := strings.TrimSpace(fields[k])
		if v == "" {
			continue
		}
		stem := v
		if i := strings.LastIndexByte(v, '.'); i > 0 {
			ext := strings.ToLower(v[i:])
			if ext == ".mdx" || ext == ".mdl" {
				stem = v[:i]
			}
		}
		return stem + ".mdx", []string{stem + ".mdl"}
	}
	return "", nil
}

// unitIconArt resolves the per-unit command-button icon path from the
// merged unit's `art` column. Reforged moved per-asset icon paths out of
// the base SLK and into unitSkin.txt's `Art=` line, which the base loader
// merges into the Mapped row alongside the SLK columns. Customs that
// override `art` get their own icon; customs that don't override it
// inherit the base's icon via the standard merge.
//
// Reforged unitSkin.txt also carries `art:hd=` / `art:sd=` per-mode
// variants for many units (e.g. Orkn Shadow Hunter has only the HD/SD
// pair, no plain `art`). Fall back to those when the unsuffixed key is
// missing — `art:sd` first because it's the canonical BLP that ships in
// every install, then `art:hd` (which is sometimes DDS-only and the
// asset handler's BLP↔DDS sibling swap recovers it transparently).
func unitIconArt(fields map[string]string) string {
	for _, k := range []string{"art", "art:sd", "art:hd"} {
		if v := strings.TrimSpace(fields[k]); v != "" {
			return normalizeIconPath(v)
		}
	}
	return ""
}

// classifyKind derives the editor-tree leaf bucket for a unit row: one of
// "hero", "building", "special", "unit". Mirrors HiveWE's UnitTreeModel::
// getFolderParent — order is special > building > hero > unit.
//
// Hero detection uses the WC3 FourCC convention: heroes have an uppercase
// first character (Hpal Paladin, Orkn Shadow Hunter, E000 custom hero),
// non-heroes start lowercase (hpea Peasant). `unitclass.contains("Hero")`
// looked tempting but breaks for neutral heroes (Orkn has no unitclass
// at all in stock data); the first-char rule is the actual data-model
// invariant Blizzard preserved across every WC3 unit row.
func classifyKind(id string, fields map[string]string) string {
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

// objectsUnitsListEntity is one row in the units tree. The frontend groups
// by Race, then Kind, to build HiveWE's three-level tree (Race → Kind →
// Unit). Sort order is name-asc within each Kind.
type objectsUnitsListEntity struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Race       string `json:"race"`        // raw lowercase tag ("human"); frontend titlecases via shared lookup
	RaceLabel  string `json:"race_label"`  // pre-titled label ("Human")
	Kind       string `json:"kind"`        // "unit" | "hero" | "building" | "special"
	Category   string `json:"category"`    // human-readable bucket combining race + kind
	IsCustom   bool   `json:"is_custom"`
	IsEdited   bool   `json:"is_edited"`
	BaseID     string `json:"base_id,omitempty"` // only set for customs
	Campaign   bool   `json:"campaign"`            // hides under "Campaign" subtree when true
	IconArt    string `json:"icon_art"`            // command-button icon path stem, e.g. "replaceabletextures/commandbuttons/btnshadowhunter.blp"
}

func handleObjectsUnitsList(_ json.RawMessage) (any, error) {
	merged, _, err := MergedUnits()
	if err != nil {
		return nil, fmt.Errorf("load units: %w", err)
	}
	mapStrings := Current.Strings()
	out := make([]objectsUnitsListEntity, 0, len(merged))
	for id, u := range merged {
		// Filter out rows that aren't actually units. UnitMetaData drives
		// the field-set; the SLK Mapped also has item rows mixed in (they
		// share the same base table family). HiveWE filters by isbldg/
		// isupper/special/unitclass + the absence of `class` (which is
		// item-only). For now, presence of `race` is the cheap split — every
		// unit/hero/building row has it, items don't.
		race := strings.ToLower(strings.TrimSpace(u.Fields["race"]))
		if race == "" {
			continue
		}
		name := resolveDisplay(u.Fields["name"], mapStrings)
		if name == "" {
			// Stock SLK rows without a resolved Name still belong in the
			// tree — fall back to the FourCC so the user can locate them.
			name = id
		}
		kind := classifyKind(u.ID, u.Fields)
		cat := titleRace(race)
		switch kind {
		case "hero":
			cat += " Hero"
		case "building":
			cat += " Building"
		case "special":
			cat += " Special"
		}
		out = append(out, objectsUnitsListEntity{
			ID:        id,
			Name:      name,
			Race:      race,
			RaceLabel: titleRace(race),
			Kind:      kind,
			Category:  cat,
			IsCustom:  u.IsCustom,
			IsEdited:  u.IsEdited,
			BaseID:    u.BaseID,
			Campaign:  u.Fields["campaign"] == "1",
			IconArt:   unitIconArt(u.Fields),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Race != out[j].Race {
			return out[i].Race < out[j].Race
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

type objectsUnitsGetParams struct {
	ID string `json:"id"`
}

type objectsUnitsField struct {
	ID          string `json:"id"`           // FourCC of the field
	Field       string `json:"field"`        // SLK column name (lowercased)
	DisplayName string `json:"display_name"` // resolved label
	Category    string `json:"category"`     // "stats" | "combat" | "text" | ...
	Type        string `json:"type"`         // "int" | "string" | "bool" | ...
	Value       string `json:"value"`        // raw cell value
	Display     string `json:"display"`      // value with TRIGSTR/WESTRING/color codes resolved
	Overridden  bool   `json:"overridden"`   // true if this came from the w3u shadow
}

type objectsUnitsGetResult struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	BaseID         string              `json:"base_id,omitempty"`
	IsCustom       bool                `json:"is_custom"`
	IsEdited       bool                `json:"is_edited"`
	Race           string              `json:"race"`
	Kind           string              `json:"kind"`
	IconArt        string              `json:"icon_art"`
	ModelPath      string              `json:"model_path"`      // primary .mdx path for the 3D preview
	ModelFallbacks []string            `json:"model_fallbacks"` // .mdl sibling + variants the previewer tries on load failure
	Fields         []objectsUnitsField `json:"fields"`
}

func handleObjectsUnitsGet(params json.RawMessage) (any, error) {
	var p objectsUnitsGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.ID == "" {
		return nil, errors.New("id is required")
	}
	merged, meta, err := MergedUnits()
	if err != nil {
		return nil, fmt.Errorf("load units: %w", err)
	}
	u, ok := merged[p.ID]
	if !ok {
		return nil, fmt.Errorf("no unit with id %q", p.ID)
	}

	mapStrings := Current.Strings()
	// Emit one entry per metadata-known field; fields without metadata
	// (e.g. helper columns the SLK carries but Blizzard doesn't expose)
	// are dropped — they have no display name or type and would just
	// clutter the editor. HiveWE behaves the same way.
	rows := make([]objectsUnitsField, 0, len(meta.Fields))
	for i := range meta.Fields {
		f := &meta.Fields[i]
		if !f.AppliesToUnits() {
			continue
		}
		col := strings.ToLower(f.Field)
		val, has := u.Fields[col]
		if !has {
			// Field doesn't exist on this unit (most don't apply per-unit;
			// HiveWE shows them as blank). Emit blank so the UI knows the
			// field is editable on this row.
			val = ""
		}
		rows = append(rows, objectsUnitsField{
			ID:          f.ID,
			Field:       col,
			DisplayName: resolveDisplay(f.DisplayName, mapStrings),
			Category:    f.Category,
			Type:        f.Type,
			Value:       val,
			Display:     resolveDisplay(val, mapStrings),
			Overridden:  u.Overridden[col],
		})
	}
	// Stable ordering inside each category (matching HiveWE's Index sort).
	// Index -1 → end of its category. Categories themselves come out in
	// insertion order; the frontend can re-bucket as it pleases.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Category != rows[j].Category {
			return rows[i].Category < rows[j].Category
		}
		// Within a category, preserve the metadata's Index ordering.
		fi := meta.ByID[rows[i].ID]
		fj := meta.ByID[rows[j].ID]
		// Treat -1 (unindexed) as +inf — sinks to bottom of its category.
		ii, jj := fi.Index, fj.Index
		if ii < 0 {
			ii = 1 << 30
		}
		if jj < 0 {
			jj = 1 << 30
		}
		if ii != jj {
			return ii < jj
		}
		return rows[i].DisplayName < rows[j].DisplayName
	})

	modelPath, modelFallbacks := unitModelPath(u.Fields)
	return &objectsUnitsGetResult{
		ID:             u.ID,
		Name:           resolveDisplay(u.Fields["name"], mapStrings),
		BaseID:         u.BaseID,
		IsCustom:       u.IsCustom,
		IsEdited:       u.IsEdited,
		Race:           strings.ToLower(u.Fields["race"]),
		Kind:           classifyKind(u.ID, u.Fields),
		IconArt:        unitIconArt(u.Fields),
		ModelPath:      modelPath,
		ModelFallbacks: modelFallbacks,
		Fields:         rows,
	}, nil
}

type objectsUnitsFieldsMetaItem struct {
	ID          string `json:"id"`
	Field       string `json:"field"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"`
	Type        string `json:"type"`
	MinVal      string `json:"min_val,omitempty"`
	MaxVal      string `json:"max_val,omitempty"`
}

// handleObjectsUnitsFieldsMeta returns the field schema so the frontend
// can render labels + type-aware widgets without round-tripping the
// metadata on every unit selection. Static for the process lifetime.
func handleObjectsUnitsFieldsMeta(_ json.RawMessage) (any, error) {
	_, meta, err := loadUnitsBase()
	if err != nil {
		return nil, fmt.Errorf("load metadata: %w", err)
	}
	mapStrings := Current.Strings()
	out := make([]objectsUnitsFieldsMetaItem, 0, len(meta.Fields))
	for i := range meta.Fields {
		f := &meta.Fields[i]
		if !f.AppliesToUnits() {
			continue
		}
		out = append(out, objectsUnitsFieldsMetaItem{
			ID:          f.ID,
			Field:       strings.ToLower(f.Field),
			DisplayName: resolveDisplay(f.DisplayName, mapStrings),
			Category:    f.Category,
			Type:        f.Type,
			MinVal:      f.MinVal,
			MaxVal:      f.MaxVal,
		})
	}
	return out, nil
}
