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
import { patchMdxParser } from './mdx-parser-patch'
import {
  ListUnits, ListDoodads, GetUnitTypeIndex, GetDoodadTypeIndex, GetTerrain,
  GetPathingMap, MoveUnit,
} from '../wailsjs/go/main/App.js'
import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
import { buildTerrain, type TerrainMesh } from './terrain'
import { buildWater, type WaterMesh } from './water'
import { buildPathingOverlay, type PathingOverlay } from './pathing'
import { createCamera, type RTSCamera } from './camera'
import { computeCliffPlacements, renderCliffs, type CliffRendering } from './cliffs'
import {
  buildSlocRenderer, type SlocRenderer, type SlocMarker,
} from './sloc-markers'

// Apply the MDX parser patch BEFORE any viewer code touches the library — the
// lib's MdxModel constructor parses on construction, and the patch fixes a
// Reforged-1.32+ format mismatch that otherwise leaves loaded models with
// zero bones / zero geosets (so the model "loads" silently but renders
// nothing). See mdx-parser-patch.ts for the failure mode and patch details.
// Runs at module-load time (before createScene is even called), guarded by
// an idempotency flag so multiple imports don't double-wrap.
patchMdxParser()

const MV: any = (MV_ns as any).default ?? MV_ns
// The previous version of this file ran a corrective re-patch here at module
// load to fix a broken readAnimations override in mdx-parser-patch.ts (commit
// dc0aaa0). That correction has been consolidated INTO mdx-parser-patch.ts
// itself — see the top-of-file imports and `patchMdxParser()` body there for
// the correct AnimationMap source and the patched readAnimations. With one
// canonical patch site, double-patching isn't possible and the failure mode
// (silent fallback to "every animation tag is unknown") can't recur.

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
  name: string
  category: string
}

