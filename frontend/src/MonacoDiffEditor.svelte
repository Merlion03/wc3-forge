<script lang="ts">
  // Lazy-loaded Monaco diff editor — side-by-side viewer for the
  // Convert-to-Lua review dialog. Sister component to MonacoEditor.svelte:
  // shares the same module-level Monaco bootstrap (loadMonaco /
  // ensureJassRegistered) so we don't pay the 3MB import twice.
  //
  // The original (left) pane is always READ-ONLY. The modified (right) pane is
  // read-only by default, but the Convert-to-Lua dialog opts it into editing
  // (modifiedEditable) so the user can hand-fix the proposed Lua; edits are
  // reported via onModifiedChange.

  import { onDestroy, onMount } from 'svelte'
  import { loadMonaco, ensureJassRegistered } from './MonacoEditor.svelte'
  import { registerWC3Intellisense } from './wc3-intellisense'

  let {
    originalValue = '',
    modifiedValue = '',
    originalLanguage = 'jass' as 'jass' | 'lua' | 'plaintext',
    modifiedLanguage = 'lua' as 'jass' | 'lua' | 'plaintext',
    theme = 'vs-dark' as 'vs-dark' | 'vs',
    modifiedEditable = false,
    onModifiedChange = (_v: string) => {},
  }: {
    originalValue?: string
    modifiedValue?: string
    originalLanguage?: 'jass' | 'lua' | 'plaintext'
    modifiedLanguage?: 'jass' | 'lua' | 'plaintext'
    theme?: 'vs-dark' | 'vs'
    modifiedEditable?: boolean
    onModifiedChange?: (v: string) => void
  } = $props()

  // Guards the modified-content listener from firing during a programmatic
  // setValue (section switch), so only genuine user keystrokes report edits.
  let suppressChange = false
  let changeListener: import('monaco-editor').IDisposable | null = null

  let container: HTMLDivElement | null = $state(null)
  let isLoading = $state(true)
  let loadError: string = $state('')

  // Non-reactive refs held in module scope (parallel to MonacoEditor.svelte).
  let monacoMod: typeof import('monaco-editor') | null = null
  let diffEditor: import('monaco-editor').editor.IStandaloneDiffEditor | null = null
  let originalModel: import('monaco-editor').editor.ITextModel | null = null
  let modifiedModel: import('monaco-editor').editor.ITextModel | null = null
  let resizeObs: ResizeObserver | null = null
  let disposed = false

  onMount(() => {
    void mount()
  })

  onDestroy(() => {
    disposed = true
    resizeObs?.disconnect()
    resizeObs = null
    changeListener?.dispose()
    changeListener = null
    diffEditor?.dispose()
    diffEditor = null
    originalModel?.dispose()
    originalModel = null
    modifiedModel?.dispose()
    modifiedModel = null
  })

  async function mount() {
    try {
      const mod = await loadMonaco()
      if (disposed || !container) return
      monacoMod = mod
      ensureJassRegistered(mod)
      registerWC3Intellisense(mod)
      originalModel = mod.editor.createModel(originalValue ?? '', originalLanguage)
      modifiedModel = mod.editor.createModel(modifiedValue ?? '', modifiedLanguage)
      diffEditor = mod.editor.createDiffEditor(container, {
        theme,
        readOnly: !modifiedEditable,
        originalEditable: false,
        automaticLayout: false,
        renderSideBySide: true,
        useInlineViewWhenSpaceIsLimited: false,
        scrollBeyondLastLine: false,
        fontFamily: 'Consolas, "Courier New", monospace',
        fontSize: 13,
        renderOverviewRuler: false,
      })
      diffEditor.setModel({ original: originalModel, modified: modifiedModel })
      if (modifiedEditable) {
        changeListener = modifiedModel.onDidChangeContent(() => {
          if (suppressChange || !modifiedModel) return
          onModifiedChange(modifiedModel.getValue())
        })
      }
      resizeObs = new ResizeObserver(() => diffEditor?.layout())
      resizeObs.observe(container)
      isLoading = false
    } catch (e) {
      loadError = String(e)
      isLoading = false
      console.error('Monaco diff load failed', e)
    }
  }

  // Sync external originalValue → model.
  $effect(() => {
    const v = originalValue ?? ''
    if (!originalModel) return
    if (originalModel.getValue() === v) return
    originalModel.setValue(v)
  })

  // Sync external modifiedValue → model. Wrapped in suppressChange so the
  // programmatic setValue (e.g. switching sections) doesn't get reported back
  // as a user edit.
  $effect(() => {
    const v = modifiedValue ?? ''
    if (!modifiedModel) return
    if (modifiedModel.getValue() === v) return
    suppressChange = true
    modifiedModel.setValue(v)
    suppressChange = false
  })

  // Language swaps.
  $effect(() => {
    if (!originalModel || !monacoMod) return
    if (originalModel.getLanguageId() === originalLanguage) return
    monacoMod.editor.setModelLanguage(originalModel, originalLanguage)
  })
  $effect(() => {
    if (!modifiedModel || !monacoMod) return
    if (modifiedModel.getLanguageId() === modifiedLanguage) return
    monacoMod.editor.setModelLanguage(modifiedModel, modifiedLanguage)
  })
</script>

<div class="monaco-diff-host" bind:this={container}>
  {#if isLoading}
    <div class="monaco-placeholder">Loading diff editor…</div>
  {:else if loadError}
    <div class="monaco-placeholder monaco-error">Diff editor failed to load: {loadError}</div>
  {/if}
</div>

<style>
  .monaco-diff-host {
    width: 100%;
    height: 100%;
    min-height: 200px;
    position: relative;
    background: #1e1e1e;
  }
  .monaco-placeholder {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: #71717a;
    font-size: 12px;
    font-family: Consolas, "Courier New", monospace;
    pointer-events: none;
  }
  .monaco-error {
    color: #f87171;
  }
</style>
