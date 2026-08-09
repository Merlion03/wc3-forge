package forge

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	sceneQueryDefaultLimit = 100
	sceneQueryMaxLimit     = 500
)

var sceneKinds = map[string]struct{}{
	"unit":           {},
	"doodad":         {},
	"region":         {},
	"start_location": {},
}

var sceneFields = map[string]struct{}{
	"kind":            {},
	"id":              {},
	"creation_number": {},
	"index":           {},
	"type_id":         {},
	"name":            {},
	"player":          {},
	"position":        {},
	"bounds":          {},
	"rotation":        {},
	"scale":           {},
	"variation":       {},
	"life":            {},
	"flags":           {},
	"distance":        {},
}

// SceneQuery is the high-level read-only map query used by AI clients. It
// intentionally spans the four spatial entity surfaces an agent reasons about
// most often: placed units, doodads/destructibles, regions, and start locations.
// All filters are ANDed. Kinds defaults to all four kinds.
type SceneQuery struct {
	Kinds     []string     `json:"kinds,omitempty"`
	Where     SceneWhere   `json:"where,omitempty"`
	Spatial   SceneSpatial `json:"spatial,omitempty"`
	NearestTo *ScenePoint  `json:"nearest_to,omitempty"`
	Sort      string       `json:"sort,omitempty"`
	Order     string       `json:"order,omitempty"`
	Limit     int          `json:"limit,omitempty"`
	Offset    int          `json:"offset,omitempty"`
	Fields    []string     `json:"fields,omitempty"`
}

// SceneWhere holds attribute filters. A candidate that does not have the
// requested attribute does not match (for example, player excludes regions).
type SceneWhere struct {
	TypeID       *string `json:"type_id,omitempty"`
	Player       *uint32 `json:"player,omitempty"`
	NameContains *string `json:"name_contains,omitempty"`
	IDs          []int64 `json:"ids,omitempty"`
}

// SceneSpatial holds spatial filters. Multiple filters are ANDed.
type SceneSpatial struct {
	Radius       *SceneRadius `json:"radius,omitempty"`
	Rect         *SceneRect   `json:"rect,omitempty"`
	WithinRegion *int32       `json:"within_region,omitempty"`
}

// ScenePoint is a point in Warcraft III world/game coordinates.
type ScenePoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// SceneRadius matches points inside a circle and regions whose rectangle
// intersects the circle.
type SceneRadius struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Radius float64 `json:"radius"`
}

// SceneRect is an inclusive axis-aligned rectangle in WC3 world coordinates.
type SceneRect struct {
	MinX float64 `json:"min_x"`
	MinY float64 `json:"min_y"`
	MaxX float64 `json:"max_x"`
	MaxY float64 `json:"max_y"`
}

// SceneQueryResult is deliberately pagination-friendly: matched is the total
// number after filters; count is the returned page size.
type SceneQueryResult struct {
	Matched    int              `json:"matched"`
	Count      int              `json:"count"`
	Offset     int              `json:"offset"`
	Limit      int              `json:"limit"`
	Truncated  bool             `json:"truncated"`
	NextOffset int              `json:"next_offset,omitempty"`
	Items      []map[string]any `json:"items"`
}

type sceneCandidate struct {
	kind string
	id   int64

	creationNumber int64
	hasCreation    bool
	index          uint32
	hasIndex       bool
	typeID         string
	name           string
	player         uint32
	hasPlayer      bool

	position     [3]float32
	hasPosition  bool
	bounds       [4]float32 // min_x, min_y, max_x, max_y
	hasBounds    bool
	rotation     float32
	hasRotation  bool
	scale        [3]float32
	hasScale     bool
	variation    uint32
	hasVariation bool
	life         uint8
	hasLife      bool
	flags        uint8
	hasFlags     bool

	distance    float64
	hasDistance bool
}

