// Sloc (start-location) marker renderer.
//
// In Warcraft III maps, a "sloc" is a unit Entity whose type_id is "sloc" —
// it's not a real unit, just a marker that says "player N starts here". The
// stock data has no MDX for "sloc", so the regular placeUnit path skips them.
//
// For an editor view, we want them visible. We render each sloc as a small
// solid vertical pillar (axis-aligned box) tinted to its owning player's
// team color so the user can spot player layouts at a glance.
//
// Approach: pure WebGL1 primitive rendering — minimal vertex/fragment shader
// owned by this module, one shared box geometry, per-instance data uploaded
// every frame via uniforms. Slocs are typically <16 per map so per-instance
// draw calls are cheap; no instancing needed.
//
// Drawn AFTER the lib's viewer.render() so the markers appear on top of
// terrain/units (depth tested). The user wants to SEE start locations even
// when zoomed out; modest overdraw on a handful of pillars is fine.
//
// Selection integration: getSlocPickInfos() returns the axis-aligned bbox
// for each marker so the scene's picking pipeline can include slocs in
// ray-vs-AABB tests alongside the unit ray-vs-sphere check.

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

// Marker dimensions tuned for visibility at default editor zoom.
// At pitch=π/3 and the camera's default fit-to-map-span, individual map
// features project to ~5–10 px per 64-stud diameter. We want slocs to be
// READABLE — chunky enough to spot at a glance, not so big they obscure
// nearby placement. 384 studs tall × 128 wide reads clearly while still
// fitting inside a single 128×128 grid cell footprint.
const PILLAR_HEIGHT = 384
const PILLAR_HALF_W = 64

// Unit cube from (-1,-1,0) to (+1,+1,+1) in local space. The shader scales
// the X/Y axes by PILLAR_HALF_W and Z by PILLAR_HEIGHT, so the base sits on
// the ground at the marker's (x, y, z) and the top is at z + PILLAR_HEIGHT.
//
// Vertices are duplicated per-face so each face can have its own outward
// normal for the simple Lambert-ish lighting in the fragment shader.
// Layout: vec3 position + vec3 normal interleaved (6 floats * 24 verts = 144).
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
  // 1.0 on the top face, 0.0 elsewhere — used by the frag shader to brighten
  // the visible cap when looking down at the marker from an editor camera.
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
  // Cheap directional shade: max with a constant ambient so back-facing sides
  // stay legible. Hard-coded "sun" coming from +X +Y +Z.
  vec3 light = normalize(vec3(0.4, 0.4, 1.0));
  float diffuse = max(0.55, dot(normalize(v_normal), light));
  vec3 col = u_color * diffuse;
  // Cap brightening — make the top read at any zoom.
  col += vec3(0.12) * v_topMask;
  // Selection tint: pulse a warm yellow over the base color.
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

// One sloc marker. We keep creation_number + player + position so we can:
//   - re-render the marker every frame
//   - feed ray-vs-AABB picking the right metadata
//   - paint a selection tint when the marker is selected
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
  /** Draw all markers; pass selected set so we tint accordingly. */
  draw(viewProj: Float32Array, selected: Set<number>): void
  /** Replace the marker list (called on map open). */
  setMarkers(ms: SlocMarker[]): void
  /** Picking info for ray-vs-AABB tests — one entry per marker. */
  pickInfos(): SlocPickInfo[]
  /** Release GL resources. */
  dispose(): void
}

