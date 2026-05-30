<script lang="ts">
  // ConvertToLuaDialog — three coupled states:
  //
  //   1. Blockers     — when CheckConvertToLua reports vJASS keywords. List
  //                     the offending triggers; no Convert button.
  //   2. Review-diff  — when no blockers. Loads TranspilePreview and shows a
  //                     side-by-side Monaco diff per section. Sections that the
  //                     transpiler couldn't translate cleanly (inline errors)
  //                     are pulled to the top under a "Needs attention" group,
  //                     most-errors-first; the rest sit under "Clean". The Lua
  //                     (right) pane is EDITABLE for needs-attention sections so
  //                     the user can hand-fix the transpiler's best-effort
  //                     output; edits are written verbatim on Convert.
  //   3. Empty        — when there are zero blockers AND zero transpilable
  //                     sections (pure-GUI map). The dialog still offers
  //                     Convert (it's still meaningful: deletes war3map.j,
  //                     flips info.Lua).
  //
  // Opinionated UX per locked decisions:
  //   - Original (JASS) pane is read-only; Lua pane is editable.
  //   - No per-section skip. Convert all or convert nothing.
  //   - Backup defaults to ON.

  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import { Checkbox } from '$lib/components/ui/checkbox'
  import { ConvertMapToLuaWithEdits, GetTranspilePreview } from '../wailsjs/go/main/App.js'
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
    libscope_warnings?: string[]
    module_warnings?: string[]
    struct_warnings?: string[]
    init_order?: Array<{
      Name: string
      InitFunc: string
      Public: string[]
      Private: string[]
    }>
    struct_inits?: Array<{
      StructName: string
      InitMethod: string
    }>
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
  let libscopeWarningsExpanded: boolean = $state(false)
  let moduleWarningsExpanded: boolean = $state(false)
  let structWarningsExpanded: boolean = $state(false)

  // Per-section user edits to the proposed Lua, keyed by section id. Present only
  // when the user changed a section away from its transpiled output. Written
  // verbatim on Convert (see doConvert / ConvertMapToLuaWithEdits).
  let edits: Record<number, string> = $state({})

  const errCount = (s: TranspileSection): number => s.errors?.length ?? 0

  let hasBlockers: boolean = $derived(blockers.length > 0)
  let hasPreview: boolean = $derived(preview.length > 0)

  // Needs-attention (any inline errors) first, most-errors-first; clean after.
  let orderedSections: TranspileSection[] = $derived(
    [...preview].sort((a, b) => {
      const ea = errCount(a)
      const eb = errCount(b)
      if ((ea > 0) !== (eb > 0)) return eb > 0 ? 1 : -1 // failed group first
      if (ea !== eb) return eb - ea // within group, more errors first
      return 0
    }),
  )
  let failedCount: number = $derived(preview.filter((s) => errCount(s) > 0).length)
  let failedSections: TranspileSection[] = $derived(orderedSections.slice(0, failedCount))
  let cleanSections: TranspileSection[] = $derived(orderedSections.slice(failedCount))
  let currentSection: TranspileSection | null = $derived(
    orderedSections[selectedSectionIdx] ?? null,
  )
  let editedCount: number = $derived(Object.keys(edits).length)
  // The Lua shown/edited for the current section: the user's edit if any, else
  // the transpiler's proposed output.
  let currentModified: string = $derived(
    currentSection ? (edits[currentSection.id] ?? currentSection.transpiled) : '',
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
      edits = {}
      selectedSectionIdx = 0 // first item = worst needs-attention section
    } catch (e) {
      previewError = e instanceof Error ? e.message : String(e)
    } finally {
      loadingPreview = false
    }
  }

  function onModifiedEdit(v: string) {
    if (!currentSection) return
    const id = currentSection.id
    if (v === currentSection.transpiled) {
      // Reverted to the transpiler's output — drop the override.
      if (id in edits) {
        const { [id]: _drop, ...rest } = edits
        edits = rest
      }
      return
    }
    edits = { ...edits, [id]: v }
  }

  function resetSectionEdit() {
    if (!currentSection) return
    const id = currentSection.id
    if (!(id in edits)) return
    const { [id]: _drop, ...rest } = edits
    edits = rest
  }

  function selectSection(i: number) {
    selectedSectionIdx = i
    warningsExpanded = false
    libscopeWarningsExpanded = false
    moduleWarningsExpanded = false
    structWarningsExpanded = false
  }

  async function doConvert() {
    if (applying) return
    applying = true
    try {
      const overrides = Object.entries(edits).map(([id, lua]) => ({
        id: Number(id),
        lua,
      }))
      const res = await ConvertMapToLuaWithEdits(backupChecked, overrides)
      // Defensive: backend may surface late-discovered blockers (race
      // between Check and Convert). Re-render as blocker dialog.
      if (res && res.blockers && res.blockers.length > 0) {
        blockers = res.blockers
        preview = []
        showToast(`Cannot convert: ${res.blockers.length} blocker(s) found`, 'error')
        return
      }
      const editNote = editedCount > 0 ? ` (${editedCount} hand-edited)` : ''
      showToast(`Converted to Lua${editNote}. Save (Ctrl+S) to persist.`, 'info')
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
    edits = {}
    selectedSectionIdx = 0
    onClose()
  }

  function goToTrigger(id: number) {
    open = false
    preview = []
    edits = {}
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
    class="!w-[95vw] !max-w-[1600px] h-[85vh] gap-0 p-0 overflow-hidden flex flex-col"
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
            <!-- Section list: needs-attention group first, then clean -->
            <div class="w-64 border-r overflow-y-auto bg-muted/20">
              {#if failedSections.length > 0}
                <div class="px-3 pt-2 pb-1 text-[10px] font-semibold uppercase tracking-wide text-red-400 sticky top-0 bg-muted/40 backdrop-blur">
                  ⚠ Needs attention ({failedSections.length})
                </div>
                <ul class="pb-1">
                  {#each failedSections as section, i (section.id + '|' + section.kind)}
                    {@const gi = i}
                    <li>
                      <button
                        type="button"
                        class="w-full text-left px-3 py-2 text-xs hover:bg-muted/50 transition-colors flex flex-col gap-0.5 border-l-2 {gi === selectedSectionIdx ? 'bg-muted text-foreground border-red-500' : 'text-muted-foreground border-red-500/40'}"
                        onclick={() => selectSection(gi)}
                      >
                        <span class="font-medium truncate flex items-center gap-1">
                          {#if section.id in edits}<span class="text-emerald-400" title="You edited this section">✎</span>{/if}
                          <span class="truncate">{section.label}</span>
                        </span>
                        <span class="text-[10px] uppercase tracking-wide opacity-70">
                          {sectionBadge(section.kind)} • <span class="text-red-400">{errCount(section)} issue{errCount(section) === 1 ? '' : 's'}</span>
                        </span>
                      </button>
                    </li>
                  {/each}
                </ul>
              {/if}
              {#if cleanSections.length > 0}
                <div class="px-3 pt-2 pb-1 text-[10px] font-semibold uppercase tracking-wide text-emerald-400/80 sticky top-0 bg-muted/40 backdrop-blur">
                  ✓ Clean ({cleanSections.length})
                </div>
                <ul class="pb-1">
                  {#each cleanSections as section, j (section.id + '|' + section.kind)}
                    {@const gi = failedCount + j}
                    <li>
                      <button
                        type="button"
                        class="w-full text-left px-3 py-2 text-xs hover:bg-muted/50 transition-colors flex flex-col gap-0.5 {gi === selectedSectionIdx ? 'bg-muted text-foreground' : 'text-muted-foreground'}"
                        onclick={() => selectSection(gi)}
                      >
                        <span class="font-medium truncate flex items-center gap-1">
                          {#if section.id in edits}<span class="text-emerald-400" title="You edited this section">✎</span>{/if}
                          <span class="truncate">{section.label}</span>
                        </span>
                        <span class="text-[10px] uppercase tracking-wide opacity-60">
                          {sectionBadge(section.kind)}{section.preprocess_warnings && section.preprocess_warnings.length > 0 ? ' • ⚠ macros' : ''}{section.libscope_warnings && section.libscope_warnings.length > 0 ? ' • ⚠ libs' : ''}{section.module_warnings && section.module_warnings.length > 0 ? ' • ⚠ modules' : ''}{section.struct_warnings && section.struct_warnings.length > 0 ? ' • ⚠ structs' : ''}
                        </span>
                      </button>
                    </li>
                  {/each}
                </ul>
              {/if}
            </div>
            <!-- Diff editor -->
            <div class="flex-1 flex flex-col min-w-0">
              {#if currentSection}
                <div class="px-4 py-1.5 border-b bg-muted/10 text-xs text-muted-foreground flex items-center gap-2">
                  <span class="opacity-60">JASS (current)</span>
                  <span class="flex-1 text-center font-mono truncate">{currentSection.label}</span>
                  <span class="opacity-60">Lua (proposed — editable)</span>
                </div>
                {#if errCount(currentSection) > 0}
                  <div class="px-4 py-2 border-b bg-red-500/10 text-xs text-red-200 flex items-start gap-2">
                    <span class="shrink-0 pt-0.5">⚠</span>
                    <span class="flex-1">
                      The transpiler couldn't fully translate this section
                      ({errCount(currentSection)} spot{errCount(currentSection) === 1 ? '' : 's'}). Its
                      best-effort Lua is on the right —
                      <code class="bg-muted px-1 py-0.5 rounded">error("jass2lua failed…")</code>
                      marks each line it gave up on. <strong>Edit the Lua to fix it;</strong>
                      your edits are written verbatim on Convert.
                    </span>
                    {#if currentSection.id in edits}
                      <Button
                        variant="ghost"
                        size="sm"
                        class="shrink-0 text-xs h-6"
                        onclick={resetSectionEdit}
                        title="Discard your edits and restore the transpiler's output"
                      >
                        Reset
                      </Button>
                    {/if}
                  </div>
                {/if}
                {#if currentSection.preprocess_warnings && currentSection.preprocess_warnings.length > 0}
                  <div class="px-4 py-1.5 border-b bg-amber-500/10 text-xs text-amber-300">
                    <button
                      type="button"
                      class="flex items-center gap-1.5 w-full text-left hover:opacity-80 transition-opacity"
                      onclick={() => (warningsExpanded = !warningsExpanded)}
                      title="Toggle textmacro preprocessor warnings"
                    >
                      <span class="opacity-70">{warningsExpanded ? '▼' : '▶'}</span>
                      <span class="font-medium">
                        ⚠ {currentSection.preprocess_warnings.length} textmacro warning{currentSection.preprocess_warnings.length === 1 ? '' : 's'}
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
                {#if currentSection.libscope_warnings && currentSection.libscope_warnings.length > 0}
                  <div class="px-4 py-1.5 border-b bg-amber-500/10 text-xs text-amber-300">
                    <button
                      type="button"
                      class="flex items-center gap-1.5 w-full text-left hover:opacity-80 transition-opacity"
                      onclick={() => (libscopeWarningsExpanded = !libscopeWarningsExpanded)}
                      title="Toggle library/scope preprocessor warnings"
                    >
                      <span class="opacity-70">{libscopeWarningsExpanded ? '▼' : '▶'}</span>
                      <span class="font-medium">
                        ⚠ {currentSection.libscope_warnings.length} library/scope warning{currentSection.libscope_warnings.length === 1 ? '' : 's'}
                      </span>
                      <span class="opacity-60 text-[10px]">(non-blocking; common: unresolved requires, dep cycles)</span>
                    </button>
                    {#if libscopeWarningsExpanded}
                      <ul class="mt-1.5 pl-5 list-disc space-y-0.5 max-h-24 overflow-y-auto">
                        {#each currentSection.libscope_warnings as w (w)}
                          <li class="font-mono break-all opacity-90">{w}</li>
                        {/each}
                      </ul>
                    {/if}
                  </div>
                {/if}
                {#if currentSection.module_warnings && currentSection.module_warnings.length > 0}
                  <div class="px-4 py-1.5 border-b bg-amber-500/10 text-xs text-amber-300">
                    <button
                      type="button"
                      class="flex items-center gap-1.5 w-full text-left hover:opacity-80 transition-opacity"
                      onclick={() => (moduleWarningsExpanded = !moduleWarningsExpanded)}
                      title="Toggle module preprocessor warnings"
                    >
                      <span class="opacity-70">{moduleWarningsExpanded ? '▼' : '▶'}</span>
                      <span class="font-medium">
                        ⚠ {currentSection.module_warnings.length} module warning{currentSection.module_warnings.length === 1 ? '' : 's'}
                      </span>
                      <span class="opacity-60 text-[10px]">(non-blocking; common: missing endmodule, duplicate name, unresolved implement)</span>
                    </button>
                    {#if moduleWarningsExpanded}
                      <ul class="mt-1.5 pl-5 list-disc space-y-0.5 max-h-24 overflow-y-auto">
                        {#each currentSection.module_warnings as w (w)}
                          <li class="font-mono break-all opacity-90">{w}</li>
                        {/each}
                      </ul>
                    {/if}
                  </div>
                {/if}
                {#if currentSection.struct_warnings && currentSection.struct_warnings.length > 0}
                  <div class="px-4 py-1.5 border-b bg-amber-500/10 text-xs text-amber-300">
                    <button
                      type="button"
                      class="flex items-center gap-1.5 w-full text-left hover:opacity-80 transition-opacity"
                      onclick={() => (structWarningsExpanded = !structWarningsExpanded)}
                      title="Toggle struct preprocessor warnings"
                    >
                      <span class="opacity-70">{structWarningsExpanded ? '▼' : '▶'}</span>
                      <span class="font-medium">
                        ⚠ {currentSection.struct_warnings.length} struct warning{currentSection.struct_warnings.length === 1 ? '' : 's'}
                      </span>
                      <span class="opacity-60 text-[10px]">(non-blocking; common: `extends array`, missing endstruct, unknown body line)</span>
                    </button>
                    {#if structWarningsExpanded}
                      <ul class="mt-1.5 pl-5 list-disc space-y-0.5 max-h-24 overflow-y-auto">
                        {#each currentSection.struct_warnings as w (w)}
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
                      modifiedValue={currentModified}
                      originalLanguage="jass"
                      modifiedLanguage="lua"
                      modifiedEditable={true}
                      onModifiedChange={onModifiedEdit}
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
        class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0 flex items-center"
      >
        {#if editedCount > 0}
          <span class="text-xs text-emerald-400 mr-auto">
            ✎ {editedCount} section{editedCount === 1 ? '' : 's'} hand-edited
          </span>
        {/if}
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
