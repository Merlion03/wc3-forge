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
  // Camera-frustum overlay is intentionally NOT drawn (deferred). The HTML
  // overlay updates the image once per map-load; a per-frame frustum overlay
  // would need RAF + canvas redraws and adds polish without changing the
  // core "click to navigate" workflow.

  import { onMount, onDestroy } from 'svelte'
  import { GetMinimapBytes, GetTerrain } from '../wailsjs/go/main/App.js'
  import { decodeImageBytes } from './icon-loader'
  import type { SceneAPI } from './scene-instances'

  // The scene API — passed from App.svelte so click-to-pan can drive the
  // 3D camera. Optional because the parent gates the mount on `scene` being
  // non-null too, but the type allows null defensively in case of mount races.
  export let scene: SceneAPI | null = null
  // Bumped from App.svelte whenever the loaded map changes — drives the
  // image reload + terrain coordinate refresh. Reactive trigger; the value
  // itself is ignored (just a generation counter).
  export let mapLoadGen: number = 0

  // The image-loading state. dataURL is the result of decoding the bytes
  // returned by GetMinimapBytes; "" means we have no image (either no map
  // loaded, no baked preview in the map, or decode failed).
  let dataURL: string = ''
  // The image's natural aspect ratio. Defaults to 1:1 (square); updated once
  // the <img> reports its natural dimensions. Non-square preview images (rare)
  // get correctly proportioned via CSS aspect-ratio.
  let aspect: number = 1
  let imgEl: HTMLImageElement | null = null
  // Terrain extents — captured at map-load so click-to-pan can convert from
  // minimap-pixel-space → world-coord. Mirrors the GetTerrain DTO fields
  // we actually need; cached here so we don't pay the IPC cost per click.
  let terrainW: number = 0   // vertex count along X
  let terrainH: number = 0   // vertex count along Y
  let centerOffset: [number, number] = [0, 0]   // game coords of vertex (0,0)
  // STUDS_PER_CELL: each terrain cell is 128 game-coord units wide. Total
  // playable extent in studs = (W-1) × STUDS_PER_CELL, same convention used
  // throughout the renderer.
  const STUDS_PER_CELL = 128
  // Reload generation tracking — when mapLoadGen changes we kick a new
  // reload(). Tracking the last applied value separately so reactive blocks
  // don't double-fire on the same generation.
  let lastAppliedGen: number = -1

  // Hot-path reactive: whenever mapLoadGen advances, reload the minimap.
  $: if (mapLoadGen !== lastAppliedGen) {
    lastAppliedGen = mapLoadGen
    void reload()
  }

  async function reload() {
    dataURL = ''
    aspect = 1
    try {
      const dto = await GetMinimapBytes()
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

  // Click-to-pan: convert minimap-pixel coords → world game-coord, then
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
  function onMinimapClick(e: MouseEvent) {
    if (!scene || !imgEl) return
    if (terrainW <= 1 || terrainH <= 1) return
    const rect = imgEl.getBoundingClientRect()
    if (rect.width <= 0 || rect.height <= 0) return
    const u = (e.clientX - rect.left) / rect.width   // 0 = left edge
    const v = (e.clientY - rect.top) / rect.height   // 0 = top edge (image space)
    if (u < 0 || u > 1 || v < 0 || v > 1) return
    const worldW = (terrainW - 1) * STUDS_PER_CELL
    const worldH = (terrainH - 1) * STUDS_PER_CELL
    // Image Y is top-down; world Y is bottom-up. Flip v before mapping.
    const x = centerOffset[0] + u * worldW
    const y = centerOffset[1] + (1 - v) * worldH
    scene.panTo(x, y)
  }

  onMount(() => {
    // Initial load when the component mounts. mapLoadGen=0 is a valid
    // generation (map may already be loaded by the time we mount), so kick
    // an explicit first reload here independently of the reactive block.
    void reload()
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
          w.go.main.App.LogJS(`[Minimap] click u=${cu} v=${cv} -> world x=${x.toFixed(1)} y=${y.toFixed(1)}`)
        }
      } catch {}
      return { x, y }
    }
  })

  onDestroy(() => {
    // No subscriptions to tear down — App.svelte owns the toggle state and
    // we just unmount when it flips false.
    try { delete (window as any).__minimapClick } catch {}
  })
</script>

<div class="minimap-overlay" style="--aspect: {aspect};">
  {#if dataURL}
    <img class="minimap-img"
         src={dataURL}
         alt="Map minimap"
         bind:this={imgEl}
         on:load={onImgLoad}
         on:click={onMinimapClick}
         title="Click to pan camera to this location" />
  {:else}
    <div class="minimap-placeholder" title="This map has no baked minimap image">
      No minimap
    </div>
  {/if}
</div>

<style>
  /* Bottom-right anchor — overlays the viewport, doesn't take space in the
     grid. The viewport has position:relative (defined in App.svelte) so we
     anchor to its bottom-right corner via absolute positioning. */
  .minimap-overlay {
    position: absolute;
    right: 12px;
    bottom: 12px;
    width: 192px;
    /* Square by default; height tracks the image's natural aspect ratio so
       non-1:1 maps (e.g. 160×128 tile maps like Enfo's FFB) display undistorted. */
    height: calc(192px / var(--aspect));
    max-height: 192px;
    /* Faint dark backdrop + 1px border so the overlay is distinguishable from
       the 3D view. Slight backdrop-blur for the "glass UI" feel without
       obscuring viewport content underneath. */
    background: rgba(20, 20, 24, 0.78);
    border: 1px solid #3f3f46;
    border-radius: 4px;
    box-shadow: 0 2px 12px rgba(0, 0, 0, 0.5);
    overflow: hidden;
    z-index: 20;
    /* Prevent the overlay from interfering with WASD/keyboard input when the
       canvas has focus — clicks land on the minimap, but typing still goes
       to the viewport (no focus stealing). */
    pointer-events: auto;
  }

  .minimap-img {
    display: block;
    width: 100%;
    height: 100%;
    /* Crisp upscale: the baked preview is usually 256×256, so a 192px display
       slightly downscales. Use auto/default smoothing — pixel-art rendering
       would look blocky for what's effectively a painted image. */
    object-fit: contain;
    cursor: crosshair;
    user-select: none;
    -webkit-user-drag: none;
  }

  .minimap-placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
    height: 100%;
    min-height: 80px;
    color: #71717a;
    font-size: 11px;
    text-align: center;
    padding: 0 8px;
    box-sizing: border-box;
  }
</style>
