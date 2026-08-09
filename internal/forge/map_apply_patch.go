package forge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

const mapPatchMaxOperations = 500

// MapApplyPatchRequest is the high-level batch mutation contract used by AI
// clients. Operations are kept raw until preflight so every op can be decoded
// strictly against its own schema. DryRun performs the same sequential target
// resolution as apply, but never calls a mutator.
type MapApplyPatchRequest struct {
	Label      string            `json:"label,omitempty"`
	DryRun     bool              `json:"dry_run,omitempty"`
	Operations []json.RawMessage `json:"operations"`
}

type PatchOperationPlan struct {
	Index               int    `json:"index"`
	Op                  string `json:"op"`
	Affected            int    `json:"affected"`
	Summary             string `json:"summary,omitempty"`
	PredictedCreationID int64  `json:"predicted_creation_number,omitempty"`
}

type MapApplyPatchResult struct {
	OK             bool                 `json:"ok"`
	DryRun         bool                 `json:"dry_run"`
	Applied        bool                 `json:"applied"`
	Label          string               `json:"label"`
	OperationCount int                  `json:"operation_count"`
	Operations     []PatchOperationPlan `json:"operations"`
	Warnings       []string             `json:"warnings,omitempty"`
}

type preparedPatchOperation struct {
	plan  PatchOperationPlan
	apply func(*Session) error
}

type patchPreflightState struct {
	// units contains only ordinary addressable units; unitCreationIDs contains
	// every war3mapUnits.doo creation number including sloc markers so create
	// prediction matches nextUnitCreationNumber exactly without exposing slocs
	// through the units.* patch surface.
	units           map[uint32]struct{}
	unitCreationIDs map[uint32]struct{}
	doodads         map[uint32]struct{}
	regions         map[int32]patchRegionState
	terrain         *patchTerrainState
}

type patchRegionState struct {
	name                     string
	left, bottom, right, top float32
}

type patchTerrainState struct {
	width, height int
	groundPalette map[string]struct{}
}

// ApplyMapPatch preflights every operation against a value snapshot and then,
// unless dry_run is set, executes the prepared mutators inside RunAtomicGroup.
// A failure during execution rolls every earlier operation back and leaves
// history/dirty state exactly as it was before the call.
func (s *Session) ApplyMapPatch(req MapApplyPatchRequest) (MapApplyPatchResult, error) {
	if req.Operations == nil {
		return MapApplyPatchResult{}, fmt.Errorf("operations is required")
	}
	if len(req.Operations) > mapPatchMaxOperations {
		return MapApplyPatchResult{}, fmt.Errorf("operation count %d exceeds max %d", len(req.Operations), mapPatchMaxOperations)
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "AI map patch"
	}

	state, err := s.snapshotPatchPreflightState()
	if err != nil {
		return MapApplyPatchResult{}, err
	}
	prepared := make([]preparedPatchOperation, 0, len(req.Operations))
	for i, raw := range req.Operations {
		op, err := preparePatchOperation(i, raw, state)
		if err != nil {
			return MapApplyPatchResult{}, fmt.Errorf("operation %d: %w", i, err)
		}
		prepared = append(prepared, op)
	}

	result := MapApplyPatchResult{
		OK: true, DryRun: req.DryRun, Applied: false, Label: label,
		OperationCount: len(prepared), Operations: make([]PatchOperationPlan, len(prepared)),
	}
	for i := range prepared {
		result.Operations[i] = prepared[i].plan
	}
	if req.DryRun || len(prepared) == 0 {
		return result, nil
	}

	err = s.RunAtomicGroup(label, func() error {
		for i, op := range prepared {
			if err := op.apply(s); err != nil {
				return fmt.Errorf("operation %d (%s): %w", i, op.plan.Op, err)
			}
		}
		return nil
	})
	if err != nil {
		return MapApplyPatchResult{}, err
	}
	result.Applied = true
	return result, nil
}

