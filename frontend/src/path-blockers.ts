// path-blockers.ts — pink/black checker overlay for "Pathing Blockers"
// category doodads.
//
// What a path blocker is: a doodad whose category in Doodads.slk is "P"
// (resolved through WESTRING_DTYPE_PATHING to "Pathing Blockers"). Their
// purpose in WC3 is to make terrain unwalkable / unbuildable / unflyable
// without showing any in-game art. Most of these rows ship with NO MDX file
// at all (info.file is empty) — the game just consumes their pathing
// footprint. That's correct for in-game, but in an editor the placements
// are invisible, which makes them impossible to inspect.
//
// HiveWE handles this two ways simultaneously: (1) substitutes
// Objects/Invalidmodel/Invalidmodel.mdx (the in-engine "broken model"
// placeholder, which is a magenta/black checker box) when the SLK row has
// no file, and (2) blits the row's pathtex texture into the editor's
// pathing-map debug view. We're not loading Invalidmodel.mdx here because
// the lib's resourceMap caching plus a missing-asset render path adds risk
// (the parser-patch shader-cache trap is the kind of bug that ate days of
// our render bring-up — see feedback-mdx-viewer-shader-cache). Instead we
// draw a small overlay quad procedurally with the same pink/black aesthetic
// the user already recognises from HiveWE / the in-engine fallback.
//
// Approach: same shape as sloc-markers.ts and cell-highlight.ts — minimal
// standalone WebGL1 shader owned by this module, drawn AFTER viewer.render()
// in the main RAF loop. Each blocker is one quad (two triangles) at the
// doodad's world XY + a small Z lift so it sits visibly above terrain. The
// fragment shader generates the checker procedurally from a stud-scaled UV
// (no texture, no asset fetch). Semi-transparent so the user can still see
// what's underneath.
//
// Picking: pickInfos() returns the AABB of each placed blocker so the
// scene-instances ray-pick walker can include them alongside units, doodads,
// and slocs. The kind reported is 'doodad' so the Properties panel resolves
// through GetDoodad (path blockers ARE doodads — they're entries in
// war3map.doo with creation_numbers in the doodad namespace).

import { flog } from './debuglog'

// Footprint of a single path blocker. World-space center at (x, y, z) with
// half-extents (hx, hy). Z lift is baked in below — the position the caller
// passes us is the raw doodad position; the overlay floats `Z_LIFT` studs
// above it. The default footprint is 64 studs half-width per side (one
// 128×128 terrain cell) which matches the most common path-blocker scale.
//
// In a future revision we could read the per-type pathtex SLK column and
// shape the overlay to match (4x4Default = 1 cell, 8x8Default = 2 cells,
// etc.), but the current single-cell default is the right out-of-the-box
// look for "I want to see WHERE the blocker is" — and adjusting per-instance
// scale via `scale` on the Doodad DTO already handles the common case of a
// blocker that's been scaled up to cover a wider region.
export interface PathBlockerMarker {
  creationNumber: number
  position: [number, number, number]
  /** Per-side half-extent in studs; defaults to 64 (one terrain cell) at scale=1. */
  halfX: number
  halfY: number
}

export interface PathBlockerPickInfo {
  creationNumber: number
  center: [number, number, number]
  half: [number, number, number]
}

export interface PathBlockerRenderer {
  /** Draw all blockers; `selected` lets us brighten / outline picks. */
  draw(viewProj: Float32Array, selected: Set<number>): void
  /** Replace the marker list (called on map open). */
  setMarkers(ms: PathBlockerMarker[]): void
  /** AABB info for ray-vs-AABB picking — one entry per marker. */
  pickInfos(): PathBlockerPickInfo[]
  /** Release GL resources. */
  dispose(): void
}

// Z lift in studs. Same tuning rationale as cell-highlight.ts: large enough
// to beat depth-precision wobble on a tilted RTS camera, small enough that
// the overlay doesn't look like it's hovering above the ground.
const Z_LIFT = 4

// Checker pattern scale: each square in the checker is CHECKER_STUDS wide in
// world space. 32 studs makes the pattern read clearly without becoming
// noisy — at a default footprint of 128×128, a single blocker shows a 4×4
// checker which is unmistakable.
const CHECKER_STUDS = 32

// Quad geometry — unit square in local space, scaled by per-blocker
// half-extents in the vertex shader. UVs come from the local position * scale
// so the checker pattern is computed in world-stud space (consistent square
// size regardless of blocker scale).
//
// Layout: vec2 local position interleaved with vec2 sign for U/V derivation.
// Verts at (-1,-1) (+1,-1) (+1,+1) (-1,+1) drawn as two triangles.
const QUAD_VERTS = new Float32Array([
  -1, -1,
  +1, -1,
  +1, +1,
  -1, +1,
])
const QUAD_INDICES = new Uint16Array([0, 1, 2, 0, 2, 3])

const VERT_SHADER = `
attribute vec2 a_localXY;
uniform mat4 u_viewProj;
uniform vec3 u_origin;
uniform vec2 u_half;
varying vec2 v_worldXY;
void main() {
  vec3 worldPos = vec3(
    u_origin.x + a_localXY.x * u_half.x,
    u_origin.y + a_localXY.y * u_half.y,
    u_origin.z
  );
  v_worldXY = worldPos.xy;
  gl_Position = u_viewProj * vec4(worldPos, 1.0);
}
`.trim()

