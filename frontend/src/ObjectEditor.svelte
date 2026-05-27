<script lang="ts">
  // Object Editor (Phase 2b — all seven kinds). Shadcn-svelte Dialog hosts a
  // three-pane layout: leftmost narrow sidebar with kind tabs (HiveWE-style),
  // middle pane with the per-kind tree, right pane with the per-field table
  // for the selected object.
  //
  // Reads + writes go through the generic Wails surface
  // (ListObjects/GetObject/SetObjectField/CreateCustomObject/DeleteCustomObject),
  // which dispatches via KindConfig on the Go side. Per-kind tree-building
  // is data-driven from each row's `category` field; tree depth and grouping
  // varies per kind (units use race→kind, items group by class, etc).
  //
  // Refresh: subscribes to wc3-forge:entity-changed with kind === current
  // kind + '_mod' (e.g. 'units_mod') so external mutations from MCP or
  // undo/redo re-pull the tree + current detail.

  import { onMount, onDestroy } from 'svelte'
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
  import {
    ListObjects,
    GetObject,
    SetObjectField,
    CreateCustomObject,
    DeleteCustomObject,
    ConvertObject,
    type ObjectKind,
  } from './object-editor-bindings'
  import type { main } from '../wailsjs/go/models'
  import * as Dialog from '$lib/components/ui/dialog'
  import { Input } from '$lib/components/ui/input'
  import { Button } from '$lib/components/ui/button'
  import { loadIconURL } from './icon-loader'
  import AssetPreview from './AssetPreview.svelte'
  import { parseWC3Color } from './wc3color'
  import IconPicker from './IconPicker.svelte'
  import ObjectSearchPalette from './ObjectSearchPalette.svelte'

  let {
    open = $bindable(false),
    onClose,
    initialId,
    initialKind = 'units',
    reforged = false,
  }: {
    open?: boolean
    onClose?: () => void
    initialId?: string | null
    initialKind?: ObjectKind
    reforged?: boolean
  } = $props()

  // Wire-stable per-kind ordering for the sidebar — mirrors HiveWE's left
  // tab strip. The kinds map to the registered KindConfig names on the Go
  // side; renaming here without renaming there breaks the bridge.
  interface KindTab {
    kind: ObjectKind
    label: string
    short: string // single-letter shortcut shown in the narrow tab strip
  }
  const KIND_TABS: KindTab[] = [
    { kind: 'units', label: 'Units', short: 'U' },
    { kind: 'items', label: 'Items', short: 'I' },
    { kind: 'doodads', label: 'Doodads', short: 'D' },
    { kind: 'destructables', label: 'Destructables', short: 'B' },
    { kind: 'abilities', label: 'Abilities', short: 'A' },
    { kind: 'buffs', label: 'Buffs', short: 'F' },
    { kind: 'upgrades', label: 'Upgrades', short: 'G' },
  ]

  // currentKind tracks the active sidebar tab. Default-initialized to
  // 'units'; the initialKind prop is applied via $effect on mount/open so
  // the lint doesn't flag a $state() seeded directly from a prop.
  let currentKind: ObjectKind = $state('units')
  let initialKindApplied = false
  let rows: main.UnitObjectListEntity[] = $state([])
  let selectedId: string = $state('')
  let detail: main.UnitObjectDetail | null = $state(null)
  let loading: boolean = $state(false)
  let detailLoading: boolean = $state(false)
  let search: string = $state('')
  let errorMsg: string = $state('')
  let showAddCustom: boolean = $state(false)
  let addCustomBaseID: string = $state('')
  let addCustomNewID: string = $state('')

  // Reload whenever the dialog opens OR the kind tab changes. Cheap enough
  // to refresh unconditionally — the per-kind base SLK is lazily cached on
  // the Go side, so subsequent loads are fast.
  //
  // Capture initialKind's first value once via a derived so the dialog-open
  // initial-id selection only triggers on the matching kind (and Svelte 5
  // doesn't warn about reading a $props default inside an effect).
  let initialKindFrozen = $derived(initialKind)
  $effect(() => {
    // Track open + currentKind explicitly.
    const _ = [open, currentKind]
    if (open) {
      // Apply initialKind ONCE on first open so subsequent tab switches don't
      // get overridden if the parent re-renders the prop.
      if (!initialKindApplied) {
        initialKindApplied = true
        if (initialKindFrozen !== currentKind) {
          currentKind = initialKindFrozen
          return // $effect re-fires with the new currentKind
        }
      }
      reload().then(() => {
        if (initialId && currentKind === initialKindFrozen) selectObject(initialId)
      })
    } else {
      initialKindApplied = false
      selectedId = ''
      detail = null
      search = ''
      errorMsg = ''
      showAddCustom = false
    }
  })

  // Subscribe to the entity-changed bus while the dialog is open.
  // <kind>_mod events fire when SetObjectField / AddCustomObject /
  // DeleteCustomObject run (from this UI OR from MCP OR from undo/redo); we
  // refresh the tree + the currently-selected detail in response. The bus is
  // kind-tagged so we only react to mutations on the kind we're showing.
  const ENTITY_EVENT = 'wc3-forge:entity-changed'
  onMount(() => {
    EventsOn(ENTITY_EVENT, async (payload: { kind: string; field?: string }) => {
      if (!payload) return
      const want = currentKind + '_mod'
      if (payload.kind !== want) return
      await reload()
      if (selectedId) {
        try {
          detail = await GetObject(currentKind, selectedId)
        } catch {
          detail = null
          selectedId = ''
        }
      }
    })
    window.addEventListener('keydown', onWindowKeydown)
  })
  onDestroy(() => {
    EventsOff(ENTITY_EVENT)
    window.removeEventListener('keydown', onWindowKeydown)
  })

  async function reload() {
    loading = true
    try {
      rows = await ListObjects(currentKind)
    } catch (e) {
      console.error('ListObjects failed', e)
      rows = []
    } finally {
      loading = false
    }
  }

  async function selectObject(id: string) {
    selectedId = id
    detailLoading = true
    try {
      detail = await GetObject(currentKind, id)
    } catch (e) {
      console.error('GetObject failed', e)
      detail = null
    } finally {
      detailLoading = false
    }
  }

  function switchKind(kind: ObjectKind) {
    if (kind === currentKind) return
    currentKind = kind
    selectedId = ''
    detail = null
    search = ''
    showAddCustom = false
    errorMsg = ''
    // reload runs via $effect when currentKind changes.
  }

  async function commitField(field: main.UnitObjectField, raw: string) {
    if (!detail) return
    if (raw === field.value) return // no-op
    errorMsg = ''
    try {
      const updated = await SetObjectField(currentKind, detail.id, field.field, raw)
      if (updated) detail = updated
    } catch (e) {
      errorMsg = String(e)
      console.error('SetObjectField failed', e)
    }
  }

  async function commitBoolField(field: main.UnitObjectField, checked: boolean) {
    return commitField(field, checked ? '1' : '0')
  }

  function openAddCustom() {
    addCustomNewID = ''
    if (selectedId) {
      addCustomBaseID = selectedId
    } else if (rows.length > 0) {
      const stock = rows.filter((r) => !r.is_custom)
      addCustomBaseID = stock[0]?.id ?? ''
    } else {
      addCustomBaseID = ''
    }
    showAddCustom = true
  }

  async function confirmAddCustom() {
    if (!addCustomBaseID) {
      errorMsg = 'pick a base'
      return
    }
    errorMsg = ''
    try {
      const res = await CreateCustomObject(currentKind, addCustomBaseID, addCustomNewID)
      showAddCustom = false
      if (res?.id) {
        await reload()
        await selectObject(res.id)
      }
    } catch (e) {
      errorMsg = String(e)
      console.error('CreateCustomObject failed', e)
    }
  }

  // Convert current selection between doodad ↔ destructable. Only enabled when
  // currentKind is doodads or destructables AND something is selected. The
  // Go side records the whole operation as a single undo group; on success we
  // switch the active tab to the destination kind and select the new id so the
  // user sees their conversion immediately.
  async function convertCurrentTo(dstKind: ObjectKind) {
    if (!detail || !selectedId) return
    errorMsg = ''
    try {
      const res = await ConvertObject(currentKind, detail.id, dstKind)
      // Switch to the destination kind first, THEN select the new id once the
      // reload completes. switchKind triggers a reload via $effect.
      currentKind = dstKind
      // Wait one tick so the $effect-driven reload has actually fetched rows
      // before we try to select; selectObject just fetches detail by id so it
      // doesn't strictly require the tree to be loaded, but the tree being
      // populated avoids a flash of "Select an object" in the middle pane.
      await reload()
      if (res?.id) {
        await selectObject(res.id)
      }
    } catch (e) {
      errorMsg = String(e)
      console.error('ConvertObject failed', e)
    }
  }

  async function deleteCustom(id: string) {
    if (!confirm(`Delete custom ${currentKind.replace(/s$/, '')} ${id}?`)) return
    errorMsg = ''
    try {
      await DeleteCustomObject(currentKind, id)
      if (selectedId === id) {
        selectedId = ''
        detail = null
      }
      await reload()
    } catch (e) {
      errorMsg = String(e)
      console.error('DeleteCustomObject failed', e)
    }
  }

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

  let stockRows = $derived.by(() => rows.filter((r) => !r.is_custom))

  // Per-kind tree structure. Units use a two-level race→kind tree; the
  // other six kinds use a single-level category grouping (HiveWE matches
  // this for items/doodads/destructables/abilities/buffs/upgrades).
  const UNIT_KIND_ORDER: Record<string, number> = {
    unit: 0,
    hero: 1,
    building: 2,
    special: 3,
  }
  const UNIT_KIND_LABEL: Record<string, string> = {
    unit: 'Units',
    hero: 'Heroes',
    building: 'Buildings',
    special: 'Special',
  }
  const ABILITY_KIND_LABEL: Record<string, string> = {
    unit: 'Unit Abilities',
    hero: 'Hero Abilities',
    item: 'Item Abilities',
  }
  const ABILITY_KIND_ORDER: Record<string, number> = { hero: 0, unit: 1, item: 2 }

  interface KindBucket {
    kind: string
    label: string
    rows: main.UnitObjectListEntity[]
  }
  interface CategoryBucket {
    category: string
    label: string
    kinds: KindBucket[]
  }

  // Tree shape varies per object kind. Units / abilities use a two-level
  // race→kind grouping; the rest collapse to a flat per-category list.
  let tree = $derived.by<CategoryBucket[]>(() => {
    if (currentKind === 'units' || currentKind === 'abilities') {
      const byRace = new Map<string, CategoryBucket>()
      const kindLabel = currentKind === 'units' ? UNIT_KIND_LABEL : ABILITY_KIND_LABEL
      const kindOrder = currentKind === 'units' ? UNIT_KIND_ORDER : ABILITY_KIND_ORDER
      for (const r of filteredRows) {
        let rb = byRace.get(r.race)
        if (!rb) {
          rb = {
            category: r.race,
            label: r.race_label || r.race || 'Other',
            kinds: [],
          }
          byRace.set(r.race, rb)
        }
        let kb = rb.kinds.find((k) => k.kind === r.kind)
        if (!kb) {
          kb = { kind: r.kind, label: kindLabel[r.kind] ?? r.kind, rows: [] }
          rb.kinds.push(kb)
        }
        kb.rows.push(r)
      }
      const out = Array.from(byRace.values())
      out.sort((a, b) => a.label.localeCompare(b.label))
      for (const r of out) {
        r.kinds.sort(
          (a, b) => (kindOrder[a.kind] ?? 99) - (kindOrder[b.kind] ?? 99),
        )
      }
      return out
    }
    // Single-level per-category tree for items/doodads/destructables/buffs/
    // upgrades. The KindConfig.CategoryFn produced the human-readable
    // category string at list time; we just bucket on it.
    const byCat = new Map<string, CategoryBucket>()
    for (const r of filteredRows) {
      const key = r.category || 'Miscellaneous'
      let cb = byCat.get(key)
      if (!cb) {
        cb = {
          category: key,
          label: key,
          kinds: [{ kind: 'all', label: '', rows: [] }],
        }
        byCat.set(key, cb)
      }
      cb.kinds[0].rows.push(r)
    }
    const out = Array.from(byCat.values())
    out.sort((a, b) => a.label.localeCompare(b.label))
    return out
  })

  const CATEGORY_ORDER: Record<string, number> = {
    text: 0,
    art: 1,
    stats: 2,
    combat: 3,
    abil: 4,
    data: 5,
    move: 6,
    path: 7,
    sound: 8,
    tech: 9,
    editor: 10,
  }
  const CATEGORY_LABEL: Record<string, string> = {
    text: 'Text',
    art: 'Art',
    stats: 'Stats',
    combat: 'Combat',
    abil: 'Abilities',
    data: 'Data',
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

  // Field IDs currently in edit-mode (focused input). Display-mode renders
  // colored segments via wc3color; edit-mode flips to a raw <input> so the
  // user can see + modify the literal |cAARRGGBB ... |r codes. Using a Set
  // keyed by metadata FourCC because the underlying object can re-render
  // (e.g. on entity-changed) without losing focus state.
  let editingFields: Set<string> = $state(new Set())

  function startEdit(id: string) {
    if (editingFields.has(id)) return
    editingFields = new Set([...editingFields, id])
  }

  function stopEdit(id: string) {
    if (!editingFields.has(id)) return
    const next = new Set(editingFields)
    next.delete(id)
    editingFields = next
  }

  // Global search palette (Cmd+P / Ctrl+P / double-Shift). State is local —
  // mounted at the bottom of this component since the dialog is what hosts
  // global keyboard while the user is editing objects.
  let searchPaletteOpen: boolean = $state(false)
  let lastShiftAt = 0
  const DOUBLE_SHIFT_MS = 300

  // window-level keydown listener — only fires while the editor dialog is open
  // so the shortcuts don't bleed into the rest of the app. Cmd+P / Ctrl+P
  // override the browser's print dialog (preventDefault). Double-Shift mirrors
  // HiveWE's "double-shift" behaviour, with a 300ms window.
  function onWindowKeydown(e: KeyboardEvent) {
    if (!open) return
    if ((e.ctrlKey || e.metaKey) && (e.key === 'p' || e.key === 'P')) {
      e.preventDefault()
      searchPaletteOpen = true
      return
    }
    if (e.key === 'Shift') {
      // Standalone shift press (no other modifier) — detect double-tap.
      if (e.ctrlKey || e.altKey || e.metaKey) return
      const now = performance.now()
      if (now - lastShiftAt <= DOUBLE_SHIFT_MS) {
        searchPaletteOpen = true
        lastShiftAt = 0
        return
      }
      lastShiftAt = now
    }
  }

  function onPaletteResult(kind: ObjectKind, id: string) {
    // Switch tab + select. switchKind drops the existing selection then the
    // $effect-driven reload populates rows; we then selectObject by id. We
    // intentionally don't await reload(); switchKind already triggers it.
    searchPaletteOpen = false
    if (kind !== currentKind) {
      currentKind = kind
      // Wait for the tab-switch reload to populate `rows` before selecting,
      // otherwise the tree pane shows the right object but the row appears
      // unhighlighted until the next render tick. selectObject only needs the
      // id; it fetches detail itself.
      void reload().then(() => selectObject(id))
    } else {
      void selectObject(id)
    }
  }

  // Icon-picker state. Open the modal with `openIconPicker(field)` from any
  // icon/art row; on commit, we route through commitField so the same dirty/
  // history/event pipeline fires as a manual edit.
  let iconPickerOpen: boolean = $state(false)
  let iconPickerField: main.UnitObjectField | null = $state(null)

  function openIconPicker(field: main.UnitObjectField) {
    iconPickerField = field
    iconPickerOpen = true
  }

  function onIconPicked(path: string) {
    if (iconPickerField) {
      void commitField(iconPickerField, path)
    }
    iconPickerField = null
    iconPickerOpen = false
  }

  function onIconPickerClose() {
    iconPickerField = null
  }

  // isIconField: spec says icon picker activates for `field.type === 'icon'`
  // OR `field.type === 'art'`. Some metadata rows use either string —
  // both refer to BLP/DDS command-button paths.
  function isIconField(t: string): boolean {
    return t === 'icon' || t === 'art'
  }

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
        return 'text'
    }
  }

  function closeAndNotify(o: boolean) {
    if (!o) {
      onClose?.()
    }
    open = o
  }

  // Dialog title reflects the current kind so the user always knows which
  // editor they're in. Titlecasing is mechanical — kinds are lowercase
  // wire-stable identifiers, the UI shows them capitalized.
  let dialogTitle = $derived.by(() => {
    const label = KIND_TABS.find((t) => t.kind === currentKind)?.label ?? currentKind
    return `Object Editor — ${label}`
  })

  // Singular-form noun used in the empty-state, add-custom button title,
  // and "stock" picker placeholder. Cheap rule: drop trailing 's'.
  let kindSingular = $derived.by(() => currentKind.replace(/s$/, ''))