func (s *Session) snapshotPatchPreflightState() (*patchPreflightState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return nil, fmt.Errorf("no map loaded")
	}
	if s.groupDepth != 0 {
		return nil, fmt.Errorf("cannot apply map patch while undo group depth is %d", s.groupDepth)
	}
	st := &patchPreflightState{
		units: make(map[uint32]struct{}), unitCreationIDs: make(map[uint32]struct{}),
		doodads: make(map[uint32]struct{}), regions: make(map[int32]patchRegionState),
	}
	if s.units != nil {
		for i := range s.units.Entities {
			e := &s.units.Entities[i]
			if _, exists := st.unitCreationIDs[e.CreationNumber]; exists {
				return nil, fmt.Errorf("cannot patch map with duplicate unit creation_number %d; run map_validate first", e.CreationNumber)
			}
			st.unitCreationIDs[e.CreationNumber] = struct{}{}
			if e.TypeID == slocTypeID {
				continue
			}
			st.units[e.CreationNumber] = struct{}{}
		}
	}
	if s.doodads != nil {
		for i := range s.doodads.Doodads {
			cn := s.doodads.Doodads[i].CreationNumber
			if _, exists := st.doodads[cn]; exists {
				return nil, fmt.Errorf("cannot patch map with duplicate doodad creation_number %d; run map_validate first", cn)
			}
			st.doodads[cn] = struct{}{}
		}
	}
	if s.regions != nil {
		for i := range s.regions.Regions {
			r := &s.regions.Regions[i]
			if _, exists := st.regions[r.CreationNumber]; exists {
				return nil, fmt.Errorf("cannot patch map with duplicate region creation_number %d; run map_validate first", r.CreationNumber)
			}
			st.regions[r.CreationNumber] = patchRegionState{name: r.Name, left: r.Left, bottom: r.Bottom, right: r.Right, top: r.Top}
		}
	}
	if s.terrain != nil {
		p := make(map[string]struct{}, len(s.terrain.GroundTilesets))
		for _, id := range s.terrain.GroundTilesets {
			p[id] = struct{}{}
		}
		st.terrain = &patchTerrainState{width: int(s.terrain.Width), height: int(s.terrain.Height), groundPalette: p}
	}
	return st, nil
}

