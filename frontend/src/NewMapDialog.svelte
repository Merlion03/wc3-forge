<script lang="ts">
  // Modal dialog for "New Map" — collects the starter-map settings (name,
  // dimensions, tileset) and hands them back via onCreate. The actual file
  // creation (a Save-location picker + the Go CreateNewMap call) lives in
  // App.svelte so this stays a pure config form, matching the other dialogs'
  // "collect options, fire a callback" convention.
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
  // letter + a display name.
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

  const inputClass =
    'flex h-9 rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm ' +
    'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring ' +
    'disabled:cursor-not-allowed disabled:opacity-50'

  function create() {
    const trimmed = name.trim() || 'Untitled'
    onCreate?.({ name: trimmed, width, height, tileset })
  }

  // Close-transition hook for the caller (clears App.svelte's show flag).
  let wasOpen = false
  $effect(() => {
    if (wasOpen && !open) onClose?.()
    wasOpen = open
  })
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="w-[min(440px,calc(100%-2rem))] max-w-[min(440px,calc(100%-2rem))] gap-0 p-0 overflow-hidden">
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

        <Label for="newmap-tileset" class="shrink-0">Tileset</Label>
        <select id="newmap-tileset" class="{inputClass} w-full" bind:value={tileset} disabled={busy}>
          {#each TILESETS as t}<option value={t.letter}>{t.name}</option>{/each}
        </select>
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
