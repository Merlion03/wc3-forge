// Monkey-patch mdx-m3-viewer's MDX parser to handle modern Reforged MDX
// formats. The library was last actively maintained around early Reforged
// (~v1.32), targeting MDX VERS=900/1000. WC3 Reforged 1.33+ shipped a new
// MDX wire format (VERS >= 1100) for Material/Layer chunks that the lib
// doesn't understand. Without these patches, the lib throws while reading
// the MTLS chunk and silently catches the exception in MdxModel's
// constructor (model.js wraps parser.load() in a no-op catch). The result
// is a model with 0 geosets / 0 bones / 0 batches — present in the viewer
// cache but rendering as nothing. This produces the "silent invisible
// units / cliffs / doodads" symptom across modern Reforged maps.
//
// **The new (post-v1100) MTLS / Layer wire format** — derived by reading
// HiveWE's `src/file_formats/mdx/mdx_reader.cpp::read_MTLS_texs_post_v1100`
// AND verified by hex-inspecting MDX files extracted from the user's local
// CASC install (units/human/footman/footman.mdx,
// doodads/terrain/cliffs/cliffsaacb0.mdx,
// doodads/lordaeronsummer/props/brazier/brazier.mdx — all VERS=1200):
//
//   Material chunk (per entry within MTLS):
//     uint32  size                       // includes itself
//     int32   priorityPlane
//     uint32  flags
//     // NO shader field — drops the 80-byte char[80] that pre-v1100
//     // versions 900/1000 carry between flags and LAYS.
//     "LAYS"  (uint32 tag)
//     uint32  numLayers
//     Layer[numLayers]
//
//   Layer:
//     uint32  size                       // includes itself
//     uint32  filterMode (a.k.a. blend_mode)
//     uint32  flags    (a.k.a. shading_flags)
//     uint32  textureId                  // unused in this format (textures
//                                        // are carried inline in the texs
//                                        // array below); lib must skip it
//     uint32  textureAnimationId
//     uint32  coordId
//     float32 alpha
//     float32 emissiveGain
//     float32[3] fresnelColor
//     float32 fresnelOpacity
//     float32 fresnelTeamColor
//     uint32  hd                         // NEW in post-v1100
//     uint32  texsCount                  // NEW in post-v1100
//     Tex[texsCount]:
//       uint32  id
//       uint32  slot                     // HiveWE comment: "always garbage"
//       [optional KMTF track for this tex]
//     // Then animation tracks: KMTA, KMTE, KFC3, KFCA, KFTC
//
// `size` covers the whole layer including the per-tex KMTF tracks AND the
// trailing animation tracks. After consuming the fixed fields + texs array,
// remaining bytes in `size` are animation tags read by the existing
// readAnimations machinery (which is correct for post-v1100 — only the
// PRE-animation layout changed).
//
// **Backward compatibility.** Files with VERS < 1100 keep the original
// readMdx logic (called via the saved `origLayerReadMdx` reference). The
// lib's readMdx is correct for VERS 800/900/1000. We dispatch on parser
// version via a `_currentVersion` capture in the Material monkey-patch.
//
// **Pre-v1100 fix (kept from the previous patch).** Some VERS > 800 files
// with version=900/1000 in pre-1.33 Reforged also drop the shader field.
// Peeking the next 4 bytes for "LAYS" detects which layout the file uses;
// the new Material patch unifies the dispatch.
//
// **Idempotent.** A flag on `(globalThis as any)` guards against double-
// patch if this module gets imported by multiple entry points.
//
// **Verification fixture.** node scripts/test-mdx-parse-with-patch.cjs
// (in the repo) loads extracted MDX files and reports the parsed-out
// model state (sequences/materials/geosets/bones counts). After this
// patch, the cliff + brazier + runeart + footman files all parse with
// non-zero geosets and bones — confirming geometry made it through the
// MTLS chunk and into GEOS.

import * as MV_ns from 'mdx-m3-viewer'

const MV: any = (MV_ns as any).default ?? MV_ns

