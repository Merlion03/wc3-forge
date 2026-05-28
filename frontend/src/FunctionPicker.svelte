<script lang="ts">
  // Modal "Add Event / Condition / Action / Call" picker. Mirrors HiveWE's
  // hierarchical function picker: left-pane category tree, right-pane filtered
  // function list with display-name + hint tooltip.
  //
  // Inputs:
  //   open         — controlled-modal visibility
  //   section      — which TriggerData.txt family to pick from
  //                  ("TriggerEvents" | "TriggerConditions" | "TriggerActions" | "TriggerCalls")
  //   meta         — the full TriggerData snapshot (functions + categories +
  //                  presets); cached in TriggerEditor and passed down so the
  //                  picker doesn't re-fetch.
  //   onPick(name) — confirmation callback. Fires the picked function name;
  //                  caller is responsible for calling Session.AddECA + closing.
  //   onClose()    — dismiss without picking.
  //
  // No version filtering (locked decision per the phase plan): every function
  // from the chosen section is offered regardless of since_version.

  import * as Dialog from '$lib/components/ui/dialog'
  import { Input } from '$lib/components/ui/input'
  import { Button } from '$lib/components/ui/button'
  import type { TriggerFunctionMeta, TriggerFunctionsMeta } from './trigger-editor-bindings'

  let {
    open = $bindable(false),
    section,
    meta,
    onPick,
    onClose,
  }: {
    open?: boolean
    section: 'TriggerEvents' | 'TriggerConditions' | 'TriggerActions' | 'TriggerCalls'
    meta: TriggerFunctionsMeta | null
    onPick: (name: string) => void
    onClose?: () => void
  } = $props()

  let search = $state('')
  let selectedCategory = $state<string | null>(null)
  let hoveredFunc = $state<string | null>(null)

  // Slice meta.functions to just the section we're picking from. Cached per
  // section change via $derived. Category buckets sorted by display label.
  let candidates = $derived.by(() => {
    if (!meta) return []
    return (meta.functions ?? []).filter((f) => f.section === section)
  })

  // Group by category code. Functions with no category land under "" so the
  // UI shows them under an "Uncategorized" bucket. Each bucket is sorted by
  // display name (falling back to name) for deterministic ordering.
  let byCategory = $derived.by(() => {
    const out: Record<string, TriggerFunctionMeta[]> = {}
    for (const f of candidates) {
      const cat = f.category || ''
      ;(out[cat] ??= []).push(f)
    }
    for (const k of Object.keys(out)) {
      out[k].sort((a, b) => {
        const la = a.display_name || a.name
        const lb = b.display_name || b.name
        return la.localeCompare(lb)
      })
    }
    return out
  })

  // Sorted category list — first the ones with a real label, then "" last.
  let categoryList = $derived.by(() => {
    const cats = Object.keys(byCategory)
    const labelOf = (c: string) => meta?.categories?.[c] || c || 'Uncategorized'
    cats.sort((a, b) => labelOf(a).localeCompare(labelOf(b)))
    return cats
  })

  // Visible items: search query takes precedence over category filter
  // (HiveWE behaves the same way — typing in the search box flattens
  // across categories). When no search, filter to the selected category.
  let visible = $derived.by(() => {
    const q = search.trim().toLowerCase()
    if (q) {
      return candidates
        .filter((f) => {
          const dn = (f.display_name || '').toLowerCase()
          const name = f.name.toLowerCase()
          return dn.includes(q) || name.includes(q)
        })
        .sort((a, b) => (a.display_name || a.name).localeCompare(b.display_name || b.name))
    }
    if (selectedCategory == null) {
      // Default to the first category — keeps the right pane non-empty
      // immediately.
      if (categoryList.length > 0) return byCategory[categoryList[0]] ?? []
      return []
    }
    return byCategory[selectedCategory] ?? []
  })

  function categoryLabel(code: string): string {
    if (!code) return 'Uncategorized'
    return meta?.categories?.[code] || code
  }

  function commitPick(f: TriggerFunctionMeta) {
    onPick(f.name)
    open = false
    onClose?.()
  }

  function dismiss(o: boolean) {
    if (!o) onClose?.()
    open = o
  }

  function sectionLabel(): string {
    switch (section) {
      case 'TriggerEvents':
        return 'Event'
      case 'TriggerConditions':
        return 'Condition'
      case 'TriggerActions':
        return 'Action'
      case 'TriggerCalls':
        return 'Call'
    }
  }
