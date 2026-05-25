<script lang="ts">
  // BridgeConsole — dockable bottom panel that streams every MCP bridge-call
  // dispatched against this wc3-forge instance. Lets the user see, live, what
  // every connected agent is doing.
  //
  // Wire-up:
  //   - Go-side: every handler registered via forge.RegisterAll is wrapped
  //     with `instrumented`, which fires a Session.notifyBridgeCall event after
  //     the dispatch (success or error).
  //   - App.startup: subscribes to that event and forwards each
  //     forge.BridgeCallEvent through Wails as `wc3-forge:bridge-call`.
  //   - This component: subscribes to the Wails event, caps history at 500
  //     rows, renders a scrollable table, and exposes filter + clear + dock
  //     toggle + auto-scroll pause/resume.
  //
  // Persistence: panel open/closed state and bottom-pane height are stored in
  // localStorage so they survive reload (per spec).
  //
  // Identifier bar (top): shows the running bridge's pid/port/token-short and
  // currently-loaded map name. Read from App.GetBridgeInfo on mount + after
  // each map-changed event.

  import { onMount, onDestroy } from 'svelte'
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
  import { GetBridgeInfo } from '../wailsjs/go/main/App.js'

  // Public component prop — bound by the parent so the parent can also drive
  // open/closed state (via header button or Ctrl+` hotkey). Two-way bind so
  // internal toggles (the X close button) propagate up.
  export let open: boolean = false

  // Each BridgeCallEvent that arrives in real time. Capped at 500 rows.
  interface BridgeCallEvent {
    timestamp: string  // ISO 8601 from Go time.Time
    method: string
    params_summary: string
    result: string
    error: string
    duration_micros: number
  }
  let calls: BridgeCallEvent[] = []
  let totalSeen: number = 0  // counts ALL events ever, including dropped-from-cap
  const MAX_ROWS = 500

  let filter: string = ''
  let autoScroll: boolean = true
  let listEl: HTMLDivElement | null = null

  // Bridge identity for the top bar. Falls back to "(unknown)" before
  // GetBridgeInfo resolves.
  interface BridgeInfo {
    pid: number
    port: number
    token_short: string
    map_name: string
    map_path: string
  }
  let bridgeInfo: BridgeInfo | null = null

  // ---- LocalStorage keys + height state ----
  const LS_HEIGHT = 'wc3forge.bridge-console.height'
  const LS_OPEN   = 'wc3forge.bridge-console.open'
  // Height in pixels. Defaults to 200 per spec; clamped on drag.
  let heightPx: number = 200
  const MIN_HEIGHT = 100
  const MAX_HEIGHT = 600

  $: filtered = filter.trim()
    ? calls.filter(c => c.method.toLowerCase().includes(filter.trim().toLowerCase()))
    : calls

  function timeOf(iso: string): string {
    // ISO 8601 from Go time.Time — extract HH:MM:SS.mmm. Local time for the
    // user (Date constructor honors timezone), millisecond-precision so
    // sub-second ordering is visible when many calls arrive within one second.
    try {
      const d = new Date(iso)
      const hh = String(d.getHours()).padStart(2, '0')
      const mm = String(d.getMinutes()).padStart(2, '0')
      const ss = String(d.getSeconds()).padStart(2, '0')
      const ms = String(d.getMilliseconds()).padStart(3, '0')
      return `${hh}:${mm}:${ss}.${ms}`
    } catch {
      return iso
    }
  }
  function durMs(us: number): string {
    return (us / 1000).toFixed(2)
  }

  function rowClass(c: BridgeCallEvent): string {
    if (c.error) return 'row err'
    if (c.duration_micros > 50_000) return 'row slow'
    return 'row ok'
  }

  function onScroll() {
    if (!listEl) return
    // Threshold: 4px from bottom counts as "at bottom" — covers sub-pixel
    // rounding from CSS-zoom and the trailing scrollbar arrow on Windows.
    const dist = listEl.scrollHeight - listEl.scrollTop - listEl.clientHeight
    autoScroll = dist < 4
  }
  function scrollToBottom() {
    if (!listEl) return
    listEl.scrollTop = listEl.scrollHeight
  }

  function clearHistory() {
    calls = []
    // totalSeen intentionally NOT reset — the "[showing last N of M]" banner
    // tracks total stream activity since open, not since clear. Less likely
    // to mislead an agent reading the count as "we've made N calls".
  }

  // ---- Drag-to-resize the panel height ----
  let dragging: boolean = false
  let dragStartY: number = 0
  let dragStartHeight: number = 0
  function onDragStart(e: MouseEvent) {
    dragging = true
    dragStartY = e.clientY
    dragStartHeight = heightPx
    e.preventDefault()
  }
  function onDragMove(e: MouseEvent) {
    if (!dragging) return
    // Window-bottom-anchored panel: dragging up makes the panel taller, so
    // subtract the cursor delta from start height.
    const dy = dragStartY - e.clientY
    const next = Math.max(MIN_HEIGHT, Math.min(MAX_HEIGHT, dragStartHeight + dy))
    heightPx = next
  }
  function onDragEnd() {
    if (dragging) {
      dragging = false
      try { localStorage.setItem(LS_HEIGHT, String(heightPx)) } catch {}
    }
  }

  // Persist open state whenever it flips (driven from inside or from parent).
  $: try { localStorage.setItem(LS_OPEN, open ? '1' : '0') } catch {}

  async function refreshBridgeInfo() {
    try {
      bridgeInfo = (await GetBridgeInfo()) as unknown as BridgeInfo
    } catch {
      bridgeInfo = null
    }
  }

  // Test-driver hook: external automation can set filter / clear / etc. on
  // the panel by calling these via the page's window.__bridgeConsole bag.
  // The `wc3-forge:test-command` event also routes commands prefixed with
  // `bridge_console.` here so MCP probes can drive the panel.
  function applyTestCommand(cmd: string): void {
    const [op, ...rest] = cmd.split(/\s+/, 2)
    if (op === 'filter') {
      filter = rest.join(' ')
    } else if (op === 'clear') {
      clearHistory()
    } else if (op === 'autoscroll' && rest[0] === 'off') {
      autoScroll = false
    } else if (op === 'autoscroll' && rest[0] === 'on') {
      autoScroll = true
      scrollToBottom()
    }
  }

  onMount(async () => {
    // Restore persisted height + open state. Note: parent passes `open` in
    // (so the toggle button reflects it); we still read LS so first-mount
    // (before the parent loaded LS) lines up.
    try {
      const h = parseInt(localStorage.getItem(LS_HEIGHT) || '', 10)
      if (Number.isFinite(h) && h >= MIN_HEIGHT && h <= MAX_HEIGHT) heightPx = h
    } catch {}

    // Subscribe to the bridge-call event stream from Go. Each event is
    // appended; we drop oldest when over MAX_ROWS so the live view stays
    // responsive on long sessions.
    EventsOn('wc3-forge:bridge-call', (c: BridgeCallEvent) => {
      totalSeen++
      const next = [...calls, c]
      if (next.length > MAX_ROWS) next.splice(0, next.length - MAX_ROWS)
      calls = next
      // RAF-defer the scroll so the row is in the DOM before we try to scroll
      // to it. Without this, listEl.scrollHeight reflects the pre-paint state
      // and we'd miss the new row's height.
      if (autoScroll) requestAnimationFrame(scrollToBottom)
    })
    // Map-changed events update the identifier bar's map name without
    // requiring the user to reopen the panel.
    EventsOn('wc3-forge:map-changed', () => { void refreshBridgeInfo() })
    await refreshBridgeInfo()

    // Wire up test-driver commands prefixed with `bridge_console.` — lets MCP
    // probes drive the panel's filter / clear / autoscroll state without
    // needing synthetic keyboard input (WebView2 drops it). Re-uses the
    // existing wc3-forge:test-command bus.
    EventsOn('wc3-forge:test-command', (payload: { cmd: string }) => {
      const cmd = payload?.cmd || ''
      const prefix = 'bridge_console.'
      if (cmd.startsWith(prefix + 'filter ')) {
        applyTestCommand('filter ' + cmd.slice((prefix + 'filter ').length))
      } else if (cmd === prefix + 'clear') {
        applyTestCommand('clear')
      } else if (cmd === prefix + 'autoscroll_off') {
        applyTestCommand('autoscroll off')
      } else if (cmd === prefix + 'autoscroll_on') {
        applyTestCommand('autoscroll on')
      }
    })
    ;(window as any).__bridgeConsole = { applyTestCommand }

    // Document-level mousemove/mouseup so drag continues outside the splitter.
    document.addEventListener('mousemove', onDragMove)
    document.addEventListener('mouseup', onDragEnd)
  })
  onDestroy(() => {
    EventsOff('wc3-forge:bridge-call')
    // Note: don't EventsOff('wc3-forge:map-changed') — App.svelte owns that
    // event and would silently lose its subscriber if we tear it down here.
    document.removeEventListener('mousemove', onDragMove)
    document.removeEventListener('mouseup', onDragEnd)
  })
