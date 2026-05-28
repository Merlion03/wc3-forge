<script lang="ts">
  // Trigger Editor (Phase 2a — structural editing). Modal dialog with a
  // two-pane layout: left pane shows the category/trigger/variable tree
  // (drag-drop + context menu); right pane shows the selected node's
  // details (Monaco editor for script triggers + Map Header, editable
  // forms for variables/comments, read-only ECA tree for GUI triggers).
  //
  // Phase 2a additions over 1a:
  // - Toolbar: Add Category / Add GUI / Add Script / Add Comment / Add Var
  // - Tree row hover: delete button + rename inline
  // - HTML5 drag-drop to re-parent (refuses self + non-category targets)
  // - Right-pane editors: Monaco for script/Map Header, form for
  //   variables, textarea for comment descriptions, toggle checkboxes for
  //   GUI trigger flags (is_enabled, initially_on, run_on_initialization)
  // - subscribes to wc3-forge:entity-changed to refresh on bridge mutations
  //
  // The Monaco editor lazy-loads on first mount (see MonacoEditor.svelte);
  // cold-start of dialogs that don't use it (Object Editor, etc.) is
  // unchanged.

  import { onMount, onDestroy } from 'svelte'
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
  import {
    ListTriggerTree,
    GetTrigger,
    GetTriggerFunctionsMeta,
    AddTriggerCategory,
    AddGUITrigger,
    AddScriptTrigger,
    AddCommentTrigger,
    AddTriggerVariable,
    DeleteTriggerNode,
    RenameTriggerNode,
    SetTriggerEnabled,
    SetTriggerInitiallyOn,
    SetTriggerRunOnInit,
    MoveTriggerNode,
    SetTriggerCustomText,
    SetTriggerDescription,
    SetTriggerVariable,
    SetMapHeaderScript,
    AddTriggerECA,
    DeleteTriggerECA,
    MoveTriggerECA,
    SetTriggerECAEnabled,
    SetTriggerParamValue,
    SetTriggerParamSubFunction,
    ClearTriggerParamSubFunction,
    SetTriggerParamArray,
    AddTriggerNestedECA,
    TestMap,
    paramLabel,
    renderECALabel,
    MAGIC_ECAS,
    ECAType,
    ParamType,
    type TriggerTree,
    type TriggerTreeNode,
    type TriggerDetail,
    type TriggerECA,
    type TriggerParameter,
    type TriggerVariableRecord,
    type TriggerFunctionMeta,
    type TriggerFunctionsMeta,
  } from './trigger-editor-bindings'
  import TriggerScriptPreview from './TriggerScriptPreview.svelte'
  import TriggerSearchPalette from './TriggerSearchPalette.svelte'
  import { parseWC3Color } from './wc3color'
  import * as Dialog from '$lib/components/ui/dialog'
  import FunctionPicker from './FunctionPicker.svelte'
  import ParamEditor from './ParamEditor.svelte'
  import TriggerEntityPicker from './TriggerEntityPicker.svelte'
  import TriggerInstancePicker, { type InstanceKind } from './TriggerInstancePicker.svelte'
  import type { ObjectKind } from './object-editor-bindings'
  import MonacoEditor from './MonacoEditor.svelte'
  import SaveIcon from '@lucide/svelte/icons/save'
  import Undo2Icon from '@lucide/svelte/icons/undo-2'
  import ListRestartIcon from '@lucide/svelte/icons/list-restart'

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

  // Phase 3 — script preview modal + search palette + Test Map state.
  let showScriptPreview = $state(false)
  let showSearchPalette = $state(false)
  let testMapBusy = $state(false)
  // Toast text + auto-clear timer; surfaces TestMap / generate-script success
  // and error feedback to the user.
  let toastText: string = $state('')
  let toastKind: 'ok' | 'err' = $state('ok')
  let toastTimer: ReturnType<typeof setTimeout> | null = null
  function showToast(msg: string, kind: 'ok' | 'err' = 'ok') {
    toastText = msg
    toastKind = kind
    if (toastTimer) clearTimeout(toastTimer)
    toastTimer = setTimeout(() => (toastText = ''), 4000)
  }

  // After search-palette navigation we briefly highlight the matched ECA so
  // the user can find it. Cleared after 2s by the highlight watcher.
  let highlightedECAPath: number[] | null = $state(null)
  let highlightTimer: ReturnType<typeof setTimeout> | null = null
  function flashECAHighlight(path: number[] | undefined) {
    if (!path) return
    highlightedECAPath = path
    if (highlightTimer) clearTimeout(highlightTimer)
    highlightTimer = setTimeout(() => (highlightedECAPath = null), 2000)
  }

  // Double-Shift detector for the search palette. Press Shift twice within
  // 300 ms to toggle. Ctrl/Cmd+P is a second binding.
  let lastShiftTime = 0
  function onKeydownGlobal(e: KeyboardEvent) {
    if (!open) return
    // Ctrl+P / Cmd+P → open palette
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'p' && !e.shiftKey && !e.altKey) {
      // Don't intercept browser print on text fields — but inside the dialog
      // we own this binding.
      e.preventDefault()
      showSearchPalette = true
      return
    }
    // Double-Shift
    if (e.key === 'Shift') {
      const now = performance.now()
      if (now - lastShiftTime < 300) {
        showSearchPalette = true
        lastShiftTime = 0
      } else {
        lastShiftTime = now
      }
    }
  }

  async function onTestMap() {
    if (testMapBusy) return
    testMapBusy = true
    try {
      // Flush all pending in-memory editor texts BEFORE TestMap. The
      // codegen reads from Session, and Session only sees text that's
      // been explicitly saved. Without this loop, pending edits would
      // silently fail to land in the generated war3map.lua.
      if (pendingTexts.size > 0) {
        const ids = Array.from(pendingTexts.keys())
        for (const id of ids) {
          await commitPendingText(id)
        }
        if (pendingTexts.size > 0) {
          showToast('Could not save all triggers; Test Map cancelled.', 'err')
          return
        }
      }
      await TestMap()
      showToast('Test Map launched — opening Warcraft III…', 'ok')
    } catch (e) {
      showToast(`Test Map failed: ${e}`, 'err')
    } finally {
      testMapBusy = false
    }
  }

  async function onPickSearchHit(triggerID: number, ecaPath: number[] | undefined) {
    showSearchPalette = false
    await selectNode(triggerID)
    flashECAHighlight(ecaPath)
  }

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
  // Also subscribe to entity-changed (Kind="trigger") so MCP-driven
  // mutations refresh both the tree and the current detail.
  let unsubMapChanged: (() => void) | null = null
  let unsubEntityChanged: (() => void) | null = null
  onMount(() => {
    void ensureFunctionsMeta()
    EventsOn('wc3-forge:map-changed', onMapChanged)
    EventsOn('wc3-forge:entity-changed', onEntityChanged)
    unsubMapChanged = () => EventsOff('wc3-forge:map-changed')
    unsubEntityChanged = () => EventsOff('wc3-forge:entity-changed')
  })
  onDestroy(() => {
    unsubMapChanged?.()
    unsubEntityChanged?.()
  })
  function onMapChanged() {
    if (open) {
      void loadTree()
    }
  }
  function onEntityChanged(payload: { kind?: string; id?: number; field?: string }) {
    if (!open || payload?.kind !== 'trigger') return
    void loadTree()
    if (selectedId != null) {
      // Skip the detail re-fetch when the user has unsaved edits for this
      // trigger. selectNode() clears `detail` to null mid-await, which would
      // drop editorBoundId, which would derive editorValue to '' and wipe
      // the in-progress edit out of the Monaco model (pendingTexts still
      // holds the text, but the editor can't see it without a bound id).
      // Non-text fields (enabled flag, name) will refresh next time the
      // user switches nodes — acceptable trade for keeping unsaved edits
      // + focus intact.
      if (pendingTexts.has(selectedId)) return
      void selectNode(selectedId)
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

  // ---------------------------------------------------------------------------
  // Phase 2a — structural editing.
  // ---------------------------------------------------------------------------

  // selectedParent resolves the parent category id for "add under this node".
  // If selectedId points at a category-like node, use it; if it points at a
  // trigger/variable, use that node's parent. Falls back to -1 (root) when
  // there's no selection.
  function selectedParent(): number {
    if (selectedId == null) return -1
    const sel = tree.nodes.find((n) => n.id === selectedId)
    if (!sel) return -1
    if (isCategoryLike(sel)) return sel.id
    return sel.parent_id
  }

  // Generic prompt-driven add. Returns true if the add succeeded.
  async function promptAndAdd(kind: 'category' | 'gui' | 'script' | 'comment' | 'variable'): Promise<void> {
    const label = kind === 'variable' ? 'variable name (udg_*)' : `${kind} name`
    const name = window.prompt(`New ${label}:`, '')
    if (!name) return
    try {
      const parent = selectedParent()
      let res
      if (kind === 'category') res = await AddTriggerCategory(name, parent)
      else if (kind === 'gui') res = await AddGUITrigger(name, parent)
      else if (kind === 'script') res = await AddScriptTrigger(name, parent)
      else if (kind === 'comment') res = await AddCommentTrigger(name, parent)
      else {
        const varType = window.prompt('Variable type (integer/unit/real/boolean/...):', 'integer')
        if (!varType) return
        res = await AddTriggerVariable(name, varType, false, 0, '')
      }
      tree = res.tree
      if (res.new_id != null) {
        selectedId = res.new_id
        if (res.detail) detail = res.detail
      }
    } catch (e) {
      errorMsg = `Add failed: ${e}`
    }
  }

  async function onClickDelete(id: number) {
    if (!window.confirm('Delete this node?')) return
    try {
      const res = await DeleteTriggerNode(id)
      tree = res.tree
      if (selectedId === id) {
        selectedId = null
        detail = null
      }
    } catch (e) {
      errorMsg = `Delete failed: ${e}`
    }
  }

  async function onClickRename(id: number, currentName: string) {
    const name = window.prompt('Rename to:', currentName)
    if (!name || name === currentName) return
    try {
      const res = await RenameTriggerNode(id, name)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Rename failed: ${e}`
    }
  }

  async function onToggleEnabled(id: number, next: boolean) {
    try {
      const res = await SetTriggerEnabled(id, next)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Toggle failed: ${e}`
    }
  }
  async function onToggleInitiallyOn(id: number, next: boolean) {
    try {
      const res = await SetTriggerInitiallyOn(id, next)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Toggle failed: ${e}`
    }
  }
  async function onToggleRunOnInit(id: number, next: boolean) {
    try {
      const res = await SetTriggerRunOnInit(id, next)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Toggle failed: ${e}`
    }
  }

  // Drag-drop state. dragId is the id being dragged; dropTargetId highlights
  // the would-be parent. The browser fires dragover continuously; we read
  // dataTransfer at drop time to confirm the type is correct.
  let dragId: number | null = $state(null)
  let dropTargetId: number | null = $state(null)
  function onDragStart(ev: DragEvent, id: number) {
    dragId = id
    if (ev.dataTransfer) {
      ev.dataTransfer.effectAllowed = 'move'
      ev.dataTransfer.setData('application/x-wc3-forge-trigger', String(id))
    }
  }
  function onDragOver(ev: DragEvent, targetID: number, isCatLike: boolean) {
    if (dragId == null) return
    if (!isCatLike) return
    if (dragId === targetID) return
    ev.preventDefault()
    if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'move'
    dropTargetId = targetID
  }
  function onDragLeave() {
    dropTargetId = null
  }
  async function onDrop(ev: DragEvent, targetID: number, isCatLike: boolean) {
    ev.preventDefault()
    if (dragId == null || !isCatLike) {
      dragId = null
      dropTargetId = null
      return
    }
    const src = dragId
    dragId = null
    dropTargetId = null
    if (src === targetID) return
    try {
      const res = await MoveTriggerNode(src, targetID)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Move failed: ${e}`
    }
  }
  function onDragEnd() {
    dragId = null
    dropTargetId = null
  }

  // ---------------------------------------------------------------------------
  // Monaco editor binding — explicit Save (VSCode-style).
  //
  // The Monaco wrapper is pure: every keystroke fires onChange synchronously
  // with the latest text. We DON'T pipe that into App.SetTriggerCustomText /
  // App.SetMapHeaderScript anymore — the old auto-commit + entity-changed
  // refresh loop re-fetched detail, which re-mounted Monaco, which dropped
  // focus and flashed on every keystroke.
  //
  // Instead we keep two per-trigger maps:
  //   - originalTexts: last server-known text for each loaded trigger.
  //     Updated by the detail-sync $effect and by commitPendingText on Save.
  //   - pendingTexts: in-memory editor text awaiting Save. Presence in this
  //     map === dirty. On Save we route to the right binding and clear the
  //     entry. On Discard (dialog close confirm) we just clear the map.
  //
  // The Monaco component reads `pendingTexts.get(id) ?? originalTexts.get(id)`,
  // so entity-changed refreshes (rename, enabled-flag, etc.) update detail
  // and originalText for OTHER triggers without ever stomping the editor's
  // unsaved buffer for the currently-edited one.
  // ---------------------------------------------------------------------------

  let editorLanguage: 'lua' | 'jass' | 'plaintext' = $state('lua')
  let editorBoundId: number | null = $state(null)
  let editorCommitKind: 'script' | 'mapheader' | 'description' | null = $state(null)
  // Per-trigger dirty/clean text. Key = trigger id (also the synthetic Map
  // Header id). Svelte 5 doesn't track Map mutations — we reassign on every
  // set/delete to force reactivity.
  let pendingTexts: Map<number, string> = $state(new Map())
  let originalTexts: Map<number, string> = $state(new Map())

  // Resolve the currently-rendered text for the bound editor: pending wins,
  // then original, then empty. $derived keeps Monaco reactive without an
  // intermediate $state that we'd have to remember to re-set.
  let editorValue = $derived.by(() => {
    if (editorBoundId == null) return ''
    return pendingTexts.get(editorBoundId) ?? originalTexts.get(editorBoundId) ?? ''
  })
  let editorDirty = $derived(editorBoundId != null && pendingTexts.has(editorBoundId))

  // Detect the script language for a given script trigger. We don't have
  // info.Lua piped to the frontend, so fall back to filename matching for
  // synthesized Map Header triggers and default to Lua otherwise (Reforged
  // bias — most new maps are Lua, and Lua's highlighter degrades better on
  // JASS source than vice versa).
  function detectLanguage(name: string | undefined): 'lua' | 'jass' {
    if (name && /\.j$/i.test(name)) return 'jass'
    return 'lua'
  }

  // Map header detection mirrors the back-end classifier: synthesized header
  // triggers are named "war3map.lua" or "war3map.j". The dot path covers both
  // languages with one regex.
  function isMapHeaderName(name: string | undefined): boolean {
    return !!name?.match(/^war3map\.(lua|j)$/)
  }

  // Sync editor metadata + originalTexts from the loaded detail. Runs on
  // EVERY detail change (rename, toggle, MCP-driven mutation refresh). The
  // pendingTexts entry — if any — is the editor's source of truth, so
  // refreshing originalTexts here is safe even mid-edit.
  //
  // NB: selectNode() clears `detail = null` BEFORE awaiting GetTrigger. We
  // tolerate that transient null when selectedId is still set + matches the
  // currently-bound editor — leaving editorBoundId intact keeps Monaco's
  // value stable so the user doesn't see a blank-flash on selection refresh
  // (notably after their own Save round-trip).
  $effect(() => {
    if (!detail) {
      if (selectedId == null || editorBoundId !== selectedId) {
        editorBoundId = null
        editorCommitKind = null
      }
      return
    }
    if (detail.kind === 'trigger_script') {
      const t = detail.trigger
      const id = t?.id ?? 0
      editorBoundId = id
      editorLanguage = detectLanguage(t?.name)
      editorCommitKind = isMapHeaderName(t?.name) ? 'mapheader' : 'script'
      // Refresh original text from server. If the user hasn't typed yet,
      // pendingTexts.get(id) is undefined and Monaco renders this directly.
      // If they HAVE typed, pendingTexts wins (see $derived above).
      const fresh = t?.custom_text || ''
      if (originalTexts.get(id) !== fresh) {
        originalTexts.set(id, fresh)
        originalTexts = new Map(originalTexts)
      }
      return
    }
    if (detail.kind === 'trigger_comment') {
      const t = detail.trigger
      const id = t?.id ?? 0
      editorBoundId = id
      editorLanguage = 'plaintext'
      editorCommitKind = 'description'
      const fresh = t?.description || ''
      if (originalTexts.get(id) !== fresh) {
        originalTexts.set(id, fresh)
        originalTexts = new Map(originalTexts)
      }
      return
    }
    editorBoundId = null
    editorCommitKind = null
  })

  // onChange from Monaco — pure local-state mutation. If the user types back
  // to the original, drop the pending entry so the dirty indicator clears
  // without a Save round-trip (matches VSCode).
  function onEditorChange(text: string) {
    if (editorBoundId == null) return
    const id = editorBoundId
    const orig = originalTexts.get(id) ?? ''
    if (text === orig) {
      if (pendingTexts.delete(id)) {
        pendingTexts = new Map(pendingTexts)
      }
    } else {
      pendingTexts.set(id, text)
      pendingTexts = new Map(pendingTexts)
    }
  }

  // Classify a trigger id for routing on Save. Map header detection uses the
  // tree row's `name` (the detail payload isn't necessarily current for the
  // id we're flushing — Test Map iterates ALL pending ids, not just the one
  // visible in the right pane). Defaults to 'script' for unknown ids; that's
  // the safer fallback (SetTriggerCustomText errors loudly on missing ids
  // and we surface the error).
  function classifyPendingId(id: number): 'script' | 'mapheader' | 'description' | null {
    const node = tree.nodes.find((n) => n.id === id)
    if (!node) return null
    if (node.kind === 'trigger_script') {
      return isMapHeaderName(node.name) ? 'mapheader' : 'script'
    }
    if (node.kind === 'trigger_comment') return 'description'
    return null
  }

  // Commit one pending editor's text via the appropriate back-end binding.
  // On success: drop from pending, refresh originalText. On failure: keep
  // pending + surface the error so the user can retry.
  async function commitPendingText(id: number): Promise<boolean> {
    const text = pendingTexts.get(id)
    if (text === undefined) return true
    const kind = classifyPendingId(id)
    if (kind == null) {
      errorMsg = `Cannot save trigger ${id}: unknown kind`
      return false
    }
    try {
      if (kind === 'mapheader') {
        await SetMapHeaderScript(text)
      } else if (kind === 'script') {
        await SetTriggerCustomText(id, text)
      } else if (kind === 'description') {
        await SetTriggerDescription(id, text)
      }
      originalTexts.set(id, text)
      originalTexts = new Map(originalTexts)
      pendingTexts.delete(id)
      pendingTexts = new Map(pendingTexts)
      return true
    } catch (e) {
      errorMsg = `Save failed: ${e}`
      return false
    }
  }

  // Discard the currently-edited trigger's pending text. Monaco re-reads
  // originalText via the `editorValue` $derived → MonacoEditor's `value`
  // $effect path; no manual setValue / remount needed.
  function discardCurrent() {
    if (editorBoundId == null) return
    if (!pendingTexts.has(editorBoundId)) return
    pendingTexts.delete(editorBoundId)
    pendingTexts = new Map(pendingTexts)
  }

  // Discard every pending edit across all triggers. Confirms once because the
  // button click itself isn't a confirmation. Dialog-close uses its own
  // confirmDiscardOnClose path — same prompt copy, but separate trigger so
  // the user can't accidentally double-confirm.
  function discardAll() {
    if (pendingTexts.size === 0) return
    const n = pendingTexts.size
    const ok = window.confirm(`Discard ${n} unsaved trigger edit${n === 1 ? '' : 's'}?`)
    if (!ok) return
    pendingTexts = new Map()
  }

  // Discard one specific trigger's pending edit (used by per-row hover icon
  // in the tree). Does NOT prompt — the user explicitly clicked the small
  // discard icon next to a known-dirty row.
  function discardOne(id: number) {
    if (!pendingTexts.has(id)) return
    pendingTexts.delete(id)
    pendingTexts = new Map(pendingTexts)
  }

  // Ctrl+S / Cmd+S inside Monaco — wired via MonacoEditor's onSave prop. The
  // wrapper registers a Monaco command so the chord never escapes the editor
  // (no browser print dialog, no Monaco "Save" overlay leakage).
  function onEditorSaveShortcut() {
    if (editorBoundId != null && pendingTexts.has(editorBoundId)) {
      void commitPendingText(editorBoundId)
    }
  }

  // Close-with-pending guard. Wired into Dialog.Root's onOpenChange. Returns
  // true when close should proceed, false to keep the dialog open. The
  // Dialog.Root's bind:open is the source of truth for the modal — if we
  // veto the close we re-assert `open = true` on the next tick.
  function confirmDiscardOnClose(): boolean {
    if (pendingTexts.size === 0) return true
    const n = pendingTexts.size
    const ok = window.confirm(`You have ${n} unsaved trigger edit${n === 1 ? '' : 's'}. Discard?`)
    if (!ok) return false
    pendingTexts = new Map()
    return true
  }

  // Variable form state. Mirrors the wtg.Variable struct.
  let varFormName = $state('')
  let varFormType = $state('integer')
  let varFormIsArray = $state(false)
  let varFormArraySize = $state(0)
  let varFormInitialValue = $state('')
  let varFormBoundId: number | null = $state(null)

  $effect(() => {
    if (detail?.kind === 'variable' && detail.variable) {
      const v = detail.variable
      if (varFormBoundId !== v.id) {
        varFormName = v.name
        varFormType = v.type
        varFormIsArray = !!v.is_array
        varFormArraySize = v.array_size ?? 0
        varFormInitialValue = v.initial_value || ''
        varFormBoundId = v.id
      }
    } else {
      varFormBoundId = null
    }
  })

  async function onVarSave() {
    if (varFormBoundId == null) return
    try {
      const res = await SetTriggerVariable(
        varFormBoundId,
        varFormName,
        varFormType,
        varFormIsArray,
        varFormArraySize,
        varFormInitialValue,
      )
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Variable save failed: ${e}`
    }
  }

  // ---------------------------------------------------------------------------
  // Phase 2b1 — ECA add/delete/move + per-param editing.
  //
  // The FunctionPicker, ParamEditor, and TriggerEntityPicker are modal — we
  // own their open state here and dispatch into Session via the bindings. All
  // mutations refresh `tree` + `detail` from the response so the UI repaints
  // immediately.
  // ---------------------------------------------------------------------------

  // FunctionPicker state. When non-null, the picker is open for the given
  // section + the currently-selected trigger. The user's pick is committed via
  // commitFunctionPick.
  let funcPickerSection: 'TriggerEvents' | 'TriggerConditions' | 'TriggerActions' | 'TriggerCalls' | null = $state(null)
  let funcPickerForTriggerId: number | null = $state(null)
  function openFunctionPicker(section: typeof funcPickerSection) {
    if (!detail || detail.kind !== 'trigger_gui' || !detail.trigger) return
    funcPickerSection = section
    funcPickerForTriggerId = detail.trigger.id
  }
  async function commitFunctionPick(name: string) {
    if (funcPickerForTriggerId == null || !funcPickerSection) return
    const ecaType = sectionToECAType(funcPickerSection)
    try {
      let res
      if (magicAddTarget) {
        // Magic-ECA child add — use the path-aware AddNestedECA. The slot's
        // ecaType comes from the magicChildSlots mapping table.
        const tgt = magicAddTarget
        res = await AddTriggerNestedECA(funcPickerForTriggerId, tgt.parentPath, tgt.slot.ecaType, name, tgt.slot.groupID)
        magicAddTarget = null
      } else {
        res = await AddTriggerECA(funcPickerForTriggerId, ecaType, name, -1)
      }
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Add ECA failed: ${e}`
    } finally {
      funcPickerSection = null
      funcPickerForTriggerId = null
    }
  }
  function sectionToECAType(section: typeof funcPickerSection): number {
    switch (section) {
      case 'TriggerEvents': return ECAType.Event
      case 'TriggerConditions': return ECAType.Condition
      case 'TriggerActions': return ECAType.Action
      case 'TriggerCalls': return ECAType.Call
      default: return ECAType.Action
    }
  }

  // ParamEditor state — 2b2 modal opens a *recursive* editor pane. The user
  // clicks an ECA's `~ArgN` token; we open the modal anchored at that param.
  // The modal then renders the outer param + (if it has sub_parameters) each
  // inner param recursively as nested ParamEditor instances. paramPath drills
  // into the sub-parameter chain.
  let paramEditorOpen = $state(false)
  let paramEditorTriggerId: number | null = $state(null)
  let paramEditorECAPath: number[] = $state([])
  // paramEditorRootPath addresses the LEAF ECA's parameter slot (paramPath
  // sense; length-1 at modal-open time). The recursive render walks deeper
  // by extending paramPath one int per level.
  let paramEditorRootPath: number[] = $state([])
  let paramEditorRootType: string = $state('')

  function openParamEditor(triggerId: number, ecaPath: number[], paramIndex: number, declaredType: string, _param: TriggerParameter | null) {
    paramEditorTriggerId = triggerId
    paramEditorECAPath = ecaPath
    paramEditorRootPath = [paramIndex]
    paramEditorRootType = declaredType
    paramEditorOpen = true
  }

  // Resolve a paramPath inside the currently-loaded `detail` to get the live
  // Parameter the editor renders. Walks ecaPath via `detail.trigger.ecas`,
  // then paramPath via .parameters and .sub_parameter chains.
  function resolveLiveParam(ecaPath: number[], paramPath: number[]): TriggerParameter | null {
    const ecas = detail?.trigger?.ecas
    if (!ecas) return null
    let cur: TriggerECA | undefined = undefined
    let curArr: TriggerECA[] | undefined = ecas
    for (let i = 0; i < ecaPath.length; i++) {
      if (!curArr || ecaPath[i] < 0 || ecaPath[i] >= curArr.length) return null
      cur = curArr[ecaPath[i]]
      if (i < ecaPath.length - 1) {
        curArr = cur.children
      }
    }
    if (!cur) return null
    if (paramPath.length === 0) return null
    if (!cur.parameters || paramPath[0] >= cur.parameters.length) return null
    let p: TriggerParameter | null | undefined = cur.parameters[paramPath[0]]
    for (let i = 1; i < paramPath.length; i++) {
      if (!p?.has_sub_parameter || !p.sub_parameter) return null
      const inner = p.sub_parameter.parameters
      if (!inner || paramPath[i] >= inner.length) return null
      p = inner[paramPath[i]]
    }
    return p ?? null
  }

  // Resolve the declared type of a param at the given paramPath, by walking
  // the same chain through TriggerData's arg_types. The root type is the
  // ECA's argTypes[paramPath[0]]; inner types come from the sub-function's
  // argTypes[paramPath[i]].
  function resolveDeclaredType(eca: TriggerECA | null, paramPath: number[]): string {
    if (!eca) return ''
    const rootMeta = metaByName.get(eca.name)
    if (!rootMeta) return ''
    let curType = rootMeta.arg_types?.[paramPath[0]] || ''
    let curMeta: TriggerFunctionMeta | undefined = rootMeta
    let p: TriggerParameter | undefined = eca.parameters?.[paramPath[0]]
    for (let i = 1; i < paramPath.length; i++) {
      if (!p?.has_sub_parameter || !p.sub_parameter) return curType
      const subName = p.sub_parameter.name
      curMeta = metaByName.get(subName)
      if (!curMeta) return curType
      curType = curMeta.arg_types?.[paramPath[i]] || ''
      p = p.sub_parameter.parameters?.[paramPath[i]]
    }
    return curType
  }

  // Build the "chain" of editor links the modal renders. Each link describes
  // one Parameter slot at a specific paramPath. The chain starts at the root
  // (the slot the user clicked) and extends through every nested sub-function
  // call's first-level parameters. The user can edit each slot independently.
  //
  // For 2b2 the chain renders ALL sub-parameters at each level (not just the
  // first), so editing a 3-arg sub-function shows three nested editors.
  type EditorLink = { path: number[]; declaredType: string; param: TriggerParameter | null }
  function buildParamEditorChain(leafECA: TriggerECA | null, rootPath: number[]): EditorLink[] {
    if (!leafECA) return []
    const out: EditorLink[] = []
    // Root link
    const rootParam = resolveLiveParam(paramEditorECAPath, rootPath)
    const rootType = resolveDeclaredType(leafECA, rootPath)
    out.push({ path: rootPath, declaredType: rootType, param: rootParam })
    // Walk sub-parameter chain. For each sub-function present, recurse into
    // its inner parameters. We render every inner param as an editor link.
    expandSubLinks(leafECA, rootPath, rootParam, out, 1)
    return out
  }

  // expandSubLinks recurses into Parameter.SubParameter.Parameters[]. depth
  // is the current nesting level (used for indent). Stops at depth 16 as a
  // hard safety bound (the user-facing warning kicks in at depth>6).
  function expandSubLinks(leafECA: TriggerECA, parentPath: number[], parentParam: TriggerParameter | null, out: EditorLink[], depth: number): void {
    if (depth > 16) return
    if (!parentParam?.has_sub_parameter || !parentParam.sub_parameter) return
    const sub = parentParam.sub_parameter
    const subMeta = metaByName.get(sub.name)
    const innerParams = sub.parameters ?? []
    for (let i = 0; i < innerParams.length; i++) {
      const ip = parentPath.concat(i)
      const decl = subMeta?.arg_types?.[i] || ''
      const inner = innerParams[i] ?? null
      out.push({ path: ip, declaredType: decl, param: inner })
      // Recurse if this inner param ALSO has a sub-function.
      if (inner?.has_sub_parameter && inner.sub_parameter) {
        expandSubLinks(leafECA, ip, inner, out, depth + 1)
      }
    }
  }

  // Resolve the leaf ECA (the one ecaPath addresses) inside detail.
  function resolveLeafECA(ecaPath: number[]): TriggerECA | null {
    const ecas = detail?.trigger?.ecas
    if (!ecas) return null
    let curArr: TriggerECA[] | undefined = ecas
    let cur: TriggerECA | undefined
    for (let i = 0; i < ecaPath.length; i++) {
      if (!curArr || ecaPath[i] < 0 || ecaPath[i] >= curArr.length) return null
      cur = curArr[ecaPath[i]]
      if (i < ecaPath.length - 1) curArr = cur.children
    }
    return cur ?? null
  }

  async function commitParamValue(paramPath: number[], { value, paramType }: { value: string; paramType: number }) {
    if (paramEditorTriggerId == null) return
    try {
      const res = await SetTriggerParamValue(paramEditorTriggerId, paramEditorECAPath, paramPath, value, paramType)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Set param failed: ${e}`
    }
  }
  async function commitPickSub(paramPath: number[], subName: string) {
    if (paramEditorTriggerId == null) return
    try {
      const res = await SetTriggerParamSubFunction(paramEditorTriggerId, paramEditorECAPath, paramPath, subName)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Set sub-function failed: ${e}`
    }
  }
  async function commitClearSub(paramPath: number[]) {
    if (paramEditorTriggerId == null) return
    try {
      const res = await ClearTriggerParamSubFunction(paramEditorTriggerId, paramEditorECAPath, paramPath)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Clear sub-function failed: ${e}`
    }
  }
  async function commitToggleArray(paramPath: number[], isArray: boolean) {
    if (paramEditorTriggerId == null) return
    try {
      const res = await SetTriggerParamArray(paramEditorTriggerId, paramEditorECAPath, paramPath, isArray)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Toggle array failed: ${e}`
    }
  }

  // *code entity-picker state. Opened from ParamEditor's "Pick…" button.
  let entityPickerOpen = $state(false)
  let entityPickerKind: ObjectKind = $state('units')
  let entityPickerCurrent = $state('')
  let entityPickerCommit: ((id: string) => void) | null = null
  function onPickEntityFromParamEditor(kind: ObjectKind, current: string, commit: (id: string) => void) {
    entityPickerKind = kind
    entityPickerCurrent = current
    entityPickerCommit = commit
    entityPickerOpen = true
  }
  function onEntityPicked(id: string) {
    entityPickerCommit?.(id)
    entityPickerCommit = null
    entityPickerOpen = false
  }

  // Instance picker state (unit / destructable / region / camera).
  let instancePickerOpen = $state(false)
  let instancePickerKind: InstanceKind = $state('unit')
  let instancePickerCommit: ((ggRef: string) => void) | null = null
  function onPickInstanceFromParamEditor(kind: InstanceKind, commit: (ggRef: string) => void) {
    instancePickerKind = kind
    instancePickerCommit = commit
    instancePickerOpen = true
  }
  function onInstancePicked(ggRef: string, _label: string) {
    instancePickerCommit?.(ggRef)
    instancePickerCommit = null
    instancePickerOpen = false
  }

  // Delete-ECA / Move-ECA / Toggle-Enabled handlers.
  async function deleteECA(triggerId: number, ecaPath: number[]) {
    if (!window.confirm('Delete this ECA?')) return
    try {
      const res = await DeleteTriggerECA(triggerId, ecaPath)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Delete ECA failed: ${e}`
    }
  }
  async function moveECAByDelta(triggerId: number, ecaPath: number[], delta: number) {
    const leaf = ecaPath[ecaPath.length - 1]
    const newPos = leaf + delta
    if (newPos < 0) return
    const newPath = [...ecaPath.slice(0, -1), newPos]
    void newPath
    try {
      const res = await MoveTriggerECA(triggerId, ecaPath, newPos)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Move ECA failed: ${e}`
    }
  }
  async function toggleECAEnabled(triggerId: number, ecaPath: number[], enabled: boolean) {
    try {
      const res = await SetTriggerECAEnabled(triggerId, ecaPath, enabled)
      tree = res.tree
      if (res.detail) detail = res.detail
    } catch (e) {
      errorMsg = `Toggle ECA failed: ${e}`
    }
  }

  // topLevelPathOf returns the index path for a top-level ECA inside its
  // trigger. Used by the template to derive the eca_path arg for mutations.
  // For 2b1 every clickable row was top-level; 2b2 adds inline editors for
  // magic-ECA children via the ecaPath extension below.
  function topLevelPathOf(all: TriggerECA[], eca: TriggerECA): number[] {
    for (let i = 0; i < all.length; i++) {
      if (all[i] === eca) return [i]
    }
    return [0]
  }

  // Magic-ECA child renderer state. For each magic ECA we group children by
  // ECA.group + render each group as its own editable list with a "+ Add"
  // button. The Add button needs to know which group + ecaType to seed the
  // child's parent assignment. We pre-compute the per-magic-name mapping
  // table once.
  type MagicChildSlot = { groupID: number; label: string; ecaType: number; section: 'TriggerEvents' | 'TriggerConditions' | 'TriggerActions' | 'TriggerCalls' }
  function magicChildSlots(parentName: string): MagicChildSlot[] {
    // HiveWE convention (trigger_editor.cpp L351-365):
    //   IfThenElse(Multiple):  group 0=Conditions(condition), 1=Then(action), 2=Else(action)
    //   ForLoopA/B/Var(Multiple): group 0=Loop body (action)
    //   AndMultiple/OrMultiple: group 0=Conditions (condition)
    switch (parentName) {
      case 'IfThenElse':
      case 'IfThenElseMultiple':
        return [
          { groupID: 0, label: 'If — Conditions', ecaType: 1, section: 'TriggerConditions' },
          { groupID: 1, label: 'Then — Actions', ecaType: 2, section: 'TriggerActions' },
          { groupID: 2, label: 'Else — Actions', ecaType: 2, section: 'TriggerActions' },
        ]
      case 'ForLoopA':
      case 'ForLoopAMultiple':
      case 'ForLoopB':
      case 'ForLoopBMultiple':
      case 'ForLoopVar':
      case 'ForLoopVarMultiple':
        return [
          { groupID: 0, label: 'Loop — Actions', ecaType: 2, section: 'TriggerActions' },
        ]
      case 'AndMultiple':
      case 'OrMultiple':
        return [
          { groupID: 0, label: 'Conditions', ecaType: 1, section: 'TriggerConditions' },
        ]
      default:
        return []
    }
  }

  // Filter the children of a parent magic ECA to one bucket. Children whose
  // group field doesn't match are skipped (their slot displays nothing).
  function childrenForGroup(parent: TriggerECA, groupID: number): TriggerECA[] {
    return (parent.children ?? []).filter((c) => (c.group ?? 0) === groupID)
  }

  // Add an ECA to a magic-ECA child slot. The child gets the right ecaType
  // for its bucket; HiveWE sets ECA.group to the bucket index post-creation.
  // Our wire shape adds via the same AddECA mutator with position appended;
  // for nested children we need a different mutator (path-extended Add). For
  // 2b2 we use the same AddTriggerECA but pass a path that addresses the
  // magic parent + the child slot via a magic-bucket convention.
  //
  // The simple approach: use the recursive mutator path. addMagicChild
  // appends to parent.Children[] then sets group via SetParamValue-style
  // path. Since we don't currently have an AddNestedECA mutator, we delegate
  // to a per-call MCP path: add the child via AddTriggerECA's normal path,
  // then we MOVE it into the right group via a follow-up step. For now,
  // since the wtg encoder accepts any nested ECA, we'll punt on grouping
  // metadata and just append — the engine renders unmarked groups in the
  // same way (Phase 2b3 polish).
  //
  // Implementation: open the FunctionPicker with the right section, then on
  // pick, call a NEW handler we'll add (AddTriggerNestedECA) — for 2b2,
  // synthesize this as a SetParamValue-like nested path via the Go-side
  // already-extended AddECA's ecaPath path arg. (The 2b1 AddECA signature
  // takes ecaType+name; we extend it client-side to address the magic
  // parent.)
  let magicAddTarget: { parentPath: number[]; slot: MagicChildSlot } | null = $state(null)
  function openMagicAdd(parentPath: number[], slot: MagicChildSlot) {
    magicAddTarget = { parentPath, slot }
    funcPickerSection = slot.section
    if (detail?.kind === 'trigger_gui' && detail.trigger) {
      funcPickerForTriggerId = detail.trigger.id
    }
  }

  // Variables visible to the currently-selected trigger's ParamEditor. Today
  // every global variable is in scope; future per-trigger locals would filter.
  let visibleVariables = $derived.by(() => {
    const out: TriggerVariableRecord[] = []
    for (const n of tree.nodes ?? []) {
      if (n.kind === 'variable') {
        out.push({
          name: n.name,
          type: 'integer', // not in the tree-node payload; default to integer
          id: n.id,
          parent_id: n.parent_id,
          is_initialized: false,
          initial_value: '',
          is_array: false,
        } as TriggerVariableRecord)
      }
    }
    return out
  })

  // For each ECA row, build a click-token spec from its parameters_template.
  // The picker calls openParamEditor with the right argument index when the
  // user clicks a `~ArgN` token. Literals stay as plain text. Returns an
  // ordered list of { kind: 'text'|'param', text? , paramIndex?, type? }.
  type LabelToken = { kind: 'text'; text: string } | { kind: 'param'; paramIndex: number; type: string; label: string }
  function buildLabelTokens(eca: TriggerECA): LabelToken[] {
    const meta = metaByName.get(eca.name)
    const tpl = meta?.parameters_template
    const params = eca.parameters ?? []
    if (!tpl || tpl.length === 0) {
      // Fallback: synthetic "name(p1, p2, …)" with every param clickable.
      const tokens: LabelToken[] = [{ kind: 'text', text: `${eca.name}(` }]
      params.forEach((p, i) => {
        if (i > 0) tokens.push({ kind: 'text', text: ', ' })
        tokens.push({
          kind: 'param',
          paramIndex: i,
          type: meta?.arg_types?.[i] || '',
          label: paramLabel(p),
        })
      })
      tokens.push({ kind: 'text', text: ')' })
      return tokens
    }
    const tokens: LabelToken[] = []
    let argIdx = 0
    for (const seg of tpl) {
      if (seg.startsWith('~')) {
        tokens.push({
          kind: 'param',
          paramIndex: argIdx,
          type: meta?.arg_types?.[argIdx] || '',
          label: paramLabel(params[argIdx]),
        })
        argIdx++
      } else {
        tokens.push({ kind: 'text', text: seg })
      }
    }
    return tokens
  }
</script>

<!-- Per-editor toolbar — Save + Discard Current + dirty status. Shared by the
     trigger_script and trigger_comment branches so both right-pane editor
     mounts get identical Save semantics. Buttons disable when clean. -->
{#snippet editorToolbar()}
  <div class="te-editor-toolbar">
    <button
      class="te-toolbtn te-toolbtn-icon te-save-btn"
      title="Save (Ctrl+S)"
      disabled={!editorDirty}
      onclick={() => { if (editorBoundId != null) void commitPendingText(editorBoundId) }}
    >
      <SaveIcon size={14} />
      Save
    </button>
    <button
      class="te-toolbtn te-toolbtn-icon"
      title="Discard changes (current trigger)"
      aria-label="Discard changes to current trigger"
      disabled={!editorDirty}
      onclick={discardCurrent}
    >
      <Undo2Icon size={14} />
      Discard
    </button>
    <span class="te-editor-status {editorDirty ? 'te-editor-status-dirty' : ''}">
      {editorDirty ? 'Modified' : ''}
    </span>
  </div>
{/snippet}

<!-- Editable ECA row snippet — used for both top-level ECAs and (recursively)
     magic-ECA children. Rendering magic children inline lets the user edit
     If/Then/Else / Loop bodies in place without entering a sub-dialog. -->
{#snippet ecaRowEditable(trigID: number, e: TriggerECA, path: number[])}
  <div class="te-eca-row te-eca-edit" title={metaByName.get(e.name)?.hint || ''}>
    <button class="te-eca-act" title="Toggle enabled" onclick={() => void toggleECAEnabled(trigID, path, !e.enabled)}>{e.enabled ? '☑' : '☐'}</button>
    <button class="te-eca-act" title="Move up" onclick={() => void moveECAByDelta(trigID, path, -1)}>↑</button>
    <button class="te-eca-act" title="Move down" onclick={() => void moveECAByDelta(trigID, path, +1)}>↓</button>
    <span class="te-eca-tokens {e.enabled ? '' : 'te-disabled'}">
      {#each buildLabelTokens(e) as tok}
        {#if tok.kind === 'text'}{@html renderColoredLabel(tok.text)}{:else}<button class="te-tok" onclick={() => openParamEditor(trigID, path, tok.paramIndex, tok.type, e.parameters?.[tok.paramIndex] ?? null)}>{@html renderColoredLabel(tok.label || '(unset)')}</button>{/if}
      {/each}
    </span>
    <button class="te-eca-act te-eca-del" title="Delete ECA" onclick={() => void deleteECA(trigID, path)}>×</button>
  </div>
  <!-- Magic-ECA children: render each group with its own header + add button. -->
  {#if MAGIC_ECAS.has(e.name)}
    {@const slots = magicChildSlots(e.name)}
    {#each slots as slot (slot.groupID)}
      {@const childrenInSlot = childrenForGroup(e, slot.groupID)}
      <div class="te-magic-slot">
        <div class="te-magic-header">{slot.label}</div>
        {#each childrenInSlot as child, ci (ci + '|' + child.name)}
          {@const childIdx = (e.children ?? []).indexOf(child)}
          {@const childPath = [...path, childIdx]}
          {@render ecaRowEditable(trigID, child, childPath)}
        {/each}
        {#if childrenInSlot.length === 0}
          <div class="te-eca-empty" style="padding-left: 16px">(none)</div>
        {/if}
        <button class="te-eca-add te-magic-add" onclick={() => openMagicAdd(path, slot)}>+ Add to {slot.label}</button>
      </div>
    {/each}
  {/if}
{/snippet}

<Dialog.Root
  bind:open
  onOpenChange={(v) => {
    if (!v) {
      // Veto the close if the user has unsaved edits and cancels the
      // discard prompt. Re-assert open=true on the next microtask so the
      // bind:open snap-back doesn't race the Dialog primitive's own close
      // animation.
      if (!confirmDiscardOnClose()) {
        queueMicrotask(() => (open = true))
        return
      }
      onClose?.()
    }
  }}
>
  <Dialog.Content class="!max-w-[1280px] !w-[95vw] h-[85vh] p-0 flex flex-col">
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>Trigger Editor</Dialog.Title>
      <Dialog.Description>
        {#if tree.is_pre_131}
          Pre-1.31 trigger format. Editing supported but legacy.
        {:else if tree.nodes.length === 0}
          No triggers in this map.
        {:else}
          {tree.nodes.length} nodes.
        {/if}
      </Dialog.Description>
    </Dialog.Header>

    {#if errorMsg}
      <div class="px-4 py-2 bg-red-900/30 text-red-200 text-sm">{errorMsg}</div>
    {/if}

    <!-- Toolbar: structural-add buttons + Phase 3 actions (preview, test map,
         search). New items land under selectedParent(). -->
    <div class="px-3 py-2 border-b flex gap-2 items-center bg-zinc-900/40" onkeydown={onKeydownGlobal} tabindex="-1" role="toolbar">
      <button class="te-toolbtn" title="Add category under selection" onclick={() => promptAndAdd('category')}>+ Category</button>
      <button class="te-toolbtn" title="Add GUI trigger under selection" onclick={() => promptAndAdd('gui')}>+ GUI</button>
      <button class="te-toolbtn" title="Add custom-script trigger under selection" onclick={() => promptAndAdd('script')}>+ Script</button>
      <button class="te-toolbtn" title="Add comment trigger under selection" onclick={() => promptAndAdd('comment')}>+ Comment</button>
      <button class="te-toolbtn" title="Add global variable" onclick={() => promptAndAdd('variable')}>+ Variable</button>
      <div class="border-l h-6 mx-1 border-zinc-700"></div>
      <button class="te-toolbtn" title="Preview generated war3map.lua" onclick={() => (showScriptPreview = true)}>Preview Script</button>
      <button class="te-toolbtn" title="Search all triggers (Ctrl+P or double-Shift)" onclick={() => (showSearchPalette = true)}>Search…</button>
      <button class="te-toolbtn te-toolbtn-icon"
        title={pendingTexts.size === 0 ? 'No unsaved edits' : `Discard ALL unsaved changes (${pendingTexts.size})`}
        onclick={discardAll}
        disabled={pendingTexts.size === 0}
        aria-label="Discard all unsaved changes"
      >
        <ListRestartIcon size={14} />
        {pendingTexts.size > 0 ? `Discard All (${pendingTexts.size})` : 'Discard All'}
      </button>
      <button class="te-toolbtn" title="Save map + regenerate war3map.lua + launch Warcraft III"
        onclick={() => void onTestMap()} disabled={testMapBusy}>
        {testMapBusy ? 'Launching…' : 'Test Map'}
      </button>
      <span class="text-zinc-500 text-xs ml-auto">Drag nodes to reorganize · Ctrl+S to save · Ctrl+P / double-Shift to search</span>
    </div>
    {#if toastText}
      <div class="px-3 py-1 text-sm {toastKind === 'err' ? 'bg-red-900/40 text-red-200' : 'bg-emerald-900/30 text-emerald-200'}">
        {toastText}
      </div>
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
          <div
            class="te-row-wrap {dropTargetId === row.node.id ? 'te-row-drop' : ''}"
            draggable={!isCategoryLike(row.node) || row.node.kind !== 'map'}
            ondragstart={(ev) => onDragStart(ev, row.node.id)}
            ondragover={(ev) => onDragOver(ev, row.node.id, isCategoryLike(row.node))}
            ondragleave={onDragLeave}
            ondrop={(ev) => onDrop(ev, row.node.id, isCategoryLike(row.node))}
            ondragend={onDragEnd}
          >
            <button
              class="te-row {selectedId === row.node.id ? 'te-row-sel' : ''}"
              style="padding-left: {8 + row.depth * 14}px"
              onclick={() => {
                selectNode(row.node.id)
                if (isCategoryLike(row.node)) toggleCollapse(row.node)
              }}
              oncontextmenu={(ev) => {
                ev.preventDefault()
                // Minimalist context menu: rename + delete. Future Phase 2a
                // polish: a proper popup with the toolbar actions.
                const action = window.prompt('Action: r=rename, d=delete (cancel to abort)', 'r')
                if (action === 'r') void onClickRename(row.node.id, row.node.name)
                else if (action === 'd') void onClickDelete(row.node.id)
              }}
              title={row.node.description || row.node.name}
            >
              <span class="te-icon">{iconFor(row.node)}</span>
              {#if pendingTexts.has(row.node.id)}
                <span class="te-row-dot" title="Unsaved changes" aria-label="unsaved"></span>
              {/if}
              <span class="te-name {row.node.is_enabled === false ? 'te-disabled' : ''}">{row.node.name}</span>
            </button>
            <span class="te-row-actions">
              {#if pendingTexts.has(row.node.id)}
                <button class="te-row-act te-row-act-discard" title="Discard changes" aria-label="Discard changes"
                  onclick={(e) => { e.stopPropagation(); discardOne(row.node.id) }}>
                  <Undo2Icon size={11} />
                </button>
              {/if}
              <button class="te-row-act" title="Rename" onclick={(e) => { e.stopPropagation(); void onClickRename(row.node.id, row.node.name) }}>✎</button>
              <button class="te-row-act" title="Delete" onclick={(e) => { e.stopPropagation(); void onClickDelete(row.node.id) }}>×</button>
            </span>
          </div>
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
          <div class="px-4 py-3 space-y-3 text-sm">
            <div class="text-zinc-300 text-lg">{v.name}</div>
            <div class="grid grid-cols-[140px_1fr] gap-y-2 items-center">
              <label for="var-name" class="text-zinc-400">name</label>
              <input id="var-name" class="te-input" bind:value={varFormName} />
              <label for="var-type" class="text-zinc-400">type</label>
              <input id="var-type" class="te-input" bind:value={varFormType} />
              <label for="var-isarr" class="text-zinc-400">is_array</label>
              <label class="text-zinc-200"><input id="var-isarr" type="checkbox" bind:checked={varFormIsArray} /> array</label>
              <label for="var-size" class="text-zinc-400">array_size</label>
              <input id="var-size" type="number" class="te-input" bind:value={varFormArraySize} disabled={!varFormIsArray} />
              <label for="var-init" class="text-zinc-400">initial_value</label>
              <input id="var-init" class="te-input" bind:value={varFormInitialValue} />
            </div>
            <button class="te-toolbtn" onclick={() => void onVarSave()}>Save variable</button>
          </div>
        {:else if detail.kind === 'trigger_comment'}
          {@const t = detail.trigger!}
          <div class="px-4 py-3">
            <div class="text-zinc-300 text-lg flex items-center gap-2">
              {#if editorDirty}<span class="te-dirty-dot" title="Unsaved changes" aria-label="unsaved"></span>{/if}
              <span>{t.name}</span>
            </div>
            <div class="text-zinc-500 text-sm mb-2">Comment · id={t.id}</div>
            {@render editorToolbar()}
            <div class="te-editor">
              <MonacoEditor value={editorValue} language={editorLanguage} onChange={onEditorChange} onSave={onEditorSaveShortcut} />
            </div>
            <div class="text-zinc-500 text-xs mt-2">Ctrl+S to save · changes stay local until saved.</div>
          </div>
        {:else if detail.kind === 'trigger_script'}
          {@const t = detail.trigger!}
          <div class="px-4 py-3">
            <div class="text-zinc-300 text-lg flex items-center gap-2">
              {#if editorDirty}<span class="te-dirty-dot" title="Unsaved changes" aria-label="unsaved"></span>{/if}
              <span>{t.name}</span>
            </div>
            {#if t.description}
              <div class="text-zinc-500 text-sm mb-2">{t.description}</div>
            {/if}
            <div class="text-zinc-500 text-xs mb-2 flex gap-3 items-center">
              <span>Script trigger · id={t.id}</span>
              <label><input type="checkbox" checked={t.is_enabled} onchange={(e) => void onToggleEnabled(t.id, e.currentTarget.checked)} /> enabled</label>
              <label><input type="checkbox" checked={t.initially_on} onchange={(e) => void onToggleInitiallyOn(t.id, e.currentTarget.checked)} /> initially_on</label>
              <label><input type="checkbox" checked={t.run_on_initialization} onchange={(e) => void onToggleRunOnInit(t.id, e.currentTarget.checked)} /> run_on_init</label>
            </div>
            {@render editorToolbar()}
            <div class="te-editor">
              <MonacoEditor value={editorValue} language={editorLanguage} onChange={onEditorChange} onSave={onEditorSaveShortcut} />
            </div>
            <div class="text-zinc-500 text-xs mt-2">Ctrl+S to save · changes stay local until saved.</div>
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
              <div class="text-zinc-500 text-xs flex gap-3 items-center">
                <span>GUI trigger · id={t.id}</span>
                <label><input type="checkbox" checked={t.is_enabled} onchange={(e) => void onToggleEnabled(t.id, e.currentTarget.checked)} /> enabled</label>
                <label><input type="checkbox" checked={t.initially_on} onchange={(e) => void onToggleInitiallyOn(t.id, e.currentTarget.checked)} /> initially_on</label>
                <label><input type="checkbox" checked={t.run_on_initialization} onchange={(e) => void onToggleRunOnInit(t.id, e.currentTarget.checked)} /> run_on_init</label>
              </div>
            </div>

            <!-- Events -->
            <details open class="te-section">
              <summary class="te-section-summary">Events ({parts.events.length})</summary>
              <div class="te-eca-list">
                {#each t.ecas?.filter((e) => e.type === 0) ?? [] as e, i (i + '|' + e.name)}
                  {@const path = topLevelPathOf(t.ecas ?? [], e)}
                  {@render ecaRowEditable(t.id, e, path)}
                {/each}
                {#if parts.events.length === 0}
                  <div class="te-eca-empty">(none)</div>
                {/if}
                <button class="te-eca-add" onclick={() => openFunctionPicker('TriggerEvents')}>+ Add Event</button>
              </div>
            </details>

            <!-- Conditions -->
            <details open class="te-section">
              <summary class="te-section-summary">Conditions ({parts.conditions.length})</summary>
              <div class="te-eca-list">
                {#each t.ecas?.filter((e) => e.type === 1) ?? [] as e, i (i + '|' + e.name)}
                  {@const path = topLevelPathOf(t.ecas ?? [], e)}
                  {@render ecaRowEditable(t.id, e, path)}
                {/each}
                {#if parts.conditions.length === 0}
                  <div class="te-eca-empty">(none)</div>
                {/if}
                <button class="te-eca-add" onclick={() => openFunctionPicker('TriggerConditions')}>+ Add Condition</button>
              </div>
            </details>

            <!-- Actions -->
            <details open class="te-section">
              <summary class="te-section-summary">Actions ({parts.actions.length})</summary>
              <div class="te-eca-list">
                {#each t.ecas?.filter((e) => e.type !== 0 && e.type !== 1) ?? [] as e, i (i + '|' + e.name)}
                  {@const path = topLevelPathOf(t.ecas ?? [], e)}
                  {@render ecaRowEditable(t.id, e, path)}
                {/each}
                {#if parts.actions.length === 0}
                  <div class="te-eca-empty">(none)</div>
                {/if}
                <button class="te-eca-add" onclick={() => openFunctionPicker('TriggerActions')}>+ Add Action</button>
              </div>
            </details>
          </div>
        {/if}
      </div>
    </div>
  </Dialog.Content>
</Dialog.Root>

<!-- Phase 2b1 modal stack — kept outside the Dialog.Root so they layer above
     the main trigger-editor dialog without z-index gymnastics. -->
{#if funcPickerSection != null}
  <FunctionPicker
    open
    section={funcPickerSection}
    meta={functionsMeta}
    onPick={commitFunctionPick}
    onClose={() => { funcPickerSection = null; funcPickerForTriggerId = null }}
  />
{/if}

{#if paramEditorOpen}
  {@const leafECA = resolveLeafECA(paramEditorECAPath)}
  {@const chain = buildParamEditorChain(leafECA, paramEditorRootPath)}
  <Dialog.Root bind:open={paramEditorOpen} onOpenChange={(v) => { if (!v) paramEditorOpen = false }}>
    <Dialog.Content class="!max-w-[640px] !w-[85vw] !max-h-[80vh] overflow-hidden flex flex-col p-0 gap-0">
      <Dialog.Header class="px-4 py-3 border-b">
        <Dialog.Title>Edit Parameter</Dialog.Title>
        <Dialog.Description class="text-xs text-muted-foreground">
          Nesting depth: {chain.length - 1}. Edits commit immediately.
        </Dialog.Description>
      </Dialog.Header>
      <div class="overflow-y-auto px-3 py-3">
        {#each chain as link, di (link.path.join('-'))}
          <ParamEditor
            paramType={link.declaredType}
            param={link.param}
            presets={functionsMeta?.presets ?? []}
            typeMeta={functionsMeta?.type_meta ?? []}
            variables={visibleVariables}
            functionsMeta={functionsMeta?.functions ?? []}
            depth={di}
            onCommitValue={(c) => void commitParamValue(link.path, c)}
            onPickSubFunction={(s) => void commitPickSub(link.path, s)}
            onClearSubFunction={() => void commitClearSub(link.path)}
            onToggleArray={(a) => void commitToggleArray(link.path, a)}
            onPickEntity={onPickEntityFromParamEditor}
            onPickInstance={onPickInstanceFromParamEditor}
          />
        {/each}
      </div>
      <div class="px-4 py-2 border-t flex justify-end">
        <button class="te-toolbtn" onclick={() => (paramEditorOpen = false)}>Done</button>
      </div>
    </Dialog.Content>
  </Dialog.Root>
{/if}

<TriggerEntityPicker
  bind:open={entityPickerOpen}
  kind={entityPickerKind}
  currentValue={entityPickerCurrent}
  onPick={onEntityPicked}
  onClose={() => (entityPickerOpen = false)}
/>

<TriggerInstancePicker
  bind:open={instancePickerOpen}
  kind={instancePickerKind}
  onPick={onInstancePicked}
  onClose={() => (instancePickerOpen = false)}
/>

<!-- Phase 3 — script preview + search palette. Mounted outside the main
     Dialog.Root so they can layer above without z-index gymnastics. -->
<TriggerScriptPreview
  bind:open={showScriptPreview}
  onClose={() => (showScriptPreview = false)}
/>

<TriggerSearchPalette
  bind:open={showSearchPalette}
  onPick={onPickSearchHit}
  onClose={() => (showSearchPalette = false)}
/>

<svelte:window onkeydown={onKeydownGlobal} />

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
  /* .te-eca-magic was used by the read-only Phase 2a row renderer for magic
     ECA section headers; Phase 2b1 doesn't surface those at the top-level
     edit list (children stay read-only behind the picker until 2b2 lands).
     Keeping the selector as a :global hook so a future child-row template
     can re-light it without rebuilding the stylesheet. */
  :global(.te-eca-magic) { color: #a5b4fc; font-style: italic; }
  .te-eca-empty { padding: 4px 10px; color: #71717a; font-size: 12px; font-style: italic; }

  /* Phase 2b1 — editable ECA rows + per-token clickable params. */
  .te-eca-edit {
    display: flex;
    align-items: baseline;
    gap: 6px;
    padding: 2px 8px;
  }
  .te-eca-edit:hover { background: rgba(255,255,255,0.03); }
  .te-eca-act {
    background: transparent;
    border: 0;
    color: #71717a;
    padding: 0 4px;
    cursor: pointer;
    font-size: 11px;
    border-radius: 2px;
    flex: none;
  }
  .te-eca-act:hover { background: rgba(255,255,255,0.08); color: #fbbf24; }
  .te-eca-del:hover { color: #f87171; }
  .te-eca-tokens {
    flex: 1;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .te-tok {
    display: inline;
    background: rgba(96, 165, 250, 0.12);
    color: #c7d2fe;
    border: 0;
    border-bottom: 1px dashed #6366f1;
    padding: 0 2px;
    margin: 0 1px;
    cursor: pointer;
    font: inherit;
    border-radius: 2px;
  }
  .te-tok:hover { background: rgba(96, 165, 250, 0.28); color: #fff; }
  .te-eca-add {
    display: block;
    width: calc(100% - 16px);
    margin: 4px 8px 2px 8px;
    padding: 4px;
    background: rgba(96, 165, 250, 0.10);
    color: #c7d2fe;
    border: 1px dashed #3f3f46;
    border-radius: 4px;
    font-size: 12px;
    cursor: pointer;
  }
  .te-eca-add:hover { background: rgba(96, 165, 250, 0.22); color: #fff; border-color: #60a5fa; }

  /* Phase 2a — toolbar + row actions + editor + form input styles. */
  .te-toolbtn {
    padding: 4px 10px;
    background: #27272a;
    color: #d4d4d8;
    border: 1px solid #3f3f46;
    border-radius: 4px;
    font-size: 12px;
    cursor: pointer;
  }
  .te-toolbtn:hover { background: #3f3f46; }
  .te-row-wrap {
    position: relative;
  }
  .te-row-wrap .te-row-actions {
    position: absolute;
    right: 4px;
    top: 2px;
    display: none;
    gap: 2px;
  }
  .te-row-wrap:hover .te-row-actions {
    display: inline-flex;
  }
  .te-row-act {
    background: transparent;
    border: 0;
    color: #71717a;
    padding: 1px 4px;
    cursor: pointer;
    font-size: 11px;
    border-radius: 2px;
  }
  .te-row-act:hover {
    background: rgba(255,255,255,0.08);
    color: #fbbf24;
  }
  .te-row-drop {
    outline: 1px dashed #60a5fa;
    outline-offset: -1px;
    background: rgba(96, 165, 250, 0.08);
  }
  .te-editor {
    border: 1px solid #27272a;
    border-radius: 4px;
    overflow: hidden;
    /* Fixed height so Monaco's internal layout has a stable parent box.
       calc keeps the editor inside the dialog viewport on small screens. */
    height: calc(85vh - 280px);
    min-height: 200px;
  }
  .te-input {
    background: #0e0e10;
    color: #e4e4e7;
    border: 1px solid #27272a;
    padding: 4px 8px;
    font-size: 13px;
    border-radius: 4px;
    width: 100%;
  }
  .te-input:disabled { opacity: 0.4; }

  /* Phase 2b2 — magic-ECA child slots (If-Conditions / Then-Actions / etc.) */
  .te-magic-slot {
    margin-left: 24px;
    border-left: 2px solid #3f3f46;
    padding: 4px 0 4px 8px;
    margin-top: 2px;
    margin-bottom: 4px;
  }
  .te-magic-header {
    font-size: 11px;
    color: #a5b4fc;
    font-style: italic;
    padding: 2px 4px;
    margin-bottom: 2px;
  }
  .te-magic-add {
    margin: 4px 0 2px 0 !important;
    width: auto !important;
    padding: 2px 8px !important;
    font-size: 11px !important;
  }

  /* Explicit-Save UI — VSCode-style dirty indicators + toolbar. */
  .te-toolbtn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .te-toolbtn-icon {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .te-save-btn:not(:disabled) {
    background: rgba(59, 130, 246, 0.18);
    border-color: #3b82f6;
    color: #dbeafe;
  }
  .te-save-btn:not(:disabled):hover {
    background: rgba(59, 130, 246, 0.32);
    color: #fff;
  }
  .te-editor-toolbar {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px 0 6px 0;
  }
  .te-editor-status {
    font-size: 11px;
    color: #71717a;
  }
  .te-editor-status-dirty {
    color: #fbbf24;
  }
  /* Per-trigger dirty dot — small filled circle next to the name. Mirrors
     VSCode's modified-tab indicator. */
  .te-dirty-dot,
  .te-row-dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #60a5fa;
    flex: none;
  }
  .te-row-dot {
    width: 6px;
    height: 6px;
    margin: 0 2px;
  }
  .te-row-act-discard:hover {
    color: #60a5fa;
  }
</style>
