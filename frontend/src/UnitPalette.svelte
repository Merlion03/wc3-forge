<script lang="ts">
  // Floating unit palette. Renders a small unit-glyph button pinned to the
  // bottom-left of the viewport (just above the Doodad Palette FAB); clicking
  // it toggles a floating panel that lists the full unit catalog (stock + map
  // overrides) grouped by category, with search. Clicking a unit icon ARMS it
  // for placement — the parent (App.svelte) puts the scene into placement mode
  // and drops a unit (owned by the selected player slot) on the next map click.
  // Clicking the armed unit again (or pressing Cancel) disarms.
  //
  // A "Start Location" toggle arms placement of a start location (sloc) instead
  // of a unit — the next map click drops a start location at the next free
  // start-location index (gg_start_location_<index>). Existing start locations
  // are listed with move/delete affordances.
  //
  // The component owns no placement logic itself: it surfaces the catalog +
  // the player selector + the start-location list, and reports the armed
  // intent via onArmUnit / onArmStartLoc. App.svelte owns the scene wiring so
  // the palette stays a pure presentation layer (same convention as the
  // Doodad Palette and the other panels — mutation lives in the scene/Go).
  import { GetUnitTypeIndex } from '../wailsjs/go/main/App.js'
  import { unitModelPaths } from './model-thumbnail'
  import { TEAM_COLORS_RGB } from './sloc-markers'
  import DoodadThumb from './DoodadThumb.svelte'
  import UsersIcon from '@lucide/svelte/icons/users'
  import XIcon from '@lucide/svelte/icons/x'
  import SearchIcon from '@lucide/svelte/icons/search'
  import ChevronRightIcon from '@lucide/svelte/icons/chevron-right'
  import FlagIcon from '@lucide/svelte/icons/flag'
  import Trash2Icon from '@lucide/svelte/icons/trash-2'

  // Must stay in lockstep with main.UnitTypeInfo (Wails drops map-valued struct
  // typedefs from models.ts — same local-declare pattern App.svelte uses).
  interface UnitTypeInfo {
    file: string
    model_scale: number
    move_height: number
    red: number
    green: number
    blue: number
    name: string
    category: string
    icon_art: string
  }

  // One existing start location (mirror of forge.StartLocationInfo). Supplied
  // by the parent so the panel can list + offer move/delete.
  export interface StartLocEntry {
    index: number
    creationNumber: number
    position: [number, number, number]
  }

  let {
    armedTypeId = null,
    armedPlayer = 0,
    startLocArmed = false,
    startLocations = [],
    reforged = false,
    onArmUnit,
    onSetPlayer,
    onArmStartLoc,
    onDeleteStartLoc,
  }: {
    // The unit type_id currently armed for placement (null = none). Owned by
    // the parent; we render the matching icon as selected.
    armedTypeId?: string | null
    // The player slot new units are placed for (0..n). Owned by the parent.
    armedPlayer?: number
    // Whether start-location placement is armed (the next click drops a sloc).
    startLocArmed?: boolean
    // Existing start locations, for the list + delete affordance.
    startLocations?: StartLocEntry[]
    // App asset mode — forwarded to the thumbnail renderer so HD/SD models
    // resolve correctly.
    reforged?: boolean
    // Fired when the user picks/unpicks a unit. Pass a type_id to arm it, or
    // null to disarm (clicking the already-armed unit).
    onArmUnit: (typeId: string | null) => void
    // Fired when the user changes the owning player slot.
    onSetPlayer: (player: number) => void
    // Fired when the Start-Location toggle flips (true = arm sloc placement).
    onArmStartLoc: (armed: boolean) => void
    // Fired when the user deletes an existing start location (by index).
    onDeleteStartLoc: (index: number) => void
  } = $props()

  let open = $state(false)
  let loaded = $state(false)
  let loading = $state(false)
  let query = $state('')
  let types: Record<string, UnitTypeInfo> = $state({})

  // Player slots offered in the owner selector: 0..11 (named colors) plus the
  // two neutral slots maps commonly place creeps/shops under. Matches the
  // labels App.svelte's playerLabel uses.
  const PLAYER_SLOTS = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 15]
  function playerLabel(p: number): string {
    const colors = ['Red', 'Blue', 'Teal', 'Purple', 'Yellow', 'Orange', 'Green',
                    'Pink', 'Gray', 'LightBlue', 'DarkGreen', 'Brown']
    if (p === 15) return 'Neutral Passive (15)'
    if (p === 12) return 'Neutral Aggressive (12)'
    if (p < colors.length) return `${colors[p]} (${p})`
    return `Player ${p}`
  }
  function playerColorCSS(p: number): string {
    const rgb = p < TEAM_COLORS_RGB.length ? TEAM_COLORS_RGB[p] : [0.55, 0.15, 0.15]
    return `rgb(${Math.round(rgb[0] * 255)}, ${Math.round(rgb[1] * 255)}, ${Math.round(rgb[2] * 255)})`
  }

  // Curated-first ordering. Unit categories come from UnitData.slk's `category`
  // column resolved to display names (e.g. "Human", "Orc", "Special", "Item").
  // We don't hard-code an exhaustive list — just float the common race buckets
  // to the top and alphabetize the rest, with "Uncategorized" last.
  const CAT_ORDER = [
    'Human', 'Orc', 'Undead', 'Night Elf', 'Neutral Hostile',
    'Neutral Passive', 'Special', 'Item',
  ]

  interface PaletteEntry {
    typeId: string; name: string; category: string; iconArt: string; file: string
  }
  interface PaletteGroup { category: string; entries: PaletteEntry[] }

  // Collapsed-category state. Default: all collapsed except the first group, so
  // the panel opens light. Searching auto-expands matches.
  let collapsed: Record<string, boolean> = $state({})

  async function ensureLoaded() {
    if (loaded || loading) return
    loading = true
    try {
      types = (await GetUnitTypeIndex()) as unknown as Record<string, UnitTypeInfo>
      loaded = true
    } catch {
      types = {}
    } finally {
      loading = false
    }
  }

  function toggleOpen() {
    open = !open
    if (open) void ensureLoaded()
  }

  // Build category-grouped, search-filtered entries. Only units with a model
  // file can be placed (and previewed), so skip the rest — same gate the
  // Doodad Palette uses.
  let groups = $derived.by<PaletteGroup[]>(() => {
    const q = query.trim().toLowerCase()
    const buckets = new Map<string, PaletteEntry[]>()
    for (const typeId of Object.keys(types)) {
      const info = types[typeId]
      if (!info || !info.file) continue // no model → can't place / preview
      const name = info.name && info.name.length ? info.name : typeId
      const category = info.category && info.category.length ? info.category : 'Uncategorized'
      if (q) {
        const hay = (name + ' ' + category + ' ' + typeId).toLowerCase()
        if (!hay.includes(q)) continue
      }
      let arr = buckets.get(category)
      if (!arr) { arr = []; buckets.set(category, arr) }
      arr.push({ typeId, name, category, iconArt: info.icon_art || '', file: info.file || '' })
    }
    for (const arr of buckets.values()) arr.sort((a, b) => a.name.localeCompare(b.name))
    const out: PaletteGroup[] = []
    for (const cat of CAT_ORDER) {
      const arr = buckets.get(cat)
      if (arr && arr.length) { out.push({ category: cat, entries: arr }); buckets.delete(cat) }
    }
    const rest = [...buckets.keys()].sort((a, b) => {
      if (a === 'Uncategorized') return 1
      if (b === 'Uncategorized') return -1
      return a.localeCompare(b)
    })
    for (const cat of rest) out.push({ category: cat, entries: buckets.get(cat)! })
    return out
  })

  let totalCount = $derived(groups.reduce((n, g) => n + g.entries.length, 0))

  function isExpanded(category: string, index: number): boolean {
    if (query.trim()) return true
    if (collapsed[category] === undefined) return index === 0
    return !collapsed[category]
  }
  function toggleCategory(category: string, index: number) {
    const currentlyExpanded = collapsed[category] === undefined ? index === 0 : !collapsed[category]
    collapsed = { ...collapsed, [category]: currentlyExpanded }
  }

  function pick(typeId: string) {
    onArmUnit(typeId === armedTypeId ? null : typeId)
  }
