// Sloc (start-location) marker renderer.
//
// In Warcraft III maps, a "sloc" is a unit Entity whose type_id is "sloc" —
// it's not a real unit, just a marker that says "player N starts here".
// Stock UnitData.slk has no row for "sloc" at all (verified via CASC probe),
// so the regular placeUnit path can't load an MDX for them.
//
// For an editor view we want them visible AND iconic. The WC3 community
// universally reads `Buildings/Other/CircleOfPower/CircleOfPower.mdx` —
// the glowing rune pad — as "a player belongs on this spot," so that's
// what we render. The model exists in stock CASC, has a team-color region
// on its runes that we tint to the owning player slot, and animates a
// gentle pulse via its "stand" sequence.
//
// Resilience: if the MDX fails to load (folder-only map opened with no
// WC3 install present, viewer not yet ready, parser error), we fall back
// per-marker to a primitive colored pillar drawn directly via WebGL. The
// pillar code below is the original implementation kept in-place as the
// fallback so slocs always render *something* the user can see and click.
//
// Picking integration: pickInfos() returns an axis-aligned bbox for each
// marker (whether MDX-rendered or pillar-rendered) so scene-instances.ts's
// ray-vs-AABB selection pipeline works uniformly across both paths.

import { flog } from './debuglog'

// WC3 player slot → team color, in RGB 0..1. Slots 0..11 are the named
// player colors; 12 = Neutral Aggressive (often dark red on minimap);
// 15 = Neutral Passive. The mdx-m3-viewer ships TeamColorNN.dds with the
// same indices; we mirror those approximations as solid RGB.
export const TEAM_COLORS_RGB: Array<[number, number, number]> = [
  [1.00, 0.02, 0.02], //  0 Red
  [0.00, 0.26, 1.00], //  1 Blue
  [0.11, 0.93, 0.84], //  2 Teal
  [0.32, 0.00, 0.50], //  3 Purple
  [1.00, 1.00, 0.00], //  4 Yellow
  [1.00, 0.55, 0.00], //  5 Orange
  [0.13, 0.78, 0.06], //  6 Green
  [1.00, 0.50, 0.78], //  7 Pink
  [0.59, 0.59, 0.59], //  8 Gray
  [0.49, 0.74, 1.00], //  9 LightBlue
  [0.06, 0.39, 0.27], // 10 DarkGreen
  [0.30, 0.16, 0.00], // 11 Brown
]

function teamColorFor(player: number): [number, number, number] {
  if (player < TEAM_COLORS_RGB.length) return TEAM_COLORS_RGB[player]
  // Neutral Aggressive (12), Hostile (24), Passive (15), unknown players →
  // dim red — distinct from any real player slot.
  return [0.55, 0.15, 0.15]
}

// CircleOfPower asset. Path is canonical mixed-case as it appears in
// CASC's listfile; pathSolver normalizes case before fetching.
const CIRCLE_OF_POWER_PATH = 'Buildings/Other/CircleOfPower/CircleOfPower.mdx'

// Selection tint applied via setVertexColor on the MDX instance when the
// marker is selected. Same warm-yellow boost units use (scene-instances.ts
// SELECT_TINT) so selection reads consistently across entity types.
const SELECT_TINT: [number, number, number, number] = [1.2, 1.4, 0.6, 1]

// Bounds half-extents used for ray-vs-AABB picking on the MDX path. The
// CircleOfPower model is roughly a 160-stud-diameter disc; we round up to
// 96 to give a comfortable click target without overlapping neighbours on
// stacked-sloc maps. Z half-extent is small — the disc lies on terrain.
const MDX_PICK_HALF_W = 96
const MDX_PICK_HALF_H = 64

// Pillar fallback dimensions (used when the MDX failed to load). Tuned for
// visibility at default editor zoom — 384 studs tall × 128 wide reads as a
// chunky cell-sized pillar while still fitting inside one 128×128 tile.
const PILLAR_HEIGHT = 384
const PILLAR_HALF_W = 64

