// Water surface: per-cell quad mesh at per-corner water heights, tinted by
// a depth-based blend matching HiveWE's `data/shaders/water.frag` + `water.vert`.
//
// Per-corner attributes:
//   - position (x, y, water_z + tileset_offset)
//   - color (vec4 — premultiplied at build-time from shallow/deep colors
//     blended by depth-above-ground)
//
// Cells with no water (none of the 4 corners has HasWater set) emit no
// triangles. Cells with partial water still emit a full quad — corners
// without water get their corner_height as a fallback so the quad slopes
// down to dry ground rather than floating.
//
// Drawn after viewer.render() in the RAF loop with depth-test ON +
// depth-write OFF + alpha blending ON, so it occludes anything below the
// water surface and is partially transparent over what's above the surface.

import { flog } from './debuglog'

interface WaterDataDTO {
  width: number
  height: number
  center_offset: [number, number]
  heights: number[]     // corner_height in studs (FinalZ — includes layer_height)
  water_z: number[]     // per-corner water surface Z in studs
  has_water: number[]   // 1 = water exists at this corner
  water: {
    ok: boolean
    offset: number      // additional Z offset from Water.slk (HiveWE units; we multiply by 128)
    shallow_min: [number, number, number, number]
    shallow_max: [number, number, number, number]
    deep_min: [number, number, number, number]
    deep_max: [number, number, number, number]
  }
}

const VERT_SHADER = `
attribute vec3 a_position;
attribute vec4 a_color;
uniform mat4 u_viewProj;
varying vec4 v_color;
void main() {
  v_color = a_color;
  gl_Position = u_viewProj * vec4(a_position, 1.0);
}
`.trim()

const FRAG_SHADER = `
precision mediump float;
varying vec4 v_color;
void main() {
  gl_FragColor = v_color;
}
`.trim()

// HiveWE's depth-blend thresholds. Values in HiveWE units (1 cell = 128 studs).
// At depth ≤ min_depth: water is too shallow to render with shallow_min (corner
// gets fully transparent contribution). Between min_depth and deeplevel: lerp
// shallow_min → shallow_max. Between deeplevel and maxdepth: lerp deep_min →
// deep_max. Beyond maxdepth: deep_max.
const MIN_DEPTH = 10 / 128
const DEEPLEVEL = 64 / 128
const MAXDEPTH = 72 / 128

function compileShader(gl: WebGLRenderingContext, type: number, src: string): WebGLShader {
  const sh = gl.createShader(type)!
  gl.shaderSource(sh, src)
  gl.compileShader(sh)
  if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
    const log = gl.getShaderInfoLog(sh)
    gl.deleteShader(sh)
    throw new Error('water shader compile: ' + log)
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
    throw new Error('water program link: ' + log)
  }
  return {
    program,
    aPosition: gl.getAttribLocation(program, 'a_position'),
    aColor: gl.getAttribLocation(program, 'a_color'),
    uViewProj: gl.getUniformLocation(program, 'u_viewProj')!,
  }
}

export interface WaterMesh {
  /** Draw using the given world→clip matrix (camera.viewProjectionMatrix). */
  draw(viewProj: Float32Array): void
  /** Triangle count for stats. */
  triCount: number
  /** Release GL resources. */
  dispose(): void
}

