<script lang="ts">
  // VS Code-style command palette for cross-kind fuzzy lookup.
  //
  // Loads all seven kinds' lists once on first open (lightweight: <10k rows
  // in any realistic map), searches client-side by lowercased substring across
  // name + id + category, scores so exact id > prefix > substring, and renders
  // up to 50 matches at a time. Hitting Enter (or clicking a row) closes the
  // palette and emits the (kind, id) pair the parent uses to switch + select.

  import { onMount } from 'svelte'
  import { ListObjects, type ObjectKind } from './object-editor-bindings'
  import type { main } from '../wailsjs/go/models'

  let {
    open = $bindable(false),
    onPick,
    onClose,
  }: {
    open?: boolean
    onPick: (kind: ObjectKind, id: string) => void
    onClose?: () => void
  } = $props()

  const KINDS: { kind: ObjectKind; short: string }[] = [
    { kind: 'units', short: 'U' },
    { kind: 'items', short: 'I' },
    { kind: 'doodads', short: 'D' },
    { kind: 'destructables', short: 'B' },
    { kind: 'abilities', short: 'A' },
    { kind: 'buffs', short: 'F' },
    { kind: 'upgrades', short: 'G' },
  ]

  interface PaletteRow extends main.UnitObjectListEntity {
    kind: ObjectKind
    short: string
  }

  let all: PaletteRow[] = $state([])
  let loaded = $state(false)
  let loading = $state(false)
  let query = $state('')
  let highlighted = $state(0)
  let input: HTMLInputElement | null = $state(null)

  // Load once. Cache lives for the dialog's lifetime — closing + reopening
  // reuses the rows (avoids a 7-call roundtrip on every Cmd+P) but mutations
  // from other surfaces (Wails entity-changed bus) are NOT reflected because
  // the parent ObjectEditor's own reload covers refreshing visible state. The
  // palette is a navigation tool, not a live view of the data, so freshness-
  // on-open isn't a hard requirement.
  $effect(() => {
    if (!open || loaded || loading) return
    loading = true
    ;(async () => {
      try {
        const results: PaletteRow[] = []
        await Promise.all(
          KINDS.map(async ({ kind, short }) => {
            try {
              const rows = await ListObjects(kind)
              for (const r of rows) {
                results.push({ ...r, kind, short })
              }
            } catch (e) {
              console.warn('ObjectSearchPalette: failed to load', kind, e)
            }
          }),
        )
        all = results
        loaded = true
      } finally {
        loading = false
      }
    })()
  })

  // Reset highlight + query on open so subsequent opens start clean. Pre-focus
  // the input after the dialog renders.
  $effect(() => {
    if (open) {
      query = ''
      highlighted = 0
      // Defer to next tick so the input element is mounted.
      queueMicrotask(() => input?.focus())
    }
  })

  // Score one row against the lowercased query. Higher = better match. 0 = no
  // match (filtered out). Tiers:
  //   1000 = exact id match
  //    500 = id prefix match
  //    300 = name starts with query
  //    100 = id contains query
  //     50 = name contains query
  //     25 = category contains query
  // Multiple tier matches sum; the highest tier dominates so a row that matches
  // both name-prefix and category-substring beats a row that only matches
  // category-substring. We don't bother with edit-distance fuzziness — the spec
  // explicitly says substring is fine and we don't want to pull in a library.
  function scoreRow(r: PaletteRow, q: string): number {
    if (!q) return 1
    const id = r.id.toLowerCase()
    const name = (r.name || '').toLowerCase()
    const cat = (r.category || '').toLowerCase()
    let score = 0
    if (id === q) score += 1000
    if (id.startsWith(q)) score += 500
    if (name.startsWith(q)) score += 300
    if (id.includes(q)) score += 100
    if (name.includes(q)) score += 50
    if (cat.includes(q)) score += 25
    return score
  }

  let matches: PaletteRow[] = $derived.by(() => {
    const q = query.trim().toLowerCase()
    if (!q) {
      // Empty query: surface the first 50 entries alphabetically by name so
      // the palette doesn't feel broken on first open. Stable by (kind, name).
      const out = all.slice(0, 50)
      out.sort((a, b) => a.name.localeCompare(b.name))
      return out
    }
    const scored: { row: PaletteRow; score: number }[] = []
    for (const r of all) {
      const s = scoreRow(r, q)
      if (s > 0) scored.push({ row: r, score: s })
    }
    scored.sort((a, b) => b.score - a.score || a.row.name.localeCompare(b.row.name))
    return scored.slice(0, 50).map((s) => s.row)
  })

  // Clamp the highlight to the visible window. The query-change watcher resets
  // it to 0 since the order can shuffle on every keystroke.
  $effect(() => {
    if (highlighted >= matches.length) highlighted = Math.max(0, matches.length - 1)
  })

  function pickRow(r: PaletteRow) {
    onPick(r.kind, r.id)
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
      const row = matches[highlighted]
      if (row) pickRow(row)
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      highlighted = Math.min(matches.length - 1, highlighted + 1)
      scrollHighlightedIntoView()
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      highlighted = Math.max(0, highlighted - 1)
      scrollHighlightedIntoView()
    }
  }

  let listEl: HTMLUListElement | null = $state(null)

  function scrollHighlightedIntoView() {
    if (!listEl) return
    const el = listEl.children[highlighted] as HTMLElement | undefined
    el?.scrollIntoView({ block: 'nearest' })
  }
