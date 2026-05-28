package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/StephenSHorton/wc3-forge/internal/bridge"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wct"
	"github.com/StephenSHorton/wc3-forge/internal/formats/wtg"
)

// Trigger Editor MCP surface (Phase 1a — read-only).
//
// Three handlers ship in 1a:
//
//   triggers.tree            — full tree of categories+triggers+variables
//                              the frontend can render without further calls.
//   triggers.get             — single trigger, category, or variable detail
//                              (ECAs + custom_text included).
//   triggers.functions_meta  — TriggerData.txt vocabulary (events, conditions,
//                              actions, calls) for label templating client-side.
//
// All three are read-only; Phase 2a will add triggers.set_*, triggers.add_*,
// triggers.delete_*, triggers.rename, triggers.move.

// registerTriggerHandlers wires the trigger-editor MCP methods onto bridge
// via the `reg` closure RegisterAll passes in. Kept as a separate function
// (matching the pattern registerObjectKind set) so the wiring point stays
// scoped and a future search-by-handler-prefix yields exactly one register
// site per namespace.
func registerTriggerHandlers(reg func(method string, h bridge.Handler)) {
	reg("triggers.tree", handleTriggersTree)
	reg("triggers.get", handleTriggersGet)
	reg("triggers.functions_meta", handleTriggersFunctionsMeta)
	// Phase 2a — structural mutation surface. Each handler validates,
	// dispatches into the corresponding Session mutator, and returns the
	// updated tree + the affected node's get-payload so the JS layer
	// doesn't need a second round-trip after each mutation.
	reg("triggers.add_category", handleTriggersAddCategory)
	reg("triggers.add_gui", handleTriggersAddGUI)
	reg("triggers.add_script", handleTriggersAddScript)
	reg("triggers.add_comment", handleTriggersAddComment)
	reg("triggers.add_variable", handleTriggersAddVariable)
	reg("triggers.delete", handleTriggersDelete)
	reg("triggers.rename", handleTriggersRename)
	reg("triggers.set_enabled", handleTriggersSetEnabled)
	reg("triggers.set_initially_on", handleTriggersSetInitiallyOn)
	reg("triggers.set_run_on_init", handleTriggersSetRunOnInit)
	reg("triggers.move", handleTriggersMove)
	reg("triggers.set_custom_text", handleTriggersSetCustomText)
	reg("triggers.set_description", handleTriggersSetDescription)
	reg("triggers.set_variable", handleTriggersSetVariable)
	reg("triggers.set_map_header_script", handleTriggersSetMapHeaderScript)
}

