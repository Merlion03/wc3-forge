<script lang="ts">
  // Lazy-loaded Monaco editor wrapper.
  //
  // Monaco is ~3MB on the wire — we don't want to pay that on cold start
  // for users who never open the Trigger Editor. The first instance to
  // mount triggers `import('monaco-editor')`; the dynamic-import cache
  // memoizes it so subsequent instances reuse the same module object
  // without a second download.
  //
  // While the import is in flight we render a "loading editor…" placeholder
  // so the modal stays interactive (the user can close it / pick a
  // different node) without a visible blank pane.
  //
  // Workers: Monaco wants `self.MonacoEnvironment.getWorker` to return a
  // Web Worker. We set up a no-op worker stub (Monaco falls back to the
  // main thread for language services if no worker is provided). For our
  // languages (lua, jass, plaintext) the worker is only used for advanced
  // diagnostics we don't have anyway, so the fallback is harmless.

  import { onDestroy, onMount, untrack } from 'svelte'
  import { jassLanguage, jassLanguageConfiguration } from './jass-monarch'
  import { registerWC3Intellisense } from './wc3-intellisense'

  let {
    value = $bindable(''),
    language = 'plaintext',
    readOnly = false,
    theme = 'vs-dark',
    onChange,
    minimap = false,
  }: {
    value?: string
    language?: 'lua' | 'jass' | 'javascript' | 'plaintext'
    readOnly?: boolean
    theme?: 'vs-dark' | 'vs'
    onChange?: (text: string) => void
    minimap?: boolean
  } = $props()

  let container: HTMLDivElement | null = $state(null)
  let isLoading = $state(true)
  let loadError: string = $state('')

  // We hold the resolved monaco module + editor instance in module-local
  // refs (not $state) because they're not reactive — Svelte 5 will
  // complain about cyclic effect re-reads if we read them inside $effect.
  let monacoMod: typeof import('monaco-editor') | null = null
  let editor: import('monaco-editor').editor.IStandaloneCodeEditor | null = null
  let model: import('monaco-editor').editor.ITextModel | null = null
  let resizeObs: ResizeObserver | null = null
  let pendingChangeTimer: ReturnType<typeof setTimeout> | null = null
  let suppressNextValueWrite = false
  let disposed = false

  onMount(() => {
    void mount()
  })

  onDestroy(() => {
    disposed = true
    if (pendingChangeTimer) {
      clearTimeout(pendingChangeTimer)
      pendingChangeTimer = null
    }
    resizeObs?.disconnect()
    resizeObs = null
    editor?.dispose()
    editor = null
    model?.dispose()
    model = null
  })

  async function mount() {
    try {
      const mod = await loadMonaco()
      if (disposed || !container) return
      monacoMod = mod
      ensureJassRegistered(mod)
      registerWC3Intellisense(mod)
      model = mod.editor.createModel(value ?? '', language)
      editor = mod.editor.create(container, {
        model,
        theme,
        readOnly,
        automaticLayout: false, // we use our own ResizeObserver
        minimap: { enabled: minimap },
        scrollBeyondLastLine: false,
        fontFamily: 'Consolas, "Courier New", monospace',
        fontSize: 13,
        lineNumbersMinChars: 3,
        renderLineHighlight: 'line',
        smoothScrolling: true,
        tabSize: 2,
      })
      editor.onDidChangeModelContent(() => {
        if (!editor || !model) return
        if (suppressNextValueWrite) {
          // The change came from us writing into the model in response to
          // a `value` prop change — don't fire onChange and don't echo
          // back to the parent.
          suppressNextValueWrite = false
          return
        }
        const text = model.getValue()
        // Mirror the post-update text into the bindable prop synchronously
        // so parents that read `value` see the latest. The debounce only
        // applies to the onChange callback (matches the CodeMirror behavior).
        value = text
        if (onChange) {
          if (pendingChangeTimer) clearTimeout(pendingChangeTimer)
          pendingChangeTimer = setTimeout(() => {
            pendingChangeTimer = null
            onChange?.(text)
          }, 400)
        }
      })
      resizeObs = new ResizeObserver(() => editor?.layout())
      resizeObs.observe(container)
      isLoading = false
    } catch (e) {
      loadError = String(e)
      isLoading = false
      console.error('Monaco load failed', e)
    }
  }

  // Sync external value -> editor model. Skip if the value already matches
  // (avoids cursor jitter when our own onChange wrote back into the prop).
  $effect(() => {
    const v = value ?? ''
    if (!model) return
    if (model.getValue() === v) return
    suppressNextValueWrite = true
    model.setValue(v)
    untrack(() => {
      // Re-position cursor at end after a programmatic value swap. Without
      // this, swapping to a longer doc can leave the cursor mid-line.
      const lastLine = model!.getLineCount()
      const lastCol = model!.getLineMaxColumn(lastLine)
      editor?.setPosition({ lineNumber: lastLine, column: lastCol })
    })
  })

  // React to language changes (e.g. switching from a Lua script trigger to
  // a JASS one). Monaco doesn't let us change a model's language in place
  // without `setModelLanguage`.
  $effect(() => {
    const lang = language
    if (!model || !monacoMod) return
    if (model.getLanguageId() === lang) return
    monacoMod.editor.setModelLanguage(model, lang)
  })

  // React to readOnly toggles.
  $effect(() => {
    const ro = readOnly
    editor?.updateOptions({ readOnly: ro })
  })
</script>

<div class="monaco-host" bind:this={container}>
  {#if isLoading}
    <div class="monaco-placeholder">Loading editor…</div>
  {:else if loadError}
    <div class="monaco-placeholder monaco-error">Editor failed to load: {loadError}</div>
  {/if}
</div>

<style>
  .monaco-host {
    width: 100%;
    height: 100%;
    min-height: 100px;
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

<script lang="ts" module>
  // Module-level singletons — the dynamic import cache + the "JASS is
  // registered" flag. Shared across every <MonacoEditor /> instance on
  // the page.

  let monacoPromise: Promise<typeof import('monaco-editor')> | null = null
  let jassRegistered = false
  let workerEnvWired = false

  export function loadMonaco(): Promise<typeof import('monaco-editor')> {
    if (!monacoPromise) {
      wireWorkerEnv()
      monacoPromise = import('monaco-editor')
    }
    return monacoPromise
  }

  function wireWorkerEnv() {
    if (workerEnvWired) return
    workerEnvWired = true
    // Monaco looks up `self.MonacoEnvironment.getWorker` to spawn its
    // language workers. We don't need the editor/typescript/css/html
    // workers (we only register a JASS Monarch tokenizer + use the base
    // Lua mode shipped with Monaco). Returning a stub keeps Monaco quiet
    // in the console; it falls back to running language services on the
    // main thread, which is fine for our payload sizes.
    ;(self as any).MonacoEnvironment = {
      getWorker: () => {
        // Inline a no-op worker via a blob URL so Monaco gets *something*
        // postMessage-able back.
        const stub = `self.onmessage = () => {}`
        const blob = new Blob([stub], { type: 'application/javascript' })
        return new Worker(URL.createObjectURL(blob))
      },
    }
  }

  export function ensureJassRegistered(monaco: typeof import('monaco-editor')) {
    if (jassRegistered) return
    jassRegistered = true
    monaco.languages.register({ id: 'jass', extensions: ['.j', '.ai'], aliases: ['JASS', 'jass'] })
    monaco.languages.setMonarchTokensProvider('jass', jassLanguage as any)
    monaco.languages.setLanguageConfiguration('jass', jassLanguageConfiguration as any)
  }
</script>
