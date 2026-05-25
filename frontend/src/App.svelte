<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import {
    OpenMapDialog, OpenMap, CloseMap, ListUnits, ListDoodads, Status,
    GetSelection, SetSelection, GetUnit,
    GetReforgedMode, SetReforgedMode,
    GetUnitTypeIndex, GetDoodadTypeIndex,
    MoveUnit, MoveDoodad, IsDirty, SaveMap,
  } from '../wailsjs/go/main/App.js'
  import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime.js'
  import type { main, unitsdoo } from '../wailsjs/go/models'
  import {
    createScene,
    type SceneAPI, type PickHit, type SelectMode, type TerrainCellInfo,
  } from './scene-instances'
  import Toast from './Toast.svelte'
  import Splitter from './Splitter.svelte'
  import Accordion from './Accordion.svelte'
  import ViewMenu from './ViewMenu.svelte'
  import AssetPreview from './AssetPreview.svelte'
  import { showToast } from './toast'

  // Wails drops struct typedefs from models.ts when they appear as map values,
  // so the unit/doodad type-index shapes are declared locally here. Must stay
  // in lockstep with main.UnitTypeInfo / main.DoodadTypeInfo on the Go side.
  interface UnitTypeInfo {
    file: string; model_scale: number; move_height: number
    red: number; green: number; blue: number
    name: string; category: string
  }
  interface DoodadTypeInfo {
    file: string; num_var: number; fixed_rot: number; model_scale: number
    name: string; category: string
  }

  let status: main.MapStatus = { loaded: false, unit_count: 0 }
  let units: main.UnitDTO[] = []
  let doodads: main.DoodadDTO[] = []
  let unitTypes: Record<string, UnitTypeInfo> = {}
  let doodadTypes: Record<string, DoodadTypeInfo> = {}
  // Selection state is split by kind because creation_number is per-kind in
  // WC3 — a unit and a doodad can share an id, so a single Set can't tell
  // them apart. The full SelectionItemDTO[] mirror (selectionItems) is what
  // we use to compose new selections on mode='add'/'toggle' picks.
  let selectedIds = new Set<number>()           // unit creation numbers
  let selectedDoodadIds = new Set<number>()     // doodad creation numbers
  let selectionItems: main.SelectionItemDTO[] = []
  let primaryEntity: unitsdoo.Entity | null = null
  let primaryDoodad: main.DoodadDTO | null = null
  // Persistent state errors only — currently just the scene-init-failed path
  // during onMount. Transient operational errors (Save/Open/Move/Reforged
  // toggle) go through showToast() so they auto-dismiss.
  let error: string = ''
  let busy: boolean = false
  let reforged: boolean = false
  let pathingVisible: boolean = false

  // Terrain-pick mode state. Owned here, mirrored to the scene via
  // scene.setTerrainPickMode. When a click hits a cell, terrainCell is set
  // and the Properties panel renders an additional section. Selecting an
  // entity (or clicking outside the map) does NOT clear it on its own — the
  // user explicitly clicks elsewhere on terrain to update.
  let terrainPickModeOn: boolean = false
  let terrainCell: TerrainCellInfo | null = null

  // Doodad-category visibility — owned here, mirrored to scene via
  // scene.setDoodadCategoryVisible. Categories absent from the map are
  // omitted from the View menu. Visibility is RENDERING-ONLY (never persists
  // to the saved map; the View-menu toggle is a viewport filter).
  let doodadVisibility: Record<string, boolean> = {}
  let doodadCategoriesPresent: string[] = []

  let canvas: HTMLCanvasElement
  let scene: SceneAPI | null = null
  let dirty: boolean = false
  let saving: boolean = false

  // ----- File menu (header dropdown) -----
  let fileMenuOpen: boolean = false
  let fileMenuEl: HTMLDivElement | null = null
  function toggleFileMenu() { fileMenuOpen = !fileMenuOpen }
  function onDocClickForFileMenu(e: MouseEvent) {
    if (!fileMenuOpen) return
    if (fileMenuEl && fileMenuEl.contains(e.target as Node)) return
    fileMenuOpen = false
  }
  function onDocKeyForFileMenu(e: KeyboardEvent) {
    if (e.key === 'Escape' && fileMenuOpen) {
      fileMenuOpen = false
      e.stopPropagation()
    }
  }
  function runMenuAction(fn: () => unknown) {
    return () => {
      fileMenuOpen = false
      void fn()
    }
  }

  // ----- Right-column split (Explorer above Properties) -----
  //
  // Vertical splitter between Explorer and Properties. Tracked as a percent
  // of the right column's height so window-resize keeps the same ratio. The
  // splitter component reports dy in pixels; we convert to a percent delta
  // against the column's pixel height each drag tick.
  // Default: 50/50. Session-only (no persistence).
  let rightExplorerPct: number = 50
  const RIGHT_MIN_PCT = 15
  const RIGHT_MAX_PCT = 85
  let rightColEl: HTMLDivElement | null = null
  function onRightSplitterDrag(dy: number) {
    if (!rightColEl) return
    const h = rightColEl.clientHeight
    if (h <= 0) return
    const dpct = (dy / h) * 100
    const next = Math.max(RIGHT_MIN_PCT, Math.min(RIGHT_MAX_PCT, rightExplorerPct + dpct))
    rightExplorerPct = next
    // Canvas size depends on the viewport's clientWidth (constant here, since
    // the right column has a fixed width) and clientHeight (also constant;
    // only the right column resizes vertically). No camera-aspect bump needed.
  }

  // ----- Explorer accordion state -----
  //
  // Per-section open/closed map. Keys: 'heroes' | 'units' | 'markers' |
  // 'doodads' for top-level, 'd:<category>' for the doodad sub-buckets,
  // 'p:<section>' for Properties sections.
  // Defaults open on first render; user toggles persist for the session.
  let sectionOpen: Record<string, boolean> = {}
  function isOpen(id: string, def: boolean = true): boolean {
    const v = sectionOpen[id]
    return v === undefined ? def : v
  }
  function onSectionToggle(e: CustomEvent<{ id: string; open: boolean }>) {
    sectionOpen = { ...sectionOpen, [e.detail.id]: e.detail.open }
  }

  const SEL_EVENT = 'wc3-forge:selection-changed'
  const MAP_EVENT = 'wc3-forge:map-changed'
  const DIRTY_EVENT = 'wc3-forge:dirty-changed'
  const ENTITY_EVENT = 'wc3-forge:entity-changed'
  const DEV_ANIM_EVENT = 'wc3-forge:dev-set-anim'

  onMount(async () => {
    try {
      try { reforged = await GetReforgedMode() } catch { reforged = false }
      scene = createScene(canvas, reforged)
      scene.setPathingVisible(pathingVisible)
      scene.onPick(handlePick)
      scene.onTerrainPick(handleTerrainPick)
      ;(window as any).__scene = scene
      ;(window as any).__showToast = showToast
    } catch (e) {
      error = 'scene init failed: ' + (e instanceof Error ? (e.stack || e.message) : String(e))
      console.error(e)
    }
    EventsOn(MAP_EVENT, async () => {
      status = await Status()
      if (status.loaded) {
        await reloadMap()
      } else {
        units = []
        doodads = []
        unitTypes = {}
        doodadTypes = {}
        selectedIds = new Set()
        selectedDoodadIds = new Set()
        selectionItems = []
        primaryEntity = null
        primaryDoodad = null
        terrainCell = null
        doodadCategoriesPresent = []
      }
    })
    EventsOn(DEV_ANIM_EVENT, (payload: { creation_number: number; anim_name: string }) => {
      scene?.setUnitAnimation(payload.creation_number, payload.anim_name)
    })
    // Verification harness. Triggered by Go-side --pick-self-test flag (which
    // emits this event ~4s after startup so map load + assets settle). The
    // scene runs a project-then-pick round-trip across every visible instance;
    // results land in wc3-forge.log via flog. WebView2 drops synthetic mouse
    // input so this is the only viable external entrypoint for pick verification.
    EventsOn('wc3-forge:pick-self-test', () => {
      const run = (scene as any)?.__runPickSelfTest
      if (typeof run === 'function') run()
    })
    let startupSpec: { x: number; y: number; z: number; distance: number } | null = null
    EventsOn('wc3-forge:startup-camera', (payload: { spec: string }) => {
      const m = (payload?.spec || '').match(/^(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?)(?:,(-?\d+(?:\.\d+)?))?(?:,(\d+(?:\.\d+)?))?$/)
      if (!m) return
      startupSpec = {
        x: parseFloat(m[1]),
        y: parseFloat(m[2]),
        z: m[3] ? parseFloat(m[3]) : 0,
        distance: m[4] ? parseFloat(m[4]) : 0,
      }
      applyStartupCamera()
    })
    ;(window as any).__applyStartupCamera = applyStartupCamera
    function applyStartupCamera() {
      if (!startupSpec || !scene) return
      scene.panTo(startupSpec.x, startupSpec.y, startupSpec.z)
      if (startupSpec.distance > 0) {
        const camCtrl = (window as any).__camera
        if (camCtrl && typeof camCtrl.setDistance === 'function') {
          camCtrl.setDistance(startupSpec.distance)
        }
      }
    }
    EventsOn(DIRTY_EVENT, (payload: { dirty: boolean }) => {
      dirty = !!payload?.dirty
    })
    EventsOn(ENTITY_EVENT, async (payload: { kind: string; id: number; field: string; position: number[] }) => {
      if (!payload) return
      if (payload.kind === 'unit') {
        if (!primaryEntity || primaryEntity.CreationNumber !== payload.id) return
        try { primaryEntity = await GetUnit(payload.id) } catch { /* ignore */ }
      } else if (payload.kind === 'doodad') {
        if (!primaryDoodad || primaryDoodad.creation_number !== payload.id) return
        const p = payload.position
        if (!p || p.length < 3) return
        primaryDoodad = { ...primaryDoodad, position: [p[0], p[1], p[2]] }
        const idx = doodads.findIndex(d => d.creation_number === payload.id)
        if (idx >= 0) {
          doodads[idx] = { ...doodads[idx], position: [p[0], p[1], p[2]] }
          doodads = doodads
        }
      }
    })
    EventsOn(SEL_EVENT, async (s: main.SelectionDTO) => {
      ingestSelection(s)
      const items = s.items || []
      if (items.length === 0) {
        primaryEntity = null
        primaryDoodad = null
        return
      }
      const idx = Math.max(0, Math.min(s.primary, items.length - 1))
      const primary = items[idx]
      if (primary.kind === 'doodad') {
        primaryEntity = null
        primaryDoodad = doodads.find(d => d.creation_number === primary.id) ?? null
      } else {
        primaryDoodad = null
        try { primaryEntity = await GetUnit(primary.id) } catch { primaryEntity = null }
      }
    })

    const s = await Status()
    status = s
    if (s.loaded) await reloadMap()
    const sel = await GetSelection()
    ingestSelection(sel)
    try { dirty = await IsDirty() } catch { dirty = false }
    window.addEventListener('keydown', onGlobalKeyDown)
    document.addEventListener('mousedown', onDocClickForFileMenu, true)
    document.addEventListener('keydown', onDocKeyForFileMenu)
    // Test-driver hook: receives commands from Go's App.EmitTestCommand so
    // verification automation can drive UI state without needing to simulate
    // clicks (WebView2 can drop synthetic input — see memory). Subscribed
    // to the Wails event and dispatched per the simple text command format.
    EventsOn('wc3-forge:test-command', (payload: { cmd: string }) => {
      const cmd = payload?.cmd || ''
      const [op, ...args] = cmd.trim().split(/\s+/)
      switch (op) {
        case 'terrain.toggle':
          toggleTerrainPickMode()
          break
        case 'terrain.set': {
          // Args: col row. Auto-fills info from the cached terrain DTO via
          // the picker module by simulating a click at that cell's center.
          // Simpler path: just call scene's terrain-pick on a synthetic
          // canvas-pixel center. We don't have direct cell→pixel projection
          // here, so reach into the scene's API. Easiest: skip and let the
          // user click manually; this op is a no-op in practice. For the
          // test driver we just trigger a cell via the picker directly.
          break
        }
        case 'doodad.toggle': {
          const cat = args.join(' ')
          const cur = doodadVisibility[cat]
          const next = cur === false ? true : false
          scene?.setDoodadCategoryVisible(cat, next)
          if (cat === '*') {
            const all: Record<string, boolean> = {}
            for (const c of doodadCategoriesPresent) all[c] = next
            doodadVisibility = all
          } else {
            doodadVisibility = { ...doodadVisibility, [cat]: next }
          }
          break
        }
        case 'section.toggle': {
          const id = args.join(' ')
          sectionOpen = { ...sectionOpen, [id]: !isOpen(id, true) }
          break
        }
        case 'splitter.set': {
          const pct = parseFloat(args[0])
          if (isFinite(pct) && pct > 0 && pct < 100) rightExplorerPct = pct
          break
        }
      }
    })
    // Test-driver hook (also exposed on window for in-page console use):
    ;(window as any).__app = {
      toggleTerrainPick: () => toggleTerrainPickMode(),
      setTerrainCell: (cell: TerrainCellInfo | null) => { terrainCell = cell },
      toggleDoodadCategory: (cat: string, visible: boolean) => {
        scene?.setDoodadCategoryVisible(cat, visible)
        if (cat === '*') {
          const next: Record<string, boolean> = {}
          for (const c of doodadCategoriesPresent) next[c] = visible
          doodadVisibility = next
        } else {
          doodadVisibility = { ...doodadVisibility, [cat]: visible }
        }
      },
      setSectionOpen: (id: string, open: boolean) => {
        sectionOpen = { ...sectionOpen, [id]: open }
      },
      getDoodadCategories: () => doodadCategoriesPresent,
    }
  })

  function onGlobalKeyDown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && (e.key === 's' || e.key === 'S')) {
      e.preventDefault()
      void doSave()
    }
  }

  async function doSave() {
    if (!status.loaded || saving) return
    saving = true
    try {
      await SaveMap()
      try { dirty = await IsDirty() } catch {}
    } catch (e) {
      const msg = String(e)
      if (/MPQ archive writing is not yet implemented/i.test(msg)) {
        showToast(
          'This map was opened from an MPQ archive. Extract it to a folder to enable saving.',
          'error',
        )
      } else {
        showToast('save failed: ' + msg, 'error')
      }
    } finally {
      saving = false
    }
  }

  function ingestSelection(s: main.SelectionDTO) {
    const items = s.items || []
    const u = new Set<number>()
    const d = new Set<number>()
    for (const it of items) {
      if (it.kind === 'doodad') d.add(it.id)
      else u.add(it.id)
    }
    selectedIds = u
    selectedDoodadIds = d
    selectionItems = items
    scene?.setSelected(u, d)
  }

  async function handlePick(hits: PickHit[], mode: SelectMode) {
    const next = composeSelection(selectionItems, hits, mode)
    await SetSelection(next.map(it => ({ kind: it.kind, id: it.id })))
  }

  function handleTerrainPick(cell: TerrainCellInfo | null) {
    // Update the panel state. Selection is NOT cleared — the user can keep
    // an entity selected and inspect cells side-by-side. The Properties panel
    // will display BOTH the entity properties (if any) and the cell info
    // (when set). Null cell = click missed the map; we keep the previous
    // cell info around so the user can still inspect what they last picked
    // until they pick another cell.
    if (cell) {
      terrainCell = cell
      // Mirror the pick to the 3D viewport so the user can SEE which cell
      // the panel data refers to. Persists until replaced or the mode toggles
      // off (see toggleTerrainPickMode).
      scene?.setHighlightedCell({ col: cell.col, row: cell.row })
    }
  }

  function composeSelection(
    current: main.SelectionItemDTO[],
    hits: PickHit[],
    mode: SelectMode,
  ): main.SelectionItemDTO[] {
    const key = (kind: string, id: number) => `${kind}:${id}`
    if (mode === 'set') {
      const seen = new Set<string>()
      const out: main.SelectionItemDTO[] = []
      for (const h of hits) {
        const k = key(h.kind, h.id)
        if (seen.has(k)) continue
        seen.add(k)
        out.push({ kind: h.kind, id: h.id })
      }
      return out
    }
    const map = new Map<string, main.SelectionItemDTO>()
    for (const it of current) map.set(key(it.kind, it.id), { kind: it.kind, id: it.id })
    if (mode === 'add') {
      for (const h of hits) map.set(key(h.kind, h.id), { kind: h.kind, id: h.id })
    } else {
      for (const h of hits) {
        const k = key(h.kind, h.id)
        if (map.has(k)) map.delete(k)
        else map.set(k, { kind: h.kind, id: h.id })
      }
    }
    return [...map.values()]
  }

  onDestroy(() => {
    EventsOff(SEL_EVENT)
    EventsOff(MAP_EVENT)
    EventsOff(DIRTY_EVENT)
    EventsOff(ENTITY_EVENT)
    EventsOff(DEV_ANIM_EVENT)
    window.removeEventListener('keydown', onGlobalKeyDown)
    document.removeEventListener('mousedown', onDocClickForFileMenu, true)
    document.removeEventListener('keydown', onDocKeyForFileMenu)
    scene?.dispose()
  })

  async function pickAndOpen() {
    busy = true
    try {
      const path = await OpenMapDialog()
      if (!path) { busy = false; return }
      status = await OpenMap(path)
      await reloadMap()
    } catch (e) {
      showToast('open failed: ' + String(e), 'error')
    } finally {
      busy = false
    }
  }

  async function reloadMap(opts?: { keepCamera?: boolean }) {
    units = await ListUnits()
    doodads = await ListDoodads()
    try { unitTypes = (await GetUnitTypeIndex()) as unknown as Record<string, UnitTypeInfo> } catch { unitTypes = {} }
    try { doodadTypes = (await GetDoodadTypeIndex()) as unknown as Record<string, DoodadTypeInfo> } catch { doodadTypes = {} }
    await scene?.loadMap(opts)
    // Pull the present-categories list from the scene (populated during
    // placeDoodad). The View menu shows ONLY categories present in the map.
    doodadCategoriesPresent = scene?.getDoodadCategories() ?? []
    // Drop terrain-cell info for a new map — the old (col, row) refers to
    // a map that's no longer loaded. Scene-side highlight is also cleared via
    // loadMap's internal setCell(null) (see scene-instances.ts), but call
    // explicitly so a keep-camera reload also clears the stale wireframe.
    terrainCell = null
    scene?.setHighlightedCell(null)
    const apply = (window as any).__applyStartupCamera
    if (typeof apply === 'function') apply()
  }

  function togglePathing() {
    pathingVisible = !pathingVisible
    scene?.setPathingVisible(pathingVisible)
  }

  function toggleTerrainPickMode() {
    terrainPickModeOn = !terrainPickModeOn
    scene?.setTerrainPickMode(terrainPickModeOn)
    if (!terrainPickModeOn) {
      terrainCell = null
      // Drop the yellow wireframe overlay when leaving terrain mode — the
      // user has switched to doodad mode and the persistent highlight would
      // otherwise sit at a cell they're no longer working with.
      scene?.setHighlightedCell(null)
    }
  }

  function onViewToggle(e: CustomEvent<{ category: string; visible: boolean }>) {
    const { category, visible } = e.detail
    scene?.setDoodadCategoryVisible(category, visible)
    if (category === '*') {
      const next: Record<string, boolean> = {}
      for (const c of doodadCategoriesPresent) next[c] = visible
      doodadVisibility = next
    } else {
      doodadVisibility = { ...doodadVisibility, [category]: visible }
    }
  }

  async function toggleReforged() {
    if (busy) return
    busy = true
    try {
      const next = !reforged
      reforged = await SetReforgedMode(next)
      scene?.setReforgedMode(reforged)
      if (status.loaded) {
        await reloadMap({ keepCamera: true })
      }
    } catch (e) {
      showToast('toggle reforged failed: ' + String(e), 'error')
    } finally {
      busy = false
    }
  }

  async function close() {
    busy = true
    try {
      status = await CloseMap()
      units = []
      doodads = []
      selectedIds = new Set()
      selectedDoodadIds = new Set()
      selectionItems = []
      primaryEntity = null
      primaryDoodad = null
      terrainCell = null
    } finally {
      busy = false
    }
  }

  async function clickRow(e: MouseEvent, kind: 'unit' | 'doodad', id: number) {
    const mode: SelectMode = e.ctrlKey || e.metaKey
      ? 'toggle'
      : (e.shiftKey ? 'add' : 'set')
    const next = composeSelection(selectionItems, [{ kind, id }], mode)
    await SetSelection(next.map(it => ({ kind: it.kind, id: it.id })))
  }

  function panToEntity(e: Event, pos: number[]) {
    e.stopPropagation()
    if (pos && pos.length >= 2) {
      scene?.panTo(pos[0], pos[1])
    }
  }

  // ----- Explorer categorization -----

  type Group = { id: string; label: string; entries: main.UnitDTO[] }
  $: groups = bucket(units, unitTypes)
  function unitDisplayName(u: main.UnitDTO): string {
    const info = unitTypes[u.type_id]
    return info && info.name ? info.name : u.type_id
  }
  function unitCategory(u: main.UnitDTO): string {
    const info = unitTypes[u.type_id]
    return info ? info.category : ''
  }
  function bucket(us: main.UnitDTO[], types: Record<string, UnitTypeInfo>): Group[] {
    const markers: main.UnitDTO[] = []
    const heroes: main.UnitDTO[] = []
    const others: main.UnitDTO[] = []
    for (const u of us) {
      if (u.type_id === 'sloc') { markers.push(u); continue }
      const info = types[u.type_id]
      const isHero = info ? /Hero/i.test(info.category)
        : u.type_id.length > 0 && u.type_id[0] >= 'A' && u.type_id[0] <= 'Z'
      if (isHero) heroes.push(u)
      else others.push(u)
    }
    const out: Group[] = []
    if (heroes.length) out.push({ id: 'heroes', label: 'Heroes', entries: heroes })
    if (others.length) out.push({ id: 'units', label: 'Units & Items', entries: others })
    if (markers.length) out.push({ id: 'markers', label: 'Markers', entries: markers })
    return out
  }

  $: doodadCount = doodads.length

  // ----- Doodad explorer grouping -----
  type DGroup = { id: string; label: string; entries: main.DoodadDTO[] }
  $: doodadGroups = bucketDoodads(doodads, doodadTypes)
  function doodadDisplayName(d: main.DoodadDTO): string {
    const info = doodadTypes[d.type_id]
    return info && info.name ? info.name : d.type_id
  }
  function doodadCategoryFor(d: main.DoodadDTO): string {
    const info = doodadTypes[d.type_id]
    return info ? info.category : ''
  }
  const DOODAD_CAT_ORDER = [
    'Trees/Destructibles',
    'Structures',
    'Props',
    'Bridges/Ramps',
    'Cliff/Terrain',
    'Terrain',
    'Water',
    'Environment',
    'Pathing Blockers',
    'Cinematic',
  ]
  function bucketDoodads(ds: main.DoodadDTO[], types: Record<string, DoodadTypeInfo>): DGroup[] {
    const buckets = new Map<string, main.DoodadDTO[]>()
    for (const d of ds) {
      const info = types[d.type_id]
      const cat = (info && info.category) ? info.category : 'Uncategorized'
      let arr = buckets.get(cat)
      if (!arr) { arr = []; buckets.set(cat, arr) }
      arr.push(d)
    }
    const out: DGroup[] = []
    for (const label of DOODAD_CAT_ORDER) {
      const arr = buckets.get(label)
      if (arr && arr.length) {
        out.push({ id: 'd:' + label, label, entries: arr })
        buckets.delete(label)
      }
    }
    const rest = [...buckets.keys()].sort((a, b) => {
      if (a === 'Uncategorized') return 1
      if (b === 'Uncategorized') return -1
      return a.localeCompare(b)
    })
    for (const label of rest) {
      out.push({ id: 'd:' + label, label, entries: buckets.get(label)! })
    }
    return out
  }

  // ----- Properties helpers -----

  // Asset-preview model path. Resolves the primary selection's MDX path from
  // the SLK type index (unitTypes / doodadTypes). Mirrors the path-picking
  // logic in scene-instances.ts:
  //   - Append .mdx if no extension declared.
  //   - For multi-variant doodads (num_var > 1), use the doodad's variation
  //     index as a suffix (ATtr0.mdx etc.). Single-variant doodads use the
  //     unsuffixed path.
  //   - Returns a PRIMARY path + ordered FALLBACK list. The AssetPreview
  //     walks them in order until one loads — same as placeDoodad's variant
  //     → unsuffixed → other-extension chain in scene-instances.ts. Without
  //     the fallbacks, doodad rows whose SLK declares N variants but whose
  //     CASC ships only the unsuffixed file (e.g. "Statue 1" on Enfo's FFB
  //     → ANst.mdx exists, ANst1.mdx 404s) surface as "load failed".
  // Slocs (type_id === 'sloc') intentionally return null — they're editor-
  // only markers with no model file.
  function mdxPathFor(file: string): string {
    if (/\.(mdl|mdx)$/i.test(file)) return file
    return file + '.mdx'
  }
  interface PreviewPaths {
    primary: string
    fallbacks: string[]
  }
  $: previewPaths = ((): PreviewPaths | null => {
    if (primaryEntity) {
      if (primaryEntity.TypeID === 'sloc') return null
      const info = unitTypes[primaryEntity.TypeID]
      if (!info || !info.file) return null
      const extMatch = info.file.match(/\.(mdl|mdx)$/i)
      const declaredExt = extMatch ? extMatch[0] : '.mdx'
      const stem = extMatch ? info.file.slice(0, -extMatch[0].length) : info.file
      const otherExt = declaredExt.toLowerCase() === '.mdx' ? '.mdl' : '.mdx'
      return {
        primary: mdxPathFor(info.file),
        fallbacks: [stem + otherExt],
      }
    }
    if (primaryDoodad) {
      const info = doodadTypes[primaryDoodad.type_id]
      if (!info || !info.file) return null
      const extMatch = info.file.match(/\.(mdl|mdx)$/i)
      const declaredExt = extMatch ? extMatch[0] : '.mdx'
      const stem = extMatch ? info.file.slice(0, -extMatch[0].length) : info.file
      const otherExt = declaredExt.toLowerCase() === '.mdx' ? '.mdl' : '.mdx'
      let primary: string
      const fallbacks: string[] = []
      if (info.num_var > 1) {
        const variantIdx = Math.min(Math.max(0, primaryDoodad.variation), info.num_var - 1)
        primary = stem + variantIdx + declaredExt
        // unsuffixed with declared extension — covers SLK-declares-N-but-
        // only-the-base-file-shipped cases (Statue 1).
        fallbacks.push(stem + declaredExt)
        // unsuffixed with the OTHER extension — custom maps often declare
        // .mdl but ship .mdx (or vice versa).
        fallbacks.push(stem + otherExt)
        // last resort: variant + other extension
        fallbacks.push(stem + variantIdx + otherExt)
      } else {
        primary = stem + declaredExt
        fallbacks.push(stem + otherExt)
      }
      return { primary, fallbacks }
    }
    return null
  })()
  $: previewModelPath = previewPaths?.primary ?? null
  $: previewModelFallbacks = previewPaths?.fallbacks ?? []

  function fmt(n: number, decimals: number = 0): string {
    return n.toFixed(decimals)
  }
  function fmtVec3(v: number[]): string {
    return `(${fmt(v[0])}, ${fmt(v[1])}, ${fmt(v[2])})`
  }
  function fmtScale(v: number[]): string {
    return `(${fmt(v[0], 2)}, ${fmt(v[1], 2)}, ${fmt(v[2], 2)})`
  }
  function playerLabel(p: number): string {
    const colors = ['Red', 'Blue', 'Teal', 'Purple', 'Yellow', 'Orange', 'Green',
                    'Pink', 'Gray', 'LightBlue', 'DarkGreen', 'Brown']
    if (p === 15) return 'Neutral Passive (15)'
    if (p === 12) return 'Neutral Aggressive (12)'
    if (p < colors.length) return `${colors[p]} (${p})`
    return `Player ${p}`
  }
  function isHero(e: unitsdoo.Entity): boolean {
    return e.HeroLevel > 0 || (e.TypeID.length > 0 && e.TypeID[0] >= 'A' && e.TypeID[0] <= 'Z')
  }

  $: singlePositionEditable = (
    selectionItems.length === 1 &&
    (
      (selectionItems[0].kind === 'unit' && primaryEntity !== null) ||
      (selectionItems[0].kind === 'doodad' && primaryDoodad !== null)
    )
  )

  let posEdit: { x: string; y: string; z: string } = { x: '', y: '', z: '' }
  $: if (singlePositionEditable) {
    const pos = primaryEntity ? primaryEntity.Position : (primaryDoodad ? primaryDoodad.position : null)
    if (pos) {
      posEdit = {
        x: fmt(pos[0]),
        y: fmt(pos[1]),
        z: fmt(pos[2]),
      }
    }
  }

  function primaryPosition(): [number, number, number] | null {
    if (primaryEntity) return [primaryEntity.Position[0], primaryEntity.Position[1], primaryEntity.Position[2]]
    if (primaryDoodad) return [primaryDoodad.position[0], primaryDoodad.position[1], primaryDoodad.position[2]]
    return null
  }

  async function commitPositionEdit(axis: 'x' | 'y' | 'z') {
    const primary = selectionItems[0]
    if (!primary) return
    const cn = primary.id
    const next = {
      x: parseFloat(posEdit.x),
      y: parseFloat(posEdit.y),
      z: parseFloat(posEdit.z),
    }
    if (!Number.isFinite(next[axis])) {
      const truth = primaryPosition()
      if (truth) posEdit = { x: fmt(truth[0]), y: fmt(truth[1]), z: fmt(truth[2]) }
      return
    }
    try {
      if (primary.kind === 'doodad') {
        await MoveDoodad(cn, next.x, next.y, next.z)
      } else {
        await MoveUnit(cn, next.x, next.y, next.z)
      }
    } catch (e) {
      console.error('Move failed:', e)
      showToast('move failed: ' + String(e), 'error')
      const truth = primaryPosition()
      if (truth) posEdit = { x: fmt(truth[0]), y: fmt(truth[1]), z: fmt(truth[2]) }
    }
  }

  function onPosKeydown(e: KeyboardEvent, axis: 'x' | 'y' | 'z') {
    if (e.key === 'Enter') {
      ;(e.currentTarget as HTMLInputElement).blur()
    } else if (e.key === 'Escape') {
      e.stopPropagation()
      const truth = primaryPosition()
      if (truth) {
        const idx = axis === 'x' ? 0 : axis === 'y' ? 1 : 2
        posEdit = { ...posEdit, [axis]: fmt(truth[idx]) }
      }
      ;(e.currentTarget as HTMLInputElement).blur()
    }
  }

  // Terrain-cell info formatting. `rampFlags` is a bitfield: bit 0 = ramp,
  // bit 1 = boundary; rendered as a human-readable list for the Properties
  // panel to surface what's actually set.
  function rampFlagsLabel(rf: number): string {
    if (!rf) return 'none'
    const parts: string[] = []
    if (rf & 0x01) parts.push('ramp')
    if (rf & 0x02) parts.push('boundary')
    return parts.join(', ') || `0x${rf.toString(16)}`
  }
  function shadowLabel(s: number): string {
    if (s < 0) return '(no shadow map)'
    return s >= 0x80 ? `${s} (shadowed)` : `${s} (lit)`
  }
