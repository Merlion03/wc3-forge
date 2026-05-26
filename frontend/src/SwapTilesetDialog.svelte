<script lang="ts">
  // Modal dialog for HiveWE-style "Swap Tileset". User picks a new tileset
  // letter from a dropdown; the dialog shows the OLD palette on the left and
  // a row of NEW-tileset tiles on the right (ground and cliff sections).
  // For each OLD tile, the user picks which NEW-palette slot it remaps to.
  // Defaults: if a tile's FourCC exists in both tilesets, it carries over.
  // Otherwise the first slot of the new tileset is the initial pick.
  //
  // Apply sends a SwapTilesetRequest to Go, which rewrites the in-memory
  // .w3e + .w3i and returns a fresh TerrainDTO. The caller wires that to
  // the terrain renderer (terrain.ts re-init) and minimap repaint.

  import { tick } from 'svelte'
  import {
    ListTilesets,
    GetTerrain,
    SwapTileset,
  } from '../wailsjs/go/main/App.js'
  import { showToast } from './toast'
  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import { Label } from '$lib/components/ui/label'

  interface TileInfo {
    fourcc: string
    name: string
    texture: string
    color: [number, number, number]
    thumb: string
  }
  interface TilesetInfo {
    letter: string
    name: string
    ground: TileInfo[]
    cliff: TileInfo[]
  }

  let {
    open = $bindable(false),
    onClose,
    onApplied,
  }: {
    open?: boolean
    onClose?: () => void
    onApplied?: () => void | Promise<void>
  } = $props()

  let loading: boolean = $state(false)
  let applying: boolean = $state(false)

  // Catalog: every available tileset (ground + cliff palettes) from CASC.
  let catalog: TilesetInfo[] = $state([])

  // Current map's tileset and palettes — the OLD side of the swap.
  let oldLetter: string = $state('')
  let oldGround: string[] = $state([])
  let oldCliff: string[] = $state([])

  // The target tileset the user has selected. Drives which NEW tiles appear
  // and is what gets written into the .w3e/.w3i tileset byte on Apply.
  let newLetter: string = $state('')
  let newTileset: TilesetInfo | null = $derived(
    catalog.find((t) => t.letter === newLetter) ?? null,
  )

  // Per-old-slot mapping into the NEW palette. Reset whenever newLetter
  // changes — old picks aren't meaningful against a different palette.
  let groundFromTo: number[] = $state([])
  let cliffFromTo: number[] = $state([])

  // Lookup helpers for the OLD palettes so we can show "Old: <FourCC>" labels
  // and look up colors for the swatches.
  function findTile(letter: string, fourcc: string, kind: 'ground' | 'cliff'): TileInfo | null {
    const ts = catalog.find((t) => t.letter === letter)
    if (!ts) return null
    const list = kind === 'ground' ? ts.ground : ts.cliff
    return list.find((t) => t.fourcc === fourcc) ?? null
  }

  async function loadEverything() {
    loading = true
    try {
      // Run both fetches in parallel; ListTilesets is the slow one (CASC
      // SLK load on first call). GetTerrain returns the current palettes.
      const [cat, terrain] = await Promise.all([
        ListTilesets() as Promise<TilesetInfo[]>,
        GetTerrain() as Promise<any>,
      ])
      catalog = cat ?? []
      oldGround = (terrain?.palette ?? []) as string[]
      oldCliff = (terrain?.cliff_palette ?? []) as string[]
      const ts = (terrain?.tileset ?? '') as string
      oldLetter = ts
      // Default the target to the current tileset — common case is the user
      // opens the dialog by mistake or wants to re-order palette slots.
      newLetter = ts || (catalog[0]?.letter ?? '')
      resetMappings()
    } catch (e) {
      showToast('Could not load tilesets: ' + String(e), 'error')
      open = false
    } finally {
      loading = false
    }
  }

  // Whenever newLetter (or first load) changes, default the per-tile picks:
  //   - if the old FourCC exists in the new palette, pick that slot
  //   - otherwise pick slot 0
  function resetMappings() {
    const target = catalog.find((t) => t.letter === newLetter)
    const ngList = target?.ground ?? []
    const ncList = target?.cliff ?? []
    groundFromTo = oldGround.map((fc) => {
      const i = ngList.findIndex((t) => t.fourcc === fc)
      return i >= 0 ? i : 0
    })
    cliffFromTo = oldCliff.map((fc) => {
      const i = ncList.findIndex((t) => t.fourcc === fc)
      return i >= 0 ? i : 0
    })
  }

  // React to dialog open transitions (mirrors MapInfoEditor's pattern).
  let lastOpen = $state(false)
  $effect(() => {
    if (open && !lastOpen) {
      lastOpen = true
      void loadEverything()
    } else if (!open && lastOpen) {
      lastOpen = false
      onClose?.()
    }
  })

  // React to newLetter changes after initial load. Avoid the no-op first run
  // (catalog empty) by gating on newTileset != null && we already have an old
  // palette to remap.
  let lastNewLetter = $state('')
  $effect(() => {
    if (!loading && newTileset && newLetter !== lastNewLetter) {
      lastNewLetter = newLetter
      resetMappings()
    }
  })

  // The picked mapping is "trivial" (no change) exactly when:
  //   - newLetter == oldLetter
  //   - the new palette is the same list of FourCCs in the same order
  //   - every groundFromTo[i] == i and every cliffFromTo[i] == i
  //
  // Disable Apply in that case so the user doesn't dirty the map for nothing.
  let isUnchanged = $derived.by(() => {
    if (!newTileset) return true
    if (newLetter !== oldLetter) return false
    const ng = newTileset.ground.map((t) => t.fourcc)
    const nc = newTileset.cliff.map((t) => t.fourcc)
    if (ng.length !== oldGround.length || nc.length !== oldCliff.length) {
      return false
    }
    for (let i = 0; i < ng.length; i++) if (ng[i] !== oldGround[i]) return false
    for (let i = 0; i < nc.length; i++) if (nc[i] !== oldCliff[i]) return false
    for (let i = 0; i < groundFromTo.length; i++) if (groundFromTo[i] !== i) return false
    for (let i = 0; i < cliffFromTo.length; i++) if (cliffFromTo[i] !== i) return false
    return true
  })

  async function applySwap() {
    if (!newTileset) return
    applying = true
    try {
      await SwapTileset({
        newLetter,
        newGroundTilesets: newTileset.ground.map((t) => t.fourcc),
        newCliffTilesets: newTileset.cliff.map((t) => t.fourcc),
        groundFromTo,
        cliffFromTo,
      } as any)
      showToast(`Tileset swapped to ${newTileset.name}.`, 'success')
      await onApplied?.()
      open = false
    } catch (e) {
      showToast('Swap failed: ' + String(e), 'error')
    } finally {
      applying = false
    }
  }

  function onCancel() {
    open = false
  }

  function rgb(c: [number, number, number] | undefined): string {
    if (!c) return 'rgb(102,102,110)'
    return `rgb(${c[0]},${c[1]},${c[2]})`
  }
