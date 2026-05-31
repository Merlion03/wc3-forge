<script lang="ts">
  // Modal dialog for "New Map" — collects the starter-map settings (name,
  // dimensions, tileset) and hands them back via onCreate. The actual file
  // creation (a Save-location picker + the Go CreateNewMap call) lives in
  // App.svelte so this stays a pure config form, matching the other dialogs'
  // "collect options, fire a callback" convention.
  import { ListTilesets } from '../wailsjs/go/main/App.js'
  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import { Label } from '$lib/components/ui/label'

  let {
    open = $bindable(false),
    busy = false,
    onClose,
    onCreate,
  }: {
    open?: boolean
    // True while App.svelte is running the create flow (Save picker + Go call);
    // disables the form so the user can't double-submit.
    busy?: boolean
    onClose?: () => void
    onCreate?: (cfg: { name: string; width: number; height: number; tileset: string }) => void
  } = $props()

  let name = $state('Untitled')
  let width = $state(64)
  let height = $state(64)
  let tileset = $state('L')

  // Playable dimensions offered in the dropdowns (tiles). Matches the WC3
  // World Editor's common sizes; the Go side accepts anything in [8, 480].
  const SIZES = [32, 64, 96, 128, 160, 192, 256]

  // Standard WC3 tilesets keyed by their single-letter code (the byte stored
  // in war3map.w3i + .w3e). The Go side derives each tileset's ground/cliff
  // palette from this letter via the CASC SLKs, so the frontend only needs the
  // letter + a display name. This static list is the fallback ordering shown
  // before ListTilesets resolves (and the only source when CASC is unavailable
  // — e.g. no WC3 install). Once the catalog loads we prefer its SLK-accurate
  // names + thumbnails, keyed by letter.
  const TILESETS: Array<{ letter: string; name: string }> = [
    { letter: 'L', name: 'Lordaeron Summer' },
    { letter: 'F', name: 'Lordaeron Fall' },
    { letter: 'W', name: 'Lordaeron Winter' },
    { letter: 'A', name: 'Ashenvale' },
    { letter: 'B', name: 'Barrens' },
    { letter: 'C', name: 'Felwood' },
    { letter: 'N', name: 'Northrend' },
    { letter: 'I', name: 'Icecrown Glacier' },
    { letter: 'D', name: 'Dungeon' },
    { letter: 'G', name: 'Underground' },
    { letter: 'Y', name: 'Cityscape' },
    { letter: 'X', name: 'Dalaran' },
    { letter: 'J', name: 'Dalaran Ruins' },
    { letter: 'V', name: 'Village' },
    { letter: 'Q', name: 'Village Fall' },
    { letter: 'Z', name: 'Sunken Ruins' },
    { letter: 'K', name: 'Black Citadel' },
    { letter: 'O', name: 'Outland' },
  ]

  // One entry in the scrollable tileset picker: letter code, display name, and
  // up to a few ground-tile thumbnails (data-URL PNGs) to preview the palette.
  interface TilesetChoice {
    letter: string
    name: string
    thumbs: string[]
  }

  // Catalog from Go's ListTilesets (ground/cliff palettes + baked thumbnails).
  // Empty until the first load resolves; loadTilesets fills it on dialog open.
  // The Wails-generated DTO types `color` loosely, so we only read the fields
  // we need (letter/name/ground[].thumb) via a minimal shape.
  type CatalogTileset = { letter: string; name: string; ground: Array<{ thumb: string }> }
  let catalog: CatalogTileset[] = $state([])
  let loadingTilesets = $state(false)

  // The list the picker renders: catalog (with thumbnails) when available,
  // otherwise the static name-only fallback. Preserves the static WC3-WE
  // ordering by walking TILESETS first, then appending any catalog-only
  // tilesets (custom letters) the static list doesn't know about.
  let choices: TilesetChoice[] = $derived.by(() => {
    const byLetter = new Map(catalog.map((t) => [t.letter, t]))
    const seen = new Set<string>()
    const out: TilesetChoice[] = []
    for (const s of TILESETS) {
      const c = byLetter.get(s.letter)
      seen.add(s.letter)
      out.push({
        letter: s.letter,
        // Prefer the SLK-accurate catalog name once loaded; the static names
        // have a couple of legacy mislabels.
        name: c?.name || s.name,
        thumbs: (c?.ground ?? []).map((g) => g.thumb).filter(Boolean).slice(0, 4),
      })
    }
    for (const c of catalog) {
      if (seen.has(c.letter)) continue
      out.push({
        letter: c.letter,
        name: c.name || `Tileset (${c.letter})`,
        thumbs: (c.ground ?? []).map((g) => g.thumb).filter(Boolean).slice(0, 4),
      })
    }
    return out
  })

  async function loadTilesets() {
    if (catalog.length > 0 || loadingTilesets) return
    loadingTilesets = true
    try {
      const cat = (await ListTilesets()) as unknown as CatalogTileset[]
      catalog = cat ?? []
    } catch {
      // CASC unavailable (no WC3 install, etc.) — keep the name-only fallback.
      catalog = []
    } finally {
      loadingTilesets = false
    }
  }

  const inputClass =
    'flex h-9 rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm ' +
    'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring ' +
    'disabled:cursor-not-allowed disabled:opacity-50'

  function create() {
    const trimmed = name.trim() || 'Untitled'
    onCreate?.({ name: trimmed, width, height, tileset })
  }

  // Close-transition hook for the caller (clears App.svelte's show flag), plus
  // a load trigger on open so thumbnails are fetched lazily (and only once).
  let wasOpen = false
  $effect(() => {
    if (open && !wasOpen) void loadTilesets()
    if (wasOpen && !open) onClose?.()
    wasOpen = open
  })
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="w-[min(520px,calc(100%-2rem))] max-w-[min(520px,calc(100%-2rem))] gap-0 p-0 overflow-hidden">
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>New Map</Dialog.Title>
      <Dialog.Description class="sr-only">
        Create a new blank Warcraft III map.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex flex-col gap-4 p-5">
      <p class="text-sm text-muted-foreground">
        Creates a flat, single-tileset map with no units or doodads. You'll pick
        where to save the <code class="text-xs bg-muted px-1 py-0.5 rounded">.w3x</code>
        next.
      </p>

      <div class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-3 items-center">
        <Label for="newmap-name" class="shrink-0">Name</Label>
        <input
          id="newmap-name"
          type="text"
          class="{inputClass} w-full"
          bind:value={name}
          disabled={busy}
          spellcheck="false"
          autocomplete="off"
        />

        <Label for="newmap-width" class="shrink-0">Size</Label>
        <div class="flex items-center gap-2">
          <select id="newmap-width" class="{inputClass} w-24" bind:value={width} disabled={busy}>
            {#each SIZES as s}<option value={s}>{s}</option>{/each}
          </select>
          <span class="text-muted-foreground text-sm">×</span>
          <select id="newmap-height" class="{inputClass} w-24" bind:value={height} disabled={busy}>
            {#each SIZES as s}<option value={s}>{s}</option>{/each}
          </select>
          <span class="text-xs text-muted-foreground">tiles</span>
        </div>
      </div>

      <div class="flex flex-col gap-1.5">
        <div class="flex items-center justify-between">
          <Label class="shrink-0">Tileset</Label>
          {#if loadingTilesets}
            <span class="text-xs text-muted-foreground">Loading previews…</span>
          {/if}
        </div>
        <div
          role="listbox"
          aria-label="Tileset"
          tabindex="-1"
          class="max-h-72 overflow-y-auto rounded-md border border-input bg-background p-1 flex flex-col gap-0.5"
        >
          {#each choices as t (t.letter)}
            <button
              type="button"
              role="option"
              aria-selected={tileset === t.letter}
              disabled={busy}
              onclick={() => (tileset = t.letter)}
              class="flex items-center gap-3 rounded px-2 py-1.5 text-left transition-colors
                disabled:cursor-not-allowed disabled:opacity-50
                {tileset === t.letter
                  ? 'bg-primary/15 ring-1 ring-primary/40'
                  : 'hover:bg-muted'}"
            >
              <div class="flex shrink-0 gap-0.5">
                {#if t.thumbs.length > 0}
                  {#each t.thumbs as src}
                    <img
                      {src}
                      alt=""
                      class="w-7 h-7 rounded-sm border border-border/60 object-cover"
                      style="image-rendering:pixelated"
                    />
                  {/each}
                {:else}
                  <span class="w-7 h-7 rounded-sm border border-border/60 bg-muted"></span>
                {/if}
              </div>
              <span class="flex-1 text-sm truncate">{t.name}</span>
              <span class="text-[10px] font-mono text-muted-foreground shrink-0">{t.letter}</span>
            </button>
          {/each}
        </div>
      </div>
    </div>

    <Dialog.Footer class="px-4 py-3 border-t">
      <Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
      <Button onclick={create} disabled={busy}>
        {busy ? 'Creating…' : 'Create Map…'}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