</script>

<!-- Floating launcher button (bottom-left, above the Doodad Palette FAB). -->
<button
  class="palette-fab"
  class:active={open}
  onclick={toggleOpen}
  title={open ? 'Close unit palette' : 'Open unit palette — place units & start locations'}
  aria-label="Unit palette"
  aria-expanded={open}
>
  <UsersIcon size={18} />
</button>

{#if open}
  <div class="palette" role="dialog" aria-label="Unit palette">
    <div class="palette-header">
      <span class="palette-title">Units</span>
      {#if totalCount > 0}
        <span class="palette-count">{totalCount}</span>
      {/if}
      <button class="palette-close" onclick={toggleOpen} title="Close" aria-label="Close palette">
        <XIcon size={15} />
      </button>
    </div>

    <!-- Owner (player) selector — new units are created for this slot. -->
    <div class="palette-owner">
      <span class="owner-swatch" style="background:{playerColorCSS(armedPlayer)}"></span>
      <label class="owner-label" for="unit-owner-select">Owner</label>
      <select
        id="unit-owner-select"
        value={armedPlayer}
        onchange={(e) => onSetPlayer(Number((e.currentTarget as HTMLSelectElement).value))}
      >
        {#each PLAYER_SLOTS as p (p)}
          <option value={p}>{playerLabel(p)}</option>
        {/each}
      </select>
    </div>

    <!-- Start-location toggle + existing list. -->
    <div class="sloc-section">
      <button
        class="sloc-toggle"
        class:armed={startLocArmed}
        onclick={() => onArmStartLoc(!startLocArmed)}
        title="Place a start location (sloc) at the next free index"
      >
        <FlagIcon size={14} />
        <span>{startLocArmed ? 'Placing start location… (click map)' : 'Place start location'}</span>
      </button>
      {#if startLocations.length}
        <ul class="sloc-list">
          {#each startLocations as sl (sl.index)}
            <li class="sloc-row">
              <FlagIcon size={12} class="sloc-row-icon" />
              <span class="sloc-row-label">Start {sl.index}</span>
              <span class="sloc-row-pos">({Math.round(sl.position[0])}, {Math.round(sl.position[1])})</span>
              <button
                class="sloc-row-del"
                title="Delete start location {sl.index}"
                aria-label="Delete start location {sl.index}"
                onclick={() => onDeleteStartLoc(sl.index)}
              >
                <Trash2Icon size={12} />
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>

    <div class="palette-search">
      <SearchIcon size={14} class="search-icon" />
      <input
        type="text"
        placeholder="Search units…"
        bind:value={query}
        spellcheck="false"
        autocomplete="off"
      />
    </div>

    {#if armedTypeId}
      <div class="palette-armed">
        <span class="dot"></span>
        <span class="armed-text">Click the map to place. <kbd>Esc</kbd> to cancel.</span>
        <button class="armed-cancel" onclick={() => onArmUnit(null)}>Cancel</button>
      </div>
    {/if}

    <div class="palette-body">
      {#if loading}
        <div class="palette-empty">Loading catalog…</div>
      {:else if totalCount === 0}
        <div class="palette-empty">{query.trim() ? 'No matching units.' : 'No units available.'}</div>
      {:else}
        {#each groups as group, gi (group.category)}
          {@const expanded = isExpanded(group.category, gi)}
          <div class="cat-section">
            <button class="cat-header" onclick={() => toggleCategory(group.category, gi)}>
              <span class="cat-chevron" class:open={expanded}><ChevronRightIcon size={13} /></span>
              <span class="cat-name">{group.category}</span>
              <span class="cat-count">{group.entries.length}</span>
            </button>
            {#if expanded}
              <div class="cat-grid">
                {#each group.entries as entry (entry.typeId)}
                  <button
                    class="unit-btn"
                    class:armed={entry.typeId === armedTypeId}
                    title={entry.name}
                    onclick={() => pick(entry.typeId)}
                  >
                    <DoodadThumb
                      typeId={entry.typeId}
                      modelPaths={unitModelPaths(entry.file)}
                      iconArt={entry.iconArt}
                      {reforged}
                      alt={entry.name}
                    />
                    <span class="unit-label">{entry.name}</span>
                  </button>
                {/each}
              </div>
            {/if}
          </div>
        {/each}
      {/if}
    </div>
  </div>
{/if}

<style>
  .palette-fab {
    position: absolute;
    left: 12px;
    bottom: 60px;
    z-index: 40;
    width: 40px;
    height: 40px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 9999px;
    border: 1px solid var(--border);
    background: var(--secondary, var(--card));
    color: var(--secondary-foreground, var(--card-foreground));
    box-shadow: 0 2px 8px rgb(0 0 0 / 0.35);
    cursor: pointer;
    transition: transform 0.12s ease, background 0.12s ease;
  }
  .palette-fab:hover { transform: translateY(-1px); }
  .palette-fab.active {
    background: var(--accent);
    color: var(--accent-foreground);
  }

  .palette {
    position: absolute;
    left: 12px;
    bottom: 108px;
    z-index: 40;
    width: 300px;
    max-height: min(560px, calc(100vh - 160px));
    display: flex;
    flex-direction: column;
    background: var(--card);
    color: var(--card-foreground);
    border: 1px solid var(--border);
    border-radius: var(--radius, 8px);
    box-shadow: 0 8px 28px rgb(0 0 0 / 0.45);
    overflow: hidden;
  }

  .palette-header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
  }
  .palette-title { font-size: 0.8125rem; font-weight: 600; }
  .palette-count {
    font-size: 0.6875rem;
    color: var(--muted-foreground);
    background: var(--muted);
    border-radius: 9999px;
    padding: 1px 7px;
  }
  .palette-close {
    margin-left: auto;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 22px; height: 22px;
    border-radius: 4px;
    color: var(--muted-foreground);
    cursor: pointer;
  }
  .palette-close:hover { background: var(--accent); color: var(--accent-foreground); }

  .palette-owner {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 7px 10px;
    border-bottom: 1px solid var(--border);
  }
  .owner-swatch {
    width: 12px; height: 12px; border-radius: 3px;
    border: 1px solid rgb(0 0 0 / 0.4);
    flex: none;
  }
  .owner-label { font-size: 0.75rem; color: var(--muted-foreground); }
  .palette-owner select {
    flex: 1;
    padding: 3px 6px;
    font-size: 0.75rem;
    color: var(--foreground);
    background: var(--background);
    border: 1px solid var(--border);
    border-radius: 6px;
    outline: none;
  }
  .palette-owner select:focus { border-color: var(--ring); }

  .sloc-section {
    padding: 7px 10px;
    border-bottom: 1px solid var(--border);
  }
  .sloc-toggle {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 5px 8px;
    font-size: 0.75rem;
    color: var(--foreground);
    background: var(--background);
    border: 1px solid var(--border);
    border-radius: 6px;
    cursor: pointer;
  }
  .sloc-toggle:hover { background: var(--accent); }
  .sloc-toggle.armed {
    border-color: var(--primary);
    background: color-mix(in oklch, var(--primary) 18%, transparent);
    color: var(--foreground);
  }
  .sloc-list {
    margin: 6px 0 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .sloc-row {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 2px 4px;
    font-size: 0.6875rem;
    color: var(--muted-foreground);
    border-radius: 4px;
  }
  .sloc-row:hover { background: var(--accent); }
  .sloc-row :global(.sloc-row-icon) { color: var(--primary); flex: none; }
  .sloc-row-label { color: var(--foreground); font-weight: 500; }
  .sloc-row-pos { margin-left: auto; }
  .sloc-row-del {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 18px; height: 18px;
    border-radius: 4px;
    color: var(--muted-foreground);
    cursor: pointer;
  }
  .sloc-row-del:hover { background: var(--destructive, #c0392b); color: #fff; }

  .palette-search {
    position: relative;
    display: flex;
    align-items: center;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
  }
  .palette-search :global(.search-icon) {
    position: absolute;
    left: 18px;
    color: var(--muted-foreground);
    pointer-events: none;
  }
  .palette-search input {
    width: 100%;
    padding: 5px 8px 5px 28px;
    font-size: 0.8125rem;
    color: var(--foreground);
    background: var(--background);
    border: 1px solid var(--border);
    border-radius: 6px;
    outline: none;
  }
  .palette-search input:focus { border-color: var(--ring); }

  .palette-armed {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 6px 10px;
    font-size: 0.75rem;
    background: color-mix(in oklch, var(--primary) 14%, transparent);
    border-bottom: 1px solid var(--border);
  }
  .palette-armed .dot {
    width: 8px; height: 8px; border-radius: 9999px;
    background: var(--primary);
    flex: none;
  }
  .palette-armed .armed-text { color: var(--foreground); }
  .palette-armed kbd {
    font-size: 0.6875rem;
    padding: 0 4px;
    border-radius: 3px;
    background: var(--muted);
    border: 1px solid var(--border);
  }
  .palette-armed .armed-cancel {
    margin-left: auto;
    font-size: 0.6875rem;
    color: var(--muted-foreground);
    text-decoration: underline;
    cursor: pointer;
  }
  .palette-armed .armed-cancel:hover { color: var(--foreground); }

  .palette-body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 4px 0 8px;
  }
  .palette-empty {
    padding: 20px 12px;
    text-align: center;
    font-size: 0.8125rem;
    color: var(--muted-foreground);
  }

  .cat-section { border-bottom: 1px solid color-mix(in oklch, var(--border) 60%, transparent); }
  .cat-header {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 6px 10px;
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--foreground);
    cursor: pointer;
  }
  .cat-header:hover { background: var(--accent); }
  .cat-chevron {
    display: inline-flex;
    color: var(--muted-foreground);
    transition: transform 0.12s ease;
  }
  .cat-chevron.open { transform: rotate(90deg); }
  .cat-name { flex: 1; text-align: left; }
  .cat-count { font-size: 0.6875rem; color: var(--muted-foreground); }

  .cat-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 4px;
    padding: 4px 8px 8px;
  }
  .unit-btn {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    padding: 4px 2px;
    border: 1px solid transparent;
    border-radius: 6px;
    background: var(--background);
    cursor: pointer;
    overflow: hidden;
  }
  .unit-btn:hover { background: var(--accent); border-color: var(--border); }
  .unit-btn.armed {
    border-color: var(--primary);
    background: color-mix(in oklch, var(--primary) 18%, transparent);
  }
  .unit-label {
    width: 100%;
    font-size: 0.625rem;
    line-height: 1.1;
    text-align: center;
    color: var(--muted-foreground);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
