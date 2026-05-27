<script lang="ts">
  // Object Editor (Phase 1b — write-side). Shadcn-svelte Dialog hosts the
  // two-pane layout (left: Race → Kind → Unit tree; right: per-field table
  // for the selected unit grouped by Category).
  //
  // Reads: ListUnitObjects + GetUnitObject (Wails-bound) wrap the same
  // forge.MergedUnits view the MCP bridge exposes through objects.units.list
  // and objects.units.get — same merged base+w3u shadow.
  //
  // Writes (Phase 1b):
  //   - inline editor per field (Enter/blur commits via SetUnitObjectField);
  //   - "Add Custom Unit" button above the tree opens a small picker that
  //     calls CreateCustomUnit;
  //   - per-row delete (×) on custom units calls DeleteCustomUnit.
  //
  // Refresh: subscribes to wc3-forge:entity-changed with Kind==='unit_mod'
  // so external mutations (MCP, undo/redo) re-pull the tree + current detail.

  import { onMount, onDestroy } from 'svelte'
  import { ListUnitObjects, GetUnitObject } from '../wailsjs/go/main/App.js'
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
  import {
    SetUnitObjectField,
    CreateCustomUnit,
    DeleteCustomUnit,
  } from './object-editor-bindings'
  import type { main } from '../wailsjs/go/models'
  import * as Dialog from '$lib/components/ui/dialog'
  import { Input } from '$lib/components/ui/input'
  import { Button } from '$lib/components/ui/button'
  import { loadIconURL } from './icon-loader'
  import AssetPreview from './AssetPreview.svelte'

  let {
    open = $bindable(false),
    onClose,
    initialId,
    reforged = false,
  }: {
    open?: boolean
    onClose?: () => void
    initialId?: string | null
    reforged?: boolean
  } = $props()

  let rows: main.UnitObjectListEntity[] = $state([])
  let selectedId: string = $state('')
  let detail: main.UnitObjectDetail | null = $state(null)
  let loading: boolean = $state(false)
  let detailLoading: boolean = $state(false)
  let search: string = $state('')
  let errorMsg: string = $state('')
  // "Add Custom Unit" inline picker state. open === false hides the panel;
  // when true the user picks a base + optional id, and CreateCustomUnit is
  // called on confirm.
  let showAddCustom: boolean = $state(false)
  let addCustomBaseID: string = $state('')
  let addCustomNewID: string = $state('')

  // Reload whenever the dialog opens — the underlying merged view is
  // map-state-dependent and the entity-changed bus may have fired while
  // closed. Cheap enough to refresh unconditionally.
  $effect(() => {
    if (open) {
      reload().then(() => {
        if (initialId) selectUnit(initialId)
      })
    } else {
      selectedId = ''
      detail = null
      search = ''
      errorMsg = ''
      showAddCustom = false
    }
  })

  // Subscribe to the entity-changed bus while the dialog is open. unit_mod
  // events fire when SetUnitField / AddCustomUnit / DeleteCustomUnit run
  // (from this UI OR from MCP OR from undo/redo); we refresh the tree + the
  // currently-selected detail in response.
  const ENTITY_EVENT = 'wc3-forge:entity-changed'
  onMount(() => {
    EventsOn(ENTITY_EVENT, async (payload: { kind: string; field?: string }) => {
      if (!payload || payload.kind !== 'unit_mod') return
      // Either an "edit" (Field='unam' etc.) or a "customs" event. Both
      // need the tree refreshed (custom counts change; edited flag may
      // flip on a stock unit). Detail also re-pulled if a unit is selected.
      await reload()
      if (selectedId) {
        try {
          detail = await GetUnitObject(selectedId)
        } catch {
          /* selected unit may have been deleted; clear */
          detail = null
          selectedId = ''
        }
      }
    })
  })
  onDestroy(() => {
    EventsOff(ENTITY_EVENT)
  })

  async function reload() {
    loading = true
    try {
      rows = await ListUnitObjects()
    } catch (e) {
      console.error('ListUnitObjects failed', e)
      rows = []
    } finally {
      loading = false
    }
  }

  async function selectUnit(id: string) {
    selectedId = id
    detailLoading = true
    try {
      detail = await GetUnitObject(id)
    } catch (e) {
      console.error('GetUnitObject failed', e)
      detail = null
    } finally {
      detailLoading = false
    }
  }

  // Commit one field edit. value is the raw string from the input — server
  // infers wire-type (int/float/string) at Encode time based on the parse
  // result. Updates detail in-place from the returned payload so the
  // Overridden flag re-flags without a second round-trip.
  async function commitField(field: main.UnitObjectField, raw: string) {
    if (!detail) return
    if (raw === field.value) return // no-op
    errorMsg = ''
    try {
      const updated = await SetUnitObjectField(detail.id, field.field, raw)
      if (updated) detail = updated
    } catch (e) {
      errorMsg = String(e)
      console.error('SetUnitObjectField failed', e)
    }
  }

  // Build the bool-checkbox commit: WC3 stores bools as 1/0 strings.
  async function commitBoolField(field: main.UnitObjectField, checked: boolean) {
    return commitField(field, checked ? '1' : '0')
  }

  function openAddCustom() {
    addCustomNewID = ''
    // Default base: currently-selected unit if any; else first stock unit
    // in the tree (alphabetical by name).
    if (selectedId) {
      addCustomBaseID = selectedId
    } else if (rows.length > 0) {
      // Pick the first non-custom row alphabetically.
      const stock = rows.filter((r) => !r.is_custom)
      addCustomBaseID = stock[0]?.id ?? ''
    } else {
      addCustomBaseID = ''
    }
    showAddCustom = true
  }

  async function confirmAddCustom() {
    if (!addCustomBaseID) {
      errorMsg = 'pick a base unit'
      return
    }
    errorMsg = ''
    try {
      const res = await CreateCustomUnit(addCustomBaseID, addCustomNewID)
      showAddCustom = false
      if (res?.id) {
        // Reload tree + select the new id. The entity-changed event will
        // also fire reload as a backstop; harmless duplicate work.
        await reload()
        await selectUnit(res.id)
      }
    } catch (e) {
      errorMsg = String(e)
      console.error('CreateCustomUnit failed', e)
    }
  }

  async function deleteCustom(id: string) {
    if (!confirm(`Delete custom unit ${id}?`)) return
    errorMsg = ''
    try {
      await DeleteCustomUnit(id)
      // Clear selection if the deleted id was selected.
      if (selectedId === id) {
        selectedId = ''
        detail = null
      }
      await reload()
    } catch (e) {
      errorMsg = String(e)
      console.error('DeleteCustomUnit failed', e)
    }
  }

  // Filter rows by search. Substring match against id, name, and category.
  let filteredRows = $derived.by(() => {
    const q = search.trim().toLowerCase()
    if (!q) return rows
    return rows.filter(
      (r) =>
        r.id.toLowerCase().includes(q) ||
        r.name.toLowerCase().includes(q) ||
        r.category.toLowerCase().includes(q),
    )
  })

  // Stock units only — for the Add Custom Unit base picker. Customs can't
  // be a base (we don't support multi-level inheritance) and showing them
  // would clutter the dropdown.
  let stockRows = $derived.by(() => rows.filter((r) => !r.is_custom))

  const KIND_ORDER: Record<string, number> = {
    unit: 0,
    hero: 1,
    building: 2,
    special: 3,
  }
  const KIND_LABEL: Record<string, string> = {
    unit: 'Units',
    hero: 'Heroes',
    building: 'Buildings',
    special: 'Special',
  }

  interface KindBucket {
    kind: string
    label: string
    rows: main.UnitObjectListEntity[]
  }
  interface RaceBucket {
    race: string
    label: string
    kinds: KindBucket[]
  }

  let tree = $derived.by<RaceBucket[]>(() => {
    const byRace = new Map<string, RaceBucket>()
    for (const r of filteredRows) {
      let rb = byRace.get(r.race)
      if (!rb) {
        rb = { race: r.race, label: r.race_label || r.race, kinds: [] }
        byRace.set(r.race, rb)
      }
      let kb = rb.kinds.find((k) => k.kind === r.kind)
      if (!kb) {
        kb = { kind: r.kind, label: KIND_LABEL[r.kind] ?? r.kind, rows: [] }
        rb.kinds.push(kb)
      }
      kb.rows.push(r)
    }
    const out = Array.from(byRace.values())
    out.sort((a, b) => a.label.localeCompare(b.label))
    for (const r of out) {
      r.kinds.sort(
        (a, b) => (KIND_ORDER[a.kind] ?? 99) - (KIND_ORDER[b.kind] ?? 99),
      )
    }
    return out
  })

  const CATEGORY_ORDER: Record<string, number> = {
    text: 0,
    art: 1,
    stats: 2,
    combat: 3,
    abil: 4,
    move: 5,
    path: 6,
    sound: 7,
    tech: 8,
    editor: 9,
  }
  const CATEGORY_LABEL: Record<string, string> = {
    text: 'Text',
    art: 'Art',
    stats: 'Stats',
    combat: 'Combat',
    abil: 'Abilities',
    move: 'Movement',
    path: 'Pathing',
    sound: 'Sound',
    tech: 'Techtree',
    editor: 'Editor',
  }

  interface FieldGroup {
    category: string
    label: string
    fields: main.UnitObjectField[]
  }

  let fieldGroups = $derived.by<FieldGroup[]>(() => {
    if (!detail) return []
    const byCat = new Map<string, FieldGroup>()
    for (const f of detail.fields) {
      let g = byCat.get(f.category)
      if (!g) {
        g = {
          category: f.category,
          label: CATEGORY_LABEL[f.category] ?? f.category,
          fields: [],
        }
        byCat.set(f.category, g)
      }
      g.fields.push(f)
    }
    const out = Array.from(byCat.values())
    out.sort(
      (a, b) =>
        (CATEGORY_ORDER[a.category] ?? 99) -
        (CATEGORY_ORDER[b.category] ?? 99),
    )
    return out
  })

  // Field-type → input-widget categorization. We don't enumerate enum
  // values yet (Phase 3 polish); enums get a plain text input.
  function inputTypeFor(t: string): 'bool' | 'int' | 'real' | 'text' {
    switch (t) {
      case 'bool':
        return 'bool'
      case 'int':
      case 'unitCode':
        return 'int'
      case 'real':
      case 'unreal':
        return 'real'
      default:
        // string, model, icon, abilityList, all enums, … → text
        return 'text'
    }
  }

  function closeAndNotify(o: boolean) {
    if (!o) {
      onClose?.()
    }
    open = o
  }
