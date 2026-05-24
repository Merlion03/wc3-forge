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
}

export function createScene(canvas: HTMLCanvasElement): SceneAPI {
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

  // Handler order matters: BLP/DDS/TGA before MDX so the MDX loader can
  // route texture loads to the right decoder. Texture handlers don't take
  // pathSolver/options; MDX handler does (pathSolver + isReforged=false for
  // SD models — Reforged HD support is a follow-up).
  viewer.addHandler(handlers.blp)
  viewer.addHandler(handlers.dds)
  viewer.addHandler(handlers.tga)
  viewer.addHandler(handlers.mdx, pathSolver, /* reforged */ false)

  // Team colors aren't loaded automatically when using the lower-level API.
  // Pre-warm them so units have player tint from the first frame they render.
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
  // Tracks the current selection so we can paint a tint on selected unit
  // instances on every render-applicable state change (selection arrives via
  // setSelected; the tint persists across loadMap since we re-apply on every
  // placeUnit call when the cn is in this set).
  let selectedSet: Set<number> = new Set()
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
    inst.uniformScale((d.scale[0] || 1) * (info.model_scale || 1))
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
      // Sequential to keep diagnostics legible during bring-up; once stable
      // we'll batch via Promise.allSettled for parallel asset fetching.
      let uPlaced = 0, uSkipped = 0
      for (const u of units) {
        const inst = await placeUnit(u, uTypes)
        if (inst) {
          unitInstances.set(u.creation_number, inst)
          uPlaced++
        } else {
          uSkipped++
        }
      }
      let dPlaced = 0, dSkipped = 0
      let xmin = Infinity, xmax = -Infinity, ymin = Infinity, ymax = -Infinity
      for (const d of doodads) {
        if (d.position[0] < xmin) xmin = d.position[0]
        if (d.position[0] > xmax) xmax = d.position[0]
        if (d.position[1] < ymin) ymin = d.position[1]
        if (d.position[1] > ymax) ymax = d.position[1]
        const inst = await placeDoodad(d, dTypes)
        if (inst) {
          doodadInstances.push(inst)
          dPlaced++
        } else {
          dSkipped++
        }
      }
      flog(`[loadMap] units placed=${uPlaced} skipped=${uSkipped}, doodads placed=${dPlaced} skipped=${dSkipped} doodad-bbox=x[${xmin.toFixed(0)},${xmax.toFixed(0)}] y[${ymin.toFixed(0)},${ymax.toFixed(0)}]`)
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
  }
}
