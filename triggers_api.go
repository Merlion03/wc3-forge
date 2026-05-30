package main

import (
	"errors"
	"fmt"

	"github.com/StephenSHorton/wc3-forge/internal/forge"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wtg"
)

// Trigger Editor Wails surface (Phase 1a — read-only). Three methods,
// matching the MCP shape under "triggers.*":
//
//   App.ListTriggerTree         — full tree hierarchy in one call
//   App.GetTrigger(id)          — full detail for one node (ECAs + custom_text)
//   App.GetTriggerFunctionsMeta — TriggerData.txt vocabulary (cached for the
//                                 session; static for the process lifetime)
//
// JSON DTOs are aliased from forge package types so Wails' binding generator
// (which doesn't follow type aliases across packages cleanly) sees concrete
// shapes locally.

// TriggerTreeNodeDTO is the per-node row in ListTriggerTree's response.
// Mirrors forge.TriggerTreeNode 1:1; reproduced here so Wails picks up the
// struct in the main package's binding output.
type TriggerTreeNodeDTO struct {
	ID                  int32  `json:"id"`
	ParentID            int32  `json:"parent_id"`
	Kind                string `json:"kind"`
	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	IsComment           bool   `json:"is_comment,omitempty"`
	IsEnabled           bool   `json:"is_enabled,omitempty"`
	IsScript            bool   `json:"is_script,omitempty"`
	InitiallyOn         bool   `json:"initially_on,omitempty"`
	RunOnInitialization bool   `json:"run_on_initialization,omitempty"`
	OpenState           bool   `json:"open_state,omitempty"`
}

// TriggerTreeDTO mirrors forge.TriggerTreeResponse. Wails-bound version so
// the frontend gets a typed shape.
type TriggerTreeDTO struct {
	Nodes         []TriggerTreeNodeDTO `json:"nodes"`
	IsPre131      bool                 `json:"is_pre_131,omitempty"`
	HasGlobalJASS bool                 `json:"has_global_jass,omitempty"`
}

// ListTriggerTree returns the loaded map's full trigger hierarchy. Empty
// (not error) when no map is loaded or the map has no triggers (and isn't a
// hand-rolled-script map).
func (a *App) ListTriggerTree() TriggerTreeDTO {
	t := forge.Current.Triggers()
	if t == nil {
		return TriggerTreeDTO{Nodes: []TriggerTreeNodeDTO{}}
	}
	out := TriggerTreeDTO{
		Nodes:    make([]TriggerTreeNodeDTO, 0, len(t.Categories)+len(t.Triggers)+len(t.Variables)),
		IsPre131: t.IsPre131,
	}
	if wctf := forge.Current.TriggersScripts(); wctf != nil && wctf.GlobalJASS != "" {
		out.HasGlobalJASS = true
	}
	for _, c := range t.Categories {
		out.Nodes = append(out.Nodes, TriggerTreeNodeDTO{
			ID: c.ID, ParentID: c.ParentID,
			Kind:      classifierKindMain(c.Classifier, false, false),
			Name:      c.Name,
			IsComment: c.IsComment,
			OpenState: c.OpenState,
		})
	}
	for _, tr := range t.Triggers {
		out.Nodes = append(out.Nodes, TriggerTreeNodeDTO{
			ID: tr.ID, ParentID: tr.ParentID,
			Kind:                triggerKindMain(tr),
			Name:                tr.Name,
			Description:         tr.Description,
			IsComment:           tr.IsComment,
			IsEnabled:           tr.IsEnabled,
			IsScript:            tr.IsScript,
			InitiallyOn:         tr.InitiallyOn,
			RunOnInitialization: tr.RunOnInitialization,
		})
	}
	for _, v := range t.Variables {
		out.Nodes = append(out.Nodes, TriggerTreeNodeDTO{
			ID: v.ID, ParentID: v.ParentID,
			Kind: "variable",
			Name: v.Name,
		})
	}
	return out
}

// TriggerDetailDTO mirrors forge.TriggerDetail. Wails-friendly shape.
type TriggerDetailDTO struct {
	Kind     string        `json:"kind"`
	Category *wtg.Category `json:"category,omitempty"`
	Trigger  *wtg.Trigger  `json:"trigger,omitempty"`
	Variable *wtg.Variable `json:"variable,omitempty"`
}