</script>

<Dialog.Root bind:open={open} onOpenChange={dismiss}>
  <Dialog.Content class="!max-w-[820px] !w-[90vw] h-[80vh] flex flex-col p-0 gap-0">
    <Dialog.Header class="px-4 py-3 border-b border-border">
      <Dialog.Title>Add {sectionLabel()}</Dialog.Title>
      <Dialog.Description class="text-xs text-muted-foreground">
        Pick a function from TriggerData.txt. The new {sectionLabel().toLowerCase()} appends to the current trigger with default parameter values.
      </Dialog.Description>
    </Dialog.Header>

    <div class="px-3 py-2 border-b border-border">
      <Input
        type="search"
        placeholder="Filter by name…"
        bind:value={search}
        class="h-8 text-sm"
      />
      <div class="mt-1 text-xs text-muted-foreground">
        {visible.length} of {candidates.length} {sectionLabel().toLowerCase()}s
      </div>
    </div>

    <div class="flex-1 min-h-0 flex">
      <!-- Left pane: categories -->
      <div class="w-[200px] border-r border-border overflow-y-auto fp-catlist">
        {#each categoryList as code (code)}
          <button
            class="fp-cat {selectedCategory === code ? 'fp-cat-sel' : ''}"
            onclick={() => { selectedCategory = code; search = '' }}
            title={categoryLabel(code)}
          >{categoryLabel(code)} <span class="text-zinc-500 ml-1">({byCategory[code].length})</span></button>
        {/each}
        {#if categoryList.length === 0}
          <div class="px-3 py-2 text-xs text-zinc-500">(no functions loaded)</div>
        {/if}
      </div>

      <!-- Right pane: functions -->
      <div class="flex-1 overflow-y-auto fp-funclist">
        {#each visible as f (f.name)}
          <button
            class="fp-func"
            onclick={() => commitPick(f)}
            onmouseenter={() => (hoveredFunc = f.name)}
            onmouseleave={() => (hoveredFunc = null)}
            title={f.hint || f.name}
          >
            <div class="fp-func-name">{f.display_name || f.name}</div>
            {#if f.hint}
              <div class="fp-func-hint">{f.hint}</div>
            {/if}
          </button>
        {/each}
        {#if visible.length === 0}
          <div class="px-3 py-2 text-xs text-zinc-500">(no matches)</div>
        {/if}
      </div>
    </div>

    <Dialog.Footer class="px-4 py-2 border-t border-border">
      <Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<style>
  .fp-catlist { background: rgba(0, 0, 0, 0.15); }
  .fp-cat {
    display: block;
    width: 100%;
    text-align: left;
    padding: 4px 10px;
    background: none;
    border: 0;
    color: #d4d4d8;
    font-size: 12px;
    cursor: pointer;
    line-height: 1.4;
  }
  .fp-cat:hover { background: rgba(255, 255, 255, 0.04); }
  .fp-cat-sel { background: rgba(96, 165, 250, 0.18); color: #fff; }

  .fp-funclist { background: #18181b; }
  .fp-func {
    display: block;
    width: 100%;
    text-align: left;
    padding: 6px 12px;
    background: none;
    border: 0;
    border-bottom: 1px solid rgba(255,255,255,0.04);
    color: #d4d4d8;
    cursor: pointer;
  }
  .fp-func:hover { background: rgba(255, 255, 255, 0.06); }
  .fp-func-name { font-size: 13px; color: #e4e4e7; }
  .fp-func-hint { font-size: 11px; color: #71717a; margin-top: 2px; line-height: 1.3; }
</style>