</script>

<Dialog.Root bind:open={open} onOpenChange={closeAndNotify}>
  <Dialog.Content class="!max-w-[1280px] !w-[95vw] h-[85vh] flex flex-col p-0 gap-0">
    <Dialog.Header class="px-4 py-3 border-b border-border">
      <Dialog.Title>{dialogTitle}</Dialog.Title>
      <Dialog.Description class="text-xs text-muted-foreground">
        Edit fields, add or delete custom objects. Edits persist on Save.
      </Dialog.Description>
    </Dialog.Header>

    {#if errorMsg}
      <div class="px-4 py-1.5 text-xs bg-red-950/40 text-red-300 border-b border-red-900">
        {errorMsg}
      </div>
    {/if}

    <div class="flex flex-1 min-h-0">
      <!-- Leftmost: kind-picker sidebar (HiveWE-style vertical tab strip) -->
      <nav class="oe-kindbar border-r border-border bg-muted/20 flex flex-col">
        {#each KIND_TABS as t (t.kind)}
          <button
            class="oe-kindtab"
            class:oe-kindtab-active={currentKind === t.kind}
            onclick={() => switchKind(t.kind)}
            title={t.label}
            aria-label={t.label}
            aria-current={currentKind === t.kind ? 'page' : undefined}
          >
            <span class="oe-kindtab-short">{t.short}</span>
            <span class="oe-kindtab-label">{t.label}</span>
          </button>
        {/each}
      </nav>

      <!-- Middle: tree + add-custom button -->
      <aside class="w-80 flex-none border-r border-border flex flex-col min-h-0">
        <div class="p-2 border-b border-border flex flex-col gap-2">
          <div class="flex gap-2">
            <Input
              type="search"
              placeholder={`Search ${currentKind}…`}
              bind:value={search}
              class="h-8 text-sm flex-1"
            />
            <Button
              size="sm"
              variant="outline"
              class="h-8 text-xs px-2"
              onclick={openAddCustom}
              title={`Add a new custom ${kindSingular} derived from a stock base`}
            >+ Custom</Button>
          </div>
          {#if showAddCustom}
            <div class="p-2 rounded border border-border bg-muted/30 flex flex-col gap-1.5">
              <div class="text-xs text-muted-foreground">
                New custom {kindSingular}
              </div>
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
              {#if search}
                No matches.
              {:else if rows.length === 0}
                No {currentKind}. Metadata not loaded (open a map first or
                check that the WC3 install is reachable for the kind's
                MetaData.slk).
              {:else}
                Empty.
              {/if}
            </div>
          {:else}
            {#each tree as r (r.category)}
              <details open class="group">
                <summary class="px-2 py-1 cursor-pointer select-none font-medium hover:bg-accent">
                  {r.label}
                  <span class="text-xs text-muted-foreground ml-1">
                    ({r.kinds.reduce((n, k) => n + k.rows.length, 0)})
                  </span>
                </summary>
                {#each r.kinds as k (k.kind)}
                  {#if k.label}
                    <details open class="ml-2">
                      <summary class="px-2 py-0.5 cursor-pointer select-none text-xs text-muted-foreground hover:bg-accent">
                        {k.label}
                        <span class="ml-1">({k.rows.length})</span>
                      </summary>
                      <ul class="ml-4">
                        {#each k.rows as u (u.id)}
                          {@render objectRow(u)}
                        {/each}
                      </ul>
                    </details>
                  {:else}
                    <!-- Flat kind: render rows directly under the category -->
                    <ul class="ml-2">
                      {#each k.rows as u (u.id)}
                        {@render objectRow(u)}
                      {/each}
                    </ul>
                  {/if}
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
            Select a {kindSingular} from the tree.
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
            {#if currentKind === 'doodads'}
              <Button
                size="sm"
                variant="outline"
                class="h-7 text-xs"
                onclick={() => convertCurrentTo('destructables')}
                title="Convert this doodad to a destructable (creates a new custom; stock doodads remain)"
              >→ Destructable</Button>
            {:else if currentKind === 'destructables'}
              <Button
                size="sm"
                variant="outline"
                class="h-7 text-xs"
                onclick={() => convertCurrentTo('doodads')}
                title="Convert this destructable to a doodad (creates a new custom; stock destructables remain)"
              >→ Doodad</Button>
            {/if}
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
                          {:else if editingFields.has(f.id)}
                            <div class="flex items-center gap-1">
                              {#if isIconField(f.type)}
                                {#await loadIconURL(f.value) then iconURL}
                                  {#if iconURL}
                                    <img class="oe-fld-icon" src={iconURL} alt="" />
                                  {:else}
                                    <span class="oe-fld-icon oe-icon-placeholder"></span>
                                  {/if}
                                {/await}
                              {/if}
                              <input
                                type="text"
                                class="oe-edit flex-1 bg-background border border-border rounded px-1.5 py-0.5 text-xs font-mono"
                                value={f.value}
                                autofocus
                                onblur={(e) => {
                                  commitField(
                                    f,
                                    (e.currentTarget as HTMLInputElement).value,
                                  )
                                  stopEdit(f.id)
                                }}
                                onkeydown={(e) => {
                                  if (e.key === 'Enter') (e.currentTarget as HTMLInputElement).blur()
                                  else if (e.key === 'Escape') stopEdit(f.id)
                                }}
                              />
                              {#if isIconField(f.type)}
                                <button
                                  type="button"
                                  class="oe-fld-pick text-xs px-1.5 py-0.5 rounded border border-border hover:bg-accent"
                                  onclick={() => openIconPicker(f)}
                                  title="Pick from icon library"
                                >Pick…</button>
                              {/if}
                            </div>
                          {:else}
                            <!-- Display mode: render WC3 color codes as inline
                                 colored spans (parser preserves segment
                                 boundaries even for unterminated |c). Click
                                 to enter edit mode, which flips to a raw
                                 input so the user can edit the codes. Icon-
                                 typed fields also show the live thumbnail +
                                 a Pick… button alongside the text. -->
                            <div class="flex items-center gap-1">
                              {#if isIconField(f.type)}
                                {#await loadIconURL(f.value) then iconURL}
                                  {#if iconURL}
                                    <img class="oe-fld-icon" src={iconURL} alt="" />
                                  {:else}
                                    <span class="oe-fld-icon oe-icon-placeholder"></span>
                                  {/if}
                                {/await}
                              {/if}
                              <button
                                type="button"
                                class="oe-edit oe-display flex-1 text-left bg-background border border-transparent rounded px-1.5 py-0.5 text-xs font-mono hover:border-border"
                                onclick={() => startEdit(f.id)}
                                title={f.value}
                              >
                                {#if f.value}
                                  {#each parseWC3Color(f.display_raw || f.display || f.value) as seg, i (i)}
                                    <span style={seg.color ? `color: ${seg.color}` : undefined}
                                    >{seg.text}</span>
                                  {/each}
                                {:else}
                                  <span class="text-muted-foreground">—</span>
                                {/if}
                              </button>
                              {#if isIconField(f.type)}
                                <button
                                  type="button"
                                  class="oe-fld-pick text-xs px-1.5 py-0.5 rounded border border-border hover:bg-accent"
                                  onclick={() => openIconPicker(f)}
                                  title="Pick from icon library"
                                >Pick…</button>
                              {/if}
                            </div>
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

<IconPicker
  bind:open={iconPickerOpen}
  currentValue={iconPickerField?.value ?? ''}
  onPick={onIconPicked}
  onClose={onIconPickerClose}
/>

<ObjectSearchPalette
  bind:open={searchPaletteOpen}
  onPick={onPaletteResult}
/>

{#snippet objectRow(u: main.UnitObjectListEntity)}
  <li>
    <div
      class="oe-row flex items-center gap-1.5 hover:bg-accent"
      class:bg-accent={selectedId === u.id}
    >
      <button
        class="flex-1 text-left px-2 py-0.5 truncate flex items-center gap-1.5"
        onclick={() => selectObject(u.id)}
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
            title="Custom"
          >C</span>
        {:else if u.is_edited}
          <span
            class="text-[10px] px-1 rounded bg-violet-700/30 text-violet-300"
            title="Edited stock object"
          >M</span>
        {/if}
      </button>
      {#if u.is_custom}
        <button
          class="oe-delete-btn text-xs text-muted-foreground hover:text-red-400 px-1.5"
          onclick={() => deleteCustom(u.id)}
          title="Delete custom"
          aria-label="Delete custom"
        >×</button>
      {/if}
    </div>
  </li>
{/snippet}

<style>
  .oe-kindbar {
    width: 90px;
    flex: none;
  }
  .oe-kindtab {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 2px;
    padding: 8px 4px;
    border: 0;
    background: transparent;
    color: rgb(var(--muted-foreground));
    font-size: 0.7rem;
    cursor: pointer;
    border-bottom: 1px solid rgb(var(--border));
  }
  .oe-kindtab:hover {
    background: rgb(var(--accent));
    color: rgb(var(--foreground));
  }
  .oe-kindtab-active {
    background: rgb(var(--accent));
    color: rgb(var(--foreground));
    box-shadow: inset 2px 0 0 rgb(var(--primary));
  }
  .oe-kindtab-short {
    font-family: var(--font-mono, monospace);
    font-size: 0.85rem;
    font-weight: 600;
  }
  .oe-kindtab-label {
    font-size: 0.65rem;
    letter-spacing: 0.02em;
    text-align: center;
    word-break: break-word;
    line-height: 1.1;
  }
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
  .oe-delete-btn {
    opacity: 0;
    transition: opacity 0.12s;
  }
  .oe-row:hover .oe-delete-btn {
    opacity: 1;
  }
  .oe-edit {
    height: 1.6em;
  }
  .oe-fld-icon {
    width: 22px;
    height: 22px;
    object-fit: cover;
    border-radius: 2px;
    flex: none;
  }
  .oe-fld-pick {
    flex: none;
  }
</style>
