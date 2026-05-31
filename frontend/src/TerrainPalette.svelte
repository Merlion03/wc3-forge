<script lang="ts">
  // Floating terrain palette. A "mountain" launcher pinned bottom-left (shown
  // in Terrain Mode, where it takes the doodad palette's slot) toggles a panel
  // with three tool groups — Texture
  // (paint a ground tile), Height (raise / lower / flatten / smooth), and Cliffs
  // (raise / lower a cliff level, toggle ramp) — plus shared brush controls
  // (size, circle/square shape, strength).
  //
  // Like DoodadPalette this is a pure presentation layer: it owns its own UI
  // state and reports the currently-armed brush via onChange. App.svelte does
  // the scene wiring (paint mode, undo grouping, the Go brush calls) so all the
  // mutation logic stays in one place. The ground + cliff tile catalog comes
  // straight from GetTerrain (the TerrainDTO already carries palette FourCCs,
  // average RGB swatch colors, and texture path stems).
  import { GetTerrain } from '../wailsjs/go/main/App.js'
  import { EventsOn } from '../wailsjs/runtime/runtime.js'
  import MountainIcon from '@lucide/svelte/icons/mountain'
  import XIcon from '@lucide/svelte/icons/x'

  // The brush descriptor reported to App. tool drives which Go brush runs;
  // App reads the rest per-tool (paint → groundTileId; cliff → cliffTileId/level;
  // height → strength). null = nothing armed (scene leaves brush mode).
  export type TerrainTool =
    | 'paint'
    | 'raise' | 'lower' | 'flatten' | 'smooth'
    | 'cliffRaise' | 'cliffLower'
    | 'ramp' | 'rampOff'
  export interface TerrainBrush {
    tool: TerrainTool
    radius: number
    shape: 'circle' | 'square'
    strength: number
    groundTileId: string
    cliffTileId: string
  }

  let {
    loaded: mapLoaded = false,
    onChange,
  }: {
    // Whether a map is loaded — arming needs a target. Drives the empty hint.
    loaded?: boolean
    // Fires whenever the armed brush changes (tool/size/shape/strength/tile) or
    // is cleared (null). App mirrors this to the scene + the brush calls.
    onChange: (brush: TerrainBrush | null) => void
  } = $props()

  interface TileEntry { fourcc: string; color: string; texture: string }

  let open = $state(false)
  let loading = $state(false)
  let ground: TileEntry[] = $state([])
  let cliffs: TileEntry[] = $state([])

  // Armed state.
  let activeTool: TerrainTool | null = $state(null)
  let selectedGround = $state('')
  let selectedCliff = $state('')
  let radius = $state(1)
  let shape: 'circle' | 'square' = $state('circle')
  let strength = $state(16)

  function rgb(c: number[] | undefined): string {
    if (!c || c.length < 3) return 'rgb(110,110,110)'
    return `rgb(${c[0] | 0}, ${c[1] | 0}, ${c[2] | 0})`
  }

  async function refresh() {
    loading = true
    try {
      const t = (await GetTerrain()) as any
      const pal: string[] = t?.palette ?? []
      const palColors: number[][] = t?.palette_colors ?? []
      const palTex: string[] = t?.palette_textures ?? []
      ground = pal.map((fourcc, i) => ({
        fourcc,
        color: rgb(palColors[i]),
        texture: palTex[i] ?? '',
      }))
      const cpal: string[] = t?.cliff_palette ?? []
      const cpalTex: string[] = t?.cliff_palette_textures ?? []
      cliffs = cpal.map((fourcc, i) => ({
        fourcc,
        // Cliffs don't ship an average color in the DTO; reuse a neutral stone
        // swatch so the grid still reads as pickable tiles.
        color: 'rgb(120,116,110)',
        texture: cpalTex[i] ?? '',
      }))
      // Default the selected tiles so the first paint/cliff click just works.
      if (!selectedGround && ground.length) selectedGround = ground[0].fourcc
      if (!selectedCliff && cliffs.length) selectedCliff = cliffs[0].fourcc
    } catch {
      ground = []
      cliffs = []
    } finally {
      loading = false
    }
  }

  function toggleOpen() {
    open = !open
    if (open) {
      void refresh()
    } else {
      // Closing the panel disarms — leaving an invisible brush armed would be
      // a surprising way to keep editing terrain.
      activeTool = null
    }
  }

  // Refetch the palette when the loaded map changes (tilesets are per-map), but
  // only while the panel is open so we don't pay for it in the background.
  function onMapChanged() {
    if (open) void refresh()
  }
  // Capture the per-listener unsubscribe so teardown removes ONLY our handler
  // (EventsOff by name would nuke App's map-changed listeners too).
  const offMapChanged = EventsOn('wc3-forge:map-changed', onMapChanged)
  $effect(() => () => { if (typeof offMapChanged === 'function') offMapChanged() })

  // Pick a tool. Clicking the active tool again disarms it.
  function pickTool(tool: TerrainTool) {
    activeTool = activeTool === tool ? null : tool
  }
  function pickGround(fourcc: string) {
    selectedGround = fourcc
    activeTool = 'paint'
  }
  function pickCliff(fourcc: string) {
    selectedCliff = fourcc
    if (activeTool !== 'cliffRaise' && activeTool !== 'cliffLower') activeTool = 'cliffRaise'
  }

  // The armed brush, recomputed from UI state. Reported to App via the effect
  // below — onChange only mutates parent state, never this component's, so no
  // Svelte 5 self-referential effect loop.
  let brush = $derived.by<TerrainBrush | null>(() => {
    if (!activeTool) return null
    return {
      tool: activeTool,
      radius,
      shape,
      strength,
      groundTileId: selectedGround,
      cliffTileId: selectedCliff,
    }
  })
  $effect(() => {
    onChange(brush)
  })

  const HEIGHT_TOOLS: { id: TerrainTool; label: string }[] = [
    { id: 'raise', label: 'Raise' },
    { id: 'lower', label: 'Lower' },
    { id: 'flatten', label: 'Flatten' },
    { id: 'smooth', label: 'Smooth' },
  ]
  // Strength only applies to the height tools; for smooth it's a 0..1 blend.
  let showStrength = $derived(
    activeTool === 'raise' || activeTool === 'lower' || activeTool === 'smooth',
  )
  let isSmooth = $derived(activeTool === 'smooth')
