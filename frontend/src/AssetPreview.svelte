<script lang="ts">
  import { run, nonpassive, createBubbler, preventDefault } from 'svelte/legacy';

  const bubble = createBubbler();
  // Tiny standalone ModelViewer for the Properties panel — loads one MDX for
  // the currently-selected unit/doodad and renders it on a small canvas with
  // orbit controls + a reset button. Independent of the main scene; uses its
  // own GL context, model cache, RAF loop. Disposes cleanly on unmount.
  //
  // Reactivity: changing `modelPath` swaps the displayed model (previous
  // instance is detached). Changing `reforged` rebuilds the viewer so the
  // MDX handler picks up the new mode.

  import { onMount, onDestroy } from 'svelte'
  import * as MV_ns from 'mdx-m3-viewer'
  import { pathSolver } from './scene-instances'
  import { patchMdxParser } from './mdx-parser-patch'

  
  
  interface Props {
    modelPath?: string | null;
    reforged?: boolean;
    /** Team-color index for setTeamColor on the loaded instance. Defaults to 0
   *  (red) — pass the unit's player for an accurate preview. Doodads ignore. */
    teamColor?: number;
    /**
   * Optional fallback paths the loader tries when `modelPath` fails (load
   * error or 404). Mirrors placeDoodad's variant → unsuffixed → other-extension
   * chain from scene-instances.ts. The Properties panel passes the variant-
   * suffixed path as `modelPath` and the unsuffixed stem + .mdl as fallbacks;
   * that's the shape that catches custom "Statue 1"-style doodad rows whose
   * SLK declares N variants but only the unsuffixed file ships in CASC.
   */
    modelPathFallbacks?: string[];
  }

  let {
    modelPath = null,
    reforged = false,
    teamColor = 0,
    modelPathFallbacks = []
  }: Props = $props();

  const MV: any = (MV_ns as any).default ?? MV_ns
  patchMdxParser() // idempotent guard inside

  let canvas: HTMLCanvasElement = $state()
  let viewer: any = $state(null)
  let scene: any = null
  let instance: any = null
  let model: any = null
  let modelCenter: [number, number, number] = [0, 0, 0]
  let modelRadius: number = 200
  let rafId = 0
  let disposed = false
  let loadToken = 0
  let error = $state('')
  let currentReforged = $state(reforged)
  // Real-dt tracking for viewer.update(). Mirrors the main scene's tick logic
  // (scene-instances.ts) — mdx-m3-viewer's `update(dt)` wants MILLISECONDS
  // elapsed since last update, but viewer.updateAndRender's DEFAULT is a
  // hardcoded `1000/60` (= 16.67ms). On a high-refresh display (120/144/240Hz)
  // RAF fires 2-4× per logical 60Hz frame, but the hardcoded dt still advances
  // animations as if a full 60Hz frame had passed — so WC3 stand cycles play
  // 2-4× too fast. Tracking real elapsed time and clamping to 250ms mirrors
  // HiveWE's `std::clamp(delta, 0.0, 0.5)` on the seconds path.
  let lastFrameTs = performance.now()
  const MAX_DT_MS = 250

  // Orbit state. yaw/pitch are spherical around the model center; distance is
  // the orbit radius. Defaults chosen for a 3/4 hero-pose framing — slightly
  // above eye level, ~45° around so the model isn't axis-aligned-flat.
  const DEFAULT_YAW = Math.PI / 4
  const DEFAULT_PITCH = Math.PI / 7 // ~25° above horizontal
  let yaw = DEFAULT_YAW
  let pitch = DEFAULT_PITCH
  let distance = 400

  function fitDistanceToModel(): number {
    // 1.6× radius pulls the model in close enough to fill the preview canvas.
    // FOV is PI/3 (60°); tan(FOV/2) ≈ 0.577 → distance = radius / 0.577 ≈
    // 1.73·radius JUST fills the viewport at the bounds equator. 1.6× sits
    // slightly inside that, so the sphere extends a hair past the canvas
    // edges — gives the model real presence without clipping the silhouette.
    // (Previous 2.4× left the model floating in a sea of dark gray.)
    return Math.max(80, modelRadius * 1.6)
  }

  function sizeToBox() {
    if (!canvas) return
    const w = Math.max(1, Math.floor(canvas.clientWidth))
    const h = Math.max(1, Math.floor(canvas.clientHeight))
    if (canvas.width !== w) canvas.width = w
    if (canvas.height !== h) canvas.height = h
  }

  function applyCamera() {
    if (!scene || !canvas) return
    const w = canvas.clientWidth || 1
    const h = canvas.clientHeight || 1
    const aspect = w / h
    const cp = Math.cos(pitch), sp = Math.sin(pitch)
    const cy = Math.cos(yaw), sy = Math.sin(yaw)
    const cx = modelCenter[0], cyC = modelCenter[1], cz = modelCenter[2]
    const eye = [
      cx - sy * cp * distance,
      cyC - cy * cp * distance,
      cz + sp * distance,
    ]
    const target = [cx, cyC, cz]
    scene.camera.perspective(Math.PI / 3, aspect,
      Math.max(1, distance * 0.001),
      Math.max(5000, distance * 10))
    scene.camera.moveToAndFace(eye, target, [0, 0, 1])
    scene.camera.update()
  }

  function tick() {
    if (disposed || !viewer) return
    sizeToBox()
    applyCamera()
    // Pass REAL elapsed ms, not the lib's default 16.67ms — otherwise
    // animations play 2-4× too fast on 120-240Hz monitors. Same fix the main
    // scene received in commit c916013 (project memory). Clamped so a tab-
    // wake or a hitch can't fast-forward several seconds in one tick.
    try {
      const nowTs = performance.now()
      const dtMs = Math.min(MAX_DT_MS, Math.max(0, nowTs - lastFrameTs))
      lastFrameTs = nowTs
      viewer.updateAndRender(dtMs)
    } catch (e) {
      error = 'render: ' + (e instanceof Error ? e.message : String(e))
    }
    rafId = requestAnimationFrame(tick)
  }

  function reset() {
    yaw = DEFAULT_YAW
    pitch = DEFAULT_PITCH
    if (model) distance = fitDistanceToModel()
  }

  function clearInstance() {
    if (instance) {
      try { instance.detach() } catch { /* already detached */ }
      instance = null
    }
    model = null
  }

  async function tryLoadOne(path: string): Promise<any | null> {
    if (!viewer) return null
    try {
      const m = await viewer.load(path, pathSolver)
      if (m && typeof m.addInstance === 'function') return m
      return null
    } catch {
      // The viewer's 'error' listener (attached in initViewer) is what stops
      // emit('error', ...) from throwing the Node EventEmitter "Unhandled
      // error. (undefined)" up here. We still catch defensively in case a
      // future code path throws synchronously.
      return null
    }
  }

  async function loadModel(path: string) {
    if (!viewer) return
    const token = ++loadToken
    error = ''
    // Walk modelPath then each fallback. First non-null wins. Mirrors
    // placeDoodad's variant → unsuffixed → other-extension chain.
    const candidates = [path, ...modelPathFallbacks].filter(Boolean)
    let m: any = null
    let triedPaths: string[] = []
    for (const candidate of candidates) {
      triedPaths.push(candidate)
      m = await tryLoadOne(candidate)
      if (token !== loadToken || disposed) return
      if (m) break
    }
    if (!m) {
      error = 'load failed: ' + triedPaths.join(' / ')
      clearInstance()
      return
    }
    clearInstance()
    model = m
    const inst = m.addInstance()
    inst.setScene(scene)
    inst.move([0, 0, 0])
    inst.uniformScale(1)
    inst.setTeamColor(teamColor)
    // Stand pose for the preview; pick the first 'stand' if present, else
    // sequence 0. Animation plays on its own via viewer.updateAndRender.
    const seqs: Array<{ name: string }> = m.sequences || []
    let idx = seqs.findIndex(s => /^\s*stand/i.test(s.name))
    if (idx < 0 && seqs.length > 0) idx = 0
    if (idx >= 0) inst.setSequence(idx)
    instance = inst
    // Bounds come from the lib's MdxModel; some models have r=0 (e.g. pure
    // particle-emitter dummies) — fall back to a sensible default so the
    // camera doesn't end up inside the model.
    const b = m.bounds
    if (b && b.r > 0) {
      modelCenter = [b.x ?? 0, b.y ?? 0, b.z ?? 0]
      modelRadius = b.r
    } else {
      modelCenter = [0, 0, 0]
      modelRadius = 200
    }
    reset()
  }



  function rebuildViewer() {
    teardownViewer()
    initViewer()
    if (modelPath) void loadModel(modelPath)
  }

  function teardownViewer() {
    if (rafId) { cancelAnimationFrame(rafId); rafId = 0 }
    clearInstance()
    // mdx-m3-viewer has no clean dispose; dropping refs lets GC reclaim the
    // GL context when the canvas detaches. We null the locals so the next
    // initViewer() can't accidentally reuse stale handles.
    scene = null
    viewer = null
  }

  function initViewer() {
    try {
      const v = MV?.viewer
      if (!v || !v.ModelViewer || !v.handlers) {
        throw new Error('mdx-m3-viewer namespace shape unexpected')
      }
      sizeToBox()
      viewer = new v.ModelViewer(canvas)
      // CRITICAL: mdx-m3-viewer extends Node's EventEmitter. When viewer.load
      // fails (404, missing handler, etc.) the lib calls emit('error', ...).
      // EventEmitter THROWS synchronously on emit('error') when no listener
      // is registered — surfaces in the console as "Unhandled error.
      // (undefined)" because the lib's error payload is an object, not an
      // Error. Without this listener, EVERY load failure crashes the load
      // path before our `catch (e)` can swallow it. The main scene-instances
      // attaches the same listener for the same reason (see line ~431).
      viewer.on('error', () => { /* swallowed; load() resolves to undefined */ })
      viewer.addHandler(v.handlers.blp)
      viewer.addHandler(v.handlers.dds)
      viewer.addHandler(v.handlers.tga)
      viewer.addHandler(v.handlers.mdx, pathSolver, reforged)
      try { v.handlers.mdx.loadTeamTextures(viewer) } catch { /* non-fatal */ }
      scene = viewer.addScene()
      ;(scene as any).color = [0.07, 0.07, 0.09]
      currentReforged = reforged
      // Reset the dt baseline so the first tick after (re)init doesn't see
      // a multi-second gap from the previous viewer lifetime / Svelte mount.
      lastFrameTs = performance.now()
      rafId = requestAnimationFrame(tick)
    } catch (e) {
      error = 'init failed: ' + (e instanceof Error ? e.message : String(e))
      viewer = null
      scene = null
    }
  }

  // Orbit input — drag anywhere on the canvas. Wheel zooms. Right-click does
  // NOT pan (no need on a single-model preview); contextmenu is just blocked
  // so right-drag-as-orbit feels natural.
  let dragging = false
  let lastX = 0, lastY = 0
  const YAW_SENS = 0.01
  const PITCH_SENS = 0.008
  const PITCH_MIN = -Math.PI / 2 + 0.05
  const PITCH_MAX = Math.PI / 2 - 0.05

  function onDown(e: MouseEvent) {
    dragging = true
    lastX = e.clientX; lastY = e.clientY
    e.preventDefault()
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }
  function onMove(e: MouseEvent) {
    if (!dragging) return
    const dx = e.clientX - lastX
    const dy = e.clientY - lastY
    lastX = e.clientX; lastY = e.clientY
    yaw -= dx * YAW_SENS
    pitch = Math.max(PITCH_MIN, Math.min(PITCH_MAX, pitch + dy * PITCH_SENS))
  }
  function onUp() {
    dragging = false
    window.removeEventListener('mousemove', onMove)
    window.removeEventListener('mouseup', onUp)
  }
  function onWheel(e: WheelEvent) {
    e.preventDefault()
    const factor = e.deltaY > 0 ? 1.15 : 1 / 1.15
    // Clamp around the model radius — let the user push in fairly close
    // (0.4×radius is "stuck to the model") and pull out 8× for context.
    const minD = Math.max(20, modelRadius * 0.4)
    const maxD = Math.max(2000, modelRadius * 8)
    distance = Math.max(minD, Math.min(maxD, distance * factor))
  }

  onMount(() => {
    initViewer()
  })

  onDestroy(() => {
    disposed = true
    window.removeEventListener('mousemove', onMove)
    window.removeEventListener('mouseup', onUp)
    teardownViewer()
  })
  // Reactive reload on path/reforged change. Guard against null/empty path.
  run(() => {
    if (viewer && currentReforged === reforged) {
      if (modelPath) void loadModel(modelPath)
      else clearInstance()
    }
  });
  // Reforged toggle: rebuild the viewer so the MDX handler re-registers with
  // the new flag. Cheap enough — the preview holds at most one model.
  run(() => {
    if (viewer && currentReforged !== reforged) {
      rebuildViewer()
    }
  });