</script>

<main>
  <header>
    <div class="file-menu" bind:this={fileMenuEl}>
      <button class="file-btn"
              class:open={fileMenuOpen}
              on:click={toggleFileMenu}
              aria-haspopup="menu"
              aria-expanded={fileMenuOpen}
              title="File menu">
        File <span class="caret">▾</span>
      </button>
      {#if fileMenuOpen}
        <div class="file-dropdown" role="menu">
          <button class="file-item"
                  role="menuitem"
                  on:click={runMenuAction(pickAndOpen)}
                  disabled={busy}>
            <span class="file-item-label">Open Map…</span>
          </button>
          <button class="file-item"
                  role="menuitem"
                  on:click={runMenuAction(doSave)}
                  disabled={!status.loaded || !dirty || saving}
                  class:dirty
                  title="Save pending edits (Ctrl+S).">
            <span class="file-item-label">Save{dirty ? ' •' : ''}</span>
            <span class="file-item-shortcut">Ctrl+S</span>
          </button>
          <div class="file-sep" role="separator"></div>
          <button class="file-item"
                  role="menuitem"
                  on:click={runMenuAction(close)}
                  disabled={!status.loaded || busy}>
            <span class="file-item-label">Close</span>
          </button>
        </div>
      {/if}
    </div>
    <ViewMenu categories={doodadCategoriesPresent}
              visibility={doodadVisibility}
              on:toggle={onViewToggle} />
    <div class="status-strip">
      {#if status.loaded}
        <span class="map-name">{status.name || '(untitled)'}</span>
        <span class="sep">·</span>
        <span class="map-count">{status.unit_count} entities</span>
      {/if}
    </div>
    <div class="actions">
      <button on:click={toggleTerrainPickMode}
              class="mode-state"
              class:terrain={terrainPickModeOn}
              class:doodad={!terrainPickModeOn}
              title={terrainPickModeOn
                ? 'Terrain mode active — clicking a cell shows its data. Click to switch to Doodad mode.'
                : 'Doodad mode active — clicking selects entities. Click to switch to Terrain mode.'}>
        {terrainPickModeOn ? 'Terrain Mode' : 'Doodad Mode'}
      </button>
      <button on:click={togglePathing}
              class="mode-toggle"
              class:on={pathingVisible}
              title="Toggle the static pathing-map overlay (red=unwalkable, blue=unflyable, yellow=unbuildable).">
        Pathing{pathingVisible ? ' ✓' : ''}
      </button>
      <button on:click={toggleReforged} disabled={busy}
              class="mode-toggle"
              class:on={reforged}
              title="Toggle Reforged graphics. Reloads the current map without resetting the camera.">
        Reforged Graphics{reforged ? ' ✓' : ''}
      </button>
    </div>
  </header>

  {#if error}<div class="error"><pre>{error}</pre></div>{/if}

  <!-- 2-column layout: viewport (left/center, big) + right column (Explorer
       stacked above Properties with vertical splitter between).
       Default: 65/35 vertical split between viewport and right column. -->
  <div class="split">
    <section class="viewport">
      <canvas bind:this={canvas}></canvas>
    </section>

    <div class="right-col" bind:this={rightColEl}>
      <aside class="panel explorer"
             style="flex: 0 0 {rightExplorerPct}%;">
        <header class="panel-header">Explorer</header>
        <div class="panel-body">
          {#if !status.loaded}
            <div class="empty">No map loaded.</div>
          {:else}
            <!-- Each top-level Explorer section is an Accordion. Doodads
                 nests sub-Accordions per category. Default-open for all
                 sections on first render; user toggles persist for session. -->
            {#each groups as g (g.id)}
              <Accordion id={g.id} label={g.label} open={isOpen(g.id, true)}
                         on:toggle={onSectionToggle}>
                <span slot="header-extras">{g.entries.length}</span>
                <ul class="explorer-list">
                  {#each g.entries as u (u.creation_number)}
                    <li class:selected={selectedIds.has(u.creation_number)}
                        on:click={(e) => clickRow(e, 'unit', u.creation_number)}
                        title="{u.type_id} #{u.creation_number}">
                      <span class="name">{unitDisplayName(u)}</span>
                      <span class="cat dim">{unitCategory(u)}</span>
                      <button class="pan-btn"
                              on:click={(e) => panToEntity(e, u.position)}
                              title="Pan camera to this entity">⊕</button>
                    </li>
                  {/each}
                </ul>
              </Accordion>
            {/each}
            {#if doodadCount > 0}
              <Accordion id="doodads" label="Doodads" open={isOpen('doodads', true)}
                         on:toggle={onSectionToggle}>
                <span slot="header-extras">{doodadCount}</span>
                <div class="doodad-subs">
                  {#each doodadGroups as dg (dg.id)}
                    <Accordion id={dg.id} label={dg.label} open={isOpen(dg.id, true)}
                               on:toggle={onSectionToggle}>
                      <span slot="header-extras">{dg.entries.length}</span>
                      <ul class="explorer-list">
                        {#each dg.entries as d (d.creation_number)}
                          <li class:selected={selectedDoodadIds.has(d.creation_number)}
                              on:click={(e) => clickRow(e, 'doodad', d.creation_number)}
                              title="{d.type_id} #{d.creation_number}">
                            <span class="name">{doodadDisplayName(d)}</span>
                            <span class="cat dim">{doodadCategoryFor(d)}</span>
                            <button class="pan-btn"
                                    on:click={(e) => panToEntity(e, d.position)}
                                    title="Pan camera to this doodad">⊕</button>
                          </li>
                        {/each}
                      </ul>
                    </Accordion>
                  {/each}
                </div>
              </Accordion>
            {/if}
          {/if}
        </div>
      </aside>

      <Splitter onDrag={onRightSplitterDrag} />

      <aside class="panel properties">
        <header class="panel-header">Properties</header>
        <div class="panel-body">
        {#if previewModelPath}
          <Accordion id="p:preview" label="Preview" open={isOpen('p:preview', true)}
                     on:toggle={onSectionToggle}>
            <AssetPreview modelPath={previewModelPath}
                          modelPathFallbacks={previewModelFallbacks}
                          {reforged}
                          teamColor={primaryEntity ? primaryEntity.Player : 0} />
          </Accordion>
        {/if}
        {#if terrainCell}
          <!-- Terrain-cell info is its own Accordion section so it survives
               an entity selection (user can keep a unit selected and inspect
               cells alongside). Default open whenever a cell exists. -->
          <Accordion id="p:cell" label="Terrain Cell" open={isOpen('p:cell', true)}
                     on:toggle={onSectionToggle}>
            <dl class="props">
              <dt>Cell</dt>                <dd class="mono">({terrainCell.col}, {terrainCell.row})</dd>
              <dt>World XY</dt>            <dd class="mono">({fmt(terrainCell.worldX)}, {fmt(terrainCell.worldY)})</dd>
              <dt>Palette #</dt>           <dd class="mono">{terrainCell.paletteIdx}</dd>
              <dt>Palette FourCC</dt>      <dd class="mono">{terrainCell.paletteFourCC || '(none)'}</dd>
              <dt>Texture</dt>             <dd class="mono small">{terrainCell.paletteTexture || '(none)'}</dd>
              <dt>Corner palettes</dt>     <dd class="mono">BL={terrainCell.cornerPalettes[0]} BR={terrainCell.cornerPalettes[1]} TL={terrainCell.cornerPalettes[2]} TR={terrainCell.cornerPalettes[3]}</dd>
              <dt>Layer height</dt>        <dd class="mono">{terrainCell.layerHeight}</dd>
              <dt>Cliff tex #</dt>         <dd class="mono">{terrainCell.cliffTexIdx}</dd>
              <dt>Cliff FourCC</dt>        <dd class="mono">{terrainCell.cliffFourCC || '(none)'}</dd>
              <dt>Cliff var</dt>           <dd class="mono">{terrainCell.cliffVar}</dd>
              <dt>Ground var</dt>          <dd class="mono">{terrainCell.groundVar}</dd>
              <dt>Ramp flags</dt>          <dd>{rampFlagsLabel(terrainCell.rampFlags)}</dd>
              <dt>Has water</dt>           <dd>{terrainCell.hasWater ? `yes (Z=${fmt(terrainCell.waterZ)})` : 'no'}</dd>
              <dt>Cell skip</dt>           <dd>{terrainCell.cellSkip ? 'yes (cliff covers)' : 'no'}</dd>
              <dt>Shadow byte</dt>         <dd class="mono">{shadowLabel(terrainCell.shadow)}</dd>
            </dl>
          </Accordion>
        {/if}
        {#if primaryDoodad}
          {@const d = primaryDoodad}
          <Accordion id="p:identity" label="Identity" open={isOpen('p:identity', true)}
                     on:toggle={onSectionToggle}>
            <dl class="props">
              <dt>Kind</dt>               <dd>Doodad</dd>
              <dt>Type ID</dt>            <dd class="mono">{d.type_id}</dd>
              {#if d.skin_id && d.skin_id !== d.type_id}
                <dt>Skin ID</dt>          <dd class="mono">{d.skin_id}</dd>
              {/if}
              <dt>Creation #</dt>         <dd class="mono">{d.creation_number}</dd>
              <dt>Name</dt>               <dd>{doodadDisplayName(d)}</dd>
              <dt>Category</dt>           <dd>{doodadCategoryFor(d)}</dd>
            </dl>
          </Accordion>
          <Accordion id="p:transform" label="Transform" open={isOpen('p:transform', true)}
                     on:toggle={onSectionToggle}>
            <dl class="props">
              {#if singlePositionEditable}
                <dt>Position</dt>
                <dd class="mono pos-edit">
                  <input type="number" step="1" bind:value={posEdit.x}
                         on:blur={() => commitPositionEdit('x')}
                         on:keydown={(e) => onPosKeydown(e, 'x')}
                         title="X (game coords). Enter to commit, Esc to revert." />
                  <input type="number" step="1" bind:value={posEdit.y}
                         on:blur={() => commitPositionEdit('y')}
                         on:keydown={(e) => onPosKeydown(e, 'y')}
                         title="Y (game coords). Enter to commit, Esc to revert." />
                  <input type="number" step="1" bind:value={posEdit.z}
                         on:blur={() => commitPositionEdit('z')}
                         on:keydown={(e) => onPosKeydown(e, 'z')}
                         title="Z (game coords). Enter to commit, Esc to revert." />
                </dd>
              {:else}
                <dt>Position</dt>         <dd class="mono">{fmtVec3(d.position)}</dd>
              {/if}
              <dt>Rotation</dt>           <dd class="mono">{fmt(d.rotation, 2)}</dd>
              <dt>Scale</dt>              <dd class="mono">{fmtScale(d.scale)}</dd>
              <dt>Variation</dt>          <dd>{d.variation}</dd>
            </dl>
          </Accordion>
          {#if d.life !== 0xFF}
            <Accordion id="p:destructible" label="Destructible" open={isOpen('p:destructible', true)}
                       on:toggle={onSectionToggle}>
              <dl class="props">
                <dt>Life %</dt>           <dd>{d.life}%</dd>
              </dl>
            </Accordion>
          {/if}
        {:else if !primaryEntity}
          {#if !terrainCell}
            <div class="empty">
              {#if selectedIds.size === 0 && selectedDoodadIds.size === 0}
                Select an entity to see its properties.
              {:else}
                Loading…
              {/if}
            </div>
          {/if}
        {:else}
          {@const e = primaryEntity}
          <Accordion id="p:identity" label="Identity" open={isOpen('p:identity', true)}
                     on:toggle={onSectionToggle}>
            <dl class="props">
              <dt>Type ID</dt>            <dd class="mono">{e.TypeID}</dd>
              {#if e.SkinID && e.SkinID !== e.TypeID}
                <dt>Skin ID</dt>          <dd class="mono">{e.SkinID}</dd>
              {/if}
              <dt>Creation #</dt>         <dd class="mono">{e.CreationNumber}</dd>
              <dt>Player</dt>             <dd>{playerLabel(e.Player)}</dd>
            </dl>
          </Accordion>
          <Accordion id="p:transform" label="Transform" open={isOpen('p:transform', true)}
                     on:toggle={onSectionToggle}>
            <dl class="props">
              {#if singlePositionEditable}
                <dt>Position</dt>
                <dd class="mono pos-edit">
                  <input type="number" step="1" bind:value={posEdit.x}
                         on:blur={() => commitPositionEdit('x')}
                         on:keydown={(e) => onPosKeydown(e, 'x')}
                         title="X (game coords). Enter to commit, Esc to revert." />
                  <input type="number" step="1" bind:value={posEdit.y}
                         on:blur={() => commitPositionEdit('y')}
                         on:keydown={(e) => onPosKeydown(e, 'y')}
                         title="Y (game coords). Enter to commit, Esc to revert." />
                  <input type="number" step="1" bind:value={posEdit.z}
                         on:blur={() => commitPositionEdit('z')}
                         on:keydown={(e) => onPosKeydown(e, 'z')}
                         title="Z (game coords). Enter to commit, Esc to revert." />
                </dd>
              {:else}
                <dt>Position</dt>         <dd class="mono">{fmtVec3(e.Position)}</dd>
              {/if}
              <dt>Rotation</dt>           <dd class="mono">{fmt(e.Rotation, 2)}</dd>
              <dt>Scale</dt>              <dd class="mono">{fmtScale(e.Scale)}</dd>
              <dt>Variation</dt>          <dd>{e.Variation}</dd>
            </dl>
          </Accordion>
          <Accordion id="p:status" label="Status" open={isOpen('p:status', true)}
                     on:toggle={onSectionToggle}>
            <dl class="props">
              <dt>HP %</dt>               <dd>{e.HitPointsPct < 0 ? 'default' : e.HitPointsPct + '%'}</dd>
              <dt>Mana %</dt>             <dd>{e.ManaPct < 0 ? 'default' : e.ManaPct + '%'}</dd>
              {#if e.GoldAmount > 0}
                <dt>Gold</dt>             <dd>{e.GoldAmount}</dd>
              {/if}
              {#if e.TargetAcquisition !== 0}
                <dt>Acquisition</dt>      <dd class="mono">{fmt(e.TargetAcquisition, 1)}</dd>
              {/if}
            </dl>
          </Accordion>
          {#if isHero(e)}
            <Accordion id="p:hero" label="Hero" open={isOpen('p:hero', true)}
                       on:toggle={onSectionToggle}>
              <dl class="props">
                <dt>Level</dt>            <dd>{e.HeroLevel || 1}</dd>
                {#if e.HeroStr > 0 || e.HeroAgi > 0 || e.HeroInt > 0}
                  <dt>Stats</dt>          <dd class="mono">STR {e.HeroStr} · AGI {e.HeroAgi} · INT {e.HeroInt}</dd>
                {/if}
              </dl>
            </Accordion>
          {/if}
          {#if e.Inventory && e.Inventory.length > 0}
            <Accordion id="p:inventory" label="Inventory" open={isOpen('p:inventory', true)}
                       on:toggle={onSectionToggle}>
              <dl class="props">
                {#each e.Inventory as slot}
                  <dt>Slot {slot.Slot}</dt><dd class="mono">{slot.ItemID}</dd>
                {/each}
              </dl>
            </Accordion>
          {/if}
          {#if e.ItemDrops && e.ItemDrops.length > 0}
            <Accordion id="p:drops" label="Item Drops" open={isOpen('p:drops', true)}
                       on:toggle={onSectionToggle}>
              <dl class="props">
                {#each e.ItemDrops as drop}
                  <dt class="mono">{drop.ItemID}</dt><dd>{drop.Chance}%</dd>
                {/each}
              </dl>
            </Accordion>
          {/if}
          {#if e.AbilityModifications && e.AbilityModifications.length > 0}
            <Accordion id="p:abilities" label="Abilities" open={isOpen('p:abilities', true)}
                       on:toggle={onSectionToggle}>
              <dl class="props">
                {#each e.AbilityModifications as ab}
                  <dt class="mono">{ab.AbilityID}</dt>
                  <dd>lvl {ab.Level}{ab.Autocast ? ' · autocast' : ''}</dd>
                {/each}
              </dl>
            </Accordion>
          {/if}
        {/if}
        </div>
      </aside>
    </div>
  </div>

  <Toast />
</main>

<style>
  :global(body) {
    margin: 0;
    background: #121214;
    color: #d4d4d8;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    font-size: 13px;
    overflow: hidden;
  }
  :global(html), :global(body), main { height: 100vh; }
  main { display: flex; flex-direction: column; }

  header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 18px;
    border-bottom: 1px solid #2a2a30;
    background: #18181b;
    flex: 0 0 auto;
  }
  .status-strip { flex: 1 1 auto; color: #a1a1aa; font-size: 12px; }
  .map-name { color: #e4e4e7; font-weight: 500; }
  .map-count { color: #71717a; }
  .sep { color: #52525b; margin: 0 8px; }
  .actions { display: flex; gap: 6px; align-items: center; }

  button {
    background: #2563eb; color: white; border: 0; padding: 5px 12px;
    font-size: 12px; border-radius: 4px; cursor: pointer;
  }
  button:hover:not(:disabled) { background: #1d4ed8; }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  button.mode-toggle {
    background: #3f3f46; font-weight: 500;
  }
  button.mode-toggle:hover:not(:disabled) { background: #52525b; }
  button.mode-toggle.on { background: #15803d; }
  button.mode-toggle.on:hover:not(:disabled) { background: #166534; }
  /* 2-state mode selector: always displays the CURRENT mode (Doodad blue, or
     Terrain green). Click toggles. Visual style mirrors the other header
     buttons — same padding, font, border radius — but the background carries
     a stable color cue so the user can tell at a glance which mode is on
     without having to read the label. */
  button.mode-state { font-weight: 500; }
  button.mode-state.doodad { background: #2563eb; }
  button.mode-state.doodad:hover:not(:disabled) { background: #1d4ed8; }
  button.mode-state.terrain { background: #16a34a; }
  button.mode-state.terrain:hover:not(:disabled) { background: #15803d; }

  .file-menu { position: relative; }
  button.file-btn {
    background: transparent; color: #d4d4d8; font-weight: 500;
    padding: 5px 10px; border: 1px solid transparent; border-radius: 3px;
    display: inline-flex; align-items: center; gap: 4px;
  }
  button.file-btn:hover:not(:disabled),
  button.file-btn.open { background: #27272a; }
  .file-btn .caret { color: #71717a; font-size: 10px; }
  .file-dropdown {
    position: absolute; top: calc(100% + 4px); left: 0;
    min-width: 220px; z-index: 100;
    background: #18181b; border: 1px solid #27272a;
    box-shadow: 0 8px 24px rgba(0,0,0,0.4);
    display: flex; flex-direction: column; padding: 4px 0;
  }
  button.file-item {
    background: transparent; color: #e4e4e7;
    border: 0; border-radius: 0;
    padding: 6px 14px; font-size: 12px; font-weight: 400;
    text-align: left; cursor: pointer;
    display: flex; align-items: center; justify-content: space-between;
    gap: 16px;
  }
  button.file-item:hover:not(:disabled) { background: #27272a; color: #fff; }
  button.file-item:disabled { color: #52525b; cursor: not-allowed; opacity: 1; }
  button.file-item.dirty .file-item-label { color: #fbbf24; }
  .file-item-shortcut { color: #71717a; font-size: 11px; font-family: 'Cascadia Mono', Consolas, monospace; }
  button.file-item:disabled .file-item-shortcut { color: #3f3f46; }
  .file-sep { height: 1px; background: #27272a; margin: 4px 0; }

  .error { background: #7f1d1d; color: #fecaca; padding: 6px 14px; font-family: 'Cascadia Mono', Consolas, monospace; font-size: 12px; flex: 0 0 auto; max-height: 200px; overflow: auto; }
  .error pre { margin: 0; white-space: pre-wrap; word-break: break-all; }

  /* 2-column layout: viewport claims left/center, right column claims right.
     The right column is fixed-width (340px); viewport gets all remaining
     horizontal space — significantly more than the prior 3-column model. */
  .split { flex: 1 1 auto; display: grid; grid-template-columns: 1fr 340px; min-height: 0; }
  .viewport { position: relative; min-width: 0; min-height: 0; border-right: 1px solid #2a2a30; }
  canvas { display: block; width: 100%; height: 100%; }

  /* Right column: Explorer (top) + Splitter + Properties (bottom).
     Splitter drags update rightExplorerPct so the Explorer's flex-basis
     adjusts; Properties absorbs the rest via flex: 1 1 auto. */
  .right-col {
    display: flex; flex-direction: column; min-height: 0;
    background: #161618;
  }
  .panel { background: #161618; display: flex; flex-direction: column; min-height: 0; overflow: hidden; }
  .panel.explorer {
    border-bottom: 1px solid #2a2a30;
  }
  .panel.properties { flex: 1 1 auto; }
  .panel-header {
    padding: 8px 14px; font-size: 10px; font-weight: 600; color: #a1a1aa;
    text-transform: uppercase; letter-spacing: 0.08em;
    border-bottom: 1px solid #27272a; background: #1c1c1f;
    flex: 0 0 auto;
  }
  .panel-body {
    flex: 1 1 auto; min-height: 0; overflow-y: auto;
    display: flex; flex-direction: column;
  }
  .empty { padding: 30px 16px; text-align: center; color: #71717a; font-size: 12px; }

  /* Explorer rows (inside Accordion bodies) */
  .explorer-list { list-style: none; margin: 0; padding: 0; }
  .explorer-list li {
    display: flex; align-items: center; gap: 8px;
    padding: 4px 14px; cursor: pointer; font-size: 12px;
    min-width: 0;
  }
  .explorer-list li:hover { background: #1f1f23; }
  .explorer-list li.selected { background: #1e3a8a; color: #e4e4e7; }
  .explorer-list li .name {
    color: #e4e4e7; font-weight: 500; flex: 0 1 auto; min-width: 0;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .explorer-list li .cat {
    font-size: 10.5px; flex: 0 1 auto; min-width: 0;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    text-align: left;
  }
  .explorer-list li .pan-btn {
    flex: 0 0 auto; margin-left: auto; display: none;
    background: transparent; color: #a1a1aa;
    border: 1px solid #3f3f46; border-radius: 3px;
    padding: 1px 6px; font-size: 11px; line-height: 1;
    cursor: pointer;
  }
  .explorer-list li:hover .pan-btn { display: inline-flex; }
  .explorer-list li .pan-btn:hover { background: #3f3f46; color: #e4e4e7; }
  .dim { color: #71717a; }

  /* Doodad sub-accordions are nested inside the Doodads accordion body.
     Slightly indent their accordion headers so the hierarchy reads. */
  .doodad-subs :global(.acc-header) {
    padding-left: 24px;
  }

  /* Properties */
  .props {
    display: grid; grid-template-columns: max-content 1fr;
    gap: 4px 12px; padding: 10px 16px; margin: 0; font-size: 12px;
  }
  .props dt {
    color: #71717a; font-size: 11px; padding-top: 2px;
    text-align: left; justify-self: start;
  }
  .props dd { margin: 0; color: #e4e4e7; }
  .mono { font-family: 'Cascadia Mono', Consolas, monospace; }
  .mono.small { font-size: 10.5px; word-break: break-all; }

  .pos-edit { display: flex; gap: 4px; }
  .pos-edit input {
    width: 64px; padding: 2px 4px;
    background: #18181b; color: #e4e4e7;
    border: 1px solid #3f3f46; border-radius: 3px;
    font-family: 'Cascadia Mono', Consolas, monospace; font-size: 12px;
  }
  .pos-edit input:focus {
    outline: none; border-color: #2563eb;
  }
  .pos-edit input::-webkit-inner-spin-button,
  .pos-edit input::-webkit-outer-spin-button {
    -webkit-appearance: none; margin: 0;
  }
</style>
