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

// CJS-via-ESM-interop: when Vite imports a CJS module that uses
// `exports.default = ...`, the namespace import lands the named exports
// at the top level AND duplicates them under .default. mdx-m3-viewer's
// top-level is named exports, so MV_ns.viewer should be there directly.
// We fall through .default just in case.
const MV: any = (MV_ns as any).default ?? MV_ns
const War3MapViewer = MV?.viewer?.handlers?.War3MapViewer

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

  // mdx-m3-viewer emits 'error' for every missing asset / shader compile failure.
  // Without CASC, expect a flood here for base-file loads. Logged once per cause.
  const seenErrors = new Set<string>()
  viewer.on('error', (target: unknown, error: unknown) => {
    const key = String(error)
    if (seenErrors.has(key)) return
    seenErrors.add(key)
    console.warn('[mdx-m3-viewer]', error, target)
  })

  // Kick off base files load. Without CASC this fails for most files; we
  // ignore the error so the viewer is at least constructed and the canvas
  // shows something.
  viewer.loadBaseFiles().catch((err: unknown) => {
    console.warn('loadBaseFiles failed (expected until CASC is wired):', err)
  })

  let rafId = 0
  const loop = () => {
    sizeToBox()
    viewer.updateAndRender()
    rafId = requestAnimationFrame(loop)
  }
  loop()

  const ro = new ResizeObserver(sizeToBox)
  ro.observe(canvas)

  return {
    loadMap(bytes: Uint8Array) {
      try {
        viewer.loadMap(bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength))
      } catch (e) {
        console.warn('loadMap threw:', e)
      }
    },
    dispose() {
      cancelAnimationFrame(rafId)
      ro.disconnect()
    },
  }
}
