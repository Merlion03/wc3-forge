<script lang="ts">
  // Shown at startup when no usable Warcraft III install (a CASC root with
  // .build.info) is found. Soft warning, not a hard block: maps that bundle
  // their own assets still open — only STOCK art/data comes from CASC. The
  // user can point at their install (validated + persisted + CASC remounted
  // by SetWC3InstallPath) or continue without stock assets.

  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import { BrowseForWC3Install, SetWC3InstallPath } from '../wailsjs/go/main/App.js'
  import { showToast } from './toast'

  let {
    open = $bindable(false),
    checkedPath = '',
  }: {
    open?: boolean
    /** The path that was probed and found to lack .build.info. */
    checkedPath?: string
  } = $props()

  let locating: boolean = $state(false)

  async function locate() {
    if (locating) return
    locating = true
    try {
      const picked = await BrowseForWC3Install()
      if (!picked) return // cancelled
      const status = await SetWC3InstallPath(picked)
      if (status.available) {
        showToast('Warcraft III install set — reload the map to see stock assets.', 'info')
        open = false
      }
    } catch (e) {
      showToast(
        `Couldn't use that folder: ${e instanceof Error ? e.message : String(e)}`,
        'error',
      )
    } finally {
      locating = false
    }
  }

  function continueAnyway() {
    open = false
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="!max-w-[520px]">
    <Dialog.Header>
      <Dialog.Title>Warcraft III not found</Dialog.Title>
      <Dialog.Description>
        wc3-forge couldn't find a Warcraft III installation, so stock models,
        textures, and game data won't load.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex flex-col gap-3 py-2 text-sm">
      <p class="text-muted-foreground">
        Maps that bundle their own assets still open fine — but anything using
        stock art (units, doodads, tilesets, UI icons) renders empty until you
        point wc3-forge at your install.
      </p>
      {#if checkedPath}
        <p class="text-xs text-muted-foreground">
          Looked in:
          <code class="bg-muted px-1 py-0.5 rounded break-all">{checkedPath}</code>
        </p>
      {/if}
      <p class="text-xs text-muted-foreground">
        Pick the install <strong>root</strong> — the folder containing
        <code class="bg-muted px-1 py-0.5 rounded">.build.info</code>, not a
        subfolder like <code class="bg-muted px-1 py-0.5 rounded">_retail_</code>.
      </p>
    </div>

    <Dialog.Footer>
      <Button variant="ghost" onclick={continueAnyway} disabled={locating}>
        Continue anyway
      </Button>
      <Button onclick={locate} disabled={locating}>
        {locating ? 'Opening…' : 'Locate installation…'}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
