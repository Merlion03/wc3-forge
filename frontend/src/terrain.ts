// Textured terrain renderer with 4-corner alpha blending (HiveWE-style).
//
// Each cell can show up to 4 distinct tile textures (one per corner). The
// corner's ground_tex picks which palette tile that corner "owns"; weights
// fall off bilinearly across the cell so adjacent cells with different
// primary tiles blend with feathered edges instead of a hard line.
//
// Single draw call for the whole terrain. To get there in WebGL1 (no
// sampler2DArray, no dynamic indexing into sampler arrays), all loaded
// palette textures are pre-composited into a single vertical-strip atlas
// (one palette per row, row height = 256, atlas width = max palette width)
// via FBO-blit at build time. The frag shader then samples that one atlas
// 4 times — once per cell corner — at offsets keyed by per-corner palette
// layer index and per-corner sub-tile slot.
//
// Extended atlases (width = 2×height, 8×4 sub-tile layout) live alongside
// non-extended (4×4) palettes in the same atlas: extended palettes occupy
// the full atlas width, non-extended only the left half. The per-palette
// `extended` flag rides along as a per-corner shader uniform-via-attribute
// (packed into the layer-index attribute's fractional part — see usage).
//
// Drawn between viewer.startFrame() and viewer.render() so it lands in the
// depth buffer before unit instances.

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
  palette_colors: [number, number, number][]
  palette_textures: string[]
  shadow_map: string
  shadow_map_width: number
  shadow_map_height: number
}

// Mirrors HiveWE's `get_tile_variation`: the per-corner variation byte
// selects a sub-tile slot inside the texture's 4×4 (non-extended) or 8×4
// (extended) sub-tile layout.
function pickSubTile(extended: boolean, variation: number): number {
  if (extended) {
    if (variation <= 15) return 16 + variation
    if (variation === 16) return 15
    return 0
  }
  if (variation === 0) return 0
  return 15
}

// Per-cell vertex shader inputs:
//   a_position    — world XYZ
//   a_shadowUV    — shadow-map sample coord (per vertex)
//   a_cellUV      — cell-local UV (0..1), bilinear weight basis
//   a_layers      — palette layer index for each of the 4 corners (TL, TR, BR, BL)
//   a_subUV0..3   — pre-computed (atlas_u0, atlas_v0) sub-tile origin for each
//                   corner's chosen palette + sub-tile slot. Half is enough
//                   because subTileSize is constant per palette and packed
//                   into a_layers (see below).
//   a_subSize     — (sub_tile_width, sub_tile_height) in atlas UV for each
//                   of the 4 corners, packed as 2 vec4s (corners 0,1 and 2,3
//                   share the attribute).
//
// All 4 verts of a cell carry the same a_layers, a_subUV*, a_subSize values
// — only a_position, a_shadowUV, a_cellUV vary per vertex.
const VERT_SHADER = `
attribute vec3 a_position;
attribute vec2 a_shadowUV;
attribute vec2 a_cellUV;
attribute vec4 a_subUV01;   // TL.uv, TR.uv
attribute vec4 a_subUV23;   // BR.uv, BL.uv
attribute vec4 a_subSize01; // TL.size, TR.size
attribute vec4 a_subSize23; // BR.size, BL.size
attribute vec4 a_rowV;      // per-corner atlas row start (v coord in atlas, 0..1)
uniform mat4 u_viewProj;
varying vec2 v_shadowUV;
varying vec2 v_cellUV;
varying vec4 v_subUV01;
varying vec4 v_subUV23;
varying vec4 v_subSize01;
varying vec4 v_subSize23;
varying vec4 v_rowV;
void main() {
  v_shadowUV = a_shadowUV;
  v_cellUV = a_cellUV;
  v_subUV01 = a_subUV01;
  v_subUV23 = a_subUV23;
  v_subSize01 = a_subSize01;
  v_subSize23 = a_subSize23;
  v_rowV = a_rowV;
  gl_Position = u_viewProj * vec4(a_position, 1.0);
}
`.trim()