// QueryScene snapshots the loaded scene under one read lock, then performs
// filtering/sorting/projection on value copies after releasing the lock. This
// gives one internally-consistent view without holding the Session lock while
// potentially sorting hundreds of results.
func (s *Session) QueryScene(q SceneQuery) (SceneQueryResult, error) {
	kinds, err := normalizeSceneKinds(q.Kinds)
	if err != nil {
		return SceneQueryResult{}, err
	}
	fields, err := normalizeSceneFields(q.Fields, q.NearestTo != nil)
	if err != nil {
		return SceneQueryResult{}, err
	}
	limit, err := normalizeSceneLimit(q.Limit)
	if err != nil {
		return SceneQueryResult{}, err
	}
	if q.Offset < 0 {
		return SceneQueryResult{}, fmt.Errorf("offset must be >= 0")
	}
	if q.Where.TypeID != nil && len(*q.Where.TypeID) != 4 {
		return SceneQueryResult{}, fmt.Errorf("where.type_id %q must be exactly 4 bytes", *q.Where.TypeID)
	}
	if q.Spatial.Radius != nil && q.Spatial.Radius.Radius < 0 {
		return SceneQueryResult{}, fmt.Errorf("spatial.radius.radius must be >= 0")
	}
	if q.Spatial.Rect != nil {
		if err := validateSceneRect(*q.Spatial.Rect, "spatial.rect"); err != nil {
			return SceneQueryResult{}, err
		}
	}
	sortBy, order, err := normalizeSceneSort(q.Sort, q.Order, q.NearestTo != nil)
	if err != nil {
		return SceneQueryResult{}, err
	}

	// Snapshot all needed values under one RLock. Do not call public Session
	// accessors here: they take their own locks and would defeat the one-snapshot
	// guarantee this high-level query is meant to provide.
	s.mu.RLock()
	if !s.loaded {
		s.mu.RUnlock()
		return SceneQueryResult{}, fmt.Errorf("no map loaded")
	}

	var within *SceneRect
	if q.Spatial.WithinRegion != nil {
		if s.regions == nil {
			s.mu.RUnlock()
			return SceneQueryResult{}, fmt.Errorf("no region with creation_number %d", *q.Spatial.WithinRegion)
		}
		for i := range s.regions.Regions {
			rg := &s.regions.Regions[i]
			if rg.CreationNumber == *q.Spatial.WithinRegion {
				r := SceneRect{MinX: float64(rg.Left), MinY: float64(rg.Bottom), MaxX: float64(rg.Right), MaxY: float64(rg.Top)}
				within = &r
				break
			}
		}
		if within == nil {
			s.mu.RUnlock()
			return SceneQueryResult{}, fmt.Errorf("no region with creation_number %d", *q.Spatial.WithinRegion)
		}
	}

	candidates := make([]sceneCandidate, 0)
	if _, ok := kinds["unit"]; ok && s.units != nil {
		for i := range s.units.Entities {
			e := &s.units.Entities[i]
			if e.TypeID == slocTypeID {
				continue // start locations have their own semantic kind below.
			}
			candidates = append(candidates, sceneCandidate{
				kind: "unit", id: int64(e.CreationNumber),
				creationNumber: int64(e.CreationNumber), hasCreation: true,
				typeID: e.TypeID, player: e.Player, hasPlayer: true,
				position: e.Position, hasPosition: true,
				rotation: e.Rotation, hasRotation: true,
				scale: e.Scale, hasScale: true,
				variation: e.Variation, hasVariation: true,
				flags: e.Flags, hasFlags: true,
			})
		}
	}
	if _, ok := kinds["start_location"]; ok && s.units != nil {
		for i := range s.units.Entities {
			e := &s.units.Entities[i]
			if e.TypeID != slocTypeID {
				continue
			}
			candidates = append(candidates, sceneCandidate{
				kind: "start_location", id: int64(e.Player),
				creationNumber: int64(e.CreationNumber), hasCreation: true,
				index: e.Player, hasIndex: true,
				position: e.Position, hasPosition: true,
				rotation: e.Rotation, hasRotation: true,
			})
		}
	}
	if _, ok := kinds["doodad"]; ok && s.doodads != nil {
		for i := range s.doodads.Doodads {
			d := &s.doodads.Doodads[i]
			candidates = append(candidates, sceneCandidate{
				kind: "doodad", id: int64(d.CreationNumber),
				creationNumber: int64(d.CreationNumber), hasCreation: true,
				typeID:   d.TypeID,
				position: d.Position, hasPosition: true,
				rotation: d.Rotation, hasRotation: true,
				scale: d.Scale, hasScale: true,
				variation: d.Variation, hasVariation: true,
				life: d.Life, hasLife: true,
				flags: d.Flags, hasFlags: true,
			})
		}
	}
	if _, ok := kinds["region"]; ok && s.regions != nil {
		for i := range s.regions.Regions {
			rg := &s.regions.Regions[i]
			candidates = append(candidates, sceneCandidate{
				kind: "region", id: int64(rg.CreationNumber),
				creationNumber: int64(rg.CreationNumber), hasCreation: true,
				name:   rg.Name,
				bounds: [4]float32{rg.Left, rg.Bottom, rg.Right, rg.Top}, hasBounds: true,
			})
		}
	}
	s.mu.RUnlock()

	idSet := make(map[int64]struct{}, len(q.Where.IDs))
	for _, id := range q.Where.IDs {
		idSet[id] = struct{}{}
	}

	filtered := candidates[:0]
	for _, c := range candidates {
		if !sceneMatchesWhere(c, q.Where, idSet) {
			continue
		}
		if q.Spatial.Radius != nil && !sceneIntersectsCircle(c, *q.Spatial.Radius) {
			continue
		}
		if q.Spatial.Rect != nil && !sceneIntersectsRect(c, *q.Spatial.Rect) {
			continue
		}
		if within != nil && !sceneIntersectsRect(c, *within) {
			continue
		}
		if q.NearestTo != nil {
			c.distance = sceneDistanceToPoint(c, *q.NearestTo)
			c.hasDistance = true
		}
		filtered = append(filtered, c)
	}

	sort.Slice(filtered, func(i, j int) bool {
		cmp := compareSceneCandidate(filtered[i], filtered[j], sortBy)
		if order == "desc" {
			cmp = -cmp
		}
		return cmp < 0
	})

	matched := len(filtered)
	start := q.Offset
	if start > matched {
		start = matched
	}
	end := start + limit
	if end > matched {
		end = matched
	}

	items := make([]map[string]any, 0, end-start)
	for _, c := range filtered[start:end] {
		items = append(items, projectSceneCandidate(c, fields))
	}

	result := SceneQueryResult{
		Matched:   matched,
		Count:     len(items),
		Offset:    q.Offset,
		Limit:     limit,
		Truncated: end < matched,
		Items:     items,
	}
	if result.Truncated {
		result.NextOffset = end
	}
	return result, nil
}

