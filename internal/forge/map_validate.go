package forge

import (
	"fmt"
	"sort"
)

const terrainWorldStep = float32(128)

type MapDiagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Entity   string `json:"entity,omitempty"`
	Message  string `json:"message"`
}

type MapValidationResult struct {
	Valid       bool            `json:"valid"`
	Errors      int             `json:"errors"`
	Warnings    int             `json:"warnings"`
	Diagnostics []MapDiagnostic `json:"diagnostics"`
}

type mapWorldBounds struct {
	minX, minY, maxX, maxY float32
}

// ValidateMap runs conservative structural checks over the loaded in-memory
// map. It never mutates state and intentionally avoids heuristic game-design
// opinions: an error means a concrete serialization/identity invariant is
// broken; warnings flag suspicious but technically representable placement.
func (s *Session) ValidateMap() (MapValidationResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return MapValidationResult{}, fmt.Errorf("no map loaded")
	}

	diags := make([]MapDiagnostic, 0)
	add := func(severity, code, entity, message string) {
		diags = append(diags, MapDiagnostic{Severity: severity, Code: code, Entity: entity, Message: message})
	}

	var world *mapWorldBounds
	if s.terrain == nil {
		add("error", "terrain.missing", "terrain", "war3map.w3e is not loaded")
	} else {
		w, h := int(s.terrain.Width), int(s.terrain.Height)
		if w <= 0 || h <= 0 {
			add("error", "terrain.invalid_dimensions", "terrain", fmt.Sprintf("terrain dimensions are %dx%d", w, h))
		} else {
			b := mapWorldBounds{
				minX: s.terrain.CenterOffset[0], minY: s.terrain.CenterOffset[1],
				maxX: s.terrain.CenterOffset[0] + float32(w-1)*terrainWorldStep,
				maxY: s.terrain.CenterOffset[1] + float32(h-1)*terrainWorldStep,
			}
			world = &b
		}
		expected := w * h
		if expected >= 0 && len(s.terrain.Tiles) != expected {
			add("error", "terrain.tile_count_mismatch", "terrain", fmt.Sprintf("terrain has %d tilepoints, expected %d from %dx%d dimensions", len(s.terrain.Tiles), expected, w, h))
		}
		for i, id := range s.terrain.GroundTilesets {
			if len(id) != 4 {
				add("error", "terrain.invalid_fourcc", fmt.Sprintf("terrain.ground_palette:%d", i), fmt.Sprintf("ground palette id %q is not exactly 4 bytes", id))
			}
		}
		for i, id := range s.terrain.CliffTilesets {
			if len(id) != 4 {
				add("error", "terrain.invalid_fourcc", fmt.Sprintf("terrain.cliff_palette:%d", i), fmt.Sprintf("cliff palette id %q is not exactly 4 bytes", id))
			}
		}
		badGround, badCliff := 0, 0
		firstGround, firstCliff := -1, -1
		for i, tp := range s.terrain.Tiles {
			if int(tp.GroundTexture) >= len(s.terrain.GroundTilesets) {
				badGround++
				if firstGround < 0 {
					firstGround = i
				}
			}
			// CliffTexture 15 is the format's conventional "no cliff" sentinel.
			if tp.CliffTexture != 15 && int(tp.CliffTexture) >= len(s.terrain.CliffTilesets) {
				badCliff++
				if firstCliff < 0 {
					firstCliff = i
				}
			}
		}
		if badGround > 0 {
			add("error", "terrain.invalid_ground_texture_index", fmt.Sprintf("terrain.tile:%d", firstGround), fmt.Sprintf("%d tilepoints reference a ground palette index outside the palette", badGround))
		}
		if badCliff > 0 {
			add("error", "terrain.invalid_cliff_texture_index", fmt.Sprintf("terrain.tile:%d", firstCliff), fmt.Sprintf("%d tilepoints reference a cliff palette index outside the palette", badCliff))
		}
	}

	if s.units != nil {
		seenCN := map[uint32]int{}
		seenStart := map[uint32]int{}
		for i := range s.units.Entities {
			e := &s.units.Entities[i]
			seenCN[e.CreationNumber]++
			entity := fmt.Sprintf("unit:%d", e.CreationNumber)
			if e.TypeID == slocTypeID {
				seenStart[e.Player]++
				entity = fmt.Sprintf("start_location:%d", e.Player)
			} else if len(e.TypeID) != 4 {
				add("error", "object.invalid_fourcc", entity, fmt.Sprintf("placed unit type_id %q is not exactly 4 bytes", e.TypeID))
			}
			if world != nil && !pointInWorld(e.Position[0], e.Position[1], *world) {
				if e.TypeID == slocTypeID {
					add("warning", "start_location.outside_map", entity, fmt.Sprintf("start location is outside terrain bounds at (%.1f, %.1f)", e.Position[0], e.Position[1]))
				} else {
					add("warning", "entity.outside_map", entity, fmt.Sprintf("placed unit is outside terrain bounds at (%.1f, %.1f)", e.Position[0], e.Position[1]))
				}
			}
		}
		for cn, count := range seenCN {
			if count > 1 {
				add("error", "entity.duplicate_creation_number", fmt.Sprintf("unit:%d", cn), fmt.Sprintf("unit creation_number %d occurs %d times", cn, count))
			}
		}
		for idx, count := range seenStart {
			if count > 1 {
				add("error", "start_location.duplicate_index", fmt.Sprintf("start_location:%d", idx), fmt.Sprintf("start-location index %d occurs %d times", idx, count))
			}
		}
	}

	if s.doodads != nil {
		seen := map[uint32]int{}
		for i := range s.doodads.Doodads {
			d := &s.doodads.Doodads[i]
			seen[d.CreationNumber]++
			entity := fmt.Sprintf("doodad:%d", d.CreationNumber)
			if len(d.TypeID) != 4 {
				add("error", "object.invalid_fourcc", entity, fmt.Sprintf("placed doodad type_id %q is not exactly 4 bytes", d.TypeID))
			}
			if world != nil && !pointInWorld(d.Position[0], d.Position[1], *world) {
				add("warning", "entity.outside_map", entity, fmt.Sprintf("placed doodad is outside terrain bounds at (%.1f, %.1f)", d.Position[0], d.Position[1]))
			}
		}
		for cn, count := range seen {
			if count > 1 {
				add("error", "entity.duplicate_creation_number", fmt.Sprintf("doodad:%d", cn), fmt.Sprintf("doodad creation_number %d occurs %d times", cn, count))
			}
		}
	}

	if s.regions != nil {
		seen := map[int32]int{}
		for i := range s.regions.Regions {
			r := &s.regions.Regions[i]
			seen[r.CreationNumber]++
			entity := fmt.Sprintf("region:%d", r.CreationNumber)
			if r.Left > r.Right || r.Bottom > r.Top {
				add("error", "region.invalid_bounds", entity, fmt.Sprintf("region bounds are inverted: left=%.1f bottom=%.1f right=%.1f top=%.1f", r.Left, r.Bottom, r.Right, r.Top))
			}
			if len(r.WeatherID) != 0 && len(r.WeatherID) != 4 {
				add("error", "region.invalid_weather_fourcc", entity, fmt.Sprintf("weather_id %q is not empty or exactly 4 bytes", r.WeatherID))
			}
			if world != nil && rectOutsideWorld(r.Left, r.Bottom, r.Right, r.Top, *world) {
				add("warning", "entity.outside_map", entity, "region rectangle lies completely outside the terrain bounds")
			}
		}
		for cn, count := range seen {
			if count > 1 {
				add("error", "entity.duplicate_creation_number", fmt.Sprintf("region:%d", cn), fmt.Sprintf("region creation_number %d occurs %d times", cn, count))
			}
		}
	}

	// Object-definition tables: custom IDs are per-kind namespaces, so detect
	// duplicates only within one kind. A matching ID in war3mapSkin.w3* is NOT a
	// duplicate — it is the intentional Reforged art/skin companion row.
	for _, cfg := range kindConfigs {
		mods := cfg.GetMods(s)
		if mods == nil {
			continue
		}
		seen := map[string]int{}
		for i := range mods.Customs {
			obj := &mods.Customs[i]
			entity := fmt.Sprintf("object:%s:%s", cfg.Kind, obj.ID)
			seen[obj.ID]++
			if len(obj.ID) != 4 {
				add("error", "object.invalid_fourcc", entity, fmt.Sprintf("custom object id %q is not exactly 4 bytes", obj.ID))
			}
			if len(obj.BaseID) != 4 {
				add("error", "object.invalid_fourcc", entity, fmt.Sprintf("custom object base_id %q is not exactly 4 bytes", obj.BaseID))
			}
		}
		for id, count := range seen {
			if count > 1 {
				add("error", "object.duplicate_custom_id", fmt.Sprintf("object:%s:%s", cfg.Kind, id), fmt.Sprintf("custom object id %q occurs %d times in %s", id, count, cfg.Kind))
			}
		}
	}

	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Severity != diags[j].Severity {
			return diags[i].Severity == "error"
		}
		if diags[i].Code != diags[j].Code {
			return diags[i].Code < diags[j].Code
		}
		if diags[i].Entity != diags[j].Entity {
			return diags[i].Entity < diags[j].Entity
		}
		return diags[i].Message < diags[j].Message
	})
	result := MapValidationResult{Diagnostics: diags}
	for _, d := range diags {
		if d.Severity == "error" {
			result.Errors++
		} else if d.Severity == "warning" {
			result.Warnings++
		}
	}
	result.Valid = result.Errors == 0
	return result, nil
}

func pointInWorld(x, y float32, b mapWorldBounds) bool {
	return x >= b.minX && x <= b.maxX && y >= b.minY && y <= b.maxY
}

func rectOutsideWorld(left, bottom, right, top float32, b mapWorldBounds) bool {
	minX, maxX := left, right
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := bottom, top
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	return maxX < b.minX || minX > b.maxX || maxY < b.minY || minY > b.maxY
}
