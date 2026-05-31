<script lang="ts">
  // Bottom-right HTML overlay that displays the map's BAKED minimap image —
  // war3mapMap.blp / war3mapMap.dds / war3mapPreview.tga, in that precedence
  // order. The image is author-drawn (NOT a render of terrain data), which
  // matches HiveWE's minimap panel exactly (see project_wc3_forge memory).
  //
  // Click-to-pan: the renderer knows the image's W×H pixels correspond to the
  // map's playable terrain extent (terrain.width-1 cells × 128 studs per cell,
  // centered on terrain.center_offset). We convert minimap-pixel → world-coord
  // and call SceneAPI.panTo to move the 3D camera. Same affordance HiveWE has.
  //
  // Camera-frustum overlay: an SVG <polygon> stroked on top of the image.
  // Each canvas-corner's screen→world ray is intersected with the z=0 ground
  // plane (delegated to SceneAPI.getViewportFrustumCorners — same math the
  // terrain picker uses). Those 4 world points are mapped to minimap pixel
  // coords via the inverse of the click-to-pan formula below. A RAF tick
  // re-reads the corners every frame so the polygon tracks live camera
  // panning / zoom. Cost is tiny (4 ray-cast + plane intersections + an SVG
  // attribute write); cheap enough not to bother with delta-checking.
  //
  // SVG over Canvas: pure DOM, Svelte-reactive, no extra GL state to babysit.
  // The polygon's points are in a normalized [0..1] viewBox so the same
  // numbers work regardless of the rendered overlay's pixel size. The
  // container's overflow:hidden naturally clips edges that fall outside the
  // minimap when the camera is at the world border or near-vertical zoom.
  //
  // Visibility is owned by App.svelte (which gates the mount), as is the
  // global 'M' hotkey — this component just renders when mounted.

  import {
    GetMinimapBytes,
    GenerateMinimapBytes,
    GetTerrain,
  } from '../wailsjs/go/main/App.js'
  import { decodeImageBytes } from './icon-loader'
  import type { SceneAPI } from './scene-instances'

  // The scene API — passed from App.svelte so click-to-pan can drive the
  // 3D camera. Optional because the parent gates the mount on `scene` being
  // non-null too, but the type allows null defensively in case of mount races.
  // mapLoadGen is bumped from App.svelte whenever the loaded map changes —
  // drives the image reload + terrain coordinate refresh. Reactive trigger;
  // the value itself is ignored (just a generation counter).
  let {
    scene = null,
    mapLoadGen = 0,
  }: {
    scene?: SceneAPI | null
    mapLoadGen?: number
  } = $props()

  // The image-loading state. dataURL is the result of decoding the bytes
  // returned by GetMinimapBytes; "" means we have no image (either no map
  // loaded, no baked preview in the map, or decode failed).
  let dataURL = $state('')
  // True when dataURL came from an on-the-fly terrain bake (the map ships no
  // baked minimap) rather than the map's own war3mapMap.blp/.dds/.tga. Drives a
  // subtle "auto" badge so the preview reads as synthesized, not authored.
  let generated = $state(false)
  // The image's natural aspect ratio. Defaults to 1:1 (square); updated once
  // the <img> reports its natural dimensions. Non-square preview images (rare)
  // get correctly proportioned via CSS aspect-ratio.
  let aspect = $state(1)
  let imgEl: HTMLImageElement | null = $state(null)
  // Terrain extents — captured at map-load so click-to-pan can convert from
  // minimap-pixel-space → world-coord. Mirrors the GetTerrain DTO fields
  // we actually need; cached here so we don't pay the IPC cost per click.
  let terrainW = 0 // vertex count along X
  let terrainH = 0 // vertex count along Y
  let centerOffset: [number, number] = [0, 0] // game coords of vertex (0,0)
  // STUDS_PER_CELL: each terrain cell is 128 game-coord units wide. Total
  // playable extent in studs = (W-1) × STUDS_PER_CELL, same convention used
  // throughout the renderer.
  const STUDS_PER_CELL = 128
  // Reload generation tracking — when mapLoadGen changes we kick a new
  // reload(). Tracking the last applied value separately so reactive blocks
  // don't double-fire on the same generation.
  let lastAppliedGen = -1

  // Frustum polygon — 4 minimap-pixel-space (px, py) tuples in TL→TR→BR→BL
  // order, normalized to the [0..1] range so the SVG viewBox math is trivial.
  // Empty array means "don't draw" (no map, or camera math returned null).
  // Updated per-RAF by tickFrustum() below; the SVG points attribute is a
  // $derived that rebuilds whenever frustumPts changes.
  let frustumPts = $state<Array<[number, number]>>([])
  // Stringified SVG points attribute. Recomputed reactively whenever
  // frustumPts changes. Use a 4-corner POLYGON (closed implicitly by <polygon>).
  let frustumPointsAttr = $derived(
    frustumPts.length === 4
      ? frustumPts.map(([u, v]) => `${u.toFixed(4)},${v.toFixed(4)}`).join(' ')
      : '',
  )

  // Hot-path reactive: whenever mapLoadGen advances, reload the minimap.
  // mapLoadGen=0 is a valid generation (map may already be loaded by the
  // time we mount), so the lastAppliedGen=-1 sentinel ensures the first
  // run fires too — no separate onMount() initial-load needed.
  $effect(() => {
    if (mapLoadGen !== lastAppliedGen) {
      lastAppliedGen = mapLoadGen
      void reload()
    }
  })

  // Per-frame frustum-poll RAF loop + the global __minimapClick hook. Setup
  // runs once at mount; the returned cleanup tears down the RAF + global on
  // unmount. Idiomatic Svelte 5 (single $effect with cleanup, no onMount /
  // onDestroy pair).
  $effect(() => {
    // Start the per-frame frustum-poll RAF loop. Cheap (4 ray casts +
    // assignment), and naturally pauses when the tab is backgrounded.
    let rafId = requestAnimationFrame(tickFrustum)
    function tickFrustum() {
      rafId = requestAnimationFrame(tickFrustum)
      if (!scene || !dataURL || terrainW <= 1 || terrainH <= 1) {
        if (frustumPts.length !== 0) frustumPts = []
        return
      }
      const corners = scene.getViewportFrustumCorners()
      if (!corners || corners.length !== 4) {
        if (frustumPts.length !== 0) frustumPts = []
        return
      }
      const worldW = (terrainW - 1) * STUDS_PER_CELL
      const worldH = (terrainH - 1) * STUDS_PER_CELL
      const next: Array<[number, number]> = []
      for (const [wx, wy] of corners) {
        const u = (wx - centerOffset[0]) / worldW
        const v = 1 - (wy - centerOffset[1]) / worldH
        next.push([u, v])
      }
      frustumPts = next
    }

    // Test-driver hook: simulates a click at normalized (u, v) coords in the
    // minimap image's pixel space (0..1 each, origin top-left). Used by the
    // verification harness because WebView2 drops synthetic mouse input on
    // the actual canvas, but the same click-to-pan math is reachable here.
    ;(window as any).__minimapClick = (u: number, v: number) => {
      if (!scene) return false
      if (terrainW <= 1 || terrainH <= 1) return false
      const cu = Math.max(0, Math.min(1, u))
      const cv = Math.max(0, Math.min(1, v))
      const worldW = (terrainW - 1) * STUDS_PER_CELL
      const worldH = (terrainH - 1) * STUDS_PER_CELL
      const x = centerOffset[0] + cu * worldW
      const y = centerOffset[1] + (1 - cv) * worldH
      scene.panTo(x, y)
      try {
        const w: any = window
        if (w.go?.main?.App?.LogJS) {
          w.go.main.App.LogJS(
            `[Minimap] click u=${cu} v=${cv} -> world x=${x.toFixed(1)} y=${y.toFixed(1)}`,
          )
        }
      } catch {}
      return { x, y }
    }

    return () => {
      // Stop the frustum-poll RAF + drop the global hook. No subscriptions
      // to tear down beyond these (App.svelte owns the toggle state and we
      // just unmount when it flips false).
      if (rafId) cancelAnimationFrame(rafId)
      try {
        delete (window as any).__minimapClick
      } catch {}
    }
  })

  async function reload() {
    dataURL = ''
    aspect = 1
    generated = false
    try {
      let dto = await GetMinimapBytes()
      // No baked minimap in the map (e.g. a freshly-created or never-rendered
      // map)? Fall back to a terrain-colored preview baked on the Go side. The
      // generated image spans the same vertex extent the click-to-pan/frustum
      // math assumes, so those affordances keep working unchanged.
      if (!dto || !dto.found || !dto.bytes) {
        try {
          const gen = await GenerateMinimapBytes()
          if (gen && gen.found && gen.bytes) {
            dto = gen
            generated = true
          }
        } catch (ge) {
          console.warn('[Minimap] generate fallback failed:', ge)
        }
      }
      if (!dto || !dto.found || !dto.bytes) return
      // Decode base64 → Uint8Array, then dispatch to BLP/DDS/TGA decoder.
      const bin = atob(dto.bytes)
      const buf = new Uint8Array(bin.length)
      for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i)
      dataURL = decodeImageBytes(buf, dto.ext)
    } catch (e) {
      // Silent failure — the placeholder renders if dataURL stays empty.
      console.warn('[Minimap] decode failed:', e)
      dataURL = ''
      generated = false
    }
    // Cache terrain coords for click-to-pan. Side-band fetch; failures here
    // just mean clicks don't pan (image still displays fine).
    try {
      const t = await GetTerrain()
      terrainW = t.width
      terrainH = t.height
      centerOffset = [t.center_offset[0], t.center_offset[1]]
    } catch (_e) {
      terrainW = 0
      terrainH = 0
      centerOffset = [0, 0]
    }
  }

  function onImgLoad() {
    if (!imgEl) return
    const w = imgEl.naturalWidth
    const h = imgEl.naturalHeight
    if (w > 0 && h > 0) aspect = w / h
  }

  // Click/drag-to-pan: convert minimap-pixel coords → world game-coord, then
  // call SceneAPI.panTo. The math:
  //   - Minimap image dimensions are arbitrary (e.g. 256×256 for BLP).
  //     The pixel-space u,v ∈ [0,1] is independent of resolution.
  //   - The map's playable terrain extent in studs:
  //       worldW = (terrain.width  - 1) × STUDS_PER_CELL
  //       worldH = (terrain.height - 1) × STUDS_PER_CELL
  //   - vertex (0,0) in game coords = center_offset (bottom-left corner)
  //   - vertex (W-1, H-1) = center_offset + (worldW, worldH) (top-right)
  //   - u=0,v=0 (top-left of image) maps to (x=center_offset.x, y=center_offset.y+worldH)
  //     — image Y is flipped vs world Y (image has +Y down, world has +Y up).
  function panFromPointer(clientX: number, clientY: number) {
    if (!scene || !imgEl) return
    if (terrainW <= 1 || terrainH <= 1) return
    const rect = imgEl.getBoundingClientRect()
    if (rect.width <= 0 || rect.height <= 0) return
    // Clamp into [0,1] so the drag still pans to the edge when the pointer
    // strays outside the minimap mid-drag (pointer capture keeps events
    // flowing to us, but the cursor can sit anywhere on the page).
    const u = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width))
    const v = Math.max(0, Math.min(1, (clientY - rect.top) / rect.height))
    const worldW = (terrainW - 1) * STUDS_PER_CELL
    const worldH = (terrainH - 1) * STUDS_PER_CELL
    // Image Y is top-down; world Y is bottom-up. Flip v before mapping.
    const x = centerOffset[0] + u * worldW
    const y = centerOffset[1] + (1 - v) * worldH
    scene.panTo(x, y)
  }

  // Drag-to-pan state. Track the active pointer id so we ignore stray
  // secondary pointers (multitouch, second mouse button) during a drag.
  let dragPointerId: number | null = $state(null)

  function onPointerDown(e: PointerEvent) {
    // Primary button (left mouse / touch) only — right-click is reserved
    // for context menus / camera orbit etc.
    if (e.button !== 0) return
    if (!imgEl) return
    dragPointerId = e.pointerId
    // Capture so we keep receiving move/up events when the cursor leaves
    // the minimap mid-drag — without this the drag would snap-stop at the
    // image bounds.
    try {
      imgEl.setPointerCapture(e.pointerId)
    } catch {}
    panFromPointer(e.clientX, e.clientY)
  }

  function onPointerMove(e: PointerEvent) {
    if (dragPointerId === null || e.pointerId !== dragPointerId) return
    panFromPointer(e.clientX, e.clientY)
  }

  function onPointerUp(e: PointerEvent) {
    if (dragPointerId === null || e.pointerId !== dragPointerId) return
    try {
      imgEl?.releasePointerCapture(e.pointerId)
    } catch {}
    dragPointerId = null
  }