</script>

<Dialog.Root bind:open={open} onOpenChange={closeAndNotify}>
  <Dialog.Content class="!max-w-[1200px] !w-[95vw] h-[85vh] flex flex-col p-0 gap-0">
    <Dialog.Header class="px-4 py-3 border-b border-border">
      <Dialog.Title>Object Editor — Units</Dialog.Title>
      <Dialog.Description class="text-xs text-muted-foreground">
        Edit unit fields; add or delete custom units. Edits persist on Save.
      </Dialog.Description>
    </Dialog.Header>

    {#if errorMsg}
      <div class="px-4 py-1.5 text-xs bg-red-950/40 text-red-300 border-b border-red-900">
        {errorMsg}
      </div>
    {/if}

    <div class="flex flex-1 min-h-0">
      <!-- Left pane: tree + add-custom button -->
      <aside class="w-80 flex-none border-r border-border flex flex-col min-h-0">
        <div class="p-2 border-b border-border flex flex-col gap-2">
          <div class="flex gap-2">
            <Input
              type="search"
              placeholder="Search units…"
              bind:value={search}
              class="h-8 text-sm flex-1"
            />
            <Button
              size="sm"
              variant="outline"
              class="h-8 text-xs px-2"
              onclick={openAddCustom}
              title="Add a new custom unit derived from a stock base"
            >+ Custom</Button>
          </div>
          {#if showAddCustom}
            <div class="p-2 rounded border border-border bg-muted/30 flex flex-col gap-1.5">
              <div class="text-xs text-muted-foreground">New custom unit</div>
              <label class="text-xs block">
                Base:
                <select
                  class="w-full mt-0.5 bg-background border border-border rounded text-xs px-1 py-0.5"
                  bind:value={addCustomBaseID}
                >
                  <option value="" disabled>— pick base —</option>
                  {#each stockRows as r (r.id)}
                    <option value={r.id}>{r.name} [{r.id}]</option>
                  {/each}
                </select>
              </label>
              <label class="text-xs block">
                ID (optional, autogen if blank):
                <Input
                  type="text"
                  bind:value={addCustomNewID}
                  placeholder="e.g. h001"
                  class="h-7 text-xs mt-0.5"
                  maxlength={4}
                />
              </label>
              <div class="flex gap-1.5 justify-end mt-1">
                <Button variant="ghost" size="sm" class="h-7 text-xs" onclick={() => (showAddCustom = false)}>
                  Cancel
                </Button>
                <Button size="sm" class="h-7 text-xs" onclick={confirmAddCustom}>
                  Create
                </Button>
              </div>
            </div>
          {/if}
        </div>
        <div class="flex-1 overflow-auto text-sm">
          {#if loading}
            <div class="p-3 text-muted-foreground">Loading…</div>
          {:else if tree.length === 0}
            <div class="p-3 text-muted-foreground">
              {search ? 'No matches.' : 'No units. Open a map first.'}
            </div>
          {:else}
            {#each tree as r (r.race)}
              <details open class="group">
                <summary class="px-2 py-1 cursor-pointer select-none font-medium hover:bg-accent">
                  {r.label}
                  <span class="text-xs text-muted-foreground ml-1">
                    ({r.kinds.reduce((n, k) => n + k.rows.length, 0)})
                  </span>
                </summary>
                {#each r.kinds as k (k.kind)}
                  <details open class="ml-2">
                    <summary class="px-2 py-0.5 cursor-pointer select-none text-xs text-muted-foreground hover:bg-accent">
                      {k.label}
                      <span class="ml-1">({k.rows.length})</span>
                    </summary>
                    <ul class="ml-4">
                      {#each k.rows as u (u.id)}
                        <li>
                          <div
                            class="oe-row flex items-center gap-1.5 hover:bg-accent"
                            class:bg-accent={selectedId === u.id}
                          >
                            <button
                              class="flex-1 text-left px-2 py-0.5 truncate flex items-center gap-1.5"
                              onclick={() => selectUnit(u.id)}
                              title={`${u.id} — ${u.category}`}
                            >
                              {#if u.icon_art}
                                {#await loadIconURL(u.icon_art) then iconURL}
                                  {#if iconURL}
                                    <img class="oe-icon" src={iconURL} alt="" />
                                  {:else}
                                    <span class="oe-icon oe-icon-placeholder" aria-hidden="true"></span>
                                  {/if}
                                {/await}
                              {:else}
                                <span class="oe-icon oe-icon-placeholder" aria-hidden="true"></span>
                              {/if}
                              <span class="truncate flex-1">{u.name}</span>
                              {#if u.is_custom}
                                <span
                                  class="text-[10px] px-1 rounded bg-emerald-700/30 text-emerald-300"
                                  title="Custom unit"
                                >C</span>
                              {:else if u.is_edited}
                                <span
                                  class="text-[10px] px-1 rounded bg-violet-700/30 text-violet-300"
                                  title="Stock unit with edits"
                                >M</span>
                              {/if}
                            </button>
                            {#if u.is_custom}
                              <button
                                class="oe-delete-btn text-xs text-muted-foreground hover:text-red-400 px-1.5"
                                onclick={() => deleteCustom(u.id)}
                                title="Delete custom unit"
                                aria-label="Delete custom unit"
                              >×</button>
                            {/if}
                          </div>
                        </li>
                      {/each}
                    </ul>
                  </details>
                {/each}
              </details>
            {/each}
          {/if}
        </div>
      </aside>

      <!-- Right pane: field table -->
      <section class="flex-1 flex flex-col min-h-0">
        {#if detailLoading}
          <div class="p-4 text-muted-foreground">Loading…</div>
        {:else if !detail}
          <div class="p-4 text-muted-foreground">
            Select a unit from the tree.
          </div>
        {:else}
          <header class="p-3 border-b border-border flex items-center gap-3">
            {#if detail.icon_art}
              {#await loadIconURL(detail.icon_art) then iconURL}
                {#if iconURL}
                  <img class="oe-icon-lg" src={iconURL} alt="" />
                {:else}
                  <span class="oe-icon-lg oe-icon-placeholder" aria-hidden="true"></span>
                {/if}
              {/await}
            {:else}
              <span class="oe-icon-lg oe-icon-placeholder" aria-hidden="true"></span>
            {/if}
            <div class="flex-1 min-w-0">
              <div class="font-medium text-base truncate">
                {detail.name || detail.id}
              </div>
              <div class="text-xs text-muted-foreground flex items-center gap-2 flex-wrap">
                <span class="font-mono">{detail.id}</span>
                {#if detail.base_id}
                  <span>base = <span class="font-mono">{detail.base_id}</span></span>
                {/if}
                {#if detail.is_custom}
                  <span class="text-emerald-400">Custom</span>
                {:else if detail.is_edited}
                  <span class="text-violet-400">Edited</span>
                {/if}
              </div>
            </div>
          </header>
          {#if detail.model_path}
            {#key detail.id}
              <div class="oe-preview border-b border-border">
                <AssetPreview
                  modelPath={detail.model_path}
                  modelPathFallbacks={detail.model_fallbacks ?? []}
                  {reforged}
                  teamColor={0}
                />
              </div>
            {/key}
          {/if}
          <div class="flex-1 overflow-auto px-3 py-2 text-sm">
            {#each fieldGroups as g (g.category)}
              <details open class="mb-3">
                <summary class="cursor-pointer select-none font-medium text-xs uppercase tracking-wide text-muted-foreground mb-1">
                  {g.label}
                </summary>
                <table class="w-full">
                  <tbody>
                    {#each g.fields as f (f.id)}
                      {@const widget = inputTypeFor(f.type)}
                      <tr
                        class="hover:bg-accent/30"
                        class:bg-violet-950={f.overridden}
                      >
                        <td
                          class="py-0.5 pr-2 align-top w-[40%] text-muted-foreground"
                          title={`${f.id} (${f.type})`}
                        >
                          {f.display_name || f.field}
                        </td>
                        <td class="py-0.5 align-top">
                          {#if widget === 'bool'}
                            <input
                              type="checkbox"
                              checked={f.value === '1'}
                              onchange={(e) =>
                                commitBoolField(
                                  f,
                                  (e.currentTarget as HTMLInputElement).checked,
                                )
                              }
                            />
                          {:else if widget === 'int'}
                            <input
                              type="number"
                              step="1"
                              class="oe-edit w-full bg-background border border-border rounded px-1.5 py-0.5 text-xs font-mono"
                              value={f.value}
                              onblur={(e) =>
                                commitField(
                                  f,
                                  (e.currentTarget as HTMLInputElement).value,
                                )
                              }
                              onkeydown={(e) => {
                                if (e.key === 'Enter') (e.currentTarget as HTMLInputElement).blur()
                              }}
                            />
                          {:else if widget === 'real'}
                            <input
                              type="number"
                              step="0.01"
                              class="oe-edit w-full bg-background border border-border rounded px-1.5 py-0.5 text-xs font-mono"
                              value={f.value}
                              onblur={(e) =>
                                commitField(
                                  f,
                                  (e.currentTarget as HTMLInputElement).value,
                                )
                              }
                              onkeydown={(e) => {
                                if (e.key === 'Enter') (e.currentTarget as HTMLInputElement).blur()
                              }}
                            />
                          {:else}
                            <input
                              type="text"
                              class="oe-edit w-full bg-background border border-border rounded px-1.5 py-0.5 text-xs font-mono"
                              value={f.value}
                              onblur={(e) =>
                                commitField(
                                  f,
                                  (e.currentTarget as HTMLInputElement).value,
                                )
                              }
                              onkeydown={(e) => {
                                if (e.key === 'Enter') (e.currentTarget as HTMLInputElement).blur()
                              }}
                            />
                          {/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </details>
            {/each}
          </div>
        {/if}
      </section>
    </div>

    <Dialog.Footer class="px-4 py-2 border-t border-border">
      <Button variant="outline" onclick={() => (open = false)}>Close</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<style>
  .oe-icon {
    width: 18px;
    height: 18px;
    object-fit: cover;
    border-radius: 2px;
    flex: none;
  }
  .oe-icon-lg {
    width: 40px;
    height: 40px;
    object-fit: cover;
    border-radius: 4px;
    flex: none;
  }
  .oe-icon-placeholder {
    background: rgb(var(--muted) / 0.4);
    display: inline-block;
  }
  .oe-preview {
    width: 100%;
    max-width: 360px;
    flex: none;
    padding: 8px 12px;
  }
  /* Delete button hidden until hover keeps the row visually clean — the
     usual editor convention for destructive per-row actions. */
  .oe-delete-btn {
    opacity: 0;
    transition: opacity 0.12s;
  }
  .oe-row:hover .oe-delete-btn {
    opacity: 1;
  }
  /* Compact edit input shouldn't visually shout — keep the same row
     height the read-only display had. */
  .oe-edit {
    height: 1.6em;
  }
</style>