// GetTrigger returns the full record for the given id (category / trigger /
// variable; the JS side branches on Kind). Errors out when no map is loaded
// or the id isn't present.
func (a *App) GetTrigger(id int32) (*TriggerDetailDTO, error) {
	t := forge.Current.Triggers()
	if t == nil {
		return nil, fmt.Errorf("no map loaded (or map has no triggers)")
	}
	for i := range t.Categories {
		if t.Categories[i].ID == id {
			return &TriggerDetailDTO{Kind: "category", Category: &t.Categories[i]}, nil
		}
	}
	for i := range t.Triggers {
		if t.Triggers[i].ID == id {
			// Match MCP triggers.get: enrich Parameter.ResolvedDisplay with
			// resolved unit/destructible names. Deep-copies the trigger so
			// the cached struct stays byte-faithful for Phase 2a's encoder.
			enriched := forge.EnrichTriggerWithResolvedNames(forge.Current, &t.Triggers[i])
			return &TriggerDetailDTO{Kind: triggerKindMain(t.Triggers[i]), Trigger: enriched}, nil
		}
	}
	for i := range t.Variables {
		if t.Variables[i].ID == id {
			return &TriggerDetailDTO{Kind: "variable", Variable: &t.Variables[i]}, nil
		}
	}
	return nil, fmt.Errorf("no trigger node with id %d", id)
}

// TriggerFunctionMetaDTO mirrors forge.TriggerFunctionMeta. One entry per
// function family ([TriggerEvents] / [TriggerConditions] / [TriggerActions] /
// [TriggerCalls]).
type TriggerFunctionMetaDTO struct {
	Name               string   `json:"name"`
	Section            string   `json:"section"`
	Argc               int      `json:"argc"`
	ArgTypes           []string `json:"arg_types"`
	ReturnType         string   `json:"return_type,omitempty"`
	DisplayName        string   `json:"display_name,omitempty"`
	ParametersTemplate []string `json:"parameters_template,omitempty"`
	Defaults           []string `json:"defaults,omitempty"`
	Limits             []string `json:"limits,omitempty"`
	Category           string   `json:"category,omitempty"`
	ScriptName         string   `json:"script_name,omitempty"`
	Hint               string   `json:"hint,omitempty"`
}

// TriggerPresetMetaDTO mirrors forge.TriggerPresetMeta — one [TriggerParams]
// row exposed to the frontend's ParamEditor for preset/enum dropdowns.
type TriggerPresetMetaDTO struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Value       string `json:"value"`
	DisplayName string `json:"display_name"`
}

