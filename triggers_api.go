package main

import (
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
			return &TriggerDetailDTO{Kind: triggerKindMain(t.Triggers[i]), Trigger: &t.Triggers[i]}, nil
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

// TriggerFunctionsMetaDTO is the full TriggerData.txt vocabulary the frontend
// needs to render labels client-side. Cached for the process lifetime.
type TriggerFunctionsMetaDTO struct {
	Functions  []TriggerFunctionMetaDTO `json:"functions"`
	Categories map[string]string        `json:"categories,omitempty"`
	Types      map[string]string        `json:"types,omitempty"`
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
