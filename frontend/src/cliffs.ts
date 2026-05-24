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
  const ramp = (k: number) => (t.ramp_flags[k] & 1) !== 0

  // `consumedByRamp` is the set of bottom-left-corner indices for cells that
  // got covered by a ramp `clifftrans` mesh. Those cells must NOT also get a
  // regular cliff mesh — the clifftrans IS the cliff transition for them.
  // This mirrors HiveWE's `corner_romp` flag, which is similarly computed
  // during the ramp pass and consulted in the regular cliff pass.
  const consumedByRamp = new Set<number>()

  // Filename char for one corner of a clifftrans piece. Non-ramp corners use
  // a normal 'A'+offset encoding; ramp corners use 'L' + offset * -4. The
  // letter L is the marker that says "this corner sits on the ramp surface",
  // and the *-4 scaling spreads ramp-step combinations across distinct chars.
  function rampChar(isRamp: boolean, layerHeight: number, base: number): number {
    const baseChar = isRamp ? 76 /* 'L' */ : 65 /* 'A' */
    const scale = isRamp ? -4 : 1
    return baseChar + (layerHeight - base) * scale
  }

  // --- Pass 1: vertical ramps (3 corners tall on each side) ---
  //
  // A vertical ramp occupies cells (i, j) and (i, j+1). One column of corners
  // ramps from one layer to another; the adjacent column either stays flat
  // (one layer) or ramps the other direction. The pattern HiveWE detects:
  //
  //   col_i:    bl(ramp=R) tl(ramp=R) ttl(ramp=R)   // 3 corners agree on ramp
  //   col_i+1:  br(ramp=S) tr(ramp=S) ttr(ramp=S)   // also agree, but S!=R
  //   layer_height[tl] == min(lh[bl], lh[ttl])      // middle is the lower end
  //   layer_height[tr] == min(lh[br], lh[ttr])
  //
  // The bl/ttl pair is one ramp-side, the br/ttr pair is the other. The
  // *layer_height* difference between bl and ttl is what makes the ramp.
  for (let j = 0; j < H - 2; j++) {
    for (let i = 0; i < W - 1; i++) {
      const bl = idx(i, j), br = idx(i + 1, j)
      const tl = idx(i, j + 1), tr = idx(i + 1, j + 1)
      const ttl = idx(i, j + 2), ttr = idx(i + 1, j + 2)

      const lhBL = t.layer_height[bl], lhBR = t.layer_height[br]
      const lhTL = t.layer_height[tl], lhTR = t.layer_height[tr]
      const lhTTL = t.layer_height[ttl], lhTTR = t.layer_height[ttr]

      const ae = Math.min(lhBL, lhTTL)
      const cf = Math.min(lhBR, lhTTR)
      if (lhTL !== ae || lhTR !== cf) continue

      const rBL = ramp(bl), rTL = ramp(tl), rTTL = ramp(ttl)
      const rBR = ramp(br), rTR = ramp(tr), rTTR = ramp(ttr)
      if (rBL !== rTL || rBL !== rTTL) continue
      if (rBR !== rTR || rBR !== rTTR) continue
      if (rBL === rBR) continue

      // Require an actual layer-height transition across the spanning
      // corners (BL, TTL, BR, TTR). If all four are at the same layer,
      // the cells are ramp-flagged but flat — Blizzard doesn't ship a
      // clifftrans asset for those, and there's nothing to transition.
      if (lhBL === lhTTL && lhBL === lhBR && lhBL === lhTTR) continue

      const base = Math.min(ae, cf)
      const name = String.fromCharCode(
        rampChar(rTTL, lhTTL, base),
        rampChar(rTTR, lhTTR, base),
        rampChar(rBR, lhBR, base),
        rampChar(rBL, lhBL, base),
      )
      const path = `doodads/terrain/clifftrans/clifftrans${name}0.mdx`
      out.push({
        path,
        pos: [cx + i * 128, cy + j * 128, (base - 2) * 128],
      })
      // The clifftrans piece covers cells (i, j) and (i, j+1) — skip both
      // in the regular cliff pass.
      consumedByRamp.add(bl)
      consumedByRamp.add(tl)
    }
  }

  // --- Pass 2: horizontal ramps (symmetric, 3 corners wide on each side) ---
  for (let j = 0; j < H - 1; j++) {
    for (let i = 0; i < W - 2; i++) {
      const bl = idx(i, j), br = idx(i + 1, j), brr = idx(i + 2, j)
      const tl = idx(i, j + 1), tr = idx(i + 1, j + 1), trr = idx(i + 2, j + 1)

      const lhBL = t.layer_height[bl], lhBR = t.layer_height[br], lhBRR = t.layer_height[brr]
      const lhTL = t.layer_height[tl], lhTR = t.layer_height[tr], lhTRR = t.layer_height[trr]

      const ae = Math.min(lhBL, lhBRR)
      const bf = Math.min(lhTL, lhTRR)
      if (lhBR !== ae || lhTR !== bf) continue

      const rBL = ramp(bl), rBR = ramp(br), rBRR = ramp(brr)
      const rTL = ramp(tl), rTR = ramp(tr), rTRR = ramp(trr)
      if (rBL !== rBR || rBL !== rBRR) continue
      if (rTL !== rTR || rTL !== rTRR) continue
      if (rBL === rTL) continue

      // Same flat-ramp filter as vertical.
      if (lhBL === lhBRR && lhBL === lhTL && lhBL === lhTRR) continue

      const base = Math.min(ae, bf)
      const name = String.fromCharCode(
        rampChar(rTL, lhTL, base),
        rampChar(rTRR, lhTRR, base),
        rampChar(rBRR, lhBRR, base),
        rampChar(rBL, lhBL, base),
      )
      const path = `doodads/terrain/clifftrans/clifftrans${name}0.mdx`
      out.push({
        path,
        pos: [cx + i * 128, cy + j * 128, (base - 2) * 128],
      })
      consumedByRamp.add(bl)
      consumedByRamp.add(br)
    }
  }

  // --- Pass 3: regular cliff transitions ---
  //
  // is_corner_ramp_entrance: cells where all 4 corners are ramp-flagged AND
  // the diagonals aren't symmetric. These are the "openings" of a ramp where
  // it meets level ground — covered by the adjacent ramp clifftrans mesh, so
  // a regular cliff piece would double up. HiveWE has the same skip.
  function isRampEntrance(i: number, j: number): boolean {
    if (i >= W - 1 || j >= H - 1) return false
    const bl = idx(i, j), br = idx(i + 1, j)
    const tl = idx(i, j + 1), tr = idx(i + 1, j + 1)
    if (!ramp(bl) || !ramp(br) || !ramp(tl) || !ramp(tr)) return false
    const symmetric =
      t.layer_height[bl] === t.layer_height[tr] &&
      t.layer_height[tl] === t.layer_height[br]
    return !symmetric
  }

  for (let j = 0; j < H - 1; j++) {
    for (let i = 0; i < W - 1; i++) {
      const bl = idx(i, j)
      if (consumedByRamp.has(bl)) continue
      if (isRampEntrance(i, j)) continue
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

      const base = Math.min(lh_tl, lh_tr, lh_br, lh_bl)
      const fileName = String.fromCharCode(
        65 + lh_tl - base, // 'A' + offset
        65 + lh_tr - base,
        65 + lh_br - base,
        65 + lh_bl - base,
      )
      if (fileName === 'AAAA') continue

      const variation = t.cliff_var[bl] | 0
      const path = `Doodads/Terrain/Cliffs/Cliffs${fileName}${variation}.mdx`

      out.push({
        path,
        pos: [cx + i * 128, cy + j * 128, (base - 2) * 128],
      })
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
    // that no longer exist. Without HiveWE's `Cliffs.slk` variation table
    // we can't pre-clamp, so we fall back at fetch time.
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
      // Sample a few unique missing paths so we can spot pattern issues.
      if (failed === group.length || failed < 10) {
        flog(`[cliffs miss] ${path} (×${group.length} cells)`)
      }
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
