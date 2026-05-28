<script lang="ts">
  // Per-parameter inline editor popup. The TriggerEditor opens this when the
  // user clicks a `~ArgN` token in a rendered ECA label.
  //
  // The editor variant depends on the parameter's declared type (passed in
  // via paramType, the argument-type string from TriggerData.txt — e.g.
  // "integer", "real", "playercolor", "unitcode", "boolean").
  //
  // Phase 2b1 scope:
  //   - integer / int / level                → number input (step 1)
  //   - real / unreal / percentunreal        → number input (step any)
  //   - string / stringnoformat / scriptcode → text input
  //   - boolean / boolexpr                   → true/false toggle (boolexpr is
  //                                            DEGRADED in 2b1; 2b2 ships the
  //                                            nested boolean builder)
  //   - playercolor / player / preset-typed  → dropdown of [TriggerParams] presets
  //   - *code (unit/item/abil/dest/doodad/   → reuse IconPicker pattern via
  //     upgrade/buff)                          callback into parent
  //   - variable references                  → dropdown of session variables of
  //                                            matching type
  //   - sub-function present (has_sub_param) → read-only display + 2b2 caption
  //
  // The editor commits via onChange({ value, paramType }). The caller routes
  // that into Session.SetParamValue + closes the popup.

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
  } from './trigger-editor-bindings'

  let {
    open = $bindable(false),
    paramType,
    param,
    presets,
    typeMeta,
    variables,
    onChange,
    onPickEntity,
    onClose,
  }: {
    open?: boolean
    // The declared type of this slot, from TriggerData.txt. Drives which
    // editor variant we show. May be empty (template literal-only slot,
    // shouldn't really happen for clickable tokens).
    paramType: string
    // The current Parameter value (so the editor starts at the user's
    // existing pick). null if the slot is being filled for the first time.
    param: TriggerParameter | null | undefined
    // The full [TriggerParams] table; filtered client-side by Type.
    presets: TriggerPresetMeta[]
    // The [TriggerTypes] table; used to walk alias chains (e.g. unitcode →
    // integer) when picking the editor variant.
    typeMeta: TriggerTypeMeta[]
    // Session variables visible at this scope (from the current map's wtg).
    // Filtered by type at render time.
    variables: TriggerVariableRecord[]
    // Commit callback. paramType matches wtg.ParamType (Preset / Variable / String).
    onChange: (commit: { value: string; paramType: number }) => void
    // *code-type pickers reuse the Object Editor IconPicker pattern — the
    // parent owns the picker modal; we just fire the callback when the
    // user clicks "Pick…".
    onPickEntity?: (kind: 'units' | 'items' | 'abilities' | 'buffs' | 'destructables' | 'doodads' | 'upgrades', current: string, commit: (value: string) => void) => void
    onClose?: () => void
  } = $props()

  // Walked-down base type for switch decisions. unitcode → integer for raw
  // editing, but we ALSO surface the picker for *code types via a separate
  // branch above the base-type fallback.
  let baseType = $derived.by(() => {
    return walkBaseType(paramType, typeMeta)
  })

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

  // Helper: is this a *code type? Used to surface the entity picker branch.
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

  // Local form state. We bind these to inputs and commit on the Set button.
  let formText = $state('')
  let formBool = $state(false)
  let formPreset = $state('')
  let formVariable = $state('')

  // Initialize once when opened with a specific param. Subsequent open
  // transitions re-seed from `param` so re-opening the same slot shows
  // whatever's currently committed.
  let lastOpenedFor = $state<TriggerParameter | null>(null)
  $effect(() => {
    if (!open) return
    if (lastOpenedFor === param) return
    lastOpenedFor = param ?? null
    const value = param?.value ?? ''
    formText = value
    formBool = value === 'true' || value === '1'
    formPreset = value
    formVariable = value
  })

  // Filter presets to slot type (or base type). HiveWE matches on the
  // declared type only — alias-walking would pollute the dropdown with
  // unrelated integer literals for `unitcode` slots.
  let filteredPresets = $derived.by(() => {
    return presets.filter((p) => p.type === paramType)
  })

  // Variables visible: those whose declared type matches paramType OR the
  // walked base type. Variables are scarce (single-digit count in most maps)
  // so the broader match is fine — the user is rarely confused.
  let filteredVariables = $derived.by(() => {
    return variables.filter((v) => v.type === paramType || v.type === baseType)
  })

  function commitInt() {
    const n = parseInt(formText, 10)
    onChange({ value: Number.isFinite(n) ? String(n) : '0', paramType: ParamType.Preset })
    close()
  }
  function commitFloat() {
    const n = parseFloat(formText)
    onChange({ value: Number.isFinite(n) ? String(n) : '0', paramType: ParamType.Preset })
    close()
  }
  function commitString() {
    onChange({ value: formText, paramType: ParamType.String })
    close()
  }
  function commitBool() {
    onChange({ value: formBool ? 'true' : 'false', paramType: ParamType.Preset })
    close()
  }
  function commitPreset() {
    onChange({ value: formPreset, paramType: ParamType.Preset })
    close()
  }
  function commitVariable() {
    onChange({ value: formVariable, paramType: ParamType.Variable })
    close()
  }

  function close() {
    open = false
    onClose?.()
  }

  function dismiss(o: boolean) {
    if (!o) onClose?.()
    open = o
  }

  // *code picker — defer to parent (IconPicker is too heavy to inline here).
  function openEntityPicker() {
    const kind = isCodeType(paramType)
    if (!kind || !onPickEntity) return
    onPickEntity(kind, param?.value || '', (picked) => {
      onChange({ value: picked, paramType: ParamType.Preset })
      close()
    })
  }

  // Display the sub-function read-only (Phase 2b1 doesn't let the user
  // build sub-functions; that's 2b2's surface). The user can replace it
  // with a literal via the editor variants above — SetParamValue clears
  // HasSubParameter automatically.
  let subFuncLabel = $derived.by(() => {
    if (!param?.has_sub_parameter || !param.sub_parameter) return ''
    return paramLabel(param)
  })
