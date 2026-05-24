// scene-instances.ts — uses mdx-m3-viewer at the LOWER level, bypassing
// War3MapViewer.loadMap (which is brittle on modern Reforged W3X files;
// see project-wc3-forge memory entry for the diagnosis).
//
// What we use from the library:
//   - viewer.ModelViewer    — WebGL context, resource cache, RAF orchestration
//   - viewer.Scene          — instance container, camera, frustum culling
//   - viewer.handlers.mdx   — MDX parsing, skeletal animation, batch shaders
//   - viewer.handlers.blp   — BLP texture decoding
//   - viewer.handlers.dds   — DDS texture decoding (Reforged HD assets)
//   - viewer.handlers.tga   — TGA fallback
//
// What we DO NOT use:
//   - War3MapViewer.loadMap and parsers/w3x/* — see memory entry.
//   - The lib's terrain/cliff/water shaders. Terrain is drawn ourselves
//     into the same scene via terrain.ts, between viewer.startFrame() and
//     viewer.render(); the scene is set to alpha=true so its own startFrame
//     doesn't clear our depth buffer.
//   - SimpleOrbitCamera from clients/shared — we ship a WC3-style RTS cam.
//
// What feeds this scene:
//   - Go-parsed map data via the App.* Wails methods.
//   - Stock assets via /asset/<path> served by Go (map MPQ → CASC fallback).
//
// Lifecycle:
//   createScene(canvas) is called once on mount. The returned SceneAPI is
//   stable for the canvas's lifetime; loadMap() is called every time the
//   Wails map-changed event fires.

import * as MV_ns from 'mdx-m3-viewer'
import { flog } from './debuglog'
import {
  ListUnits, ListDoodads, GetUnitTypeIndex, GetDoodadTypeIndex, GetTerrain,
} from '../wailsjs/go/main/App.js'
import { buildTerrain, type TerrainMesh } from './terrain'
import { buildWater, type WaterMesh } from './water'
import { createCamera, type RTSCamera } from './camera'
import { computeCliffPlacements, renderCliffs, type CliffRendering } from './cliffs'
import {
  buildSlocRenderer, type SlocRenderer, type SlocMarker,
} from './sloc-markers'

const MV: any = (MV_ns as any).default ?? MV_ns

// Defensive access to the lib's namespaced classes. CJS+ESM interop is
// finicky enough that a single bundler config change can move things from
// `MV.foo` to `MV.default.foo`; we crash with a descriptive error if either
// shape is missing rather than getting "undefined is not a constructor"
// somewhere deep in user code.
function getMV() {
  const viewer = MV?.viewer
  const ModelViewer = viewer?.ModelViewer
  const handlers = viewer?.handlers
  if (!ModelViewer || !handlers?.mdx || !handlers?.blp || !handlers?.dds || !handlers?.tga) {
    const v = viewer ? Object.keys(viewer) : []
    const h = handlers ? Object.keys(handlers) : []
    throw new Error(`mdx-m3-viewer shape unexpected. viewer=[${v.join(',')}] handlers=[${h.join(',')}]`)
  }
  return { ModelViewer, handlers }
}

// pathSolver converts the library's raw asset name ("Units\Human\Footman\
// Footman.mdx") into a fetchable URL ("/asset/units/human/footman/footman.mdx").
// Wails' embedded HTTP server routes that to our Go assetHandler, which does
// map-MPQ-then-CASC lookup. mdx-m3-viewer accepts either bytes or a URL from
// pathSolver; URLs let us keep all byte-source resolution in Go.
export function pathSolver(src: unknown): unknown {
  if (typeof src !== 'string') return undefined
  return '/asset/' + src.toLowerCase().replace(/\\/g, '/')
}

// Mirrors main.UnitTypeInfo / main.DoodadTypeInfo (Wails drops them from
// models.ts when they appear as a map value — we declare the shapes locally
// instead).
interface UnitTypeInfo {
  file: string
  model_scale: number
  move_height: number
  red: number
  green: number
  blue: number
}

interface DoodadTypeInfo {
  file: string
  num_var: number
  fixed_rot: number
  model_scale: number
}

// Resolve the SLK 'file' column to an asset path. SLK rows for stock
// content come without an extension (we append .mdx — the lib's MDX handler
// also accepts MDL via content-magic sniffing, but stock assets are MDX).
// w3d-override rows from custom maps DO carry the extension (often .mdl
// for imported text-format models); we preserve it as-is.
function mdxPath(file: string): string {
  if (/\.(mdl|mdx)$/i.test(file)) return file
  return file + '.mdx'
}