export function buildWater(gl: WebGLRenderingContext, t: WaterDataDTO): WaterMesh | null {
  if (!t || !t.water_z || !t.has_water) return null

  const W = t.width
  const H = t.height
  if (W < 2 || H < 2) return null

  // Apply the per-tileset water offset (HiveWE units → studs). Each corner's
  // final water surface Z = water_z[i] + offset*128.
  const offsetStuds = (t.water?.offset ?? 0) * 128

  // Pre-compute per-corner colors. Color depends on (water_z + offset) - ground_z
  // — i.e., how deep the water is above the ground at that corner. Corners
  // outside any water cell still get a color computed (cheap, harmless;
  // they'll be skipped via the cell-water check).
  const cornerColor = new Float32Array(W * H * 4)
  const sMin = t.water.shallow_min, sMax = t.water.shallow_max
  const dMin = t.water.deep_min, dMax = t.water.deep_max
  for (let k = 0; k < W * H; k++) {
    // Depth is in HiveWE units (1 cell = 128 studs).
    const depthStuds = (t.water_z[k] + offsetStuds) - t.heights[k]
    const depth = Math.max(0, Math.min(1, depthStuds / 128))
    let r: number, g: number, b: number, a: number
    if (depth <= DEEPLEVEL) {
      const u = Math.max(0, depth - MIN_DEPTH) / (DEEPLEVEL - MIN_DEPTH)
      r = lerp(sMin[0], sMax[0], u)
      g = lerp(sMin[1], sMax[1], u)
      b = lerp(sMin[2], sMax[2], u)
      a = lerp(sMin[3], sMax[3], u)
    } else {
      const u = Math.min(1, Math.max(0, depth - DEEPLEVEL) / (MAXDEPTH - DEEPLEVEL))
      r = lerp(dMin[0], dMax[0], u)
      g = lerp(dMin[1], dMax[1], u)
      b = lerp(dMin[2], dMax[2], u)
      a = lerp(dMin[3], dMax[3], u)
    }
    const o = k * 4
    cornerColor[o] = r / 255
    cornerColor[o + 1] = g / 255
    cornerColor[o + 2] = b / 255
    cornerColor[o + 3] = a / 255
  }

  // Build per-corner vertex array. Only emit indices for cells whose 4
  // corners include at least one HasWater — matches HiveWE's
  // is_water-per-cell check in water.vert. Vertices are emitted once per
  // corner (shared between cells); indexing keeps the buffer small.
  const cx = t.center_offset[0]
  const cy = t.center_offset[1]
  const verts = new Float32Array(W * H * 7) // pos(3) + color(4)
  for (let j = 0; j < H; j++) {
    for (let i = 0; i < W; i++) {
      const k = j * W + i
      const o = k * 7
      verts[o] = cx + i * 128
      verts[o + 1] = cy + j * 128
      verts[o + 2] = t.water_z[k] + offsetStuds
      verts[o + 3] = cornerColor[k * 4]
      verts[o + 4] = cornerColor[k * 4 + 1]
      verts[o + 5] = cornerColor[k * 4 + 2]
      verts[o + 6] = cornerColor[k * 4 + 3]
    }
  }

  // Index buffer: 6 indices per water cell (2 triangles per quad).
  // Uint16 caps at 65535 vertices — a 256x256 map is 65k corners, right at
  // the limit. Larger maps would need OES_element_index_uint or chunking.
  // For now, log and skip past the limit.
  const N = W * H
  if (N > 65535) {
    flog(`[water] vertex count ${N} exceeds 16-bit index limit; water disabled`)
    return null
  }

  // Count + emit indices for water cells only.
  let cellCount = 0
  for (let j = 0; j < H - 1; j++) {
    for (let i = 0; i < W - 1; i++) {
      const bl = j * W + i
      const br = bl + 1
      const tl = bl + W
      const tr = tl + 1
      if (t.has_water[bl] || t.has_water[br] || t.has_water[tl] || t.has_water[tr]) {
        cellCount++
      }
    }
  }
  if (cellCount === 0) return null

  const indices = new Uint16Array(cellCount * 6)
  let idxPos = 0
  for (let j = 0; j < H - 1; j++) {
    for (let i = 0; i < W - 1; i++) {
      const bl = j * W + i
      const br = bl + 1
      const tl = bl + W
      const tr = tl + 1
      if (!(t.has_water[bl] || t.has_water[br] || t.has_water[tl] || t.has_water[tr])) continue
      // CCW from above. Same winding as terrain mesh.
      indices[idxPos++] = bl
      indices[idxPos++] = br
      indices[idxPos++] = tr
      indices[idxPos++] = bl
      indices[idxPos++] = tr
      indices[idxPos++] = tl
    }
  }

  const vbo = gl.createBuffer()!
  gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
  gl.bufferData(gl.ARRAY_BUFFER, verts, gl.STATIC_DRAW)

  const ibo = gl.createBuffer()!
  gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
  gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, indices, gl.STATIC_DRAW)

  const prog = buildProgram(gl)
  const stride = 7 * 4

  return {
    triCount: cellCount * 2,
    draw(viewProj: Float32Array) {
      gl.useProgram(prog.program)
      gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)

      gl.enableVertexAttribArray(prog.aPosition)
      gl.vertexAttribPointer(prog.aPosition, 3, gl.FLOAT, false, stride, 0)
      gl.enableVertexAttribArray(prog.aColor)
      gl.vertexAttribPointer(prog.aColor, 4, gl.FLOAT, false, stride, 3 * 4)

      gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)

      // Translucent pass: keep depth-TEST on so water is occluded by ground
      // / structures above, but depth-WRITE off so multiple transparent
      // pixels at the same Z don't fight. Standard alpha blend.
      gl.enable(gl.DEPTH_TEST)
      gl.depthFunc(gl.LEQUAL)
      gl.depthMask(false)
      gl.enable(gl.BLEND)
      gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)
      gl.disable(gl.CULL_FACE)

      gl.drawElements(gl.TRIANGLES, indices.length, gl.UNSIGNED_SHORT, 0)

      gl.depthMask(true)
      gl.disable(gl.BLEND)
      gl.disableVertexAttribArray(prog.aPosition)
      gl.disableVertexAttribArray(prog.aColor)
    },
    dispose() {
      gl.deleteBuffer(vbo)
      gl.deleteBuffer(ibo)
      gl.deleteProgram(prog.program)
    },
  }
}

function lerp(a: number, b: number, u: number): number {
  return a + (b - a) * u
}
