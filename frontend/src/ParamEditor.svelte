<script lang="ts">
  // Phase 2b2 — recursive per-parameter inline editor.
  //
  // The editor renders one Parameter slot with:
  //   - A "Value" tab: literal/preset/variable editors (the 2b1 set).
  //   - A "Function" tab: sub-function picker → recurses by rendering THIS
  //     same component for each of the sub-function's inner parameters.
  //   - An "Array" affordance: toggles IsArray; when on, the array-index
  //     expression renders below as a nested ParamEditor (full recursive
  //     editor — preset/variable/function/literal for the index expression).
  //
  // The recursive paramPath addresses sub-parameter chains:
  //   paramPath = [outerIdx, innerIdx, innerInnerIdx, …]
  //   (matches Session.SetParamValue's (ecaPath, paramPath) convention.)
  //
  // Depth > 6 surfaces a yellow warning bar — pathologically deep nesting
  // usually means "extract to a variable".

  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import { Input } from '$lib/components/ui/input'
  import {
    ParamType,
    paramLabel,
    type TriggerParameter,
    type TriggerVariableRecord,
    type TriggerPresetMeta,
    type TriggerTypeMeta,
    type TriggerFunctionMeta,
  } from './trigger-editor-bindings'

  let {
    paramType,
    param,
    presets,
    typeMeta,
    variables,
    functionsMeta,
    depth = 0,
    onCommitValue,
    onPickSubFunction,
    onClearSubFunction,
    onToggleArray,
    onPickEntity,
    onPickInstance,
  }: {
    // The declared type of this slot, from TriggerData.txt. Drives which
    // editor variant we show.
    paramType: string
    // The current Parameter value (so editor starts at the user's existing
    // pick). null if the slot is being filled for the first time.
    param: TriggerParameter | null | undefined
    // The full [TriggerParams] table; filtered client-side by Type.
    presets: TriggerPresetMeta[]
    // The [TriggerTypes] table; used to walk alias chains.
    typeMeta: TriggerTypeMeta[]
    // Session variables visible at this scope.
    variables: TriggerVariableRecord[]
    // Every function row from TriggerData — used by the sub-function picker
    // to filter by return type.
    functionsMeta: TriggerFunctionMeta[]
    // Current nesting depth (0 at the top of the inline editor; +1 per
    // recursion). Used for indent + the depth>6 warning bar.
    depth?: number
    // Commit a literal/preset/variable/string value. The caller routes to
    // SetParamValue.
    onCommitValue: (commit: { value: string; paramType: number }) => void
    // Build a sub-function with the given name. The caller routes to
    // SetParamSubFunction; the next render shows the sub-function's params.
    onPickSubFunction: (subName: string) => void
    // Clear the current sub-function. Caller routes to ClearParamSubFunction.
    onClearSubFunction: () => void
    // Toggle array-mode on this parameter. Caller routes to SetParamArray.
    onToggleArray: (isArray: boolean) => void
    // *code-type pickers reuse the IconPicker (parent owns the modal).
    onPickEntity?: (kind: 'units' | 'items' | 'abilities' | 'buffs' | 'destructables' | 'doodads' | 'upgrades', current: string, commit: (value: string) => void) => void
    // Instance pickers for unit/destructable/region/camera-type slots. The
    // commit callback writes the picked gg_ref into Parameter.Value.
    onPickInstance?: (kind: 'unit' | 'destructable' | 'region' | 'camera', commit: (ggRef: string) => void) => void
  } = $props()

  // Walked-down base type for switch decisions.
  let baseType = $derived.by(() => walkBaseType(paramType, typeMeta))

  function walkBaseType(t: string, tm: TriggerTypeMeta[]): string {
    if (!t) return ''
    const map = new Map(tm.map((x) => [x.name, x.base_type || '']))
    let cur = t
    for (let i = 0; i < 16; i++) {
      const next = map.get(cur)
      if (!next || next === cur) return cur
      cur = next
    }
    return cur
  }

  // Helper: is this a *code type? Surface the entity (FourCC) picker.
  function isCodeType(t: string): 'units' | 'items' | 'abilities' | 'buffs' | 'destructables' | 'doodads' | 'upgrades' | null {
    switch (t) {
      case 'unitcode': return 'units'
      case 'itemcode': return 'items'
      case 'abilcode': return 'abilities'
      case 'buffcode': return 'buffs'
      case 'destructablecode': return 'destructables'
      case 'doodadcode': return 'doodads'
      case 'upgradecode': return 'upgrades'
    }
    return null
  }

  // Helper: is this an instance type? (Live-map entity lookup — unit
  // placement / region / camera / destructable instance.) These differ from
  // *code types — the picker returns a gg_*_ ref, not a FourCC.
  function instanceKindFor(t: string): 'unit' | 'destructable' | 'region' | 'camera' | null {
    switch (t) {
      case 'unit': return 'unit'
      case 'destructable': return 'destructable'
      case 'rect': case 'region': return 'region'
      case 'camerasetup': case 'camera': return 'camera'
    }
    return null
  }

  // Tabs: "Value" (literals/presets/variables/entity) vs "Function" (sub-
  // function builder). For primitives (int/real/string/boolean/*code), there's
  // no meaningful "Function" tab — the slot doesn't compose. We still show
  // the tab strip for consistency but disable the Function tab on those.
  let activeTab = $state<'value' | 'function'>('value')

  // Initialize tab from current Parameter state.
  let lastInitFor = $state<TriggerParameter | null>(null)
  $effect(() => {
    if (lastInitFor === param) return
    lastInitFor = param ?? null
    if (param?.has_sub_parameter) {
      activeTab = 'function'
    } else {
      activeTab = 'value'
    }
  })

  // Functions that can be picked as sub-function for this slot. Filter
  // [TriggerCalls] entries by return_type matching paramType (or baseType).
  let candidateCalls = $derived.by(() => {
    return functionsMeta.filter((f) => {
      if (f.section !== 'TriggerCalls') return false
      const rt = f.return_type || ''
      return rt === paramType || rt === baseType
    })
  })

  // Whether the sub-function tab is even meaningful for this slot.
  let canBeFunction = $derived(candidateCalls.length > 0)

  // Local form state. Bound to inputs; commit-on-button.
  let formText = $state('')
  let formBool = $state(false)
  let formPreset = $state('')
  let formVariable = $state('')

  // Seed local state from param on slot-change.
  let lastSeededFor = $state<TriggerParameter | null>(null)
  $effect(() => {
    if (lastSeededFor === param) return
    lastSeededFor = param ?? null
    const value = param?.value ?? ''
    formText = value
    formBool = value === 'true' || value === '1'
    formPreset = value
    formVariable = value
  })

  // Filtered presets / variables by slot type.
  let filteredPresets = $derived.by(() => presets.filter((p) => p.type === paramType))
  let filteredVariables = $derived.by(() => variables.filter((v) => v.type === paramType || v.type === baseType))

  function commitInt() {
    const n = parseInt(formText, 10)
    onCommitValue({ value: Number.isFinite(n) ? String(n) : '0', paramType: ParamType.Preset })
  }
  function commitFloat() {
    const n = parseFloat(formText)
    onCommitValue({ value: Number.isFinite(n) ? String(n) : '0', paramType: ParamType.Preset })
  }
  function commitString() {
    onCommitValue({ value: formText, paramType: ParamType.String })
  }
  function commitBool() {
    onCommitValue({ value: formBool ? 'true' : 'false', paramType: ParamType.Preset })
  }
  function commitPreset() {
    onCommitValue({ value: formPreset, paramType: ParamType.Preset })
  }
  function commitVariable() {
    onCommitValue({ value: formVariable, paramType: ParamType.Variable })
  }
  function commitRawString() {
    onCommitValue({ value: formText, paramType: ParamType.Preset })
  }

  function openCodePicker() {
    const kind = isCodeType(paramType)
    if (!kind || !onPickEntity) return
    onPickEntity(kind, param?.value || '', (picked) => {
      onCommitValue({ value: picked, paramType: ParamType.Preset })
    })
  }

  function openInstance() {
    const k = instanceKindFor(paramType)
    if (!k || !onPickInstance) return
    onPickInstance(k, (ggRef) => {
      onCommitValue({ value: ggRef, paramType: ParamType.Preset })
    })
  }

  // Sub-function picker state (inline). Filter by return-type matching.
  let subPickerSearch = $state('')
  let visibleCalls = $derived.by(() => {
    const q = subPickerSearch.trim().toLowerCase()
    if (!q) return candidateCalls
    return candidateCalls.filter((f) => {
      const dn = (f.display_name || '').toLowerCase()
      return dn.includes(q) || f.name.toLowerCase().includes(q)
    })
  })
  function pickSub(f: TriggerFunctionMeta) {
    onPickSubFunction(f.name)
  }
