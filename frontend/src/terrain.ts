// Custom terrain renderer that paints a heightmap colored by tileset FourCC.
// Built independently of the lib's ground/cliff/water shaders so it doesn't
// share state with War3MapViewer — which we no longer use; see the
// project-wc3-forge memory entry for the rationale.
//
// Drawn between viewer.startFrame() and viewer.render() so it lands in the
// depth buffer before unit instances, letting WebGL's depth test occlude
// models that are below ground or sticking out of cliffs.

import { flog } from './debuglog'

// Matches the JSON shape App.GetTerrain returns. Field names are snake_case
// because the Go side carries `json:"..."` struct tags (Wails honors those
// when marshaling — not the Go field name).
interface TerrainDTO {
  width: number
  height: number
  center_offset: [number, number]
  heights: number[]
  ground_tex: number[]
  tileset: string
  palette: string[]
  /** Real per-tile RGB (0..255), sampled from the WC3 tileset BLP/DDS in
   *  CASC via Terrain.slk. Same length as `palette`. */
  palette_colors: [number, number, number][]
  /** war3map.shd as base64 (Wails encodes []byte that way). Empty string
   *  when the map ships no shadow map. */
  shadow_map: string
  shadow_map_width: number
  shadow_map_height: number
}

// Convert a Go-side palette color triplet (0..255) to GL float (0..1).
// Go sources these by sampling the actual WC3 tileset DDS/BLP from CASC
// via Terrain.slk — same data path HiveWE uses.
function paletteColorToGL(rgb: [number, number, number]): [number, number, number] {
  return [rgb[0] / 255, rgb[1] / 255, rgb[2] / 255]
}

const VERT_SHADER = `
attribute vec3 a_position;
attribute vec3 a_color;
attribute vec2 a_shadowUV;
uniform mat4 u_viewProj;
varying vec3 v_color;
varying vec2 v_shadowUV;
void main() {
  v_color = a_color;
  v_shadowUV = a_shadowUV;
  gl_Position = u_viewProj * vec4(a_position, 1.0);
}
`.trim()

