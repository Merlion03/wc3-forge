<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import {
    OpenMapDialog, OpenMap, CloseMap, ListUnits, ListDoodads, Status,
    GetSelection, SetSelection, GetUnit,
    GetReforgedMode, SetReforgedMode,
    GetUnitTypeIndex, GetDoodadTypeIndex,
  } from '../wailsjs/go/main/App.js'
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
  import type { main, unitsdoo } from '../wailsjs/go/models'
  import { createScene, type SceneAPI, type PickHit, type SelectMode } from './scene-instances'

  // Wails drops struct typedefs from models.ts when they appear as map values,
  // so the unit/doodad type-index shapes are declared locally here. Must stay
  // in lockstep with main.UnitTypeInfo / main.DoodadTypeInfo on the Go side.
  interface UnitTypeInfo {
    file: string; model_scale: number; move_height: number
    red: number; green: number; blue: number
    name: string; category: string
  }
  interface DoodadTypeInfo {
    file: string; num_var: number; fixed_rot: number; model_scale: number
    name: string; category: string
  }

  let status: main.MapStatus = { loaded: false, unit_count: 0 }
  let units: main.UnitDTO[] = []
  let doodads: main.DoodadDTO[] = []
  let unitTypes: Record<string, UnitTypeInfo> = {}
  let doodadTypes: Record<string, DoodadTypeInfo> = {}
  // Selection state is split by kind because creation_number is per-kind in
  // WC3 — a unit and a doodad can share an id, so a single Set can't tell
  // them apart. The full SelectionItemDTO[] mirror (selectionItems) is what
  // we use to compose new selections on mode='add'/'toggle' picks.
  let selectedIds = new Set<number>()           // unit creation numbers
  let selectedDoodadIds = new Set<number>()     // doodad creation numbers
  let selectionItems: main.SelectionItemDTO[] = []
  let primaryEntity: unitsdoo.Entity | null = null
  let primaryDoodad: main.DoodadDTO | null = null
  let error: string = ''
  let busy: boolean = false
  let reforged: boolean = false

  let canvas: HTMLCanvasElement
  let scene: SceneAPI | null = null

  const SEL_EVENT = 'wc3-forge:selection-changed'
  const MAP_EVENT = 'wc3-forge:map-changed'
  const DEV_ANIM_EVENT = 'wc3-forge:dev-set-anim'

  onMount(async () => {
    try {
      // Pull initial mode from Go so reloads of the UI (HMR, route) honor
      // any persistent setting once we add one. Currently session-only.
      try { reforged = await GetReforgedMode() } catch { reforged = false }
      scene = createScene(canvas, reforged)
      scene.onPick(handlePick)
      // Devtools / screenshot-automation hook: lets external test drivers
      // pump scene-level operations without needing keyboard simulation.
      ;(window as any).__scene = scene
    } catch (e) {
      error = 'scene init failed: ' + (e instanceof Error ? (e.stack || e.message) : String(e))
      console.error(e)
    }
    // Map changes from any source (App method, MCP bridge, --open flag).
    EventsOn(MAP_EVENT, async () => {
      status = await Status()
      if (status.loaded) {
        await reloadMap()
      } else {
        units = []
        doodads = []
        unitTypes = {}
        doodadTypes = {}
        selectedIds = new Set()
        selectedDoodadIds = new Set()
        selectionItems = []
        primaryEntity = null
        primaryDoodad = null
      }
    })
    // Dev-only animation poke from App.SetUnitAnimation (devtools / MCP).
    // Payload: { creation_number: number, anim_name: string }
    EventsOn(DEV_ANIM_EVENT, (payload: { creation_number: number; anim_name: string }) => {
      scene?.setUnitAnimation(payload.creation_number, payload.anim_name)
    })
    EventsOn(SEL_EVENT, async (s: main.SelectionDTO) => {
      ingestSelection(s)
      // Primary entity fetch — kind-aware so doodad primaries don't crash
      // through GetUnit and stay perma-"Loading…".
      const items = s.items || []
      if (items.length === 0) {
        primaryEntity = null
        primaryDoodad = null
        return
      }
      const idx = Math.max(0, Math.min(s.primary, items.length - 1))
      const primary = items[idx]
      if (primary.kind === 'doodad') {
        primaryEntity = null
        primaryDoodad = doodads.find(d => d.creation_number === primary.id) ?? null
      } else {
        primaryDoodad = null
        try { primaryEntity = await GetUnit(primary.id) } catch { primaryEntity = null }
      }
    })

    const s = await Status()
    status = s
    if (s.loaded) await reloadMap()
    const sel = await GetSelection()
    ingestSelection(sel)
  })

  // Mirror Go-side selection into the local split sets + the scene-side tint.
  // The scene's tint maps are keyed by kind, so we must hand it units and
  // doodads separately — see scene-instances.setSelected.
  function ingestSelection(s: main.SelectionDTO) {
    const items = s.items || []
    const u = new Set<number>()
    const d = new Set<number>()
    for (const it of items) {
      if (it.kind === 'doodad') d.add(it.id)
      else u.add(it.id) // 'unit' | 'item' | any future kind that lives in units.doo
    }
    selectedIds = u
    selectedDoodadIds = d
    selectionItems = items
    scene?.setSelected(u, d)
  }

  // Picker entry point. The scene fires hits + a combine-mode based on
  // modifier keys (set/add/toggle); we compose against the current Go-side
  // selection and push the result back through App.SetSelection. The Go
  // side owns canonical selection state, so we always round-trip through it
  // rather than mutating local state and hoping the event catches up.
  async function handlePick(hits: PickHit[], mode: SelectMode) {
    const next = composeSelection(selectionItems, hits, mode)
    await SetSelection(next.map(it => ({ kind: it.kind, id: it.id })))
  }

  // Pure composition: takes the current selection + new hits + mode, returns
  // the new selection array. Items are de-duplicated by (kind, id).
  function composeSelection(
    current: main.SelectionItemDTO[],
    hits: PickHit[],
    mode: SelectMode,
  ): main.SelectionItemDTO[] {
    const key = (kind: string, id: number) => `${kind}:${id}`
    if (mode === 'set') {
      const seen = new Set<string>()
      const out: main.SelectionItemDTO[] = []
      for (const h of hits) {
        const k = key(h.kind, h.id)
        if (seen.has(k)) continue
        seen.add(k)
        out.push({ kind: h.kind, id: h.id })
      }
      return out
    }
    const map = new Map<string, main.SelectionItemDTO>()
    for (const it of current) map.set(key(it.kind, it.id), { kind: it.kind, id: it.id })
    if (mode === 'add') {
      for (const h of hits) map.set(key(h.kind, h.id), { kind: h.kind, id: h.id })
    } else { // toggle
      for (const h of hits) {
        const k = key(h.kind, h.id)
        if (map.has(k)) map.delete(k)
        else map.set(k, { kind: h.kind, id: h.id })
      }
    }
    return [...map.values()]
  }

  onDestroy(() => {
    EventsOff(SEL_EVENT)
    EventsOff(MAP_EVENT)
    EventsOff(DEV_ANIM_EVENT)
    scene?.dispose()
  })

  async function pickAndOpen() {
    error = ''
    busy = true
    try {
      const path = await OpenMapDialog()
      if (!path) { busy = false; return }
      status = await OpenMap(path)
      await reloadMap()
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }

  async function reloadMap(opts?: { keepCamera?: boolean }) {
    units = await ListUnits()
    doodads = await ListDoodads()
    // Per-map indexes — overlay (w3u/w3d/w3b/w3t) changes per map.
    try { unitTypes = (await GetUnitTypeIndex()) as unknown as Record<string, UnitTypeInfo> } catch { unitTypes = {} }
    try { doodadTypes = (await GetDoodadTypeIndex()) as unknown as Record<string, DoodadTypeInfo> } catch { doodadTypes = {} }
    // The viewport pulls its own data via App.* methods now; no need to
    // marshal the raw .w3x bytes across the boundary.
    await scene?.loadMap(opts)
  }

  async function toggleReforged() {
    if (busy) return
    busy = true
    error = ''
    try {
      const next = !reforged
      // Push to Go first — it owns the canonical state (CASC prefix order,
      // asset-handler sibling preference), then flip the scene which will
      // drop cached models/textures.
      reforged = await SetReforgedMode(next)
      scene?.setReforgedMode(reforged)
      // Re-load the current map so all models + team-color textures come
      // back through the new mode, BUT keep the camera where the user
      // panned it — re-framing on an in-place reload throws them back to
      // the default view they just left.
      if (status.loaded) {
        await reloadMap({ keepCamera: true })
      }
    } catch (e) {
      error = 'toggle reforged failed: ' + String(e)
    } finally {
      busy = false
    }
  }

  async function close() {
    busy = true
    try {
      status = await CloseMap()
      units = []
      doodads = []
      selectedIds = new Set()
      selectedDoodadIds = new Set()
      selectionItems = []
      primaryEntity = null
      primaryDoodad = null
      // No clean "unload map" in mdx-m3-viewer; we'd recreate the viewer
      // on close. For now the canvas just keeps the last-loaded map until
      // a new one is opened.
    } finally {
      busy = false
    }
  }

  // Row click → kind-aware single-select via Go. Mirrors plain-click in the
  // viewport (mode='set'). Modifier-held row clicks fall through to the same
  // composer so shift/ctrl in the Explorer behaves like in the viewport.
  async function clickRow(e: MouseEvent, kind: 'unit' | 'doodad', id: number) {
    const mode: SelectMode = e.ctrlKey || e.metaKey
      ? 'toggle'
      : (e.shiftKey ? 'add' : 'set')
    const next = composeSelection(selectionItems, [{ kind, id }], mode)
    await SetSelection(next.map(it => ({ kind: it.kind, id: it.id })))
  }

  function panToEntity(e: Event, pos: number[]) {
    e.stopPropagation()  // don't trigger row selection
    if (pos && pos.length >= 2) {
      scene?.panTo(pos[0], pos[1])
    }
  }

  // ----- Explorer categorization -----
  //
  // Categorization uses the SLK-derived type index when available:
  //   "sloc" → Markers
  //   info.category contains "Hero" → Heroes
  //   everything else → Units & Items
  //
  // Display name + category come from unitTypes[type_id]. Falls back to the
  // FourCC when the row is unknown (custom map types with empty Name fields,
  // or pre-Reforged retired types) so the entity is still listable.

  type Group = { id: string; label: string; entries: main.UnitDTO[] }
  $: groups = bucket(units, unitTypes)
  function unitDisplayName(u: main.UnitDTO): string {
    const info = unitTypes[u.type_id]
    return info && info.name ? info.name : u.type_id
  }
  function unitCategory(u: main.UnitDTO): string {
    const info = unitTypes[u.type_id]
    return info ? info.category : ''
  }
  function bucket(us: main.UnitDTO[], types: Record<string, UnitTypeInfo>): Group[] {
    const markers: main.UnitDTO[] = []
    const heroes: main.UnitDTO[] = []
    const others: main.UnitDTO[] = []
    for (const u of us) {
      if (u.type_id === 'sloc') { markers.push(u); continue }
      const info = types[u.type_id]
      const isHero = info ? /Hero/i.test(info.category)
        : u.type_id.length > 0 && u.type_id[0] >= 'A' && u.type_id[0] <= 'Z'
      if (isHero) heroes.push(u)
      else others.push(u)
    }
    const out: Group[] = []
    if (heroes.length) out.push({ id: 'heroes', label: 'Heroes', entries: heroes })
    if (others.length) out.push({ id: 'units', label: 'Units & Items', entries: others })
    if (markers.length) out.push({ id: 'markers', label: 'Markers', entries: markers })
    return out
  }

  // Doodad count for Explorer header.
  $: doodadCount = doodads.length

  // ----- Doodad explorer grouping -----
  //
  // Doodads bucket by their SLK `category` column, resolved via the WESTRING
  // table (typeindex.go: doodadCategoryKeys → "Trees/Destructibles",
  // "Structures", "Cliff/Terrain", etc.). Unknown / empty categories collapse
  // into a single "Uncategorized" group so derived custom types without a
  // category override still surface in the list.
  //
  // Groups are emitted in a stable curated order — the rest of the buckets
  // come after, alphabetized, so a map that introduces a novel category via
  // custom doodads still shows up predictably.
  type DGroup = { id: string; label: string; entries: main.DoodadDTO[] }
  $: doodadGroups = bucketDoodads(doodads, doodadTypes)
  function doodadDisplayName(d: main.DoodadDTO): string {
    const info = doodadTypes[d.type_id]
    return info && info.name ? info.name : d.type_id
  }
  function doodadCategoryFor(d: main.DoodadDTO): string {
    const info = doodadTypes[d.type_id]
    return info ? info.category : ''
  }
  const DOODAD_CAT_ORDER = [
    'Trees/Destructibles',
    'Structures',
    'Props',
    'Bridges/Ramps',
    'Cliff/Terrain',
    'Terrain',
    'Water',
    'Environment',
    'Pathing Blockers',
    'Cinematic',
  ]
  function bucketDoodads(ds: main.DoodadDTO[], types: Record<string, DoodadTypeInfo>): DGroup[] {
    const buckets = new Map<string, main.DoodadDTO[]>()
    for (const d of ds) {
      const info = types[d.type_id]
      const cat = (info && info.category) ? info.category : 'Uncategorized'
      let arr = buckets.get(cat)
      if (!arr) { arr = []; buckets.set(cat, arr) }
      arr.push(d)
    }
    const out: DGroup[] = []
    // Curated order first.
    for (const label of DOODAD_CAT_ORDER) {
      const arr = buckets.get(label)
      if (arr && arr.length) {
        out.push({ id: 'd:' + label, label, entries: arr })
        buckets.delete(label)
      }
    }
    // Remaining categories alphabetized; "Uncategorized" pinned last.
    const rest = [...buckets.keys()].sort((a, b) => {
      if (a === 'Uncategorized') return 1
      if (b === 'Uncategorized') return -1
      return a.localeCompare(b)
    })
    for (const label of rest) {
      out.push({ id: 'd:' + label, label, entries: buckets.get(label)! })
    }
    return out
  }

  // ----- Properties helpers -----

  function fmt(n: number, decimals: number = 0): string {
    return n.toFixed(decimals)
  }
  function fmtVec3(v: number[]): string {
    return `(${fmt(v[0])}, ${fmt(v[1])}, ${fmt(v[2])})`
  }
  function fmtScale(v: number[]): string {
    return `(${fmt(v[0], 2)}, ${fmt(v[1], 2)}, ${fmt(v[2], 2)})`
  }
  function playerLabel(p: number): string {
    const colors = ['Red', 'Blue', 'Teal', 'Purple', 'Yellow', 'Orange', 'Green',
                    'Pink', 'Gray', 'LightBlue', 'DarkGreen', 'Brown']
    if (p === 15) return 'Neutral Passive (15)'
    if (p === 12) return 'Neutral Aggressive (12)'
    if (p < colors.length) return `${colors[p]} (${p})`
    return `Player ${p}`
  }
  function isHero(e: unitsdoo.Entity): boolean {
    return e.HeroLevel > 0 || (e.TypeID.length > 0 && e.TypeID[0] >= 'A' && e.TypeID[0] <= 'Z')
  }
</script>

<main>
  <header>
    <h1>wc3-forge</h1>
    <div class="status-strip">
      {#if status.loaded}
        <span class="map-name">{status.name || '(untitled)'}</span>
        <span class="sep">·</span>
        <span class="map-count">{status.unit_count} entities</span>
      {/if}
    </div>
    <div class="actions">
      <button on:click={toggleReforged} disabled={busy}
              class="mode-toggle"
              class:on={reforged}
              title="Toggle Reforged graphics. Reloads the current map without resetting the camera.">
        Reforged Graphics{reforged ? ' ✓' : ''}
      </button>
      <button on:click={pickAndOpen} disabled={busy}>Open Map…</button>
      {#if status.loaded}
        <button on:click={close} disabled={busy} class="secondary">Close</button>
      {/if}
    </div>
  </header>

  {#if error}<div class="error"><pre>{error}</pre></div>{/if}

  <div class="split">
    <aside class="panel explorer">
      <header class="panel-header">Explorer</header>
      {#if !status.loaded}
        <div class="empty">No map loaded.</div>
      {:else}
        {#each groups as g (g.id)}
          <div class="category">
            <header class="cat-header">{g.label} <span class="count">{g.entries.length}</span></header>
            <ul>
              {#each g.entries as u (u.creation_number)}
                <li class:selected={selectedIds.has(u.creation_number)}
                    on:click={(e) => clickRow(e, 'unit', u.creation_number)}
                    title="{u.type_id} #{u.creation_number}">
                  <span class="name">{unitDisplayName(u)}</span>
                  <span class="cat dim">{unitCategory(u)}</span>
                  <button class="pan-btn"
                          on:click={(e) => panToEntity(e, u.position)}
                          title="Pan camera to this entity">⊕</button>
                </li>
              {/each}
            </ul>
          </div>
        {/each}
        {#if doodadCount > 0}
          <div class="category">
            <header class="cat-header">Doodads <span class="count">{doodadCount}</span></header>
          </div>
          {#each doodadGroups as g (g.id)}
            <div class="category subcategory">
              <header class="cat-header sub">{g.label} <span class="count">{g.entries.length}</span></header>
              <ul>
                {#each g.entries as d (d.creation_number)}
                  <li class:selected={selectedDoodadIds.has(d.creation_number)}
                      on:click={(e) => clickRow(e, 'doodad', d.creation_number)}
                      title="{d.type_id} #{d.creation_number}">
                    <span class="name">{doodadDisplayName(d)}</span>
                    <span class="cat dim">{doodadCategoryFor(d)}</span>
                    <button class="pan-btn"
                            on:click={(e) => panToEntity(e, d.position)}
                            title="Pan camera to this doodad">⊕</button>
                  </li>
                {/each}
              </ul>
            </div>
          {/each}
        {/if}
      {/if}
    </aside>

    <section class="viewport">
      <canvas bind:this={canvas}></canvas>
    </section>

    <aside class="panel properties">
      <header class="panel-header">Properties</header>
      {#if primaryDoodad}
        {@const d = primaryDoodad}
        <dl class="props">
          <dt>Kind</dt>               <dd>Doodad</dd>
          <dt>Type ID</dt>            <dd class="mono">{d.type_id}</dd>
          {#if d.skin_id && d.skin_id !== d.type_id}
            <dt>Skin ID</dt>          <dd class="mono">{d.skin_id}</dd>
          {/if}
          <dt>Creation #</dt>         <dd class="mono">{d.creation_number}</dd>
          <dt>Name</dt>               <dd>{doodadDisplayName(d)}</dd>
          <dt>Category</dt>           <dd>{doodadCategoryFor(d)}</dd>

          <dt class="section">Transform</dt>
          <dt>Position</dt>           <dd class="mono">{fmtVec3(d.position)}</dd>
          <dt>Rotation</dt>           <dd class="mono">{fmt(d.rotation, 2)}</dd>
          <dt>Scale</dt>              <dd class="mono">{fmtScale(d.scale)}</dd>
          <dt>Variation</dt>          <dd>{d.variation}</dd>

          {#if d.life !== 0xFF}
            <dt class="section">Destructible</dt>
            <dt>Life %</dt>           <dd>{d.life}%</dd>
          {/if}
        </dl>
      {:else if !primaryEntity}
        <div class="empty">
          {#if selectedIds.size === 0 && selectedDoodadIds.size === 0}
            Select an entity to see its properties.
          {:else}
            Loading…
          {/if}
        </div>
      {:else}
        {@const e = primaryEntity}
        <dl class="props">
          <dt>Type ID</dt>            <dd class="mono">{e.TypeID}</dd>
          {#if e.SkinID && e.SkinID !== e.TypeID}
            <dt>Skin ID</dt>          <dd class="mono">{e.SkinID}</dd>
          {/if}
          <dt>Creation #</dt>         <dd class="mono">{e.CreationNumber}</dd>
          <dt>Player</dt>             <dd>{playerLabel(e.Player)}</dd>

          <dt class="section">Transform</dt>
          <dt>Position</dt>           <dd class="mono">{fmtVec3(e.Position)}</dd>
          <dt>Rotation</dt>           <dd class="mono">{fmt(e.Rotation, 2)}</dd>
          <dt>Scale</dt>              <dd class="mono">{fmtScale(e.Scale)}</dd>
          <dt>Variation</dt>          <dd>{e.Variation}</dd>

          <dt class="section">Status</dt>
          <dt>HP %</dt>               <dd>{e.HitPointsPct < 0 ? 'default' : e.HitPointsPct + '%'}</dd>
          <dt>Mana %</dt>             <dd>{e.ManaPct < 0 ? 'default' : e.ManaPct + '%'}</dd>
          {#if e.GoldAmount > 0}
            <dt>Gold</dt>             <dd>{e.GoldAmount}</dd>
          {/if}
          {#if e.TargetAcquisition !== 0}
            <dt>Acquisition</dt>      <dd class="mono">{fmt(e.TargetAcquisition, 1)}</dd>
          {/if}

          {#if isHero(e)}
            <dt class="section">Hero</dt>
            <dt>Level</dt>            <dd>{e.HeroLevel || 1}</dd>
            {#if e.HeroStr > 0 || e.HeroAgi > 0 || e.HeroInt > 0}
              <dt>Stats</dt>          <dd class="mono">STR {e.HeroStr} · AGI {e.HeroAgi} · INT {e.HeroInt}</dd>
            {/if}
          {/if}

          {#if e.Inventory && e.Inventory.length > 0}
            <dt class="section">Inventory</dt>
            {#each e.Inventory as slot}
              <dt>Slot {slot.Slot}</dt><dd class="mono">{slot.ItemID}</dd>
            {/each}
          {/if}

          {#if e.ItemDrops && e.ItemDrops.length > 0}
            <dt class="section">Item Drops</dt>
            {#each e.ItemDrops as drop}
              <dt class="mono">{drop.ItemID}</dt><dd>{drop.Chance}%</dd>
            {/each}
          {/if}

          {#if e.AbilityModifications && e.AbilityModifications.length > 0}
            <dt class="section">Abilities</dt>
            {#each e.AbilityModifications as ab}
              <dt class="mono">{ab.AbilityID}</dt>
              <dd>lvl {ab.Level}{ab.Autocast ? ' · autocast' : ''}</dd>
            {/each}
          {/if}
        </dl>
      {/if}
    </aside>
  </div>
</main>

<style>
  :global(body) {
    margin: 0;
    background: #121214;
    color: #d4d4d8;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    font-size: 13px;
    overflow: hidden;
  }
  :global(html), :global(body), main { height: 100vh; }
  main { display: flex; flex-direction: column; }

  header {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 8px 18px;
    border-bottom: 1px solid #2a2a30;
    background: #18181b;
    flex: 0 0 auto;
  }
  h1 { margin: 0; font-size: 13px; font-weight: 600; color: #e4e4e7; }
  .status-strip { flex: 1 1 auto; color: #a1a1aa; font-size: 12px; }
  .map-name { color: #e4e4e7; font-weight: 500; }
  .map-count { color: #71717a; }
  .sep { color: #52525b; margin: 0 8px; }
  .actions { display: flex; gap: 6px; align-items: center; }

  button {
    background: #2563eb; color: white; border: 0; padding: 5px 12px;
    font-size: 12px; border-radius: 4px; cursor: pointer;
  }
  button:hover:not(:disabled) { background: #1d4ed8; }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  button.secondary { background: #3f3f46; }
  button.secondary:hover:not(:disabled) { background: #52525b; }
  button.mode-toggle {
    background: #3f3f46; font-weight: 500;
  }
  button.mode-toggle:hover:not(:disabled) { background: #52525b; }
  button.mode-toggle.on { background: #15803d; }
  button.mode-toggle.on:hover:not(:disabled) { background: #166534; }

  .error { background: #7f1d1d; color: #fecaca; padding: 6px 14px; font-family: 'Cascadia Mono', Consolas, monospace; font-size: 12px; flex: 0 0 auto; max-height: 200px; overflow: auto; }
  .error pre { margin: 0; white-space: pre-wrap; word-break: break-all; }

  .split { flex: 1 1 auto; display: grid; grid-template-columns: 260px 1fr 340px; min-height: 0; }
  .panel { background: #161618; display: flex; flex-direction: column; min-height: 0; overflow: hidden; }
  .explorer { border-right: 1px solid #2a2a30; }
  .properties { border-left: 1px solid #2a2a30; }
  .panel-header {
    padding: 8px 14px; font-size: 10px; font-weight: 600; color: #a1a1aa;
    text-transform: uppercase; letter-spacing: 0.08em;
    border-bottom: 1px solid #27272a; background: #1c1c1f;
    flex: 0 0 auto;
  }
  .empty { padding: 30px 16px; text-align: center; color: #71717a; font-size: 12px; }
  .viewport { position: relative; min-width: 0; min-height: 0; }
  canvas { display: block; width: 100%; height: 100%; }

  /* Explorer */
  .explorer > .category { padding: 8px 0; border-bottom: 1px solid #1f1f23; }
  .explorer .cat-header {
    padding: 4px 14px; font-size: 11px; font-weight: 600; color: #d4d4d8;
    display: flex; justify-content: space-between; align-items: center;
  }
  .explorer .cat-header .count { color: #71717a; font-weight: 400; font-size: 11px; }
  .explorer ul { list-style: none; margin: 0; padding: 0; overflow-y: auto; }
  .explorer li {
    display: flex; align-items: center; gap: 8px;
    padding: 4px 14px; cursor: pointer; font-size: 12px;
    min-width: 0;
  }
  .explorer li:hover { background: #1f1f23; }
  .explorer li.selected { background: #1e3a8a; color: #e4e4e7; }
  .explorer li .name {
    color: #e4e4e7; font-weight: 500; flex: 1 1 auto; min-width: 0;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .explorer li .cat {
    font-size: 10.5px; flex: 0 1 auto; min-width: 0;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    text-align: right;
  }
  .explorer li .pan-btn {
    flex: 0 0 auto; display: none;
    background: transparent; color: #a1a1aa;
    border: 1px solid #3f3f46; border-radius: 3px;
    padding: 1px 6px; font-size: 11px; line-height: 1;
    cursor: pointer;
  }
  .explorer li:hover .pan-btn { display: inline-flex; }
  .explorer li .pan-btn:hover { background: #3f3f46; color: #e4e4e7; }
  /* Doodad sub-buckets sit visually under the "Doodads" header with a slight
     indent on the category label so the hierarchy reads at a glance. */
  .explorer > .category.subcategory { padding: 2px 0 4px; border-bottom: 0; }
  .explorer .cat-header.sub {
    padding-left: 22px; color: #a1a1aa; font-weight: 500; font-size: 10.5px;
    text-transform: uppercase; letter-spacing: 0.04em;
  }

  /* Properties */
  .properties { overflow-y: auto; }
  .props {
    display: grid; grid-template-columns: max-content 1fr;
    gap: 4px 12px; padding: 10px 16px; margin: 0; font-size: 12px;
  }
  .props dt {
    color: #71717a; font-size: 11px; padding-top: 2px;
    text-align: left; justify-self: start;
  }
  .props dt.section {
    grid-column: 1 / -1; color: #a1a1aa; font-weight: 600; font-size: 10px;
    text-transform: uppercase; letter-spacing: 0.06em; margin-top: 8px;
    border-top: 1px solid #27272a; padding-top: 8px;
  }
  .props dd { margin: 0; color: #e4e4e7; }
  .mono { font-family: 'Cascadia Mono', Consolas, monospace; }
  .dim { color: #71717a; }
</style>
