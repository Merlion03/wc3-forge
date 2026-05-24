<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import {
    OpenMapDialog, OpenMap, CloseMap, ListUnits, Status, GetTerrain,
    GetSelection, SelectUnit, ClearSelection,
  } from '../wailsjs/go/main/App.js'
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
  import type { main } from '../wailsjs/go/models'
  import { createScene, type SceneAPI, type TerrainData, type UnitData } from './scene'

  let status: main.MapStatus = { loaded: false, unit_count: 0 }
  let units: main.UnitDTO[] = []
  let selectedIds = new Set<number>()
  let error: string = ''
  let busy: boolean = false

  let canvas: HTMLCanvasElement
  let scene: SceneAPI | null = null

  // Server-side selection events → local store → scene + table.
  const SEL_EVENT = 'wc3-forge:selection-changed'

  onMount(async () => {
    scene = createScene(canvas)
    scene.onPick(cn => {
      if (cn == null) {
        ClearSelection()
      } else {
        SelectUnit(cn)
      }
    })
    EventsOn(SEL_EVENT, (s: main.SelectionDTO) => {
      const ids = new Set<number>()
      for (const item of s.items || []) {
        if (item.kind === 'unit' || item.kind === 'item') ids.add(item.id)
      }
      selectedIds = ids
      scene?.setSelection(Array.from(ids))
    })

    // Initial state in case the bridge has a map already (e.g., --open flag).
    const s = await Status()
    status = s
    if (s.loaded) {
      await reloadMap()
    }
    const sel = await GetSelection()
    selectedIds = new Set((sel.items || []).map(i => i.id))
    scene?.setSelection(Array.from(selectedIds))
  })

  onDestroy(() => {
    EventsOff(SEL_EVENT)
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

  async function reloadMap() {
    units = await ListUnits()
    try {
      const terrain = await GetTerrain() as TerrainData
      scene?.setTerrain(terrain)
    } catch {
      // No terrain in this map — that's fine, viewport just stays empty.
      scene?.setTerrain(null)
    }
    scene?.setUnits(units.map(u => ({
      creation_number: u.creation_number,
      type_id: u.type_id,
      player: u.player,
      position: u.position,
      scale: u.scale,
    } as UnitData)))
  }

  async function close() {
    busy = true
    try {
      status = await CloseMap()
      units = []
      scene?.setTerrain(null)
      scene?.setUnits([])
      selectedIds = new Set()
    } finally {
      busy = false
    }
  }

  async function clickRow(cn: number) {
    await SelectUnit(cn)
  }

  function fmtPos(p: number[]): string {
    return `(${Math.round(p[0])}, ${Math.round(p[1])}, ${Math.round(p[2])})`
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
      <button on:click={pickAndOpen} disabled={busy}>Open Map…</button>
      {#if status.loaded}
        <button on:click={close} disabled={busy} class="secondary">Close</button>
      {/if}
    </div>
  </header>

  {#if error}
    <div class="error">{error}</div>
  {/if}

  <div class="split">
    <aside class="left">
      {#if !status.loaded}
        <div class="empty">
          <p>No map loaded.</p>
          <p class="hint">Open an extracted <code>.w3x</code> folder<br>(one containing <code>war3map.w3i</code>).</p>
        </div>
      {:else}
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>Type</th>
              <th>P</th>
              <th>Position</th>
            </tr>
          </thead>
          <tbody>
            {#each units as u (u.creation_number)}
              <tr
                class:selected={selectedIds.has(u.creation_number)}
                on:click={() => clickRow(u.creation_number)}
              >
                <td class="mono dim">{u.creation_number}</td>
                <td class="mono">{u.type_id}</td>
                <td>{u.player}</td>
                <td class="mono">{fmtPos(u.position)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </aside>
    <section class="viewport">
      <canvas bind:this={canvas}></canvas>
    </section>
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
    padding: 10px 20px;
    border-bottom: 1px solid #2a2a30;
    background: #18181b;
    flex: 0 0 auto;
  }
  h1 {
    margin: 0;
    font-size: 14px;
    font-weight: 600;
    color: #e4e4e7;
  }
  .status-strip { flex: 1 1 auto; color: #a1a1aa; font-size: 12px; }
  .map-name { color: #e4e4e7; font-weight: 500; }
  .map-count { color: #71717a; }
  .sep { color: #52525b; margin: 0 8px; }
  .actions { display: flex; gap: 6px; }

  button {
    background: #2563eb;
    color: white;
    border: 0;
    padding: 6px 14px;
    font-size: 12px;
    border-radius: 4px;
    cursor: pointer;
  }
  button:hover:not(:disabled) { background: #1d4ed8; }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  button.secondary { background: #3f3f46; }
  button.secondary:hover:not(:disabled) { background: #52525b; }

  .error {
    background: #7f1d1d;
    color: #fecaca;
    padding: 6px 14px;
    font-family: 'Cascadia Mono', Consolas, monospace;
    font-size: 12px;
    flex: 0 0 auto;
  }

  .split {
    flex: 1 1 auto;
    display: grid;
    grid-template-columns: 360px 1fr;
    min-height: 0;
  }
  .left {
    border-right: 1px solid #2a2a30;
    overflow-y: auto;
    background: #161618;
  }
  .viewport {
    position: relative;
    min-width: 0;
    min-height: 0;
  }
  canvas {
    display: block;
    width: 100%;
    height: 100%;
  }

  .empty { padding: 40px 20px; text-align: center; color: #71717a; }
  .hint { font-size: 12px; }
  code {
    background: #27272a;
    padding: 1px 6px;
    border-radius: 3px;
    font-family: 'Cascadia Mono', Consolas, monospace;
  }

  table { width: 100%; border-collapse: collapse; font-size: 12px; }
  thead { position: sticky; top: 0; background: #161618; }
  th, td {
    text-align: left;
    padding: 5px 10px;
    border-bottom: 1px solid #27272a;
  }
  th {
    color: #a1a1aa;
    font-weight: 500;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom-color: #3f3f46;
  }
  tbody tr { cursor: pointer; }
  tbody tr:hover { background: #1f1f23; }
  tbody tr.selected { background: #1e3a8a; color: #e4e4e7; }
  tbody tr.selected td.dim { color: #93c5fd; }
  td.dim { color: #71717a; }
  .mono { font-family: 'Cascadia Mono', Consolas, monospace; }
</style>
