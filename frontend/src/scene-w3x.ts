// Wraps mdx-m3-viewer's War3MapViewer. Replaces the previous Three.js
// scene. Architecture:
//
//   1. We construct a War3MapViewer attached to our <canvas>.
//   2. Asset requests (every BLP/MDX/SLK the map or stock assets reference)
//      flow through pathSolver. We return "/asset/<lowercased-path>" URLs;
//      the viewer fetches via XHR, Wails' embedded HTTP server routes the
//      request to our Go assetHandler (see asset_handler.go), which reads
//      from the current map's MPQ or (TODO) CASC.
//   3. War3MapViewer.loadMap takes raw .w3x bytes — it has its own MPQ
//      parser. We hand it whatever GetMapBytes() returned.
//
// What's NOT here yet:
//   - CASC source for stock assets — without it, base files won't load
//     and the terrain won't have textures. Errors are logged as warnings.
//   - Selection picking — needs ray-AABB testing against scene instances.
//   - Camera controls (orbit/pan/zoom) — mdx-m3-viewer ships a camera
//     class but no controls; ours are TBD.

import * as MV_ns from 'mdx-m3-viewer'
import { flog } from './debuglog'

const MV: any = (MV_ns as any).default ?? MV_ns
const War3MapViewer = MV?.viewer?.handlers?.War3MapViewer

// Monkey-patch the w3u Modification parser. mdx-m3-viewer only knows
// variable types 0..3 and throws on anything else. Modern Reforged w3u
// files (and possibly the older custom-edited maps via JassNewGenPack)
// contain types outside this range; without the patch loadMap aborts on
// the first such modification and nothing renders. We swallow the
// exception and treat unknown types as int32 — the bytestream may end
// up slightly off, but rendering proceeds.
;(function patchModification() {
  try {
    const Modification = MV?.parsers?.w3x?.w3u?.Modification
      ?? MV?.parsers?.w3u?.Modification
    if (!Modification?.prototype?.load) {
      flog('[patch] could not locate w3u.Modification')
      return
    }
    const origLoad = Modification.prototype.load
    Modification.prototype.load = function (stream: any, useOptionalInts: boolean) {
      const startPos = stream.index
      try {
        origLoad.call(this, stream, useOptionalInts)
      } catch (e) {
        // Rewind and treat as a generic int32 modification of the same length.
        stream.index = startPos
        this.id = stream.readBinary(4)
        this.variableType = stream.readInt32()
        if (useOptionalInts) {
          this.levelOrVariation = stream.readInt32()
          this.dataPointer = stream.readInt32()
        }
        // Read the value as int32 regardless of declared type.
        this.value = stream.readInt32()
        this.u1 = stream.readInt32()
      }
    }
    flog('[patch] w3u.Modification.load is now lenient')
  } catch (e) {
    flog('[patch] failed:', e instanceof Error ? e.message : String(e))
  }
})()

export interface SceneAPI {
  loadMap(rawW3xBytes: Uint8Array): void
  dispose(): void
  // Render loop is internal — viewer.updateAndRender() runs every RAF tick.
}

// Returned by pathSolver. mdx-m3-viewer accepts either bytes directly or
// a URL string (which it fetches via XHR). We always return URLs so the
// Wails HTTP asset handler can do the actual byte source resolution.
function pathSolver(src: unknown): unknown {
  if (typeof src !== 'string') return undefined
  const normalized = src.toLowerCase().replace(/\\/g, '/')
  return '/asset/' + normalized
}

export function createScene(canvas: HTMLCanvasElement): SceneAPI {
  if (!War3MapViewer) {
    const keys = MV ? Object.keys(MV) : []
    const viewerKeys = MV?.viewer ? Object.keys(MV.viewer) : []
    const handlerKeys = MV?.viewer?.handlers ? Object.keys(MV.viewer.handlers) : []
    throw new Error(
      `War3MapViewer not found. MV keys=[${keys.join(',')}] viewer=[${viewerKeys.join(',')}] handlers=[${handlerKeys.join(',')}]`
    )
  }
  // mdx-m3-viewer reads canvas.width/height as PIXELS — set them from the
  // CSS box explicitly. Otherwise everything's blurry (README's headline gotcha).
  const sizeToBox = () => {
    const w = Math.max(1, Math.floor(canvas.clientWidth))
    const h = Math.max(1, Math.floor(canvas.clientHeight))
    if (canvas.width !== w) canvas.width = w
    if (canvas.height !== h) canvas.height = h
  }
  sizeToBox()

  const viewer = new War3MapViewer(canvas, pathSolver, /* isReforged */ false)
  ;(window as any).__viewer = viewer // for inspection if devtools is open

  // mdx-m3-viewer emits 'error' for every missing asset / shader compile failure.
  const seenErrors = new Set<string>()
  viewer.on('error', (target: unknown, error: unknown) => {
    const key = (error instanceof Error ? error.message : String(error)) +
      ' :: ' + (target as any)?.fetchUrl
    if (seenErrors.has(key)) return
    seenErrors.add(key)
    flog('[viewer error]', error, 'target:', JSON.stringify({
      fetchUrl: (target as any)?.fetchUrl,
      name: (target as any)?.name,
    }))
  })
  viewer.on('loadend', (target: unknown) => {
    const t = target as any
    if (t && t.error) flog('[loadend with error]', t.error, 'url:', t.fetchUrl)
  })

  viewer.loadBaseFiles()
    .then(() => flog('[loadBaseFiles] succeeded'))
    .catch((err: unknown) => {
      flog('[loadBaseFiles FAILED]', err instanceof Error ? err.stack : String(err))
    })

  let rafId = 0
  let crashed = false
  const loop = () => {
    if (!crashed) {
      try {
        sizeToBox()
        viewer.updateAndRender()
      } catch (e) {
        crashed = true
        flog('[render-loop crash]', e instanceof Error ? e.stack : String(e))
      }
    }
    rafId = requestAnimationFrame(loop)
  }
  loop()

  const ro = new ResizeObserver(sizeToBox)
  ro.observe(canvas)

  return {
    loadMap(bytes: Uint8Array) {
      flog('[loadMap] starting,', bytes.byteLength, 'bytes')
      try {
        viewer.loadMap(bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength))
        flog('[loadMap] returned normally')
      } catch (e) {
        flog('[loadMap CRASH]', e instanceof Error ? e.stack : String(e))
      }
    },
    dispose() {
      cancelAnimationFrame(rafId)
      ro.disconnect()
    },
  }
}