interface DoodadTypeInfo {
  file: string
  num_var: number
  fixed_rot: number
  model_scale: number
  // Per-type pitch / roll override from Doodads.slk maxPitch / maxRoll
  // (or the destructible / w3d-overlay equivalents). Stored in radians.
  // HiveWE convention:
  //   - negative value: fixed pitch/roll — apply as-is regardless of terrain
  //     (this is what custom maps use to author flame-on-sword effects whose
  //     emitters point along +X in model-local space and need a 90° tilt up).
  //   - positive value: clamp to terrain slope sampled around the doodad
  //     (terrain-follow path — currently unimplemented; we treat positive
  //     as zero, since the only stock rows we've seen use 0 here and the
  //     real consumers of this field in custom maps are all negative).
  //   - zero: no rotation (the default; everything points the way the MDX
  //     author left it).
  max_pitch: number
  max_roll: number
  name: string
  category: string
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

// Axis-angle quaternion around +Y (model-local pitch). Used to apply per-type
// maxPitch overrides for doodads whose source MDX puts particle emitters along
// +X in model-local space and needs a fixed tilt to point them upright (see
// Enfo FFB's D002 Fire Sword Effect — maxPitch=-4.71 ≈ +π/2 after sign-flip).
function quatY(angle: number): number[] {
  const h = angle * 0.5
  return [0, Math.sin(h), 0, Math.cos(h)]
}

// Axis-angle quaternion around +X (model-local roll). Used for the maxRoll
// override sibling of maxPitch; rarely populated on stock data but custom
// maps occasionally set it.
function quatX(angle: number): number[] {
  const h = angle * 0.5
  return [Math.sin(h), 0, 0, Math.cos(h)]
}

// Hamilton product a * b — quaternion composition. Convention matches gl-matrix
// (and HiveWE's glm): the result rotates by b FIRST, then by a. The lib's
// `rotateLocal` takes a single quaternion and right-multiplies it into the
// instance's local rotation, so to apply yaw → pitch → roll in that order
// (matching HiveWE's doodad.ixx update sequence) we hand it a product where
// yaw is the leftmost factor.
function quatMul(a: number[], b: number[]): number[] {
  const ax = a[0], ay = a[1], az = a[2], aw = a[3]
  const bx = b[0], by = b[1], bz = b[2], bw = b[3]
  return [
    aw * bx + ax * bw + ay * bz - az * by,
    aw * by - ax * bz + ay * bw + az * bx,
    aw * bz + ax * by - ay * bx + az * bw,
    aw * bw - ax * bx - ay * by - az * bz,
  ]
}

// Rarity-weighted sequence picker ported from
// mdx-m3-viewer/src/viewer/handlers/w3x/standsequence.ts (default export).
// The lib doesn't re-export it from its index, so we inline the algorithm.
// Parameterized on `type` since walk/death/etc. follow the same rules.
//
// Algorithm: normalize each sequence's name ("Stand - 1" → "stand"), sort
// the filtered list by ascending rarity, then walk: rarity 0 sequences form
// the "common" pool; rarity ≥ 1 sequences are each accepted with probability
// (rarity / 10). First acceptance wins; if none, uniform-random over the
// remaining (rarity-0) common pool. MDX `rarity` ranges 0..10 in practice.
function filterSequencesByType(
  type: string,
  sequences: ReadonlyArray<{ name: string; rarity: number }>,
): { index: number; rarity: number }[] {
  const filtered: { index: number; rarity: number }[] = []
  for (let i = 0; i < sequences.length; i++) {
    const s = sequences[i]
    if (!s) continue
    const normalized = String(s.name).split('-')[0].replace(/\d/g, '').trim().toLowerCase()
    if (normalized === type) filtered.push({ index: i, rarity: s.rarity })
  }
  return filtered
}

function selectSequenceIndex(
  type: string,
  sequences: ReadonlyArray<{ name: string; rarity: number }>,
): number {
  const filtered = filterSequencesByType(type, sequences)
  if (filtered.length === 0) return -1
  filtered.sort((a, b) => a.rarity - b.rarity)
  let i = 0
  for (; i < filtered.length; i++) {
    const rarity = filtered[i].rarity
    if (rarity === 0) break
    if (Math.random() * 10 > rarity) return filtered[i].index
  }
  const sequencesLeft = filtered.length - i
  if (sequencesLeft <= 0) return filtered[filtered.length - 1].index
  return filtered[i + Math.floor(Math.random() * sequencesLeft)].index
}

function rollSequence(instance: any, type: string): boolean {
  const seqs = instance?.model?.sequences
  if (!Array.isArray(seqs)) return false
  const idx = selectSequenceIndex(type, seqs)
  if (idx < 0) return false
  instance.setSequence(idx)
  return true
}

export type PickKind = 'unit' | 'doodad'
export interface PickHit { kind: PickKind; id: number }

// Imported from terrain-picker so consumers can reference the result shape
// without a separate import. Re-exported below the interface stub.
import { pickTerrainCell, type TerrainCellInfo } from './terrain-picker'
export type { TerrainCellInfo } from './terrain-picker'
export type TerrainPickCallback = (cell: TerrainCellInfo | null) => void
/**
 * How a pick result combines with the current selection.
 *   - 'set'    — replace selection with hits (plain click; empty hits clears)
 *   - 'add'    — union hits into the current selection (shift+click, rubber-band)
 *   - 'toggle' — XOR hits with the current selection (ctrl+click)
 */
export type SelectMode = 'set' | 'add' | 'toggle'
export type PickCallback = (hits: PickHit[], mode: SelectMode) => void

export interface SceneAPI {
  /**
   * Re-populate the scene from current Go-side state. Called on map-changed.
   * Frames the camera at the loaded map by default; pass `keepCamera: true`
   * to preserve the current camera position (used when reloading the same
   * map — e.g. after a graphics-mode toggle — so the user doesn't lose
   * their viewpoint).
   */
  loadMap(opts?: { keepCamera?: boolean }): Promise<void>
  /** Drop every instance we created. Models stay in the viewer cache. */
  clear(): void
  /** Stop the RAF loop and tear down listeners. */
  dispose(): void
  /**
   * Paint the highlight tint on selected instances. Pass split sets so the
   * scene can tint units and doodads independently — the underlying instance
   * maps are kind-separated and a single Set can't disambiguate creation
   * numbers that happen to collide across kinds.
   *
   * Sloc markers piggyback on the unit set (their type_id is "sloc" in the
   * unit DTO; they're rendered separately but selected as units).
   */
  setSelected(units: Set<number>, doodads: Set<number>): void
  /**
   * Register the picker callback. Fires for every selection-affecting gesture
   * in the viewport: plain click, shift/ctrl click, shift-drag rubber-band,
   * empty-click (hits=[], mode='set' to clear), Escape (same as empty-click).
   * Modifier-held clicks on empty space are intentionally swallowed — losing
   * a multi-step selection to a stray click in the void is more annoying than
   * losing the no-op gesture.
   */
  onPick(cb: PickCallback): void
  /**
   * Flip the MDX handler's reforged flag in-place. Drops cached MDX models +
   * team-color textures so the next loadMap() reloads them under the new mode.
   * Caller is responsible for triggering loadMap() after this.
   */
  setReforgedMode(reforged: boolean): void
  /** Current reforged flag — for UI display. */
  isReforged(): boolean
  /** Move the camera pivot to (x, y) in world XY. Z defaults to 0 (ground plane). */
  panTo(x: number, y: number, z?: number): void
  /**
   * Re-position an already-placed unit instance to (x, y, z) in game-space
   * coordinates (the same coords stored in war3map.doo / GetUnit's Position).
   * Applies the type's move_height Z offset internally to match what placeUnit
   * does on initial placement, so callers pass in raw game coords.
   *
   * No-op when the unit isn't in the unit-instance map — slocs (rendered by
   * slocRenderer, not unitInstances), doodads, and creation_numbers from a
   * stale-but-since-removed unit all silently return. The Properties-panel
   * single-unit position-edit gate already blocks the bad cases on the
   * caller side; this is defense-in-depth.
   *
   * mdx-m3-viewer's render loop is already RAFing — the new position renders
   * on the next tick without any explicit invalidation call here.
   */
  updateUnitPosition(creationNumber: number, x: number, y: number, z: number): void
  /**
   * Dev-only: pin a unit to a named animation (e.g. 'Walk', 'Death'). Empty
   * string or 'stand' returns the unit to the idle reroll loop. Unknown names
   * are ignored. Returns true if a sequence matching `animName` was found.
   * Routed in from App.SetUnitAnimation via Wails event.
   */
  setUnitAnimation(creationNumber: number, animName: string): boolean
  /** Toggle the pathing-map overlay. Defaults to off. */
  setPathingVisible(visible: boolean): void
  /** Current pathing-overlay visibility — for UI display. */
  isPathingVisible(): boolean
  /**
   * Toggle terrain-pick mode. When active, plain LMB clicks on the canvas
   * route to the terrain picker (onTerrainPick callback) instead of the
   * entity ray-pick. Drag-pan + rubber-band + camera controls still work.
   * Cursor changes to crosshair over the canvas when active.
   */
  setTerrainPickMode(active: boolean): void
  /** Current terrain-pick-mode flag — for UI display. */
  isTerrainPickMode(): boolean
  /**
   * Register the terrain-pick callback. Fires on canvas click when terrain
   * pick mode is active. `cell` is null when the click misses the map (e.g.
   * sky background or outside the map's bounds).
   */
  onTerrainPick(cb: TerrainPickCallback): void
  /**
   * Hide or show every doodad instance in a category. Pass "*" to affect
   * every doodad. Visibility is rendering-only — the underlying data is
   * unchanged, never persisted. Re-applied on every loadMap so hidden
   * categories stay hidden across map opens.
   *
   * Implementation: instances are detached/re-attached via setScene(null)
   * vs setScene(scene). The MDX library doesn't have a `show(b)` per-instance
   * affordance that's universally supported, but setScene controls render
   * participation cleanly across all model types.
   */
  setDoodadCategoryVisible(category: string, visible: boolean): void
  /**
   * Categories currently present in the loaded map's doodads (order matches
   * App.svelte's DOODAD_CAT_ORDER curated-first ordering). Empty when no map
   * is loaded.
   */
  getDoodadCategories(): string[]
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
  ;(window as any).__camera = camera // devtools + verification-script access

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
  let pathing: PathingOverlay | null = null
  // Pathing overlay visibility — toggled from the header pill. Default off
  // to match HiveWE; users opt in when debugging unit placement / arena
  // boundaries.
  let pathingVisible = false
  // Tracks the current selection so we can paint a tint on selected unit
  // instances on every render-applicable state change (selection arrives via
  // setSelected; the tint persists across loadMap since we re-apply on every
  // placeUnit call when the cn is in this set). Declared here (before the
  // render loop's first invocation) to satisfy TDZ — the loop closure
  // captures the binding name and would crash on first invocation otherwise.
  let selectedSet: Set<number> = new Set()
  // Doodad-side analogue of selectedSet. Creation_number is per-kind in WC3
  // (a unit and a doodad can share the same numeric ID), so selection state
  // for doodads lives in its own set and the renderer mirrors that split.
  let selectedDoodadSet: Set<number> = new Set()
  // Creation numbers whose animation is being driven manually via the dev
  // SetUnitAnimation hook. The per-frame stand re-roll skips these so a
  // user-pinned 'Walk' or 'Death' doesn't get clobbered on the next idle tick.
  // Doodads aren't keyed by creation_number on our side (we don't track them
  // by ID), so the dev hook is units-only — doodads always idle-reroll.
  let manualAnimCns: Set<number> = new Set()
  // Active instances we placed during the most recent loadMap. Keyed by
  // creation_number so selection events can highlight individually. Models
  // remain cached in viewer.resourceMap across loads — only instances are
  // dropped on re-open. Hoisted here (before the render loop's first
  // invocation) so the per-frame stand-reroll's iteration doesn't trip TDZ.
  // Units and doodads use separate maps because creation_number is per-kind
  // in WC3 — same reason the selected* sets above are split.
  const unitInstances = new Map<number, any>()
  const doodadInstances = new Map<number, any>()
  // Per-doodad-instance category tag (resolved at placement time from the
  // SLK type-index). Lets setDoodadCategoryVisible flip a whole category's
  // visibility in O(visible-instances) without re-walking the type index.
  // Same set of keys as `doodadInstances`; cleared together in clearInstances.
  const doodadCategoryByCn = new Map<number, string>()
  // Per-category visibility (true = visible / default). Categories absent
  // from this map are considered visible. Persists across loadMap so hiding
  // "Trees/Destructibles" in one map keeps trees hidden when a new map opens.
  const doodadVisibility = new Map<string, boolean>()
  // Ordered list of categories present in the current map. Recomputed at the
  // end of loadMap so the View menu can render an up-to-date checkbox list.
  // Curated order first (Trees/Destructibles, Structures, …), then remainder
  // alphabetized — matches App.svelte's DOODAD_CAT_ORDER constant.
  let doodadCategoriesPresent: string[] = []
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
  // Terrain-pick mode state. When true, plain LMB clicks fire the terrain-
  // pick callback instead of the entity ray-pick. The most-recent TerrainDTO
  // is cached on loadMap so the picker reads cell data without a round-trip
  // to Go.
  let terrainPickMode = false
  let terrainPickCallback: TerrainPickCallback | null = null
  let cachedTerrainDTO: any = null
  // Subset of doodad instances whose model has MORE THAN ONE 'stand' sequence
  // — only these benefit from per-frame sequenceEnded checks. Most doodads
  // (trees, rocks, props) have a single stand variation: rolling at placement
  // sets it, and reroll would just pick the same index. Tracking the subset
  // avoids iterating ~hundreds of static props every RAF tick.
  const doodadInstancesToReroll = new Set<any>()

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
  // Real-dt tracking for viewer.update(). Mdx-m3-viewer's contract is that
  // `viewer.update(dt)` receives MILLISECONDS elapsed since the previous
  // tick (see viewer.js line 425: `dt *= 0.001` to convert internally to
  // seconds, then each MdxModelInstance.updateAnimations multiplies back by
  // 1000 to advance KGTR/KGRT/KGSC keyframe times — which live in ms).
  //
  // The previous version passed a HARDCODED `1000 / 60` (= 16.67ms), which
  // is correct only at exactly 60 Hz. On a high-refresh display (120 Hz =
  // 8.33ms real, 144 Hz = 6.94ms real, 240 Hz = 4.17ms real) RAF fires
  // faster — but the hardcoded value still advanced the animation by 16.67ms
  // per frame, so all WC3 animations played at 2×, 2.4×, 4× speed
  // respectively. (User reported "a little faster than HiveWE" — which
  // matches the common 120Hz case.) HiveWE's `update(delta)` in
  // skeletal_model_instance.ixx uses the real frame delta from the editor's
  // QElapsedTimer; mirroring that here.
  //
  // Clamped to 250ms to keep a stalled tab from advancing animations by
  // multiple seconds on the first frame after wake (would skip past
  // non-looping idle re-rolls and look jarring). Mirrors HiveWE's
  // `std::clamp(delta, 0.0, 0.5)` on the seconds path.
  let lastFrameTs = performance.now()
  const MAX_DT_MS = 250
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
        const nowTs = performance.now()
        const dtMs = Math.min(MAX_DT_MS, Math.max(0, nowTs - lastFrameTs))
        lastFrameTs = nowTs
        viewer.update(dtMs)
        // Re-roll stand variation when a non-looping idle finishes. Mirrors
        // mdx-m3-viewer's Widget.update (handlers/w3x/widget.ts): MDX models
        // typically have N stand variations marked nonLooping=1, so most
        // tick once then leave `sequenceEnded=true` for us to pick a new one.
        // Units pinned to a manual sequence (via setUnitAnimation) are skipped
        // so the user's poke isn't overwritten. Doodads have no per-instance
        // ID on our side, so they always idle-reroll.
        for (const [cn, inst] of unitInstances) {
          if (manualAnimCns.has(cn)) continue
          if (inst.sequence === -1 || inst.sequenceEnded) rollSequence(inst, 'stand')
        }
        for (const inst of doodadInstancesToReroll) {
          if (inst.sequenceEnded) rollSequence(inst, 'stand')
        }
        gl.viewport(0, 0, canvas.width, canvas.height)
        gl.depthMask(true)
        gl.clearColor(bg[0], bg[1], bg[2], 1)
        gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
        if (terrain) terrain.draw(scene.camera.viewProjectionMatrix)
        // **CRITICAL**: terrain.draw calls gl.useProgram(terrainProg) directly,
        // bypassing mdx-m3-viewer's webgl.useShader() state cache. The cache
        // still thinks the LAST-USED MDX shader is bound. On the next frame,
        // when the lib does shader.use() → useShader(sameShader), it short-
        // circuits because `shader === currentShader` — but the actual GL
        // program is now the TERRAIN program. Every subsequent uniform call
        // then targets terrain's program with locations queried against the
        // MDX shader → GL_INVALID_OPERATION on every uniformXxx call → no
        // pixels painted. The lib's `webgl` is a state-caching wrapper;
        // we must invalidate its cache after any direct useProgram from our
        // own custom shaders (terrain, water, slocs, pathing). Resetting to
        // null forces the next `webgl.useShader(s)` to re-bind the program
        // and re-sync attribute indices. This is the fix for the "every MDX
        // is invisible on second frame onward" bug — see commit log.
        ;(viewer as any).webgl.currentShader = null
        viewer.render()
        // Sloc markers go AFTER viewer.render() and BEFORE water so they're
        // opaque-on-top-of-units but still get alpha-blended water in front.
        // Slocs are small and rare (typically 2-12 per map), so drawing N
        // pillars per frame is cheap enough that we don't bother with any
        // visibility culling.
        if (slocRenderer) slocRenderer.draw(scene.camera.viewProjectionMatrix, selectedSet)
        // Pathing overlay — alpha-blended, depth-test on, depth-write off
        // (see pathing.ts). Drawn after slocs so it shades the markers' base
        // too, before water so submerged pathing flags stay readable under
        // shallow water.
        if (pathing && pathingVisible) pathing.draw(scene.camera.viewProjectionMatrix)
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

