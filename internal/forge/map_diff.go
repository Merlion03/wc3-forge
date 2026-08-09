package forge

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

const (
	mapDiffDefaultLimit = 500
	mapDiffMaxLimit     = 2000
)

// MapDiffRequest previews the final state produced by a map patch without
// mutating the live Session. Operations use the same schemas and sequential
// target resolution as map_apply_patch.
type MapDiffRequest struct {
	Label      string            `json:"label,omitempty"`
	Operations []json.RawMessage `json:"operations"`
	Limit      int               `json:"limit,omitempty"`
}

type MapDiffChange struct {
	Entity string     `json:"entity"`
	Kind   string     `json:"kind"`
	Action string     `json:"action"` // create | modify | delete
	Before any        `json:"before,omitempty"`
	After  any        `json:"after,omitempty"`
	Bounds *SceneRect `json:"bounds,omitempty"`
}

type MapDiffResult struct {
	OK             bool                 `json:"ok"`
	Label          string               `json:"label"`
	OperationCount int                  `json:"operation_count"`
	Operations     []PatchOperationPlan `json:"operations"`
	TotalChanges   int                  `json:"total_changes"`
	Count          int                  `json:"count"`
	Truncated      bool                 `json:"truncated"`
	Changes        []MapDiffChange      `json:"changes"`
	Bounds         *SceneRect           `json:"bounds,omitempty"`
}

// PreviewMapPatch executes both preflight and apply only on deep in-memory
// clones, then compares the before/after snapshots. The live Session is never
// mutated and its history/dirty state are untouched.
func (s *Session) PreviewMapPatch(req MapDiffRequest) (MapDiffResult, error) {
	if req.Operations == nil {
		return MapDiffResult{}, fmt.Errorf("operations is required")
	}
	limit := req.Limit
	if limit == 0 {
		limit = mapDiffDefaultLimit
	}
	if limit < 1 || limit > mapDiffMaxLimit {
		return MapDiffResult{}, fmt.Errorf("limit must be between 1 and %d", mapDiffMaxLimit)
	}

	before, err := clonePatchPreviewSession(s)
	if err != nil {
		return MapDiffResult{}, err
	}
	plan, err := before.ApplyMapPatch(MapApplyPatchRequest{Label: req.Label, DryRun: true, Operations: req.Operations})
	if err != nil {
		return MapDiffResult{}, err
	}
	after, err := clonePatchPreviewSession(before)
	if err != nil {
		return MapDiffResult{}, err
	}
	if _, err := after.ApplyMapPatch(MapApplyPatchRequest{Label: req.Label, Operations: req.Operations}); err != nil {
		return MapDiffResult{}, fmt.Errorf("preview apply: %w", err)
	}

	changes, total, bounds := diffPatchSnapshots(snapshotPatchPreview(before), snapshotPatchPreview(after), limit)
	return MapDiffResult{
		OK: true, Label: plan.Label, OperationCount: len(plan.Operations), Operations: plan.Operations,
		TotalChanges: total, Count: len(changes), Truncated: total > len(changes), Changes: changes, Bounds: bounds,
	}, nil
}

func clonePatchPreviewSession(src *Session) (*Session, error) {
	src.mu.RLock()
	defer src.mu.RUnlock()
	if !src.loaded {
		return nil, fmt.Errorf("no map loaded")
	}
	info, err := clonePatchJSONPtr(src.info)
	if err != nil {
		return nil, fmt.Errorf("clone map info: %w", err)
	}
	units, err := clonePatchJSONPtr(src.units)
	if err != nil {
		return nil, fmt.Errorf("clone units: %w", err)
	}
	doodads, err := clonePatchJSONPtr(src.doodads)
	if err != nil {
		return nil, fmt.Errorf("clone doodads: %w", err)
	}
	terrain, err := clonePatchJSONPtr(src.terrain)
	if err != nil {
		return nil, fmt.Errorf("clone terrain: %w", err)
	}
	regions, err := clonePatchJSONPtr(src.regions)
	if err != nil {
		return nil, fmt.Errorf("clone regions: %w", err)
	}
	stringsCopy, err := clonePatchJSONValue(src.strings)
	if err != nil {
		return nil, fmt.Errorf("clone strings: %w", err)
	}
	return &Session{loaded: true, info: info, units: units, doodads: doodads, terrain: terrain, regions: regions, strings: stringsCopy, infoTokens: clonePatchStringMap(src.infoTokens)}, nil
}