func normalizeSceneKinds(in []string) (map[string]struct{}, error) {
	if len(in) == 0 {
		out := make(map[string]struct{}, len(sceneKinds))
		for k := range sceneKinds {
			out[k] = struct{}{}
		}
		return out, nil
	}
	out := make(map[string]struct{}, len(in))
	for _, kind := range in {
		if _, ok := sceneKinds[kind]; !ok {
			return nil, fmt.Errorf("unknown scene kind %q", kind)
		}
		out[kind] = struct{}{}
	}
	return out, nil
}

func normalizeSceneFields(in []string, hasNearest bool) (map[string]struct{}, error) {
	if len(in) == 0 {
		return nil, nil // nil means per-kind defaults.
	}
	out := make(map[string]struct{}, len(in)+2)
	out["kind"] = struct{}{}
	out["id"] = struct{}{}
	for _, field := range in {
		if _, ok := sceneFields[field]; !ok {
			return nil, fmt.Errorf("unknown scene field %q", field)
		}
		if field == "distance" && !hasNearest {
			return nil, fmt.Errorf("field %q requires nearest_to", field)
		}
		out[field] = struct{}{}
	}
	return out, nil
}

func normalizeSceneLimit(limit int) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("limit must be >= 0")
	}
	if limit == 0 {
		return sceneQueryDefaultLimit, nil
	}
	if limit > sceneQueryMaxLimit {
		return 0, fmt.Errorf("limit must be <= %d", sceneQueryMaxLimit)
	}
	return limit, nil
}

func normalizeSceneSort(sortBy, order string, hasNearest bool) (string, string, error) {
	if sortBy == "" {
		if hasNearest {
			sortBy = "distance"
		} else {
			sortBy = "kind"
		}
	}
	switch sortBy {
	case "distance":
		if !hasNearest {
			return "", "", fmt.Errorf("sort %q requires nearest_to", sortBy)
		}
	case "kind", "id", "x", "y", "type_id":
	default:
		return "", "", fmt.Errorf("unknown scene sort %q", sortBy)
	}
	if order == "" {
		order = "asc"
	}
	if order != "asc" && order != "desc" {
		return "", "", fmt.Errorf("order must be %q or %q", "asc", "desc")
	}
	return sortBy, order, nil
}

func validateSceneRect(r SceneRect, label string) error {
	if r.MinX > r.MaxX || r.MinY > r.MaxY {
		return fmt.Errorf("%s has invalid bounds: min must be <= max", label)
	}
	return nil
}

func sceneMatchesWhere(c sceneCandidate, w SceneWhere, ids map[int64]struct{}) bool {
	if w.TypeID != nil && c.typeID != *w.TypeID {
		return false
	}
	if w.Player != nil && (!c.hasPlayer || c.player != *w.Player) {
		return false
	}
	if w.NameContains != nil {
		if c.name == "" || !strings.Contains(strings.ToLower(c.name), strings.ToLower(*w.NameContains)) {
			return false
		}
	}
	if len(ids) > 0 {
		if _, ok := ids[c.id]; !ok {
			return false
		}
	}
	return true
}

func sceneIntersectsCircle(c sceneCandidate, circle SceneRadius) bool {
	return sceneDistanceToPoint(c, ScenePoint{X: circle.X, Y: circle.Y}) <= circle.Radius
}