const PATCHED_FLAG = '__wc3ForgeMdxParserPatched'

export function patchMdxParser(): void {
  if ((globalThis as any)[PATCHED_FLAG]) return

  const Layer = MV?.parsers?.mdlx?.Layer
  const Material = MV?.parsers?.mdlx?.Material
  if (!Layer || !Material) {
    // Lib shape changed — bail loud so the failure mode is visible (a thrown
    // error in console) rather than silently leaving the patch off and
    // reproducing the "invisible geometry" symptom.
    throw new Error('mdx-parser-patch: cannot find Layer/Material classes')
  }

  // Save the lib's original Layer.readMdx so files with VERS < 1100 still
  // go through the lib's well-tested path.
  const origLayerReadMdx = Layer.prototype.readMdx

  // Post-v1100 Layer reader. Parses the new wire format (see the doc block
  // at top). Mutates `this` the same way the lib's reader does, so the
  // surrounding viewer code (which treats Layer the same regardless of
  // version) keeps working.
  function readLayerPostV1100(this: any, stream: any, version: number): void {
    const start = stream.index
    const size = stream.readUint32()
    this.filterMode = stream.readUint32()
    this.flags = stream.readUint32()
    // texture_id present in file but unused in the post-v1100 format — each
    // layer carries its own per-texture struct in the texs array below.
    // HiveWE skips it via `reader.advance(4)`. The lib's pre-v1100 model
    // reads it into this.textureId; we set -1 here so downstream code that
    // would look at it sees a sentinel rather than stale data.
    stream.readInt32() // ignored
    this.textureId = -1
    this.textureAnimationId = stream.readInt32()
    this.coordId = stream.readUint32()
    this.alpha = stream.readFloat32()
    this.emissiveGain = stream.readFloat32()
    stream.readFloat32Array(this.fresnelColor)
    this.fresnelOpacity = stream.readFloat32()
    this.fresnelTeamColor = stream.readFloat32()
    // NEW in post-v1100:
    const hd = stream.readUint32()
    const texsCount = stream.readUint32()
    // Stash for any downstream consumer that wants to know whether the
    // layer was authored as HD-only. The mdx-m3-viewer Layer class doesn't
    // declare these, so use bracket access via `as any` semantics. The viewer
    // pipeline doesn't read these — the texture binding has already routed
    // through `viewer.load(path)` for both SD and HD paths.
    ;(this as any).hd = hd
    // For each inline tex entry: parse and discard the slot. The texture id
    // points into the model-level Textures chunk; for the lib's downstream
    // rendering, we set `this.textureId` to the FIRST encountered id so the
    // pre-v1100 codepath (which expects this.textureId to be the layer's
    // texture) keeps working without further patches. Extra texs (PBR
    // multi-layer for HD) get dropped — HiveWE's similar fallback for the
    // pre-v1100 code path is the same trick.
    let firstTexId = -1
    for (let t = 0; t < texsCount; t++) {
      const id = stream.readInt32()
      stream.readInt32() // slot — HiveWE comment: "always a garbage value"
      if (t === 0) firstTexId = id
      // The per-tex inline KMTF track is technically possible per HiveWE's
      // reader (peek the next 4 bytes, if 'KMTF' consume the track). We
      // skip that for now — if subsequent track tags appear in the remaining
      // animations area, readAnimations will skip them via the 'K' filter.
    }
    if (firstTexId >= 0) this.textureId = firstTexId
    // Whatever bytes remain in this layer's size are KMTA/KMTE/KFC3/KFCA/
    // KFTC animation tracks. Use the lib's readAnimations (animatedobject
    // protocol). The patched-for-safety version (see below) handles
    // unknown tags by skipping to `end` instead of crashing on
    // `animationmap[undefined][1]`.
    this.readAnimations(stream, size - (stream.index - start))
  }

  // The version dispatcher. We need to know the file's VERS to choose between
  // the lib's original Layer.readMdx (good for VERS 800/900/1000) and our
  // new post-v1100 reader. The Material chunk's readMdx receives `version`
  // — capture it there and stash on `this` (the layer being constructed)
  // via a closure variable set on the Material's iteration loop.
  Layer.prototype.readMdx = function patchedLayerReadMdx(stream: any, version: number): void {
    if (version >= 1100) {
      readLayerPostV1100.call(this, stream, version)
    } else {
      origLayerReadMdx.call(this, stream, version)
    }
  }

  // Material.readMdx — for VERS >= 1100 the shader field is dropped (LAYS
  // appears immediately after flags). For VERS 900/1000 the lib's
  // readMdx is correct; we still peek-LAYS to handle pre-1.33 stock assets
  // that drop the shader field (the previous patch handled that case).
  Material.prototype.readMdx = function patchedMaterialReadMdx(stream: any, version: number): void {
    stream.readUint32() // size — lib's reader also ignores this
    this.priorityPlane = stream.readInt32()
    this.flags = stream.readUint32()
    // Peek-LAYS: detects shader-dropped layouts regardless of declared
    // version. Reforged 1.33+ (VERS >= 1100) ALWAYS drops shader; some
    // pre-1.33 stock assets (VERS = 900/1000 with hand-stripped shader)
    // also drop it. The peek decides per-material rather than per-file.
    const u = stream.uint8array as Uint8Array
    const i = stream.index
    let peekLAYS = false
    if (i + 4 <= u.length) {
      peekLAYS = u[i] === 0x4C /* L */ && u[i+1] === 0x41 /* A */
              && u[i+2] === 0x59 /* Y */ && u[i+3] === 0x53 /* S */
    }
    if (!peekLAYS && version > 800) {
      this.shader = stream.read(80)
    } else {
      this.shader = ''
    }
    stream.skip(4) // LAYS
    const numLayers = stream.readUint32()
    for (let n = 0; n < numLayers; n++) {
      const layer = new Layer()
      // Dispatches via patched Layer.readMdx → readLayerPostV1100 for
      // VERS >= 1100, origLayerReadMdx otherwise.
      layer.readMdx(stream, version)
      this.layers.push(layer)
    }
  }

  // Safety-belt readAnimations. The lib's original throws when it encounters
  // an animation tag that isn't in the hardcoded animationmap; we treat
  // unknown tags as "skip to chunk end" instead, so a single unknown tag
  // doesn't crash the whole model parse. This is defense-in-depth for any
  // model variant that adds new animation tags the lib doesn't recognize.
  // Without it, MDX files with KFGA or other future tags would silently
  // miss the rest of their chunks (model.js's outer catch).
  const animationMap = MV?.parsers?.mdlx?.AnimationMap
    ?? MV?.parsers?.mdlx?.animationMap
    ?? null
  const AnimatedObjectProto = Object.getPrototypeOf(Layer.prototype)
  if (AnimatedObjectProto && typeof AnimatedObjectProto.readAnimations === 'function') {
    AnimatedObjectProto.readAnimations = function patchedReadAnimations(stream: any, size: number): void {
      const end = stream.index + size
      while (stream.index < end) {
        const name = stream.readBinary(4)
        // Every legitimate animation tag starts with 'K' (KGTR, KMTA, KMTF, …)
        // — a non-K byte is either a future format we don't handle, or a
        // misaligned read. Either way: skip the rest of the animation block.
        // The chunk-level `size` field already told us where it ends so this
        // is a clean recovery without losing chunk alignment.
        if (typeof name !== 'string' || name.length < 1 || name.charCodeAt(0) !== 0x4B /* K */) {
          stream.index = end
          return
        }
        const entry = animationMap ? animationMap[name] : undefined
        if (entry) {
          const animation = new entry[1]()
          animation.readMdx(stream, name)
          this.animations.push(animation)
        } else {
          // Unknown but K-prefixed tag — recovery is the same. Animations
          // are visual polish; geometry/bones are what we strictly need
          // and they've already been read by this point.
          stream.index = end
          return
        }
      }
    }
  }

  ;(globalThis as any)[PATCHED_FLAG] = true
}
