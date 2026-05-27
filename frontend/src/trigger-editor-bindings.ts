// Local shim for the Trigger Editor's Wails bindings (Phase 1a, read-only).
// Pattern matches object-editor-bindings.ts: declare the App methods + JSON
// shapes here so TypeScript is happy before the first `wails build`
// regenerates wailsjs/go/main/App.js. After a fresh build these can collapse
// to re-exports from the generated module.

export type TriggerKind =
  | 'map'
  | 'library'
  | 'category'
  | 'trigger_gui'
  | 'trigger_script'
  | 'trigger_comment'
  | 'variable'
  | 'unknown'

export interface TriggerTreeNode {
  id: number
  parent_id: number
  kind: TriggerKind
  name: string
  description?: string
  is_comment?: boolean
  is_enabled?: boolean
  is_script?: boolean
  initially_on?: boolean
  run_on_initialization?: boolean
  open_state?: boolean
}

export interface TriggerTree {
  nodes: TriggerTreeNode[]
  is_pre_131?: boolean
  has_global_jass?: boolean
}

// One ECA parameter. SubParameter is recursive (a sub-function call's
// parameters are themselves Parameters). ArrayIndex carries the index
// expression for variable[expr] references.
//
// ResolvedDisplay is a UI-only enrichment populated server-side by the
// Trigger Editor's GET path — present when `value` is a recognized
// codegen-generated entity reference (gg_unit_*, gg_dest_*) that resolved
// to a placed entity's display name. Empty / absent otherwise. The frontend
// prefers it over `value` for display, but the literal `value` stays
// canonical for round-trip / copy-paste.
export interface TriggerParameter {
  type: number // ParamType: -1 invalid, 0 preset, 1 variable, 2 function, 3 string
  value: string
  has_sub_parameter?: boolean
  sub_parameter?: TriggerECA
  unknown?: number
  is_array?: boolean
  array_index?: TriggerParameter
  resolved_display?: string
}

// One Event / Condition / Action / Call row inside a trigger.
export interface TriggerECA {
  type: number // ECAType: 0 event, 1 condition, 2 action, 3 call
  group?: number
  name: string
  enabled: boolean
  parameters?: TriggerParameter[]
  children?: TriggerECA[]
}

export interface TriggerCategoryRecord {
  classifier: number
  id: number
  parent_id: number
  name: string
  open_state: boolean
  is_comment?: boolean
}

export interface TriggerRecord {
  classifier: number
  id: number
  parent_id: number
  name: string
  description?: string
  custom_text?: string
  is_comment?: boolean
  is_enabled: boolean
  is_script?: boolean
  initially_on: boolean
  run_on_initialization?: boolean
  ecas?: TriggerECA[]
}

export interface TriggerVariableRecord {
  name: string
  type: string
  unknown?: number
  is_array?: boolean
  array_size?: number
  is_initialized?: boolean
  initial_value?: string
  id: number
  parent_id: number
}

export interface TriggerDetail {
  kind: TriggerKind
  category?: TriggerCategoryRecord
  trigger?: TriggerRecord
  variable?: TriggerVariableRecord
}

// One row from a TriggerData.txt function family. ArgTypes is the ordered
// type list (e.g. ["unitcode", "player", "location"]); ParametersTemplate
// is the _Foo_Parameters template the UI walks to render labels.
export interface TriggerFunctionMeta {
  name: string
  section: 'TriggerEvents' | 'TriggerConditions' | 'TriggerActions' | 'TriggerCalls'
  argc: number
  arg_types: string[]
  return_type?: string
  display_name?: string
  parameters_template?: string[]
  defaults?: string[]
  limits?: string[]
  category?: string
  script_name?: string
  hint?: string
}

export interface TriggerFunctionsMeta {
  functions: TriggerFunctionMeta[]
  categories?: Record<string, string>
  types?: Record<string, string>
}