  let pickCallback: PickCallback | null = null
  // Note: selectedSet, selectedDoodadSet, unitInstances, doodadInstances are
  // all declared earlier in this function (before the render loop runs) to
  // avoid a TDZ trap — the loop closure captures them on first invocation.
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
    // Stash the unit's type_id on the instance so later position-update calls
    // (SceneAPI.updateUnitPosition) can resolve the type's move_height without
    // needing a parallel cn → typeId map. Cheap, non-intrusive — the lib
    // never touches custom properties on its node objects.
    ;(inst as any).__wc3ForgeTypeId = unit.type_id
    if (info.red || info.green || info.blue) {
      inst.setVertexColor([info.red / 255, info.green / 255, info.blue / 255, 1])
    }
    inst.setScene(scene)
    rollSequence(inst, 'stand')
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
    let chosenPath: string | undefined
    for (const p of [variantPath, fallbackPath, otherExtPath]) {
      if (model && typeof model.addInstance === 'function') break
      try { model = await viewer.load(p, pathSolver); chosenPath = p } catch { /* swallow, try next */ }
    }
    if (!model || typeof model.addInstance !== 'function') return null

    // DIAGNOSTIC: print per-doodad geometry counts so we can correlate
    // "log says placed=N" with "user sees actual MDX on screen." If geosets
    // is 0 the parser patch isn't doing what it claimed; if geosets > 0 but
    // user sees nothing, the bug is downstream (texture load, material
    // linking, etc.). Limit to first 12 to keep the log readable.
    if ((doodadInstances.size + 1) <= 12) {
      const g = model.geosets?.length ?? -1
      const b = model.batches?.length ?? -1
      const bones = model.bones?.length ?? -1
      const seqs = model.sequences?.length ?? -1
      const mats = model.materials?.length ?? -1
      flog(`[doodad geom] cn=${d.creation_number} type=${d.type_id} path=${chosenPath} geosets=${g} batches=${b} bones=${bones} mats=${mats} seqs=${seqs}`)
    }