</script>

<div class="preview">
  <canvas bind:this={canvas}
          onmousedown={onDown}
          use:nonpassive={['wheel', () => onWheel]}
          oncontextmenu={preventDefault(bubble('contextmenu'))}></canvas>
  <button class="reset" onclick={reset} title="Reset view">Reset</button>
  {#if error}<div class="error">{error}</div>{/if}
</div>

<style>
  .preview {
    position: relative;
    width: 100%;
    aspect-ratio: 4 / 3;
    background: #0e0e10;
    border-bottom: 1px solid #27272a;
    overflow: hidden;
    flex: 0 0 auto;
  }
  canvas {
    display: block;
    width: 100%;
    height: 100%;
    cursor: grab;
  }
  canvas:active { cursor: grabbing; }
  .reset {
    position: absolute; top: 6px; right: 6px;
    background: rgba(24, 24, 27, 0.85); color: #d4d4d8;
    border: 1px solid #3f3f46; border-radius: 3px;
    padding: 2px 8px; font-size: 11px; cursor: pointer;
  }
  .reset:hover { background: rgba(63, 63, 70, 0.95); color: #fff; }
  .error {
    position: absolute; bottom: 6px; left: 6px; right: 6px;
    background: rgba(127, 29, 29, 0.9); color: #fecaca;
    padding: 3px 6px; font-size: 11px;
    font-family: 'Cascadia Mono', Consolas, monospace;
    border-radius: 2px;
  }
</style>
