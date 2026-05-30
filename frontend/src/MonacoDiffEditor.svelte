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
  //
  // When markLeftUntranslated is set, the original (left) JASS lines that the
  // diff finds have NO Lua counterpart (or only a blank one) are highlighted —
  // i.e. the code that couldn't be transpiled — so the user can write the Lua
  // into the aligned (blank, editable) space on the right.

  import { onDestroy, onMount } from 'svelte'
  import { loadMonaco, ensureJassRegistered } from './MonacoEditor.svelte'
  import { registerWC3Intellisense } from './wc3-intellisense'

  // One untranslatable statement: source is the (token-reconstructed) JASS text
  // of the failing line, used to locate + highlight it in the original pane.
  interface Diagnostic {
    lua_line: number
    reason: string
    source?: string
  }

  let {
    originalValue = '',
    modifiedValue = '',
    originalLanguage = 'jass' as 'jass' | 'lua' | 'plaintext',
    modifiedLanguage = 'lua' as 'jass' | 'lua' | 'plaintext',
    theme = 'vs-dark' as 'vs-dark' | 'vs',
    modifiedEditable = false,
    onModifiedChange = (_v: string) => {},
    diagnostics = [] as Diagnostic[],
    onUnresolvedCount = (_n: number) => {},
  }: {
    originalValue?: string
    modifiedValue?: string
    originalLanguage?: 'jass' | 'lua' | 'plaintext'
    modifiedLanguage?: 'jass' | 'lua' | 'plaintext'
    theme?: 'vs-dark' | 'vs'
    modifiedEditable?: boolean
    onModifiedChange?: (v: string) => void
    diagnostics?: Diagnostic[]
    onUnresolvedCount?: (n: number) => void
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

  // Highlights on the original (left/JASS) editor marking untranslated lines.
  let leftDecorations: import('monaco-editor').editor.IEditorDecorationsCollection | null = null
  // Gap markers on the modified (right/Lua) editor — the blank lines the user
  // must fill in. These ride the user's edits (Monaco tracks decoration ranges).
  let gapDecorations: import('monaco-editor').editor.IEditorDecorationsCollection | null = null
  let gapNavIdx = -1

  onMount(() => {
    void mount()
  })

  onDestroy(() => {
    disposed = true
    resizeObs?.disconnect()
    resizeObs = null
    changeListener?.dispose()
    changeListener = null
    leftDecorations?.clear()
    leftDecorations = null
    gapDecorations?.clear()
    gapDecorations = null
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
        automaticLayout: true,
        renderSideBySide: true,
        useInlineViewWhenSpaceIsLimited: false,
        glyphMargin: true,
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
          recomputeUnresolved() // a gap may have just been filled (or cleared)
        })
      }
      applyLeftHighlights()
      applyRightGaps()
      revealFirstGap()
      resizeObs = new ResizeObserver(() => diffEditor?.layout())
      resizeObs.observe(container)
      isLoading = false
    } catch (e) {
      loadError = String(e)
      isLoading = false
      console.error('Monaco diff load failed', e)
    }
  }

  const normalizeJass = (s: string) => s.replace(/\s+/g, '').toLowerCase()

  // Highlight, on the ORIGINAL (left/JASS) pane, the source lines that the
  // transpiler couldn't translate. Each diagnostic carries the token-
  // reconstructed text of its failing line; we whitespace-normalize it and find
  // the matching original line(s). Synthesized lines (macro/struct-generated)
  // that have no real original simply don't match — and aren't highlighted,
  // which is honest. No reliance on diff geometry or error text.
  function applyLeftHighlights() {
    if (!diffEditor || !monacoMod || !originalModel) return
    leftDecorations?.clear()
    leftDecorations = null
    if (!diagnostics || diagnostics.length === 0) return
    const Range = monacoMod.Range
    const total = originalModel.getLineCount()
    const origNorm: string[] = new Array(total)
    for (let i = 1; i <= total; i++) origNorm[i - 1] = normalizeJass(originalModel.getLineContent(i))

    const marked = new Set<number>()
    for (const d of diagnostics) {
      const target = normalizeJass(d.source ?? '')
      if (target.length < 3) continue
      for (let i = 0; i < total; i++) {
        const o = origNorm[i]
        if (o.length === 0) continue
        // Exact line match, or (for substantial snippets) a contained substring
        // — the reconstruction can drop a trailing inline comment or modifier.
        if (o === target || (target.length >= 8 && o.includes(target))) marked.add(i + 1)
      }
    }
    const decos: import('monaco-editor').editor.IModelDeltaDecoration[] = [...marked].map((ln) => ({
      range: new Range(ln, 1, ln, 1),
      options: {
        isWholeLine: true,
        className: 'jass-untranslated-line',
        glyphMarginClassName: 'jass-untranslated-glyph',
        glyphMarginHoverMessage: { value: 'This JASS produced no Lua — write its translation on the right.' },
      },
    }))
    leftDecorations = diffEditor.getOriginalEditor().createDecorationsCollection(decos)
  }

  // Mark every untranslatable spot as a GAP on the modified (Lua) pane — a
  // whole-line highlight + glyph at the blank line the user must fill. These are
  // the backend-computed lua_line positions (exact), so the right pane is the
  // reliable "write here" surface complementing the (best-effort) left highlight.
  function applyRightGaps() {
    if (!diffEditor || !monacoMod || !modifiedModel) return
    gapDecorations?.clear()
    gapDecorations = null
    gapNavIdx = -1
    if (!diagnostics || diagnostics.length === 0) {
      onUnresolvedCount(0)
      return
    }
    const Range = monacoMod.Range
    const lc = modifiedModel.getLineCount()
    const decos: import('monaco-editor').editor.IModelDeltaDecoration[] = diagnostics
      .filter((d) => d.lua_line >= 1 && d.lua_line <= lc)
      .map((d) => ({
        range: new Range(d.lua_line, 1, d.lua_line, 1),
        options: {
          isWholeLine: true,
          className: 'lua-gap-line',
          glyphMarginClassName: 'lua-gap-glyph',
          glyphMarginHoverMessage: { value: 'Untranslatable JASS — write its Lua here.' },
        },
      }))
    gapDecorations = diffEditor.getModifiedEditor().createDecorationsCollection(decos)
    recomputeUnresolved()
  }

  // A gap is "unresolved" while its (edit-tracked) line is still blank.
  function unresolvedGapLines(): number[] {
    if (!gapDecorations || !modifiedModel) return []
    const out = new Set<number>()
    for (const r of gapDecorations.getRanges()) {
      const ln = r.startLineNumber
      if (ln >= 1 && ln <= modifiedModel.getLineCount() && modifiedModel.getLineContent(ln).trim() === '') out.add(ln)
    }
    return [...out].sort((a, b) => a - b)
  }

  function recomputeUnresolved() {
    onUnresolvedCount(unresolvedGapLines().length)
  }

  function revealFirstGap() {
    const gaps = unresolvedGapLines()
    if (!gaps.length || !diffEditor) return
    gapNavIdx = 0
    diffEditor.getModifiedEditor().revealLineInCenter(gaps[0])
  }

  // Jump to the next/prev unresolved gap and place the cursor there to type.
  export function gotoGap(dir: 1 | -1) {
    const gaps = unresolvedGapLines()
    if (!gaps.length || !diffEditor || !modifiedModel) return
    gapNavIdx = ((gapNavIdx + dir) % gaps.length + gaps.length) % gaps.length
    const line = gaps[gapNavIdx]
    const ed = diffEditor.getModifiedEditor()
    ed.revealLineInCenter(line)
    ed.setPosition({ lineNumber: line, column: modifiedModel.getLineMaxColumn(line) })
    ed.focus()
  }

  // Re-mark when the section (diagnostics / original text) changes.
  $effect(() => {
    void diagnostics
    void originalValue
    if (diffEditor && originalModel) {
      applyLeftHighlights()
      applyRightGaps()
    }
  })

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

  /* Decorations are applied by Monaco to its own DOM, so the class names must
     be global (Svelte scopes component styles by default). */
  :global(.jass-untranslated-line) {
    background: rgba(251, 191, 36, 0.14);
    border-left: 2px solid rgba(251, 191, 36, 0.75);
  }
  :global(.jass-untranslated-glyph::before) {
    content: '✎';
    color: #fbbf24;
    font-size: 11px;
    margin-left: 2px;
  }
  /* Right-pane gap markers: the blank lines to write into. */
  :global(.lua-gap-line) {
    background: rgba(251, 191, 36, 0.10);
    border-left: 2px solid rgba(251, 191, 36, 0.6);
  }
  :global(.lua-gap-glyph::before) {
    content: '✎';
    color: #fbbf24;
    font-size: 11px;
    margin-left: 2px;
  }
</style>