    const inst = model.addInstance()
    inst.move([d.position[0], d.position[1], d.position[2]])
    // Composed rotation: world-Z yaw (placement rotation) → model-local Y
    // pitch (per-type maxPitch override) → model-local X roll (per-type
    // maxRoll override). Mirrors HiveWE's Doodad::update:
    //
    //   glm::quat rotation = glm::angleAxis(angle, glm::vec3(0, 0, 1));
    //   rotation *= glm::angleAxis(-pitch, glm::vec3(0, 1, 0));
    //   rotation *= glm::angleAxis(roll,  glm::vec3(1, 0, 0));
    //
    // For maxPitch / maxRoll values < 0 (the "user-set fixed" case), HiveWE
    // takes the value as-is (already-negated by the user for the Y axis;
    // unsigned for the X axis). For values > 0 it samples surrounding
    // terrain heights and pitches/rolls to follow the slope, clamped by
    // ±value. We currently only implement the fixed-negative path — that's
    // the only one custom maps in the wild actually use to author flame /
    // statue / floating-doodad effects whose source MDX is laid out flat.
    // Zero (the default for most stock doodads) is a no-op.
    let rot = quatZ(d.rotation)
    const mp = info.max_pitch || 0
    if (mp < 0) {
      // HiveWE's "-pitch" inside the angleAxis: with mp already negative,
      // the effective rotation about Y is +|mp|. The Ember Sword's mp=-4.71
      // (≈ -3π/2) produces a +3π/2 about Y, equivalent to a -π/2 (-90°)
      // rotation that flips the +X-axis emitters to point along +Z.
      rot = quatMul(rot, quatY(-mp))
    }
    const mr = info.max_roll || 0
    if (mr < 0) {
      // HiveWE flips the sign in its `roll = -max_roll` line; mirror that.
      rot = quatMul(rot, quatX(-mr))
    }
    inst.rotateLocal(rot)
    inst.uniformScale((d.scale[0] || 1) * (info.model_scale || 1))
    // Record category so visibility toggles can find this instance by cat
    // without re-walking the type index. "" → "Uncategorized" so unknown rows
    // land in a single bucket that's still toggleable via the View menu.
    const cat = (info.category && info.category.length > 0) ? info.category : 'Uncategorized'
    doodadCategoryByCn.set(d.creation_number, cat)
    // Respect the user's existing category-visibility choice. If they hid
    // "Trees/Destructibles" before opening this map, don't briefly flash trees
    // on the way to hidden — attach the instance to the scene only when its
    // category is currently visible.
    const visible = doodadVisibility.get(cat) !== false
    if (visible) inst.setScene(scene)
    rollSequence(inst, 'stand')
    // Only doodads with multiple 'stand' variations need the per-frame reroll.
    // Single-stand and no-stand cases are no-ops — exclude them from the loop.
    const standCount = filterSequencesByType('stand', model.sequences || []).length
    if (standCount > 1) doodadInstancesToReroll.add(inst)
    if (selectedDoodadSet.has(d.creation_number)) {
      inst.setVertexColor(SELECT_TINT)
    }
    return inst
  }

  function clearInstances() {
    // Manual-anim pins are scoped to a loaded map — drop them so a fresh
    // map opens with all units back in the idle-reroll pool.
    manualAnimCns.clear()
    for (const inst of unitInstances.values()) {
      try { inst.detach() } catch { /* already detached */ }
    }
    unitInstances.clear()
    for (const inst of doodadInstances.values()) {
      try { inst.detach() } catch { /* already detached */ }
    }
    doodadInstances.clear()
    doodadInstancesToReroll.clear()
    // Drop the per-instance category tags. The doodadVisibility map (per-cat
    // user choices) intentionally persists across loadMap.
    doodadCategoryByCn.clear()
    doodadCategoriesPresent = []
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
    if (pathing) {
      pathing.dispose()
      pathing = null
    }
  }

  // --- Picking: ray vs instance bounds ---
  //
  // Each mdx-m3-viewer Bounds object is sphere-shaped (despite the type name)
  // — center + radius, in model-local space. We translate to world space
  // using the instance's worldLocation, then do ray-sphere intersection.
  // Walking units, doodads, and slocs in one pass lets us report the closest
  // hit across kinds; the picker callback receives a kind tag so the caller
  // can route through the kind-aware Go selection API.
  //
  // Click vs drag is detected by tracking mousedown/mouseup pixel distance;
  // MMB/RMB are camera drag gestures and we don't observe them here.
  // shift+LMB drag past the threshold becomes a rubber-band rectangle, also
  // handled in this module.
  const CLICK_PIXEL_THRESHOLD = 5

  // Project a world point through the current view-projection matrix into
  // canvas-pixel space (DOM-Y, origin top-left). Returns null if the point
  // is behind the camera (w <= 0). Used by rubber-band hit-testing.
  function worldToCanvasPx(wx: number, wy: number, wz: number): { x: number; y: number } | null {
    const m = scene.camera.viewProjectionMatrix as Float32Array
    const x = m[0]*wx + m[4]*wy + m[8]*wz + m[12]
    const y = m[1]*wx + m[5]*wy + m[9]*wz + m[13]
    const w = m[3]*wx + m[7]*wy + m[11]*wz + m[15]
    if (w <= 0) return null
    const nx = x / w, ny = y / w
    const sx = (nx * 0.5 + 0.5) * canvas.clientWidth
    const syBottom = (ny * 0.5 + 0.5) * canvas.clientHeight
    return { x: sx, y: canvas.clientHeight - syBottom }
  }

  // World-space pick-bounds (sphere center + radius) for one unit/doodad
  // instance. Centralizes the "bounds.r is sphere, scale up by largest axis"
  // dance so both ray-pick and rubber-band agree on what's hittable.
  function instanceWorldBounds(inst: any): { cx: number; cy: number; cz: number; r: number } | null {
    const b = inst.getBounds?.()
    if (!b || b.r <= 0) return null
    const sx = inst.worldScale?.[0] ?? 1
    const sy = inst.worldScale?.[1] ?? 1
    const sz = inst.worldScale?.[2] ?? 1
    const biggest = Math.max(sx, sy, sz)
    return {
      cx: inst.worldLocation[0] + b.x,
      cy: inst.worldLocation[1] + b.y,
      cz: inst.worldLocation[2] + b.z,
      r: b.r * biggest,
    }
  }

  // Project a canvas-pixel (px, py) into world XY on the z=0 ground plane.
  // Returns null in the degenerate case where the camera is looking nearly
  // horizontal (dir.z ≈ 0 means the ray never hits the ground in front of
  // it) — the RTS camera's enforced pitch (PITCH_MIN ~10°) keeps us out of
  // that regime in practice, but the guard prevents NaN propagation if the
  // camera ever does end up there.
  function groundPlaneXY(px: number, py: number): [number, number] | null {
    const out = new Float32Array(6)
    const vp = (scene as any).viewport as Float32Array
    // mdx-m3-viewer screen Y origin is at canvas BOTTOM; DOM event Y is from
    // the top — flip before handing to screenToWorldRay (matches rayPick).
    const screen = new Float32Array([px, canvas.clientHeight - py])
    scene.camera.screenToWorldRay(out, screen, vp)
    const nx = out[0], ny = out[1], nz = out[2]
    const dx = out[3] - nx, dy = out[4] - ny, dz = out[5] - nz
    if (Math.abs(dz) < 1e-6) return null
    // Plane equation: near.z + t * dz = 0  →  t = -near.z / dz.
    const t = -nz / dz
    if (!isFinite(t)) return null
    return [nx + dx * t, ny + dy * t]
  }