</script>

<Dialog.Root bind:open={open} onOpenChange={dismiss}>
  <Dialog.Content class="!max-w-[480px] !w-[80vw] p-0 gap-0">
    <Dialog.Header class="px-4 py-3 border-b border-border">
      <Dialog.Title>Edit Parameter</Dialog.Title>
      <Dialog.Description class="text-xs text-muted-foreground">
        Slot type: <code>{paramType || '(unknown)'}</code>
        {#if baseType && baseType !== paramType}
          (base: <code>{baseType}</code>)
        {/if}
      </Dialog.Description>
    </Dialog.Header>

    <div class="px-4 py-3 pe-body">
      {#if param?.has_sub_parameter}
        <!-- Sub-function present: 2b1 shows read-only, 2b2 lands the builder. -->
        <div class="pe-readonly">
          <div class="text-xs text-zinc-500 mb-1">Current (sub-function)</div>
          <div class="font-mono text-sm text-zinc-300 break-all">{subFuncLabel}</div>
          <div class="text-xs text-amber-400 mt-2">
            Sub-function editing lands in Phase 2b2. To replace this with a literal value, pick one of the options below.
          </div>
        </div>
      {/if}

      {#if isCodeType(paramType)}
        <!-- *code types — punch out to the entity picker (IconPicker pattern). -->
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">Pick a {isCodeType(paramType)} (FourCC)</div>
          <div class="flex gap-2 items-center">
            <Input bind:value={formText} placeholder="Type FourCC or click Pick…" class="h-8" />
            <Button onclick={openEntityPicker}>Pick…</Button>
          </div>
          <Button class="w-full" onclick={() => { onChange({ value: formText, paramType: ParamType.Preset }); close() }}>Set FourCC</Button>
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
      {:else if paramType === 'boolean' || paramType === 'boolexpr'}
        <div class="space-y-2">
          <div class="text-xs text-zinc-400">
            Boolean
            {#if paramType === 'boolexpr'}
              <span class="text-amber-400 ml-1">— degraded to true/false in 2b1 (2b2 ships the nested boolean builder)</span>
            {/if}
          </div>
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
        <!-- Fallback: this slot type has no presets, no matching variables,
             and isn't one of the simple primitives. 2b2 will fold these into
             the sub-function builder. For 2b1, expose a raw text input so
             power users can still hand-type a value. -->
        <div class="space-y-2">
          <div class="text-xs text-amber-400">
            No simple editor for type <code>{paramType}</code>. Phase 2b2 will add the sub-function builder for this slot — for now, you can hand-type a literal preset name or a JASS expression.
          </div>
          <Input type="text" bind:value={formText} class="h-8" />
          <Button class="w-full" onclick={() => { onChange({ value: formText, paramType: ParamType.Preset }); close() }}>Set raw</Button>
        </div>
      {/if}
    </div>

    <Dialog.Footer class="px-4 py-2 border-t border-border">
      <Button variant="outline" onclick={close}>Cancel</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<style>
  .pe-body { min-height: 80px; }
  .pe-readonly {
    background: #0e0e10;
    border: 1px solid #27272a;
    border-radius: 4px;
    padding: 8px 10px;
    margin-bottom: 12px;
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
</style>
