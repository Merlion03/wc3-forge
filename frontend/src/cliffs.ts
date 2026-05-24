// Per-cell cliff mesh selection + placement. Mirrors HiveWE's
// `update_cliff_meshes` in `src/base/terrain.ixx`.
//
// Algorithm sketch:
//   For each cell (i, j) whose four corners are NOT all at the same layer
//   height, render a cliff transition mesh. The mesh chosen depends on the
//   pattern of layer heights at the 4 corners — encoded as a 4-character
//   string of layer-height-offsets-from-the-min, with each char being
//   'A' + offset:
//     "AAAA" → no transition (filtered out)
//     "BAAA" → TL one layer above the rest
//     "BBAA" → top edge is one layer above bottom edge
//     "BCBA" → TR two above, BR one above, others at base
//   …and so on.
//
//   The filename is then `Doodads/Terrain/Cliffs/Cliffs<pattern><variation>.mdx`
//   where <variation> is the per-cell variation (0..N) chosen at map-author
//   time and stored in the bottom-left corner's `cliff_var`. We try the
//   stored variation; if the file 404s we fall back to variation 0.
//
//   Ramps (sloped transitions) are a separate special case — three-cell
//   spans with a rampflag pattern. Not implemented in this first pass;
//   filed as future work. They render as smooth slopes between layers.

import { flog } from './debuglog'
import type { ModelViewer } from 'mdx-m3-viewer'

interface TerrainCliffData {
  width: number   // vertex count along X
  height: number  // vertex count along Y
  center_offset: [number, number]
  layer_height: number[]
  cliff_tex: number[]
  cliff_var: number[]
  ramp_flags: number[]
  cliff_palette: string[]
}

export interface CliffPlacement {
  /** MDX path to load. */
  path: string
  /** World position of the cell's bottom-left corner at the base layer's Z. */
  pos: [number, number, number]
}

export function computeCliffPlacements(t: TerrainCliffData): CliffPlacement[] {
  const out: CliffPlacement[] = []
  if (!t || !t.layer_height || t.layer_height.length === 0) return out

  const W = t.width
  const H = t.height
  if (W < 2 || H < 2) return out
  const cx = t.center_offset[0]
  const cy = t.center_offset[1]

  // Cliff palette FourCC determines which subdirectory of cliff models we use.
  // HiveWE caches cliff_to_ground per cliffset; the cliff palette index lives
  // on the bottom-left corner of each cell. For a first pass we ignore the
  // cliff-tileset distinction and always read the variations from the
  // canonical Cliffs/ tree — that matches stock WC3 where all cliff models
  // share the same `Doodads/Terrain/Cliffs/` path (the tileset only affects
  // the wraparound TEXTURE, not the mesh selection).

  const idx = (i: number, j: number) => j * W + i

  for (let j = 0; j < H - 1; j++) {
    for (let i = 0; i < W - 1; i++) {
      const bl = idx(i, j)
      const br = idx(i + 1, j)
      const tl = idx(i, j + 1)
      const tr = idx(i + 1, j + 1)

      const lh_bl = t.layer_height[bl]
      const lh_br = t.layer_height[br]
      const lh_tl = t.layer_height[tl]
      const lh_tr = t.layer_height[tr]

      // Skip cells where all 4 corners are at the same layer (no cliff
      // transition needed — flat ground).
      if (lh_bl === lh_br && lh_bl === lh_tl && lh_bl === lh_tr) continue

      // Skip ramp cells — those need a separate clifftrans algorithm we
      // haven't ported yet. For now: silently skip, terrain mesh handles
      // the slope visually via per-corner Z interpolation.
      if ((t.ramp_flags[bl] & 1) !== 0) continue

      const base = Math.min(lh_tl, lh_tr, lh_br, lh_bl)
      const fileName = String.fromCharCode(
        65 + lh_tl - base, // 'A' + offset
        65 + lh_tr - base,
        65 + lh_br - base,
        65 + lh_bl - base,
      )

      if (fileName === 'AAAA') continue // double-check guard

      // Stock cliff models live at: Doodads/Terrain/Cliffs/Cliffs<pattern><var>.mdx
      const variation = t.cliff_var[bl] | 0
      const path = `Doodads/Terrain/Cliffs/Cliffs${fileName}${variation}.mdx`

      // Cell (i, j) in vertex coords means the cell whose bottom-left
      // corner is vertex (i, j). World position of that corner:
      const wx = cx + i * 128
      const wy = cy + j * 128
      // Base Z: cliff models are authored expecting Z=0 to be the bottom-
      // (lowest) layer of the cell. WC3 convention: each layer step = 128
      // studs; layer 2 is "ground level" (FinalZ formula: corner_height +
      // (layer - 2) * 128). Cliff mesh placed at base layer's nominal Z.
      const wz = (base - 2) * 128

      out.push({ path, pos: [wx, wy, wz] })
    }
  }
  return out
}

export interface CliffRendering {
  /** Number of cliff instances actually placed (some MDXs may 404). */
  placed: number
  /** Disposal: detach all instances from the scene. */
  dispose(): void
}

export async function renderCliffs(
  viewer: ModelViewer,
  scene: any,
  pathSolver: any,
  placements: CliffPlacement[],
): Promise<CliffRendering> {
  const instances: any[] = []
  let placed = 0
  let failed = 0
  // Group placements by path so each unique MDX loads once. mdx-m3-viewer's
  // resourceMap already dedupes, but grouping lets us bail early when an
  // MDX 404s instead of retrying per instance.
  const byPath = new Map<string, CliffPlacement[]>()
  for (const p of placements) {
    const list = byPath.get(p.path) ?? []
    list.push(p)
    byPath.set(p.path, list)
  }

  for (const [path, group] of byPath) {
    // Try the requested path; if it 404s (lib resolves the promise with
    // undefined rather than throwing on missing assets), retry with the
    // variation-0 fallback path. Cliffs only ship variations 0..N where N
    // varies per pattern, and authored maps sometimes reference variations
    // that no longer exist.
    let model: any
    try { model = await viewer.load(path, pathSolver) } catch { /* fall through */ }
    if (!model || typeof model.addInstance !== 'function') {
      const fallback = path.replace(/(\d+)\.mdx$/i, '0.mdx')
      if (fallback !== path) {
        try { model = await viewer.load(fallback, pathSolver) } catch { /* ignore */ }
      }
    }
    if (!model || typeof model.addInstance !== 'function') {
      failed += group.length
      continue
    }
    for (const p of group) {
      const inst = model.addInstance()
      inst.move(p.pos)
      inst.setScene(scene)
      instances.push(inst)
      placed++
    }
  }
  if (failed > 0) {
    flog(`[cliffs] placed=${placed} failed=${failed} unique-paths=${byPath.size}`)
  } else {
    flog(`[cliffs] placed=${placed} unique-paths=${byPath.size}`)
  }
  return {
    placed,
    dispose() {
      for (const inst of instances) {
        try { inst.detach() } catch { /* already detached */ }
      }
      instances.length = 0
    },
  }
}