  function rayPick(px: number, py: number): PickHit | null {
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

    let bestHit: PickHit | null = null
    let bestT = Infinity
    const considerSphere = (cx: number, cy: number, cz: number, r: number, hit: PickHit) => {
      // Ray-sphere intersection. Standard quadratic:
      //   |o + t*d - c|² = r²  →  t² - 2t·(d·(c-o)) + |c-o|²-r² = 0
      const lx = cx - ox, ly = cy - oy, lz = cz - oz
      const tca = lx * rx + ly * ry + lz * rz
      if (tca < 0) return // behind the camera
      const d2 = lx * lx + ly * ly + lz * lz - tca * tca
      if (d2 > r * r) return // ray misses sphere
      const thc = Math.sqrt(r * r - d2)
      const t = tca - thc // nearest intersection in front of camera
      if (t < bestT) {
        bestT = t
        bestHit = hit
      }
    }

    for (const [cn, inst] of unitInstances) {
      const wb = instanceWorldBounds(inst)
      if (wb) considerSphere(wb.cx, wb.cy, wb.cz, wb.r, { kind: 'unit', id: cn })
    }
    for (const [cn, inst] of doodadInstances) {
      const wb = instanceWorldBounds(inst)
      if (wb) considerSphere(wb.cx, wb.cy, wb.cz, wb.r, { kind: 'doodad', id: cn })
    }

    // Sloc markers — ray vs AABB (slab method). Slocs have no MDX so they're
    // not in unitInstances; rays still need to hit them for selection. The
    // Go-side selection routes them as kind="unit" because they're entries
    // in war3mapUnits.doo.
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
        bestHit = { kind: 'unit', id: s.creationNumber }
      }
    }
    return bestHit
  }

  // Rubber-band: collect every instance whose world-space bound-center
  // projects to a point inside the rectangle. Using the center (not full
  // sphere extent) matches Blender/Photoshop convention — the gesture asks
  // "what's in here?", not "what touches the edge?".
  function rubberBandPick(rect: { x: number; y: number; w: number; h: number }): PickHit[] {
    const hits: PickHit[] = []
    const inside = (p: { x: number; y: number } | null) => {
      if (!p) return false
      return p.x >= rect.x && p.x <= rect.x + rect.w
          && p.y >= rect.y && p.y <= rect.y + rect.h
    }
    for (const [cn, inst] of unitInstances) {
      const wb = instanceWorldBounds(inst)
      if (!wb) continue
      if (inside(worldToCanvasPx(wb.cx, wb.cy, wb.cz))) {
        hits.push({ kind: 'unit', id: cn })
      }
    }
    for (const [cn, inst] of doodadInstances) {
      const wb = instanceWorldBounds(inst)
      if (!wb) continue
      if (inside(worldToCanvasPx(wb.cx, wb.cy, wb.cz))) {
        hits.push({ kind: 'doodad', id: cn })
      }
    }
    const slocInfos = slocRenderer?.pickInfos() ?? []
    for (const s of slocInfos) {
      if (inside(worldToCanvasPx(s.center[0], s.center[1], s.center[2]))) {
        hits.push({ kind: 'unit', id: s.creationNumber })
      }
    }
    return hits
  }

  // --- Mouse + keyboard gesture handling ---
  //
  // State machine for LMB:
  //   mousedown LMB     → record downAt + modifiers
  //   mousemove past 5px while shift was held at down → start rubber-band
  //   mousemove during rubber-band → resize the overlay rectangle
  //   mouseup LMB       → either commit rubber-band (mode=add)
  //                     OR treat as click and ray-pick (mode from modifiers)
  // Modifier-held click on empty space is a no-op (preserves selection);
  // plain click on empty space clears via mode='set' with hits=[].
  //
  // RMB/MMB drag belong to the camera and we don't observe them here.

  interface DownState {
    x: number; y: number          // canvas-relative px
    clientX: number; clientY: number // viewport px (for mousemove distance)
    shift: boolean
    ctrl: boolean
    // Did the user start a rubber-band gesture? Lazy-set on the first
    // mousemove that crosses the click-vs-drag threshold while shift is held.
    rubberBanding: boolean
    // Drag-to-move armed at mousedown: the user clicked on a unit that was
    // already in the selection without modifier keys. Stays null when the
    // hit was empty space, a non-selected unit, or any modifier was held —
    // those paths defer to the existing rubber-band / click-select code.
    // `active` flips true once motion crosses CLICK_PIXEL_THRESHOLD; before
    // that, mouseup still routes through the regular click handler (so a
    // click on a selected unit re-selects it cleanly via mode='set').
    dragMove: {
      cns: number[]                                   // unit creation_numbers
      anchorXY: [number, number]                      // ground XY at mousedown
      original: Map<number, {                         // per-cn original state
        wl: [number, number, number]                  // worldLocation (post-move_height)
        gameZ: number                                 // game-space Z (pre-move_height)
      }>
      active: boolean
    } | null
  }
  let downAt: DownState | null = null

  // The rubber-band rectangle is rendered as a positioned DOM div. WebGL
  // would be lower-overhead but a one-element DOM overlay is dramatically
  // simpler and doesn't conflict with any of the render-state assumptions
  // baked into our terrain/water/sloc passes.
  const overlay = document.createElement('div')
  overlay.style.cssText = [
    'position:absolute',
    'pointer-events:none',
    'border:1px solid rgba(120,180,255,0.9)',
    'background:rgba(60,120,220,0.18)',
    'display:none',
    'z-index:10',
  ].join(';')
  // The viewport container is position:relative so absolute children anchor
  // to its box. Fall back to document.body when no parent is available (unit
  // test contexts) so the listener wiring doesn't crash.
  const overlayHost = canvas.parentElement ?? document.body
  overlayHost.appendChild(overlay)

  function modeFromModifiers(shift: boolean, ctrl: boolean): SelectMode {
    if (ctrl) return 'toggle'
    if (shift) return 'add'
    return 'set'
  }

  function onCanvasMouseDown(e: MouseEvent) {
    if (e.button !== 0) return
    const r = canvas.getBoundingClientRect()
    const px = e.clientX - r.left
    const py = e.clientY - r.top
    const shift = e.shiftKey
    const ctrl = e.ctrlKey || e.metaKey
    // Arm a drag-to-move only on a plain LMB click that lands on a unit
    // that's ALREADY in the current selection. Modifier-held clicks defer to
    // toggle/add semantics (shift+click a selected unit = deselect via
    // toggle; shift-drag empty = rubber-band) so the drag-move path stays
    // narrowly scoped to "user clicked something they had selected."
    //
    // Doodads don't get a drag-move treatment — the spec focuses on units,
    // and doodad position editing has its own deferred work (different
    // preservation fields in war3map.doo encoding).
    let dragMove: DownState['dragMove'] = null
    if (!shift && !ctrl) {
      const hit = rayPick(px, py)
      if (hit && hit.kind === 'unit' && selectedSet.has(hit.id)) {
        const anchor = groundPlaneXY(px, py)
        if (anchor) {
          const cns = [...selectedSet]
          const original = new Map<number, { wl: [number, number, number]; gameZ: number }>()
          for (const cn of cns) {
            const inst = unitInstances.get(cn)
            if (!inst) continue
            const wl = inst.worldLocation
            const typeId = (inst as any).__wc3ForgeTypeId as string | undefined
            const info = typeId ? unitTypeIndexCache?.[typeId] : undefined
            const moveHeight = info?.move_height ?? 0
            original.set(cn, {
              wl: [wl[0], wl[1], wl[2]],
              gameZ: wl[2] - moveHeight,
            })
          }
          if (original.size > 0) {
            dragMove = { cns, anchorXY: anchor, original, active: false }
          }
        }
      }
    }
    downAt = {
      x: px,
      y: py,
      clientX: e.clientX,
      clientY: e.clientY,
      shift,
      ctrl,
      rubberBanding: false,
      dragMove,
    }
  }

  function updateOverlayRect(rect: { x: number; y: number; w: number; h: number }) {
    overlay.style.left = `${rect.x}px`
    overlay.style.top = `${rect.y}px`
    overlay.style.width = `${rect.w}px`
    overlay.style.height = `${rect.h}px`
    overlay.style.display = 'block'
  }

  function currentRubberRect(curClientX: number, curClientY: number): { x: number; y: number; w: number; h: number } | null {
    if (!downAt) return null
    const r = canvas.getBoundingClientRect()
    const cx = curClientX - r.left
    const cy = curClientY - r.top
    const x = Math.min(downAt.x, cx)
    const y = Math.min(downAt.y, cy)
    const w = Math.abs(cx - downAt.x)
    const h = Math.abs(cy - downAt.y)
    return { x, y, w, h }
  }

  // Document-level so we keep tracking the drag even if the cursor leaves
  // the canvas mid-gesture (rubber-band should survive a wobble over the
  // app chrome — matches what every other editor does).
  function onDocMouseMove(e: MouseEvent) {
    if (!downAt) return
    // Drag-to-move on a selected unit. Threshold-gated like rubber-band:
    // sub-threshold motion stays as a click (so the mouseup path re-selects),
    // past-threshold motion locks into a drag-move that ignores any further
    // selection events until release. We mutate setLocation on every move
    // tick — the lib's render loop picks up the new worldLocation next RAF.
    if (downAt.dragMove) {
      const dragMove = downAt.dragMove
      if (!dragMove.active) {
        const dist = Math.hypot(e.clientX - downAt.clientX, e.clientY - downAt.clientY)
        if (dist <= CLICK_PIXEL_THRESHOLD) return
        dragMove.active = true
      }
      const r = canvas.getBoundingClientRect()
      const curXY = groundPlaneXY(e.clientX - r.left, e.clientY - r.top)
      if (!curXY) return
      const dx = curXY[0] - dragMove.anchorXY[0]
      const dy = curXY[1] - dragMove.anchorXY[1]
      for (const cn of dragMove.cns) {
        const orig = dragMove.original.get(cn)
        if (!orig) continue
        const inst = unitInstances.get(cn)
        if (!inst) continue
        ;(inst as any).setLocation([orig.wl[0] + dx, orig.wl[1] + dy, orig.wl[2]])
      }
      return
    }
    if (!downAt.rubberBanding) {
      // Promote to rubber-band only if shift was held at mousedown AND the
      // pointer has actually moved enough to look like a drag, not a click.
      // (Without the shift gate, a plain LMB drag — which does nothing
      // useful today — would steal selection on every accidental tiny drag.)
      if (!downAt.shift) return
      const dist = Math.hypot(e.clientX - downAt.clientX, e.clientY - downAt.clientY)
      if (dist <= CLICK_PIXEL_THRESHOLD) return
      downAt.rubberBanding = true
    }
    const rect = currentRubberRect(e.clientX, e.clientY)
    if (rect) updateOverlayRect(rect)
  }

  function onDocMouseUp(e: MouseEvent) {
    const d = downAt
    downAt = null
    if (!d || e.button !== 0) return

    if (d.dragMove?.active) {
      // Commit each unit's final position via MoveUnit. Sequential (not
      // Promise.all) so the resulting entity-changed events arrive in a
      // predictable order — same reason the Properties path uses a single
      // await + commit. One failure logs via flog and we continue: a partial
      // batch is more useful than blowing the whole drag away.
      //
      // The 3D model has already been moved live during the drag via
      // setLocation, so MoveUnit's role here is to persist into Go memory +
      // mark the session dirty. The OnEntityChanged → setLocation callback
      // is idempotent so the re-paint on event delivery is a no-op.
      const dragMove = d.dragMove
      const r = canvas.getBoundingClientRect()
      const finalXY = groundPlaneXY(e.clientX - r.left, e.clientY - r.top)
      // Recompute the final position from the release coords (don't trust
      // the per-frame setLocation calls' last value — a missed RAF tick
      // could leave the visible position slightly behind the cursor).
      const dx = finalXY ? finalXY[0] - dragMove.anchorXY[0] : 0
      const dy = finalXY ? finalXY[1] - dragMove.anchorXY[1] : 0
      ;(async () => {
        for (const cn of dragMove.cns) {
          const orig = dragMove.original.get(cn)
          if (!orig) continue
          const finalX = orig.wl[0] + dx
          const finalY = orig.wl[1] + dy
          try {
            await MoveUnit(cn, finalX, finalY, orig.gameZ)
          } catch (err) {
            flog(`[drag-move] MoveUnit cn=${cn} failed: ${err instanceof Error ? err.message : String(err)}`)
          }
        }
      })()
      return
    }

    if (d.rubberBanding) {
      // Commit the rubber-band: collect everything inside the final rect
      // and union it into the current selection (mode='add'). Empty boxes
      // are a no-op (don't clear selection when the user just barely
      // dragged then released with nothing inside).
      overlay.style.display = 'none'
      const rect = currentRubberRect(e.clientX, e.clientY)
      if (!rect || rect.w < 2 || rect.h < 2) return
      const hits = rubberBandPick(rect)
      if (hits.length > 0 && pickCallback) pickCallback(hits, 'add')
      return
    }

    // Click vs drag. A non-rubber-banding mouseup whose mousedown was on
    // the canvas counts as a click iff it ended close to where it started.
    const dist = Math.hypot(e.clientX - d.clientX, e.clientY - d.clientY)
    if (dist > CLICK_PIXEL_THRESHOLD) return

    // Terrain-pick mode takes over plain clicks. Modifier-held clicks are
    // intentionally still entity-pick / no-op so users can briefly pop out
    // of terrain-pick to add/remove an entity from selection without leaving
    // the mode (matches the convention rubber-band mode also follows). The
    // terrain-pick callback receives null when the click misses the map
    // (sky, outside bounds) — caller decides whether to clear UI or ignore.
    if (terrainPickMode && !d.shift && !d.ctrl) {
      if (terrainPickCallback && cachedTerrainDTO) {
        const cell = pickTerrainCell(d.x, d.y, canvas, scene, cachedTerrainDTO)
        terrainPickCallback(cell)
      }
      return
    }

    const hit = rayPick(d.x, d.y)
    if (!pickCallback) return

    if (hit) {
      pickCallback([hit], modeFromModifiers(d.shift, d.ctrl))
      return
    }
    // Empty click. Modifier-held empty clicks are no-ops so a stray click
    // in the void doesn't wipe an in-progress multi-select. Plain empty
    // click clears.
    if (!d.shift && !d.ctrl) pickCallback([], 'set')
  }

  function onWindowKeyDown(e: KeyboardEvent) {
    if (e.key !== 'Escape') return
    // Don't steal Escape from text inputs / contenteditable / form controls.
    // Future inline-edit fields, search boxes, or modals need to handle their
    // own Escape (close themselves) without losing the viewport selection too.
    const a = document.activeElement as HTMLElement | null
    if (a) {
      const tag = a.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
      if (a.isContentEditable) return
    }
    // Mid-drag Escape = cancel the move and snap each unit back to its
    // pre-drag position. The mouseup that follows will see `downAt` is
    // null and won't commit MoveUnit. Selection is NOT cleared in this
    // path — Escape during a drag cancels the drag, not the selection.
    if (downAt?.dragMove?.active) {
      const dragMove = downAt.dragMove
      for (const cn of dragMove.cns) {
        const orig = dragMove.original.get(cn)
        if (!orig) continue
        const inst = unitInstances.get(cn)
        if (!inst) continue
        ;(inst as any).setLocation(orig.wl)
      }
      // Drop the gesture state so the impending mouseup is a no-op.
      // Rubber-band overlay (if visible) is also hidden in case the user
      // managed to be in both states somehow.
      downAt = null
      overlay.style.display = 'none'
      return
    }
    if (!pickCallback) return
    pickCallback([], 'set')
  }

  canvas.addEventListener('mousedown', onCanvasMouseDown)
  // Listen on document for move/up so the rubber-band gesture survives the
  // cursor leaving the canvas. Mirrors the camera controller's window-level
  // mouseup listener for the same reason.
  document.addEventListener('mousemove', onDocMouseMove)
  document.addEventListener('mouseup', onDocMouseUp)
  window.addEventListener('keydown', onWindowKeyDown)

  // Cursor feedback for hover. Picks per mousemove but throttled to ~30 Hz —
  // a full rayPick on every native mousemove event (which can fire at 1000Hz
  // on high-poll mice) walks every unit + doodad + sloc and would chug on
  // 500+-entity maps. ~33ms gating keeps the cursor responsive without
  // burning CPU. We also skip when a drag-related gesture is in progress
  // (downAt set) — once the user is committed to a drag the cursor doesn't
  // need to reflect what's under it.
  let lastHoverTs = 0
  const HOVER_THROTTLE_MS = 33
  function onCanvasHoverMove(e: MouseEvent) {
    if (downAt) return // suppress hover-pick during any active gesture
    // e.buttons bit 0=LMB, bit 1=RMB, bit 2=MMB. Any held button means an
    // ongoing drag (LMB without downAt is weird but safe to ignore; RMB/MMB
    // = camera pan, no point updating cursor under a pan).
    if (e.buttons !== 0) return
    // Terrain-pick mode owns the cursor — keep crosshair regardless of what's
    // under the pointer. The picker itself doesn't care whether anything's
    // there; it picks a terrain cell or null. Without this short-circuit the
    // hover-pick below would flip the cursor to "pointer" / "move" any time
    // the user passed over an entity, which is misleading.
    if (terrainPickMode) {
      if (canvas.style.cursor !== 'crosshair') canvas.style.cursor = 'crosshair'
      return
    }
    const now = performance.now()
    if (now - lastHoverTs < HOVER_THROTTLE_MS) return
    lastHoverTs = now
    const r = canvas.getBoundingClientRect()
    const px = e.clientX - r.left
    const py = e.clientY - r.top
    const hit = rayPick(px, py)
    let cursor = 'default'
    if (hit) {
      if (hit.kind === 'unit' && selectedSet.has(hit.id)) cursor = 'move'
      else if (hit.kind === 'doodad' && selectedDoodadSet.has(hit.id)) cursor = 'move'
      else cursor = 'pointer'
    }
    if (canvas.style.cursor !== cursor) canvas.style.cursor = cursor
  }
  function onCanvasHoverLeave() {
    if (canvas.style.cursor !== '') canvas.style.cursor = ''
  }
  canvas.addEventListener('mousemove', onCanvasHoverMove)
  canvas.addEventListener('mouseleave', onCanvasHoverLeave)

  // Internal helper for re-positioning a placed unit instance to (x, y, z) in
  // game-space. Used by both the public SceneAPI.updateUnitPosition (kept for
  // direct callers) AND the entity-changed event subscriber below.
  //
  // Mirrors placeUnit's Z-offset: the type's move_height was applied at
  // placement and must be re-applied here so the instance lands at the same
  // visual offset above the ground as a freshly-placed unit at the same
  // game coords.
  function updateUnitPositionImpl(cn: number, x: number, y: number, z: number): void {
    const inst = unitInstances.get(cn)
    // Defense-in-depth: silently skip if the cn isn't a placed unit. Slocs
    // (rendered via slocRenderer, not unitInstances), doodads, and stale
    // creation_numbers from a race with map-changed all silently return.
    if (!inst) return
    const typeId = (inst as any).__wc3ForgeTypeId as string | undefined
    const info = typeId ? unitTypeIndexCache?.[typeId] : undefined
    const moveHeight = info?.move_height ?? 0
    // setLocation (not move) — move() is ADDITIVE (vec3.add into
    // localLocation). It only worked as "set" in placeUnit because the
    // instance was fresh with localLocation=[0,0,0]. Here localLocation is
    // already non-zero, so we need an absolute set.
    ;(inst as any).setLocation([x, y, z + moveHeight])
  }

  // Internal helper for re-positioning a placed doodad instance. Parallel
  // to updateUnitPositionImpl, with one structural difference: doodads have
  // NO move_height offset (placeDoodad uses d.position verbatim, no SLK Z
  // adjustment unlike units), so the JS-side Z passes through unchanged.
  // setLocation (absolute), not move (additive) — same rationale as units.
  function updateDoodadPositionImpl(cn: number, x: number, y: number, z: number): void {
    const inst = doodadInstances.get(cn)
    if (!inst) return
    ;(inst as any).setLocation([x, y, z])
  }

  // Subscribe to Go-side entity-change events. Any mutation that fires
  // OnEntityChanged (MoveUnit + MoveDoodad today; future SetRotation/etc.)
  // flows through here, regardless of whether the mutator was the JS
  // Properties panel, the MCP bridge, or any future code path. This is what
  // keeps the 3D scene in sync with bridge-driven edits without polling.
  //
  // Field-aware: ignores changes whose Field isn't one we handle (no-op for
  // future Field values that don't map to scene state). Kind-branched so
  // unit + doodad position updates take their respective code paths.
  const ENTITY_EVENT = 'wc3-forge:entity-changed'
  EventsOn(ENTITY_EVENT, (payload: { kind: string; id: number; field: string; position: number[] }) => {
    if (!payload) return
    if (payload.field !== 'position') return
    const p = payload.position
    if (!p || p.length < 3) return
    if (payload.kind === 'unit') {
      updateUnitPositionImpl(payload.id, p[0], p[1], p[2])
    } else if (payload.kind === 'doodad') {
      updateDoodadPositionImpl(payload.id, p[0], p[1], p[2])
    }
  })

  return {
    async loadMap(opts?: { keepCamera?: boolean }) {
      const keepCamera = !!opts?.keepCamera
      clearInstances()
      // Terrain first so it's visible even while units/doodads stream in.
      try {
        const t = await GetTerrain()
        // Cache for the terrain-pick path — picker reads cell data straight
        // from this without a Go round-trip per click.
        cachedTerrainDTO = t
        const gl = (viewer as any).gl as WebGLRenderingContext
        terrain = await buildTerrain(gl, viewer as any, pathSolver, t as unknown as any)
        if (terrain && !keepCamera) {
          // Frame the map. width/height in w3e are vertex counts; tile size
          // is 128, so playable span is roughly (width-1)*128 in each axis.
          // Skip when keepCamera is true — that's the case for in-place
          // reloads like a graphics-mode toggle, where re-framing would
          // throw the user back to the default view they just panned away
          // from.
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
        // Pathing overlay. Built unconditionally so the toggle is
        // instant — the per-frame cost when invisible is one branch.
        // Empty pathing DTOs (map without war3map.wpm) return null and
        // the toggle becomes a no-op for that map.
        try {
          const p = await GetPathingMap()
          if (p && p.width > 0 && p.height > 0) {
            pathing = buildPathingOverlay(gl, p as unknown as any, t as unknown as any)
          }
        } catch (e) {
          flog('[pathing load]', e instanceof Error ? e.message : String(e))
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
          doodadInstances.set(d.creation_number, inst)
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
      flog(`[loadMap] units placed=${uPlaced} skipped=${uSkipped}, doodads placed=${dPlaced} skipped=${dSkipped} doodad-bbox=x[${xmin.toFixed(0)},${xmax.toFixed(0)}] y[${ymin.toFixed(0)},${ymax.toFixed(0)}] slocs=${slocMarkers.length}`)
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
      // Build the ordered present-categories list for the View menu. Curated
      // first (mirrors App.svelte's DOODAD_CAT_ORDER), then remaining cats
      // alphabetized, "Uncategorized" pinned last.
      const presentSet = new Set<string>()
      for (const c of doodadCategoryByCn.values()) presentSet.add(c)
      const ordered: string[] = []
      for (const c of DOODAD_CAT_ORDER) {
        if (presentSet.has(c)) { ordered.push(c); presentSet.delete(c) }
      }
      const rest = [...presentSet].sort((a, b) => {
        if (a === 'Uncategorized') return 1
        if (b === 'Uncategorized') return -1
        return a.localeCompare(b)
      })
      ordered.push(...rest)
      doodadCategoriesPresent = ordered
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
      canvas.removeEventListener('mousemove', onCanvasHoverMove)
      canvas.removeEventListener('mouseleave', onCanvasHoverLeave)
      document.removeEventListener('mousemove', onDocMouseMove)
      document.removeEventListener('mouseup', onDocMouseUp)
      window.removeEventListener('keydown', onWindowKeyDown)
      // Drop the entity-changed subscription. App.svelte also calls
      // EventsOff for this name on its own unmount; the Wails runtime is
      // tolerant of duplicate offs (no-op when no listener is attached).
      EventsOff(ENTITY_EVENT)
      try { overlay.remove() } catch { /* parent already gone */ }
      if (slocRenderer) {
        slocRenderer.dispose()
        slocRenderer = null
      }
    },
    setSelected(units: Set<number>, doodads: Set<number>) {
      // Units: clear tint on no-longer-selected, paint tint on newly-selected.
      // Doodads: same dance against the doodad set + map.
      for (const cn of selectedSet) {
        if (units.has(cn)) continue
        const inst = unitInstances.get(cn)
        if (inst) inst.setVertexColor([1, 1, 1, 1])
      }
      for (const cn of units) {
        if (selectedSet.has(cn)) continue
        const inst = unitInstances.get(cn)
        if (inst) inst.setVertexColor(SELECT_TINT)
      }
      selectedSet = new Set(units)

      for (const cn of selectedDoodadSet) {
        if (doodads.has(cn)) continue
        const inst = doodadInstances.get(cn)
        if (inst) inst.setVertexColor([1, 1, 1, 1])
      }
      for (const cn of doodads) {
        if (selectedDoodadSet.has(cn)) continue
        const inst = doodadInstances.get(cn)
        if (inst) inst.setVertexColor(SELECT_TINT)
      }
      selectedDoodadSet = new Set(doodads)
    },
    onPick(cb: PickCallback) {
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
    panTo(x: number, y: number, z: number = 0) {
      camera.setPivot(x, y, z)
    },
    updateUnitPosition(cn: number, x: number, y: number, z: number) {
      // Delegates to the shared impl so direct callers and the
      // entity-changed event subscriber run identical code.
      updateUnitPositionImpl(cn, x, y, z)
    },
    setUnitAnimation(cn: number, animName: string): boolean {
      const inst = unitInstances.get(cn)
      if (!inst) {
        flog(`[setUnitAnimation] no unit for cn=${cn}`)
        return false
      }
      const normalized = animName.trim().toLowerCase()
      // Empty / 'stand' → release manual control and reroll an idle.
      if (normalized === '' || normalized === 'stand') {
        manualAnimCns.delete(cn)
        const ok = rollSequence(inst, 'stand')
        flog(`[setUnitAnimation] cn=${cn} -> stand (release manual) ok=${ok}`)
        return ok
      }
      const ok = rollSequence(inst, normalized)
      if (ok) {
        manualAnimCns.add(cn)
        flog(`[setUnitAnimation] cn=${cn} -> ${normalized} ok=true`)
      } else {
        flog(`[setUnitAnimation] cn=${cn} -> ${normalized} ok=false (no matching sequence)`)
      }
      return ok
    },
    setPathingVisible(visible: boolean) {
      pathingVisible = visible
    },
    isPathingVisible() {
      return pathingVisible
    },
    setTerrainPickMode(active: boolean) {
      if (active === terrainPickMode) return
      terrainPickMode = active
      // Visual feedback: crosshair cursor over the canvas. Clear any active
      // hover-pick cursor when leaving the mode so we don't leave the user
      // staring at "move" / "pointer" cursors that no longer make sense.
      if (active) canvas.style.cursor = 'crosshair'
      else canvas.style.cursor = ''
    },
    isTerrainPickMode() { return terrainPickMode },
    onTerrainPick(cb: TerrainPickCallback) {
      terrainPickCallback = cb
    },
    setDoodadCategoryVisible(category: string, visible: boolean) {
      // "*" affects every category. Walk the per-instance category map and
      // attach/detach by setScene; setScene(null) detaches without disposing,
      // so toggling back on is cheap (no model re-load).
      if (category === '*') {
        // Mirror across all categories so future loadMap respects the choice.
        for (const c of doodadCategoriesPresent) doodadVisibility.set(c, visible)
        for (const inst of doodadInstances.values()) {
          if (visible) inst.setScene(scene)
          else inst.setScene(null)
        }
        return
      }
      doodadVisibility.set(category, visible)
      for (const [cn, inst] of doodadInstances) {
        const c = doodadCategoryByCn.get(cn) ?? 'Uncategorized'
        if (c !== category) continue
        if (visible) inst.setScene(scene)
        else inst.setScene(null)
      }
    },
    getDoodadCategories() { return [...doodadCategoriesPresent] },
  }
}
