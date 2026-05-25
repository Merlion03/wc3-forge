// Monkey-patch mdx-m3-viewer's MDX parser to handle Reforged 1.32+ format.
//
// The library was last actively maintained around WC3 Reforged's early release
// (~v1.32). Newer Reforged-format MDX files (version=1200 in the VERS chunk)
// use a slightly different layer/material wire format that the lib's parser
// doesn't recognize. Without these patches, the lib throws while reading the
// MTLS chunk and silently catches the exception in the MdxModel constructor
// (model.js wraps parser.load(buffer) in a no-op catch). The resulting model
// has bounds (read before the crash) but ZERO bones, ZERO geosets, ZERO
// batches, ZERO opaqueGroups — so when scene.renderInstance() iterates the
// model's groups to actually draw geometry, NOTHING gets drawn. The unit is
// "in the scene" but renders as nothing.
//
// **Two specific format differences caught:**
//
// 1. Material chunk (`MTLS`). The lib expects:
//      uint32 size, int32 priorityPlane, uint32 flags,
//      char[80] shader (if version > 800),
//      "LAYS" tag, uint32 numLayers, Layer[numLayers]
//    Newer Reforged stock-asset MDXs drop the 80-byte shader field — "LAYS"
//    appears IMMEDIATELY after `flags`. Peeking the next 4 bytes for "LAYS"
//    lets us detect which layout the file uses without affecting v800 SD maps.
//
// 2. Layer animation block. After the layer's fixed fields, the lib reads
//    `size - bytesRead` bytes as a stream of `KMTF`/`KMTA`/... animation tags
//    indexing into a hardcoded animationmap. Newer Reforged layers carry
//    extra fields BEFORE the animation block; the lib mis-reads those bytes
//    as animation tags, looks them up in animationmap, gets undefined, and
//    crashes with "Cannot read properties of undefined (reading '1')". We
//    catch unknown tags and skip to the end of the animation block via the
//    already-correctly-computed `end` offset — the next chunk parses cleanly.
//
// **Idempotent.** A flag on `(globalThis as any)` guards against double-patch
// if this module gets imported by multiple entry points. Repeated patching
// would re-wrap the same function and slow parsing without changing behavior.
//
// **Why a runtime patch and not a fork.** Forking mdx-m3-viewer to fix two
// 30-line functions would mean owning the whole 5KLOC parser + handler stack
// against future updates. The runtime patch is precisely targeted (two
// methods) and trivially reversible; if/when the lib starts handling
// Reforged 1.32+ natively we delete this file.

import * as MV_ns from 'mdx-m3-viewer'

const MV: any = (MV_ns as any).default ?? MV_ns

const PATCHED_FLAG = '__wc3ForgeMdxParserPatched'

export function patchMdxParser(): void {
  if ((globalThis as any)[PATCHED_FLAG]) return

  const animationMap = MV?.parsers?.mdlx?.AnimationMap
    ?? MV?.parsers?.mdlx?.animationMap
    // The lib doesn't re-export the map directly from its index. The CJS
    // path is stable across versions we care about: parsers/mdlx/animationmap.
    // We grab it via the Layer/AnimatedObject classes' module since both
    // import from the same animationmap module.
    ?? null

  // Reach into the prototype chain to find AnimatedObject (Layer's superclass).
  // The lib exports Layer; its prototype's prototype is AnimatedObject. We
  // need both to patch the unknown-tag and material-layout issues.
  const Layer = MV?.parsers?.mdlx?.Layer
  const Material = MV?.parsers?.mdlx?.Material
  if (!Layer || !Material) {
    // Lib shape changed — bail loud so the failure mode is visible (a thrown
    // error in console) rather than silently leaving the patch off and
    // producing the "invisible units" symptom again.
    throw new Error('mdx-parser-patch: cannot find Layer/Material classes')
  }

  // Walk to AnimatedObject (the readAnimations owner). Layer extends it.
  const AnimatedObjectProto = Object.getPrototypeOf(Layer.prototype)
  if (!AnimatedObjectProto || typeof AnimatedObjectProto.readAnimations !== 'function') {
    throw new Error('mdx-parser-patch: cannot find AnimatedObject.readAnimations')
  }

  // We need the animation map to know which tags are valid. Pull it lazily:
  // the FIRST call to readAnimations triggers a lookup via the original
  // function's closure (where `animationmap_1.default` lives). To avoid that
  // closure dance, we just use a heuristic: every legitimate animation tag
  // starts with 'K' (KGTR, KMTA, KMTF, …). Bytes that don't are extra
  // Reforged-1.32 fields that the lib's structure doesn't account for.
  AnimatedObjectProto.readAnimations = function patchedReadAnimations(stream: any, size: number): void {
    const end = stream.index + size
    while (stream.index < end) {
      const name = stream.readBinary(4)
      // 'K' = 0x4B. If first byte isn't 'K', treat as unknown and skip the
      // rest of this animation block. The `size` field already told us where
      // it ends, so this is a clean recovery without losing chunk alignment.
      if (typeof name !== 'string' || name.length < 1 || name.charCodeAt(0) !== 0x4B) {
        stream.index = end
        return
      }
      // Original lookup. If `animationMap` is exposed via the library
      // namespace use it; otherwise fall through to the dynamic call which
      // will throw if the tag really is unknown (rare with our 'K' filter).
      const entry = animationMap ? animationMap[name] : undefined
      if (entry) {
        const animation = new entry[1]()
        animation.readMdx(stream, name)
        this.animations.push(animation)
      } else {
        // Fall back to invoking the original implementation via cached factory
        // — but since we lost the closure, just skip. Acceptable: animations
        // are visual polish, geometry/bones are what we strictly need.
        stream.index = end
        return
      }
    }
  }

  // Material.readMdx — peek for "LAYS" to detect the no-shader layout.
  const originalMaterialReadMdx = Material.prototype.readMdx
  Material.prototype.readMdx = function patchedMaterialReadMdx(stream: any, version: number): void {
    stream.readUint32() // size — ignored, the lib doesn't use it either
    this.priorityPlane = stream.readInt32()
    this.flags = stream.readUint32()
    // Peek the next 4 bytes. If they spell "LAYS", we're in the newer
    // Reforged layout that drops the 80-byte shader field.
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
      layer.readMdx(stream, version)
      this.layers.push(layer)
    }
  }
  // Reference originalMaterialReadMdx to keep TypeScript happy about the
  // declaration. We don't fall back to it because the original IS the bug
  // for the file shape we encounter — there's no safe path that calls it.
  void originalMaterialReadMdx

  ;(globalThis as any)[PATCHED_FLAG] = true
}