// Unit cube from (-1,-1,0) to (+1,+1,+1) in local space. Shader scales
// X/Y by PILLAR_HALF_W and Z by PILLAR_HEIGHT so the base sits at the
// marker's (x,y,z) and the top is at z + PILLAR_HEIGHT.
const BOX_VERTS = new Float32Array([
  // +X face
  +1, -1, 0,   1, 0, 0,
  +1, +1, 0,   1, 0, 0,
  +1, +1, 1,   1, 0, 0,
  +1, -1, 1,   1, 0, 0,
  // -X face
  -1, +1, 0,  -1, 0, 0,
  -1, -1, 0,  -1, 0, 0,
  -1, -1, 1,  -1, 0, 0,
  -1, +1, 1,  -1, 0, 0,
  // +Y face
  +1, +1, 0,   0, 1, 0,
  -1, +1, 0,   0, 1, 0,
  -1, +1, 1,   0, 1, 0,
  +1, +1, 1,   0, 1, 0,
  // -Y face
  -1, -1, 0,   0, -1, 0,
  +1, -1, 0,   0, -1, 0,
  +1, -1, 1,   0, -1, 0,
  -1, -1, 1,   0, -1, 0,
  // +Z face (top)
  -1, -1, 1,   0, 0, 1,
  +1, -1, 1,   0, 0, 1,
  +1, +1, 1,   0, 0, 1,
  -1, +1, 1,   0, 0, 1,
  // -Z face (bottom) — drawn for completeness; rarely visible from above
  -1, +1, 0,   0, 0, -1,
  +1, +1, 0,   0, 0, -1,
  +1, -1, 0,   0, 0, -1,
  -1, -1, 0,   0, 0, -1,
])
const BOX_INDICES = new Uint16Array([
  0,1,2, 0,2,3,
  4,5,6, 4,6,7,
  8,9,10, 8,10,11,
  12,13,14, 12,14,15,
  16,17,18, 16,18,19,
  20,21,22, 20,22,23,
])

const VERT_SHADER = `
attribute vec3 a_position;
attribute vec3 a_normal;
uniform mat4 u_viewProj;
uniform vec3 u_origin;
uniform vec3 u_scale;
varying vec3 v_normal;
varying float v_topMask;
void main() {
  vec3 worldPos = u_origin + a_position * u_scale;
  gl_Position = u_viewProj * vec4(worldPos, 1.0);
  v_normal = a_normal;
  v_topMask = a_normal.z > 0.5 ? 1.0 : 0.0;
}
`.trim()

const FRAG_SHADER = `
precision mediump float;
uniform vec3 u_color;
uniform float u_selected;
varying vec3 v_normal;
varying float v_topMask;
void main() {
  vec3 light = normalize(vec3(0.4, 0.4, 1.0));
  float diffuse = max(0.55, dot(normalize(v_normal), light));
  vec3 col = u_color * diffuse;
  col += vec3(0.12) * v_topMask;
  if (u_selected > 0.5) {
    col = mix(col, vec3(1.0, 0.95, 0.4), 0.55);
  }
  gl_FragColor = vec4(col, 1.0);
}
`.trim()

function compileShader(gl: WebGLRenderingContext, type: number, source: string): WebGLShader {
  const sh = gl.createShader(type)!
  gl.shaderSource(sh, source)
  gl.compileShader(sh)
  if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
    const log = gl.getShaderInfoLog(sh)
    gl.deleteShader(sh)
    throw new Error('sloc shader compile: ' + log)
  }
  return sh
}

function buildProgram(gl: WebGLRenderingContext) {
  const program = gl.createProgram()!
  gl.attachShader(program, compileShader(gl, gl.VERTEX_SHADER, VERT_SHADER))
  gl.attachShader(program, compileShader(gl, gl.FRAGMENT_SHADER, FRAG_SHADER))
  gl.linkProgram(program)
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
    const log = gl.getProgramInfoLog(program)
    gl.deleteProgram(program)
    throw new Error('sloc program link: ' + log)
  }
  return {
    program,
    aPosition: gl.getAttribLocation(program, 'a_position'),
    aNormal: gl.getAttribLocation(program, 'a_normal'),
    uViewProj: gl.getUniformLocation(program, 'u_viewProj')!,
    uOrigin: gl.getUniformLocation(program, 'u_origin')!,
    uScale: gl.getUniformLocation(program, 'u_scale')!,
    uColor: gl.getUniformLocation(program, 'u_color')!,
    uSelected: gl.getUniformLocation(program, 'u_selected')!,
  }
}

// One sloc marker.
export interface SlocMarker {
  creationNumber: number
  player: number
  position: [number, number, number]
  rotation: number
}

export interface SlocPickInfo {
  creationNumber: number
  /** World-space center of the marker AABB. */
  center: [number, number, number]
  /** Half-extents of the marker AABB. */
  half: [number, number, number]
}