func preparePatchOperation(index int, raw json.RawMessage, st *patchPreflightState) (preparedPatchOperation, error) {
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return preparedPatchOperation{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if head.Op == "" {
		return preparedPatchOperation{}, fmt.Errorf("op is required")
	}
	if err := requirePatchFields(raw, requiredPatchFields(head.Op)...); err != nil {
		return preparedPatchOperation{}, err
	}

	plan := func(op, summary string, affected int, predicted int64, apply func(*Session) error) preparedPatchOperation {
		return preparedPatchOperation{plan: PatchOperationPlan{Index: index, Op: op, Affected: affected, Summary: summary, PredictedCreationID: predicted}, apply: apply}
	}
	requireUnit := func(cn uint32) error {
		if _, ok := st.units[cn]; !ok {
			return fmt.Errorf("no unit with creation_number %d", cn)
		}
		return nil
	}
	requireDoodad := func(cn uint32) error {
		if _, ok := st.doodads[cn]; !ok {
			return fmt.Errorf("no doodad with creation_number %d", cn)
		}
		return nil
	}
	requireRegion := func(cn int32) error {
		if _, ok := st.regions[cn]; !ok {
			return fmt.Errorf("no region with creation_number %d", cn)
		}
		return nil
	}

	switch head.Op {
	case "units.move":
		var p patchUnitsMove
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireUnit(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, fmt.Sprintf("move unit %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.MoveUnit(p.CreationNumber, p.X, p.Y, p.Z) }), nil
	case "units.rotate":
		var p patchUnitsRotate
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireUnit(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, fmt.Sprintf("rotate unit %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.RotateUnit(p.CreationNumber, p.Rotation) }), nil
	case "units.scale":
		var p patchScale
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireUnit(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, fmt.Sprintf("scale unit %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.ScaleUnit(p.CreationNumber, p.SX, p.SY, p.SZ) }), nil
	case "units.set_field":
		var p patchUnitSetField
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireUnit(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := preflightUnitField(p); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, fmt.Sprintf("set unit %d field %s", p.CreationNumber, p.Field), 1, 0, func(s *Session) error {
			if p.Field == UnitFieldItemDrops {
				return s.SetUnitInstanceItemDrops(p.CreationNumber, *p.ItemDrops)
			}
			return s.SetUnitInstanceField(p.CreationNumber, p.Field, *p.Value)
		}), nil
	case "units.create":
		var p patchUnitCreate
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if len(p.TypeID) != 4 {
			return preparedPatchOperation{}, fmt.Errorf("type_id %q must be exactly 4 bytes", p.TypeID)
		}
		if p.TypeID == slocTypeID {
			return preparedPatchOperation{}, fmt.Errorf("type_id %q is a start location; use the start_locations surface instead of units.create", p.TypeID)
		}
		predicted := nextPatchUint32(st.unitCreationIDs)
		if predicted == 0 {
			return preparedPatchOperation{}, fmt.Errorf("unit creation_number space exhausted")
		}
		st.units[predicted] = struct{}{}
		st.unitCreationIDs[predicted] = struct{}{}
		return plan(head.Op, fmt.Sprintf("create unit %s as %d", p.TypeID, predicted), 1, int64(predicted), func(s *Session) error {
			got, err := s.CreateUnit(p.TypeID, p.Player, [3]float32{p.X, p.Y, p.Z}, p.Rotation, p.Scale)
			if err == nil && got != predicted {
				return fmt.Errorf("creation_number changed after preflight: predicted %d, got %d", predicted, got)
			}
			return err
		}), nil
	case "units.delete":
		var p patchEntityDelete
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireUnit(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		delete(st.units, p.CreationNumber)
		delete(st.unitCreationIDs, p.CreationNumber)
		return plan(head.Op, fmt.Sprintf("delete unit %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.DeleteUnit(p.CreationNumber) }), nil

	case "doodads.move":
		var p patchDoodadMove
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireDoodad(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, fmt.Sprintf("move doodad %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.MoveDoodad(p.CreationNumber, p.X, p.Y, p.Z) }), nil
	case "doodads.rotate":
		var p patchDoodadRotate
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireDoodad(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, fmt.Sprintf("rotate doodad %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.RotateDoodad(p.CreationNumber, p.Rotation) }), nil
	case "doodads.scale":
		var p patchScale
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireDoodad(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, fmt.Sprintf("scale doodad %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.ScaleDoodad(p.CreationNumber, p.SX, p.SY, p.SZ) }), nil
	case "doodads.create":
		var p patchDoodadCreate
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if len(p.TypeID) != 4 {
			return preparedPatchOperation{}, fmt.Errorf("type_id %q must be exactly 4 bytes", p.TypeID)
		}
		predicted := nextPatchUint32(st.doodads)
		if predicted == 0 {
			return preparedPatchOperation{}, fmt.Errorf("doodad creation_number space exhausted")
		}
		st.doodads[predicted] = struct{}{}
		return plan(head.Op, fmt.Sprintf("create doodad %s as %d", p.TypeID, predicted), 1, int64(predicted), func(s *Session) error {
			got, err := s.CreateDoodad(p.TypeID, [3]float32{p.X, p.Y, p.Z}, p.Rotation, p.Scale, p.Variation)
			if err == nil && got != predicted {
				return fmt.Errorf("creation_number changed after preflight: predicted %d, got %d", predicted, got)
			}
			return err
		}), nil
	case "doodads.delete":
		var p patchEntityDelete
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireDoodad(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		delete(st.doodads, p.CreationNumber)
		return plan(head.Op, fmt.Sprintf("delete doodad %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.DeleteDoodad(p.CreationNumber) }), nil

	case "regions.create":
		var p patchRegionCreate
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if p.Name == "" {
			return preparedPatchOperation{}, fmt.Errorf("name is required")
		}
		if err := validateRegionStrings(p.Name, p.WeatherID, p.AmbientID); err != nil {
			return preparedPatchOperation{}, err
		}
		color, err := ParseRegionColor(p.Color)
		if err != nil {
			return preparedPatchOperation{}, err
		}
		left, bottom, right, top := normalizeBounds(p.MinX, p.MinY, p.MaxX, p.MaxY)
		predicted := nextPatchInt32(st.regions)
		if predicted <= 0 {
			return preparedPatchOperation{}, fmt.Errorf("region creation_number space exhausted")
		}
		st.regions[predicted] = patchRegionState{name: p.Name, left: left, bottom: bottom, right: right, top: top}
		return plan(head.Op, fmt.Sprintf("create region %q as %d", p.Name, predicted), 1, int64(predicted), func(s *Session) error {
			got, err := s.CreateRegion(p.Name, p.MinX, p.MinY, p.MaxX, p.MaxY, p.WeatherID, p.AmbientID, color)
			if err == nil && got != predicted {
				return fmt.Errorf("creation_number changed after preflight: predicted %d, got %d", predicted, got)
			}
			return err
		}), nil
	case "regions.move":
		var p patchRegionMove
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireRegion(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		r := st.regions[p.CreationNumber]
		r.left += p.DX
		r.right += p.DX
		r.bottom += p.DY
		r.top += p.DY
		st.regions[p.CreationNumber] = r
		return plan(head.Op, fmt.Sprintf("move region %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.MoveRegion(p.CreationNumber, p.DX, p.DY) }), nil
	case "regions.resize":
		var p patchRegionResize
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireRegion(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		l, b, r, t := normalizeBounds(p.MinX, p.MinY, p.MaxX, p.MaxY)
		rg := st.regions[p.CreationNumber]
		rg.left = l
		rg.bottom = b
		rg.right = r
		rg.top = t
		st.regions[p.CreationNumber] = rg
		return plan(head.Op, fmt.Sprintf("resize region %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.ResizeRegion(p.CreationNumber, p.MinX, p.MinY, p.MaxX, p.MaxY) }), nil
	case "regions.rename":
		var p patchRegionRename
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireRegion(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		if p.Name == "" {
			return preparedPatchOperation{}, fmt.Errorf("name is required")
		}
		if err := validateRegionStrings(p.Name, "", ""); err != nil {
			return preparedPatchOperation{}, err
		}
		r := st.regions[p.CreationNumber]
		r.name = p.Name
		st.regions[p.CreationNumber] = r
		return plan(head.Op, fmt.Sprintf("rename region %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.RenameRegion(p.CreationNumber, p.Name) }), nil
	case "regions.delete":
		var p patchRegionDelete
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := requireRegion(p.CreationNumber); err != nil {
			return preparedPatchOperation{}, err
		}
		delete(st.regions, p.CreationNumber)
		return plan(head.Op, fmt.Sprintf("delete region %d", p.CreationNumber), 1, 0, func(s *Session) error { return s.DeleteRegion(p.CreationNumber) }), nil

	case "terrain.set_tile":
		var p patchTerrainSetTile
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := preflightTerrainPoint(st, p.Col, p.Row); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := preflightGroundTile(st, p.GroundTileID); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, fmt.Sprintf("set terrain tile (%d,%d)", p.Col, p.Row), 1, 0, func(s *Session) error { return s.SetTerrainTile(p.Col, p.Row, p.GroundTileID) }), nil
	case "terrain.set_height":
		var p patchTerrainSetHeight
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := preflightTerrainPoint(st, p.Col, p.Row); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, fmt.Sprintf("set terrain height (%d,%d)", p.Col, p.Row), 1, 0, func(s *Session) error { return s.SetTerrainHeight(p.Col, p.Row, p.Height) }), nil
	case "terrain.paint_tile":
		var p patchTerrainPaint
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := preflightTerrainBrush(st, p.Col, p.Row, p.Radius, p.Shape); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := preflightGroundTile(st, p.GroundTileID); err != nil {
			return preparedPatchOperation{}, err
		}
		affected := estimateBrushCorners(st.terrain, p.Col, p.Row, p.Radius, p.Shape)
		return plan(head.Op, fmt.Sprintf("paint terrain around (%d,%d)", p.Col, p.Row), affected, 0, func(s *Session) error { return s.PaintTileBrush(p.Col, p.Row, p.Radius, p.Shape, p.GroundTileID) }), nil
	case "terrain.brush_height":
		var p patchTerrainHeightBrush
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := preflightTerrainBrush(st, p.Col, p.Row, p.Radius, p.Shape); err != nil {
			return preparedPatchOperation{}, err
		}
		if p.Mode != "raise" && p.Mode != "lower" && p.Mode != "flatten" && p.Mode != "smooth" {
			return preparedPatchOperation{}, fmt.Errorf("mode must be raise, lower, flatten, or smooth")
		}
		if p.Mode == "flatten" {
			if err := requirePatchFields(raw, "target"); err != nil {
				return preparedPatchOperation{}, err
			}
		}
		affected := estimateBrushCorners(st.terrain, p.Col, p.Row, p.Radius, p.Shape)
		return plan(head.Op, fmt.Sprintf("brush terrain height around (%d,%d)", p.Col, p.Row), affected, 0, func(s *Session) error {
			return s.HeightBrush(p.Col, p.Row, p.Radius, p.Shape, p.Mode, p.Strength, p.Target)
		}), nil

	case "map.info_set":
		var p patchMapInfoSet
		if err := decodePatchStrict(raw, &p); err != nil {
			return preparedPatchOperation{}, err
		}
		if err := preflightMapInfoUpdates(p.Updates); err != nil {
			return preparedPatchOperation{}, err
		}
		return plan(head.Op, "update map info", len(p.Updates), 0, func(s *Session) error { _, err := s.SetMapInfo(p.Updates); return err }), nil
	default:
		return preparedPatchOperation{}, fmt.Errorf("unsupported op %q", head.Op)
	}
}

func requiredPatchFields(op string) []string {
	switch op {
	case "units.move", "doodads.move":
		return []string{"creation_number", "x", "y", "z"}
	case "units.rotate", "doodads.rotate":
		return []string{"creation_number", "rotation"}
	case "units.scale", "doodads.scale":
		return []string{"creation_number", "sx", "sy", "sz"}
	case "units.set_field":
		return []string{"creation_number", "field"}
	case "units.create":
		return []string{"type_id", "player", "x", "y"}
	case "doodads.create":
		return []string{"type_id", "x", "y"}
	case "units.delete", "doodads.delete", "regions.delete":
		return []string{"creation_number"}
	case "regions.create":
		return []string{"name", "min_x", "min_y", "max_x", "max_y"}
	case "regions.move":
		return []string{"creation_number", "dx", "dy"}
	case "regions.resize":
		return []string{"creation_number", "min_x", "min_y", "max_x", "max_y"}
	case "regions.rename":
		return []string{"creation_number", "name"}
	case "terrain.set_tile":
		return []string{"col", "row", "ground_tile_id"}
	case "terrain.set_height":
		return []string{"col", "row", "height"}
	case "terrain.paint_tile":
		return []string{"col", "row", "radius", "ground_tile_id"}
	case "terrain.brush_height":
		return []string{"col", "row", "radius", "mode"}
	case "map.info_set":
		return []string{"updates"}
	default:
		return nil
	}
}

func requirePatchFields(raw []byte, fields ...string) error {
	if len(fields) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	for _, field := range fields {
		v, ok := obj[field]
		if !ok || len(bytes.TrimSpace(v)) == 0 || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return fmt.Errorf("%s is required", field)
		}
	}
	return nil
}

func decodePatchStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid params: trailing JSON")
		}
		return fmt.Errorf("invalid params: trailing JSON: %w", err)
	}
	return nil
}

func nextPatchUint32(ids map[uint32]struct{}) uint32 {
	var max uint32
	for id := range ids {
		if id > max {
			max = id
		}
	}
	return max + 1
}
func nextPatchInt32(ids map[int32]patchRegionState) int32 {
	var max int32
	for id := range ids {
		if id > max {
			max = id
		}
	}
	return max + 1
}

func preflightTerrainPoint(st *patchPreflightState, col, row int) error {
	if st.terrain == nil {
		return fmt.Errorf("no terrain loaded")
	}
	if col < 0 || col >= st.terrain.width || row < 0 || row >= st.terrain.height {
		return fmt.Errorf("terrain corner (%d,%d) out of range [0,%d)x[0,%d)", col, row, st.terrain.width, st.terrain.height)
	}
	return nil
}
func preflightGroundTile(st *patchPreflightState, id string) error {
	if len(id) != 4 {
		return fmt.Errorf("ground_tile_id %q is not a 4-char FourCC", id)
	}
	if st.terrain == nil {
		return fmt.Errorf("no terrain loaded")
	}
	if _, ok := st.terrain.groundPalette[id]; !ok {
		return fmt.Errorf("ground_tile_id %q not in map ground palette", id)
	}
	return nil
}
func preflightTerrainBrush(st *patchPreflightState, col, row int, radius float64, shape string) error {
	if err := preflightTerrainPoint(st, col, row); err != nil {
		return err
	}
	if radius < 0 {
		return fmt.Errorf("radius must be >= 0")
	}
	if shape != "" && shape != "circle" && shape != "square" {
		return fmt.Errorf("shape must be circle or square")
	}
	return nil
}
func estimateBrushCorners(t *patchTerrainState, col, row int, radius float64, shape string) int {
	if t == nil {
		return 0
	}
	reach := radius + 0.5
	lim := int(math.Ceil(reach))
	square := shape == "square"
	n := 0
	for dy := -lim; dy <= lim; dy++ {
		r := row + dy
		if r < 0 || r >= t.height {
			continue
		}
		for dx := -lim; dx <= lim; dx++ {
			c := col + dx
			if c < 0 || c >= t.width {
				continue
			}
			if square {
				if math.Abs(float64(dx)) > reach || math.Abs(float64(dy)) > reach {
					continue
				}
			} else if float64(dx*dx+dy*dy) > reach*reach {
				continue
			}
			n++
		}
	}
	return n
}

func preflightUnitField(p patchUnitSetField) error {
	if p.Field == "" {
		return fmt.Errorf("field is required")
	}
	if p.Field == UnitFieldItemDrops {
		if p.Value != nil {
			return fmt.Errorf("value and item_drops are mutually exclusive")
		}
		if p.ItemDrops == nil {
			return fmt.Errorf("item_drops is required for field %q", p.Field)
		}
		for i, d := range *p.ItemDrops {
			if len(d.ItemID) != 4 {
				return fmt.Errorf("item_drops[%d].item_id %q must be exactly 4 bytes", i, d.ItemID)
			}
		}
		return nil
	}
	if p.ItemDrops != nil {
		return fmt.Errorf("item_drops is only valid for field %q", UnitFieldItemDrops)
	}
	if p.Value == nil {
		return fmt.Errorf("value is required for field %q", p.Field)
	}
	switch p.Field {
	case UnitFieldGold, UnitFieldPlayer, UnitFieldHeroLevel, UnitFieldHeroStr, UnitFieldHeroAgi, UnitFieldHeroInt:
		_, err := toUint32(p.Field, *p.Value)
		return err
	case UnitFieldHPPct, UnitFieldManaPct, UnitFieldTargetAcquisition, UnitFieldCustomColor:
		return nil
	default:
		return fmt.Errorf("unknown unit instance field %q", p.Field)
	}
}

func preflightMapInfoUpdates(updates map[string]any) error {
	if len(updates) == 0 {
		return fmt.Errorf("updates must not be empty")
	}
	for k, v := range updates {
		switch k {
		case "name", "author", "description", "suggestedPlayers":
			if _, ok := v.(string); !ok {
				return fmt.Errorf("updates.%s must be a string", k)
			}
		case "lua":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("updates.lua must be a boolean")
			}
		default:
			return fmt.Errorf("unsupported map.info_set key %q", k)
		}
	}
	return nil
}

// Operation DTOs. Separate strict shapes keep accidental fields from silently
// landing on the wrong operation.
type patchUnitsMove struct {
	Op             string  `json:"op"`
	CreationNumber uint32  `json:"creation_number"`
	X              float32 `json:"x"`
	Y              float32 `json:"y"`
	Z              float32 `json:"z"`
}
type patchUnitsRotate struct {
	Op             string  `json:"op"`
	CreationNumber uint32  `json:"creation_number"`
	Rotation       float32 `json:"rotation"`
}
type patchScale struct {
	Op             string  `json:"op"`
	CreationNumber uint32  `json:"creation_number"`
	SX             float32 `json:"sx"`
	SY             float32 `json:"sy"`
	SZ             float32 `json:"sz"`
}
type patchUnitSetField struct {
	Op             string                  `json:"op"`
	CreationNumber uint32                  `json:"creation_number"`
	Field          string                  `json:"field"`
	Value          *float64                `json:"value,omitempty"`
	ItemDrops      *[]UnitInstanceItemDrop `json:"item_drops,omitempty"`
}
type patchUnitCreate struct {
	Op       string  `json:"op"`
	TypeID   string  `json:"type_id"`
	Player   uint32  `json:"player"`
	X        float32 `json:"x"`
	Y        float32 `json:"y"`
	Z        float32 `json:"z"`
	Rotation float32 `json:"rotation,omitempty"`
	Scale    float32 `json:"scale,omitempty"`
}
type patchEntityDelete struct {
	Op             string `json:"op"`
	CreationNumber uint32 `json:"creation_number"`
}
type patchDoodadMove struct {
	Op             string  `json:"op"`
	CreationNumber uint32  `json:"creation_number"`
	X              float32 `json:"x"`
	Y              float32 `json:"y"`
	Z              float32 `json:"z"`
}
type patchDoodadRotate struct {
	Op             string  `json:"op"`
	CreationNumber uint32  `json:"creation_number"`
	Rotation       float32 `json:"rotation"`
}
type patchDoodadCreate struct {
	Op        string  `json:"op"`
	TypeID    string  `json:"type_id"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
	Z         float32 `json:"z"`
	Rotation  float32 `json:"rotation,omitempty"`
	Scale     float32 `json:"scale,omitempty"`
	Variation uint32  `json:"variation,omitempty"`
}
type patchRegionCreate struct {
	Op        string  `json:"op"`
	Name      string  `json:"name"`
	MinX      float32 `json:"min_x"`
	MinY      float32 `json:"min_y"`
	MaxX      float32 `json:"max_x"`
	MaxY      float32 `json:"max_y"`
	WeatherID string  `json:"weather_id,omitempty"`
	AmbientID string  `json:"ambient_id,omitempty"`
	Color     []int   `json:"color,omitempty"`
}
type patchRegionMove struct {
	Op             string  `json:"op"`
	CreationNumber int32   `json:"creation_number"`
	DX             float32 `json:"dx"`
	DY             float32 `json:"dy"`
}
type patchRegionResize struct {
	Op             string  `json:"op"`
	CreationNumber int32   `json:"creation_number"`
	MinX           float32 `json:"min_x"`
	MinY           float32 `json:"min_y"`
	MaxX           float32 `json:"max_x"`
	MaxY           float32 `json:"max_y"`
}
type patchRegionRename struct {
	Op             string `json:"op"`
	CreationNumber int32  `json:"creation_number"`
	Name           string `json:"name"`
}
type patchRegionDelete struct {
	Op             string `json:"op"`
	CreationNumber int32  `json:"creation_number"`
}
type patchTerrainSetTile struct {
	Op           string `json:"op"`
	Col          int    `json:"col"`
	Row          int    `json:"row"`
	GroundTileID string `json:"ground_tile_id"`
}
type patchTerrainSetHeight struct {
	Op     string  `json:"op"`
	Col    int     `json:"col"`
	Row    int     `json:"row"`
	Height float32 `json:"height"`
}
type patchTerrainPaint struct {
	Op           string  `json:"op"`
	Col          int     `json:"col"`
	Row          int     `json:"row"`
	Radius       float64 `json:"radius"`
	Shape        string  `json:"shape,omitempty"`
	GroundTileID string  `json:"ground_tile_id"`
}
type patchTerrainHeightBrush struct {
	Op       string  `json:"op"`
	Col      int     `json:"col"`
	Row      int     `json:"row"`
	Radius   float64 `json:"radius"`
	Shape    string  `json:"shape,omitempty"`
	Mode     string  `json:"mode"`
	Strength float32 `json:"strength,omitempty"`
	Target   float32 `json:"target,omitempty"`
}
type patchMapInfoSet struct {
	Op      string         `json:"op"`
	Updates map[string]any `json:"updates"`
}