</script>

<button
  class="palette-fab"
  class:active={open}
  onclick={toggleOpen}
  title={open ? 'Close terrain palette' : 'Open terrain palette — paint tiles, height & cliffs'}
  aria-label="Terrain palette"
  aria-expanded={open}
>
  <MountainIcon size={19} />
</button>

{#if open}
  <div class="palette" role="dialog" aria-label="Terrain palette">
    <div class="palette-header">
      <span class="palette-title">Terrain</span>
      <button class="palette-close" onclick={toggleOpen} title="Close" aria-label="Close palette">
        <XIcon size={15} />
      </button>
    </div>

    {#if !mapLoaded}
      <div class="palette-empty">Open a map to edit terrain.</div>
    {:else}
      {#if activeTool}
        <div class="palette-armed">
          <span class="dot"></span>
          <span class="armed-text">Drag on the map to {activeTool === 'paint' ? 'paint' : 'edit'}. <kbd>Esc</kbd> stops.</span>
          <button class="armed-cancel" onclick={() => (activeTool = null)}>Disarm</button>
        </div>
      {/if}

      <div class="palette-body">
        <!-- Texture group -->
        <div class="section">
          <div class="section-title">Texture</div>
          {#if loading}
            <div class="muted">Loading tiles…</div>
          {:else if ground.length === 0}
            <div class="muted">No ground tiles.</div>
          {:else}
            <div class="tile-grid">
              {#each ground as g (g.fourcc)}
                <button
                  class="tile"
                  class:armed={activeTool === 'paint' && selectedGround === g.fourcc}
                  title={`${g.fourcc}${g.texture ? ' — ' + g.texture : ''}`}
                  onclick={() => pickGround(g.fourcc)}
                >
                  <span class="swatch" style={`background:${g.color}`}></span>
                  <span class="tile-label">{g.fourcc}</span>
                </button>
              {/each}
            </div>
          {/if}
        </div>

        <!-- Height group -->
        <div class="section">
          <div class="section-title">Height</div>
          <div class="tool-row">
            {#each HEIGHT_TOOLS as ht (ht.id)}
              <button
                class="tool-btn"
                class:armed={activeTool === ht.id}
                onclick={() => pickTool(ht.id)}
              >{ht.label}</button>
            {/each}
          </div>
        </div>

        <!-- Cliffs group -->
        <div class="section">
          <div class="section-title">Cliffs</div>
          <div class="tool-row">
            <button class="tool-btn" class:armed={activeTool === 'cliffRaise'} onclick={() => pickTool('cliffRaise')}>Raise</button>
            <button class="tool-btn" class:armed={activeTool === 'cliffLower'} onclick={() => pickTool('cliffLower')}>Lower</button>
            <button class="tool-btn" class:armed={activeTool === 'ramp'} onclick={() => pickTool('ramp')}>Ramp</button>
            <button class="tool-btn" class:armed={activeTool === 'rampOff'} onclick={() => pickTool('rampOff')}>No Ramp</button>
          </div>
          {#if cliffs.length > 0}
            <div class="tile-grid cliff-grid">
              {#each cliffs as c (c.fourcc)}
                <button
                  class="tile"
                  class:armed={(activeTool === 'cliffRaise' || activeTool === 'cliffLower') && selectedCliff === c.fourcc}
                  title={`${c.fourcc}${c.texture ? ' — ' + c.texture : ''}`}
                  onclick={() => pickCliff(c.fourcc)}
                >
                  <span class="swatch" style={`background:${c.color}`}></span>
                  <span class="tile-label">{c.fourcc}</span>
                </button>
              {/each}
            </div>
          {/if}
        </div>

        <!-- Brush controls (shared) -->
        <div class="section brush-controls">
          <div class="section-title">Brush</div>
          <label class="ctl">
            <span>Size</span>
            <input type="range" min="0" max="10" step="0.25" bind:value={radius} />
            <span class="ctl-val">{radius.toFixed(2)}</span>
          </label>
          <div class="ctl">
            <span>Shape</span>
            <div class="seg">
              <button class:on={shape === 'circle'} onclick={() => (shape = 'circle')}>Circle</button>
              <button class:on={shape === 'square'} onclick={() => (shape = 'square')}>Square</button>
            </div>
          </div>
          {#if showStrength}
            <label class="ctl">
              <span>{isSmooth ? 'Blend' : 'Strength'}</span>
              {#if isSmooth}
                <input type="range" min="0" max="1" step="0.05" bind:value={strength} />
                <span class="ctl-val">{strength.toFixed(2)}</span>
              {:else}
                <input type="range" min="1" max="128" step="1" bind:value={strength} />
                <span class="ctl-val">{strength}</span>
              {/if}
            </label>
          {/if}
        </div>
      </div>
    {/if}
  </div>
{/if}

<style>
  .palette-fab {
    position: absolute;
    left: 12px;
    bottom: 12px;
    z-index: 40;
    width: 40px;
    height: 40px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 9999px;
    border: 1px solid var(--border);
    background: var(--secondary, var(--muted));
    color: var(--secondary-foreground, var(--foreground));
    box-shadow: 0 2px 8px rgb(0 0 0 / 0.35);
    cursor: pointer;
    transition: transform 0.12s ease, background 0.12s ease;
  }
  .palette-fab:hover { transform: translateY(-1px); }
  .palette-fab.active {
    background: var(--accent);
    color: var(--accent-foreground);
  }

  .palette {
    position: absolute;
    left: 12px;
    bottom: 60px;
    z-index: 40;
    width: 300px;
    max-height: min(600px, calc(100vh - 120px));
    display: flex;
    flex-direction: column;
    background: var(--card);
    color: var(--card-foreground);
    border: 1px solid var(--border);
    border-radius: var(--radius, 8px);
    box-shadow: 0 8px 28px rgb(0 0 0 / 0.45);
    overflow: hidden;
  }

  .palette-header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border);
  }
  .palette-title { font-size: 0.8125rem; font-weight: 600; }
  .palette-close {
    margin-left: auto;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 22px; height: 22px;
    border-radius: 4px;
    color: var(--muted-foreground);
    cursor: pointer;
  }
  .palette-close:hover { background: var(--accent); color: var(--accent-foreground); }

  .palette-empty {
    padding: 20px 12px;
    text-align: center;
    font-size: 0.8125rem;
    color: var(--muted-foreground);
  }

  .palette-armed {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 6px 10px;
    font-size: 0.75rem;
    background: color-mix(in oklch, var(--primary) 14%, transparent);
    border-bottom: 1px solid var(--border);
  }
  .palette-armed .dot { width: 8px; height: 8px; border-radius: 9999px; background: var(--primary); flex: none; }
  .palette-armed .armed-text { color: var(--foreground); }
  .palette-armed kbd {
    font-size: 0.6875rem; padding: 0 4px; border-radius: 3px;
    background: var(--muted); border: 1px solid var(--border);
  }
  .palette-armed .armed-cancel {
    margin-left: auto; font-size: 0.6875rem; color: var(--muted-foreground);
    text-decoration: underline; cursor: pointer;
  }
  .palette-armed .armed-cancel:hover { color: var(--foreground); }

  .palette-body { flex: 1; min-height: 0; overflow-y: auto; padding: 4px 0 8px; }

  .section { padding: 8px 10px; border-bottom: 1px solid color-mix(in oklch, var(--border) 60%, transparent); }
  .section-title {
    font-size: 0.6875rem; font-weight: 600; text-transform: uppercase;
    letter-spacing: 0.04em; color: var(--muted-foreground); margin-bottom: 6px;
  }
  .muted { font-size: 0.75rem; color: var(--muted-foreground); padding: 4px 0; }

  .tile-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 4px; }
  .tile {
    display: flex; flex-direction: column; align-items: center; gap: 2px;
    padding: 4px 2px; border: 1px solid transparent; border-radius: 6px;
    background: var(--background); cursor: pointer; overflow: hidden;
  }
  .tile:hover { background: var(--accent); border-color: var(--border); }
  .tile.armed { border-color: var(--primary); background: color-mix(in oklch, var(--primary) 18%, transparent); }
  .swatch {
    width: 100%; height: 28px; border-radius: 4px;
    border: 1px solid color-mix(in oklch, var(--border) 70%, transparent);
  }
  .tile-label {
    width: 100%; font-size: 0.625rem; line-height: 1.1; text-align: center;
    color: var(--muted-foreground); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }

  .tool-row { display: flex; flex-wrap: wrap; gap: 4px; }
  .tool-btn {
    flex: 1 1 auto; min-width: 56px; padding: 5px 8px; font-size: 0.75rem;
    border: 1px solid var(--border); border-radius: 6px;
    background: var(--background); color: var(--foreground); cursor: pointer;
  }
  .tool-btn:hover { background: var(--accent); }
  .tool-btn.armed { border-color: var(--primary); background: color-mix(in oklch, var(--primary) 20%, transparent); color: var(--foreground); }
  .cliff-grid { margin-top: 6px; }

  .brush-controls { border-bottom: none; }
  .ctl { display: flex; align-items: center; gap: 8px; font-size: 0.75rem; margin-bottom: 6px; }
  .ctl > span:first-child { width: 54px; color: var(--muted-foreground); }
  .ctl input[type='range'] { flex: 1; accent-color: var(--primary); }
  .ctl-val { width: 30px; text-align: right; font-variant-numeric: tabular-nums; }
  .seg { display: flex; gap: 2px; }
  .seg button {
    padding: 3px 8px; font-size: 0.6875rem; border: 1px solid var(--border);
    background: var(--background); color: var(--foreground); cursor: pointer;
  }
  .seg button:first-child { border-radius: 6px 0 0 6px; }
  .seg button:last-child { border-radius: 0 6px 6px 0; }
  .seg button.on { background: var(--primary); color: var(--primary-foreground); border-color: var(--primary); }
</style>
