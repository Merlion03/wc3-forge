<script lang="ts">
  // Floating doodad palette. Renders a small "+" button pinned to the bottom-
  // left of the viewport; clicking it toggles a floating panel that lists the
  // full doodad catalog (stock + map overrides) grouped by category, with
  // search. Clicking a doodad icon ARMS it for placement — the parent
  // (App.svelte) puts the scene into placement mode and drops a doodad on the
  // next map click. Clicking the armed doodad again (or pressing the panel's
  // disarm hint) cancels.
  //
  // The component owns no placement logic itself: it surfaces the catalog and
  // reports the armed type via onArm. App.svelte owns the scene wiring so the
  // palette stays a pure presentation layer (same convention as the other
  // panels — selection + mutation live in the scene/Go, not in the panel).
  import { GetDoodadTypeIndex } from '../wailsjs/go/main/App.js'
  import { doodadModelPaths } from './model-thumbnail'
  import DoodadThumb from './DoodadThumb.svelte'
  import PlusIcon from '@lucide/svelte/icons/plus'
  import XIcon from '@lucide/svelte/icons/x'
  import SearchIcon from '@lucide/svelte/icons/search'
  import ChevronRightIcon from '@lucide/svelte/icons/chevron-right'

  // Must stay in lockstep with main.DoodadTypeInfo (Wails drops map-valued
  // struct typedefs from models.ts — same local-declare pattern App.svelte uses).
  interface DoodadTypeInfo {
    file: string
    num_var: number
    name: string
    category: string
    icon_art: string
  }

  let {
    armedTypeId = null,
    reforged = false,
    onArm,
  }: {
    // The type_id currently armed for placement (null = none). Owned by the
    // parent; we render the matching icon as selected.
    armedTypeId?: string | null
    // App asset mode — forwarded to the thumbnail renderer so HD/SD models
    // resolve correctly.
    reforged?: boolean
    // Fired when the user picks/unpicks a doodad. Pass a type_id to arm it,
    // or null to disarm (clicking the already-armed doodad).
    onArm: (typeId: string | null) => void
  } = $props()

  let open = $state(false)
  let loaded = $state(false)
  let loading = $state(false)
  let query = $state('')
  let types: Record<string, DoodadTypeInfo> = $state({})

  // Curated-first ordering, mirrors App.svelte's DOODAD_CAT_ORDER + the scene's.
  const CAT_ORDER = [
    'Trees/Destructibles', 'Structures', 'Props', 'Bridges/Ramps',
    'Cliff/Terrain', 'Terrain', 'Water', 'Environment',
    'Pathing Blockers', 'Cinematic',
  ]

  interface PaletteEntry {
    typeId: string; name: string; category: string; iconArt: string
    file: string; numVar: number
  }
  interface PaletteGroup { category: string; entries: PaletteEntry[] }

  // Collapsed-category state. Default: all collapsed except the first group,
  // so the panel opens light (a few hundred icons across the full catalog
  // would otherwise all decode at once). Searching auto-expands matches.
  let collapsed: Record<string, boolean> = $state({})

  async function ensureLoaded() {
    if (loaded || loading) return
    loading = true
    try {
      types = (await GetDoodadTypeIndex()) as unknown as Record<string, DoodadTypeInfo>
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

  // Build category-grouped, search-filtered entries. Curated categories first,
  // then the rest alphabetized with "Uncategorized" last — same shape as the
  // Explorer's doodad buckets so the two panels read consistently.
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
      arr.push({
        typeId, name, category,
        iconArt: info.icon_art || '',
        file: info.file || '',
        numVar: info.num_var || 0,
      })
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

  // A category renders its icons when it's not collapsed. While searching,
  // every matching group is forced open (collapse state is ignored) so results
  // are visible without per-section clicking.
  function isExpanded(category: string, index: number): boolean {
    if (query.trim()) return true
    // Default-expand the first group only; everything else honors `collapsed`,
    // which starts undefined (→ collapsed) for all but index 0.
    if (collapsed[category] === undefined) return index === 0
    return !collapsed[category]
  }

  function toggleCategory(category: string, index: number) {
    const currentlyExpanded = collapsed[category] === undefined ? index === 0 : !collapsed[category]
    collapsed = { ...collapsed, [category]: currentlyExpanded } // expanded → collapse
  }

  function pick(typeId: string) {
    onArm(typeId === armedTypeId ? null : typeId)
  }
</script>

<!-- Floating launcher button (bottom-left). Always visible; the panel floats
     above it when open. -->
<button
  class="palette-fab"
  class:active={open}
  onclick={toggleOpen}
  title={open ? 'Close doodad palette' : 'Open doodad palette — place doodads on the map'}
  aria-label="Doodad palette"
  aria-expanded={open}
>
  <PlusIcon size={20} />
</button>

{#if open}
  <div class="palette" role="dialog" aria-label="Doodad palette">
    <div class="palette-header">
      <span class="palette-title">Doodads</span>
      {#if totalCount > 0}
        <span class="palette-count">{totalCount}</span>
      {/if}
      <button class="palette-close" onclick={toggleOpen} title="Close" aria-label="Close palette">
        <XIcon size={15} />
      </button>
    </div>

    <div class="palette-search">
      <SearchIcon size={14} class="search-icon" />
      <input
        type="text"
        placeholder="Search doodads…"
        bind:value={query}
        spellcheck="false"
        autocomplete="off"
      />
    </div>

    {#if armedTypeId}
      <div class="palette-armed">
        <span class="dot"></span>
        <span class="armed-text">Click the map to place. <kbd>Esc</kbd> to cancel.</span>
        <button class="armed-cancel" onclick={() => onArm(null)}>Cancel</button>
      </div>
    {/if}

    <div class="palette-body">
      {#if loading}
        <div class="palette-empty">Loading catalog…</div>
      {:else if totalCount === 0}
        <div class="palette-empty">{query.trim() ? 'No matching doodads.' : 'No doodads available.'}</div>
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
                    class="doodad-btn"
                    class:armed={entry.typeId === armedTypeId}
                    title={entry.name}
                    onclick={() => pick(entry.typeId)}
                  >
                    <DoodadThumb
                      typeId={entry.typeId}
                      modelPaths={doodadModelPaths(entry.file, entry.numVar)}
                      iconArt={entry.iconArt}
                      {reforged}
                      alt={entry.name}
                    />
                    <span class="doodad-label">{entry.name}</span>
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
    bottom: 12px;
    z-index: 40;
    width: 40px;
    height: 40px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 9999px;
    border: 1px solid var(--border);
    background: var(--primary);
    color: var(--primary-foreground);
    box-shadow: 0 2px 8px rgb(0 0 0 / 0.35);
    cursor: pointer;
    transition: transform 0.12s ease, background 0.12s ease;
  }
  .palette-fab:hover { transform: translateY(-1px); }
  .palette-fab.active {
    background: var(--accent);
    color: var(--accent-foreground);
    transform: rotate(45deg);
  }

  .palette {
    position: absolute;
    left: 12px;
    bottom: 60px;
    z-index: 40;
    width: 300px;
    max-height: min(560px, calc(100vh - 120px));
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
  .doodad-btn {
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
  .doodad-btn:hover { background: var(--accent); border-color: var(--border); }
  .doodad-btn.armed {
    border-color: var(--primary);
    background: color-mix(in oklch, var(--primary) 18%, transparent);
  }
  .doodad-label {
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
