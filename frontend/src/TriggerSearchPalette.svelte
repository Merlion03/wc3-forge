<script lang="ts">
  // Phase 3 — cross-trigger global search palette.
  //
  // Mirrors ObjectSearchPalette.svelte's shape (overlay + input + scored
  // results list + keyboard navigation). Differences:
  //
  //   - results come from the Go side (SearchTriggers Wails binding) so
  //     scoring/walking happens in one place
  //   - hit kinds: trigger / category / variable / eca / param
  //   - onPick passes (trigger_id, eca_path) so the host can scroll +
  //     highlight the matched ECA after navigating to its trigger
  //
  // Open trigger: double-Shift (within 300 ms) or Ctrl/Cmd+P. The host
  // wires those listeners + bindings to `open` directly.

  import { SearchTriggers, type TriggerSearchHit } from './trigger-editor-bindings'

  let {
    open = $bindable(false),
    onPick,
    onClose,
  }: {
    open?: boolean
    onPick: (triggerID: number, ecaPath: number[] | undefined) => void
    onClose?: () => void
  } = $props()

  let query = $state('')
  let highlighted = $state(0)
  let input: HTMLInputElement | null = $state(null)
  let listEl: HTMLUListElement | null = $state(null)
  let hits: TriggerSearchHit[] = $state([])
  let loading = $state(false)
  let lastQuery = ''

  // On open: focus + reset state, then load initial results (empty query
  // returns the first N triggers alphabetically per server logic).
  $effect(() => {
    if (open) {
      query = ''
      highlighted = 0
      hits = []
      queueMicrotask(() => input?.focus())
      void doSearch('')
    }
  })

  // Debounced re-search on query change. 80 ms feels snappy without
  // hammering the Wails bus on every keystroke.
  let searchTimer: ReturnType<typeof setTimeout> | null = null
  $effect(() => {
    const q = query
    if (!open) return
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(() => void doSearch(q), 80)
  })

  async function doSearch(q: string) {
    if (!open) return
    if (q === lastQuery) return
    lastQuery = q
    loading = true
    try {
      hits = await SearchTriggers(q, 50)
      highlighted = 0
    } catch (e) {
      console.warn('SearchTriggers failed', e)
      hits = []
    } finally {
      loading = false
    }
  }

  function pickRow(h: TriggerSearchHit) {
    onPick(h.trigger_id, h.path)
    closeAndNotify()
  }

  function closeAndNotify() {
    open = false
    onClose?.()
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      closeAndNotify()
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const row = hits[highlighted]
      if (row) pickRow(row)
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      highlighted = Math.min(hits.length - 1, highlighted + 1)
      scrollIntoView()
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      highlighted = Math.max(0, highlighted - 1)
      scrollIntoView()
    }
  }

  function scrollIntoView() {
    if (!listEl) return
    const el = listEl.children[highlighted] as HTMLElement | undefined
    el?.scrollIntoView({ block: 'nearest' })
  }

  // Kind short-codes for the leftmost badge in each row.
  function kindShort(k: TriggerSearchHit['kind']): string {
    switch (k) {
      case 'trigger':
        return 'T'
      case 'category':
        return 'C'
      case 'variable':
        return 'V'
      case 'eca':
        return 'E'
      case 'param':
        return 'P'
    }
    return '?'
  }
</script>

{#if open}
  <div
    class="tsp-overlay"
    onclick={(e) => {
      if (e.target === e.currentTarget) closeAndNotify()
    }}
    role="presentation"
  >
    <div class="tsp-panel" onkeydown={onKeydown} role="dialog" aria-label="Global Trigger Search" tabindex="-1">
      <input
        bind:this={input}
        type="search"
        placeholder="Search triggers, ECAs, parameters (Esc cancels, ↑/↓ navigates, Enter selects)…"
        bind:value={query}
        class="tsp-input"
      />
      {#if loading}
        <div class="tsp-status">Searching…</div>
      {:else if hits.length === 0}
        <div class="tsp-status">{query ? 'No matches.' : 'No triggers in the loaded map.'}</div>
      {:else}
        <div class="tsp-meta">{hits.length} {hits.length === 1 ? 'result' : 'results'}</div>
        <ul bind:this={listEl} class="tsp-list">
          {#each hits as h, i (h.trigger_id + '|' + h.kind + '|' + (h.path?.join('.') ?? '') + '|' + i)}
            <li>
              <button
                type="button"
                class="tsp-row"
                class:tsp-row-active={i === highlighted}
                onclick={() => pickRow(h)}
                onmousemove={() => (highlighted = i)}
              >
                <span class="tsp-kind" title={h.kind}>{kindShort(h.kind)}</span>
                <span class="tsp-name truncate">{h.trigger_name}</span>
                <span class="tsp-snippet truncate">{h.snippet}</span>
                {#if h.category}
                  <span class="tsp-cat truncate">{h.category}</span>
                {/if}
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </div>
{/if}

<style>
  .tsp-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.4);
    z-index: 110;
    display: flex;
    justify-content: center;
    padding-top: 80px;
  }
  .tsp-panel {
    width: min(820px, 92vw);
    background: var(--popover);
    color: var(--popover-foreground);
    border: 1px solid var(--border);
    border-radius: 6px;
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
    display: flex;
    flex-direction: column;
    max-height: calc(100vh - 160px);
    overflow: hidden;
  }
  .tsp-input {
    width: 100%;
    padding: 10px 14px;
    border: 0;
    background: transparent;
    color: inherit;
    font-size: 0.9rem;
    outline: none;
    border-bottom: 1px solid var(--border);
  }
  .tsp-status {
    padding: 12px;
    color: var(--muted-foreground);
    font-size: 0.85rem;
  }
  .tsp-meta {
    padding: 6px 12px;
    font-size: 0.7rem;
    color: var(--muted-foreground);
    border-bottom: 1px solid var(--border);
  }
  .tsp-list {
    margin: 0;
    padding: 0;
    list-style: none;
    overflow: auto;
  }
  .tsp-row {
    width: 100%;
    display: grid;
    grid-template-columns: 22px 1fr 1fr auto;
    gap: 8px;
    align-items: center;
    padding: 4px 10px;
    background: transparent;
    border: 0;
    cursor: pointer;
    text-align: left;
    color: inherit;
    font-size: 0.8rem;
    border-bottom: 1px solid color-mix(in oklch, var(--border) 30%, transparent);
  }
  .tsp-row-active {
    background: var(--accent);
  }
  .tsp-kind {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    background: color-mix(in oklch, var(--primary) 25%, transparent);
    color: var(--primary-foreground);
    font-family: var(--font-mono, monospace);
    font-size: 0.7rem;
    border-radius: 3px;
  }
  .tsp-snippet {
    font-family: var(--font-mono, monospace);
    font-size: 0.72rem;
    color: var(--muted-foreground);
  }
  .tsp-cat {
    font-size: 0.7rem;
    color: var(--muted-foreground);
    max-width: 160px;
  }
  .truncate {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
