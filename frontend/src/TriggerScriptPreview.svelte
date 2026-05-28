<script lang="ts">
  // Phase 3 — read-only Lua preview of the codegen output.
  //
  // Modal dialog (NOT a tab inside TriggerEditor — Dialogs nested inside a
  // Dialog don't play nice in shadcn-svelte's focus-trap model; mounting
  // this as a sibling dialog with a Z above TriggerEditor keeps both
  // interactive). On open, calls GenerateTriggerScript() to fetch the Lua
  // source, then renders it in a CodeMirror 6 read-only buffer with JS
  // syntax-highlighting (no Lua mode in @codemirror/lang-* so we use JS —
  // close enough for visual scan of structure / parens / quotes).
  //
  // A "Refresh" button re-runs codegen on demand. The Lua text is also
  // auto-regenerated whenever the dialog opens.

  import { onDestroy, tick } from 'svelte'
  import * as Dialog from '$lib/components/ui/dialog'
  import { EditorView, lineNumbers, highlightActiveLine } from '@codemirror/view'
  import { EditorState } from '@codemirror/state'
  import { javascript } from '@codemirror/lang-javascript'
  import { syntaxHighlighting, defaultHighlightStyle } from '@codemirror/language'
  import { GenerateTriggerScript } from './trigger-editor-bindings'

  let {
    open = $bindable(false),
    onClose,
  }: {
    open?: boolean
    onClose?: () => void
  } = $props()

  let scriptText: string = $state('')
  let loading: boolean = $state(false)
  let errorMsg: string = $state('')
  let editorContainer: HTMLDivElement | null = $state(null)
  let view: EditorView | null = null

  // Regenerate on open. Wrap in $effect so opening + closing the dialog
  // re-loads each time (no caching — the user may have edited triggers
  // between opens).
  $effect(() => {
    if (open) {
      void regenerate()
    } else {
      destroyEditor()
      scriptText = ''
      errorMsg = ''
    }
  })

  async function regenerate() {
    loading = true
    errorMsg = ''
    try {
      const res = await GenerateTriggerScript()
      scriptText = res.text ?? ''
      await tick()
      mountEditor()
    } catch (e) {
      errorMsg = String(e)
      scriptText = ''
    } finally {
      loading = false
    }
  }

  function mountEditor() {
    if (!editorContainer) return
    destroyEditor()
    view = new EditorView({
      parent: editorContainer,
      state: EditorState.create({
        doc: scriptText,
        extensions: [
          lineNumbers(),
          highlightActiveLine(),
          javascript(), // close-enough Lua syntax for visual scan
          syntaxHighlighting(defaultHighlightStyle),
          EditorState.readOnly.of(true),
          EditorView.theme({
            '&': { height: '100%', fontSize: '13px' },
            '.cm-scroller': { overflow: 'auto' },
          }),
        ],
      }),
    })
  }

  function destroyEditor() {
    view?.destroy()
    view = null
  }

  onDestroy(destroyEditor)

  function copyToClipboard() {
    if (!scriptText) return
    void navigator.clipboard.writeText(scriptText)
  }
</script>

<Dialog.Root bind:open onOpenChange={(v) => { if (!v) onClose?.() }}>
  <Dialog.Content class="w-[80vw] max-w-[80vw] h-[80vh] p-0 flex flex-col">
    <Dialog.Header class="px-4 py-3 border-b flex flex-row items-center justify-between">
      <div>
        <Dialog.Title>Script Preview · war3map.lua</Dialog.Title>
        <Dialog.Description>
          {#if loading}
            Generating…
          {:else if errorMsg}
            <span class="text-red-300">{errorMsg}</span>
          {:else}
            {scriptText.length.toLocaleString()} chars · {scriptText.split('\n').length.toLocaleString()} lines · read-only
          {/if}
        </Dialog.Description>
      </div>
      <div class="flex gap-2">
        <button class="te-toolbtn" onclick={() => void regenerate()}>Refresh</button>
        <button class="te-toolbtn" onclick={copyToClipboard} disabled={!scriptText}>Copy</button>
      </div>
    </Dialog.Header>

    {#if errorMsg}
      <div class="px-4 py-2 bg-red-900/30 text-red-200 text-sm">{errorMsg}</div>
    {/if}

    <div bind:this={editorContainer} class="flex-1 overflow-hidden bg-zinc-950"></div>
  </Dialog.Content>
</Dialog.Root>

<style>
  /* Inherit the te-toolbtn class style from TriggerEditor so the buttons
     match. Defined inline to keep this component self-contained. */
  :global(.te-toolbtn) {
    padding: 0.25rem 0.75rem;
    font-size: 0.8rem;
    background: rgb(var(--secondary, 39 39 42));
    color: rgb(var(--secondary-foreground, 244 244 245));
    border: 1px solid rgb(var(--border, 63 63 70));
    border-radius: 4px;
    cursor: pointer;
  }
  :global(.te-toolbtn:hover) {
    background: rgb(var(--accent, 63 63 70));
  }
  :global(.te-toolbtn:disabled) {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
