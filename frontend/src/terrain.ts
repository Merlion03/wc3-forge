// Textured terrain renderer.
//
// Per-cell tile selection (v1): each cell uses the tile texture chosen by
// its bottom-left corner's `ground_tex` palette index. Variation comes from
// the bottom-left corner's `ground_var` (low 5 bits of TextureDetails) —
// picks a sub-tile within the texture's 4×4 (non-extended) or 4×8 (extended)
// sub-tile atlas.
//
// HiveWE's terrain shader does up to 4-texture-per-cell alpha-blended
// rendering keyed on per-corner texture types, which gives blended tile-type
// boundaries. We render a single texture per cell — a v1 that still matches
// the user-visible model "this cell is grass / that cell is dirt" but without
// the blended transitions between them. (Blending is a stretch goal — see
// project memory.)
//
// Each palette tile that's actually used in the map becomes one draw call;
// the mesh for that palette draw covers ONLY the cells using that palette as
// their primary tile. Up to 16 palette entries → up to 16 draw calls per
// frame; in practice maps use 4–8.
//
// Texture loading uses the same `viewer.load(path, pathSolver)` pattern as
// water.ts. The asset-handler's BLP↔DDS swap covers Reforged-DDS-only
// installs when we request `.blp`, but we ask for `.dds` first because that's
// the primary format on every Reforged install we've seen.
//
// Drawn between viewer.startFrame() and viewer.render() so it lands in the
// depth buffer before unit instances. Same approach as the previous
// color-only renderer.

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
  ground_var: number[]
  tileset: string
  palette: string[]
  /** Real per-tile RGB (0..255), sampled from the WC3 tileset BLP/DDS in
   *  CASC via Terrain.slk. Used as a fallback color when the matching
   *  texture fails to load. Same length as `palette`. */
  palette_colors: [number, number, number][]
  /** Asset path stem (no extension) for each palette tile — JS appends
   *  `.dds`. Empty string when Terrain.slk lookup failed. */
  palette_textures: string[]
  /** war3map.shd as base64 (Wails encodes []byte that way). Empty string
   *  when the map ships no shadow map. */
  shadow_map: string
  shadow_map_width: number
  shadow_map_height: number
}

// Pick the sub-tile index (0..31 for extended, 0..15 for non-extended)
// matching HiveWE's `get_tile_variation`. Sub-tile k is at atlas coord
// (k % 4, k / 4) for non-extended (4×4 layout) or (k % 8, k / 8) for
// extended (8×4 layout) — see the UV-build code below for the actual math.
function pickSubTile(extended: boolean, variation: number): number {
  if (extended) {
    if (variation <= 15) return 16 + variation
    if (variation === 16) return 15
    return 0
  }
  if (variation === 0) return 0
  return 15
}

const VERT_SHADER = `
attribute vec3 a_position;
attribute vec2 a_uv;
attribute vec2 a_shadowUV;
uniform mat4 u_viewProj;
varying vec2 v_uv;
varying vec2 v_shadowUV;
void main() {
  v_uv = a_uv;
  v_shadowUV = a_shadowUV;
  gl_Position = u_viewProj * vec4(a_position, 1.0);
}
`.trim()