</script>

{#if open}
  <section class="bridge-console" style="height: {heightPx}px">
    <!-- Drag handle: 4px-tall strip across the top of the panel. Visual
         affordance is the slightly-lighter background + ns-resize cursor. -->
    <div class="drag-handle"
         on:mousedown={onDragStart}
         class:dragging
         title="Drag to resize"></div>

    <header class="console-header">
      <span class="title">Agent Console</span>
      <span class="identifier">
        {#if bridgeInfo}
          wc3-forge PID {bridgeInfo.pid} · bridge 127.0.0.1:{bridgeInfo.port}
          {#if bridgeInfo.token_short} · token {bridgeInfo.token_short}{/if}
          {#if bridgeInfo.map_name} · map <strong>{bridgeInfo.map_name}</strong>{/if}
        {:else}
          (bridge info unavailable)
        {/if}
      </span>
      <span class="spacer"></span>
      <input class="filter"
             type="text"
             placeholder="filter by method…"
             bind:value={filter}
             aria-label="Filter rows by method substring" />
      <button class="clear-btn"
              on:click={clearHistory}
              title="Clear local history (stream continues)">Clear</button>
      <button class="close-btn"
              on:click={() => open = false}
              title="Close (Ctrl+`)">×</button>
    </header>

    <div class="row-count">
      {#if totalSeen > calls.length}
        showing last {calls.length} of {totalSeen}
      {:else}
        {calls.length} call{calls.length === 1 ? '' : 's'}
      {/if}
      {#if filter}
        · filtered: {filtered.length}
      {/if}
      {#if !autoScroll}
        · <span class="paused">auto-scroll paused</span>
      {/if}
    </div>

    <div class="rows" bind:this={listEl} on:scroll={onScroll}>
      {#if filtered.length === 0}
        <div class="empty">
          {#if filter}
            No calls match filter <code>{filter}</code>.
          {:else}
            Waiting for bridge calls…
          {/if}
        </div>
      {:else}
        {#each filtered as c}
          <div class={rowClass(c)}>
            <span class="cell time">{timeOf(c.timestamp)}</span>
            <span class="cell method">{c.method}</span>
            <span class="cell params" title={c.params_summary}>{c.params_summary}</span>
            <span class="cell result" title={c.error || c.result}>
              {#if c.error}
                <span class="err-text">{c.error}</span>
              {:else}
                {c.result}
              {/if}
            </span>
            <span class="cell dur">{durMs(c.duration_micros)} ms</span>
          </div>
        {/each}
      {/if}
    </div>
  </section>
{/if}

<style>
  .bridge-console {
    flex: 0 0 auto;
    display: flex; flex-direction: column;
    background: #0f0f12; color: #d4d4d8;
    border-top: 1px solid #2a2a30;
    font-size: 12px;
    min-height: 100px;
    overflow: hidden;
  }
  .drag-handle {
    flex: 0 0 4px;
    background: #2a2a30; cursor: ns-resize;
    transition: background 120ms ease;
  }
  .drag-handle:hover, .drag-handle.dragging { background: #3f3f46; }

  .console-header {
    flex: 0 0 auto;
    display: flex; align-items: center; gap: 10px;
    padding: 6px 12px; background: #18181b;
    border-bottom: 1px solid #27272a;
    font-size: 11px;
  }
  .title {
    font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em;
    color: #a1a1aa; font-size: 10px;
  }
  .identifier {
    color: #71717a; font-family: 'Cascadia Mono', Consolas, monospace;
    font-size: 11px;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    flex: 0 1 auto; min-width: 0;
  }
  .identifier strong { color: #e4e4e7; font-weight: 600; }
  .spacer { flex: 1 1 auto; }
  .filter {
    width: 200px;
    background: #18181b; color: #e4e4e7;
    border: 1px solid #3f3f46; border-radius: 3px;
    padding: 3px 6px; font-size: 11px;
    font-family: 'Cascadia Mono', Consolas, monospace;
  }
  .filter:focus { outline: none; border-color: #2563eb; }
  .clear-btn, .close-btn {
    background: #27272a; color: #d4d4d8;
    border: 0; border-radius: 3px;
    padding: 3px 10px; font-size: 11px; cursor: pointer;
  }
  .clear-btn:hover, .close-btn:hover { background: #3f3f46; }
  .close-btn { padding: 3px 8px; font-size: 14px; line-height: 1; }

  .row-count {
    flex: 0 0 auto;
    padding: 2px 14px; font-size: 10.5px; color: #71717a;
    background: #131316; border-bottom: 1px solid #27272a;
  }
  .row-count .paused { color: #fbbf24; }

  .rows {
    flex: 1 1 auto; min-height: 0;
    overflow-y: auto; overflow-x: hidden;
    font-family: 'Cascadia Mono', Consolas, monospace;
    font-size: 11.5px;
  }
  .row {
    display: grid;
    grid-template-columns: 100px 220px 1fr 1fr 80px;
    gap: 8px; padding: 2px 14px;
    border-bottom: 1px solid #1c1c1f;
    align-items: baseline;
  }
  .row:hover { background: #18181b; }
  .row.ok .method { color: #e4e4e7; }
  .row.err {
    background: rgba(127, 29, 29, 0.18);
  }
  .row.err .method, .row.err .err-text {
    color: #fca5a5;
  }
  .row.slow .method, .row.slow .dur { color: #fbbf24; }

  .cell { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cell.time { color: #52525b; }
  .cell.method { font-weight: 500; }
  .cell.params { color: #71717a; }
  .cell.result { color: #a1a1aa; }
  .cell.dur { color: #a1a1aa; text-align: right; }

  .empty { padding: 20px; text-align: center; color: #52525b; }
  .empty code { background: #27272a; padding: 1px 4px; border-radius: 2px; color: #a1a1aa; }
</style>