</script>

<!-- Bottom-right anchor — overlays the viewport, doesn't take space in the
     grid. The viewport has position:relative (defined in App.svelte) so we
     anchor to its bottom-right corner via absolute positioning. The
     overlay's height tracks the image's natural aspect ratio so non-1:1
     maps (e.g. 160×128 tile maps like Enfo's FFB) display undistorted.
     pointer-events:auto so clicks land on the minimap, but typing still
     goes to the viewport (no focus stealing). overflow-hidden clips any
     frustum-polygon edge that falls outside the minimap (camera looking
     past the world border, extreme zoom-out). -->
<div
  class="absolute right-3 bottom-3 w-48 max-h-48 bg-card/80 border border-border rounded shadow-lg overflow-hidden z-20 pointer-events-auto backdrop-blur-sm"
  style="height: calc(192px / {aspect});"
>
  {#if dataURL}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <img
      class="block w-full h-full object-contain cursor-crosshair select-none touch-none"
      src={dataURL}
      alt="Map minimap"
      bind:this={imgEl}
      onload={onImgLoad}
      onpointerdown={onPointerDown}
      onpointermove={onPointerMove}
      onpointerup={onPointerUp}
      onpointercancel={onPointerUp}
      draggable="false"
      title="Click or drag to pan camera"
    />
    {#if frustumPointsAttr}
      <!-- Frustum-corner polygon. viewBox is [0,1]×[0,1] so the
           normalized (u,v) frustumPts numbers map directly to viewBox coords;
           the rendered SVG inherits the overlay's pixel size. preserveAspectRatio
           is "none" so the polygon stretches to match the IMAGE's aspect (which
           the overlay's height var already accounts for). pointer-events-none
           keeps clicks routing to the underlying <img> for click-to-pan.
           drop-shadow lifts the line off busy minimap textures without
           needing a heavier stroke. -->
      <svg
        class="absolute inset-0 w-full h-full pointer-events-none [filter:drop-shadow(0_0_1px_rgba(0,0,0,0.85))]"
        viewBox="0 0 1 1"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <polygon
          points={frustumPointsAttr}
          fill="none"
          stroke="#fbbf24"
          stroke-width="1.5"
          stroke-linejoin="round"
          vector-effect="non-scaling-stroke"
          opacity="0.85"
        />
      </svg>
    {/if}
    {#if generated}
      <!-- Subtle badge marking this as a synthesized terrain preview rather
           than the map's own baked minimap. Bottom-left so it stays clear of
           the typical camera frustum and click target. -->
      <div
        class="absolute left-1 bottom-1 px-1 py-px rounded-sm bg-black/55 text-[9px] leading-none uppercase tracking-wide text-white/80 pointer-events-none select-none"
        title="Auto-generated from terrain — this map has no baked minimap"
      >
        auto
      </div>
    {/if}
  {:else}
    <div
      class="flex items-center justify-center w-full h-full min-h-[80px] text-muted-foreground text-[11px] text-center px-2 box-border"
      title="This map has no baked minimap image"
    >
      No minimap
    </div>
  {/if}
</div>