func clonePatchJSONPtr[T any](in *T) (*T, error) {
	if in == nil {
		return nil, nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func clonePatchJSONValue[T any](in T) (T, error) {
	var out T
	b, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, err
	}
	return out, nil
}

func clonePatchStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type patchPreviewNode struct {
	kind   string
	value  any
	bounds *SceneRect
}

func snapshotPatchPreview(s *Session) map[string]patchPreviewNode {
	out := map[string]patchPreviewNode{}
	if s.units != nil {
		for _, e := range s.units.Entities {
			out[fmt.Sprintf("unit:%d", e.CreationNumber)] = patchPreviewNode{kind: "unit", value: e, bounds: pointDiffBounds(e.Position)}
		}
	}
	if s.doodads != nil {
		for _, d := range s.doodads.Doodads {
			out[fmt.Sprintf("doodad:%d", d.CreationNumber)] = patchPreviewNode{kind: "doodad", value: d, bounds: pointDiffBounds(d.Position)}
		}
	}
	if s.regions != nil {
		for _, r := range s.regions.Regions {
			out[fmt.Sprintf("region:%d", r.CreationNumber)] = patchPreviewNode{kind: "region", value: r, bounds: &SceneRect{MinX: float64(r.Left), MinY: float64(r.Bottom), MaxX: float64(r.Right), MaxY: float64(r.Top)}}
		}
	}
	if s.terrain != nil && s.terrain.Width > 0 {
		w := int(s.terrain.Width)
		for i, tp := range s.terrain.Tiles {
			col, row := i%w, i/w
			x := float64(s.terrain.CenterOffset[0] + float32(col)*terrainWorldStep)
			y := float64(s.terrain.CenterOffset[1] + float32(row)*terrainWorldStep)
			out[fmt.Sprintf("terrain:%d,%d", col, row)] = patchPreviewNode{kind: "terrain", value: tp, bounds: &SceneRect{MinX: x, MinY: y, MaxX: x, MaxY: y}}
		}
	}
	if s.info != nil {
		out["map_info"] = patchPreviewNode{kind: "map_info", value: *s.info}
	}
	return out
}

func pointDiffBounds(pos [3]float32) *SceneRect {
	x, y := float64(pos[0]), float64(pos[1])
	return &SceneRect{MinX: x, MinY: y, MaxX: x, MaxY: y}
}

func diffPatchSnapshots(before, after map[string]patchPreviewNode, limit int) ([]MapDiffChange, int, *SceneRect) {
	keys := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		keys[k] = struct{}{}
	}
	for k := range after {
		keys[k] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	changes := make([]MapDiffChange, 0)
	total := 0
	var allBounds *SceneRect
	for _, entity := range ordered {
		b, bok := before[entity]
		a, aok := after[entity]
		if bok && aok && reflect.DeepEqual(b.value, a.value) {
			continue
		}
		total++
		bounds := unionDiffBounds(b.bounds, a.bounds)
		allBounds = unionDiffBounds(allBounds, bounds)
		if len(changes) >= limit {
			continue
		}
		change := MapDiffChange{Entity: entity, Action: "modify", Kind: a.kind, Bounds: bounds}
		if bok {
			change.Before = b.value
		}
		if aok {
			change.After = a.value
		}
		if !bok {
			change.Action = "create"
		} else if !aok {
			change.Action, change.Kind = "delete", b.kind
		}
		changes = append(changes, change)
	}
	return changes, total, allBounds
}

func unionDiffBounds(a, b *SceneRect) *SceneRect {
	if a == nil {
		if b == nil {
			return nil
		}
		c := *b
		return &c
	}
	if b == nil {
		c := *a
		return &c
	}
	out := &SceneRect{MinX: a.MinX, MinY: a.MinY, MaxX: a.MaxX, MaxY: a.MaxY}
	if b.MinX < out.MinX {
		out.MinX = b.MinX
	}
	if b.MinY < out.MinY {
		out.MinY = b.MinY
	}
	if b.MaxX > out.MaxX {
		out.MaxX = b.MaxX
	}
	if b.MaxY > out.MaxY {
		out.MaxY = b.MaxY
	}
	return out
}