// Axis-angle quaternion around +Z (game-space yaw). Avoids pulling gl-matrix
// in as a direct dep — it's transitive via mdx-m3-viewer, but the surface
// shape is small enough that depending on the transitive package is fragile.
function quatZ(angle: number): number[] {
  const h = angle * 0.5
  return [0, 0, Math.sin(h), Math.cos(h)]
}

// Inline replacement for handlers/w3x/standsequence so we don't depend on
// that file's deep import path. Sequences in MDX models are loose — names
// often look like "Stand", "Stand - 1", "Stand Ready", etc. The lib's
// rarity-weighted pick is nice-to-have; first-match is fine for now.
function setStandSequence(instance: any): void {
  const seqs = instance?.model?.sequences
  if (!Array.isArray(seqs)) return
  for (let i = 0; i < seqs.length; i++) {
    const name = String(seqs[i]?.name ?? '').toLowerCase()
    if (name === 'stand' || name.startsWith('stand ') || name.startsWith('stand-')) {
      instance.setSequence(i)
      return
    }
  }
}

export interface SceneAPI {
  /** Re-populate the scene from current Go-side state. Called on map-changed. */
  loadMap(): Promise<void>
  /** Drop every instance we created. Models stay in the viewer cache. */
  clear(): void
  /** Stop the RAF loop and tear down listeners. */
  dispose(): void
  /** Tint selected unit instances; pass empty Set to clear highlight. */
  setSelected(creationNumbers: Set<number>): void
  /** Register a callback fired when the user clicks a unit in the viewport. */
  onUnitClicked(cb: (creationNumber: number) => void): void
  /**
   * Flip the MDX handler's reforged flag in-place. Drops cached MDX models +
   * team-color textures so the next loadMap() reloads them under the new mode.
   * Caller is responsible for triggering loadMap() after this.
   */
  setReforgedMode(reforged: boolean): void
  /** Current reforged flag — for UI display. */
  isReforged(): boolean
  /**
   * Multiply every doodad's uniform scale by this factor on the next loadMap.
   * Doodads are visually tiny relative to map size (a brazier is ~50 studs in
   * an 18000-stud map). A 4x boost makes them readable at default zoom; 1x
   * is the WC3-accurate baseline. The change applies to the NEXT loadMap —
   * call loadMap() after setting if you want it to take effect immediately.
   */
  setDoodadScale(multiplier: number): void
}

