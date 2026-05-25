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
  import { createScene, type SceneAPI, type PickHit, type SelectMode } from './scene-instances'
  import Toast from './Toast.svelte'
  import Splitter from './Splitter.svelte'
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
  // toggle) go through showToast() so they auto-dismiss. The rule:
  //   - state error that should stick until reload → `error` band
  //   - transient operational failure → showToast(..., 'error')
  let error: string = ''
  let busy: boolean = false
  let reforged: boolean = false
  let pathingVisible: boolean = false

  let canvas: HTMLCanvasElement
  let scene: SceneAPI | null = null
  let dirty: boolean = false
  let saving: boolean = false

  // ----- File menu (header dropdown) -----
  //
  // Click File button → toggles open. Click outside / Escape → closes. Open
  // Map / Save / Close items live inside; the previous header buttons for
  // these were moved here so the header reads as "File-menu + view-toggles"
  // rather than a mixed action+toggle strip.
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
  // Menu-item adapter: closes the menu THEN runs the action so the dropdown
  // animation/visibility doesn't linger over a modal (Open Map's file dialog,
  // notably).
  function runMenuAction(fn: () => unknown) {
    return () => {
      fileMenuOpen = false
      void fn()
    }
  }

  // ----- Explorer section sizes -----
  //
  // The Explorer is split into stacked sections (Heroes / Units & Items /
  // Markers / Doodads) separated by drag handles. Sizes are pixel-based and
  // tracked per section id. The LAST visible section uses flex: 1 1 auto so
  // it absorbs leftover space + window-resize delta — that keeps the splitter
  // ratios sensible across window resize without strict pixel math.
  //
  // Defaults chosen so that on a typical map (a few heroes, lots of units &
  // doodads) Doodads gets the lion's share by being the trailing flex-fill
  // section. Heroes/Units/Markers start small so a Doodad-heavy map shows
  // doodads prominently right after open.
  //
  // Sizes survive map reload but reset on page reload (not persisted). If we
  // ever want persistence, drop them into localStorage on dragEnd.
  type SectionId = 'heroes' | 'units' | 'markers' | 'doodads'
  const DEFAULT_SECTION_SIZE: Record<SectionId, number> = {
    heroes: 140,
    units: 180,
    markers: 120,
    doodads: 260, // last section ends up flex:1 1 auto regardless, but default is the seed if it's not last
  }
  let sectionSizes: Record<SectionId, number> = { ...DEFAULT_SECTION_SIZE }
  const MIN_SECTION_HEIGHT = 56 // header (~28) + ~28 of content visible

  // Returns the ordered list of visible major sections. Used by the Explorer
  // markup so non-present sections are skipped entirely and splitters only
  // appear between sections that actually render.
  function visibleSections(): SectionId[] {
    const out: SectionId[] = []
    if (groups.some(g => g.id === 'heroes')) out.push('heroes')
    if (groups.some(g => g.id === 'units')) out.push('units')
    if (groups.some(g => g.id === 'markers')) out.push('markers')
    if (doodadCount > 0) out.push('doodads')
    return out
  }
  // Reactive recompute when units/doodads change (open new map, etc.). The
  // explicit dep references (groups, doodadCount) make Svelte's compiler pick
  // them up — visibleSections() reads from them but the static analyzer
  // doesn't follow through function calls. Without this line, opening a new
  // map would leave the previous map's section list rendered.
  let visSections: SectionId[] = []
  $: {
    void groups
    void doodadCount
    visSections = visibleSections()
  }

  // Splitter drag: id = the section ABOVE the splitter being dragged. Adjusts
  // that section's pixel size by dy; clamps to MIN_SECTION_HEIGHT. The
  // section BELOW (which may be flex:1:1:auto or pixel-sized) absorbs the
  // delta implicitly via the flexbox model.
  function onSplitterDrag(id: SectionId, dy: number) {
    const next = Math.max(MIN_SECTION_HEIGHT, sectionSizes[id] + dy)
    if (next === sectionSizes[id]) return
    sectionSizes = { ...sectionSizes, [id]: next }
  }

  const SEL_EVENT = 'wc3-forge:selection-changed'
  const MAP_EVENT = 'wc3-forge:map-changed'
  const DIRTY_EVENT = 'wc3-forge:dirty-changed'
  const ENTITY_EVENT = 'wc3-forge:entity-changed'
  const DEV_ANIM_EVENT = 'wc3-forge:dev-set-anim'

  onMount(async () => {
    try {
      // Pull initial mode from Go so reloads of the UI (HMR, route) honor
      // any persistent setting once we add one. Currently session-only.
      try { reforged = await GetReforgedMode() } catch { reforged = false }
      scene = createScene(canvas, reforged)
      scene.setPathingVisible(pathingVisible)
      scene.onPick(handlePick)
      // Devtools / screenshot-automation hook: lets external test drivers
      // pump scene-level operations without needing keyboard simulation.
      ;(window as any).__scene = scene
      // Same idea for toasts — handy when manually verifying severity styles
      // or stack behavior. Cheap to leave in; it's just a function ref.
      ;(window as any).__showToast = showToast
    } catch (e) {
      error = 'scene init failed: ' + (e instanceof Error ? (e.stack || e.message) : String(e))
      console.error(e)
    }
    // Map changes from any source (App method, MCP bridge, --open flag).
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
      }
    })
    // Dev-only animation poke from App.SetUnitAnimation (devtools / MCP).
    // Payload: { creation_number: number, anim_name: string }
    EventsOn(DEV_ANIM_EVENT, (payload: { creation_number: number; anim_name: string }) => {
      scene?.setUnitAnimation(payload.creation_number, payload.anim_name)
    })
    // Startup --camera spec from main.go. Applied (potentially repeatedly)
    // after every loadMap so a fresh map-open lands on the spec'd position
    // rather than the auto-frame. Lets verification automation pin the
    // camera to a known feature on startup without synthetic mouse input
    // (which WebView2 drops). Payload: { spec: "x,y[,z[,distance]]" }.
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
      // Apply immediately if a map's already loaded by the time we receive.
      applyStartupCamera()
    })
    // Hooked into reloadMap below — re-apply if a fresh map opened.
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
    // Dirty-state changes (MoveUnit edits, Save flushes). Keeps the header
    // Save pill's modified-dot indicator reactive without polling. The OS
    // window title's "* " prefix is updated Go-side via runtime.WindowSetTitle
    // in App.startup's OnDirtyChanged handler — the inner WebView2 child's
    // document.title isn't visible to the user.
    EventsOn(DIRTY_EVENT, (payload: { dirty: boolean }) => {
      dirty = !!payload?.dirty
    })
    // Entity-changed: a mutation landed on the Go side (MoveUnit from the UI
    // OR the MCP bridge OR any future code path). If the changed entity is
    // the one we're currently displaying in the Properties panel, re-fetch
    // it so the inputs reflect new truth. The scene's own subscription
    // (scene-instances.ts) handles the 3D-model repaint independently —
    // both refreshes are driven by this same Go-side event.
    //
    // Field-aware: today only "position" fires, but the handler intentionally
    // re-fetches the whole entity rather than patching one field so future
    // Field values (rotation/type/etc.) work without changes here.
    EventsOn(ENTITY_EVENT, async (payload: { kind: string; id: number; field: string; position: number[] }) => {
      if (!payload) return
      // Refresh Properties when the changed entity matches our primary.
      // Branches by kind because units + doodads share the creation-number
      // space (per-kind ids in WC3 — a unit and a doodad can have the same
      // numeric id, so kind disambiguates them).
      if (payload.kind === 'unit') {
        if (!primaryEntity || primaryEntity.CreationNumber !== payload.id) return
        try { primaryEntity = await GetUnit(payload.id) } catch { /* ignore — entity may have been removed */ }
        // posEdit re-syncs automatically via the reactive seed block below
        // (primaryEntity reassignment triggers it).
      } else if (payload.kind === 'doodad') {
        if (!primaryDoodad || primaryDoodad.creation_number !== payload.id) return
        // Patch the in-memory DoodadDTO position from the event payload (no
        // GetDoodad round-trip — the payload IS the new truth). Also patch
        // the parent `doodads` array's entry so the Explorer pan-to button
        // (which reads from `doodads`, not `primaryDoodad`) sees the new
        // location for subsequent pans.
        const p = payload.position
        if (!p || p.length < 3) return
        primaryDoodad = { ...primaryDoodad, position: [p[0], p[1], p[2]] }
        const idx = doodads.findIndex(d => d.creation_number === payload.id)
        if (idx >= 0) {
          doodads[idx] = { ...doodads[idx], position: [p[0], p[1], p[2]] }
          doodads = doodads // trigger Svelte reactivity on the array
        }
      }
    })
    EventsOn(SEL_EVENT, async (s: main.SelectionDTO) => {
      ingestSelection(s)
      // Primary entity fetch — kind-aware so doodad primaries don't crash
      // through GetUnit and stay perma-"Loading…".
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
    // File-menu dismiss: click-outside (capture so an in-bubble preventDefault
    // can't swallow it) + Escape (non-capture, lets local Esc handlers run
    // first if anything claims it).
    document.addEventListener('mousedown', onDocClickForFileMenu, true)
    document.addEventListener('keydown', onDocKeyForFileMenu)
  })

  // Global Ctrl+S / Cmd+S → save. preventDefault() stops the browser's
  // "save page as…" dialog. The scene-instances Escape handler is keyed on
  // 'Escape' only so this doesn't collide. We intentionally leave inputs
  // alone (Ctrl+S in an X/Y/Z input still saves — there's no reason an
  // input would want to swallow Ctrl+S).
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
      // dirty event will arrive; refresh defensively in case nothing was dirty.
      try { dirty = await IsDirty() } catch {}
    } catch (e) {
      // MPQ-write rejection is the expected hot path until we ship MPQ writing.
      // Transient operational error → toast (auto-dismisses), NOT the
      // persistent error band.
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

  // Mirror Go-side selection into the local split sets + the scene-side tint.
  // The scene's tint maps are keyed by kind, so we must hand it units and
  // doodads separately — see scene-instances.setSelected.
  function ingestSelection(s: main.SelectionDTO) {
    const items = s.items || []
    const u = new Set<number>()
    const d = new Set<number>()
    for (const it of items) {
      if (it.kind === 'doodad') d.add(it.id)
      else u.add(it.id) // 'unit' | 'item' | any future kind that lives in units.doo
    }
    selectedIds = u
    selectedDoodadIds = d
    selectionItems = items
    scene?.setSelected(u, d)
  }

  // Picker entry point. The scene fires hits + a combine-mode based on
  // modifier keys (set/add/toggle); we compose against the current Go-side
  // selection and push the result back through App.SetSelection. The Go
  // side owns canonical selection state, so we always round-trip through it
  // rather than mutating local state and hoping the event catches up.
  async function handlePick(hits: PickHit[], mode: SelectMode) {
    const next = composeSelection(selectionItems, hits, mode)
    await SetSelection(next.map(it => ({ kind: it.kind, id: it.id })))
  }

  // Pure composition: takes the current selection + new hits + mode, returns
  // the new selection array. Items are de-duplicated by (kind, id).
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
    } else { // toggle
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
      // Transient: bad path, parse failure on one map shouldn't permanently
      // wedge the UI.
      showToast('open failed: ' + String(e), 'error')
    } finally {
      busy = false
    }
  }

  async function reloadMap(opts?: { keepCamera?: boolean }) {
    units = await ListUnits()
    doodads = await ListDoodads()
    // Per-map indexes — overlay (w3u/w3d/w3b/w3t) changes per map.
    try { unitTypes = (await GetUnitTypeIndex()) as unknown as Record<string, UnitTypeInfo> } catch { unitTypes = {} }
    try { doodadTypes = (await GetDoodadTypeIndex()) as unknown as Record<string, DoodadTypeInfo> } catch { doodadTypes = {} }
    // The viewport pulls its own data via App.* methods now; no need to
    // marshal the raw .w3x bytes across the boundary.
    await scene?.loadMap(opts)
    // After every load, re-apply the --camera spec (if one was passed at
    // startup). Without this, the auto-frame in scene.loadMap() would put
    // us at the default overview pose, blowing away the verification
    // automation's intended viewpoint.
    const apply = (window as any).__applyStartupCamera
    if (typeof apply === 'function') apply()
  }

  function togglePathing() {
    pathingVisible = !pathingVisible
    scene?.setPathingVisible(pathingVisible)
  }

  async function toggleReforged() {
    if (busy) return
    busy = true
    try {
      const next = !reforged
      // Push to Go first — it owns the canonical state (CASC prefix order,
      // asset-handler sibling preference), then flip the scene which will
      // drop cached models/textures.
      reforged = await SetReforgedMode(next)
      scene?.setReforgedMode(reforged)
      // Re-load the current map so all models + team-color textures come
      // back through the new mode, BUT keep the camera where the user
      // panned it — re-framing on an in-place reload throws them back to
      // the default view they just left.
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
      // No clean "unload map" in mdx-m3-viewer; we'd recreate the viewer
      // on close. For now the canvas just keeps the last-loaded map until
      // a new one is opened.
    } finally {
      busy = false
    }
  }

  // Row click → kind-aware single-select via Go. Mirrors plain-click in the
  // viewport (mode='set'). Modifier-held row clicks fall through to the same
  // composer so shift/ctrl in the Explorer behaves like in the viewport.
  async function clickRow(e: MouseEvent, kind: 'unit' | 'doodad', id: number) {
    const mode: SelectMode = e.ctrlKey || e.metaKey
      ? 'toggle'
      : (e.shiftKey ? 'add' : 'set')
    const next = composeSelection(selectionItems, [{ kind, id }], mode)
    await SetSelection(next.map(it => ({ kind: it.kind, id: it.id })))
  }

  function panToEntity(e: Event, pos: number[]) {
    e.stopPropagation()  // don't trigger row selection
    if (pos && pos.length >= 2) {
      scene?.panTo(pos[0], pos[1])
    }
  }

  // ----- Explorer categorization -----
  //
  // Categorization uses the SLK-derived type index when available:
  //   "sloc" → Markers
  //   info.category contains "Hero" → Heroes
  //   everything else → Units & Items
  //
  // Display name + category come from unitTypes[type_id]. Falls back to the
  // FourCC when the row is unknown (custom map types with empty Name fields,
  // or pre-Reforged retired types) so the entity is still listable.

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

  // Doodad count for Explorer header.
  $: doodadCount = doodads.length

  // ----- Doodad explorer grouping -----
  //
  // Doodads bucket by their SLK `category` column, resolved via the WESTRING
  // table (typeindex.go: doodadCategoryKeys → "Trees/Destructibles",
  // "Structures", "Cliff/Terrain", etc.). Unknown / empty categories collapse
  // into a single "Uncategorized" group so derived custom types without a
  // category override still surface in the list.
  //
  // Groups are emitted in a stable curated order — the rest of the buckets
  // come after, alphabetized, so a map that introduces a novel category via
  // custom doodads still shows up predictably.
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
    // Curated order first.
    for (const label of DOODAD_CAT_ORDER) {
      const arr = buckets.get(label)
      if (arr && arr.length) {
        out.push({ id: 'd:' + label, label, entries: arr })
        buckets.delete(label)
      }
    }
    // Remaining categories alphabetized; "Uncategorized" pinned last.
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

  // Single entity selected with an editable Position? Drives the X/Y/Z inputs
  // in the Properties panel. Today both unit AND doodad qualify; multi-select
  // and sloc selection fall back to read-only static text. The branch in
  // commitPositionEdit dispatches MoveUnit vs MoveDoodad based on which
  // primary is non-null.
  $: singlePositionEditable = (
    selectionItems.length === 1 &&
    (
      (selectionItems[0].kind === 'unit' && primaryEntity !== null) ||
      (selectionItems[0].kind === 'doodad' && primaryDoodad !== null)
    )
  )

  // Local edit buffers so the inputs don't reset on every keystroke as the
  // bound entity object refreshes from selection events. We commit on Enter
  // or blur, then refresh from Go so on-disk-truth wins on any failed write.
  let posEdit: { x: string; y: string; z: string } = { x: '', y: '', z: '' }
  $: if (singlePositionEditable) {
    // Seed buffers when the primary changes (new selection or refresh).
    // Reads from whichever primary is populated — unit's PascalCase Position
    // or doodad's snake_case position; both are [3]float32 in game coords.
    const pos = primaryEntity ? primaryEntity.Position : (primaryDoodad ? primaryDoodad.position : null)
    if (pos) {
      posEdit = {
        x: fmt(pos[0]),
        y: fmt(pos[1]),
        z: fmt(pos[2]),
      }
    }
  }

  // Reads current truth position from whichever primary is populated. Returns
  // null when neither is set (caller bails). Centralized so the commit/revert
  // paths don't duplicate the kind-branch.
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
    // If the user typed garbage, revert that field to truth and bail before
    // issuing the move (don't move the entity to NaN).
    if (!Number.isFinite(next[axis])) {
      const truth = primaryPosition()
      if (truth) posEdit = { x: fmt(truth[0]), y: fmt(truth[1]), z: fmt(truth[2]) }
      return
    }
    try {
      // Kind-branch on the primary selection's kind: unit → MoveUnit (writes
      // war3mapUnits.doo), doodad → MoveDoodad (writes war3map.doo). The
      // Go-side OnEntityChanged event then drives both the scene repaint
      // and the Properties re-fetch — no explicit refresh needed here.
      if (primary.kind === 'doodad') {
        await MoveDoodad(cn, next.x, next.y, next.z)
      } else {
        await MoveUnit(cn, next.x, next.y, next.z)
      }
    } catch (e) {
      // Move failure → snap the buffer back to truth.
      console.error('Move failed:', e)
      showToast('move failed: ' + String(e), 'error')
      const truth = primaryPosition()
      if (truth) posEdit = { x: fmt(truth[0]), y: fmt(truth[1]), z: fmt(truth[2]) }
    }
  }

  function onPosKeydown(e: KeyboardEvent, axis: 'x' | 'y' | 'z') {
    if (e.key === 'Enter') {
      ;(e.currentTarget as HTMLInputElement).blur() // commit via blur path
    } else if (e.key === 'Escape') {
      // Revert just this field to truth and blur (don't propagate so the
      // viewport doesn't also clear selection).
      e.stopPropagation()
      const truth = primaryPosition()
      if (truth) {
        const idx = axis === 'x' ? 0 : axis === 'y' ? 1 : 2
        posEdit = { ...posEdit, [axis]: fmt(truth[idx]) }
      }
      ;(e.currentTarget as HTMLInputElement).blur()
    }
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
    <div class="status-strip">
      {#if status.loaded}
        <span class="map-name">{status.name || '(untitled)'}</span>
        <span class="sep">·</span>
        <span class="map-count">{status.unit_count} entities</span>
      {/if}
    </div>
    <div class="actions">
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

  <div class="split">
    <aside class="panel explorer">
      <header class="panel-header">Explorer</header>
      {#if !status.loaded}
        <div class="empty">No map loaded.</div>
      {:else}
        <div class="explorer-sections">
          {#each visSections as sid, i (sid)}
            {@const g = groups.find(x => x.id === sid)}
            {@const isLast = i === visSections.length - 1}
            {#if sid === 'doodads'}
              <div class="section"
                   class:section-last={isLast}
                   style={isLast ? '' : `flex: 0 0 ${sectionSizes.doodads}px;`}>
                <header class="cat-header section-header">
                  Doodads <span class="count">{doodadCount}</span>
                </header>
                <div class="section-body">
                  {#each doodadGroups as dg (dg.id)}
                    <div class="subcategory">
                      <header class="cat-header sub">{dg.label} <span class="count">{dg.entries.length}</span></header>
                      <ul>
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
                    </div>
                  {/each}
                </div>
              </div>
            {:else if g}
              <div class="section"
                   class:section-last={isLast}
                   style={isLast ? '' : `flex: 0 0 ${sectionSizes[sid]}px;`}>
                <header class="cat-header section-header">
                  {g.label} <span class="count">{g.entries.length}</span>
                </header>
                <div class="section-body">
                  <ul>
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
                </div>
              </div>
            {/if}
            {#if !isLast}
              <Splitter onDrag={(dy) => onSplitterDrag(sid, dy)} />
            {/if}
          {/each}
        </div>
      {/if}
    </aside>

    <section class="viewport">
      <canvas bind:this={canvas}></canvas>
    </section>

    <aside class="panel properties">
      <header class="panel-header">Properties</header>
      <div class="properties-body">
      {#if primaryDoodad}
        {@const d = primaryDoodad}
        <dl class="props">
          <dt>Kind</dt>               <dd>Doodad</dd>
          <dt>Type ID</dt>            <dd class="mono">{d.type_id}</dd>
          {#if d.skin_id && d.skin_id !== d.type_id}
            <dt>Skin ID</dt>          <dd class="mono">{d.skin_id}</dd>
          {/if}
          <dt>Creation #</dt>         <dd class="mono">{d.creation_number}</dd>
          <dt>Name</dt>               <dd>{doodadDisplayName(d)}</dd>
          <dt>Category</dt>           <dd>{doodadCategoryFor(d)}</dd>

          <dt class="section">Transform</dt>
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

          {#if d.life !== 0xFF}
            <dt class="section">Destructible</dt>
            <dt>Life %</dt>           <dd>{d.life}%</dd>
          {/if}
        </dl>
      {:else if !primaryEntity}
        <div class="empty">
          {#if selectedIds.size === 0 && selectedDoodadIds.size === 0}
            Select an entity to see its properties.
          {:else}
            Loading…
          {/if}
        </div>
      {:else}
        {@const e = primaryEntity}
        <dl class="props">
          <dt>Type ID</dt>            <dd class="mono">{e.TypeID}</dd>
          {#if e.SkinID && e.SkinID !== e.TypeID}
            <dt>Skin ID</dt>          <dd class="mono">{e.SkinID}</dd>
          {/if}
          <dt>Creation #</dt>         <dd class="mono">{e.CreationNumber}</dd>
          <dt>Player</dt>             <dd>{playerLabel(e.Player)}</dd>

          <dt class="section">Transform</dt>
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

          <dt class="section">Status</dt>
          <dt>HP %</dt>               <dd>{e.HitPointsPct < 0 ? 'default' : e.HitPointsPct + '%'}</dd>
          <dt>Mana %</dt>             <dd>{e.ManaPct < 0 ? 'default' : e.ManaPct + '%'}</dd>
          {#if e.GoldAmount > 0}
            <dt>Gold</dt>             <dd>{e.GoldAmount}</dd>
          {/if}
          {#if e.TargetAcquisition !== 0}
            <dt>Acquisition</dt>      <dd class="mono">{fmt(e.TargetAcquisition, 1)}</dd>
          {/if}

          {#if isHero(e)}
            <dt class="section">Hero</dt>
            <dt>Level</dt>            <dd>{e.HeroLevel || 1}</dd>
            {#if e.HeroStr > 0 || e.HeroAgi > 0 || e.HeroInt > 0}
              <dt>Stats</dt>          <dd class="mono">STR {e.HeroStr} · AGI {e.HeroAgi} · INT {e.HeroInt}</dd>
            {/if}
          {/if}

          {#if e.Inventory && e.Inventory.length > 0}
            <dt class="section">Inventory</dt>
            {#each e.Inventory as slot}
              <dt>Slot {slot.Slot}</dt><dd class="mono">{slot.ItemID}</dd>
            {/each}
          {/if}

          {#if e.ItemDrops && e.ItemDrops.length > 0}
            <dt class="section">Item Drops</dt>
            {#each e.ItemDrops as drop}
              <dt class="mono">{drop.ItemID}</dt><dd>{drop.Chance}%</dd>
            {/each}
          {/if}

          {#if e.AbilityModifications && e.AbilityModifications.length > 0}
            <dt class="section">Abilities</dt>
            {#each e.AbilityModifications as ab}
              <dt class="mono">{ab.AbilityID}</dt>
              <dd>lvl {ab.Level}{ab.Autocast ? ' · autocast' : ''}</dd>
            {/each}
          {/if}
        </dl>
      {/if}
      </div>
    </aside>
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
    gap: 16px;
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

  /* File menu dropdown — anchored under the File button, opens on click,
     closes on click-outside or Escape. Styled to match the header chrome
     (dark background + thin border, no rounded corners on the dropdown so it
     reads as part of the header bar rather than a floating popup). */
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

  .split { flex: 1 1 auto; display: grid; grid-template-columns: 260px 1fr 340px; min-height: 0; }
  .panel { background: #161618; display: flex; flex-direction: column; min-height: 0; overflow: hidden; }
  .explorer { border-right: 1px solid #2a2a30; }
  .properties { border-left: 1px solid #2a2a30; }
  .panel-header {
    padding: 8px 14px; font-size: 10px; font-weight: 600; color: #a1a1aa;
    text-transform: uppercase; letter-spacing: 0.08em;
    border-bottom: 1px solid #27272a; background: #1c1c1f;
    flex: 0 0 auto;
  }
  .empty { padding: 30px 16px; text-align: center; color: #71717a; font-size: 12px; }
  .viewport { position: relative; min-width: 0; min-height: 0; }
  canvas { display: block; width: 100%; height: 100%; }

  /* Explorer — stacked resizable sections.
     Layout model:
       .explorer (flex column, fills panel height)
         .panel-header                                 // "EXPLORER" bar, fixed
         .explorer-sections (flex column, fills rest)  // owns the stack
           .section (flex 0 0 Npx OR 1 1 auto last)    // per-section box
             .section-header                          // sticky title bar
             .section-body (overflow-y: auto)         // scrolls within section
           Splitter (4px tall drag-handle)
           .section …                                  // next section
     The trailing section uses flex:1:1:auto so window resize + leftover space
     land there sensibly. Earlier sections have explicit pixel heights driven
     by `sectionSizes`; the splitter above the section below adjusts the
     section ABOVE the handle. */
  .explorer-sections {
    flex: 1 1 auto; min-height: 0;
    display: flex; flex-direction: column;
    /* Whole-panel scroll fallback: if window-shrink squeezes all sections
       below their min-heights, the stack overflows here. Per-section scroll
       (inside .section-body below) handles the common case; this is the
       safety net. */
    overflow-y: auto;
  }
  .section {
    display: flex; flex-direction: column;
    min-height: 56px; overflow: hidden;
    background: #161618;
  }
  /* Last section absorbs leftover space + window-resize delta so splitter
     ratios survive resizes without strict pixel math. flex-basis: 0 forces
     it to start from 0 and grow into the remaining flex space; otherwise
     `flex: 1 1 auto` keeps it at its intrinsic content height even when
     other sections are pixel-sized, which is wrong here. */
  .section.section-last { flex: 1 1 0; min-height: 120px; }
  .section-header {
    flex: 0 0 auto;
    background: #1c1c1f; border-bottom: 1px solid #27272a;
    padding: 6px 14px;
    font-size: 10px; font-weight: 600; color: #a1a1aa;
    text-transform: uppercase; letter-spacing: 0.06em;
  }
  .section-header .count { color: #71717a; font-weight: 400; }
  .section-body { flex: 1 1 auto; min-height: 0; overflow-y: auto; padding: 4px 0; }

  /* Sub-buckets only appear inside the Doodads section. Same row chrome as
     top-level entries (rows are .explorer-section ul li below); just a
     slightly-dimmed indented header. */
  .subcategory { padding: 2px 0 4px; }
  .cat-header {
    padding: 4px 14px; font-size: 11px; font-weight: 600; color: #d4d4d8;
    display: flex; justify-content: space-between; align-items: center;
  }
  .cat-header .count { color: #71717a; font-weight: 400; font-size: 11px; }
  .cat-header.sub {
    padding-left: 22px; color: #a1a1aa; font-weight: 500; font-size: 10.5px;
    text-transform: uppercase; letter-spacing: 0.04em;
  }

  .explorer ul { list-style: none; margin: 0; padding: 0; }
  .explorer li {
    display: flex; align-items: center; gap: 8px;
    padding: 4px 14px; cursor: pointer; font-size: 12px;
    min-width: 0;
  }
  .explorer li:hover { background: #1f1f23; }
  .explorer li.selected { background: #1e3a8a; color: #e4e4e7; }
  /* Name + category sit as a tight pair on the left of the row. The name
     doesn't flex-grow — letting it claim the full width would push the
     category to the row's far edge and break the visual pairing. The pan
     button uses margin-left:auto to claim the remaining space. */
  .explorer li .name {
    color: #e4e4e7; font-weight: 500; flex: 0 1 auto; min-width: 0;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .explorer li .cat {
    font-size: 10.5px; flex: 0 1 auto; min-width: 0;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    text-align: left;
  }
  .explorer li .pan-btn {
    flex: 0 0 auto; margin-left: auto; display: none;
    background: transparent; color: #a1a1aa;
    border: 1px solid #3f3f46; border-radius: 3px;
    padding: 1px 6px; font-size: 11px; line-height: 1;
    cursor: pointer;
  }
  .explorer li:hover .pan-btn { display: inline-flex; }
  .explorer li .pan-btn:hover { background: #3f3f46; color: #e4e4e7; }

  /* Properties — panel-header stays sticky at the top; only the body scrolls
     when the selected entity's fields overflow (Heroes with abilities +
     inventory + item drops + 7 base fields can run long). */
  .properties-body { flex: 1 1 auto; min-height: 0; overflow-y: auto; }
  .props {
    display: grid; grid-template-columns: max-content 1fr;
    gap: 4px 12px; padding: 10px 16px; margin: 0; font-size: 12px;
  }
  .props dt {
    color: #71717a; font-size: 11px; padding-top: 2px;
    text-align: left; justify-self: start;
  }
  /* Section labels wear the same dark bar treatment as .panel-header so they
     read as subsections rather than loose dividers. The bar must reach the
     panel's outer edges (matching .panel-header). Negative margins alone
     don't suffice: grid items with `justify-self: stretch` (the default)
     constrain their margin box to the grid area, so negative inline margins
     visually shift the box without widening it. The fix: pin `box-sizing:
     border-box`, set explicit width = grid-area + total side-padding, and
     pull left by the same amount so the bar extends symmetrically into the
     panel's gutter. .panel.properties has no horizontal padding, so 16px
     each side (= .props padding) reaches the panel's outer edge. */
  .props dt.section {
    grid-column: 1 / -1;
    box-sizing: border-box;
    width: calc(100% + 32px);
    margin: 10px -16px 0; padding: 6px 16px;
    color: #a1a1aa; font-weight: 600; font-size: 9.5px;
    text-transform: uppercase; letter-spacing: 0.08em;
    background: #1c1c1f; border-bottom: 1px solid #27272a;
  }
  .props dd { margin: 0; color: #e4e4e7; }
  .mono { font-family: 'Cascadia Mono', Consolas, monospace; }
  .dim { color: #71717a; }

  /* Position-edit row: 3 narrow numeric inputs, tight gap, monospace for
     alignment with the rest of the Properties panel. */
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
  /* Strip the spinner arrows — they crowd the value at this width and most
     editing happens via type-and-Enter anyway. */
  .pos-edit input::-webkit-inner-spin-button,
  .pos-edit input::-webkit-outer-spin-button {
    -webkit-appearance: none; margin: 0;
  }
</style>
