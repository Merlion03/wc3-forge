<script lang="ts">
  // ConvertToLuaDialog — three coupled states:
  //
  //   1. Blockers     — when CheckConvertToLua reports vJASS keywords. List
  //                     the offending triggers; no Convert button.
  //   2. Review-diff  — when no blockers. Loads TranspilePreview and shows a
  //                     side-by-side Monaco diff per section. Top warning
  //                     panel + [✓] Back up checkbox. Two buttons:
  //                     Convert & Save / Cancel.
  //   3. Empty        — when there are zero blockers AND zero transpilable
  //                     sections (pure-GUI map). The dialog still offers
  //                     Convert (it's still meaningful: deletes war3map.j,
  //                     flips info.Lua).
  //
  // Opinionated UX per locked decisions:
  //   - Both diff panes are read-only.
  //   - No per-section skip. Convert all or convert nothing.
  //   - Backup defaults to ON.

  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import { Checkbox } from '$lib/components/ui/checkbox'
  import { ConvertMapToLuaWithOptions, GetTranspilePreview } from '../wailsjs/go/main/App.js'
  import MonacoDiffEditor from './MonacoDiffEditor.svelte'
  import { showToast } from './toast'

  interface Blocker {
    trigger_id: number
    trigger_name: string
    kind: string
    reason: string
  }

  interface TranspileSection {
    id: number
    label: string
    kind: string
    original: string
    transpiled: string
    errors?: string[]
    preprocess_warnings?: string[]
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
  let backupChecked: boolean = $state(true)
  let preview: TranspileSection[] = $state([])
  let selectedSectionIdx: number = $state(0)
  let loadingPreview: boolean = $state(false)
  let previewError: string = $state('')
  let warningsExpanded: boolean = $state(false)

  let hasBlockers: boolean = $derived(blockers.length > 0)
  let hasPreview: boolean = $derived(preview.length > 0)
  let currentSection: TranspileSection | null = $derived(
    preview[selectedSectionIdx] ?? null,
  )

  // Lazy-load the preview only when the dialog opens in non-blocker mode.
  // No need to fetch when we're showing the blocker list.
  $effect(() => {
    if (!open) return
    if (hasBlockers) return
    if (loadingPreview) return
    if (preview.length > 0) return
    loadPreview()
  })

  async function loadPreview() {
    loadingPreview = true
    previewError = ''
    try {
      const res = await GetTranspilePreview()
      preview = (res?.sections ?? []) as TranspileSection[]
      selectedSectionIdx = 0
    } catch (e) {
      previewError = e instanceof Error ? e.message : String(e)
    } finally {
      loadingPreview = false
    }
  }

  async function doConvert() {
    if (applying) return
    applying = true
    try {
      const res = await ConvertMapToLuaWithOptions(backupChecked)
      // Defensive: backend may surface late-discovered blockers (race
      // between Check and Convert). Re-render as blocker dialog.
      if (res && res.blockers && res.blockers.length > 0) {
        blockers = res.blockers
        preview = []
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
    preview = []
    selectedSectionIdx = 0
    onClose()
  }

  function goToTrigger(id: number) {
    open = false
    preview = []
    selectedSectionIdx = 0
    onClose()
    onOpenTrigger(id)
  }

  function kindBadgeLabel(k: string): string {
    switch (k) {
      case 'script_vjass': return 'Custom-script (vJASS)'
      case 'custom_text_vjass': return 'GUI overlay (vJASS)'
      case 'map_header_vjass': return 'Hand-rolled war3map.j (vJASS)'
      case 'global_jass_vjass': return 'war3map.wct GlobalJASS (vJASS)'
      default: return k
    }
  }

  function sectionBadge(kind: string): string {
    switch (kind) {
      case 'script': return 'script'
      case 'custom_text': return 'overlay'
      case 'global_jass': return 'global_jass'
      case 'map_header': return 'map_header'
      default: return kind
    }
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content
    class="w-[min(1100px,calc(100%-2rem))] max-w-[min(1100px,calc(100%-2rem))] sm:max-w-[min(1100px,calc(100%-2rem))] gap-0 p-0 overflow-hidden"
  >
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>
        {hasBlockers ? 'Cannot auto-convert this map' : 'Convert to Lua — Review'}
      </Dialog.Title>
      <Dialog.Description class="sr-only">
        {hasBlockers
          ? 'List of triggers that block automatic JASS-to-Lua conversion.'
          : 'Side-by-side diff preview of pure-JASS sections that will be transpiled to Lua.'}
      </Dialog.Description>
    </Dialog.Header>

    {#if hasBlockers}
      <div class="flex flex-col gap-3 p-5 max-h-[calc(100vh-12rem)] overflow-y-auto min-h-[160px]">
        <p class="text-sm text-muted-foreground">
          These triggers use vJASS — a preprocessor (JassHelper) layer that
          isn't supported by auto-conversion. Rewrite them as pure JASS or
          Lua first (or delete them), then retry.
        </p>
        <div class="flex flex-col gap-1.5 mt-2">
          {#each blockers as b (b.trigger_id + '|' + b.kind)}
            <div class="rounded-md border border-amber-500/40 bg-amber-500/5 p-2.5">
              <div class="flex items-center justify-between gap-2">
                <div class="flex items-center gap-2 min-w-0">
                  <span class="font-mono text-sm truncate">{b.trigger_name}</span>
                  <span class="text-xs text-amber-400 shrink-0">
                    {kindBadgeLabel(b.kind)}
                  </span>
                </div>
                {#if b.kind !== 'map_header_vjass' && b.kind !== 'global_jass_vjass' && b.trigger_id >= 0}
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
      </div>
      <Dialog.Footer
        class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0"
      >
        <Button onclick={onCancel}>Close</Button>
      </Dialog.Footer>
    {:else}
      <div class="flex flex-col p-0 max-h-[calc(100vh-8rem)] min-h-[480px]">
        <!-- Warning panel -->
        <div class="px-5 py-3 border-b bg-amber-500/5 flex flex-col gap-2">
          <p class="text-sm font-medium">Permanent change</p>
          <p class="text-xs text-muted-foreground">
            Converting permanently rewrites <code class="text-xs bg-muted px-1 py-0.5 rounded">war3map.j</code>
            → <code class="text-xs bg-muted px-1 py-0.5 rounded">war3map.lua</code> and
            sets the map's Lua flag. Undoable until Save. Strongly recommend a backup.
          </p>
          <label class="flex items-center gap-2 mt-1 cursor-pointer">
            <Checkbox bind:checked={backupChecked} />
            <span class="text-xs">
              Back up current map (creates
              <code class="text-xs bg-muted px-1 py-0.5 rounded">&lt;name&gt;.backup</code>)
            </span>
          </label>
        </div>

        <!-- Review body -->
        {#if loadingPreview}
          <div class="flex-1 flex items-center justify-center text-sm text-muted-foreground">
            Loading transpile preview…
          </div>
        {:else if previewError}
          <div class="flex-1 flex items-center justify-center text-sm text-red-400 p-5">
            Preview failed: {previewError}
          </div>
        {:else if !hasPreview}
          <div class="flex-1 flex items-center justify-center text-sm text-muted-foreground p-5 text-center">
            No JASS to transpile — this is a pure-GUI map. Converting still
            deletes <code class="text-xs bg-muted px-1 py-0.5 rounded">war3map.j</code>
            and sets the Lua flag.
          </div>
        {:else}
          <div class="flex-1 flex min-h-0">
            <!-- Section list -->
            <div class="w-56 border-r overflow-y-auto bg-muted/20">
              <ul class="py-1">
                {#each preview as section, i (section.id + '|' + section.kind + '|' + i)}
                  <li>
                    <button
                      type="button"
                      class="w-full text-left px-3 py-2 text-xs hover:bg-muted/50 transition-colors flex flex-col gap-0.5 {i === selectedSectionIdx ? 'bg-muted text-foreground' : 'text-muted-foreground'}"
                      onclick={() => {
                        selectedSectionIdx = i
                        warningsExpanded = false
                      }}
                    >
                      <span class="font-medium truncate">{section.label}</span>
                      <span class="text-[10px] uppercase tracking-wide opacity-60">
                        {sectionBadge(section.kind)}{section.errors && section.errors.length > 0 ? ' • ⚠ errors' : ''}{section.preprocess_warnings && section.preprocess_warnings.length > 0 ? ' • ⚠ macros' : ''}
                      </span>
                    </button>
                  </li>
                {/each}
              </ul>
            </div>
            <!-- Diff editor -->
            <div class="flex-1 flex flex-col min-w-0">
              {#if currentSection}
                <div class="px-4 py-1.5 border-b bg-muted/10 text-xs text-muted-foreground flex items-center gap-2">
                  <span class="opacity-60">JASS (current)</span>
                  <span class="flex-1 text-center font-mono truncate">{currentSection.label}</span>
                  <span class="opacity-60">Lua (proposed)</span>
                </div>
                {#if currentSection.preprocess_warnings && currentSection.preprocess_warnings.length > 0}
                  <div class="px-4 py-1.5 border-b bg-amber-500/10 text-xs text-amber-300">
                    <button
                      type="button"
                      class="flex items-center gap-1.5 w-full text-left hover:opacity-80 transition-opacity"
                      onclick={() => (warningsExpanded = !warningsExpanded)}
                      title="Toggle preprocessor warnings"
                    >
                      <span class="opacity-70">{warningsExpanded ? '▼' : '▶'}</span>
                      <span class="font-medium">
                        ⚠ {currentSection.preprocess_warnings.length} preprocessor warning{currentSection.preprocess_warnings.length === 1 ? '' : 's'}
                      </span>
                      <span class="opacity-60 text-[10px]">(non-blocking)</span>
                    </button>
                    {#if warningsExpanded}
                      <ul class="mt-1.5 pl-5 list-disc space-y-0.5 max-h-24 overflow-y-auto">
                        {#each currentSection.preprocess_warnings as w (w)}
                          <li class="font-mono break-all opacity-90">{w}</li>
                        {/each}
                      </ul>
                    {/if}
                  </div>
                {/if}
                <div class="flex-1 min-h-0">
                  {#key currentSection.id + '|' + currentSection.kind}
                    <MonacoDiffEditor
                      originalValue={currentSection.original}
                      modifiedValue={currentSection.transpiled}
                      originalLanguage="jass"
                      modifiedLanguage="lua"
                    />
                  {/key}
                </div>
                {#if currentSection.errors && currentSection.errors.length > 0}
                  <div class="px-4 py-2 border-t bg-red-500/10 text-xs text-red-300 max-h-32 overflow-y-auto">
                    <p class="font-medium mb-1">Transpiler diagnostics:</p>
                    <ul class="list-disc list-inside space-y-0.5">
                      {#each currentSection.errors as err (err)}
                        <li class="font-mono break-all">{err}</li>
                      {/each}
                    </ul>
                  </div>
                {/if}
              {/if}
            </div>
          </div>
        {/if}
      </div>

      <Dialog.Footer
        class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0"
      >
        <Button variant="ghost" onclick={onCancel} disabled={applying}>
          Cancel
        </Button>
        <Button onclick={doConvert} disabled={applying}>
          {applying ? 'Converting…' : 'Convert & Save'}
        </Button>
      </Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>