// TriggerTreeNode is the one-shot tree-shape DTO. The frontend stitches
// nodes together via ParentID into a hierarchical view; we send a flat array
// to avoid the overhead of repeated network round-trips.
//
// Kind is a wire-stable discriminator that maps directly to the per-node UI:
//   - "category" / "map" / "library" — folder, may have children
//   - "trigger_gui"   — a normal trigger with ECAs
//   - "trigger_script" — a custom-script (JASS/Lua) trigger
//   - "trigger_comment" — a comment node
//   - "variable" — a global trigger variable (udg_*)
//
// Display-only fields (Icon hint, IsComment, IsEnabled, IsScript, etc.) are
// kept verbatim so the UI can reflect the exact toggles set in the .wtg.
type TriggerTreeNode struct {
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

// TriggerTreeResponse is the triggers.tree payload. Nodes are returned in
// the same order they were parsed from disk — the frontend keys off
// ParentID for hierarchy and uses array position for sibling order.
type TriggerTreeResponse struct {
	Nodes []TriggerTreeNode `json:"nodes"`
	// IsPre131 + HasGlobalJASS are advisory hints the UI uses to label the
	// map's trigger-format origin in the dialog header. Optional.
	IsPre131      bool `json:"is_pre_131,omitempty"`
	HasGlobalJASS bool `json:"has_global_jass,omitempty"`
}

// handleTriggersTree returns the full tree of categories/triggers/variables
// for the loaded map. Empty (but non-error) when no map is loaded or the map
// has no triggers; the frontend renders an empty-state message in that case.
func handleTriggersTree(_ json.RawMessage) (any, error) {
	t := Current.Triggers()
	if t == nil {
		return TriggerTreeResponse{Nodes: []TriggerTreeNode{}}, nil
	}
	out := make([]TriggerTreeNode, 0, len(t.Categories)+len(t.Triggers)+len(t.Variables))
	for _, c := range t.Categories {
		out = append(out, TriggerTreeNode{
			ID: c.ID, ParentID: c.ParentID,
			Kind:      classifierToKind(c.Classifier, false, false),
			Name:      c.Name,
			IsComment: c.IsComment,
			OpenState: c.OpenState,
		})
	}
	for _, tr := range t.Triggers {
		out = append(out, TriggerTreeNode{
			ID: tr.ID, ParentID: tr.ParentID,
			Kind:                triggerKind(tr),
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
		out = append(out, TriggerTreeNode{
			ID: v.ID, ParentID: v.ParentID,
			Kind: "variable",
			Name: v.Name,
		})
	}
	return TriggerTreeResponse{
		Nodes:         out,
		IsPre131:      t.IsPre131,
		HasGlobalJASS: Current.TriggersScripts() != nil && Current.TriggersScripts().GlobalJASS != "",
	}, nil
}

// classifierToKind maps a wtg.Classifier to the wire kind tag. For non-leaf
// nodes (map / library / category) this is the entire mapping; for triggers,
// triggerKind below handles the script/gui/comment branch.
func classifierToKind(c wtg.Classifier, isComment, isScript bool) string {
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

// triggerKind picks the per-trigger node kind. Branch on Classifier first;
// for ClassifierGUI we further break out IsComment / IsScript so the
// frontend's icon picker can show the right glyph (the same Classifier::gui
// hosts every flavor in older sub_versions).
func triggerKind(tr wtg.Trigger) string {
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

// triggersGetParams is the input DTO for triggers.get. We accept an int32 id
// and resolve it across categories/triggers/variables in one pass — the wtg
// guarantees ids are unique across all three kinds within a single map.
type triggersGetParams struct {
	ID int32 `json:"id"`
}

// TriggerDetail is the triggers.get payload. Exactly one of Category /
// Trigger / Variable is non-nil; Kind disambiguates.
type TriggerDetail struct {
	Kind     string         `json:"kind"`
	Category *wtg.Category  `json:"category,omitempty"`
	Trigger  *wtg.Trigger   `json:"trigger,omitempty"`
	Variable *wtg.Variable  `json:"variable,omitempty"`
}

// handleTriggersGet resolves an id to its full record. The Trigger payload
// includes ECAs (with nested children + sub-parameters) and custom_text in
// one shot — the dialog's right-pane render needs everything together.
func handleTriggersGet(params json.RawMessage) (any, error) {
	var p triggersGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	t := Current.Triggers()
	if t == nil {
		return nil, errors.New("no map loaded (or map has no triggers)")
	}
	for i := range t.Categories {
		if t.Categories[i].ID == p.ID {
			return TriggerDetail{Kind: "category", Category: &t.Categories[i]}, nil
		}
	}
	for i := range t.Triggers {
		if t.Triggers[i].ID == p.ID {
			// Enrich every Parameter.ResolvedDisplay with the human-readable
			// name for gg_unit_/gg_dest_ references before returning. Deep
			// copies so the cached *wtg.Triggers stays byte-faithful for
			// Phase 2a's encoder. enrichTriggerWithResolvedNames is a
			// no-op when the trigger has no ECAs (script + comment kinds).
			enriched := enrichTriggerWithResolvedNames(Current, &t.Triggers[i])
			return TriggerDetail{Kind: triggerKind(t.Triggers[i]), Trigger: enriched}, nil
		}
	}
	for i := range t.Variables {
		if t.Variables[i].ID == p.ID {
			return TriggerDetail{Kind: "variable", Variable: &t.Variables[i]}, nil
		}
	}
	return nil, fmt.Errorf("no trigger node with id %d", p.ID)
}

// TriggerFunctionMeta is one row from a TriggerData.txt function section
// ([TriggerEvents]/[TriggerConditions]/[TriggerActions]/[TriggerCalls]),
// flattened into a UI-renderable shape. Argc is the resolved argument count
// (1 of the 2 load-bearing numbers; the other is in ArgTypes). DisplayName,
// ParametersTemplate, and Category come from the _Foo_* companion keys.
//
// The wire stays close to the TriggerData layout so the frontend can do all
// its label-substitution client-side using get_parameters_names-style
// template walks — no Go-side label rendering, which keeps the per-trigger
// payload trim and lets the UI re-render labels on font / locale changes
// without re-fetching.
type TriggerFunctionMeta struct {
	Name               string   `json:"name"`
	Section            string   `json:"section"` // "TriggerEvents" | "TriggerConditions" | "TriggerActions" | "TriggerCalls"
	Argc               int      `json:"argc"`
	ArgTypes           []string `json:"arg_types"` // ordered, length == argc
	ReturnType         string   `json:"return_type,omitempty"` // TriggerCalls only
	DisplayName        string   `json:"display_name,omitempty"`
	ParametersTemplate []string `json:"parameters_template,omitempty"` // _Foo_Parameters tokens (~Arg slots interleaved with literal segments)
	Defaults           []string `json:"defaults,omitempty"`
	Limits             []string `json:"limits,omitempty"`
	Category           string   `json:"category,omitempty"`
	ScriptName         string   `json:"script_name,omitempty"`
	Hint               string   `json:"hint,omitempty"`
}

// TriggerFunctionsMetaResponse is the triggers.functions_meta payload —
// every function-shaped entry in TriggerData.txt, plus the type/category
// dictionaries the UI needs to render labels (resolve preset values, look
// up category icons, etc.).
//
// Static for the process lifetime (the snapshot doesn't change at runtime).
// Cached at the handler-level by triggerFunctionsMetaOnce.
type TriggerFunctionsMetaResponse struct {
	Functions  []TriggerFunctionMeta `json:"functions"`
	// Categories maps TC_* code → display label (after WESTRING resolution
	// would happen if we had that wired — for 1a we expose the raw label).
	Categories map[string]string `json:"categories,omitempty"`
	// Types maps trigger-variable-type name ("integer", "unit", "abilcode")
	// → the type's display label.
	Types map[string]string `json:"types,omitempty"`
}

// handleTriggersFunctionsMeta returns the full TriggerData.txt vocabulary,
// cached after first call. Backed by the embedded snapshot — no live CASC
// involvement. Returned payload is large (~6k entries × ~200 bytes each) but
// only one call per session.
func handleTriggersFunctionsMeta(_ json.RawMessage) (any, error) {
	resp := triggerFunctionsMetaCached()
	if resp == nil {
		return nil, errors.New("trigger data snapshot unavailable")
	}
	return resp, nil
}

// triggerFunctionsMetaOnce + triggerFunctionsMetaResp memoize the response
// across calls; the underlying TriggerData snapshot is itself process-wide.
var (
	triggerFunctionsMetaResp *TriggerFunctionsMetaResponse
)

// GetTriggerFunctionsMetaCached is the exported accessor used by package
// main's Wails wrapper. Same semantics as triggerFunctionsMetaCached but
// callable from outside the package.
func GetTriggerFunctionsMetaCached() *TriggerFunctionsMetaResponse {
	return triggerFunctionsMetaCached()
}

func triggerFunctionsMetaCached() *TriggerFunctionsMetaResponse {
	if triggerFunctionsMetaResp != nil {
		return triggerFunctionsMetaResp
	}
	td := TriggerDataSnapshot()
	if td == nil {
		return nil
	}
	resp := &TriggerFunctionsMetaResponse{
		Functions:  make([]TriggerFunctionMeta, 0, len(td.ArgumentCounts)),
		Categories: map[string]string{},
		Types:      map[string]string{},
	}
	sections := []string{"TriggerEvents", "TriggerConditions", "TriggerActions", "TriggerCalls"}
	for _, sect := range sections {
		rows, ok := td.Sections[sect]
		if !ok {
			continue
		}
		for key, tokens := range rows {
			if len(key) > 0 && key[0] == '_' {
				continue // companion meta
			}
			m := TriggerFunctionMeta{Name: key, Section: sect}
			argc, _ := td.Argc(key)
			m.Argc = argc
			// Slice out argument types per the per-section layout.
			// TriggerActions/Events/Conditions: [version, types...]
			// TriggerCalls: [version, event_flag, return_type, types...]
			args := tokens
			switch sect {
			case "TriggerCalls":
				if len(args) >= 3 {
					m.ReturnType = args[2]
					m.ArgTypes = filterNonTypes(args[3:])
				}
			default:
				if len(args) >= 1 {
					m.ArgTypes = filterNonTypes(args[1:])
				}
			}
			// Companion meta — _Foo_DisplayName, _Foo_Parameters, etc.
			if dn, ok := rows["_"+key+"_DisplayName"]; ok && len(dn) > 0 {
				m.DisplayName = strings.Join(dn, ",")
			}
			if pt, ok := rows["_"+key+"_Parameters"]; ok {
				m.ParametersTemplate = pt
			}
			if d, ok := rows["_"+key+"_Defaults"]; ok {
				m.Defaults = d
			}
			if l, ok := rows["_"+key+"_Limits"]; ok {
				m.Limits = l
			}
			if c, ok := rows["_"+key+"_Category"]; ok && len(c) > 0 {
				m.Category = c[0]
			}
			if sn, ok := rows["_"+key+"_ScriptName"]; ok && len(sn) > 0 {
				m.ScriptName = sn[0]
			}
			if hint := td.HintFor(key); hint != "" {
				m.Hint = hint
			}
			resp.Functions = append(resp.Functions, m)
		}
	}
	// Categories ([TriggerCategories]) — TC_* → display label (col 0).
	if rows, ok := td.Sections["TriggerCategories"]; ok {
		for key, tokens := range rows {
			if len(key) > 0 && key[0] == '_' {
				continue
			}
			if len(tokens) > 0 {
				resp.Categories[key] = tokens[0]
			}
		}
	}
	// Types ([TriggerTypes]) — name → display string (col 3 per the txt
	// comment block).
	if rows, ok := td.Sections["TriggerTypes"]; ok {
		for key, tokens := range rows {
			if len(key) > 0 && key[0] == '_' {
				continue
			}
			// Col 3 holds the display string; some rows are shorter (legacy).
			if len(tokens) >= 4 {
				resp.Types[key] = tokens[3]
			} else if len(tokens) > 0 {
				resp.Types[key] = tokens[0]
			}
		}
	}
	triggerFunctionsMetaResp = resp
	return resp
}

// filterNonTypes drops the "nothing" sentinel HiveWE skips for argc but keeps
// the typed slots (in their original order). The shape of TriggerData.txt
// rows like `DoNothing=0,nothing` means we'd otherwise emit a phantom
// argument; the editor's argc count skips it (per ParseTriggerData's
// counter) and the per-function detail should match.
func filterNonTypes(toks []string) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if t == "" || t == "nothing" {
			continue
		}
		// Numeric tokens are version markers / flags carried in the wrong
		// position by some companion meta — drop them too. (Real WC3 type
		// names are non-numeric.)
		if isNumeric(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

// Keep wct + wtg imports referenced for compile-time guard against future
// goimports re-grooming. The types here use them indirectly.
var _ = wct.File{}
var _ = wtg.Triggers{}

// ---------------------------------------------------------------------------
// Phase 2a mutation handlers. Pattern matches the Object Editor's
// makeSetFieldHandler/makeCreateCustomHandler: validate, dispatch, return
// the updated tree + the affected node's get-payload in one round-trip so
// the JS caller doesn't have to follow up with a tree-refresh fetch.
// ---------------------------------------------------------------------------

// triggerMutationResponse is the standard wire shape returned by every
// mutation handler. Tree is the post-mutation full tree; Detail is the
// triggers.get payload for the affected node (NewID for adds, ID for
// edits/deletes). Detail is nil when the affected node was deleted.
type triggerMutationResponse struct {
	Tree   TriggerTreeResponse `json:"tree"`
	NewID  int32               `json:"new_id,omitempty"`
	Detail *TriggerDetail      `json:"detail,omitempty"`
}

// buildMutationResponse re-runs handleTriggersTree + handleTriggersGet so
// the response carries the latest state. Detail is best-effort — a missing
// node returns nil rather than erroring (the deleted-node case).
func buildMutationResponse(affectedID int32) triggerMutationResponse {
	resp := triggerMutationResponse{NewID: affectedID}
	if t, err := handleTriggersTree(nil); err == nil {
		if tt, ok := t.(TriggerTreeResponse); ok {
			resp.Tree = tt
		}
	}
	if affectedID != 0 {
		params, _ := json.Marshal(triggersGetParams{ID: affectedID})
		if d, err := handleTriggersGet(params); err == nil {
			if dd, ok := d.(TriggerDetail); ok {
				resp.Detail = &dd
			}
		}
	}
	return resp
}

type triggersAddCategoryParams struct {
	Name     string `json:"name"`
	ParentID int32  `json:"parent_id"`
}

func handleTriggersAddCategory(params json.RawMessage) (any, error) {
	var p triggersAddCategoryParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	id, err := Current.AddTriggerCategory(p.Name, p.ParentID)
	if err != nil {
		return nil, err
	}
	return buildMutationResponse(id), nil
}

type triggersAddTriggerParams struct {
	Name     string `json:"name"`
	ParentID int32  `json:"parent_id"`
}

func handleTriggersAddGUI(params json.RawMessage) (any, error) {
	var p triggersAddTriggerParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	id, err := Current.AddGUITrigger(p.Name, p.ParentID)
	if err != nil {
		return nil, err
	}
	return buildMutationResponse(id), nil
}

func handleTriggersAddScript(params json.RawMessage) (any, error) {
	var p triggersAddTriggerParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	id, err := Current.AddScriptTrigger(p.Name, p.ParentID)
	if err != nil {
		return nil, err
	}
	return buildMutationResponse(id), nil
}

func handleTriggersAddComment(params json.RawMessage) (any, error) {
	var p triggersAddTriggerParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	id, err := Current.AddCommentTrigger(p.Name, p.ParentID)
	if err != nil {
		return nil, err
	}
	return buildMutationResponse(id), nil
}

type triggersAddVariableParams struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	IsArray      bool   `json:"is_array"`
	ArraySize    int32  `json:"array_size"`
	InitialValue string `json:"initial_value"`
}

func handleTriggersAddVariable(params json.RawMessage) (any, error) {
	var p triggersAddVariableParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	id, err := Current.AddTriggerVariable(p.Name, p.Type, p.IsArray, p.ArraySize, p.InitialValue)
	if err != nil {
		return nil, err
	}
	return buildMutationResponse(id), nil
}

type triggersIDOnlyParams struct {
	ID int32 `json:"id"`
}

func handleTriggersDelete(params json.RawMessage) (any, error) {
	var p triggersIDOnlyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.DeleteTriggerNode(p.ID); err != nil {
		return nil, err
	}
	// Affected id is 0 — the node is gone; just return the refreshed tree.
	return buildMutationResponse(0), nil
}

type triggersRenameParams struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

func handleTriggersRename(params json.RawMessage) (any, error) {
	var p triggersRenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.RenameTriggerNode(p.ID, p.Name); err != nil {
		return nil, err
	}
	return buildMutationResponse(p.ID), nil
}

type triggersIDBoolParams struct {
	ID    int32 `json:"id"`
	Value bool  `json:"value"`
}

func handleTriggersSetEnabled(params json.RawMessage) (any, error) {
	var p triggersIDBoolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.SetTriggerEnabled(p.ID, p.Value); err != nil {
		return nil, err
	}
	return buildMutationResponse(p.ID), nil
}

func handleTriggersSetInitiallyOn(params json.RawMessage) (any, error) {
	var p triggersIDBoolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.SetTriggerInitiallyOn(p.ID, p.Value); err != nil {
		return nil, err
	}
	return buildMutationResponse(p.ID), nil
}

func handleTriggersSetRunOnInit(params json.RawMessage) (any, error) {
	var p triggersIDBoolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.SetTriggerRunOnInit(p.ID, p.Value); err != nil {
		return nil, err
	}
	return buildMutationResponse(p.ID), nil
}

type triggersMoveParams struct {
	ID          int32 `json:"id"`
	NewParentID int32 `json:"new_parent_id"`
}

func handleTriggersMove(params json.RawMessage) (any, error) {
	var p triggersMoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.MoveTriggerNode(p.ID, p.NewParentID); err != nil {
		return nil, err
	}
	return buildMutationResponse(p.ID), nil
}

type triggersSetTextParams struct {
	ID   int32  `json:"id"`
	Text string `json:"text"`
}

func handleTriggersSetCustomText(params json.RawMessage) (any, error) {
	var p triggersSetTextParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.SetTriggerCustomText(p.ID, p.Text); err != nil {
		return nil, err
	}
	return buildMutationResponse(p.ID), nil
}

func handleTriggersSetDescription(params json.RawMessage) (any, error) {
	var p triggersSetTextParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.SetTriggerDescription(p.ID, p.Text); err != nil {
		return nil, err
	}
	return buildMutationResponse(p.ID), nil
}

type triggersSetVariableParams struct {
	ID           int32  `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	IsArray      bool   `json:"is_array"`
	ArraySize    int32  `json:"array_size"`
	InitialValue string `json:"initial_value"`
}

func handleTriggersSetVariable(params json.RawMessage) (any, error) {
	var p triggersSetVariableParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.SetTriggerVariable(p.ID, p.Name, p.Type, p.IsArray, p.ArraySize, p.InitialValue); err != nil {
		return nil, err
	}
	return buildMutationResponse(p.ID), nil
}

type triggersSetMapHeaderParams struct {
	Content string `json:"content"`
}

func handleTriggersSetMapHeaderScript(params json.RawMessage) (any, error) {
	var p triggersSetMapHeaderParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.SetMapHeaderScript(p.Content); err != nil {
		return nil, err
	}
	return buildMutationResponse(0), nil
}
