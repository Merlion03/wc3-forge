<script lang="ts">
  // Trigger Editor (Phase 1a — read-only). Modal dialog with a two-pane
  // layout: left pane shows the category/trigger/variable tree built from
  // ListTriggerTree(); right pane shows the selected node's details.
  //
  // - GUI triggers render their Events / Conditions / Actions sections via
  //   client-side label templating against GetTriggerFunctionsMeta.
  // - Script triggers (custom-script or Map Header) render their custom_text
  //   in a monospace <pre>. CodeMirror lands in Phase 2a.
  // - Variables get a 5-field read-only display.
  // - Comments show the description text.
  //
  // No add/delete/rename/drag-drop in 1a — tree clicks only select. Phase 2a
  // adds structural editing.

  import { onMount, onDestroy, tick } from 'svelte'
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
  import {
    ListTriggerTree,
    GetTrigger,
    GetTriggerFunctionsMeta,
    paramLabel,
    renderECALabel,
    MAGIC_ECAS,
    type TriggerTree,
    type TriggerTreeNode,
    type TriggerDetail,
    type TriggerECA,
    type TriggerFunctionMeta,
    type TriggerFunctionsMeta,
  } from './trigger-editor-bindings'
  import { parseWC3Color } from './wc3color'
  import * as Dialog from '$lib/components/ui/dialog'

  let {
    open = $bindable(false),
    onClose,
    initialId,
  }: {
    open?: boolean
    onClose?: () => void
    initialId?: number | null
  } = $props()

  // Loaded tree (flat node list ordered as-written). Hierarchy is built
  // client-side via parent_id walks.
  let tree: TriggerTree = $state({ nodes: [] })
  let selectedId: number | null = $state(null)
  let detail: TriggerDetail | null = $state(null)
  let loading: boolean = $state(false)
  let detailLoading: boolean = $state(false)
  let errorMsg: string = $state('')

  // TriggerData.txt vocabulary — fetched once on mount and cached. Used to
  // render ECA labels via the _Foo_Parameters templates.
  let functionsMeta: TriggerFunctionsMeta | null = $state(null)
  let metaByName: Map<string, TriggerFunctionMeta> = $state(new Map())

  // Track open transitions to (re)load the tree. Initial-id selection runs
  // once the tree is populated.
  let initialApplied = false
  $effect(() => {
    const _ = open
    if (open) {
      void loadTree().then(() => {
        if (!initialApplied && initialId != null) {
          initialApplied = true
          selectNode(initialId)
        }
      })
    } else {
      initialApplied = false
      selectedId = null
      detail = null
      errorMsg = ''
    }
  })

  // Subscribe to map-changed events so a Close/Open cycle refreshes the
  // tree without the user having to close/reopen the dialog manually.
  let unsubMapChanged: (() => void) | null = null
  onMount(() => {
    void ensureFunctionsMeta()
    EventsOn('wc3-forge:map-changed', onMapChanged)
    unsubMapChanged = () => EventsOff('wc3-forge:map-changed')
  })
  onDestroy(() => {
    unsubMapChanged?.()
  })
  function onMapChanged() {
    if (open) {
      void loadTree()
    }
  }

  async function ensureFunctionsMeta() {
    if (functionsMeta) return
    try {
      const meta = await GetTriggerFunctionsMeta()
      functionsMeta = meta ?? { functions: [] }
      const map = new Map<string, TriggerFunctionMeta>()
      for (const f of functionsMeta.functions ?? []) {
        map.set(f.name, f)
      }
      metaByName = map
    } catch (e) {
      // Non-fatal; ECA labels fall back to name(arg, arg, …) without
      // template substitution.
      console.warn('GetTriggerFunctionsMeta failed', e)
    }
  }

  async function loadTree() {
    loading = true
    errorMsg = ''
    try {
      tree = await ListTriggerTree()
    } catch (e) {
      errorMsg = `Failed to load trigger tree: ${e}`
      tree = { nodes: [] }
    } finally {
      loading = false
    }
  }

  async function selectNode(id: number) {
    selectedId = id
    detailLoading = true
    detail = null
    try {
      detail = await GetTrigger(id)
    } catch (e) {
      errorMsg = `Failed to load trigger detail: ${e}`
    } finally {
      detailLoading = false
    }
  }

  // Tree-building. We walk parent_id once and build a children map keyed on
  // each parent id. Sibling order matches the array order in tree.nodes —
  // that's the on-disk order from the .wtg.
  type TreeRow = { node: TriggerTreeNode; depth: number }
  function buildVisibleRows(): TreeRow[] {
    const childrenByParent = new Map<number, TriggerTreeNode[]>()
    const allIds = new Set<number>()
    for (const n of tree.nodes) {
      allIds.add(n.id)
      const arr = childrenByParent.get(n.parent_id) ?? []
      arr.push(n)
      childrenByParent.set(n.parent_id, arr)
    }
    // Roots are nodes whose parent isn't in the tree (parent_id == -1 for
    // the Map Header; other dangling parents indicate a malformed wtg).
    const roots = tree.nodes.filter((n) => !allIds.has(n.parent_id))
    const rows: TreeRow[] = []
    const visit = (n: TriggerTreeNode, depth: number) => {
      rows.push({ node: n, depth })
      // Categories with open_state=false collapse their children in HiveWE;
      // we honor that as the default. The user can override per node via
      // collapsedOverrides.
      const isOpen = isNodeOpen(n)
      if (!isOpen) return
      const kids = childrenByParent.get(n.id) ?? []
      for (const c of kids) {
        visit(c, depth + 1)
      }
    }
    for (const r of roots) {
      visit(r, 0)
    }
    return rows
  }
  // collapsedOverrides lets the user click-to-toggle a category's collapse
  // state during the session (doesn't persist to disk; that's Phase 2a).
  let collapsedOverrides: Record<number, boolean> = $state({})
  function isNodeOpen(n: TriggerTreeNode): boolean {
    if (!isCategoryLike(n)) return true
    if (collapsedOverrides[n.id] !== undefined) {
      return !collapsedOverrides[n.id]
    }
    return n.open_state !== false
  }
  function toggleCollapse(n: TriggerTreeNode) {
    if (!isCategoryLike(n)) return
    const currentlyOpen = isNodeOpen(n)
    collapsedOverrides = { ...collapsedOverrides, [n.id]: currentlyOpen }
  }
  function isCategoryLike(n: TriggerTreeNode): boolean {
    return n.kind === 'category' || n.kind === 'map' || n.kind === 'library'
  }

  let visibleRows = $derived(buildVisibleRows())

  // Per-node icon glyph. Unicode is good enough for 1a; richer SVGs land
  // alongside Phase 2a styling. The same icon set HiveWE uses (folder,
  // gear, document, lambda, hash) is approximated here.
  function iconFor(n: TriggerTreeNode): string {
    if (n.is_comment || n.kind === 'trigger_comment') return '#'
    switch (n.kind) {
      case 'map':
      case 'library':
      case 'category':
        return '▸'
      case 'trigger_gui':
        return '⚙'
      case 'trigger_script':
        return '✎'
      case 'variable':
        return 'λ'
    }
    return '·'
  }

  // For a selected GUI trigger, partition ECAs into Events / Conditions /
  // Actions per their type field. ECA.Type values: 0=event, 1=condition,
  // 2=action, 3=call. Calls are inline (sub-functions) so they don't
  // appear at the top level normally.
  function partitionECAs(ecas: TriggerECA[] | undefined) {
    const events: TriggerECA[] = []
    const conditions: TriggerECA[] = []
    const actions: TriggerECA[] = []
    for (const e of ecas ?? []) {
      if (e.type === 0) events.push(e)
      else if (e.type === 1) conditions.push(e)
      else actions.push(e)
    }
    return { events, conditions, actions }
  }

  // Render one ECA + its children to a flat row list with depth so the UI
  // can indent. Magic ECAs (IfThenElseMultiple, etc.) get synthetic section
  // headers per HiveWE's trigger_editor.cpp L351-365.
  type ECARow = { label: string; depth: number; isMagicChild?: boolean; raw: TriggerECA; hint?: string }
  function renderECARows(ecas: TriggerECA[], depth: number): ECARow[] {
    const out: ECARow[] = []
    for (const e of ecas) {
      const meta = metaByName.get(e.name)
      const label = renderECALabel(e, meta?.parameters_template)
      const hint = meta?.hint || ''
      out.push({ label, depth, raw: e, hint })
      if (e.children && e.children.length > 0) {
        if (MAGIC_ECAS.has(e.name)) {
          // Bucket children under section headers. The bucket assignment
          // matches HiveWE's labeling: condition-typed children land under
          // "Conditions"; action-typed under "Actions"/"Then"/"Else"/"Loop".
          const buckets = bucketMagicChildren(e)
          for (const [bucketLabel, bucketEcas] of buckets) {
            out.push({ label: bucketLabel, depth: depth + 1, isMagicChild: true, raw: e })
            const subRows = renderECARows(bucketEcas, depth + 2)
            for (const r of subRows) out.push(r)
          }
        } else {
          const subRows = renderECARows(e.children, depth + 1)
          for (const r of subRows) out.push(r)
        }
      }
    }
    return out
  }

  // Per-magic-ECA child bucketing. The wire uses ECA.group on children for
  // the magic functions (0=conditions, 1=then/loop, 2=else) — matching
  // HiveWE's convention. Falls back to a single "Children" bucket when
  // groups aren't set.
  function bucketMagicChildren(parent: TriggerECA): Array<[string, TriggerECA[]]> {
    const groups: Record<number, TriggerECA[]> = {}
    for (const c of parent.children ?? []) {
      const g = c.group ?? 0
      ;(groups[g] ??= []).push(c)
    }
    const haveGroups = Object.keys(groups).length > 0
    if (!haveGroups) return [['Children', parent.children ?? []]]
    // Labels per parent kind. We branch on a small whitelist; unknown
    // magic ECAs get generic "Group N" labels so we never silently drop
    // children.
    const name = parent.name
    const isIfThenElse =
      name === 'IfThenElse' || name === 'IfThenElseMultiple'
    const isForLoop =
      name === 'ForLoopA' ||
      name === 'ForLoopAMultiple' ||
      name === 'ForLoopB' ||
      name === 'ForLoopBMultiple' ||
      name === 'ForLoopVar' ||
      name === 'ForLoopVarMultiple'
    const labelFor = (g: number): string => {
      if (isIfThenElse) {
        if (g === 0) return 'If — Conditions'
        if (g === 1) return 'Then — Actions'
        if (g === 2) return 'Else — Actions'
      }
      if (isForLoop) {
        return 'Loop — Actions'
      }
      if (name === 'AndMultiple' || name === 'OrMultiple') {
        return g === 0 ? 'Conditions' : `Group ${g}`
      }
      return `Group ${g}`
    }
    const out: Array<[string, TriggerECA[]]> = []
    const sortedKeys = Object.keys(groups)
      .map((k) => Number(k))
      .sort((a, b) => a - b)
    for (const g of sortedKeys) {
      out.push([labelFor(g), groups[g]])
    }
    return out
  }

  // Light-weight syntax tint for the script <pre> block. Lua keywords
  // get highlighted; comments + strings get neutral colors. Pulls from a
  // small Lua-flavored token list — JASS uses similar enough keywords
  // (set/call/function/if/then/else/endif/loop/exitwhen/endloop/local/return)
  // that we cover both with a combined set.
  const SCRIPT_KEYWORDS = new Set([
    // Lua
    'and', 'break', 'do', 'else', 'elseif', 'end', 'false', 'for', 'function',
    'goto', 'if', 'in', 'local', 'nil', 'not', 'or', 'repeat', 'return',
    'then', 'true', 'until', 'while',
    // JASS additions
    'set', 'call', 'globals', 'endglobals', 'takes', 'returns', 'nothing',
    'endfunction', 'loop', 'endloop', 'exitwhen', 'endif',
  ])
  function highlightScript(text: string): string {
    // Escape HTML first.
    const escaped = text
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
    // Comments — Lua // and -- and JASS //. Apply line-wise.
    const lines = escaped.split('\n')
    const out: string[] = []
    for (const line of lines) {
      const trimmed = line.trimStart()
      if (trimmed.startsWith('//') || trimmed.startsWith('--')) {
        out.push(`<span class="te-cmt">${line}</span>`)
        continue
      }
      // Highlight strings (single + double quoted) and keywords inline.
      let s = line
      s = s.replace(/("[^"\\]*(?:\\.[^"\\]*)*")/g, '<span class="te-str">$1</span>')
      s = s.replace(/('[^'\\]*(?:\\.[^'\\]*)*')/g, '<span class="te-str">$1</span>')
      s = s.replace(/\b([A-Za-z_][A-Za-z_0-9]*)\b/g, (_, w: string) => {
        if (SCRIPT_KEYWORDS.has(w)) {
          return `<span class="te-kw">${w}</span>`
        }
        return w
      })
      out.push(s)
    }
    return out.join('\n')
  }

  // renderColoredLabel converts an ECA label string with embedded WC3 color
  // codes (|cAARRGGBB…|r) into HTML with one <span> per color run. Pure-text
  // labels (no |c tokens) round-trip as a single HTML-escaped span — the
  // overhead is one parseWC3Color call per visible row, which is negligible
  // versus the cost of the surrounding Svelte each-block.
  //
  // Returns {@html}-safe HTML — all text is HTML-escaped before being wrapped
  // in style="color: ..." spans, so script injection from a malicious wts /
  // mod can't escape the span boundary.
  function renderColoredLabel(label: string): string {
    const segs = parseWC3Color(label)
    let out = ''
    for (const s of segs) {
      if (!s.text) continue
      const esc = escapeHTML(s.text)
      if (s.color) {
        out += `<span style="color: ${s.color}">${esc}</span>`
      } else {
        out += esc
      }
    }
    return out
  }

  // escapeHTML keeps the rendered label safe from injection — user-authored
  // map text (unit names, tooltips, custom triggers) can contain `<`/`&`/`"`
  // and we never want the browser to interpret those as markup.
  function escapeHTML(s: string): string {
    return s
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
  }

  function onClickClose() {
    onClose?.()
  }

  // For variable detail rendering — JSON-friendly field formatting.
  function fmtBool(v: boolean | undefined) {
    return v ? 'yes' : 'no'
  }
</script>

<Dialog.Root bind:open onOpenChange={(v) => { if (!v) onClose?.() }}>
  <Dialog.Content class="w-[95vw] max-w-[95vw] h-[85vh] p-0 flex flex-col">
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>Trigger Editor</Dialog.Title>
      <Dialog.Description>
        {#if tree.is_pre_131}
          Pre-1.31 trigger format. Read-only in this build.
        {:else if tree.nodes.length === 0}
          No triggers in this map.
        {:else}
          {tree.nodes.length} nodes. Read-only in this build (Phase 1a).
        {/if}
      </Dialog.Description>
    </Dialog.Header>

    {#if errorMsg}
      <div class="px-4 py-2 bg-red-900/30 text-red-200 text-sm">{errorMsg}</div>
    {/if}

    <div class="flex-1 flex min-h-0">
      <!-- Left pane: tree -->
      <div class="w-[320px] border-r overflow-y-auto py-1 te-tree">
        {#if loading}
          <div class="px-3 py-2 text-zinc-500 text-sm">Loading…</div>
        {:else if visibleRows.length === 0}
          <div class="px-3 py-2 text-zinc-500 text-sm">(empty)</div>
        {/if}
        {#each visibleRows as row (row.node.id)}
          <button
            class="te-row {selectedId === row.node.id ? 'te-row-sel' : ''}"
            style="padding-left: {8 + row.depth * 14}px"
            onclick={() => {
              selectNode(row.node.id)
              if (isCategoryLike(row.node)) toggleCollapse(row.node)
            }}
            title={row.node.description || row.node.name}
          >
            <span class="te-icon">{iconFor(row.node)}</span>
            <span class="te-name {row.node.is_enabled === false ? 'te-disabled' : ''}">{row.node.name}</span>
          </button>
        {/each}
      </div>

      <!-- Right pane: detail -->
      <div class="flex-1 overflow-y-auto te-detail">
        {#if detailLoading}
          <div class="px-4 py-3 text-zinc-500">Loading…</div>
        {:else if !detail}
          <div class="px-4 py-3 text-zinc-500">Select a trigger or category on the left.</div>
        {:else if detail.kind === 'category' || detail.kind === 'map' || detail.kind === 'library'}
          {@const c = detail.category!}
          <div class="px-4 py-3">
            <div class="text-zinc-300 text-lg">{c.name}</div>
            <div class="text-zinc-500 text-sm mt-1">
              {detail.kind === 'map' ? 'Map header' : detail.kind === 'library' ? 'Library' : 'Category'} · id={c.id}
            </div>
            <div class="text-zinc-500 text-sm">parent_id={c.parent_id} · open_state={fmtBool(c.open_state)} · is_comment={fmtBool(c.is_comment)}</div>
          </div>
        {:else if detail.kind === 'variable'}
          {@const v = detail.variable!}
          <div class="px-4 py-3 space-y-1 text-sm">
            <div class="text-zinc-300 text-lg">{v.name}</div>
            <div class="grid grid-cols-[140px_1fr] gap-y-1 text-zinc-400 mt-2">
              <div>type</div><div class="text-zinc-200">{v.type}</div>
              <div>is_array</div><div class="text-zinc-200">{fmtBool(v.is_array)}</div>
              <div>array_size</div><div class="text-zinc-200">{v.array_size ?? 0}</div>
              <div>is_initialized</div><div class="text-zinc-200">{fmtBool(v.is_initialized)}</div>
              <div>initial_value</div><div class="text-zinc-200 break-all">{v.initial_value || '(none)'}</div>
            </div>
          </div>
        {:else if detail.kind === 'trigger_comment'}
          {@const t = detail.trigger!}
          <div class="px-4 py-3">
            <div class="text-zinc-300 text-lg">{t.name}</div>
            <div class="text-zinc-500 text-sm mb-2">Comment · id={t.id}</div>
            <pre class="te-script">{t.description || '(empty)'}</pre>
          </div>
        {:else if detail.kind === 'trigger_script'}
          {@const t = detail.trigger!}
          <div class="px-4 py-3">
            <div class="text-zinc-300 text-lg">{t.name}</div>
            {#if t.description}
              <div class="text-zinc-500 text-sm mb-2">{t.description}</div>
            {/if}
            <div class="text-zinc-500 text-xs mb-2">
              Script trigger · id={t.id} ·
              enabled={fmtBool(t.is_enabled)} · initially_on={fmtBool(t.initially_on)}
            </div>
            <pre class="te-script">{@html highlightScript(t.custom_text || '')}</pre>
          </div>
        {:else if detail.kind === 'trigger_gui'}
          {@const t = detail.trigger!}
          {@const parts = partitionECAs(t.ecas)}
          <div class="px-4 py-3 space-y-3">
            <div>
              <div class="text-zinc-300 text-lg">{t.name}</div>
              {#if t.description}
                <div class="text-zinc-500 text-sm">{t.description}</div>
              {/if}
              <div class="text-zinc-500 text-xs">
                GUI trigger · id={t.id} · enabled={fmtBool(t.is_enabled)} ·
                initially_on={fmtBool(t.initially_on)} ·
                run_on_init={fmtBool(t.run_on_initialization)}
              </div>
            </div>

            <details open class="te-section">
              <summary class="te-section-summary">Events ({parts.events.length})</summary>
              <div class="te-eca-list">
                {#each renderECARows(parts.events, 0) as r (r.raw === r.raw && r.label)}
                  <div
                    class="te-eca-row {r.isMagicChild ? 'te-eca-magic' : ''}"
                    style="padding-left: {r.depth * 16}px"
                    title={r.hint || undefined}
                  >{@html renderColoredLabel(r.label)}</div>
                {/each}
                {#if parts.events.length === 0}
                  <div class="te-eca-empty">(none)</div>
                {/if}
              </div>
            </details>

            <details open class="te-section">
              <summary class="te-section-summary">Conditions ({parts.conditions.length})</summary>
              <div class="te-eca-list">
                {#each renderECARows(parts.conditions, 0) as r (r.raw === r.raw && r.label)}
                  <div
                    class="te-eca-row {r.isMagicChild ? 'te-eca-magic' : ''}"
                    style="padding-left: {r.depth * 16}px"
                    title={r.hint || undefined}
                  >{@html renderColoredLabel(r.label)}</div>
                {/each}
                {#if parts.conditions.length === 0}
                  <div class="te-eca-empty">(none)</div>
                {/if}
              </div>
            </details>

            <details open class="te-section">
              <summary class="te-section-summary">Actions ({parts.actions.length})</summary>
              <div class="te-eca-list">
                {#each renderECARows(parts.actions, 0) as r (r.raw === r.raw && r.label)}
                  <div
                    class="te-eca-row {r.isMagicChild ? 'te-eca-magic' : ''}"
                    style="padding-left: {r.depth * 16}px"
                    title={r.hint || undefined}
                  >{@html renderColoredLabel(r.label)}</div>
                {/each}
                {#if parts.actions.length === 0}
                  <div class="te-eca-empty">(none)</div>
                {/if}
              </div>
            </details>
          </div>
        {/if}
      </div>
    </div>
  </Dialog.Content>
</Dialog.Root>

<style>
  .te-tree {
    background: rgba(0, 0, 0, 0.15);
  }
  .te-row {
    display: flex;
    align-items: center;
    width: 100%;
    text-align: left;
    padding: 2px 8px;
    gap: 6px;
    font-size: 12px;
    color: #d4d4d8;
    background: none;
    border: 0;
    cursor: pointer;
    line-height: 1.45;
  }
  .te-row:hover { background: rgba(255, 255, 255, 0.04); }
  .te-row-sel { background: rgba(96, 165, 250, 0.18) !important; color: #fff; }
  .te-icon { display: inline-block; width: 14px; text-align: center; color: #71717a; font-size: 11px; }
  .te-name { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .te-disabled { color: #71717a; text-decoration: line-through; }

  .te-detail { background: #18181b; }
  .te-section { border: 1px solid #27272a; border-radius: 4px; }
  .te-section-summary {
    cursor: pointer;
    padding: 6px 10px;
    background: #1f1f23;
    color: #e4e4e7;
    font-size: 13px;
    user-select: none;
  }
  .te-eca-list { padding: 4px 0; background: #1c1c20; }
  .te-eca-row {
    font-family: "Consolas", "Courier New", monospace;
    font-size: 12px;
    padding: 1px 10px;
    white-space: pre-wrap;
    color: #d4d4d8;
  }
  .te-eca-magic { color: #a5b4fc; font-style: italic; }
  .te-eca-empty { padding: 4px 10px; color: #71717a; font-size: 12px; font-style: italic; }

  .te-script {
    font-family: "Consolas", "Courier New", monospace;
    font-size: 12px;
    line-height: 1.4;
    background: #0e0e10;
    color: #d4d4d8;
    padding: 10px;
    border-radius: 4px;
    overflow: auto;
    white-space: pre;
    max-height: calc(85vh - 200px);
  }
  /* These are emitted by highlightScript via {@html}; not scoped — keep
     globals so the inline classes match. */
  :global(.te-kw) { color: #c084fc; }
  :global(.te-str) { color: #86efac; }
  :global(.te-cmt) { color: #71717a; font-style: italic; }
</style>