export function createScene(canvas: HTMLCanvasElement, reforged: boolean = false): SceneAPI {
  const { ModelViewer, handlers } = getMV()

  // mdx-m3-viewer reads canvas.width/height as PIXELS — keep them synced
  // with the CSS box or everything's blurry (lib README's headline gotcha).
  const sizeToBox = () => {
    const w = Math.max(1, Math.floor(canvas.clientWidth))
    const h = Math.max(1, Math.floor(canvas.clientHeight))
    if (canvas.width !== w) canvas.width = w
    if (canvas.height !== h) canvas.height = h
  }
  sizeToBox()

  const viewer = new ModelViewer(canvas)
  ;(window as any).__viewer = viewer // devtools inspection
  flog(`[scene] init reforged=${reforged}`)

  // Handler order matters: BLP/DDS/TGA before MDX so the MDX loader can
  // route texture loads to the right decoder. Texture handlers don't take
  // pathSolver/options; MDX handler does — its third arg (reforged) toggles
  //   - 28 team-color textures (vs 16 in SD)
  //   - .dds team-color URL extension (vs .blp in SD)
  //   - HD-aware sound + splat lookups in getEventObjectData
  // The shader variant (sd vs hd) is chosen per-batch at draw time based on
  // each model's own `solverParams.hd` flag, not this global.
  viewer.addHandler(handlers.blp)
  viewer.addHandler(handlers.dds)
  viewer.addHandler(handlers.tga)
  viewer.addHandler(handlers.mdx, pathSolver, /* reforged */ reforged)

  // Team colors aren't loaded automatically when using the lower-level API.
  // Pre-warm them so units have player tint from the first frame they render.
  let currentReforged = reforged
  try { handlers.mdx.loadTeamTextures(viewer) } catch (e) {
    flog('[loadTeamTextures]', e instanceof Error ? e.message : String(e))
  }

  const scene = viewer.addScene()
  // alpha=true → scene.startFrame() WON'T clear the framebuffer. We render
  // terrain ourselves between viewer.startFrame() and viewer.render(), so
  // we need the depth buffer to survive into the scene's render pass.
  ;(scene as any).alpha = true
  ;(scene as any).color = [0.07, 0.07, 0.09] // matches the app's dark chrome

  // WC3-style RTS camera. Mutates scene.camera in response to mouse/keyboard;
  // initial state has it framing the world origin from a 45°-down angle, and
  // loadMap() will re-frame whenever a new map opens.
  const camera = createCamera(canvas, scene.camera)

  // Visible-error logger: dedupe by (message + target URL) so a single
  // missing asset doesn't spam the log thousands of times per second.
  const seenErrors = new Set<string>()
  viewer.on('error', (target: unknown, error: unknown) => {
    const msg = error instanceof Error ? error.message : String(error)
    const url = (target as any)?.fetchUrl ?? '(no url)'
    const key = msg + ' :: ' + url
    if (seenErrors.has(key)) return
    seenErrors.add(key)
    flog('[viewer error]', msg, 'url:', url)
  })

  let crashed = false
  let rafId = 0
  let terrain: TerrainMesh | null = null
  let water: WaterMesh | null = null
  let cliffs: CliffRendering | null = null
  let slocRenderer: SlocRenderer | null = null
  // Multiplier applied to each doodad's uniformScale at placeDoodad time.
  // Default 1 = WC3-accurate; the UI can dial this up to make tiny doodads
  // visible without having to mouse-wheel-zoom in.
  let doodadScaleMul = 1
  // Tracks the current selection so we can paint a tint on selected unit
  // instances on every render-applicable state change (selection arrives via
  // setSelected; the tint persists across loadMap since we re-apply on every
  // placeUnit call when the cn is in this set). Declared here (before the
  // render loop's first invocation) to satisfy TDZ — the loop closure
  // captures the binding name and would crash on first invocation otherwise.
  let selectedSet: Set<number> = new Set()

  // Sloc-marker renderer. Owns its own shader + box geometry. Failure to
  // build is non-fatal: we just won't have visible markers. Built here
  // (after `let slocRenderer = null` above) rather than earlier, because
  // assigning to a `let` before its declaration is a TDZ violation in JS.
  try {
    slocRenderer = buildSlocRenderer((viewer as any).gl as WebGLRenderingContext)
  } catch (e) {
    flog('[slocs] init failed:', e instanceof Error ? e.message : String(e))
  }
  // Background color (used by our own clear call now that scene.alpha=true).
  const bg = [0.07, 0.07, 0.09]
  const loop = () => {
    if (!crashed) {
      try {
        sizeToBox()
        // Push the canvas aspect through the camera controller; setAspect
        // no-ops if unchanged, so this is cheap to call every frame.
        camera.setAspect(canvas.width / Math.max(1, canvas.height))
        // Scene viewport defaults to [0,0,0,0]; keep it pinned to the canvas
        // so frustum culling + (later) screen→world ray casts use the right
        // pixel dims.
        const vp = (scene as any).viewport
        if (vp) {
          vp[0] = 0; vp[1] = 0; vp[2] = canvas.width; vp[3] = canvas.height
        }
        // Split updateAndRender so we can interleave our terrain pass:
        //   1. update — animation/particle tick (no GL state changes)
        //   2. our manual clear (scene.alpha=true means scene won't clear)
        //   3. terrain.draw — writes color + depth across the framebuffer
        //   4. scene.render — units render on top, depth-tested vs terrain
        const gl = (viewer as any).gl as WebGLRenderingContext
        viewer.update(1000 / 60)
        gl.viewport(0, 0, canvas.width, canvas.height)
        gl.depthMask(true)
        gl.clearColor(bg[0], bg[1], bg[2], 1)
        gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
        if (terrain) terrain.draw(scene.camera.viewProjectionMatrix)
        viewer.render()
        // Sloc markers go AFTER viewer.render() and BEFORE water so they're
        // opaque-on-top-of-units but still get alpha-blended water in front.
        // Slocs are small and rare (typically 2-12 per map), so drawing N
        // pillars per frame is cheap enough that we don't bother with any
        // visibility culling.
        if (slocRenderer) slocRenderer.draw(scene.camera.viewProjectionMatrix, selectedSet)
        // Water is translucent — must render LAST so it alpha-blends on top
        // of terrain + cliffs + opaque model passes. The lib's translucent
        // pass already ran inside viewer.render(); water sits above that.
        if (water) water.draw(scene.camera.viewProjectionMatrix)
      } catch (e) {
        crashed = true
        flog('[render-loop crash]', e instanceof Error ? e.stack : String(e))
      }
    }
    rafId = requestAnimationFrame(loop)
  }
  loop()

  const ro = new ResizeObserver(sizeToBox)
  ro.observe(canvas)

  // Active instances we placed during the most recent loadMap. Keyed by
  // creation_number so selection events can highlight individually. Models
  // remain cached in viewer.resourceMap across loads — only instances are
  // dropped on re-open.
  const unitInstances = new Map<number, any>()
  const doodadInstances: any[] = []
  let pickCallback: ((cn: number) => void) | null = null
  // Note: selectedSet is declared earlier in this function (before the
  // render loop runs) to avoid a TDZ trap; see comment up there.
  const SELECT_TINT: [number, number, number, number] = [1.2, 1.4, 0.6, 1] // warm yellow boost

  // Unit type index: stock-only and process-stable (we don't yet apply
  // per-map w3u modifications), so cache for process lifetime.
  let unitTypeIndexCache: Record<string, UnitTypeInfo> | null = null
  async function getUnitTypes(): Promise<Record<string, UnitTypeInfo>> {
    if (!unitTypeIndexCache) {
      unitTypeIndexCache = (await GetUnitTypeIndex()) as unknown as Record<string, UnitTypeInfo>
    }
    return unitTypeIndexCache
  }
  // Doodad type index includes per-map w3d/w3b overlays, so it must be
  // re-fetched on every loadMap. Stored as a per-load promise so concurrent
  // placeDoodad calls share one fetch.
  async function getDoodadTypes(): Promise<Record<string, DoodadTypeInfo>> {
    return (await GetDoodadTypeIndex()) as unknown as Record<string, DoodadTypeInfo>
  }

  // Place a single unit. Pulls model via viewer.load (deduplicated via
  // viewer.resourceMap so repeat type-ids reuse the same MdxModel) and
  // returns the placed instance, or null if the type has no MDX.
  async function placeUnit(unit: any, types: Record<string, UnitTypeInfo>): Promise<any | null> {
    const info = types[unit.type_id]
    if (!info || !info.file) return null
    const path = mdxPath(info.file)
    let model: any
    try {
      model = await viewer.load(path, pathSolver)
    } catch (e) {
      flog('[unit load fail]', unit.type_id, path, e instanceof Error ? e.message : String(e))
      return null
    }
    if (!model || typeof model.addInstance !== 'function') return null

    const inst = model.addInstance()
    inst.move([unit.position[0], unit.position[1], unit.position[2] + info.move_height])
    inst.rotateLocal(quatZ(unit.rotation))
    inst.uniformScale(unit.scale[0] * (info.model_scale || 1))
    inst.setTeamColor(unit.player)
    if (info.red || info.green || info.blue) {
      inst.setVertexColor([info.red / 255, info.green / 255, info.blue / 255, 1])
    }
    inst.setScene(scene)
    setStandSequence(inst)
    if (selectedSet.has(unit.creation_number)) {
      inst.setVertexColor(SELECT_TINT)
    }
    return inst
  }

  // Place a single doodad. Doodads can have N variations on disk; the
  // file path picks up a numeric suffix when numVar > 1 (e.g. ATtr0.mdx,
  // ATtr1.mdx, …). Falls back to the unsuffixed name if the variant 404s.
  //
  // Custom-map overrides (war3map.w3d) often supply a fully-qualified path
  // with extension already attached (`war3mapImported\Lantern.mdl`); we
  // preserve the original extension when present and only synthesize one
  // for the stock-style "no-extension" stem case.
  //
  // Doodad scale is stored RAW in war3map.doo (memory note: no /128 divide),
  // so we pass unit.scale through verbatim, multiplied by the SLK base scale.
  async function placeDoodad(d: any, types: Record<string, DoodadTypeInfo>): Promise<any | null> {
    const info = types[d.type_id]
    if (!info || !info.file) return null
    const extMatch = info.file.match(/\.(mdl|mdx)$/i)
    const declaredExt = extMatch ? extMatch[0] : '.mdx'
    const stem = extMatch ? info.file.slice(0, -extMatch[0].length) : info.file
    const variantIdx = Math.min(Math.max(0, d.variation), Math.max(0, info.num_var - 1))

    // Try (in order):
    //   1. variant path with the declared extension
    //   2. unsuffixed path with the declared extension
    //   3. unsuffixed path with the OTHER extension (custom maps often
    //      declare .mdl but ship .mdx, or vice versa — mdx-m3-viewer's
    //      MDX handler accepts both formats from the same handler)
    const otherExt = declaredExt.toLowerCase() === '.mdx' ? '.mdl' : '.mdx'
    const variantPath = (info.num_var > 1 ? stem + variantIdx : stem) + declaredExt
    const fallbackPath = stem + declaredExt
    const otherExtPath = stem + otherExt

    let model: any
    for (const p of [variantPath, fallbackPath, otherExtPath]) {
      if (model && typeof model.addInstance === 'function') break
      try { model = await viewer.load(p, pathSolver) } catch { /* swallow, try next */ }
    }
    if (!model || typeof model.addInstance !== 'function') return null

    const inst = model.addInstance()
    inst.move([d.position[0], d.position[1], d.position[2]])
    inst.rotateLocal(quatZ(d.rotation))
    inst.uniformScale((d.scale[0] || 1) * (info.model_scale || 1) * doodadScaleMul)
    inst.setScene(scene)
    setStandSequence(inst)
    return inst
  }

  function clearInstances() {
    for (const inst of unitInstances.values()) {
      try { inst.detach() } catch { /* already detached */ }
    }
    unitInstances.clear()
    for (const inst of doodadInstances) {
      try { inst.detach() } catch { /* already detached */ }
    }
    doodadInstances.length = 0
    // Slocs hold no GL resources of their own — they live in the renderer's
    // marker list. Replace with empty list so the next loadMap re-populates.
    slocRenderer?.setMarkers([])
    if (terrain) {
      terrain.dispose()
      terrain = null
    }
    if (water) {
      water.dispose()
      water = null
    }
    if (cliffs) {
      cliffs.dispose()
      cliffs = null
    }
  }

  // --- Picking: ray vs unit-sphere ---
  //
  // Each unit instance's mdx-m3-viewer Bounds object is sphere-shaped
  // (despite the type's name) — center + radius, in model-local space.
  // We translate to world space using the instance's worldLocation, then
  // do ray-sphere intersection. Closest hit wins.
  //
  // Click vs drag is detected by tracking mousedown/mouseup pixel distance;
  // we ignore MMB/RMB (those are camera drag gestures).
  const CLICK_PIXEL_THRESHOLD = 5
  let downAt: { x: number; y: number; button: number } | null = null

  function rayPick(px: number, py: number): number | null {
    // screenToWorldRay outputs near + far points (vec3 + vec3 = 6 floats).
    const out = new Float32Array(6)
    const vp = (scene as any).viewport as Float32Array
    // mdx-m3-viewer convention: screen Y origin at canvas BOTTOM. DOM events
    // give Y from the top, so flip.
    const screen = new Float32Array([px, canvas.clientHeight - py])
    scene.camera.screenToWorldRay(out, screen, vp)
    const ox = out[0], oy = out[1], oz = out[2]
    const dx = out[3] - ox, dy = out[4] - oy, dz = out[5] - oz
    const len = Math.hypot(dx, dy, dz)
    if (len < 1e-6) return null
    const rx = dx / len, ry = dy / len, rz = dz / len

    let bestCn: number | null = null
    let bestT = Infinity
    for (const [cn, inst] of unitInstances) {
      const b = inst.getBounds?.()
      if (!b || b.r <= 0) continue
      const sx = inst.worldScale?.[0] ?? 1
      const sy = inst.worldScale?.[1] ?? 1
      const sz = inst.worldScale?.[2] ?? 1
      const biggest = Math.max(sx, sy, sz)
      const cx = inst.worldLocation[0] + b.x
      const cy = inst.worldLocation[1] + b.y
      const cz = inst.worldLocation[2] + b.z
      const r = b.r * biggest
      // Ray-sphere intersection. Standard quadratic:
      //   |o + t*d - c|² = r²  →  t² - 2t·(d·(c-o)) + |c-o|²-r² = 0
      const lx = cx - ox, ly = cy - oy, lz = cz - oz
      const tca = lx * rx + ly * ry + lz * rz
      if (tca < 0) continue // behind the camera
      const d2 = lx * lx + ly * ly + lz * lz - tca * tca
      if (d2 > r * r) continue // ray misses sphere
      const thc = Math.sqrt(r * r - d2)
      const t = tca - thc // nearest intersection in front of camera
      if (t < bestT) {
        bestT = t
        bestCn = cn
      }
    }

    // Sloc markers — ray vs AABB (slab method). Slocs have no MDX so they're
    // not in unitInstances; rays still need to hit them for selection.
    const slocInfos = slocRenderer?.pickInfos() ?? []
    for (const s of slocInfos) {
      // Per-axis slab intersection. Ray dir component near zero gets clamped
      // to ±Infinity so we don't divide-by-zero and the per-axis interval
      // collapses to "always inside" iff the ray origin is within the slab.
      const cx = s.center[0], cy = s.center[1], cz = s.center[2]
      const hx = s.half[0], hy = s.half[1], hz = s.half[2]
      const minX = cx - hx, maxX = cx + hx
      const minY = cy - hy, maxY = cy + hy
      const minZ = cz - hz, maxZ = cz + hz
      let tmin = -Infinity, tmax = Infinity
      const axes: Array<[number, number, number, number]> = [
        [rx, ox, minX, maxX],
        [ry, oy, minY, maxY],
        [rz, oz, minZ, maxZ],
      ]
      let skip = false
      for (const [rd, ro, lo, hi] of axes) {
        if (Math.abs(rd) < 1e-9) {
          // Ray parallel to this slab — must be inside it or no hit.
          if (ro < lo || ro > hi) { skip = true; break }
          continue
        }
        let t1 = (lo - ro) / rd
        let t2 = (hi - ro) / rd
        if (t1 > t2) { const tmp = t1; t1 = t2; t2 = tmp }
        if (t1 > tmin) tmin = t1
        if (t2 < tmax) tmax = t2
        if (tmin > tmax) { skip = true; break }
      }
      if (skip) continue
      // tmin < 0 means the ray origin is inside the box — pick anyway,
      // using tmax as the exit point. tmax < 0 means the box is entirely
      // behind the camera, skip.
      const t = tmin >= 0 ? tmin : tmax
      if (t < 0) continue
      if (t < bestT) {
        bestT = t
        bestCn = s.creationNumber
      }
    }
    return bestCn
  }

  function onCanvasMouseDown(e: MouseEvent) {
    downAt = { x: e.clientX, y: e.clientY, button: e.button }
  }
  function onCanvasMouseUp(e: MouseEvent) {
    const d = downAt
    downAt = null
    if (!d || d.button !== 0 || e.button !== 0) return
    const dist = Math.hypot(e.clientX - d.x, e.clientY - d.y)
    if (dist > CLICK_PIXEL_THRESHOLD) return // it was a drag, not a click
    const r = canvas.getBoundingClientRect()
    const cn = rayPick(e.clientX - r.left, e.clientY - r.top)
    if (cn != null && pickCallback) pickCallback(cn)
  }
  canvas.addEventListener('mousedown', onCanvasMouseDown)
  canvas.addEventListener('mouseup', onCanvasMouseUp)

  return {
    async loadMap() {
      clearInstances()
      // Terrain first so it's visible even while units/doodads stream in.
      try {
        const t = await GetTerrain()
        const gl = (viewer as any).gl as WebGLRenderingContext
        terrain = await buildTerrain(gl, viewer as any, pathSolver, t as unknown as any)
        if (terrain) {
          // Frame the map. width/height in w3e are vertex counts; tile size
          // is 128, so playable span is roughly (width-1)*128 in each axis.
          const tw = ((t as any).width - 1) * 128
          const th = ((t as any).height - 1) * 128
          const span = Math.max(tw, th)
          // Center the camera on the actual mesh center, not game-coord origin.
          // For maps with non-symmetric border padding, those differ.
          const cx = (t as any).center_offset[0] + tw / 2
          const cy = (t as any).center_offset[1] + th / 2
          camera.frame(cx, cy, span)
        }
        // Cliff transition meshes. Walks per-corner layer-heights and
        // selects the appropriate Doodads/Terrain/Cliffs/Cliffs<pattern>N.mdx
        // for each cell with a layer-height transition.
        const cliffPlacements = computeCliffPlacements(t as unknown as any)
        if (cliffPlacements.length > 0) {
          cliffs = await renderCliffs(viewer as any, scene, pathSolver, cliffPlacements)
        }
        // Water surface. Per-cell quad mesh with HiveWE-style depth-blended
        // color × animated wave texture from Water.slk. Rendered after
        // viewer.render() in the RAF loop with alpha blending — depth-test
        // ON, depth-write OFF so multiple translucent pixels at the same Z
        // don't fight.
        water = await buildWater(gl, viewer as any, pathSolver, t as unknown as any)
        if (water) {
          flog(`[water] ${water.triCount} triangles, ${water.frameCount} animation frames`)
        }
      } catch (e) {
        flog('[terrain load]', e instanceof Error ? e.message : String(e))
      }
      const [units, doodads, uTypes, dTypes] = await Promise.all([
        ListUnits(), ListDoodads(), getUnitTypes(), getDoodadTypes(),
      ])
      // Split slocs out of the unit list. Slocs (start-location markers)
      // have type_id "sloc" and no MDX in stock data — they're rendered as
      // colored pillars by slocRenderer instead. Filter here so placeUnit
      // doesn't try to load a non-existent MDX for each one.
      const slocMarkers: SlocMarker[] = []
      const realUnits: typeof units = []
      for (const u of units) {
        if (u.type_id === 'sloc') {
          slocMarkers.push({
            creationNumber: u.creation_number,
            player: u.player,
            position: [u.position[0], u.position[1], u.position[2]],
            rotation: u.rotation,
          })
        } else {
          realUnits.push(u)
        }
      }
      slocRenderer?.setMarkers(slocMarkers)
      // Quick diagnostic: log a sloc bounding box so we can spot maps where
      // every sloc is degenerately stacked at the same position (Enfo's FFB
      // does this — see the doodad-audit notes in the project memory).
      if (slocMarkers.length > 0) {
        let sxmin = Infinity, sxmax = -Infinity, symin = Infinity, symax = -Infinity
        for (const m of slocMarkers) {
          if (m.position[0] < sxmin) sxmin = m.position[0]
          if (m.position[0] > sxmax) sxmax = m.position[0]
          if (m.position[1] < symin) symin = m.position[1]
          if (m.position[1] > symax) symax = m.position[1]
        }
        const stacked = (sxmin === sxmax && symin === symax)
        flog(`[slocs] n=${slocMarkers.length} bbox=x[${sxmin.toFixed(0)},${sxmax.toFixed(0)}] y[${symin.toFixed(0)},${symax.toFixed(0)}]${stacked ? ' (all stacked at same pos)' : ''}`)
      }

      // Sequential to keep diagnostics legible during bring-up; once stable
      // we'll batch via Promise.allSettled for parallel asset fetching.
      let uPlaced = 0, uSkipped = 0
      for (const u of realUnits) {
        const inst = await placeUnit(u, uTypes)
        if (inst) {
          unitInstances.set(u.creation_number, inst)
          uPlaced++
        } else {
          uSkipped++
        }
      }
      // Doodad placement + audit instrumentation.
      //
      // We collect:
      //   - placed/skipped counters
      //   - per-typeID skip counts (typeIDs that 404'd or had no MDX entry,
      //     which lets us spot patterns of missing assets)
      //   - sample world data for the first few placed instances
      //     (worldLocation, worldScale, bounds.r) so we can prove they're
      //     in the right place / right size and decide if "invisible" means
      //     "broken" or just "tiny relative to map span".
      let dPlaced = 0, dSkipped = 0
      let xmin = Infinity, xmax = -Infinity, ymin = Infinity, ymax = -Infinity
      const skipReasons = new Map<string, number>()
      const sampleAudits: string[] = []
      for (const d of doodads) {
        if (d.position[0] < xmin) xmin = d.position[0]
        if (d.position[0] > xmax) xmax = d.position[0]
        if (d.position[1] < ymin) ymin = d.position[1]
        if (d.position[1] > ymax) ymax = d.position[1]
        const inst = await placeDoodad(d, dTypes)
        if (inst) {
          doodadInstances.push(inst)
          dPlaced++
          // Sample the first 5 placements + 5 from later in the list to
          // confirm bounds are sane across the map. The lib's bounds object
          // is a sphere — center + radius in MODEL-local space. World-space
          // radius = bounds.r * max(worldScale). We log both for sanity.
          if (sampleAudits.length < 10 && (dPlaced <= 5 || dPlaced % 100 === 0)) {
            const b = inst.getBounds?.()
            const wl = inst.worldLocation
            const ws = inst.worldScale
            const biggest = Math.max(ws?.[0] ?? 1, ws?.[1] ?? 1, ws?.[2] ?? 1)
            const worldR = b ? (b.r * biggest) : NaN
            sampleAudits.push(
              `#${dPlaced} ${d.type_id} loc=[${wl?.[0]?.toFixed(0)},${wl?.[1]?.toFixed(0)},${wl?.[2]?.toFixed(0)}] scale=[${ws?.[0]?.toFixed(2)},${ws?.[1]?.toFixed(2)},${ws?.[2]?.toFixed(2)}] boundsR=${b?.r?.toFixed(1)} worldR=${worldR.toFixed(1)}`
            )
          }
        } else {
          dSkipped++
          skipReasons.set(d.type_id, (skipReasons.get(d.type_id) ?? 0) + 1)
        }
      }
      flog(`[loadMap] units placed=${uPlaced} skipped=${uSkipped}, doodads placed=${dPlaced} skipped=${dSkipped} doodad-bbox=x[${xmin.toFixed(0)},${xmax.toFixed(0)}] y[${ymin.toFixed(0)},${ymax.toFixed(0)}] slocs=${slocMarkers.length} doodadScale=${doodadScaleMul}x`)
      if (skipReasons.size > 0) {
        // Top 8 most-skipped type IDs so we can spot patterns in missing
        // doodad assets without flooding the log.
        const top = [...skipReasons.entries()]
          .sort((a, b) => b[1] - a[1])
          .slice(0, 8)
          .map(([id, n]) => `${id}×${n}`)
          .join(' ')
        flog(`[doodad audit] skipped-type-ids: ${top}`)
      }
      for (const line of sampleAudits) {
        flog(`[doodad audit] ${line}`)
      }
    },
    clear() {
      clearInstances()
    },
    dispose() {
      clearInstances()
      camera.dispose()
      cancelAnimationFrame(rafId)
      ro.disconnect()
      canvas.removeEventListener('mousedown', onCanvasMouseDown)
      canvas.removeEventListener('mouseup', onCanvasMouseUp)
      if (slocRenderer) {
        slocRenderer.dispose()
        slocRenderer = null
      }
    },
    setSelected(creationNumbers: Set<number>) {
      // Clear tint on previously-selected, paint tint on newly-selected.
      for (const cn of selectedSet) {
        if (creationNumbers.has(cn)) continue
        const inst = unitInstances.get(cn)
        if (inst) inst.setVertexColor([1, 1, 1, 1])
      }
      for (const cn of creationNumbers) {
        if (selectedSet.has(cn)) continue
        const inst = unitInstances.get(cn)
        if (inst) inst.setVertexColor(SELECT_TINT)
      }
      selectedSet = new Set(creationNumbers)
    },
    onUnitClicked(cb: (cn: number) => void) {
      pickCallback = cb
    },
    setReforgedMode(b: boolean) {
      if (b === currentReforged) return
      flog(`[scene] reforged mode: ${currentReforged} -> ${b}`)
      currentReforged = b
      // Mutate the MDX handler's shared cache so getEventObjectData and any
      // future loadTeamTextures call see the new flag. Then drop cached
      // team colors so the next loadTeamTextures reloads the right texture
      // set (28 .dds vs 16 .blp). The shader programs themselves stay —
      // they were all built up front in the handler load() call regardless
      // of mode, and the shader-picker (getBatchShader) consults each
      // batch's own `isHd` flag, not the handler-global reforged.
      const cache: any = (viewer as any).sharedCache?.get?.('mdx')
      if (cache) {
        cache.reforged = b
        cache.teamColors.length = 0
        cache.teamGlows.length = 0
      }
      // Also flush the model + texture resource cache so the next loadMap
      // re-fetches under the new mode. Without this, units retain the SD
      // textures + skeletons they were first loaded with.
      const v: any = viewer
      if (v.resourceMap && typeof v.resourceMap.clear === 'function') {
        v.resourceMap.clear()
      }
      // Detach all instances so we don't keep stale model refs around.
      clearInstances()
      // Re-prime team textures for the new mode.
      try { handlers.mdx.loadTeamTextures(viewer) } catch (e) {
        flog('[loadTeamTextures-reload]', e instanceof Error ? e.message : String(e))
      }
    },
    isReforged() { return currentReforged },
    setDoodadScale(multiplier: number) {
      if (!isFinite(multiplier) || multiplier <= 0) return
      doodadScaleMul = multiplier
    },
  }
}
