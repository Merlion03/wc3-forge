<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import {
    OpenMapDialog, OpenMap, CloseMap, ListUnits, ListDoodads, Status,
    GetSelection, SelectUnit, ClearSelection, GetUnit,
    GetReforgedMode, SetReforgedMode,
    GetUnitTypeIndex, GetDoodadTypeIndex,
  } from '../wailsjs/go/main/App.js'
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
  import type { main, unitsdoo } from '../wailsjs/go/models'
  import { createScene, type SceneAPI } from './scene-instances'

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
  let selectedIds = new Set<number>()
  let primaryEntity: unitsdoo.Entity | null = null
  let error: string = ''
  let busy: boolean = false
  let reforged: boolean = false

  let canvas: HTMLCanvasElement
  let scene: SceneAPI | null = null

  const SEL_EVENT = 'wc3-forge:selection-changed'
  const MAP_EVENT = 'wc3-forge:map-changed'

  onMount(async () => {
    try {
      // Pull initial mode from Go so reloads of the UI (HMR, route) honor
      // any persistent setting once we add one. Currently session-only.
      try { reforged = await GetReforgedMode() } catch { reforged = false }
      scene = createScene(canvas, reforged)
      scene.onUnitClicked((cn) => { SelectUnit(cn) })
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
        primaryEntity = null
      }
    })
    EventsOn(SEL_EVENT, async (s: main.SelectionDTO) => {
      const ids = new Set<number>()
      for (const item of s.items || []) {
        if (item.kind === 'unit' || item.kind === 'item') ids.add(item.id)
      }
      selectedIds = ids
      scene?.setSelected(ids)
      // Fetch full entity for the primary selection (or first item).
      const primaryCn = (s.items && s.items.length > 0)
        ? s.items[Math.max(0, Math.min(s.primary, s.items.length - 1))].id
        : null
      if (primaryCn != null) {
        try { primaryEntity = await GetUnit(primaryCn) } catch { primaryEntity = null }
      } else {
        primaryEntity = null
      }
    })

    const s = await Status()
    status = s
    if (s.loaded) await reloadMap()
    const sel = await GetSelection()
    selectedIds = new Set((sel.items || []).map(i => i.id))
  })

  onDestroy(() => {
    EventsOff(SEL_EVENT)
    EventsOff(MAP_EVENT)
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
      primaryEntity = null
      // No clean "unload map" in mdx-m3-viewer; we'd recreate the viewer
      // on close. For now the canvas just keeps the last-loaded map until
      // a new one is opened.
    } finally {
      busy = false
    }
  }

  async function clickRow(cn: number) { await SelectUnit(cn) }

  function panToUnit(e: Event, u: main.UnitDTO) {
    e.stopPropagation()  // don't trigger row selection
    if (u.position && u.position.length >= 2) {
      scene?.panTo(u.position[0], u.position[1])
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
                    on:click={() => clickRow(u.creation_number)}
                    title="{u.type_id} #{u.creation_number}">
                  <span class="name">{unitDisplayName(u)}</span>
                  <span class="cat dim">{unitCategory(u)}</span>
                  <button class="pan-btn"
                          on:click={(e) => panToUnit(e, u)}
                          title="Pan camera to this entity">⊕</button>
                </li>
              {/each}
            </ul>
          </div>
        {/each}
        {#if doodadCount > 0}
          <div class="category">
            <header class="cat-header">Doodads <span class="count">{doodadCount}</span></header>
            <div class="dim doodad-note">Decorative — visible in viewport; clicking not yet wired.</div>
          </div>
        {/if}
      {/if}
    </aside>

    <section class="viewport">
      <canvas bind:this={canvas}></canvas>
    </section>

    <aside class="panel properties">
      <header class="panel-header">Properties</header>
      {#if !primaryEntity}
        <div class="empty">
          {#if selectedIds.size === 0}
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
  .doodad-note { padding: 4px 14px 8px; font-size: 11px; color: #71717a; }

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