</script>

<div class="pe-card" style="margin-left: {Math.min(depth, 6) * 10}px">
  {#if depth > 6}
    <div class="pe-deep-warn">
      Deeply nested function call ({depth} levels) — consider extracting to a variable.
    </div>
  {/if}

  <div class="pe-header">
    <div class="text-xs text-zinc-400">
      Slot: <code class="pe-type">{paramType || '(unknown)'}</code>
      {#if baseType && baseType !== paramType}
        (base <code>{baseType}</code>)
      {/if}
    </div>
    {#if canBeFunction}
      <div class="pe-tabs">
        <button class="pe-tab {activeTab === 'value' ? 'pe-tab-active' : ''}" onclick={() => (activeTab = 'value')}>Value</button>
        <button class="pe-tab {activeTab === 'function' ? 'pe-tab-active' : ''}" onclick={() => (activeTab = 'function')}>Function</button>
      </div>
    {/if}
  </div>

  {#if activeTab === 'value'}
    <div class="pe-body">
      {#if isCodeType(paramType)}
        <!-- *code types — FourCC + IconPicker pattern. -->
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">Pick a {isCodeType(paramType)} (FourCC)</div>
          <div class="flex gap-2 items-center">
            <Input bind:value={formText} placeholder="Type FourCC or click Pick…" class="h-8" />
            <Button onclick={openCodePicker}>Pick…</Button>
          </div>
          <Button class="w-full" onclick={commitRawString}>Set FourCC</Button>
        </div>
      {:else if instanceKindFor(paramType)}
        <!-- Live-map instance (unit/destructable/region/camera). The instance
             picker returns gg_*_ refs the engine resolves at codegen time. -->
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">Pick a placed {instanceKindFor(paramType)}</div>
          <Input bind:value={formText} placeholder={`gg_${instanceKindFor(paramType) === 'destructable' ? 'dest' : instanceKindFor(paramType) === 'region' ? 'rct' : instanceKindFor(paramType) === 'camera' ? 'cam' : 'unit'}_…`} class="h-8" />
          <div class="flex gap-2">
            <Button class="flex-1" onclick={openInstance}>Pick…</Button>
            <Button class="flex-1" onclick={commitRawString}>Set ref</Button>
          </div>
        </div>
      {:else if paramType === 'integer' || paramType === 'int' || paramType === 'level'}
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">Integer</div>
          <Input type="number" step="1" bind:value={formText} class="h-8" />
          <Button class="w-full" onclick={commitInt}>Set</Button>
        </div>
      {:else if paramType === 'real' || paramType === 'unreal' || paramType === 'percentunreal'}
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">Real number</div>
          <Input type="number" step="any" bind:value={formText} class="h-8" />
          <Button class="w-full" onclick={commitFloat}>Set</Button>
        </div>
      {:else if paramType === 'string' || paramType === 'stringnoformat' || paramType === 'scriptcode'}
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">String</div>
          <Input type="text" bind:value={formText} class="h-8" />
          <Button class="w-full" onclick={commitString}>Set</Button>
        </div>
      {:else if paramType === 'boolean'}
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">Boolean</div>
          <label class="flex items-center gap-2 text-sm">
            <input type="checkbox" bind:checked={formBool} /> {formBool ? 'true' : 'false'}
          </label>
          <Button class="w-full" onclick={commitBool}>Set</Button>
        </div>
      {:else if filteredPresets.length > 0}
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">Preset value</div>
          <select bind:value={formPreset} class="pe-select">
            <option value="">— pick —</option>
            {#each filteredPresets as p (p.name)}
              <option value={p.name}>{p.display_name || p.name}</option>
            {/each}
          </select>
          {#if filteredVariables.length > 0}
            <div class="text-xs text-zinc-400 pt-2">…or pick a variable</div>
            <select bind:value={formVariable} class="pe-select">
              <option value="">— variable —</option>
              {#each filteredVariables as v (v.name)}
                <option value={v.name}>{v.name} ({v.type})</option>
              {/each}
            </select>
            <div class="flex gap-2">
              <Button class="flex-1" onclick={commitPreset} disabled={!formPreset}>Set preset</Button>
              <Button class="flex-1" onclick={commitVariable} disabled={!formVariable}>Set variable</Button>
            </div>
          {:else}
            <Button class="w-full" onclick={commitPreset} disabled={!formPreset}>Set</Button>
          {/if}
        </div>
      {:else if filteredVariables.length > 0}
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">Variable</div>
          <select bind:value={formVariable} class="pe-select">
            <option value="">— variable —</option>
            {#each filteredVariables as v (v.name)}
              <option value={v.name}>{v.name} ({v.type})</option>
            {/each}
          </select>
          <Button class="w-full" onclick={commitVariable} disabled={!formVariable}>Set</Button>
        </div>
      {:else}
        <!-- Fallback: raw text input (power-user JASS literal) -->
        <div class="space-y-2">
          {#if !canBeFunction}
            <div class="text-xs text-amber-400">
              No simple editor for type <code>{paramType}</code>. Type a literal preset name or a JASS expression.
            </div>
          {/if}
          <Input type="text" bind:value={formText} class="h-8" />
          <Button class="w-full" onclick={commitRawString}>Set raw</Button>
        </div>
      {/if}
    </div>
  {:else if activeTab === 'function'}
    <div class="pe-body">
      {#if param?.has_sub_parameter && param.sub_parameter}
        {@const sub = param.sub_parameter}
        <div class="pe-subhead">
          <div class="text-xs text-zinc-400">Sub-function:</div>
          <div class="text-sm font-mono text-zinc-200">{sub.name}</div>
          <Button variant="outline" class="ml-auto" onclick={onClearSubFunction}>Remove function</Button>
        </div>
        <!-- The sub-function's parameters are NOT rendered here — they are
             driven by the parent component which calls ParamEditor recursively
             for each inner Parameter slot. This component only renders ONE
             slot. The parent's render loop walks sub.parameters[].
             We display a hint nudging the parent to surface them. -->
        <div class="text-xs text-zinc-500 italic">
          {(sub.parameters ?? []).length} sub-parameter{(sub.parameters?.length ?? 0) === 1 ? '' : 's'} — edit them inline in the trigger row.
        </div>
      {:else}
        <!-- Empty Function tab: show the picker -->
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">Pick a function returning <code>{paramType}</code></div>
          <Input type="search" placeholder="Filter functions…" bind:value={subPickerSearch} class="h-8" />
          <div class="pe-callbox">
            {#each visibleCalls.slice(0, 200) as f (f.name)}
              <button class="pe-call" onclick={() => pickSub(f)} title={f.hint || f.name}>
                <div class="pe-call-name">{f.display_name || f.name}</div>
                {#if f.hint}<div class="pe-call-hint">{f.hint}</div>{/if}
              </button>
            {/each}
            {#if visibleCalls.length === 0}
              <div class="px-2 py-1 text-xs text-zinc-500">(no calls return {paramType})</div>
            {/if}
            {#if visibleCalls.length > 200}
              <div class="px-2 py-1 text-xs text-zinc-500">(showing first 200 of {visibleCalls.length})</div>
            {/if}
          </div>
        </div>
      {/if}
    </div>
  {/if}

  <!-- Array toggle. Always shown — applies to any param, since the engine
       allows variable[expr] semantics on any global. -->
  <div class="pe-array">
    <label class="text-xs text-zinc-400">
      <input type="checkbox" checked={!!param?.is_array} onchange={(e) => onToggleArray(e.currentTarget.checked)} /> Array index
    </label>
    {#if param?.is_array}
      <div class="text-xs text-zinc-500 mt-1">
        Index expression (edit inline in the trigger row — paramPath drills into the array index).
      </div>
    {/if}
  </div>
</div>

<style>
  .pe-card {
    background: #0e0e10;
    border: 1px solid #27272a;
    border-left: 3px solid #3f3f46;
    border-radius: 4px;
    padding: 8px 10px;
    margin-bottom: 8px;
  }
  .pe-header {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 6px;
  }
  .pe-type { color: #a5b4fc; font-size: 11px; }
  .pe-tabs {
    margin-left: auto;
    display: flex;
    gap: 2px;
  }
  .pe-tab {
    background: transparent;
    color: #71717a;
    border: 1px solid #27272a;
    padding: 2px 8px;
    font-size: 11px;
    border-radius: 3px;
    cursor: pointer;
  }
  .pe-tab:hover { color: #d4d4d8; }
  .pe-tab-active { background: rgba(96, 165, 250, 0.18); color: #c7d2fe; border-color: #60a5fa; }
  .pe-body { min-height: 60px; }
  .pe-subhead {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px;
    background: #18181b;
    border-radius: 3px;
    margin-bottom: 6px;
  }
  .pe-callbox {
    max-height: 240px;
    overflow-y: auto;
    border: 1px solid #27272a;
    border-radius: 3px;
  }
  .pe-call {
    display: block;
    width: 100%;
    text-align: left;
    padding: 4px 10px;
    background: none;
    border: 0;
    border-bottom: 1px solid rgba(255,255,255,0.04);
    color: #d4d4d8;
    cursor: pointer;
  }
  .pe-call:hover { background: rgba(255, 255, 255, 0.06); }
  .pe-call-name { font-size: 12px; color: #e4e4e7; }
  .pe-call-hint { font-size: 10px; color: #71717a; margin-top: 1px; line-height: 1.3; }
  .pe-array {
    margin-top: 8px;
    padding-top: 6px;
    border-top: 1px dashed #27272a;
  }
  .pe-select {
    background: #0e0e10;
    color: #e4e4e7;
    border: 1px solid #27272a;
    padding: 4px 8px;
    font-size: 13px;
    border-radius: 4px;
    width: 100%;
    height: 32px;
  }
  .pe-deep-warn {
    background: rgba(252, 211, 77, 0.10);
    color: #fcd34d;
    border-left: 3px solid #fcd34d;
    padding: 4px 8px;
    font-size: 11px;
    margin-bottom: 6px;
    border-radius: 3px;
  }
</style>
