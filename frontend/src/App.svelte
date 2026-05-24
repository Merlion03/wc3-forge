<script lang="ts">
  import { OpenMapDialog, OpenMap, CloseMap, ListUnits, Status } from '../wailsjs/go/main/App.js'
  import type { main } from '../wailsjs/go/models'

  let status: main.MapStatus = { loaded: false, unit_count: 0 }
  let units: main.UnitDTO[] = []
  let error: string = ''
  let busy: boolean = false

  // Initial status check (in case the bridge already has a map loaded).
  Status().then(s => { status = s; if (s.loaded) refreshUnits() })

  async function pickAndOpen() {
    error = ''
    busy = true
    try {
      const path = await OpenMapDialog()
      if (!path) { busy = false; return }
      status = await OpenMap(path)
      await refreshUnits()
    } catch (e) {
      error = String(e)
    } finally {
      busy = false
    }
  }

  async function refreshUnits() {
    units = await ListUnits()
  }

  async function close() {
    busy = true
    try {
      status = await CloseMap()
      units = []
    } finally {
      busy = false
    }
  }

  function fmtPos(p: number[]): string {
    return `(${Math.round(p[0])}, ${Math.round(p[1])}, ${Math.round(p[2])})`
  }
</script>

<main>
  <header>
    <h1>wc3-forge</h1>
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

  {#if !status.loaded}
    <div class="empty">
      <p>No map loaded.</p>
      <p class="hint">Open an extracted <code>.w3x</code> folder (one containing <code>war3map.w3i</code>).</p>
    </div>
  {:else}
    <section class="info">
      <div><span class="label">Name</span><span class="value">{status.name || '(untitled)'}</span></div>
      <div><span class="label">Path</span><span class="value mono">{status.path}</span></div>
      <div><span class="label">Units</span><span class="value">{status.unit_count}</span></div>
    </section>

    <section class="units">
      <table>
        <thead>
          <tr>
            <th>#</th>
            <th>Type</th>
            <th>Skin</th>
            <th>Player</th>
            <th>Position</th>
            <th>Rot</th>
            <th>Scale</th>
            <th>HP%</th>
            <th>MP%</th>
            <th>Lvl</th>
            <th>Gold</th>
          </tr>
        </thead>
        <tbody>
          {#each units as u (u.creation_number)}
            <tr>
              <td class="mono dim">{u.creation_number}</td>
              <td class="mono">{u.type_id}</td>
              <td class="mono dim">{u.skin_id === u.type_id ? '—' : u.skin_id}</td>
              <td>{u.player}</td>
              <td class="mono">{fmtPos(u.position)}</td>
              <td class="mono dim">{u.rotation.toFixed(2)}</td>
              <td class="mono dim">{u.scale[0].toFixed(2)}</td>
              <td class="dim">{u.hit_points_pct < 0 ? '—' : u.hit_points_pct}</td>
              <td class="dim">{u.mana_pct < 0 ? '—' : u.mana_pct}</td>
              <td class="dim">{u.hero_level || '—'}</td>
              <td class="dim">{u.gold_amount || '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}
</main>

<style>
  :global(body) {
    margin: 0;
    background: #121214;
    color: #d4d4d8;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    font-size: 13px;
  }

  main {
    padding: 16px 24px;
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 16px;
    border-bottom: 1px solid #2a2a30;
    padding-bottom: 12px;
  }

  h1 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
    color: #e4e4e7;
  }

  .actions {
    display: flex;
    gap: 8px;
  }

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
    padding: 8px 12px;
    border-radius: 4px;
    margin-bottom: 12px;
    font-family: 'Cascadia Mono', Consolas, monospace;
    font-size: 12px;
  }

  .empty {
    text-align: center;
    padding: 48px 0;
    color: #71717a;
  }
  .hint { font-size: 12px; }
  code {
    background: #27272a;
    padding: 1px 6px;
    border-radius: 3px;
    font-family: 'Cascadia Mono', Consolas, monospace;
  }

  .info {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 4px 16px;
    margin-bottom: 16px;
    padding: 12px 16px;
    background: #1c1c1f;
    border-radius: 6px;
  }
  .info > div { display: contents; }
  .label {
    color: #71717a;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .value { color: #e4e4e7; }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }
  th, td {
    text-align: left;
    padding: 6px 10px;
    border-bottom: 1px solid #27272a;
  }
  th {
    color: #a1a1aa;
    font-weight: 500;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    border-bottom-color: #3f3f46;
  }
  td.dim { color: #71717a; }
  .mono { font-family: 'Cascadia Mono', Consolas, monospace; }
</style>
