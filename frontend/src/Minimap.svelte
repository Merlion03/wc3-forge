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
  //
  // Visibility is owned by App.svelte (which gates the mount), as is the
  // global 'M' hotkey — this component just renders when mounted.

  import { onMount, onDestroy } from 'svelte'
  import { GetMinimapBytes, GetTerrain } from '../wailsjs/go/main/App.js'
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

  // Hot-path reactive: whenever mapLoadGen advances, reload the minimap.
  $effect(() => {
    if (mapLoadGen !== lastAppliedGen) {
      lastAppliedGen = mapLoadGen
      void reload()
    }
  })

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
    const u = (e.clientX - rect.left) / rect.width // 0 = left edge
    const v = (e.clientY - rect.top) / rect.height // 0 = top edge (image space)
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
          w.go.main.App.LogJS(
            `[Minimap] click u=${cu} v=${cv} -> world x=${x.toFixed(1)} y=${y.toFixed(1)}`,
          )
        }
      } catch {}
      return { x, y }
    }
  })

  onDestroy(() => {
    // No subscriptions to tear down — App.svelte owns the toggle state and
    // we just unmount when it flips false.
    try {
      delete (window as any).__minimapClick
    } catch {}
  })
</script>

<!-- Bottom-right anchor — overlays the viewport, doesn't take space in the
     grid. The viewport has position:relative (defined in App.svelte) so we
     anchor to its bottom-right corner via absolute positioning. The
     overlay's height tracks the image's natural aspect ratio so non-1:1
     maps (e.g. 160×128 tile maps like Enfo's FFB) display undistorted.
     pointer-events:auto so clicks land on the minimap, but typing still
     goes to the viewport (no focus stealing). -->
<div
  class="absolute right-3 bottom-3 w-48 max-h-48 bg-card/80 border border-border rounded shadow-lg overflow-hidden z-20 pointer-events-auto backdrop-blur-sm"
  style="height: calc(192px / {aspect});"
>
  {#if dataURL}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <img
      class="block w-full h-full object-contain cursor-crosshair select-none"
      src={dataURL}
      alt="Map minimap"
      bind:this={imgEl}
      onload={onImgLoad}
      onclick={onMinimapClick}
      draggable="false"
      title="Click to pan camera to this location"
    />
  {:else}
    <div
      class="flex items-center justify-center w-full h-full min-h-[80px] text-muted-foreground text-[11px] text-center px-2 box-border"
      title="This map has no baked minimap image"
    >
      No minimap
    </div>
  {/if}
</div>