export function buildSlocRenderer(gl: WebGLRenderingContext): SlocRenderer | null {
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

  return {
    setMarkers(ms: SlocMarker[]) {
      // Detect degenerate input: many maps (e.g. Enfo's FFB) place every
      // sloc at the exact same coordinate and rely on scripted spawning to
      // distribute players at runtime. Drawing N coincident boxes on top of
      // each other produces severe z-fighting and only the last-drawn color
      // shows. Stack them vertically so each player gets a visible band.
      const xyKey = (m: SlocMarker) => `${m.position[0]}|${m.position[1]}`
      const counts = new Map<string, number>()
      for (const m of ms) {
        counts.set(xyKey(m), (counts.get(xyKey(m)) ?? 0) + 1)
      }
      const seen = new Map<string, number>()
      const out: SlocMarker[] = []
      for (const m of ms) {
        const k = xyKey(m)
        const idx = seen.get(k) ?? 0
        seen.set(k, idx + 1)
        // If multiple slocs share an XY position, stack them vertically so
        // each is independently visible. PILLAR_HEIGHT-tall steps mean the
        // boxes line up bottom-to-top without overlap. Single-occupancy
        // positions render unmodified.
        const dup = (counts.get(k) ?? 1) > 1
        const stackZ = dup ? idx * (PILLAR_HEIGHT + 24) : 0
        out.push({
          creationNumber: m.creationNumber,
          player: m.player,
          rotation: m.rotation,
          position: [m.position[0], m.position[1], m.position[2] + stackZ],
        })
      }
      markers = out
    },

    pickInfos(): SlocPickInfo[] {
      const out: SlocPickInfo[] = []
      for (const m of markers) {
        out.push({
          creationNumber: m.creationNumber,
          // Box spans z..z+PILLAR_HEIGHT in world; center is at z + half.
          center: [m.position[0], m.position[1], m.position[2] + PILLAR_HEIGHT * 0.5],
          half: [PILLAR_HALF_W, PILLAR_HALF_W, PILLAR_HEIGHT * 0.5],
        })
      }
      return out
    },

    draw(viewProj: Float32Array, selected: Set<number>) {
      if (markers.length === 0) return
      // The lib's render path leaves an unknown WebGL state behind: vertex
      // attribs 2+ may still be enabled and pointing into model buffers, depth
      // mask may be off, blend func may be set to translucent-merge, current
      // program changes per batch, etc. We explicitly re-establish every bit
      // of state we care about — this is cheap and avoids ghost-rendering.
      gl.useProgram(prog.program)
      gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
      // Disable any other attrib slots the lib might have enabled before us.
      // Attribs 0/1 we set up; attribs 2..7 are turned off so stale pointers
      // don't bleed into the draw call.
      const maxAttribs = gl.getParameter(gl.MAX_VERTEX_ATTRIBS) | 0
      for (let i = 0; i < maxAttribs; i++) gl.disableVertexAttribArray(i)
      gl.enableVertexAttribArray(prog.aPosition)
      gl.enableVertexAttribArray(prog.aNormal)
      const stride = 6 * 4
      gl.vertexAttribPointer(prog.aPosition, 3, gl.FLOAT, false, stride, 0)
      gl.vertexAttribPointer(prog.aNormal, 3, gl.FLOAT, false, stride, 3 * 4)
      gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)
      // Depth test ON, depth write ON: markers occlude / get occluded by
      // terrain and units correctly.
      gl.enable(gl.DEPTH_TEST)
      gl.depthFunc(gl.LEQUAL)
      gl.depthMask(true)
      // No back-face culling — only 12 markers per map and the boxes are
      // small. Sidesteps any front-face winding-order surprises.
      gl.disable(gl.CULL_FACE)
      gl.disable(gl.BLEND)

      for (const m of markers) {
        gl.uniform3f(prog.uOrigin, m.position[0], m.position[1], m.position[2])
        gl.uniform3f(prog.uScale, PILLAR_HALF_W, PILLAR_HALF_W, PILLAR_HEIGHT)
        const c = teamColorFor(m.player)
        gl.uniform3f(prog.uColor, c[0], c[1], c[2])
        gl.uniform1f(prog.uSelected, selected.has(m.creationNumber) ? 1 : 0)
        gl.drawElements(gl.TRIANGLES, BOX_INDICES.length, gl.UNSIGNED_SHORT, 0)
      }

      // Be a good citizen — don't leak attribs into the next pass.
      gl.disableVertexAttribArray(prog.aPosition)
      gl.disableVertexAttribArray(prog.aNormal)
    },

    dispose() {
      gl.deleteBuffer(vbo)
      gl.deleteBuffer(ibo)
      gl.deleteProgram(prog.program)
      markers = []
    },
  }
}