// Frag shader: bilinear weights from cellUV, sample atlas 4 times.
// Corner order (matches HiveWE 4-corner conventions):
//   0 = TL (u=0, v=1)  weight = (1-u) * v
//   1 = TR (u=1, v=1)  weight = u     * v
//   2 = BR (u=1, v=0)  weight = u     * (1-v)
//   3 = BL (u=0, v=0)  weight = (1-u) * (1-v)
// cellUV is (0,0) at BL, (1,1) at TR — standard quad UV with v-up.
//
// Each corner's atlas sample = subUV + cellUV_localized * subSize. The
// localization flips cellUV per corner so each corner's "own" texel sits at
// the appropriate sub-tile interior coord.
const FRAG_SHADER = `
precision mediump float;
uniform sampler2D u_atlas;
uniform sampler2D u_shadowTex;
uniform bool u_hasShadow;
varying vec2 v_shadowUV;
varying vec2 v_cellUV;
varying vec4 v_subUV01;
varying vec4 v_subUV23;
varying vec4 v_subSize01;
varying vec4 v_subSize23;
varying vec4 v_rowV;
void main() {
  float u = v_cellUV.x;
  float v = v_cellUV.y;
  // Bilinear weights (must sum to 1).
  float wTL = (1.0 - u) * v;
  float wTR = u * v;
  float wBR = u * (1.0 - v);
  float wBL = (1.0 - u) * (1.0 - v);

  // Per-corner atlas sample coord: (subOrigin.x + cellUV.x * subSize.x,
  // rowV + (1 - cellUV.y) * subSize.y).  The v-flip matches HiveWE's
  // "UV = vec2(vPos.x, 1 - vPos.y)" sub-tile orientation (image y-down,
  // cellUV v-up). The atlas row "rowV" is the bottom of the palette's
  // strip in the atlas — sub-tile slot's vertical offset (subUV.y) is
  // already baked into the per-corner subUV value, so rowV adds the
  // palette base only.
  vec2 uvTL = vec2(v_subUV01.x + u * v_subSize01.x,
                   v_subUV01.y + (1.0 - v) * v_subSize01.y);
  vec2 uvTR = vec2(v_subUV01.z + u * v_subSize01.z,
                   v_subUV01.w + (1.0 - v) * v_subSize01.w);
  vec2 uvBR = vec2(v_subUV23.x + u * v_subSize23.x,
                   v_subUV23.y + (1.0 - v) * v_subSize23.y);
  vec2 uvBL = vec2(v_subUV23.z + u * v_subSize23.z,
                   v_subUV23.w + (1.0 - v) * v_subSize23.w);

  vec4 cTL = texture2D(u_atlas, uvTL);
  vec4 cTR = texture2D(u_atlas, uvTR);
  vec4 cBR = texture2D(u_atlas, uvBR);
  vec4 cBL = texture2D(u_atlas, uvBL);

  vec3 col = cTL.rgb * wTL + cTR.rgb * wTR + cBR.rgb * wBR + cBL.rgb * wBL;

  if (u_hasShadow) {
    float shadow = texture2D(u_shadowTex, v_shadowUV).r;
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
    aShadowUV: gl.getAttribLocation(program, 'a_shadowUV'),
    aCellUV: gl.getAttribLocation(program, 'a_cellUV'),
    aSubUV01: gl.getAttribLocation(program, 'a_subUV01'),
    aSubUV23: gl.getAttribLocation(program, 'a_subUV23'),
    aSubSize01: gl.getAttribLocation(program, 'a_subSize01'),
    aSubSize23: gl.getAttribLocation(program, 'a_subSize23'),
    aRowV: gl.getAttribLocation(program, 'a_rowV'),
    uViewProj: gl.getUniformLocation(program, 'u_viewProj')!,
    uAtlas: gl.getUniformLocation(program, 'u_atlas')!,
    uShadowTex: gl.getUniformLocation(program, 'u_shadowTex')!,
    uHasShadow: gl.getUniformLocation(program, 'u_hasShadow')!,
  }
}

// Decode the base64 shadow map and upload as a single-channel GL texture.
function uploadShadowTexture(
  gl: WebGLRenderingContext, b64: string, w: number, h: number,
): WebGLTexture | null {
  if (!b64 || w <= 0 || h <= 0) return null
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
  gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1)
  gl.texImage2D(gl.TEXTURE_2D, 0, gl.LUMINANCE, w, h, 0, gl.LUMINANCE, gl.UNSIGNED_BYTE, bytes)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
  return tex
}

// Composite each loaded palette texture into a single vertical-strip atlas
// via FBO-blit. Atlas layout: width = max palette width (256 or 512),
// height = numPalettes * 256, palette p occupies row [p*256 .. (p+1)*256).
//
// Extended palettes (512×256) fill the row width; non-extended (256×256)
// occupy the left half and the right half is left clear (sub-tile picks for
// non-extended palettes never index beyond u < 0.5 — pickSubTile only
// returns slots 0..15 there, and the per-corner subUV computed at build time
// stays inside that half).
//
// Returns the atlas texture handle plus per-palette metadata the geometry
// build step needs to map (paletteIdx, subSlot) → atlas UV.
interface PaletteRow {
  extended: boolean
  loaded: boolean
  fallbackColor: [number, number, number]
}
function buildAtlas(
  gl: WebGLRenderingContext,
  palettes: { glTex: WebGLTexture | null; extended: boolean; width: number; height: number; fallback: [number, number, number] }[],
): { atlas: WebGLTexture; atlasW: number; atlasH: number; rows: PaletteRow[] } | null {
  const numPalettes = palettes.length
  if (numPalettes === 0) return null

  // Atlas width = widest palette (256 for non-extended-only, 512 if any
  // extended). Atlas height = numPalettes * 256.
  let atlasW = 256
  for (const p of palettes) {
    if (p.glTex && p.width > atlasW) atlasW = p.width
  }
  const rowH = 256
  const atlasH = numPalettes * rowH

  // Round atlas dims up to power-of-two? Not required for non-mip CLAMP
  // textures in WebGL1, but min/mag filtering is more well-defined. We're
  // already POT (256/512 width, num*256 height — only POT when numPalettes
  // is itself a power of two, but the WebGL1 NPOT-with-CLAMP path supports
  // LINEAR sampling fine).

  const atlas = gl.createTexture()
  if (!atlas) return null
  gl.bindTexture(gl.TEXTURE_2D, atlas)
  gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, atlasW, atlasH, 0, gl.RGBA, gl.UNSIGNED_BYTE, null)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)

  // FBO with the atlas as color attachment; we draw each palette into its
  // row via a fullscreen-style textured quad. Viewport is set to the row's
  // sub-rect of the atlas, so the quad covers exactly that row.
  const fbo = gl.createFramebuffer()
  gl.bindFramebuffer(gl.FRAMEBUFFER, fbo)
  gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, atlas, 0)
  const fbStatus = gl.checkFramebufferStatus(gl.FRAMEBUFFER)
  if (fbStatus !== gl.FRAMEBUFFER_COMPLETE) {
    flog(`[terrain atlas] FBO incomplete: 0x${fbStatus.toString(16)}`)
    gl.bindFramebuffer(gl.FRAMEBUFFER, null)
    gl.deleteFramebuffer(fbo)
    gl.deleteTexture(atlas)
    return null
  }

  // Build a tiny throwaway program: textured quad (positions in clip space,
  // UVs 0..1) sampling u_src. We use it once per palette row, then dispose.
  const QV = `
