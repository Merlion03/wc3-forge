<script lang="ts">
  // Top-left diagnostics overlay. Toggleable (F9 / View menu). Polls the scene
  // for live render + camera + GL state each animation frame and prints it as a
  // compact monospace readout. Built to crack the "frozen viewport" mystery:
  // the `frame` counter here updates via the DOM, while the scene draws a
  // sweeping heartbeat bar INTO the canvas. Compare them:
  //   - DOM frame climbs, canvas bar frozen → the canvas isn't being presented
  //     (present-stall): the GPU draws but WebView2 won't flip it to screen.
  //   - both move                           → the canvas IS presenting; a
  //     "frozen" look is then a camera/content problem, not presentation.
  import { onMount, onDestroy } from 'svelte'

  let {
    scene,
    mapName = '',
    mapPath = '',
  }: {
    scene: any
    mapName?: string
    mapPath?: string
  } = $props()

  type Diag = ReturnType<NonNullable<typeof scene>['getDiagnostics']>
  let d: Diag | null = $state(null)
  let docState = $state('')
  let raf = 0

  function tick() {
    try { d = scene?.getDiagnostics?.() ?? null } catch { d = null }
    docState =
      `${document.visibilityState}` +
      `${document.hasFocus() ? ' focused' : ' BLUR'}` +
      `${document.hidden ? ' HIDDEN' : ''}`
    raf = requestAnimationFrame(tick)
  }
  onMount(() => { raf = requestAnimationFrame(tick) })
  onDestroy(() => cancelAnimationFrame(raf))

  const n = (v: number) => Math.round(v)
  const n2 = (v: number) => (Math.round(v * 100) / 100).toFixed(2)

  // The hand-laid hot fields above are rendered explicitly; everything else in
  // the snapshot is a registry namespace (an object from collectDiag) that we
  // render generically below, so new subsystem signals appear with no overlay
  // edit. KNOWN excludes the explicit fields from the generic pass.
  const KNOWN = new Set([
    'frame', 'fps', 'crashed', 'crashReason', 'crashAtFrame', 'diagnosticsArmed',
    'logLevel', 'doodads', 'units', 'visible', 'terrainWidth', 'terrainHeight',
    'centerOffset', 'hasSky', 'hasWater', 'hasTerrain', 'glRenderer',
    'lastGlError', 'glErrFrame', 'glErrStage', 'camera',
  ])
  let regSections = $derived.by(() => {
    const out: Array<{ ns: string; rows: Array<[string, unknown]> }> = []
    if (!d) return out
    for (const [k, v] of Object.entries(d as Record<string, unknown>)) {
      if (KNOWN.has(k)) continue
      if (v && typeof v === 'object' && !Array.isArray(v)) {
        out.push({ ns: k, rows: Object.entries(v as Record<string, unknown>) })
      }
    }
    return out
  })
  // Standardized visual alerting: any numeric field whose name ends in
  // Fails/Errors/Skipped/Miss(es) that's > 0 tints red — subsystems opt in for
  // free by naming their failure counters that way.
  const isAlert = (key: string, val: unknown) =>
    typeof val === 'number' && val > 0 && /(?:Fails?|Errors?|Errs?|Skipped|Miss(?:es)?)$/i.test(key)
  const fmtVal = (v: unknown): string =>
    v == null ? '–'
      : typeof v === 'number' ? (Number.isInteger(v) ? String(v) : String(Math.round(v * 100) / 100))
      : typeof v === 'object' ? JSON.stringify(v)
      : String(v)
</script>