// TriggerTypeMetaDTO mirrors forge.TriggerTypeMeta — one [TriggerTypes] row
// with the BaseType alias chain + per-type capability flags the frontend
// uses to decide which editor variant to show for a param.
type TriggerTypeMetaDTO struct {
	Name        string `json:"name"`
	BaseType    string `json:"base_type,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	CanBeGlobal bool   `json:"can_be_global,omitempty"`
	CanCompare  bool   `json:"can_compare,omitempty"`
}

// TriggerFunctionsMetaDTO is the full TriggerData.txt vocabulary the frontend
// needs to render labels client-side. Cached for the process lifetime.
type TriggerFunctionsMetaDTO struct {
	Functions  []TriggerFunctionMetaDTO `json:"functions"`
	Categories map[string]string        `json:"categories,omitempty"`
	Types      map[string]string        `json:"types,omitempty"`
	Presets    []TriggerPresetMetaDTO   `json:"presets,omitempty"`
	TypeMeta   []TriggerTypeMetaDTO     `json:"type_meta,omitempty"`
}

// GetTriggerFunctionsMeta returns the cached TriggerData.txt vocabulary.
// Static for the process lifetime; the frontend can stash + memoize.
func (a *App) GetTriggerFunctionsMeta() *TriggerFunctionsMetaDTO {
	resp := forge.GetTriggerFunctionsMetaCached()
	if resp == nil {
		return nil
	}
	out := &TriggerFunctionsMetaDTO{
		Functions:  make([]TriggerFunctionMetaDTO, 0, len(resp.Functions)),
		Categories: resp.Categories,
		Types:      resp.Types,
	}
	for _, f := range resp.Functions {
		out.Functions = append(out.Functions, TriggerFunctionMetaDTO{
			Name:               f.Name,
			Section:            f.Section,
			Argc:               f.Argc,
			ArgTypes:           f.ArgTypes,
			ReturnType:         f.ReturnType,
			DisplayName:        f.DisplayName,
			ParametersTemplate: f.ParametersTemplate,
			Defaults:           f.Defaults,
			Limits:             f.Limits,
			Category:           f.Category,
			ScriptName:         f.ScriptName,
			Hint:               f.Hint,
		})
	}
	for _, p := range resp.Presets {
		out.Presets = append(out.Presets, TriggerPresetMetaDTO{
			Name: p.Name, Type: p.Type, Value: p.Value, DisplayName: p.DisplayName,
		})
	}
	for _, tm := range resp.TypeMeta {
		out.TypeMeta = append(out.TypeMeta, TriggerTypeMetaDTO{
			Name:        tm.Name,
			BaseType:    tm.BaseType,
			DisplayName: tm.DisplayName,
			CanBeGlobal: tm.CanBeGlobal,
			CanCompare:  tm.CanCompare,
		})
	}
	return out
}

// classifierKindMain mirrors forge.classifierToKind. Duplicated here to keep
// app.go independent of internal/forge's package-private helpers (the same
// pattern resolveDisplay uses).
func classifierKindMain(c wtg.Classifier, isComment, isScript bool) string {
	switch c {
	case wtg.ClassifierMap:
		return "map"
	case wtg.ClassifierLibrary:
		return "library"
	case wtg.ClassifierCategory:
		return "category"
	case wtg.ClassifierGUI:
		if isComment {
			return "trigger_comment"
		}
		if isScript {
			return "trigger_script"
		}
		return "trigger_gui"
	case wtg.ClassifierComment:
		return "trigger_comment"
	case wtg.ClassifierScript:
		return "trigger_script"
	case wtg.ClassifierVariable:
		return "variable"
	}
	return "unknown"
}

// triggerKindMain mirrors forge.triggerKind for the same DTO reason.
func triggerKindMain(tr wtg.Trigger) string {
	if tr.Classifier == wtg.ClassifierComment {
		return "trigger_comment"
	}
	if tr.Classifier == wtg.ClassifierScript {
		return "trigger_script"
	}
	if tr.IsComment {
		return "trigger_comment"
	}
	if tr.IsScript {
		return "trigger_script"
	}
	return "trigger_gui"
}

// ---------------------------------------------------------------------------
// Phase 2a — structural mutation Wails surface. Each method dispatches to
// the matching Session mutator. Errors propagate to the JS caller via the
// usual Wails error channel.
//
// Return type for every mutation is TriggerMutationResultDTO — the updated
// tree + (when applicable) the affected node's detail. The JS caller can
// repaint without a follow-up fetch.
// ---------------------------------------------------------------------------

// TriggerMutationResultDTO mirrors forge.triggerMutationResponse. NewID is
// the affected node's id (or 0 for delete / map-header); Detail is the full
// triggers.get payload for that node (nil for deleted nodes).
type TriggerMutationResultDTO struct {
	Tree   TriggerTreeDTO    `json:"tree"`
	NewID  int32             `json:"new_id,omitempty"`
	Detail *TriggerDetailDTO `json:"detail,omitempty"`
}

// buildTriggerMutationResult re-fetches the tree + detail post-mutation,
// duplicating the bridge's buildMutationResponse but with the main-package
// DTO shapes so Wails picks them up cleanly.
func (a *App) buildTriggerMutationResult(affectedID int32) TriggerMutationResultDTO {
	resp := TriggerMutationResultDTO{NewID: affectedID, Tree: a.ListTriggerTree()}
	if affectedID != 0 {
		if d, err := a.GetTrigger(affectedID); err == nil {
			resp.Detail = d
		}
	}
	return resp
}

// AddTriggerCategory appends a category and returns the updated tree.
func (a *App) AddTriggerCategory(name string, parentID int32) (TriggerMutationResultDTO, error) {
	id, err := forge.Current.AddTriggerCategory(name, parentID)
	if err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// AddGUITrigger appends a GUI trigger.
func (a *App) AddGUITrigger(name string, parentID int32) (TriggerMutationResultDTO, error) {
	id, err := forge.Current.AddGUITrigger(name, parentID)
	if err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// AddScriptTrigger appends a custom-script trigger.
func (a *App) AddScriptTrigger(name string, parentID int32) (TriggerMutationResultDTO, error) {
	id, err := forge.Current.AddScriptTrigger(name, parentID)
	if err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// AddCommentTrigger appends a comment-only trigger.
func (a *App) AddCommentTrigger(name string, parentID int32) (TriggerMutationResultDTO, error) {
	id, err := forge.Current.AddCommentTrigger(name, parentID)
	if err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// AddTriggerVariable appends a global trigger variable.
func (a *App) AddTriggerVariable(name, varType string, isArray bool, arraySize int32, initValue string) (TriggerMutationResultDTO, error) {
	id, err := forge.Current.AddTriggerVariable(name, varType, isArray, arraySize, initValue)
	if err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// DeleteTriggerNode removes a node by id (category re-parents children to root).
func (a *App) DeleteTriggerNode(id int32) (TriggerMutationResultDTO, error) {
	if err := forge.Current.DeleteTriggerNode(id); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(0), nil
}

// RenameTriggerNode updates a node's name.
func (a *App) RenameTriggerNode(id int32, name string) (TriggerMutationResultDTO, error) {
	if err := forge.Current.RenameTriggerNode(id, name); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// SetTriggerEnabled toggles is_enabled.
func (a *App) SetTriggerEnabled(id int32, enabled bool) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetTriggerEnabled(id, enabled); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// SetTriggerInitiallyOn toggles initially_on.
func (a *App) SetTriggerInitiallyOn(id int32, initiallyOn bool) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetTriggerInitiallyOn(id, initiallyOn); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// SetTriggerRunOnInit toggles run_on_initialization.
func (a *App) SetTriggerRunOnInit(id int32, runOnInit bool) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetTriggerRunOnInit(id, runOnInit); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// MoveTriggerNode re-parents a node. newParentID must point at a
// category-like node (map/library/category).
func (a *App) MoveTriggerNode(id, newParentID int32) (TriggerMutationResultDTO, error) {
	if err := forge.Current.MoveTriggerNode(id, newParentID); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// SetTriggerCustomText updates a script trigger's custom_text. Also handles
// the synthetic Map Header script entry in hand-rolled-script maps.
func (a *App) SetTriggerCustomText(id int32, text string) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetTriggerCustomText(id, text); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// SetTriggerDescription updates a comment/trigger description.
func (a *App) SetTriggerDescription(id int32, text string) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetTriggerDescription(id, text); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// SetTriggerVariable updates a variable's full field set (5-field form).
func (a *App) SetTriggerVariable(id int32, name, varType string, isArray bool, arraySize int32, initValue string) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetTriggerVariable(id, name, varType, isArray, arraySize, initValue); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(id), nil
}

// SetMapHeaderScript replaces the synthesized Map Header trigger's
// custom_text for hand-rolled-script maps. Convenience over
// SetTriggerCustomText.
func (a *App) SetMapHeaderScript(content string) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetMapHeaderScript(content); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(0), nil
}

// ---------------------------------------------------------------------------
// Phase 2b1 — ECA / param mutation Wails wrappers. The picker / param-editor
// frontend components call these; each is a thin dispatch into a Session
// mutator and returns the standard mutation-result DTO so the dialog can
// repaint without a follow-up fetch.
// ---------------------------------------------------------------------------

// AddTriggerECA appends or inserts a new ECA. ecaType is an int because Wails
// doesn't preserve type-aliased uint32s cleanly across the JS boundary; the
// frontend uses 0/1/2/3 for event/condition/action/call. position < 0 means
// "append".
func (a *App) AddTriggerECA(triggerID int32, ecaType int, name string, position int) (TriggerMutationResultDTO, error) {
	if _, err := forge.Current.AddECA(triggerID, wtg.ECAType(ecaType), name, position); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(triggerID), nil
}

// DeleteTriggerECA removes the ECA at ecaPath (length-1 for top-level).
func (a *App) DeleteTriggerECA(triggerID int32, ecaPath []int) (TriggerMutationResultDTO, error) {
	if err := forge.Current.DeleteECA(triggerID, ecaPath); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(triggerID), nil
}

// MoveTriggerECA reorders the ECA at ecaPath within its parent slice to
// newPosition (post-removal target index).
func (a *App) MoveTriggerECA(triggerID int32, ecaPath []int, newPosition int) (TriggerMutationResultDTO, error) {
	if err := forge.Current.MoveECA(triggerID, ecaPath, newPosition); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(triggerID), nil
}

// SetTriggerECAEnabled toggles the per-ECA Enabled flag.
func (a *App) SetTriggerECAEnabled(triggerID int32, ecaPath []int, enabled bool) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetECAEnabled(triggerID, ecaPath, enabled); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(triggerID), nil
}

// SetTriggerParamValue writes one Parameter slot of an existing ECA. paramType
// is an int (0=preset, 1=variable, 2=function, 3=string).
//
// Phase 2b2: signature takes paramPath []int (replaces 2b1's paramIndex int).
// paramPath addresses sub-parameter chains via the recursive .SubParameter
// .Parameters walk — paramPath[0] indexes the leaf ECA's Parameters, each
// subsequent element drills into one level of SubParameter.
func (a *App) SetTriggerParamValue(triggerID int32, ecaPath []int, paramPath []int, value string, paramType int) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetParamValue(triggerID, ecaPath, paramPath, value, wtg.ParamType(paramType)); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(triggerID), nil
}

// SetTriggerParamSubFunction replaces the parameter at (triggerID, ecaPath,
// paramPath) with a sub-function call to subName. The sub-function's parameters
// are seeded from TriggerData defaults.
func (a *App) SetTriggerParamSubFunction(triggerID int32, ecaPath []int, paramPath []int, subName string) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetParamSubFunction(triggerID, ecaPath, paramPath, subName); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(triggerID), nil
}

// ClearTriggerParamSubFunction collapses a sub-function back to an empty
// literal preset value. Inverse of SetTriggerParamSubFunction.
func (a *App) ClearTriggerParamSubFunction(triggerID int32, ecaPath []int, paramPath []int) (TriggerMutationResultDTO, error) {
	if err := forge.Current.ClearParamSubFunction(triggerID, ecaPath, paramPath); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(triggerID), nil
}

// SetTriggerParamArray toggles array-mode on the parameter. When enabling,
// initializes ArrayIndex to a default preset Parameter.
func (a *App) SetTriggerParamArray(triggerID int32, ecaPath []int, paramPath []int, isArray bool) (TriggerMutationResultDTO, error) {
	if err := forge.Current.SetParamArray(triggerID, ecaPath, paramPath, isArray); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(triggerID), nil
}

// AddTriggerNestedECA appends a child ECA inside a magic-ECA parent (e.g. an
// IfThenElseMultiple's Then-actions). groupID maps to ECA.Group; the magic
// parent's renderer keys on group to bucket children.
func (a *App) AddTriggerNestedECA(triggerID int32, parentPath []int, ecaType int, name string, groupID uint32) (TriggerMutationResultDTO, error) {
	if _, err := forge.Current.AddNestedECA(triggerID, parentPath, wtg.ECAType(ecaType), name, groupID); err != nil {
		return TriggerMutationResultDTO{}, err
	}
	return a.buildTriggerMutationResult(triggerID), nil
}

// ---------------------------------------------------------------------------
// Phase 2b2 — entity instance picker Wails wrappers. Surface live-map data
// (unit/destructable instances by creation_number, named regions, named
// cameras) to the recursive ParamEditor's entity-typed branches.
// ---------------------------------------------------------------------------

// TriggerUnitInstanceDTO mirrors forge.TriggerUnitInstance — one placed unit
// row exposed to the unit-instance picker.
type TriggerUnitInstanceDTO struct {
	CreationNumber uint32  `json:"creation_number"`
	TypeID         string  `json:"type_id"`
	Player         uint32  `json:"player"`
	X              float32 `json:"x"`
	Y              float32 `json:"y"`
	Name           string  `json:"name"`
	GGRef          string  `json:"gg_ref"`
}

// ListTriggerUnitInstances returns every placed unit in the loaded map with
// its creation_number-keyed gg_unit_*_NNNN ref. Empty (not error) when no map
// is loaded or the map has no units.
func (a *App) ListTriggerUnitInstances() []TriggerUnitInstanceDTO {
	src := forge.BuildTriggerUnitInstances()
	out := make([]TriggerUnitInstanceDTO, 0, len(src))
	for _, r := range src {
		out = append(out, TriggerUnitInstanceDTO{
			CreationNumber: r.CreationNumber,
			TypeID:         r.TypeID,
			Player:         r.Player,
			X:              r.X,
			Y:              r.Y,
			Name:           r.Name,
			GGRef:          r.GGRef,
		})
	}
	return out
}

// TriggerDestructableInstanceDTO mirrors forge.TriggerDestructableInstance.
type TriggerDestructableInstanceDTO struct {
	CreationNumber uint32  `json:"creation_number"`
	TypeID         string  `json:"type_id"`
	X              float32 `json:"x"`
	Y              float32 `json:"y"`
	Name           string  `json:"name"`
	GGRef          string  `json:"gg_ref"`
}

// ListTriggerDestructableInstances returns every placed destructable.
func (a *App) ListTriggerDestructableInstances() []TriggerDestructableInstanceDTO {
	src := forge.BuildTriggerDestructableInstances()
	out := make([]TriggerDestructableInstanceDTO, 0, len(src))
	for _, r := range src {
		out = append(out, TriggerDestructableInstanceDTO{
			CreationNumber: r.CreationNumber,
			TypeID:         r.TypeID,
			X:              r.X,
			Y:              r.Y,
			Name:           r.Name,
			GGRef:          r.GGRef,
		})
	}
	return out
}

// TriggerRegionInfoDTO mirrors forge.TriggerRegionInfo.
type TriggerRegionInfoDTO struct {
	Name           string  `json:"name"`
	CreationNumber int32   `json:"creation_number"`
	Left           float32 `json:"left"`
	Right          float32 `json:"right"`
	Top            float32 `json:"top"`
	Bottom         float32 `json:"bottom"`
	GGRef          string  `json:"gg_ref"`
}

// ListTriggerRegions returns the war3map.w3r contents (or empty when absent).
func (a *App) ListTriggerRegions() []TriggerRegionInfoDTO {
	src := forge.BuildTriggerRegions()
	out := make([]TriggerRegionInfoDTO, 0, len(src))
	for _, r := range src {
		out = append(out, TriggerRegionInfoDTO{
			Name:           r.Name,
			CreationNumber: r.CreationNumber,
			Left:           r.Left,
			Right:          r.Right,
			Top:            r.Top,
			Bottom:         r.Bottom,
			GGRef:          r.GGRef,
		})
	}
	return out
}

// TriggerCameraInfoDTO mirrors forge.TriggerCameraInfo.
type TriggerCameraInfoDTO struct {
	Name     string  `json:"name"`
	TargetX  float32 `json:"target_x"`
	TargetY  float32 `json:"target_y"`
	Distance float32 `json:"distance"`
	GGRef    string  `json:"gg_ref"`
}

// ---------------------------------------------------------------------------
// Phase 3 — Lua codegen + Test Map + search Wails surface.
// ---------------------------------------------------------------------------

// TriggerScriptResultDTO mirrors forge.triggerScriptResult.
type TriggerScriptResultDTO struct {
	Text  string `json:"text,omitempty"`
	Bytes int    `json:"bytes,omitempty"`
}

// GenerateTriggerScript returns the Lua war3map.lua source for the loaded
// map without writing. Errors propagate verbatim (ErrLuaOnly for JASS
// maps; ErrPreserveScript when the existing war3map.lua starts with the
// preserve marker; "no map loaded" otherwise).
func (a *App) GenerateTriggerScript() (TriggerScriptResultDTO, error) {
	text, err := forge.Current.GenerateTriggerScript()
	if err != nil {
		return TriggerScriptResultDTO{}, err
	}
	return TriggerScriptResultDTO{Text: text, Bytes: len(text)}, nil
}

// SaveTriggerScript generates + writes war3map.lua into the loaded map's
// source. Returns the byte count.
//
// Non-destructive: if the existing war3map.lua was not produced by wc3-forge's
// codegen, the original is backed up to war3map.lua.bak before regeneration so
// a hand-authored script is never silently destroyed (the live-confirmed
// save_script clobber bug). The GUI button is an explicit user action, so it
// proceeds (overwrite=true) rather than blocking — but the backup guarantees
// the original bytes remain recoverable.
func (a *App) SaveTriggerScript() (TriggerScriptResultDTO, error) {
	n, err := forge.Current.SaveTriggerScriptGuarded(true)
	if err != nil {
		return TriggerScriptResultDTO{}, err
	}
	return TriggerScriptResultDTO{Bytes: n}, nil
}

// TestMap runs the full pipeline: save pending edits → regen war3map.lua →
// launch WC3 with the map preloaded. Returns nil on success; errors from
// any step propagate so the JS layer can toast appropriately.
func (a *App) TestMap() error {
	return forge.Current.TestMap()
}

// ConvertBlockerDTO mirrors forge.ConvertBlocker for the Wails boundary so
// the binding generator picks it up in the main package.
type ConvertBlockerDTO struct {
	TriggerID   int32  `json:"trigger_id"`
	TriggerName string `json:"trigger_name"`
	Kind        string `json:"kind"`
	Reason      string `json:"reason"`
}

// ConvertToLuaResultDTO mirrors forge.ConvertToLuaCheckResult. Blockers is
// non-nil (possibly empty) — JS callers should branch on length.
type ConvertToLuaResultDTO struct {
	Blockers []ConvertBlockerDTO `json:"blockers"`
}

// CheckConvertToLua scans the loaded map for unconvertable JASS content.
// Returns a result with empty Blockers when conversion is safe; errors only
// when no map is loaded. The UI uses this to gate the confirm dialog vs.
// the blocker dialog.
func (a *App) CheckConvertToLua() (*ConvertToLuaResultDTO, error) {
	res, err := forge.Current.CheckConvertToLua()
	if err != nil {
		return nil, err
	}
	return convertResultToDTO(res), nil
}

// ConvertMapToLua runs the full convert flow with backup=true (the historical
// default). New UIs should call ConvertMapToLuaWithOptions instead. Returns
// the blocker list (instead of erroring) when blockers are found — same shape
// as Check, letting the JS caller route to the blocker dialog without a
// separate error-message-parse path. Other failure modes (ErrAlreadyLua,
// codegen errors, disk errors) propagate as Go errors.
func (a *App) ConvertMapToLua() (*ConvertToLuaResultDTO, error) {
	return a.ConvertMapToLuaWithOptions(true)
}

// ConvertMapToLuaWithOptions is the Phase-3.5 convert-flow entry. `backup`
// is the only knob today; defaults are baked into ConvertToLuaOptions on the
// Go side.
func (a *App) ConvertMapToLuaWithOptions(backup bool) (*ConvertToLuaResultDTO, error) {
	return a.ConvertMapToLuaWithEdits(backup, nil)
}

// SectionOverride is one user-edited section from the convert dialog: the
// section's ID (a trigger ID, or -1 for the GlobalJASS header) and the Lua the
// user hand-fixed. Passed as a slice rather than a map so the JSON wire shape is
// unambiguous about the int32 keys.
type SectionOverride struct {
	ID  int32  `json:"id"`
	Lua string `json:"lua"`
}

// ConvertMapToLuaWithEdits is the convert-flow entry that honors per-section
// user edits. `overrides` carries sections the user hand-fixed in the review
// dialog; for each, the supplied Lua is written verbatim instead of the
// transpiler's output. Pass nil/empty for a plain auto-conversion.
func (a *App) ConvertMapToLuaWithEdits(backup bool, overrides []SectionOverride) (*ConvertToLuaResultDTO, error) {
	var ovMap map[int32]string
	if len(overrides) > 0 {
		ovMap = make(map[int32]string, len(overrides))
		for _, o := range overrides {
			ovMap[o.ID] = o.Lua
		}
	}
	res, err := forge.Current.ConvertToLuaWithOptions(forge.ConvertToLuaOptions{Backup: backup, Overrides: ovMap})
	if err != nil {
		if errors.Is(err, forge.ErrConvertBlocked) {
			return convertResultToDTO(res), nil
		}
		return nil, err
	}
	return convertResultToDTO(res), nil
}

// TranspileSectionDTO mirrors forge.TranspileSection — one diff entry the
// review dialog renders. PreprocessWarnings carries non-fatal textmacro
// preprocessor diagnostics; the UI surfaces them as a small warning bar
// above the diff editor.
type TranspileSectionDTO struct {
	ID                 int32    `json:"id"`
	Label              string   `json:"label"`
	Kind               string   `json:"kind"`
	Original           string             `json:"original"`
	Transpiled         string             `json:"transpiled"`
	Diagnostics        []forge.Diagnostic `json:"diagnostics,omitempty"`
	Errors             []string           `json:"errors,omitempty"`
	PreprocessWarnings []string           `json:"preprocess_warnings,omitempty"`
}

// TranspilePreviewDTO mirrors forge.TranspilePreview.
type TranspilePreviewDTO struct {
	Sections []TranspileSectionDTO `json:"sections"`
}

// GetTranspilePreview returns the read-only diff payload for the Convert-to-Lua
// review dialog. The whole preview is rebuilt per call (the dialog opens
// rarely; cache invalidation isn't worth the complexity).
func (a *App) GetTranspilePreview() (*TranspilePreviewDTO, error) {
	preview, err := forge.Current.TranspilePreview()
	if err != nil {
		return nil, err
	}
	out := &TranspilePreviewDTO{Sections: make([]TranspileSectionDTO, 0, len(preview.Sections))}
	for _, s := range preview.Sections {
		out.Sections = append(out.Sections, TranspileSectionDTO{
			ID:                 s.ID,
			Label:              s.Label,
			Kind:               s.Kind,
			Original:           s.Original,
			Transpiled:         s.Transpiled,
			Diagnostics:        s.Diagnostics,
			Errors:             s.Errors,
			PreprocessWarnings: s.PreprocessWarnings,
		})
	}
	return out, nil
}

// convertResultToDTO copies forge.ConvertToLuaCheckResult into the main-
// package DTO so Wails picks up the shape. nil-in / nil-out, but always
// returns a non-nil Blockers slice so JS .length doesn't blow up on null.
func convertResultToDTO(in *forge.ConvertToLuaCheckResult) *ConvertToLuaResultDTO {
	if in == nil {
		return &ConvertToLuaResultDTO{Blockers: []ConvertBlockerDTO{}}
	}
	out := &ConvertToLuaResultDTO{Blockers: make([]ConvertBlockerDTO, 0, len(in.Blockers))}
	for _, b := range in.Blockers {
		out.Blockers = append(out.Blockers, ConvertBlockerDTO{
			TriggerID:   b.TriggerID,
			TriggerName: b.TriggerName,
			Kind:        b.Kind,
			Reason:      b.Reason,
		})
	}
	return out
}

// TriggerSearchHitDTO mirrors forge.TriggerSearchHit.
type TriggerSearchHitDTO struct {
	TriggerID   int32  `json:"trigger_id"`
	TriggerName string `json:"trigger_name"`
	Kind        string `json:"kind"`
	Path        []int  `json:"path,omitempty"`
	ECAName     string `json:"eca_name,omitempty"`
	Snippet     string `json:"snippet"`
	Category    string `json:"category,omitempty"`
}

// SearchTriggers performs the cross-trigger fuzzy search. Empty query
// returns up to `limit` trigger names alphabetically (so the palette feels
// populated on first open). Limit defaults to 50 when ≤ 0.
func (a *App) SearchTriggers(query string, limit int) []TriggerSearchHitDTO {
	if limit <= 0 {
		limit = 50
	}
	hits := forge.Current.SearchTriggers(query, limit)
	out := make([]TriggerSearchHitDTO, 0, len(hits))
	for _, h := range hits {
		out = append(out, TriggerSearchHitDTO{
			TriggerID:   h.TriggerID,
			TriggerName: h.TriggerName,
			Kind:        h.Kind,
			Path:        h.Path,
			ECAName:     h.ECAName,
			Snippet:     h.Snippet,
			Category:    h.Category,
		})
	}
	return out
}

// ListTriggerCameras returns the war3map.w3c contents (or empty when absent).
func (a *App) ListTriggerCameras() []TriggerCameraInfoDTO {
	src := forge.BuildTriggerCameras()
	out := make([]TriggerCameraInfoDTO, 0, len(src))
	for _, r := range src {
		out = append(out, TriggerCameraInfoDTO{
			Name:     r.Name,
			TargetX:  r.TargetX,
			TargetY:  r.TargetY,
			Distance: r.Distance,
			GGRef:    r.GGRef,
		})
	}
	return out
}
