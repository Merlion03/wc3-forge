<script lang="ts">
  // Phase 1a Object Editor: read-only view of map units. Shadcn-svelte Dialog
  // hosts a two-pane layout — left tree (Race → Kind → Unit) with search;
  // right table of fields for the selected unit, grouped by the same
  // categories HiveWE's UnitMetaData drives (text, stats, combat, art, …).
  //
  // Wire format: ListUnitObjects() returns flat rows; the tree is built
  // client-side by grouping on race+kind. GetUnitObject(id) returns the
  // ordered field list for one unit; we re-bucket by Category for the
  // collapsible sections. Both APIs are Wails-bound to the same forge.*
  // funcs the MCP bridge exposes — Wails surface and MCP wire stay in
  // lockstep, so a future agent-driven workflow sees identical data.

  import { ListUnitObjects, GetUnitObject } from '../wailsjs/go/main/App.js'
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

  // Reload whenever the dialog opens — the underlying merged view is
  // map-state-dependent, and a save-back path in a future phase will need
  // this re-fetch to reflect new shadow data. Cheap enough to refresh
  // unconditionally rather than tracking dirty-state across opens.
  $effect(() => {
    if (open) {
      reload().then(() => {
        if (initialId) selectUnit(initialId)
      })
    } else {
      selectedId = ''
      detail = null
      search = ''
    }
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

  // Filter rows by search. Substring match against id, name, and category.
  // Lowercased once per keystroke; the row count is small enough to filter
  // synchronously per keystroke without debouncing.
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

  // Tree: Race → Kind → [Unit]. Order: race alpha, kind in conventional
  // order (unit, hero, building, special), unit by name.
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

  // Field table for the selected unit: group fields by Category. Within a
  // category, fields are already pre-sorted server-side (by Index then
  // display name). HiveWE's category order is the natural reading order
  // for unit data — Text first (Name, Description), then visible/visual
  // (Art), then numeric (Stats, Combat), then specialized.
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
        Read-only view (Phase 1a). Showing merged stock base + map customizations.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex flex-1 min-h-0">
      <!-- Left pane: tree -->
      <aside class="w-80 flex-none border-r border-border flex flex-col min-h-0">
        <div class="p-2 border-b border-border">
          <Input
            type="search"
            placeholder="Search units…"
            bind:value={search}
            class="h-8 text-sm"
          />
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
                          <button
                            class="w-full text-left px-2 py-0.5 truncate hover:bg-accent flex items-center gap-1.5"
                            class:bg-accent={selectedId === u.id}
                            onclick={() => selectUnit(u.id)}
                            title={`${u.id} — ${u.category}`}
                          >
                            <!-- Per-row command-button icon; same pattern the
                                 Explorer uses. The asset HTTP handler routes
                                 /asset/<path> through map-first-then-CASC and
                                 swaps BLP↔DDS siblings, so the path the Go
                                 side hands us resolves regardless of which
                                 extension actually shipped. -->
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
            <!-- 3D model preview. Same component the Properties panel uses
                 for placed-entity previews; reuses the asset HTTP handler's
                 map-first-then-CASC + extension-swap routing. Fixed height
                 keeps the preview from squeezing the field table on short
                 windows. Keyed by the unit id so swapping selection fully
                 unmounts the previous viewer (own GL context, own RAF
                 loop) instead of re-loading inside a stale one. -->
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
                        <td class="py-0.5 align-top font-mono text-xs break-all">
                          {f.display || f.value || '—'}
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
  /* Tree-row icon. WC3 command-button BLPs are square; 18px keeps row
     height tight while staying readable at typical zoom. flex-none stops
     the icon from being squeezed when a long unit name fills the cell. */
  .oe-icon {
    width: 18px;
    height: 18px;
    object-fit: cover;
    border-radius: 2px;
    flex: none;
  }
  /* Larger icon in the detail header — gives the selected unit a clear
     visual anchor matching the WC3 World Editor / HiveWE convention. */
  .oe-icon-lg {
    width: 40px;
    height: 40px;
    object-fit: cover;
    border-radius: 4px;
    flex: none;
  }
  /* Placeholder for rows whose `art` column is blank (rare, mostly
     special/internal units). Same dimensions so layout doesn't shift on
     icon-load failure. */
  .oe-icon-placeholder {
    background: rgb(var(--muted) / 0.4);
    display: inline-block;
  }
  /* 3D preview pane — width-constrained, not height-constrained.
     AssetPreview's inner wrapper carries `aspect-[4/3] w-full` so its
     height is driven by width — fixing height fights the aspect ratio
     and produces a clipped canvas. Capping width at 360px yields a
     ~270px-tall preview that anchors the detail pane without overwhelming
     the field table below. flex-none stops the flex column from
     stretching it vertically. */
  .oe-preview {
    width: 100%;
    max-width: 360px;
    flex: none;
    padding: 8px 12px;
  }
</style>