<div class="diag" aria-hidden="true">
  {#if d}
    <div class="hdr">DIAGNOSTICS <span class="dim">(F9)</span></div>
    <div class="grid">
      <span class="k">frame</span><span class="v hi">{d.frame}</span>
      <span class="k">fps</span><span class="v">{d.fps}</span>
      <span class="k">heartbeat</span><span class="v dim">bar sweeps ↓ canvas bottom</span>
      <span class="k">armed / log</span><span class="v {d.diagnosticsArmed ? '' : 'warn'}">{d.diagnosticsArmed ? 'ARMED' : 'off'} / {d.logLevel}</span>
      <span class="k">crashed</span><span class="v {d.crashed ? 'err' : ''}">{d.crashed}</span>
      {#if d.crashed}<span class="k">crash</span><span class="v err small">{d.crashReason} @f{d.crashAtFrame}</span>{/if}
      <span class="k">doc</span><span class="v {docState.includes('BLUR') || docState.includes('hidden') ? 'warn' : ''}">{docState}</span>

      <span class="k sec">cam eye</span><span class="v">{n(d.camera.eye[0])}, {n(d.camera.eye[1])}, {n(d.camera.eye[2])}</span>
      <span class="k">cam tgt</span><span class="v">{n(d.camera.pivot[0])}, {n(d.camera.pivot[1])}, {n(d.camera.pivot[2])}</span>
      <span class="k">dist</span><span class="v">{n(d.camera.distance)}</span>
      <span class="k">yaw/pitch</span><span class="v">{n2(d.camera.yaw)} / {n2(d.camera.pitch)}</span>

      <span class="k sec">doodads</span><span class="v">{d.doodads}</span>
      <span class="k">units</span><span class="v">{d.units}</span>
      <span class="k">visible</span><span class="v">{d.visible}</span>
      <span class="k">terrain</span><span class="v {d.hasTerrain ? '' : 'err'}">{d.hasTerrain ? `${d.terrainWidth}×${d.terrainHeight}` : 'NONE'}</span>
      <span class="k">center</span><span class="v">{n(d.centerOffset[0])}, {n(d.centerOffset[1])}</span>
      <span class="k">sky / water</span><span class="v">{d.hasSky ? 'sky' : '–'} / {d.hasWater ? 'water' : '–'}</span>

      <span class="k sec">gl err</span><span class="v {d.lastGlError ? 'err' : ''}">{d.lastGlError ? '0x' + d.lastGlError.toString(16) : 'none'}</span>
      <span class="k">gpu</span><span class="v dim small">{d.glRenderer || '?'}</span>

      <span class="k sec">map</span><span class="v dim small">{mapName || '(untitled)'}</span>
      {#if mapPath}<span class="k">path</span><span class="v dim small break">{mapPath}</span>{/if}

      <!-- Generic registry sections — one block per subsystem namespace, no
           per-field overlay edits. Failure-counter fields auto-tint red. -->
      {#each regSections as section (section.ns)}
        <span class="k sec nsh">▾ {section.ns}</span><span class="v dim"></span>
        {#each section.rows as [key, val] (key)}
          <span class="k small">{key}</span><span class="v small {isAlert(key, val) ? 'err' : (key === '_providerError' || key === '_armedError' ? 'err' : '')}">{fmtVal(val)}</span>
        {/each}
      {/each}
    </div>
  {:else}
    <div class="hdr">DIAGNOSTICS — no scene</div>
  {/if}
</div>

<style>
  .diag {
    position: absolute;
    top: 8px;
    left: 8px;
    z-index: 60;
    pointer-events: none;
    user-select: none;
    max-width: 340px;
    padding: 6px 8px;
    background: rgb(0 0 0 / 0.62);
    border: 1px solid var(--border);
    border-radius: 6px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 11px;
    line-height: 1.35;
    color: var(--foreground);
  }
  .hdr { font-weight: 700; letter-spacing: 0.04em; margin-bottom: 4px; color: #7dd3fc; }
  .grid { display: grid; grid-template-columns: auto 1fr; column-gap: 8px; }
  .k { color: var(--muted-foreground); white-space: nowrap; }
  .k.sec { margin-top: 4px; }
  .k.nsh { color: #7dd3fc; font-weight: 600; margin-top: 5px; }
  .v { text-align: right; word-break: break-word; }
  .v.hi { color: #86efac; font-weight: 700; }
  .v.err { color: #fca5a5; font-weight: 700; }
  .v.warn { color: #fcd34d; }
  .dim { color: var(--muted-foreground); }
  .small { font-size: 10px; }
  .break { word-break: break-all; }
</style>