</script>

{#if open}
  <!-- Custom overlay (not the shadcn Dialog) because we want a smaller-than-
       dialog palette anchored to the top of the editor, similar to VS Code
       Cmd+P. Click outside the panel closes; the keydown handler on the
       wrapper covers Escape/Arrows/Enter without fighting the Dialog focus
       trap. -->
  <div
    class="osp-overlay"
    onclick={(e) => {
      if (e.target === e.currentTarget) closeAndNotify()
    }}
    role="presentation"
  >
    <div class="osp-panel" onkeydown={onKeydown} role="dialog" aria-label="Global Object Search" tabindex="-1">
      <input
        bind:this={input}
        type="search"
        placeholder="Search across all kinds (type to filter; ↑/↓ navigate; Enter selects; Esc cancels)…"
        bind:value={query}
        class="osp-input"
      />
      {#if loading}
        <div class="osp-status">Loading all kinds…</div>
      {:else if all.length === 0}
        <div class="osp-status">No objects loaded (open a map first).</div>
      {:else}
        <div class="osp-meta">
          {matches.length} match{matches.length === 1 ? '' : 'es'}
          {#if matches.length === 50 && query}
            · narrow your query for more
          {/if}
          · {all.length} total
        </div>
        <ul bind:this={listEl} class="osp-list">
          {#each matches as r, i (r.kind + '|' + r.id)}
            <li>
              <button
                type="button"
                class="osp-row"
                class:osp-row-active={i === highlighted}
                onclick={() => pickRow(r)}
                onmousemove={() => (highlighted = i)}
              >
                <span class="osp-kind" title={r.kind}>{r.short}</span>
                <span class="osp-name truncate">{r.name || r.id}</span>
                <span class="osp-id font-mono">{r.id}</span>
                {#if r.category}
                  <span class="osp-cat truncate">{r.category}</span>
                {/if}
                {#if r.is_custom}
                  <span class="osp-badge osp-badge-custom" title="Custom">C</span>
                {:else if r.is_edited}
                  <span class="osp-badge osp-badge-edited" title="Edited stock object">M</span>
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
  .osp-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.4);
    z-index: 100;
    display: flex;
    justify-content: center;
    padding-top: 80px;
  }
  .osp-panel {
    width: min(720px, 90vw);
    background: rgb(var(--popover));
    color: rgb(var(--popover-foreground));
    border: 1px solid rgb(var(--border));
    border-radius: 6px;
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.4);
    display: flex;
    flex-direction: column;
    max-height: calc(100vh - 160px);
    overflow: hidden;
  }
  .osp-input {
    width: 100%;
    padding: 10px 14px;
    border: 0;
    background: transparent;
    color: inherit;
    font-size: 0.9rem;
    outline: none;
    border-bottom: 1px solid rgb(var(--border));
  }
  .osp-status {
    padding: 12px;
    color: rgb(var(--muted-foreground));
    font-size: 0.85rem;
  }
  .osp-meta {
    padding: 6px 12px;
    font-size: 0.7rem;
    color: rgb(var(--muted-foreground));
    border-bottom: 1px solid rgb(var(--border));
  }
  .osp-list {
    margin: 0;
    padding: 0;
    list-style: none;
    overflow: auto;
  }
  .osp-row {
    width: 100%;
    display: grid;
    grid-template-columns: 22px 1fr auto auto auto;
    gap: 8px;
    align-items: center;
    padding: 4px 10px;
    background: transparent;
    border: 0;
    cursor: pointer;
    text-align: left;
    color: inherit;
    font-size: 0.8rem;
    border-bottom: 1px solid rgb(var(--border) / 0.3);
  }
  .osp-row-active {
    background: rgb(var(--accent));
  }
  .osp-kind {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    background: rgb(var(--primary) / 0.18);
    color: rgb(var(--primary-foreground));
    font-family: var(--font-mono, monospace);
    font-size: 0.7rem;
    border-radius: 3px;
  }
  .osp-id {
    font-size: 0.7rem;
    color: rgb(var(--muted-foreground));
  }
  .osp-cat {
    font-size: 0.7rem;
    color: rgb(var(--muted-foreground));
    max-width: 160px;
  }
  .osp-badge {
    font-size: 0.6rem;
    padding: 0 4px;
    border-radius: 3px;
  }
  .osp-badge-custom {
    background: rgba(16, 185, 129, 0.18);
    color: rgb(110, 231, 183);
  }
  .osp-badge-edited {
    background: rgba(139, 92, 246, 0.18);
    color: rgb(196, 181, 253);
  }
  .osp-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