export interface SlocRenderer {
  /** Draw markers that don't have an MDX instance (pillar fallback). */
  draw(viewProj: Float32Array, selected: Set<number>): void
  /** Replace the marker list (called on map open). */
  setMarkers(ms: SlocMarker[]): void
  /** Picking info for ray-vs-AABB tests — one entry per marker. */
  pickInfos(): SlocPickInfo[]
  /** Release GL resources and detach lib instances. */
  dispose(): void
}

/**
 * Path solver matching scene-instances.ts pathSolver — kept inline so this
 * module doesn't pull in the full scene module. mdx-m3-viewer hands raw
 * MDX asset names (and texture names) to its solver; we route them through
 * Wails' embedded HTTP server to Go's assetHandler.
 */
function slocPathSolver(src: unknown): unknown {
  if (typeof src !== 'string') return undefined
  return '/asset/' + src.toLowerCase().replace(/\\/g, '/')
}

function quatZ(angle: number): number[] {
  const h = angle * 0.5
  return [0, 0, Math.sin(h), Math.cos(h)]
}

/**
 * Build the sloc renderer. `viewer` and `scene` are optional — when both
 * are present we try to render markers as CircleOfPower MDX instances; if
 * either is missing or the model load fails, every marker falls back to a
 * primitive pillar drawn here in this module.
 *
 * `unitInstances` is the scene's master cn→MdxModelInstance map. When
 * provided, sloc MDX instances are registered there alongside real units
 * so the gizmo, drag-to-move, and entity-changed handlers in
 * scene-instances.ts all find them by creation_number without needing
 * sloc-specific branches. WC3 itself stores slocs as units (type_id
 * "sloc") in war3mapUnits.doo with the same position/rotation/scale
 * fields as any other unit, so they're transformable like anything else.
 */