// Pink/black checker, alpha-blended. mod() on a vec2 gives us 2D checker for
// free: floor(worldXY / CHECKER_STUDS) summed modulo 2 picks alternating
// cells. Pink uses a saturated magenta tuned to read clearly against the
// editor's mostly-green/blue/brown terrain palette; black is true 0 so the
// contrast is unmistakable. Alpha at 0.7 leaves enough of the underlying
// terrain readable for context.
//
// `u_selected` brightens the pattern when the blocker is in the selection
// set so click feedback matches the rest of the editor (slocs, units,
// doodads all tint when selected).
const FRAG_SHADER = `
precision mediump float;
uniform float u_selected;
varying vec2 v_worldXY;
void main() {
  vec2 cell = floor(v_worldXY / ${CHECKER_STUDS.toFixed(1)});
  float odd = mod(cell.x + cell.y, 2.0);
  vec3 pink = vec3(1.00, 0.10, 0.85);
  vec3 dark = vec3(0.02, 0.02, 0.02);
  vec3 col = mix(dark, pink, odd);
  // Selection tint: pulse warm yellow over the checker so picked blockers
  // are obvious in dense areas. Matches the warm-yellow boost the unit and
  // doodad pickers use (SELECT_TINT in scene-instances).
  if (u_selected > 0.5) {
    col = mix(col, vec3(1.0, 0.95, 0.4), 0.55);
  }
  gl_FragColor = vec4(col, 0.7);
}
`.trim()

function compileShader(gl: WebGLRenderingContext, type: number, src: string): WebGLShader {
  const sh = gl.createShader(type)!
  gl.shaderSource(sh, src)
  gl.compileShader(sh)
  if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
    const log = gl.getShaderInfoLog(sh)
    gl.deleteShader(sh)
    throw new Error('path-blockers shader compile: ' + log)
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
    throw new Error('path-blockers program link: ' + log)
  }
  return {
    program,
    aLocalXY: gl.getAttribLocation(program, 'a_localXY'),
    uViewProj: gl.getUniformLocation(program, 'u_viewProj')!,
    uOrigin: gl.getUniformLocation(program, 'u_origin')!,
    uHalf: gl.getUniformLocation(program, 'u_half')!,
    uSelected: gl.getUniformLocation(program, 'u_selected')!,
  }
}

export function buildPathBlockerRenderer(gl: WebGLRenderingContext): PathBlockerRenderer | null {
  let prog: ReturnType<typeof buildProgram>
  try {
    prog = buildProgram(gl)
  } catch (e) {
    flog('[path-blockers] program build failed:', e instanceof Error ? e.message : String(e))
    return null
  }

  const vbo = gl.createBuffer()!
  gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
  gl.bufferData(gl.ARRAY_BUFFER, QUAD_VERTS, gl.STATIC_DRAW)

  const ibo = gl.createBuffer()!
  gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
  gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, QUAD_INDICES, gl.STATIC_DRAW)

  let markers: PathBlockerMarker[] = []

  return {
    setMarkers(ms: PathBlockerMarker[]) {
      markers = ms.slice()
    },

    pickInfos(): PathBlockerPickInfo[] {
      const out: PathBlockerPickInfo[] = []
      for (const m of markers) {
        // AABB: thin Z extent so the quad picks correctly when the camera is
        // tilted but doesn't grab clicks that miss vertically. 4 studs of
        // Z thickness centered at the lifted-quad height.
        out.push({
          creationNumber: m.creationNumber,
          center: [m.position[0], m.position[1], m.position[2] + Z_LIFT],
          half: [m.halfX, m.halfY, 4],
        })
      }
      return out
    },

    draw(viewProj: Float32Array, selected: Set<number>) {
      if (markers.length === 0) return
      // Re-establish every bit of GL state we touch — the lib's render loop
      // leaves attribs / programs / blend state in unknown configuration.
      // Same playbook as sloc-markers + cell-highlight. The shader-cache
      // invalidation in scene-instances.ts covers the lib's side.
      gl.useProgram(prog.program)
      gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
      const maxAttribs = gl.getParameter(gl.MAX_VERTEX_ATTRIBS) | 0
      for (let i = 0; i < maxAttribs; i++) gl.disableVertexAttribArray(i)
      gl.enableVertexAttribArray(prog.aLocalXY)
      gl.vertexAttribPointer(prog.aLocalXY, 2, gl.FLOAT, false, 8, 0)
      gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)
      // Alpha-blended overlay: depth-test ON so terrain / cliffs / models
      // can occlude us correctly; depth-write OFF so multiple overlapping
      // blockers don't z-fight each other into hard-edged tile boundaries.
      // No back-face culling — blockers are flat quads, cullable winding
      // would be a footgun if the camera tilts past horizontal.
      gl.enable(gl.DEPTH_TEST)
      gl.depthFunc(gl.LEQUAL)
      gl.depthMask(false)
      gl.disable(gl.CULL_FACE)
      gl.enable(gl.BLEND)
      gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)

      for (const m of markers) {
        gl.uniform3f(
          prog.uOrigin,
          m.position[0],
          m.position[1],
          m.position[2] + Z_LIFT,
        )
        gl.uniform2f(prog.uHalf, m.halfX, m.halfY)
        gl.uniform1f(prog.uSelected, selected.has(m.creationNumber) ? 1 : 0)
        gl.drawElements(gl.TRIANGLES, QUAD_INDICES.length, gl.UNSIGNED_SHORT, 0)
      }

      // Don't leak attribs / state into the next pass. Restore depth-write
      // (a number of downstream passes assume depth-write is back on).
      gl.disableVertexAttribArray(prog.aLocalXY)
      gl.depthMask(true)
      gl.disable(gl.BLEND)
    },

    dispose() {
      gl.deleteBuffer(vbo)
      gl.deleteBuffer(ibo)
      gl.deleteProgram(prog.program)
      markers = []
    },
  }
}
