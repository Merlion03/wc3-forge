<script lang="ts">
  // ConvertToLuaDialog — modernizes JASS maps to Lua in one click.
  //
  // Two visual states:
  //   1. Confirm  — when CheckConvertToLua reports no blockers. The user
  //      sees a short explanation + Convert/Cancel buttons.
  //   2. Blockers — when blockers exist. The user sees a list of triggers
  //      that need manual conversion first. No Convert button; only Close
  //      + a "Go to trigger…" link per row that opens the Trigger Editor.
  //
  // Mount-on-demand from App.svelte (same pattern as SwapTilesetDialog /
  // MapInfoEditor). The parent decides which state to render by passing
  // the blocker list in props.

  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import { ConvertMapToLua } from '../wailsjs/go/main/App.js'
  import { showToast } from './toast'

  interface Blocker {
    trigger_id: number
    trigger_name: string
    kind: string
    reason: string
  }

  let {
    open = $bindable(false),
    blockers = [] as Blocker[],
    onClose = () => {},
    onConverted = () => {},
    onOpenTrigger = (_id: number) => {},
  }: {
    open?: boolean
    blockers?: Blocker[]
    onClose?: () => void
    onConverted?: () => void | Promise<void>
    onOpenTrigger?: (id: number) => void
  } = $props()

  let applying: boolean = $state(false)

  let hasBlockers: boolean = $derived(blockers.length > 0)

  async function doConvert() {
    if (applying) return
    applying = true
    try {
      const res = await ConvertMapToLua()
      // Defensive: backend may surface late-discovered blockers (race
      // between Check and Convert). Re-render as blocker dialog.
      if (res && res.blockers && res.blockers.length > 0) {
        blockers = res.blockers
        showToast(`Cannot convert: ${res.blockers.length} blocker(s) found`, 'error')
        return
      }
      showToast('Converted to Lua. Save (Ctrl+S) to persist.', 'info')
      await onConverted()
      open = false
      onClose()
    } catch (e) {
      showToast(`Convert failed: ${e instanceof Error ? e.message : String(e)}`, 'error')
    } finally {
      applying = false
    }
  }

  function onCancel() {
    open = false
    onClose()
  }

  function goToTrigger(id: number) {
    // Caller wires this to openTriggerEditor(id). Close the dialog so the
    // Trigger Editor mounts without overlap.
    open = false
    onClose()
    onOpenTrigger(id)
  }

  // Format the blocker kind into a short human-readable badge label.
  function kindLabel(k: string): string {
    switch (k) {
      case 'script': return 'Custom-script trigger'
      case 'custom_text': return 'GUI + JASS overlay'
      case 'map_header': return 'Hand-rolled war3map.j'
      default: return k
    }
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content
    class="w-[min(640px,calc(100%-2rem))] max-w-[min(640px,calc(100%-2rem))] sm:max-w-[min(640px,calc(100%-2rem))] gap-0 p-0 overflow-hidden"
  >
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>
        {hasBlockers ? 'Cannot auto-convert this map' : 'Convert this map to Lua?'}
      </Dialog.Title>
      <Dialog.Description class="sr-only">
        {hasBlockers
          ? 'List of triggers that block automatic JASS-to-Lua conversion.'
          : 'Re-emits triggers as Lua and removes the old JASS script.'}
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex flex-col gap-3 p-5 max-h-[calc(100vh-12rem)] overflow-y-auto min-h-[160px]">
      {#if hasBlockers}
        <p class="text-sm text-muted-foreground">
          These triggers contain hand-written JASS that won't translate to Lua.
          Convert them manually first (delete them, or change the GUI structure
          + rewrite the JASS as Lua), then retry.
        </p>
        <div class="flex flex-col gap-1.5 mt-2">
          {#each blockers as b (b.trigger_id + '|' + b.kind)}
            <div class="rounded-md border border-amber-500/40 bg-amber-500/5 p-2.5">
              <div class="flex items-center justify-between gap-2">
                <div class="flex items-center gap-2 min-w-0">
                  <span class="font-mono text-sm truncate">{b.trigger_name}</span>
                  <span class="text-xs text-amber-400 shrink-0">
                    {kindLabel(b.kind)}
                  </span>
                </div>
                {#if b.kind !== 'map_header'}
                  <Button
                    variant="ghost"
                    size="sm"
                    class="shrink-0 text-xs"
                    onclick={() => goToTrigger(b.trigger_id)}
                    title="Open this trigger in the Trigger Editor"
                  >
                    Go to trigger…
                  </Button>
                {/if}
              </div>
              <p class="mt-1 text-xs text-muted-foreground">{b.reason}</p>
            </div>
          {/each}
        </div>
      {:else}
        <p class="text-sm">
          Re-emits all GUI triggers as Lua, deletes <code class="text-xs bg-muted px-1 py-0.5 rounded">war3map.j</code>,
          sets <code class="text-xs bg-muted px-1 py-0.5 rounded">MapInfo.Lua = true</code>.
        </p>
        <p class="text-sm text-muted-foreground">
          Undoable until you save (Ctrl+Z). Save (Ctrl+S) persists the change to disk.
        </p>
      {/if}
    </div>

    <Dialog.Footer
      class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0"
    >
      {#if hasBlockers}
        <Button onclick={onCancel}>Close</Button>
      {:else}
        <Button variant="ghost" onclick={onCancel} disabled={applying}>
          Cancel
        </Button>
        <Button onclick={doConvert} disabled={applying}>
          {applying ? 'Converting…' : 'Convert'}
        </Button>
      {/if}
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