export function buildSlocRenderer(
  gl: WebGLRenderingContext,
  viewer: any | null = null,
  scene: any | null = null,
  unitInstances: Map<number, any> | null = null,
): SlocRenderer | null {
  let prog: ReturnType<typeof buildProgram>
  try {
    prog = buildProgram(gl)
  } catch (e) {
    flog('[slocs] program build failed:', e instanceof Error ? e.message : String(e))
    return null
  }

  const vbo = gl.createBuffer()!
  gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
  gl.bufferData(gl.ARRAY_BUFFER, BOX_VERTS, gl.STATIC_DRAW)

  const ibo = gl.createBuffer()!
  gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
  gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, BOX_INDICES, gl.STATIC_DRAW)

  let markers: SlocMarker[] = []
  // Per-marker MdxModelInstance, or absent if the marker has no MDX (model
  // failed to load, or load is still in flight when setMarkers was called).
  const mdxInstances = new Map<number, any>()
  // Track which markers are currently shown as selected (via setVertexColor
  // SELECT_TINT) so we only invoke the lib's color update on edge changes.
  const selectedTinted = new Set<number>()

  // CircleOfPower model — loaded lazily on first setMarkers when viewer+scene
  // are both available. `null` means "load not yet attempted"; a Promise
  // means "load in flight"; a resolved model means success; `circleModelFailed`
  // means a prior attempt failed permanently and we shouldn't retry.
  let circleModelP: Promise<any> | null = null
  let circleModel: any = null
  let circleModelFailed = false

  function loadCircleModel(): Promise<any | null> {
    if (circleModel) return Promise.resolve(circleModel)
    if (circleModelFailed) return Promise.resolve(null)
    if (!viewer || typeof viewer.load !== 'function') return Promise.resolve(null)
    if (!circleModelP) {
      circleModelP = (async () => {
        try {
          const m = await viewer.load(CIRCLE_OF_POWER_PATH, slocPathSolver)
          if (!m || typeof m.addInstance !== 'function') {
            circleModelFailed = true
            flog('[slocs] CircleOfPower load returned no addInstance — falling back to pillars')
            return null
          }
          circleModel = m
          flog('[slocs] CircleOfPower loaded')
          return m
        } catch (e) {
          circleModelFailed = true
          flog('[slocs] CircleOfPower load failed, using pillar fallback:', e instanceof Error ? e.message : String(e))
          return null
        }
      })()
    }
    return circleModelP
  }

  // Create one CircleOfPower instance for a marker. Safe to call only after
  // loadCircleModel has resolved with a non-null model.
  function spawnMdxInstance(m: SlocMarker): any | null {
    if (!circleModel || !scene) return null
    let inst: any
    try {
      inst = circleModel.addInstance()
      if (!inst) return null
      inst.move([m.position[0], m.position[1], m.position[2]])
      inst.rotateLocal(quatZ(m.rotation))
      inst.setTeamColor(m.player)
      inst.setScene(scene)
      // Tag the instance with the same convenience props placeUnit writes
      // (scene-instances.ts) so the gizmo, drag-move arming, and
      // entity-changed rotation/scale handlers can treat slocs identically
      // to real units. Stock UnitData.slk has no row for "sloc" — there is
      // no SLK model_scale or move_height to bake in — so the multipliers
      // are 1× / 0 by definition.
      ;(inst as any).__wc3ForgeTypeId = 'sloc'
      ;(inst as any).__wc3ForgeRotation = m.rotation
      ;(inst as any).__wc3ForgeScale = [1, 1, 1]
      ;(inst as any).__wc3ForgeModelScale = 1
      // Best-effort: kick the model into its "stand" sequence so the runes
      // animate. CircleOfPower's stand is index 0 in practice but we look
      // it up by name to stay robust to MDX edits.
      const seqs = inst?.model?.sequences
      if (Array.isArray(seqs)) {
        let standIdx = -1
        for (let i = 0; i < seqs.length; i++) {
          const name = String(seqs[i]?.name ?? '')
          if (name.split('-')[0].replace(/\d/g, '').trim().toLowerCase() === 'stand') {
            standIdx = i
            break
          }
        }
        if (standIdx >= 0) inst.setSequence(standIdx)
      }
      // Register in the scene's master unit-instance map so the gizmo +
      // drag-move + entity-changed flows in scene-instances.ts find slocs
      // by creation_number with no sloc-specific branches.
      if (unitInstances) unitInstances.set(m.creationNumber, inst)
    } catch (e) {
      flog('[slocs] spawn instance failed:', e instanceof Error ? e.message : String(e))
      return null
    }
    return inst
  }

  function clearMdxInstances() {
    for (const [cn, inst] of mdxInstances) {
      if (!inst) continue
      // detach() is the lib's "remove from scene" op (scene.removeInstance).
      // Matches what scene-instances.clearInstances() does on real units.
      // Idempotent: returns false (no-op) if the instance is already
      // detached, so double-cleanup from a clearInstances → setMarkers([])
      // sequence is safe.
      try { inst.detach() } catch { /* swallow */ }
      // Mirror in unitInstances. Safe when unitInstances was already
      // cleared by scene-instances.clearInstances() — delete on a missing
      // key is a no-op.
      if (unitInstances) unitInstances.delete(cn)
    }
    mdxInstances.clear()
    selectedTinted.clear()
  }

  return {
    setMarkers(ms: SlocMarker[]) {
      // Render slocs at their on-disk positions verbatim, even when a map
      // (e.g. Enfo's FFB) places every sloc at the same coordinate. The
      // map author chose to stack them; the editor should show that truth
      // and let the author see what they authored. Coincident discs will
      // z-fight and only one player's tint will read — that's correct.
      markers = ms.slice()

      // Tear down any instances from a previous map.
      clearMdxInstances()

      // If we already have the model cached, spawn instances immediately;
      // otherwise kick off the async load and spawn when it resolves.
      // Either way, the pillar fallback (in draw()) covers the gap while
      // MDX instances are missing.
      if (!viewer || !scene) return
      if (circleModel) {
        for (const m of markers) {
          const inst = spawnMdxInstance(m)
          if (inst) mdxInstances.set(m.creationNumber, inst)
        }
        return
      }
      if (circleModelFailed) return
      // Capture markers at-call-time so a fast subsequent setMarkers (e.g.
      // user opens map B while map A's load is mid-flight) doesn't spawn
      // stale markers into the new state.
      const myMarkers = markers
      loadCircleModel().then((m) => {
        if (!m) return
        if (myMarkers !== markers) return // superseded by a later setMarkers
        for (const mk of markers) {
          if (mdxInstances.has(mk.creationNumber)) continue
          const inst = spawnMdxInstance(mk)
          if (inst) mdxInstances.set(mk.creationNumber, inst)
        }
      })
    },

    pickInfos(): SlocPickInfo[] {
      const out: SlocPickInfo[] = []
      for (const m of markers) {
        const inst = mdxInstances.get(m.creationNumber)
        if (inst) {
          // MDX path: read live worldLocation so the picking AABB tracks
          // the disc as the gizmo / drag-to-move moves it. Without this
          // the click target would lag behind the visible disc until the
          // next setMarkers() rebuild.
          const wl = inst.worldLocation ?? m.position
          out.push({
            creationNumber: m.creationNumber,
            center: [wl[0], wl[1], wl[2] + MDX_PICK_HALF_H],
            half: [MDX_PICK_HALF_W, MDX_PICK_HALF_W, MDX_PICK_HALF_H],
          })
        } else {
          // Pillar fallback: tall box spanning z..z+PILLAR_HEIGHT. No live
          // instance to read from, so the marker's stored position is the
          // source of truth — the pillar shader uses the same value.
          out.push({
            creationNumber: m.creationNumber,
            center: [m.position[0], m.position[1], m.position[2] + PILLAR_HEIGHT * 0.5],
            half: [PILLAR_HALF_W, PILLAR_HALF_W, PILLAR_HEIGHT * 0.5],
          })
        }
      }
      return out
    },

    draw(viewProj: Float32Array, selected: Set<number>) {
      // Apply / clear selection tint on MDX instances on edge changes only.
      // setVertexColor is cheap but it churns the lib's color uniforms; for
      // ~12 markers this is a non-issue either way, but the edge-only path
      // keeps the lib's batch state stable across frames.
      for (const [cn, inst] of mdxInstances) {
        if (!inst) continue
        const wantSelected = selected.has(cn)
        const isSelected = selectedTinted.has(cn)
        if (wantSelected && !isSelected) {
          try { inst.setVertexColor(SELECT_TINT) } catch { /* lib path; swallow */ }
          selectedTinted.add(cn)
        } else if (!wantSelected && isSelected) {
          try { inst.setVertexColor([1, 1, 1, 1]) } catch { /* lib path; swallow */ }
          selectedTinted.delete(cn)
        }
      }

      // Pillar fallback for any marker that doesn't have an MDX instance.
      const fallback: SlocMarker[] = []
      for (const m of markers) {
        if (!mdxInstances.get(m.creationNumber)) fallback.push(m)
      }
      if (fallback.length === 0) return

      // The lib's render path leaves an unknown WebGL state behind: vertex
      // attribs 2+ may still be enabled and pointing into model buffers,
      // depth mask may be off, blend func may be set to translucent-merge,
      // current program changes per batch. We explicitly re-establish every
      // bit of state we care about — cheap and avoids ghost-rendering.
      gl.useProgram(prog.program)
      gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
      const maxAttribs = gl.getParameter(gl.MAX_VERTEX_ATTRIBS) | 0
      for (let i = 0; i < maxAttribs; i++) gl.disableVertexAttribArray(i)
      gl.enableVertexAttribArray(prog.aPosition)
      gl.enableVertexAttribArray(prog.aNormal)
      const stride = 6 * 4
      gl.vertexAttribPointer(prog.aPosition, 3, gl.FLOAT, false, stride, 0)
      gl.vertexAttribPointer(prog.aNormal, 3, gl.FLOAT, false, stride, 3 * 4)
      gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)
      gl.enable(gl.DEPTH_TEST)
      gl.depthFunc(gl.LEQUAL)
      gl.depthMask(true)
      gl.disable(gl.CULL_FACE)
      gl.disable(gl.BLEND)

      for (const m of fallback) {
        gl.uniform3f(prog.uOrigin, m.position[0], m.position[1], m.position[2])
        gl.uniform3f(prog.uScale, PILLAR_HALF_W, PILLAR_HALF_W, PILLAR_HEIGHT)
        const c = teamColorFor(m.player)
        gl.uniform3f(prog.uColor, c[0], c[1], c[2])
        gl.uniform1f(prog.uSelected, selected.has(m.creationNumber) ? 1 : 0)
        gl.drawElements(gl.TRIANGLES, BOX_INDICES.length, gl.UNSIGNED_SHORT, 0)
      }

      gl.disableVertexAttribArray(prog.aPosition)
      gl.disableVertexAttribArray(prog.aNormal)
    },

    dispose() {
      clearMdxInstances()
      gl.deleteBuffer(vbo)
      gl.deleteBuffer(ibo)
      gl.deleteProgram(prog.program)
      markers = []
    },
  }
}