// Shadow map: single-channel texture sampled at the per-vertex shadow UV
// (computed at build time from each vertex's grid position). WC3's shadow
// bytes are effectively binary in practice — 0x00 = lit, 0x80+ = shadowed.
// With LINEAR filtering on the texture the edges get smooth interpolation,
// but the core values still drive the lit/shadowed branch.
//
// u_hasTexture controls fall-through to flat color when the palette texture
// failed to load (rare: missing asset or unusual format). u_fallbackColor is
// the per-palette average we computed Go-side from the DDS reference colors.
const FRAG_SHADER = `
precision mediump float;
uniform sampler2D u_tileTex;
uniform sampler2D u_shadowTex;
uniform bool u_hasTexture;
uniform bool u_hasShadow;
uniform vec3 u_fallbackColor;
varying vec2 v_uv;
varying vec2 v_shadowUV;
void main() {
  vec3 col = u_hasTexture ? texture2D(u_tileTex, v_uv).rgb : u_fallbackColor;
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
    aUV: gl.getAttribLocation(program, 'a_uv'),
    aShadowUV: gl.getAttribLocation(program, 'a_shadowUV'),
    uViewProj: gl.getUniformLocation(program, 'u_viewProj')!,
    uTileTex: gl.getUniformLocation(program, 'u_tileTex')!,
    uShadowTex: gl.getUniformLocation(program, 'u_shadowTex')!,
    uHasTexture: gl.getUniformLocation(program, 'u_hasTexture')!,
    uHasShadow: gl.getUniformLocation(program, 'u_hasShadow')!,
    uFallbackColor: gl.getUniformLocation(program, 'u_fallbackColor')!,
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

// One draw bucket per palette tile index: covers all cells where that
// palette is the primary tile.
interface TileBucket {
  paletteIdx: number
  vbo: WebGLBuffer
  ibo: WebGLBuffer
  /** Index data type: gl.UNSIGNED_SHORT or gl.UNSIGNED_INT (via the
   *  OES_element_index_uint extension). Used in drawElements. */
  indexType: number
  triCount: number
  // mdx-m3-viewer-managed texture. null when the tile load failed; we then
  // fall back to the per-palette average color uniform.
  glTex: WebGLTexture | null
  fallbackColor: [number, number, number]
}

export interface TerrainMesh {
  /** Draw using the given world→clip matrix (camera.viewProjectionMatrix). */
  draw(viewProj: Float32Array): void
  /** Release GL resources. */
  dispose(): void
  /** Number of draw calls (one per used palette tile). */
  drawCallCount: number
}

// Build a textured terrain mesh from the DTO. Async because we wait on
// viewer.load for each palette texture.
//
// Coordinate system: w3e stores width × height VERTICES (not tiles) in a
// row-major grid. center_offset is the game-coord position of vertex (0,0)
// (typically the negative half-map-size so the map center lands at 0,0).
// Vertex (i, j) is at game-space (x, y, z) where:
//   x = centerOffsetX + i * 128
//   y = centerOffsetY + j * 128
//   z = heights[j*width + i]       (FinalZ — includes cliff layer offset)
//
// Cells are indexed by their bottom-left corner (i, j) where 0 <= i < W-1,
// 0 <= j < H-1. The 4 corners of cell (i, j) are at vertex indices:
//   bl = j*W + i
//   br = bl + 1
//   tl = bl + W
//   tr = tl + 1
//
// Per-palette mesh: vertices are NOT shared between cells (each cell owns
// its own 4 verts) so adjacent cells using different palettes can have
// independent UVs without conflict. Index-buffer size = 6 indices/cell;
// cells with that palette as primary contribute one quad each.
export async function buildTerrain(
  gl: WebGLRenderingContext,
  viewer: any,
  pathSolver: any,
  t: TerrainDTO,
): Promise<TerrainMesh | null> {
  if (!t || !t.width || !t.height || !t.heights?.length) return null

  const W = t.width
  const H = t.height
  const N = W * H
  if (t.heights.length !== N) {
    flog(`[terrain] dim mismatch: W*H=${N}, heights=${t.heights.length}`)
    return null
  }

  // Each cell needs 4 unshared vertices for per-cell UVs. For a per-palette
  // bucket on a large map (Enfo: 162×162 ≈ 26000 cells, single palette
  // covering 17855 cells → 71420 verts), we need 32-bit indices via the
  // OES_element_index_uint WebGL1 extension. Modern browsers ship this
  // universally; we fall back to UNSIGNED_SHORT when not available (and
  // skip oversize buckets in that case).
  const uintExt = gl.getExtension('OES_element_index_uint')
  const has32Bit = !!uintExt

  const numCells = (W - 1) * (H - 1)
  if (numCells === 0) {
    flog(`[terrain] no cells: ${W}×${H}`)
    return null
  }

  // ---- Phase 1: pick per-cell palette index ----
  //
  // HiveWE picks the cell's textures by examining all 4 corners' ground_tex
  // values and emitting up to 4 textures with alpha-faded edge masks. We
  // simplify to one tile per cell via the bottom-left corner's palette idx.
  // This loses tile-type transitions but matches the per-cell variation
  // index naturally (variations are per-corner; bl's variation is canonical).
  const cellPalette = new Uint8Array(numCells)
  const cellVariation = new Uint8Array(numCells)
  // Histogram of palette usage so we know which buckets to even build.
  const paletteUseCount = new Array<number>(t.palette.length).fill(0)
  for (let j = 0; j < H - 1; j++) {
    for (let i = 0; i < W - 1; i++) {
      const bl = j * W + i
      const cell = j * (W - 1) + i
      const p = t.ground_tex[bl]
      cellPalette[cell] = p
      cellVariation[cell] = t.ground_var[bl] & 0x1F
      if (p < paletteUseCount.length) paletteUseCount[p]++
    }
  }

  // ---- Phase 2: load palette textures ----
  //
  // Issue each viewer.load in parallel. Reforged ships DDS only; we ask for
  // `.dds` first and let the assetHandler's BLP↔DDS swap cover the rare
  // legacy case where the texture file is actually a BLP.
  //
  // The loaded resource has `webglResource: WebGLTexture | null` and
  // `width`/`height`. Extended atlases are width = 2 * height (an extra 4×4
  // grid of variation tiles fused to the right of the main 4×4).
  type PaletteTex = {
    glTex: WebGLTexture | null
    extended: boolean
  }
  const paletteTextures: PaletteTex[] = new Array(t.palette.length)
  const palettePromises: Promise<void>[] = []
  for (let p = 0; p < t.palette.length; p++) {
    paletteTextures[p] = { glTex: null, extended: false }
    if (paletteUseCount[p] === 0) continue // not used by any cell, skip the load
    const stem = t.palette_textures?.[p]
    if (!stem) {
      flog(`[terrain] palette[${p}] (${t.palette[p]}) has no Terrain.slk path; using fallback color`)
      continue
    }
    const path = stem + '.dds'
    palettePromises.push(
      viewer.load(path, pathSolver).then((res: any) => {
        if (!res || !res.webglResource) {
          flog(`[terrain] palette[${p}] (${t.palette[p]}) failed to load: ${path}`)
          return
        }
        // Detect extended (variation half packed to the right).
        // Typical textures: 256×256 (non-extended), 512×256 (extended).
        const w = res.width as number
        const h = res.height as number
        const extended = w >= 2 * h
        paletteTextures[p] = { glTex: res.webglResource as WebGLTexture, extended }
        flog(`[terrain] palette[${p}] (${t.palette[p]}) loaded ${w}×${h} extended=${extended} from ${path}`)
      }).catch((e: unknown) => {
        flog(`[terrain] palette[${p}] (${t.palette[p]}) load threw:`,
          e instanceof Error ? e.message : String(e))
      }),
    )
  }
  await Promise.all(palettePromises)

  // ---- Phase 3: build per-palette buckets ----
  //
  // For each palette that's actually used: walk all cells, collect those
  // with this palette as primary, emit 4 verts + 6 indices per cell with UVs
  // that point into the chosen sub-tile slot in the (4×4) or (4×8) atlas.
  //
  // Sub-tile slot k maps to atlas pixel-coord origin:
  //   non-extended (4×4 layout): (k % 4) / 4, (k / 4) / 4
  //   extended    (8×4 layout): (k % 8) / 8, (k / 8) / 4
  // Sub-tile size in normalized UV = (1/4, 1/4) for non-extended,
  //                                 (1/8, 1/4) for extended.
  //
  // Per the BLP/DDS convention, UV (0,0) is top-left of the image; sub-tile
  // (col, row) sits at (col * size, row * size) in normalized UV. WebGL's
  // texture sampling is top-down by default for these decoded resources
  // (mdx-m3-viewer doesn't apply UNPACK_FLIP_Y for terrain textures).
  //
  // Within a cell, we map quad corners to sub-tile corners:
  //   bottom-left  vert → (u0, v1)  [bottom-left of the sub-tile in v-down]
  //   bottom-right vert → (u1, v1)
  //   top-left     vert → (u0, v0)
  //   top-right    vert → (u1, v0)
  // …matching the HiveWE convention `UV = vec2(vPos.x, 1 - vPos.y)`.

  const cx = t.center_offset[0]
  const cy = t.center_offset[1]
  const invWm1 = 1 / Math.max(1, W - 1)
  const invHm1 = 1 / Math.max(1, H - 1)

  const buckets: TileBucket[] = []
  for (let p = 0; p < t.palette.length; p++) {
    const usedCells = paletteUseCount[p]
    if (usedCells === 0) continue

    const ptx = paletteTextures[p]
    const extended = ptx.extended
    const subCols = extended ? 8 : 4
    const subRows = 4
    const dU = 1 / subCols
    const dV = 1 / subRows

    // Layout: 8 floats per vert (pos.xyz + uv.uv + shadowUV.uv + padding).
    // We use 7 floats actually — pos(3) + uv(2) + shadowUV(2) = 7.
    const vCount = usedCells * 4
    const need32 = vCount > 65535
    if (need32 && !has32Bit) {
      flog(`[terrain] palette[${p}] uses ${usedCells} cells (${vCount} verts) — needs uint32 indices but OES_element_index_uint missing, skipping`)
      continue
    }
    const verts = new Float32Array(vCount * 7)
    const indices = need32
      ? new Uint32Array(usedCells * 6)
      : new Uint16Array(usedCells * 6)
    const indexType = need32 ? gl.UNSIGNED_INT : gl.UNSIGNED_SHORT
    let vOff = 0
    let iOff = 0
    let baseVert = 0

    for (let j = 0; j < H - 1; j++) {
      for (let i = 0; i < W - 1; i++) {
        const cell = j * (W - 1) + i
        if (cellPalette[cell] !== p) continue

        const variation = cellVariation[cell]
        const subSlot = pickSubTile(extended, variation)
        // sub-tile column (0..subCols-1) and row (0..3)
        const sc = subSlot % subCols
        const sr = Math.floor(subSlot / subCols)
        const u0 = sc * dU
        const v0 = sr * dV
        const u1 = u0 + dU
        const v1 = v0 + dV

        // Inset the UVs by half a sub-texel to avoid bleeding from the
        // adjacent sub-tile during LINEAR filtering at the seams. Using
        // ~0.5% of the sub-tile width as inset — too little = visible
        // bleed, too much = duplicated edge artifacts.
        const inset = Math.min(dU, dV) * 0.01
        const uL = u0 + inset
        const uR = u1 - inset
        const vT = v0 + inset
        const vB = v1 - inset

        const bl = j * W + i
        const br = bl + 1
        const tl = bl + W
        const tr = tl + 1

        const xL = cx + i * 128
        const xR = cx + (i + 1) * 128
        const yB = cy + j * 128
        const yT = cy + (j + 1) * 128

        // Shadow UVs — sample the shadow texture at the vertex's grid
        // position (smooth bilinear). One float-pair per vertex.
        const sBLu = i * invWm1, sBLv = j * invHm1
        const sBRu = (i + 1) * invWm1, sBRv = sBLv
        const sTLu = sBLu, sTLv = (j + 1) * invHm1
        const sTRu = sBRu, sTRv = sTLv

        // Vert order: bl, br, tl, tr. Triangles: (bl, br, tr), (bl, tr, tl).
        // bottom-left vertex
        verts[vOff++] = xL; verts[vOff++] = yB; verts[vOff++] = t.heights[bl]
        verts[vOff++] = uL; verts[vOff++] = vB
        verts[vOff++] = sBLu; verts[vOff++] = sBLv
        // bottom-right vertex
        verts[vOff++] = xR; verts[vOff++] = yB; verts[vOff++] = t.heights[br]
        verts[vOff++] = uR; verts[vOff++] = vB
        verts[vOff++] = sBRu; verts[vOff++] = sBRv
        // top-left vertex
        verts[vOff++] = xL; verts[vOff++] = yT; verts[vOff++] = t.heights[tl]
        verts[vOff++] = uL; verts[vOff++] = vT
        verts[vOff++] = sTLu; verts[vOff++] = sTLv
        // top-right vertex
        verts[vOff++] = xR; verts[vOff++] = yT; verts[vOff++] = t.heights[tr]
        verts[vOff++] = uR; verts[vOff++] = vT
        verts[vOff++] = sTRu; verts[vOff++] = sTRv

        // Triangles. CCW when viewed from +Z (above).
        indices[iOff++] = baseVert + 0
        indices[iOff++] = baseVert + 1
        indices[iOff++] = baseVert + 3
        indices[iOff++] = baseVert + 0
        indices[iOff++] = baseVert + 3
        indices[iOff++] = baseVert + 2
        baseVert += 4
      }
    }

    const vbo = gl.createBuffer()!
    gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
    gl.bufferData(gl.ARRAY_BUFFER, verts, gl.STATIC_DRAW)
    const ibo = gl.createBuffer()!
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
    gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, indices, gl.STATIC_DRAW)

    const fb = t.palette_colors?.[p]
    const fallbackColor: [number, number, number] = fb
      ? [fb[0] / 255, fb[1] / 255, fb[2] / 255]
      : [0.4, 0.4, 0.4]

    buckets.push({
      paletteIdx: p,
      vbo,
      ibo,
      indexType,
      triCount: usedCells * 2,
      glTex: ptx.glTex,
      fallbackColor,
    })
  }

  if (buckets.length === 0) {
    flog('[terrain] no buckets built (every palette had zero cells?)')
    return null
  }

  const prog = buildProgram(gl)
  const stride = 7 * 4

  const shadowTex = uploadShadowTexture(gl, t.shadow_map, t.shadow_map_width, t.shadow_map_height)
  if (shadowTex) {
    flog(`[terrain] shadow map ${t.shadow_map_width}×${t.shadow_map_height} uploaded`)
  }

  // Configure each loaded palette texture for CLAMP_TO_EDGE wrap so the
  // sub-tile inset doesn't bleed across the full-texture boundary (the
  // viewer's defaults are REPEAT which is wrong for atlas sampling).
  for (const b of buckets) {
    if (!b.glTex) continue
    gl.bindTexture(gl.TEXTURE_2D, b.glTex)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
    // LINEAR_MIPMAP_LINEAR would be nicer, but viewer.load already generated
    // mipmaps for BLP/DDS; setting LINEAR-only here avoids mipmap-bleed
    // between sub-tiles at low zoom (mip levels of the atlas average across
    // sub-tile boundaries, producing a smudged look). Trade-off: distant
    // terrain looks slightly aliased.
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
  }
  gl.bindTexture(gl.TEXTURE_2D, null)

  flog(`[terrain] built ${buckets.length} palette buckets covering ${numCells} cells`)

  return {
    drawCallCount: buckets.length,
    draw(viewProj: Float32Array) {
      gl.useProgram(prog.program)

      gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)

      if (shadowTex) {
        gl.activeTexture(gl.TEXTURE1)
        gl.bindTexture(gl.TEXTURE_2D, shadowTex)
        gl.uniform1i(prog.uShadowTex, 1)
        gl.uniform1i(prog.uHasShadow, 1)
      } else {
        gl.uniform1i(prog.uHasShadow, 0)
      }

      // Depth test on; depth write on; no blending — opaque ground.
      gl.enable(gl.DEPTH_TEST)
      gl.depthFunc(gl.LEQUAL)
      gl.depthMask(true)
      gl.disable(gl.BLEND)
      gl.disable(gl.CULL_FACE)

      for (const b of buckets) {
        gl.bindBuffer(gl.ARRAY_BUFFER, b.vbo)
        gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, b.ibo)

        gl.enableVertexAttribArray(prog.aPosition)
        gl.vertexAttribPointer(prog.aPosition, 3, gl.FLOAT, false, stride, 0)
        gl.enableVertexAttribArray(prog.aUV)
        gl.vertexAttribPointer(prog.aUV, 2, gl.FLOAT, false, stride, 3 * 4)
        if (prog.aShadowUV >= 0) {
          gl.enableVertexAttribArray(prog.aShadowUV)
          gl.vertexAttribPointer(prog.aShadowUV, 2, gl.FLOAT, false, stride, 5 * 4)
        }

        if (b.glTex) {
          gl.activeTexture(gl.TEXTURE0)
          gl.bindTexture(gl.TEXTURE_2D, b.glTex)
          gl.uniform1i(prog.uTileTex, 0)
          gl.uniform1i(prog.uHasTexture, 1)
        } else {
          gl.uniform1i(prog.uHasTexture, 0)
        }
        gl.uniform3f(prog.uFallbackColor, b.fallbackColor[0], b.fallbackColor[1], b.fallbackColor[2])

        gl.drawElements(gl.TRIANGLES, b.triCount * 3, b.indexType, 0)
      }

      gl.disableVertexAttribArray(prog.aPosition)
      gl.disableVertexAttribArray(prog.aUV)
      if (prog.aShadowUV >= 0) gl.disableVertexAttribArray(prog.aShadowUV)
    },
    dispose() {
      for (const b of buckets) {
        gl.deleteBuffer(b.vbo)
        gl.deleteBuffer(b.ibo)
        // Don't delete b.glTex — owned by viewer.resourceMap, reusable
        // across map loads.
      }
      if (shadowTex) gl.deleteTexture(shadowTex)
      gl.deleteProgram(prog.program)
    },
  }
}