func sceneIntersectsRect(c sceneCandidate, rect SceneRect) bool {
	if c.hasBounds {
		return float64(c.bounds[0]) <= rect.MaxX && float64(c.bounds[2]) >= rect.MinX &&
			float64(c.bounds[1]) <= rect.MaxY && float64(c.bounds[3]) >= rect.MinY
	}
	if c.hasPosition {
		x, y := float64(c.position[0]), float64(c.position[1])
		return x >= rect.MinX && x <= rect.MaxX && y >= rect.MinY && y <= rect.MaxY
	}
	return false
}

func sceneDistanceToPoint(c sceneCandidate, p ScenePoint) float64 {
	if c.hasBounds {
		x := clampFloat64(p.X, float64(c.bounds[0]), float64(c.bounds[2]))
		y := clampFloat64(p.Y, float64(c.bounds[1]), float64(c.bounds[3]))
		return math.Hypot(p.X-x, p.Y-y)
	}
	if c.hasPosition {
		return math.Hypot(p.X-float64(c.position[0]), p.Y-float64(c.position[1]))
	}
	return math.Inf(1)
}

func clampFloat64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func compareSceneCandidate(a, b sceneCandidate, sortBy string) int {
	var cmp int
	switch sortBy {
	case "distance":
		cmp = cmpFloat64(a.distance, b.distance)
	case "kind":
		cmp = strings.Compare(a.kind, b.kind)
	case "id":
		cmp = cmpInt64(a.id, b.id)
	case "x":
		cmp = cmpFloat64(sceneCandidateX(a), sceneCandidateX(b))
	case "y":
		cmp = cmpFloat64(sceneCandidateY(a), sceneCandidateY(b))
	case "type_id":
		cmp = strings.Compare(a.typeID, b.typeID)
	}
	if cmp != 0 {
		return cmp
	}
	if cmp = strings.Compare(a.kind, b.kind); cmp != 0 {
		return cmp
	}
	return cmpInt64(a.id, b.id)
}

func sceneCandidateX(c sceneCandidate) float64 {
	if c.hasBounds {
		return (float64(c.bounds[0]) + float64(c.bounds[2])) / 2
	}
	if c.hasPosition {
		return float64(c.position[0])
	}
	return 0
}

func sceneCandidateY(c sceneCandidate) float64 {
	if c.hasBounds {
		return (float64(c.bounds[1]) + float64(c.bounds[3])) / 2
	}
	if c.hasPosition {
		return float64(c.position[1])
	}
	return 0
}

func cmpFloat64(a, b float64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func cmpInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func projectSceneCandidate(c sceneCandidate, fields map[string]struct{}) map[string]any {
	out := map[string]any{"kind": c.kind, "id": c.id}
	want := func(field string) bool {
		if fields != nil {
			_, ok := fields[field]
			return ok
		}
		switch c.kind {
		case "unit":
			switch field {
			case "creation_number", "type_id", "player", "position", "rotation", "scale":
				return true
			}
		case "doodad":
			switch field {
			case "creation_number", "type_id", "position", "rotation", "scale", "variation", "life", "flags":
				return true
			}
		case "region":
			switch field {
			case "creation_number", "name", "bounds":
				return true
			}
		case "start_location":
			switch field {
			case "index", "creation_number", "position", "rotation":
				return true
			}
		}
		return field == "distance" && c.hasDistance
	}

	if c.hasCreation && want("creation_number") {
		out["creation_number"] = c.creationNumber
	}
	if c.hasIndex && want("index") {
		out["index"] = c.index
	}
	if c.typeID != "" && want("type_id") {
		out["type_id"] = c.typeID
	}
	if c.name != "" && want("name") {
		out["name"] = c.name
	}
	if c.hasPlayer && want("player") {
		out["player"] = c.player
	}
	if c.hasPosition && want("position") {
		out["position"] = c.position
	}
	if c.hasBounds && want("bounds") {
		out["bounds"] = map[string]float32{
			"min_x": c.bounds[0], "min_y": c.bounds[1], "max_x": c.bounds[2], "max_y": c.bounds[3],
		}
	}
	if c.hasRotation && want("rotation") {
		out["rotation"] = c.rotation
	}
	if c.hasScale && want("scale") {
		out["scale"] = c.scale
	}
	if c.hasVariation && want("variation") {
		out["variation"] = c.variation
	}
	if c.hasLife && want("life") {
		out["life"] = c.life
	}
	if c.hasFlags && want("flags") {
		out["flags"] = c.flags
	}
	if c.hasDistance && want("distance") {
		out["distance"] = c.distance
	}
	return out
}
