<script lang="ts">
  // Modal for the in-app updater. Used in two flows that share one surface:
  //  • auto-check on launch found a newer release  → opens showing the update
  //  • the user picked Help → Check for Updates…    → opens showing either an
  //    available update or a "you're up to date" confirmation.
  // App.svelte owns the actual check/download calls and the persisted prefs;
  // this stays a presentational dialog driven by props + callbacks (matching
  // NewMapDialog's "collect intent, fire a callback" convention).
  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import type { update } from '../wailsjs/go/models'

  let {
    open = $bindable(false),
    result = null,
    current = 'dev',
    downloading = false,
    downloadPct = 0,
    error = '',
    autoCheck = $bindable(true),
    onClose,
    onUpdate,
  }: {
    open?: boolean
    result?: update.CheckResult | null
    current?: string
    downloading?: boolean
    downloadPct?: number
    error?: string
    autoCheck?: boolean
    onClose?: () => void
    onUpdate?: () => void
  } = $props()

  const hasUpdate = $derived(!!result?.isNewer && !!result?.release?.installer)
  const latest = $derived(result?.latest ?? '')
  const notes = $derived(result?.release?.notes?.trim() ?? '')
  const sizeMB = $derived(
    result?.release?.installer?.size
      ? (result.release.installer.size / (1024 * 1024)).toFixed(1)
      : null,
  )

  // Close-transition hook for the caller (clears App.svelte's show flag).
  let wasOpen = false
  $effect(() => {
    if (wasOpen && !open) onClose?.()
    wasOpen = open
  })
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="w-[min(520px,calc(100%-2rem))] max-w-[min(520px,calc(100%-2rem))] gap-0 p-0 overflow-hidden">
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>
        {#if hasUpdate}Update available{:else}Check for updates{/if}
      </Dialog.Title>
      <Dialog.Description class="sr-only">
        Check for and install wc3-forge updates.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex flex-col gap-4 p-5">
      {#if error}
        <p class="text-sm text-red-400">
          Couldn't check for updates: {error}
        </p>
      {:else if hasUpdate}
        <p class="text-sm">
          <span class="text-muted-foreground">You're on</span>
          <code class="text-xs bg-muted px-1 py-0.5 rounded">v{current}</code>
          <span class="text-muted-foreground">—</span>
          <code class="text-xs bg-emerald-500/15 text-emerald-400 px-1 py-0.5 rounded">v{latest}</code>
          <span class="text-muted-foreground">is available{sizeMB ? ` (${sizeMB} MB)` : ''}.</span>
        </p>

        {#if notes}
          <div class="rounded-md border bg-muted/40 max-h-48 overflow-auto">
            <pre class="text-xs whitespace-pre-wrap break-words p-3 font-mono leading-relaxed">{notes}</pre>
          </div>
        {/if}

        {#if downloading}
          <div class="flex flex-col gap-1.5">
            <div class="h-2 rounded-full bg-muted overflow-hidden">
              <div class="h-full bg-emerald-500 transition-all duration-150" style="width: {Math.max(2, downloadPct)}%"></div>
            </div>
            <p class="text-xs text-muted-foreground">
              Downloading installer… {downloadPct.toFixed(0)}%
            </p>
          </div>
        {:else}
          <p class="text-xs text-muted-foreground">
            Clicking Update downloads the installer and relaunches it. wc3-forge will close to
            finish the upgrade — save your work first.
          </p>
        {/if}
      {:else if result}
        <p class="text-sm">
          You're on the latest version
          (<code class="text-xs bg-muted px-1 py-0.5 rounded">v{current}</code>).
        </p>
      {:else}
        <p class="text-sm text-muted-foreground">Checking for updates…</p>
      {/if}

      <!-- Update preferences (persisted by App.svelte in localStorage). -->
      <div class="flex flex-col gap-2 pt-1 border-t">
        <label class="flex items-center gap-2 text-sm pt-2 cursor-pointer">
          <input type="checkbox" bind:checked={autoCheck} disabled={downloading} />
          <span>Check for updates automatically on launch</span>
        </label>
      </div>
    </div>

    <Dialog.Footer class="px-4 py-3 border-t">
      {#if hasUpdate}
        <Button variant="ghost" onclick={() => (open = false)} disabled={downloading}>Later</Button>
        <Button onclick={() => onUpdate?.()} disabled={downloading}>
          {downloading ? 'Downloading…' : 'Update Now'}
        </Button>
      {:else}
        <Button onclick={() => (open = false)}>Close</Button>
      {/if}
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