interface WailsApp {
  ListTriggerTree: () => Promise<TriggerTree>
  GetTrigger: (id: number) => Promise<TriggerDetail>
  GetTriggerFunctionsMeta: () => Promise<TriggerFunctionsMeta | null>
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function app(): WailsApp {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return (window as any).go?.main?.App as WailsApp
}

export function ListTriggerTree(): Promise<TriggerTree> {
  return app().ListTriggerTree()
}

export function GetTrigger(id: number): Promise<TriggerDetail> {
  return app().GetTrigger(id)
}

export function GetTriggerFunctionsMeta(): Promise<TriggerFunctionsMeta | null> {
  return app().GetTriggerFunctionsMeta()
}

// Stringify a parameter into a human-readable inline label. Mirrors
// HiveWE's get_parameters_names recursive substitution (trigger_editor.cpp
// L389-476).
//
// For a parameter:
//   - If has_sub_parameter: render `subname(arg1, arg2, …)` recursively
//   - Otherwise: render the literal `value` (strings get quoted)
//
// The caller usually drives this via renderECALabel which walks the
// _Foo_Parameters template's ~ArgN slots.
export function paramLabel(p: TriggerParameter | undefined | null): string {
  if (!p) return ''
  if (p.has_sub_parameter && p.sub_parameter) {
    const sub = p.sub_parameter
    const args = (sub.parameters ?? []).map((sp) => paramLabel(sp))
    return `${sub.name}(${args.join(', ')})`
  }
  if (p.type === 3 /* ParamString */) {
    // String params show with surrounding quotes so the user can tell them
    // apart from preset / variable references. Color codes (|c…|r) inside
    // the value stay intact — the downstream renderer parses the whole
    // labelled string for color tokens, and double-quote chars never appear
    // in a color tag so the parse is unambiguous.
    return JSON.stringify(p.resolved_display || p.value)
  }
  // For non-string params, prefer the resolved display when present (it's
  // the entity's human-readable name) but keep the raw value as the source
  // of truth for any caller that needs canonical bytes.
  return p.resolved_display || p.value || '(unset)'
}

// Render an ECA's display label by walking its parameters_template (from
// TriggerData.txt's _Foo_Parameters key). Tokens starting with "~" are
// parameter slot references — pop the next Parameter and call paramLabel.
// All other tokens are literal text fragments.
//
// Returns the function name if no template is available (the picker shows
// "FunctionName(arg1, arg2)" as a fallback). Magic ECAs (IfThenElseMultiple,
// ForLoopAMultiple, AndMultiple) are usually displayed as "If, then, else"
// at the parent level with their child rows handling the body — the caller
// branches on the function name.
export function renderECALabel(
  eca: TriggerECA,
  template: string[] | undefined,
): string {
  if (!template || template.length === 0) {
    const args = (eca.parameters ?? []).map((p) => paramLabel(p))
    return `${eca.name}(${args.join(', ')})`
  }
  const params = eca.parameters ?? []
  let argIdx = 0
  const parts: string[] = []
  for (const tok of template) {
    if (tok.startsWith('~')) {
      parts.push(paramLabel(params[argIdx]))
      argIdx++
    } else {
      parts.push(tok)
    }
  }
  return parts.join('')
}

// Magic ECAs whose children render under synthetic "If/Then/Else/Loop"
// section headers per HiveWE trigger_editor.cpp L351-365. Used by the
// Trigger Editor's right pane to add the extra labels under each parent
// row's child list. Phase 2a: lift to a richer per-name child-bucket
// renderer when authoring lands.
export const MAGIC_ECAS = new Set<string>([
  'IfThenElse',
  'IfThenElseMultiple',
  'ForLoopA',
  'ForLoopAMultiple',
  'ForLoopB',
  'ForLoopBMultiple',
  'ForLoopVar',
  'ForLoopVarMultiple',
  'AndMultiple',
  'OrMultiple',
])