attribute vec2 a_p;
attribute vec2 a_uv;
varying vec2 v_uv;
void main() {
  v_uv = a_uv;
  gl_Position = vec4(a_p, 0.0, 1.0);
}
`.trim()
  const QF = `
precision mediump float;
uniform sampler2D u_src;
varying vec2 v_uv;
void main() {
  gl_FragColor = texture2D(u_src, v_uv);
}
`.trim()
  const vs = compileShader(gl, gl.VERTEX_SHADER, QV)
  const fs = compileShader(gl, gl.FRAGMENT_SHADER, QF)
  const prog = gl.createProgram()!
  gl.attachShader(prog, vs)
  gl.attachShader(prog, fs)
  gl.linkProgram(prog)
  gl.useProgram(prog)
  const aP = gl.getAttribLocation(prog, 'a_p')
  const aUV = gl.getAttribLocation(prog, 'a_uv')
  const uSrc = gl.getUniformLocation(prog, 'u_src')!

  const quadVbo = gl.createBuffer()
  gl.bindBuffer(gl.ARRAY_BUFFER, quadVbo)
  // Quad covering clip-space [-1..1] in both axes. UV (0,0) at bottom-left,
  // (1,1) at top-right. The blit will be drawn into a viewport rect that
  // selects the destination row inside the atlas.
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([
    -1, -1, 0, 0,
     1, -1, 1, 0,
    -1,  1, 0, 1,
     1,  1, 1, 1,
  ]), gl.STATIC_DRAW)

  // Some lib state is globally enabled by mdx-m3-viewer at boot — most
  // importantly SCISSOR_TEST (viewer.js line 102), with a scissor rect
  // pinned to the canvas size by scene.render(). That clips our FBO writes
  // to the canvas region (e.g. 768×768) and silently drops writes outside
  // it — the cause of mysteriously-empty atlas rows when atlasH > canvasH.
  // Disable scissor for the duration of the blit; the caller re-enables
  // via its own state setup (terrain.draw doesn't toggle it, but
  // viewer.update/render will set the scissor box again next frame).
  gl.disable(gl.SCISSOR_TEST)
  gl.disable(gl.BLEND)
  gl.disable(gl.DEPTH_TEST)
  gl.disable(gl.CULL_FACE)

  const rows: PaletteRow[] = new Array(numPalettes)
  for (let p = 0; p < numPalettes; p++) {
    const pal = palettes[p]
    rows[p] = { extended: pal.extended, loaded: !!pal.glTex, fallbackColor: pal.fallback }
    if (!pal.glTex) continue

    // Destination viewport rect in the atlas for palette p. v=0 is atlas
    // bottom; we lay palettes from top→bottom so palette 0 sits at the
    // visually-top of the atlas. But sampling convention only requires that
    // we agree with ourselves — pick top-to-bottom so row v-coord =
    // (atlasH - (p+1)*rowH) / atlasH .. (atlasH - p*rowH) / atlasH.
    //
    // For non-extended palettes (width 256), only fill the left half of the
    // atlas row. The right half is undefined memory; non-extended sub-tile
    // UVs never reach into u >= 0.5, so it doesn't matter.
    const dstW = pal.extended ? atlasW : Math.min(pal.width, atlasW)
    const dstH = rowH
    const dstX = 0
    const dstY = atlasH - (p + 1) * rowH

    gl.viewport(dstX, dstY, dstW, dstH)
    gl.activeTexture(gl.TEXTURE0)
    gl.bindTexture(gl.TEXTURE_2D, pal.glTex)
    // The source palette texture was uploaded by mdx-m3-viewer with
    // its own filter/wrap state. We need CLAMP + LINEAR for a clean copy.
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
    gl.uniform1i(uSrc, 0)

    gl.bindBuffer(gl.ARRAY_BUFFER, quadVbo)
    gl.enableVertexAttribArray(aP)
    gl.vertexAttribPointer(aP, 2, gl.FLOAT, false, 16, 0)
    gl.enableVertexAttribArray(aUV)
    gl.vertexAttribPointer(aUV, 2, gl.FLOAT, false, 16, 8)
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4)
  }

  // Re-enable scissor test for the viewer's per-frame pipeline.
  gl.enable(gl.SCISSOR_TEST)
  // Cleanup of the throwaway program. The atlas + rows live on.
  gl.bindFramebuffer(gl.FRAMEBUFFER, null)
  gl.deleteFramebuffer(fbo)
  gl.deleteBuffer(quadVbo)
  gl.deleteProgram(prog)
  gl.deleteShader(vs)
  gl.deleteShader(fs)

  return { atlas, atlasW, atlasH, rows }
}

export interface TerrainMesh {
  draw(viewProj: Float32Array): void
  dispose(): void
  drawCallCount: number
}

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

  const numCells = (W - 1) * (H - 1)
  if (numCells === 0) {
    flog(`[terrain] no cells: ${W}×${H}`)
    return null
  }

  // ---- Load palette textures (parallel) ----
  type PaletteTex = {
    glTex: WebGLTexture | null
    extended: boolean
    width: number
    height: number
    fallback: [number, number, number]
  }
  const paletteTextures: PaletteTex[] = new Array(t.palette.length)
  const palettePromises: Promise<void>[] = []
  for (let p = 0; p < t.palette.length; p++) {
    const fb = t.palette_colors?.[p]
    const fallback: [number, number, number] = fb
      ? [fb[0] / 255, fb[1] / 255, fb[2] / 255]
      : [0.4, 0.4, 0.4]
    paletteTextures[p] = { glTex: null, extended: false, width: 256, height: 256, fallback }
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
        const w = res.width as number
        const h = res.height as number
        const extended = w >= 2 * h
        paletteTextures[p] = {
          glTex: res.webglResource as WebGLTexture,
          extended, width: w, height: h, fallback,
        }
        flog(`[terrain] palette[${p}] (${t.palette[p]}) loaded ${w}×${h} extended=${extended} from ${path}`)
      }).catch((e: unknown) => {
        flog(`[terrain] palette[${p}] (${t.palette[p]}) load threw:`,
          e instanceof Error ? e.message : String(e))
      }),
    )
  }
  await Promise.all(palettePromises)

  // ---- Composite atlas ----
  const atlasInfo = buildAtlas(gl, paletteTextures)
  if (!atlasInfo) {
    flog('[terrain] atlas build failed; aborting')
    return null
  }
  const { atlas, atlasW, atlasH, rows } = atlasInfo
  const numPalettes = rows.length

  // ---- Pre-compute per-palette atlas mapping helpers ----
  //
  // For palette p, sub-tile slot k, the sub-tile origin (in normalized atlas
  // UV) is (atlasU0, atlasV0) and the sub-tile size in normalized atlas UV
  // is (subW, subH). Both depend on whether the palette is extended.
  //
  // Layout within a row (palette p occupies rows [(numPalettes-p-1)*256,
  // (numPalettes-p)*256] in pixels):
  //   non-extended: 4 cols × 4 rows of 64×64 sub-tiles in the left half
  //                 → col c=0..3 at u = c * (1/8), width = (1/8) of atlas
  //                 → row r=0..3 at v_within_row = r * 0.25 (top→bottom in
  //                   image y-down, so r=0 is the TOP of the sub-tile grid)
  //   extended:     8 cols × 4 rows of 64×64 sub-tiles spanning full row
  //                 → col c=0..7 at u = c * (1/8), width = (1/8)
  //                 → same row layout
  //
  // Edge inset: tighten by 1% of sub-tile size to suppress LINEAR-filter
  // bleed from adjacent sub-tiles. The atlas's row boundaries are also a
  // potential bleed source (palette p+1's first row bleeds up into
  // palette p's last row); inset on the row v handles that too.
  function subTileAtlasRect(p: number, slot: number): {
    u0: number; v0: number; sw: number; sh: number; rowV: number;
  } {
    const ext = rows[p].extended
    const subCols = ext ? 8 : 4
    const subRows = 4
    // In pixels within the row (image y-down, row top at pixel 0):
    const colW = 64 // 256/4 or 512/8 = 64
    const rowH = 64 // 256/4
    const sc = slot % subCols
    const sr = Math.floor(slot / subCols)
    const xPx = sc * colW
    const yPxTop = sr * rowH // top of the sub-tile within the row (image y-down)

    // The row sits at atlas pixel rows [palY .. palY + 256), where palY is
    // the row's BOTTOM in atlas-pixel-y (since we used dstY = atlasH -
    // (p+1)*256 above — yes, atlas pixel y goes UP from bottom in GL coords,
    // so the row spans GL y from (atlasH-(p+1)*256) up to (atlasH-p*256)).
    //
    // The blit copied the source texture with UV (0,0) at the destination
    // viewport's bottom-left. That means the image's "y=0 pixel" (top of
    // the source) ended up at GL pixel y = atlasH - p*256 - 1 (top of the
    // destination row). And image's "y=255 pixel" (bottom of source) at
    // GL pixel y = atlasH - (p+1)*256.
    //
    // So sub-tile (sc, sr) in image-y-down coordinates occupies image rows
    // [sr*64 .. (sr+1)*64), which maps to GL pixel-y rows:
    //   GL y_top    = atlasH - p*256 - sr*64 - 1
    //   GL y_bottom = atlasH - p*256 - (sr+1)*64
    //
    // Atlas UV v = GL_pixel_y / atlasH. Sub-tile origin (bottom-left in UV
    // coords with v-up): u = xPx / atlasW, v = (atlasH - p*256 -
    // (sr+1)*64) / atlasH. Sub-tile size: sw = colW/atlasW, sh = rowH/atlasH.
    // Palette p occupies atlas v ∈ [base_v .. base_v + 256/atlasH], where
    // base_v = (atlasH - (p+1)*256) / atlasH (palette 0 is at the TOP of
    // the atlas in v-up GL UV-space, so base_v=1-256/atlasH for p=0).
    // Within that palette's strip, source UV v is "v-down" in image terms:
    // sub-tile row sr=0 is the top of the image, occupying source UV v
    // [0..0.25]. Translated to atlas UV (which is v-up but the blit
    // preserves the source's v-down orientation across the strip):
    //   atlas_v(image_top_of_subtile) = base_v + sr * 256/atlasH * 0.25
    //                                 = base_v + sr * 64/atlasH
    const baseV = (atlasH - (p + 1) * 256) / atlasH
    const u0 = xPx / atlasW
    const v0 = baseV + sr * (rowH / atlasH)
    const sw = colW / atlasW
    const sh = rowH / atlasH
    // Inset by 1% of sub-tile to suppress LINEAR bleed from neighbors.
    const insetU = sw * 0.01
    const insetV = sh * 0.01
    return {
      u0: u0 + insetU,
      v0: v0 + insetV,
      sw: sw - 2 * insetU,
      sh: sh - 2 * insetV,
      rowV: baseV,
    }
  }

  // ---- Build whole-map mesh (single VBO/IBO) ----
  //
  // Per-vertex: pos(3) + shadowUV(2) + cellUV(2) + subUV01(4) + subUV23(4)
  //           + subSize01(4) + subSize23(4) + rowV(4) = 27 floats.
  //
  // 4 verts per cell × numCells cells. uint32 indices when vCount > 65535
  // (needs OES_element_index_uint, ships universally in modern browsers).
  const uintExt = gl.getExtension('OES_element_index_uint')
  const has32Bit = !!uintExt

  const vCount = numCells * 4
  const need32 = vCount > 65535
  if (need32 && !has32Bit) {
    flog(`[terrain] needs uint32 indices (${vCount} verts) but OES_element_index_uint missing`)
    return null
  }
  const STRIDE_F = 27
  const verts = new Float32Array(vCount * STRIDE_F)
  const indices = need32
    ? new Uint32Array(numCells * 6)
    : new Uint16Array(numCells * 6)
  const indexType = need32 ? gl.UNSIGNED_INT : gl.UNSIGNED_SHORT

  const cx = t.center_offset[0]
  const cy = t.center_offset[1]
  const invWm1 = 1 / Math.max(1, W - 1)
  const invHm1 = 1 / Math.max(1, H - 1)

  let vOff = 0
  let iOff = 0
  let baseVert = 0

  // Per-corner data assembled per cell. Corner order TL, TR, BR, BL matches
  // the bilinear-weight layout in the frag shader.
  for (let j = 0; j < H - 1; j++) {
    for (let i = 0; i < W - 1; i++) {
      const bl = j * W + i
      const br = bl + 1
      const tl = bl + W
      const tr = tl + 1

      const xL = cx + i * 128
      const xR = cx + (i + 1) * 128
      const yB = cy + j * 128
      const yT = cy + (j + 1) * 128

      // 4 corners' palette indices.
      // ground_tex[v] is the palette index for vertex v (NOT cell). The
      // 4 corners of cell (i, j) live at vertex indices bl, br, tl, tr.
      // HiveWE's convention: each corner's "owned" tile is what
      // ground_tex[that-corner-vertex] points to. The bilinear weights
      // then fade between the 4 corners' tiles across the cell.
      const pTL = Math.min(t.ground_tex[tl] | 0, numPalettes - 1)
      const pTR = Math.min(t.ground_tex[tr] | 0, numPalettes - 1)
      const pBR = Math.min(t.ground_tex[br] | 0, numPalettes - 1)
      const pBL = Math.min(t.ground_tex[bl] | 0, numPalettes - 1)

      // Each corner's own variation chooses its sub-tile slot within its
      // own palette. Variations are per-corner (ground_var array indexed by
      // vertex), independent for each owned tile.
      const vTL = t.ground_var[tl] & 0x1F
      const vTR = t.ground_var[tr] & 0x1F
      const vBR = t.ground_var[br] & 0x1F
      const vBL = t.ground_var[bl] & 0x1F

      const slotTL = pickSubTile(rows[pTL].extended, vTL)
      const slotTR = pickSubTile(rows[pTR].extended, vTR)
      const slotBR = pickSubTile(rows[pBR].extended, vBR)
      const slotBL = pickSubTile(rows[pBL].extended, vBL)

      const rTL = subTileAtlasRect(pTL, slotTL)
      const rTR = subTileAtlasRect(pTR, slotTR)
      const rBR = subTileAtlasRect(pBR, slotBR)
      const rBL = subTileAtlasRect(pBL, slotBL)

      // Shadow UVs.
      const sBLu = i * invWm1, sBLv = j * invHm1
      const sBRu = (i + 1) * invWm1, sBRv = sBLv
      const sTLu = sBLu, sTLv = (j + 1) * invHm1
      const sTRu = sBRu, sTRv = sTLv

      // Pre-pack the 4 per-corner sub-tile attributes (same across all 4
      // verts of this cell).
      const subUV01x = rTL.u0, subUV01y = rTL.v0, subUV01z = rTR.u0, subUV01w = rTR.v0
      const subUV23x = rBR.u0, subUV23y = rBR.v0, subUV23z = rBL.u0, subUV23w = rBL.v0
      const subSz01x = rTL.sw, subSz01y = rTL.sh, subSz01z = rTR.sw, subSz01w = rTR.sh
      const subSz23x = rBR.sw, subSz23y = rBR.sh, subSz23z = rBL.sw, subSz23w = rBL.sh
      // rowV is unused in the frag shader currently (sub-tile origin already
      // includes the row's vertical offset) — keep the attribute reserved
      // for future per-palette work (e.g. cliff cover textures).
      const rowVx = rTL.rowV, rowVy = rTR.rowV, rowVz = rBR.rowV, rowVw = rBL.rowV

      // Vert layout helper: write one vertex's STRIDE_F floats.
      // cellUV per vertex: bl=(0,0), br=(1,0), tl=(0,1), tr=(1,1).
      function writeVert(
        x: number, y: number, z: number,
        cellU: number, cellV: number,
        shU: number, shV: number,
      ) {
        verts[vOff++] = x; verts[vOff++] = y; verts[vOff++] = z
        verts[vOff++] = shU; verts[vOff++] = shV
        verts[vOff++] = cellU; verts[vOff++] = cellV
        verts[vOff++] = subUV01x; verts[vOff++] = subUV01y; verts[vOff++] = subUV01z; verts[vOff++] = subUV01w
        verts[vOff++] = subUV23x; verts[vOff++] = subUV23y; verts[vOff++] = subUV23z; verts[vOff++] = subUV23w
        verts[vOff++] = subSz01x; verts[vOff++] = subSz01y; verts[vOff++] = subSz01z; verts[vOff++] = subSz01w
        verts[vOff++] = subSz23x; verts[vOff++] = subSz23y; verts[vOff++] = subSz23z; verts[vOff++] = subSz23w
        verts[vOff++] = rowVx; verts[vOff++] = rowVy; verts[vOff++] = rowVz; verts[vOff++] = rowVw
      }

      writeVert(xL, yB, t.heights[bl], 0, 0, sBLu, sBLv) // BL
      writeVert(xR, yB, t.heights[br], 1, 0, sBRu, sBRv) // BR
      writeVert(xL, yT, t.heights[tl], 0, 1, sTLu, sTLv) // TL
      writeVert(xR, yT, t.heights[tr], 1, 1, sTRu, sTRv) // TR

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

  const prog = buildProgram(gl)
  const STRIDE_B = STRIDE_F * 4
  const triCount = numCells * 2

  // war3map.shd is the baked doodad-cast shadow map. HiveWE skips rendering it;
  // we follow suit because at game-scale viewport zooms the shadows appear as
  // floating cell-aligned dark blocks without their visible casters.
  const shadowTex: WebGLTexture | null = null

  flog(`[terrain] built blended mesh: ${numCells} cells, ${numPalettes} palettes, atlas ${atlasW}×${atlasH}`)

  return {
    drawCallCount: 1,
    draw(viewProj: Float32Array) {
      gl.useProgram(prog.program)
      gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)

      gl.activeTexture(gl.TEXTURE0)
      gl.bindTexture(gl.TEXTURE_2D, atlas)
      gl.uniform1i(prog.uAtlas, 0)

      if (shadowTex) {
        gl.activeTexture(gl.TEXTURE1)
        gl.bindTexture(gl.TEXTURE_2D, shadowTex)
        gl.uniform1i(prog.uShadowTex, 1)
        gl.uniform1i(prog.uHasShadow, 1)
      } else {
        gl.uniform1i(prog.uHasShadow, 0)
      }

      gl.enable(gl.DEPTH_TEST)
      gl.depthFunc(gl.LEQUAL)
      gl.depthMask(true)
      gl.disable(gl.BLEND)
      gl.disable(gl.CULL_FACE)

      gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)

      let off = 0
      function setAttr(loc: number, size: number) {
        if (loc >= 0) {
          gl.enableVertexAttribArray(loc)
          gl.vertexAttribPointer(loc, size, gl.FLOAT, false, STRIDE_B, off)
        }
        off += size * 4
      }
      setAttr(prog.aPosition, 3)
      setAttr(prog.aShadowUV, 2)
      setAttr(prog.aCellUV, 2)
      setAttr(prog.aSubUV01, 4)
      setAttr(prog.aSubUV23, 4)
      setAttr(prog.aSubSize01, 4)
      setAttr(prog.aSubSize23, 4)
      setAttr(prog.aRowV, 4)

      gl.drawElements(gl.TRIANGLES, triCount * 3, indexType, 0)

      if (prog.aPosition >= 0) gl.disableVertexAttribArray(prog.aPosition)
      if (prog.aShadowUV >= 0) gl.disableVertexAttribArray(prog.aShadowUV)
      if (prog.aCellUV >= 0) gl.disableVertexAttribArray(prog.aCellUV)
      if (prog.aSubUV01 >= 0) gl.disableVertexAttribArray(prog.aSubUV01)
      if (prog.aSubUV23 >= 0) gl.disableVertexAttribArray(prog.aSubUV23)
      if (prog.aSubSize01 >= 0) gl.disableVertexAttribArray(prog.aSubSize01)
      if (prog.aSubSize23 >= 0) gl.disableVertexAttribArray(prog.aSubSize23)
      if (prog.aRowV >= 0) gl.disableVertexAttribArray(prog.aRowV)
    },
    dispose() {
      gl.deleteBuffer(vbo)
      gl.deleteBuffer(ibo)
      gl.deleteTexture(atlas)
      if (shadowTex) gl.deleteTexture(shadowTex)
      gl.deleteProgram(prog.program)
    },
  }
}