// Shadow map: single-channel texture sampled at the per-vertex shadow UV
// (computed at build time from each vertex's grid position). WC3's shadow
// bytes are effectively binary in practice — 0x00 = lit, 0x80+ = shadowed.
// With LINEAR filtering on the texture the edges get smooth interpolation,
// but the core values still drive the lit/shadowed branch.
const FRAG_SHADER = `
precision mediump float;
uniform sampler2D u_shadowTex;
uniform bool u_hasShadow;
varying vec3 v_color;
varying vec2 v_shadowUV;
void main() {
  vec3 col = v_color;
  if (u_hasShadow) {
    float shadow = texture2D(u_shadowTex, v_shadowUV).r;
    // Darken by up to 45% in fully-shadowed cells. Editor visibility
    // matters more than perfect WC3 mood lighting — we want the user to
    // still see what's under a shadow.
    col *= 1.0 - shadow * 0.45;
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
    throw new Error('terrain shader compile: ' + log)
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
    throw new Error('terrain program link: ' + log)
  }
  return {
    program,
    aPosition: gl.getAttribLocation(program, 'a_position'),
    aColor: gl.getAttribLocation(program, 'a_color'),
    aShadowUV: gl.getAttribLocation(program, 'a_shadowUV'),
    uViewProj: gl.getUniformLocation(program, 'u_viewProj')!,
    uShadowTex: gl.getUniformLocation(program, 'u_shadowTex')!,
    uHasShadow: gl.getUniformLocation(program, 'u_hasShadow')!,
  }
}

// Decode the base64 shadow map and upload as a single-channel GL texture.
// Returns null if the map shipped no shadow data. Uses GL_LUMINANCE for
// WebGL1 compatibility (gl.RED + GL_R8 needs WebGL2 / EXT_texture_rg).
function uploadShadowTexture(
  gl: WebGLRenderingContext, b64: string, w: number, h: number,
): WebGLTexture | null {
  if (!b64 || w <= 0 || h <= 0) return null
  // Wails serialises []byte as a base64 string with no padding tricks; the
  // browser atob produces a binary string we then read by char-code.
  const bin = atob(b64)
  if (bin.length !== w * h) {
    flog(`[terrain shadow] size mismatch (got ${bin.length}, want ${w}*${h}=${w * h})`)
    return null
  }
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)

  const tex = gl.createTexture()
  if (!tex) return null
  gl.bindTexture(gl.TEXTURE_2D, tex)
  // LUMINANCE: shader reads .r and gets the byte value (replicated across
  // R/G/B). Linear filter gives smooth shadow edges instead of pixelated
  // 4×4 cell blocks; clamp-to-edge avoids wrap-around at map boundaries.
  gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1)
  gl.texImage2D(gl.TEXTURE_2D, 0, gl.LUMINANCE, w, h, 0, gl.LUMINANCE, gl.UNSIGNED_BYTE, bytes)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
  return tex
}

export interface TerrainMesh {
  /** Draw using the given world→clip matrix (camera.viewProjectionMatrix). */
  draw(viewProj: Float32Array): void
  /** Release GL resources. */
  dispose(): void
}

// Build a heightfield mesh + uploaded buffers + shader program from a
// terrain DTO. Returns null if WebGL fails or the DTO is malformed.
//
// Coordinate system: w3e stores width × height VERTICES (not tiles) in a
// row-major grid. center_offset is the game-coord position of vertex (0,0)
// (typically the negative half-map-size so the map center lands at 0,0).
// Vertex (i, j) is at game-space (x, y, z) where:
//   x = centerOffsetX + i * 128
//   y = centerOffsetY + j * 128
//   z = heights[j*width + i]       (already in stud units; t.Z() did the math)
//
// Color per vertex comes from palette[groundTex[…]] hashed to HSL — a stand-in
// for real tileset BLP textures until those come online (deferred work, see
// project-wc3-forge memory).
export function buildTerrain(gl: WebGLRenderingContext, t: TerrainDTO): TerrainMesh | null {
  if (!t || !t.width || !t.height || !t.heights?.length) return null

  const W = t.width
  const H = t.height
  const N = W * H
  if (t.heights.length !== N) {
    flog(`[terrain] dim mismatch: W*H=${N}, heights=${t.heights.length}`)
    return null
  }

  if (N > 65535) {
    flog(`[terrain] too many vertices (${N} > 65535); large-map support is TODO`)
    return null
  }

  // Precompute per-palette-slot colors so the inner loop is a table lookup.
  // palette_colors comes from Go (sampled from real CASC tileset textures).
  // Fall back to a neutral gray if the Go side didn't supply colors (e.g.
  // when Terrain.slk lookup failed for one tile).
  const palCols = (t.palette_colors ?? []).map(paletteColorToGL)
  const defaultCol: [number, number, number] = [0.4, 0.4, 0.4]

  // Vertex layout: vec3 position + vec3 color + vec2 shadowUV, interleaved.
  // 8 floats per vertex. Shadow UV maps each vertex to its position in the
  // shadow texture's [0..1] space — vertex (i, j) → (i/(W-1), j/(H-1)).
  // With LINEAR filter on the shadow texture, the fragment shader gets a
  // smooth interpolation between the 4×4 shadow cells per tile.
  const verts = new Float32Array(N * 8)
  const cx = t.center_offset[0]
  const cy = t.center_offset[1]
  const invWm1 = 1 / Math.max(1, W - 1)
  const invHm1 = 1 / Math.max(1, H - 1)
  for (let j = 0; j < H; j++) {
    for (let i = 0; i < W; i++) {
      const k = j * W + i
      const o = k * 8
      verts[o + 0] = cx + i * 128
      verts[o + 1] = cy + j * 128
      verts[o + 2] = t.heights[k]
      const slot = t.ground_tex[k]
      const col = (slot >= 0 && slot < palCols.length) ? palCols[slot] : defaultCol
      verts[o + 3] = col[0]
      verts[o + 4] = col[1]
      verts[o + 5] = col[2]
      verts[o + 6] = i * invWm1
      verts[o + 7] = j * invHm1
    }
  }

  // Indices: 2 triangles per quad, (W-1) * (H-1) quads. CCW winding when
  // viewed from +Z so face-culling-friendly rendering renders ground correctly.
  const quads = (W - 1) * (H - 1)
  const indices = new Uint16Array(quads * 6)
  let idx = 0
  for (let j = 0; j < H - 1; j++) {
    for (let i = 0; i < W - 1; i++) {
      const tl = j * W + i
      const tr = tl + 1
      const bl = tl + W
      const br = bl + 1
      indices[idx++] = tl
      indices[idx++] = bl
      indices[idx++] = br
      indices[idx++] = tl
      indices[idx++] = br
      indices[idx++] = tr
    }
  }

  const vbo = gl.createBuffer()!
  gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
  gl.bufferData(gl.ARRAY_BUFFER, verts, gl.STATIC_DRAW)

  const ibo = gl.createBuffer()!
  gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
  gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, indices, gl.STATIC_DRAW)

  const prog = buildProgram(gl)
  const stride = 8 * 4

  const shadowTex = uploadShadowTexture(gl, t.shadow_map, t.shadow_map_width, t.shadow_map_height)
  if (shadowTex) {
    flog(`[terrain] shadow map ${t.shadow_map_width}×${t.shadow_map_height} uploaded`)
  }

  return {
    draw(viewProj: Float32Array) {
      gl.useProgram(prog.program)
      gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)

      gl.enableVertexAttribArray(prog.aPosition)
      gl.vertexAttribPointer(prog.aPosition, 3, gl.FLOAT, false, stride, 0)
      gl.enableVertexAttribArray(prog.aColor)
      gl.vertexAttribPointer(prog.aColor, 3, gl.FLOAT, false, stride, 3 * 4)
      if (prog.aShadowUV >= 0) {
        gl.enableVertexAttribArray(prog.aShadowUV)
        gl.vertexAttribPointer(prog.aShadowUV, 2, gl.FLOAT, false, stride, 6 * 4)
      }

      gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)
      if (shadowTex) {
        gl.activeTexture(gl.TEXTURE0)
        gl.bindTexture(gl.TEXTURE_2D, shadowTex)
        gl.uniform1i(prog.uShadowTex, 0)
        gl.uniform1i(prog.uHasShadow, 1)
      } else {
        gl.uniform1i(prog.uHasShadow, 0)
      }

      // Depth test on; depth write on; no blending — opaque ground.
      gl.enable(gl.DEPTH_TEST)
      gl.depthFunc(gl.LEQUAL)
      gl.depthMask(true)
      gl.disable(gl.BLEND)
      // No face culling — we don't know the source winding for sure and a
      // double-sided ground costs ~0 at this vertex count.
      gl.disable(gl.CULL_FACE)

      gl.drawElements(gl.TRIANGLES, indices.length, gl.UNSIGNED_SHORT, 0)

      // Unbind attributes so the next renderer (mdx-m3-viewer's scene.render)
      // doesn't see lingering state. The lib re-enables per shader anyway,
      // but this is cheap insurance.
      gl.disableVertexAttribArray(prog.aPosition)
      gl.disableVertexAttribArray(prog.aColor)
      if (prog.aShadowUV >= 0) gl.disableVertexAttribArray(prog.aShadowUV)
    },
    dispose() {
      gl.deleteBuffer(vbo)
      gl.deleteBuffer(ibo)
      if (shadowTex) gl.deleteTexture(shadowTex)
      gl.deleteProgram(prog.program)
    },
  }
}