</script>

{#snippet swatch(info: TileInfo | null | undefined)}
  {#if info?.thumb}
    <img
      src={info.thumb}
      alt={info.fourcc}
      class="inline-block w-24 h-24 rounded border border-border object-cover"
      style="image-rendering:pixelated"
    />
  {:else}
    <span
      class="inline-block w-24 h-24 rounded border border-border"
      style="background:{rgb(info?.color)}"
    ></span>
  {/if}
{/snippet}

<Dialog.Root bind:open>
  <Dialog.Content
    class="w-[min(880px,calc(100%-2rem))] max-w-[min(880px,calc(100%-2rem))] sm:max-w-[min(880px,calc(100%-2rem))] gap-0 p-0 overflow-hidden"
  >
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>Swap Tileset</Dialog.Title>
      <Dialog.Description class="sr-only">
        Re-tile the map by mapping each current tile to a tile in the new tileset.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex flex-col gap-4 p-5 max-h-[calc(100vh-12rem)] overflow-y-auto min-h-[420px]">
      {#if loading}
        <div class="text-center text-muted-foreground py-16 text-sm">
          Loading tilesets…
        </div>
      {:else}
        <div class="flex items-center gap-3">
          <Label for="swap-target" class="shrink-0">New tileset</Label>
          <select
            id="swap-target"
            class="flex h-9 rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
            bind:value={newLetter}
          >
            {#each catalog as t (t.letter)}
              <option value={t.letter}>{t.name} ({t.letter})</option>
            {/each}
          </select>
          <span class="text-xs text-muted-foreground">
            Current: {catalog.find((t) => t.letter === oldLetter)?.name ?? oldLetter} ({oldLetter})
          </span>
        </div>

        <section>
          <h3 class="text-sm font-semibold mb-2">
            Ground tiles ({oldGround.length})
          </h3>
          {#if oldGround.length === 0}
            <p class="text-xs text-muted-foreground">
              No ground palette on this map (unexpected).
            </p>
          {:else if !newTileset || newTileset.ground.length === 0}
            <p class="text-xs text-destructive">
              Target tileset "{newLetter}" has no ground tiles.
            </p>
          {:else}
            <div class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5 items-center">
              {#each oldGround as oldFC, i (oldFC)}
                {@const oldInfo = findTile(oldLetter, oldFC, 'ground')}
                <div class="flex items-center gap-2 min-w-[200px]">
                  {@render swatch(oldInfo)}
                  <span class="text-sm font-mono">{oldFC}</span>
                  <span class="text-xs text-muted-foreground truncate">
                    {oldInfo?.name ?? ''}
                  </span>
                </div>
                <div class="flex items-center gap-2">
                  <span class="text-xs text-muted-foreground">→</span>
                  {@render swatch(newTileset.ground[groundFromTo[i] ?? 0])}
                  <select
                    class="flex h-8 rounded-md border border-input bg-background px-2 py-1 text-xs shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                    bind:value={groundFromTo[i]}
                  >
                    {#each newTileset.ground as nt, j (nt.fourcc)}
                      <option value={j}>{nt.fourcc} — {nt.name}</option>
                    {/each}
                  </select>
                </div>
              {/each}
            </div>
          {/if}
        </section>

        {#if oldCliff.length > 0}
          <section>
            <h3 class="text-sm font-semibold mb-2">
              Cliff tiles ({oldCliff.length})
            </h3>
            {#if !newTileset || newTileset.cliff.length === 0}
              <p class="text-xs text-destructive">
                Target tileset "{newLetter}" has no cliff tiles. Pick a tileset
                with cliffs or remove cliffs from the map first.
              </p>
            {:else}
              <div class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5 items-center">
                {#each oldCliff as oldFC, i (oldFC)}
                  {@const oldInfo = findTile(oldLetter, oldFC, 'cliff')}
                  <div class="flex items-center gap-2 min-w-[200px]">
                    {@render swatch(oldInfo)}
                    <span class="text-sm font-mono">{oldFC}</span>
                    <span class="text-xs text-muted-foreground truncate">
                      {oldInfo?.name ?? ''}
                    </span>
                  </div>
                  <div class="flex items-center gap-2">
                    <span class="text-xs text-muted-foreground">→</span>
                    {@render swatch(newTileset.cliff[cliffFromTo[i] ?? 0])}
                    <select
                      class="flex h-8 rounded-md border border-input bg-background px-2 py-1 text-xs shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                      bind:value={cliffFromTo[i]}
                    >
                      {#each newTileset.cliff as nt, j (nt.fourcc)}
                        <option value={j}>{nt.fourcc} — {nt.name}</option>
                      {/each}
                    </select>
                  </div>
                {/each}
              </div>
            {/if}
          </section>
        {/if}
      {/if}
    </div>

    <Dialog.Footer
      class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0"
    >
      <Button variant="ghost" onclick={onCancel} disabled={applying}>
        Cancel
      </Button>
      <Button
        onclick={applySwap}
        disabled={
          loading ||
          applying ||
          !newTileset ||
          isUnchanged ||
          (oldCliff.length > 0 && (newTileset?.cliff.length ?? 0) === 0)
        }
      >
        {applying ? 'Applying…' : 'Apply'}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
