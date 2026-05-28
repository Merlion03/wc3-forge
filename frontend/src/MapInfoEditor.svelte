<script lang="ts">
  // Map Info Editor — six-tab modal dialog for editing war3map.w3i.
  //
  // Wire contract: every field flows through MapInfoApply with the wire keys
  // documented in internal/forge/handlers.go::ApplyInfoUpdates. Only changed
  // fields are sent (diff against the baseline snapshot taken at open).
  //
  // Tab parity with HiveWE's MapInfoEditor — Description, Loading Screen,
  // Options, Lighting & Weather (we split out Sky from HiveWE's Options bucket
  // since wc3-forge live-previews sky picks), Size & Camera Bounds, Advanced.
  //
  // Modal chrome (backdrop, Esc-to-close, click-outside, focus trap) is
  // delegated to shadcn-svelte's Dialog (bits-ui under the hood).
  //
  // TRIGSTR handling: Option A (literal strings). Strings in the Info struct
  // are pre-resolved post-Session.Open; on save they're written back verbatim.
  // See encode.go's top-of-file note.

  import { tick } from 'svelte'
  import {
    MapInfoGet,
    MapInfoApply,
    ListSkyModels,
    SetSkyModel,
    GetTerrain,
    HistoryUndoCount,
    DiscardTo,
    ListWeathers,
    ListSoundEnvironments,
    ListTilesetOptions,
    ListCampaignLoadingScreens,
    ListImportedLoadingScreens,
  } from '../wailsjs/go/main/App.js'
  import { showToast } from './toast'
  import * as Dialog from '$lib/components/ui/dialog'
  import * as Tabs from '$lib/components/ui/tabs'
  import { Input } from '$lib/components/ui/input'
  import { Textarea } from '$lib/components/ui/textarea'
  import { Label } from '$lib/components/ui/label'
  import { Button } from '$lib/components/ui/button'
  import { Checkbox } from '$lib/components/ui/checkbox'

  type TabId =
    | 'description'
    | 'loading'
    | 'options'
    | 'lighting'
    | 'size'
    | 'advanced'
  interface Tab { id: TabId; label: string }
  const TABS: Tab[] = [
    { id: 'description', label: 'Description' },
    { id: 'loading', label: 'Loading Screen' },
    { id: 'options', label: 'Options' },
    { id: 'lighting', label: 'Lighting & Weather' },
    { id: 'size', label: 'Size & Camera Bounds' },
    { id: 'advanced', label: 'Advanced' },
  ]

  let {
    open = $bindable(false),
    onClose,
    onSkyChanged,
  }: {
    open?: boolean
    onClose?: () => void
    /** Fires when the sky picker changes value. Receives the resolved path
     *  (normalized "environment/sky/.../....mdx", or "" for None). The
     *  parent uses this to rebuild the scene's sky renderer immediately so
     *  the user sees their pick without round-tripping a save. */
    onSkyChanged?: (path: string) => void
  } = $props()

  let activeTab: string = $state('description')

  // ----- Pending edits ------------------------------------------------------
  //
  // The dialog buffers ALL edits in `pending` and diffs against `baseline`
  // on Apply. Each field maps 1:1 to a wire key in ApplyInfoUpdates.

  type Color = { r: number; g: number; b: number; a: number }
  type IVec4 = { left: number; right: number; bottom: number; top: number }

  interface Pending {
    // Description
    name: string
    author: string
    description: string
    suggestedPlayers: string
    // Loading Screen — mode + per-mode fields
    loadingMode: 'default' | 'campaign' | 'imported'
    loadingScreenNumber: number   // campaign index when mode==campaign, else -1
    loadingScreenModel: string    // imported relative path when mode==imported, else ""
    loadingScreenText: string
    loadingScreenTitle: string
    loadingScreenSubtitle: string
    // Options
    meleeMap: boolean
    hideMinimapPreview: boolean
    maskedAreaPartiallyVisible: boolean
    cliffShoreWaves: boolean
    rollingShoreWaves: boolean
    itemClassification: boolean
    waterTinting: boolean
    waterColor: Color
    fogEnabled: boolean
    fogStyle: number    // 0..3 (linear/exponential/exp2/user) — preserved when fogEnabled flips
    fogStartZ: number
    fogEndZ: number
    fogDensity: number
    fogColor: Color
    // Lighting & Weather
    weatherID: string         // FourCC, "" for none
    customSoundEnv: string    // "" for default
    customLightTileset: string // single char, "" for default
    // Size & Camera Bounds
    cameraComplements: IVec4
    // Advanced
    lua: boolean
    gameDataSet: number
    supportedModes: number
    gameDataVersion: number
    camDistanceDefault: number
    camDistanceMax: number
    camDistanceMin: number
    forceDefaultZoom: boolean
    forceMaxZoom: boolean
    forceMinZoom: boolean
  }
  let pending: Pending = $state(blankPending())
  let baseline: Pending = $state(blankPending())

  // Readonly metadata pulled at open. Not edited.
  let mapVersion: number = $state(0)
  let editorVersion: number = $state(0)
  let fileVersion: number = $state(0)
  let gameVersion: number[] = $state([0, 0, 0, 0])

  // Map size in cells (from terrain). Used by the Size tab read-out + by the
  // playable-area derivation. Pulled at open from GetTerrain.
  let mapCellsX: number = $state(0)
  let mapCellsY: number = $state(0)

  // Validation state. Per-field error message; empty string = valid.
  let nameError: string = $state('')
  let descError: string = $state('')
  let camBoundsError: string = $state('')
  let hasErrors = $derived(!!nameError || !!descError || !!camBoundsError)

  const NAME_MAX = 80
  const DESC_MAX = 512

  let loading: boolean = $state(false)
  let applying: boolean = $state(false)

  let firstFocusableEl: HTMLInputElement | null = $state(null)

  let showMetadata: boolean = $state(false)

  // ----- Picker option caches ----------------------------------------------
  // Populated once per open via loadOptionLists(); reused across tabs.
  interface SkyOpt { path: string; name_key: string; display_name: string }
  interface LabeledOption { key: string; label: string }
  interface LoadingScreenOption { index: number; label: string }
  let skyOptions: SkyOpt[] = $state([])
  let weatherOptions: LabeledOption[] = $state([])
  let soundEnvOptions: LabeledOption[] = $state([])
  let tilesetOptions: LabeledOption[] = $state([])
  let campaignLoadingScreens: LoadingScreenOption[] = $state([])
  let importedLoadingScreens: string[] = $state([])

  // Sky picker state — sky's special because saving it doesn't go through
  // MapInfoApply (it's a script rewrite committed by SetSkyModel on Save).
  let skySelected: string = $state('')
  let skyBaseline: string = $state('')
  let skyChanged = $derived(skySelected !== skyBaseline)
  // Undo-stack depth captured at dialog open. Cancel calls DiscardTo(this) to
  // revert every sky pick made during the dialog session in one RPC.
  let skyHistoryAnchor: number = $state(0)

  // Live-changed: pending differs from baseline (any field). Declared after
  // skySelected/skyBaseline so the $derived expression can reference them.
  let isChanged = $derived(
    !pendingEquals(pending, baseline) || skySelected !== skyBaseline,
  )

  function blankPending(): Pending {
    return {
      name: '', author: '', description: '', suggestedPlayers: '',
      loadingMode: 'default',
      loadingScreenNumber: -1,
      loadingScreenModel: '',
      loadingScreenText: '', loadingScreenTitle: '', loadingScreenSubtitle: '',
      meleeMap: false, hideMinimapPreview: false,
      maskedAreaPartiallyVisible: false,
      cliffShoreWaves: false, rollingShoreWaves: false,
      itemClassification: false,
      waterTinting: false,
      waterColor: { r: 80, g: 120, b: 255, a: 255 },
      fogEnabled: false, fogStyle: 0,
      fogStartZ: 3000, fogEndZ: 5000, fogDensity: 0.5,
      fogColor: { r: 80, g: 80, b: 80, a: 255 },
      weatherID: '', customSoundEnv: '', customLightTileset: '',
      cameraComplements: { left: 6, right: 6, bottom: 4, top: 8 },
      lua: false,
      gameDataSet: 0,
      supportedModes: 0,
      gameDataVersion: 0,
      camDistanceDefault: 0, camDistanceMax: 0, camDistanceMin: 0,
      forceDefaultZoom: false, forceMaxZoom: false, forceMinZoom: false,
    }
  }

  function pendingEquals(a: Pending, b: Pending): boolean {
    return (
      a.name === b.name &&
      a.author === b.author &&
      a.description === b.description &&
      a.suggestedPlayers === b.suggestedPlayers &&
      a.loadingMode === b.loadingMode &&
      a.loadingScreenNumber === b.loadingScreenNumber &&
      a.loadingScreenModel === b.loadingScreenModel &&
      a.loadingScreenText === b.loadingScreenText &&
      a.loadingScreenTitle === b.loadingScreenTitle &&
      a.loadingScreenSubtitle === b.loadingScreenSubtitle &&
      a.meleeMap === b.meleeMap &&
      a.hideMinimapPreview === b.hideMinimapPreview &&
      a.maskedAreaPartiallyVisible === b.maskedAreaPartiallyVisible &&
      a.cliffShoreWaves === b.cliffShoreWaves &&
      a.rollingShoreWaves === b.rollingShoreWaves &&
      a.itemClassification === b.itemClassification &&
      a.waterTinting === b.waterTinting &&
      colorEquals(a.waterColor, b.waterColor) &&
      a.fogEnabled === b.fogEnabled &&
      a.fogStyle === b.fogStyle &&
      a.fogStartZ === b.fogStartZ &&
      a.fogEndZ === b.fogEndZ &&
      a.fogDensity === b.fogDensity &&
      colorEquals(a.fogColor, b.fogColor) &&
      a.weatherID === b.weatherID &&
      a.customSoundEnv === b.customSoundEnv &&
      a.customLightTileset === b.customLightTileset &&
      ivec4Equals(a.cameraComplements, b.cameraComplements) &&
      a.lua === b.lua &&
      a.gameDataSet === b.gameDataSet &&
      a.supportedModes === b.supportedModes &&
      a.gameDataVersion === b.gameDataVersion &&
      a.camDistanceDefault === b.camDistanceDefault &&
      a.camDistanceMax === b.camDistanceMax &&
      a.camDistanceMin === b.camDistanceMin &&
      a.forceDefaultZoom === b.forceDefaultZoom &&
      a.forceMaxZoom === b.forceMaxZoom &&
      a.forceMinZoom === b.forceMinZoom
    )
  }
  function colorEquals(a: Color, b: Color): boolean {
    return a.r === b.r && a.g === b.g && a.b === b.b && a.a === b.a
  }
  function ivec4Equals(a: IVec4, b: IVec4): boolean {
    return a.left === b.left && a.right === b.right && a.bottom === b.bottom && a.top === b.top
  }

  async function loadOptionLists() {
    try {
      const [sky, weather, sound, tile, camp, imp] = await Promise.all([
        ListSkyModels(),
        ListWeathers(),
        ListSoundEnvironments(),
        ListTilesetOptions(),
        ListCampaignLoadingScreens(),
        ListImportedLoadingScreens(),
      ])
      skyOptions = (sky ?? []) as SkyOpt[]
      weatherOptions = (weather ?? []) as LabeledOption[]
      soundEnvOptions = (sound ?? []) as LabeledOption[]
      tilesetOptions = (tile ?? []) as LabeledOption[]
      campaignLoadingScreens = (camp ?? []) as LoadingScreenOption[]
      importedLoadingScreens = (imp ?? []) as string[]
    } catch (e) {
      showToast('Could not load Map Info options: ' + String(e), 'error')
    }
  }

  async function loadSkyState() {
    try {
      const [t, anchor] = await Promise.all([GetTerrain(), HistoryUndoCount()])
      const terrain = t as any
      skySelected = terrain?.sky_model ?? ''
      skyBaseline = skySelected
      skyHistoryAnchor = anchor
      // Cell counts: TerrainDTO.width/height are vertex counts, so cells =
      // width-1 × height-1.
      mapCellsX = Math.max(0, (terrain?.width ?? 1) - 1)
      mapCellsY = Math.max(0, (terrain?.height ?? 1) - 1)
    } catch (e) {
      showToast('Could not load sky state: ' + String(e), 'error')
    }
  }

  async function onSkyPicked(path: string) {
    skySelected = path
    try {
      const resolved = await SetSkyModel(path)
      onSkyChanged?.(resolved)
    } catch (e) {
      showToast('Failed to set sky: ' + String(e), 'error')
    }
  }

  // ----- Loading Info into pending -----------------------------------------

  async function loadInfo() {
    loading = true
    try {
      const info = await MapInfoGet()
      pending = infoToPending(info)
      baseline = clonePending(pending)
      mapVersion = info?.MapVersion ?? 0
      editorVersion = info?.EditorVersion ?? 0
      fileVersion = info?.FileVersion ?? 0
      gameVersion = info?.GameVersion ?? [0, 0, 0, 0]
      validateAll()
    } catch (e) {
      showToast('Could not load map info: ' + String(e), 'error')
      open = false
    } finally {
      loading = false
    }
  }

  function infoToPending(info: any): Pending {
    const flags = info?.Flags ?? {}
    const wc = info?.WaterColor ?? { R: 80, G: 120, B: 255, A: 255 }
    const fg = info?.Fog ?? {}
    const fc = fg?.Color ?? { R: 80, G: 80, B: 80, A: 255 }
    const cc = info?.CameraComplements ?? { A: 6, B: 6, C: 4, D: 8 }
    const cd = info?.CamDistance ?? {}
    const ls = info?.LoadingScreen ?? {}
    const lsModel: string = ls?.Model ?? ''
    const lsNumber: number = typeof ls?.Number === 'number' ? ls.Number : -1
    let mode: 'default' | 'campaign' | 'imported' = 'default'
    if (lsNumber >= 0) mode = 'campaign'
    else if (lsModel.length > 0) mode = 'imported'
    return {
      name: info?.Name ?? '',
      author: info?.Author ?? '',
      description: info?.Description ?? '',
      suggestedPlayers: info?.SuggestedPlayers ?? '',
      loadingMode: mode,
      loadingScreenNumber: lsNumber,
      loadingScreenModel: lsModel,
      loadingScreenText: ls?.Text ?? '',
      loadingScreenTitle: ls?.Title ?? '',
      loadingScreenSubtitle: ls?.Subtitle ?? '',
      meleeMap: !!flags.MeleeMap,
      hideMinimapPreview: !!flags.HideMinimapPreview,
      maskedAreaPartiallyVisible: !!flags.MaskedAreaPartiallyVisible,
      cliffShoreWaves: !!flags.CliffShoreWaves,
      rollingShoreWaves: !!flags.RollingShoreWaves,
      itemClassification: !!flags.ItemClassification,
      waterTinting: !!flags.WaterTinting,
      waterColor: colorFromGo(wc),
      fogEnabled: (fg?.Style ?? 0) !== 0,
      // Preserve style across enable/disable so re-enabling restores the
      // user's last pick. HiveWE's UI does the same.
      fogStyle: Math.max(0, fg?.Style ?? 0),
      fogStartZ: fg?.StartZ ?? 0,
      fogEndZ: fg?.EndZ ?? 0,
      fogDensity: fg?.Density ?? 0,
      fogColor: colorFromGo(fc),
      weatherID: int32ToFourCC(info?.WeatherID ?? 0),
      customSoundEnv: info?.CustomSoundEnv ?? '',
      customLightTileset: byteToChar(info?.CustomLightTileset ?? 0),
      cameraComplements: {
        left: cc?.A ?? 0,
        right: cc?.B ?? 0,
        bottom: cc?.C ?? 0,
        top: cc?.D ?? 0,
      },
      lua: !!info?.Lua,
      gameDataSet: info?.GameDataSet ?? 0,
      supportedModes: info?.SupportedModes ?? 0,
      gameDataVersion: info?.GameDataVersion ?? 0,
      camDistanceDefault: cd?.Default ?? 0,
      camDistanceMax: cd?.Max ?? 0,
      camDistanceMin: cd?.Min ?? 0,
      forceDefaultZoom: !!flags.ForceDefaultZoom,
      forceMaxZoom: !!flags.ForceMaxZoom,
      forceMinZoom: !!flags.ForceMinZoom,
    }
  }

  function clonePending(p: Pending): Pending {
    return {
      ...p,
      waterColor: { ...p.waterColor },
      fogColor: { ...p.fogColor },
      cameraComplements: { ...p.cameraComplements },
    }
  }

  // ----- Color helpers ------------------------------------------------------
  // Go-side Color is {R,G,B,A uint8}; JS side keeps lowercase. Wire format for
  // MapInfoApply matches the Go convention ({r,g,b,a}).
  function colorFromGo(c: any): Color {
    return {
      r: c?.R ?? 0,
      g: c?.G ?? 0,
      b: c?.B ?? 0,
      a: c?.A ?? 255,
    }
  }
  function colorToHex(c: Color): string {
    const h = (n: number) => Math.max(0, Math.min(255, n | 0)).toString(16).padStart(2, '0')
    return `#${h(c.r)}${h(c.g)}${h(c.b)}`
  }
  function hexToColor(hex: string, a: number): Color {
    const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim())
    if (!m) return { r: 0, g: 0, b: 0, a }
    const v = parseInt(m[1], 16)
    return { r: (v >> 16) & 0xff, g: (v >> 8) & 0xff, b: v & 0xff, a }
  }

  // ----- FourCC / tileset-char helpers --------------------------------------
  // Mirrors forge.Int32ToFourCC / packFourCCToInt32 on the wire — the Go side
  // takes the FourCC string directly via "weatherID" in ApplyInfoUpdates.
  function int32ToFourCC(v: number): string {
    if (!v) return ''
    const b = [v & 0xff, (v >> 8) & 0xff, (v >> 16) & 0xff, (v >> 24) & 0xff]
    let end = 4
    while (end > 0 && b[end - 1] === 0) end--
    let s = ''
    for (let i = 0; i < end; i++) s += String.fromCharCode(b[i] & 0xff)
    return s
  }
  function byteToChar(b: number): string {
    if (!b) return ''
    return String.fromCharCode(b & 0xff)
  }

  // ----- Open/close lifecycle ----------------------------------------------
  let lastOpen = $state(false)
  $effect(() => {
    if (open && !lastOpen) {
      lastOpen = true
      activeTab = 'description'
      void loadOptionLists()
      void loadInfo().then(() => tick().then(() => firstFocusableEl?.focus()))
      void loadSkyState()
    } else if (!open && lastOpen) {
      lastOpen = false
      pending = clonePending(baseline)
      validateAll()
      if (skySelected !== skyBaseline) {
        skySelected = skyBaseline
        void DiscardTo(skyHistoryAnchor)
      }
      onClose?.()
    }
  })

  // ----- Validation ---------------------------------------------------------

  function validateAll() {
    validateName()
    validateDescription()
    validateCameraComplements()
  }

  function validateName() {
    const t = pending.name ?? ''
    if (t.trim().length === 0) {
      nameError = 'Name is required.'
    } else if (t.length > NAME_MAX) {
      nameError = `Name is ${t.length}/${NAME_MAX} characters; trim to ${NAME_MAX} or fewer.`
    } else {
      nameError = ''
    }
  }

  function validateDescription() {
    const t = pending.description ?? ''
    if (t.length > DESC_MAX) {
      descError = `Description is ${t.length}/${DESC_MAX} characters; trim to ${DESC_MAX} or fewer.`
    } else {
      descError = ''
    }
  }

  // Camera complements must leave a playable area of at least 9×5 cells —
  // HiveWE's lower bound. The map itself can't be resized from this dialog,
  // so we validate only against the existing map dimensions.
  function validateCameraComplements() {
    if (mapCellsX <= 0 || mapCellsY <= 0) {
      camBoundsError = ''
      return
    }
    const { left, right, bottom, top } = pending.cameraComplements
    const playableW = mapCellsX - left - right
    const playableH = mapCellsY - bottom - top
    if (left < 0 || right < 0 || bottom < 0 || top < 0) {
      camBoundsError = 'Camera-bound margins must be ≥ 0.'
    } else if (playableW < 9 || playableH < 5) {
      camBoundsError = `Playable area must be at least 9×5 cells (current would be ${playableW}×${playableH}).`
    } else {
      camBoundsError = ''
    }
  }

  // ----- Diff builder + apply ----------------------------------------------
  // Only changed fields go on the wire (saves a round-trip and avoids
  // touching unrelated parts of the Info struct).

  function buildDiff(): Record<string, any> {
    const out: Record<string, any> = {}
    // Description
    if (pending.name !== baseline.name) out.name = pending.name
    if (pending.author !== baseline.author) out.author = pending.author
    if (pending.description !== baseline.description) out.description = pending.description
    if (pending.suggestedPlayers !== baseline.suggestedPlayers) out.suggestedPlayers = pending.suggestedPlayers

    // Loading Screen — derive concrete number/model from mode + sub-state.
    const lsCurrent = derivedLoadingScreen(pending)
    const lsBaseline = derivedLoadingScreen(baseline)
    if (lsCurrent.number !== lsBaseline.number) out.loadingScreenNumber = lsCurrent.number
    if (lsCurrent.model !== lsBaseline.model) out.loadingScreenModel = lsCurrent.model
    if (pending.loadingScreenText !== baseline.loadingScreenText) out.loadingScreenText = pending.loadingScreenText
    if (pending.loadingScreenTitle !== baseline.loadingScreenTitle) out.loadingScreenTitle = pending.loadingScreenTitle
    if (pending.loadingScreenSubtitle !== baseline.loadingScreenSubtitle) out.loadingScreenSubtitle = pending.loadingScreenSubtitle

    // Options
    if (pending.meleeMap !== baseline.meleeMap) out.meleeMap = pending.meleeMap
    if (pending.hideMinimapPreview !== baseline.hideMinimapPreview) out.hideMinimapPreview = pending.hideMinimapPreview
    if (pending.maskedAreaPartiallyVisible !== baseline.maskedAreaPartiallyVisible) out.maskedAreaPartiallyVisible = pending.maskedAreaPartiallyVisible
    if (pending.cliffShoreWaves !== baseline.cliffShoreWaves) out.cliffShoreWaves = pending.cliffShoreWaves
    if (pending.rollingShoreWaves !== baseline.rollingShoreWaves) out.rollingShoreWaves = pending.rollingShoreWaves
    if (pending.itemClassification !== baseline.itemClassification) out.itemClassification = pending.itemClassification
    if (pending.waterTinting !== baseline.waterTinting) out.waterTinting = pending.waterTinting
    if (!colorEquals(pending.waterColor, baseline.waterColor)) out.waterColor = pending.waterColor
    // Fog: enable=false collapses to style 0. Otherwise the current style
    // (which we preserve across the enable toggle).
    const baseStyle = baseline.fogEnabled ? baseline.fogStyle : 0
    const curStyle = pending.fogEnabled ? pending.fogStyle : 0
    if (curStyle !== baseStyle) out.fogStyle = curStyle
    if (pending.fogStartZ !== baseline.fogStartZ) out.fogStartZ = pending.fogStartZ
    if (pending.fogEndZ !== baseline.fogEndZ) out.fogEndZ = pending.fogEndZ
    if (pending.fogDensity !== baseline.fogDensity) out.fogDensity = pending.fogDensity
    if (!colorEquals(pending.fogColor, baseline.fogColor)) out.fogColor = pending.fogColor

    // Lighting & Weather
    if (pending.weatherID !== baseline.weatherID) out.weatherID = pending.weatherID
    if (pending.customSoundEnv !== baseline.customSoundEnv) out.customSoundEnv = pending.customSoundEnv
    if (pending.customLightTileset !== baseline.customLightTileset) out.customLightTileset = pending.customLightTileset

    // Size & Camera Bounds
    if (!ivec4Equals(pending.cameraComplements, baseline.cameraComplements))
      out.cameraComplements = pending.cameraComplements

    // Advanced
    if (pending.lua !== baseline.lua) out.lua = pending.lua
    if (pending.gameDataSet !== baseline.gameDataSet) out.gameDataSet = pending.gameDataSet
    if (pending.supportedModes !== baseline.supportedModes) out.supportedModes = pending.supportedModes
    if (pending.gameDataVersion !== baseline.gameDataVersion) out.gameDataVersion = pending.gameDataVersion
    if (pending.camDistanceDefault !== baseline.camDistanceDefault) out.camDistanceDefault = pending.camDistanceDefault
    if (pending.camDistanceMax !== baseline.camDistanceMax) out.camDistanceMax = pending.camDistanceMax
    if (pending.camDistanceMin !== baseline.camDistanceMin) out.camDistanceMin = pending.camDistanceMin
    if (pending.forceDefaultZoom !== baseline.forceDefaultZoom) out.forceDefaultZoom = pending.forceDefaultZoom
    if (pending.forceMaxZoom !== baseline.forceMaxZoom) out.forceMaxZoom = pending.forceMaxZoom
    if (pending.forceMinZoom !== baseline.forceMinZoom) out.forceMinZoom = pending.forceMinZoom

    return out
  }

  // derivedLoadingScreen collapses the UI's tri-state mode + per-mode values
  // into the {number, model} pair that lives on disk. HiveWE's convention:
  //   default  : number=-1, model=""
  //   campaign : number=index, model=""
  //   imported : number=-1, model="<relative .mdx path>"
  function derivedLoadingScreen(p: Pending): { number: number; model: string } {
    if (p.loadingMode === 'campaign') return { number: p.loadingScreenNumber, model: '' }
    if (p.loadingMode === 'imported') return { number: -1, model: p.loadingScreenModel }
    return { number: -1, model: '' }
  }

  async function applyDiff(closeAfter: boolean) {
    if (applying) return
    if (hasErrors) return
    const diff = buildDiff()
    if (Object.keys(diff).length === 0) {
      if (skySelected !== skyBaseline) {
        skyBaseline = skySelected
        showToast('Sky updated — Save Map to commit to script', 'info')
      }
      if (closeAfter) open = false
      return
    }
    applying = true
    try {
      await MapInfoApply(diff)
      const info = await MapInfoGet()
      pending = infoToPending(info)
      baseline = clonePending(pending)
      skyBaseline = skySelected
      validateAll()
      showToast('Map info updated', 'info')
      if (closeAfter) open = false
    } catch (e) {
      showToast('Map info update failed: ' + String(e), 'error')
    } finally {
      applying = false
    }
  }

  function onCancel() {
    if (skySelected !== skyBaseline) {
      skySelected = skyBaseline
      void DiscardTo(skyHistoryAnchor)
    }
    pending = clonePending(baseline)
    validateAll()
    open = false
  }

  let dialogTitle = $derived(
    `Map Info — ${(pending.name || '').trim() || '(untitled)'}`,
  )

  let nameCount = $derived((pending.name ?? '').length)
  let descCount = $derived((pending.description ?? '').length)
  let nameCountOver = $derived(nameCount > NAME_MAX)
  let descCountOver = $derived(descCount > DESC_MAX)

  function onNameInput() { validateName() }
  function onDescInput() { validateDescription() }

  function gameVersionLabel(v: number[]): string {
    if (!v || v.length < 4) return '(unknown)'
    return `${v[0]}.${v[1]}.${v[2]}.${v[3]}`
  }

  // --- Camera-bounds derived values + nudgers (Size tab) -------------------
  let playableWidth = $derived(Math.max(0, mapCellsX - pending.cameraComplements.left - pending.cameraComplements.right))
  let playableHeight = $derived(Math.max(0, mapCellsY - pending.cameraComplements.bottom - pending.cameraComplements.top))
  // Size description — surface-area thresholds straight from HiveWE.
  let mapSizeLabel = $derived(describeMapSize(mapCellsX, mapCellsY))
  function describeMapSize(w: number, h: number): string {
    if (w <= 0 || h <= 0) return ''
    const a = w * h
    if (a <= 80 * 80) return 'Tiny'
    if (a <= 112 * 112) return 'Extra Small'
    if (a <= 144 * 144) return 'Small'
    if (a <= 176 * 176) return 'Medium'
    if (a <= 208 * 208) return 'Large'
    if (a <= 272 * 272) return 'Extra Large'
    if (a <= 364 * 364) return 'Huge'
    return 'Epic'
  }
  function nudgeBound(side: keyof IVec4, delta: number) {
    pending.cameraComplements = {
      ...pending.cameraComplements,
      [side]: Math.max(0, pending.cameraComplements[side] + delta),
    }
    validateCameraComplements()
  }
  function resetBounds() {
    pending.cameraComplements = { left: 6, right: 6, bottom: 4, top: 8 }
    validateCameraComplements()
  }

  // --- Supported-modes bitfield helpers (Advanced) -------------------------
  // war3map.w3i v31+ supported_modes is a bitfield: bit 0 = SD, bit 1 = HD.
  function getSupportedMode(bit: number): boolean {
    return (pending.supportedModes & (1 << bit)) !== 0
  }
  function setSupportedMode(bit: number, on: boolean) {
    const mask = 1 << bit
    pending.supportedModes = on
      ? pending.supportedModes | mask
      : pending.supportedModes & ~mask
  }

  // --- Loading-screen mode helpers -----------------------------------------
  function setLoadingMode(mode: 'default' | 'campaign' | 'imported') {
    pending.loadingMode = mode
    if (mode === 'campaign' && pending.loadingScreenNumber < 0) {
      pending.loadingScreenNumber = campaignLoadingScreens[0]?.index ?? 0
    }
    if (mode === 'imported' && !pending.loadingScreenModel && importedLoadingScreens.length > 0) {
      pending.loadingScreenModel = importedLoadingScreens[0]
    }
  }

  // Fog style options (HiveWE order). Numeric wire values match the Info
  // struct's Fog.Style.
  const FOG_STYLES: { value: number; label: string }[] = [
    { value: 0, label: 'Linear' },
    { value: 1, label: 'Exponential 1' },
    { value: 2, label: 'Exponential 2' },
    { value: 3, label: 'User Defined' },
  ]

  // Game data set options — HiveWE exposes a fixed dropdown; we mirror.
  const GAME_DATA_SETS: { value: number; label: string }[] = [
    { value: 0, label: 'Default (Reign of Chaos + Frozen Throne)' },
    { value: 1, label: 'Custom' },
    { value: 2, label: 'Melee (latest patch)' },
  ]
</script>

<Dialog.Root bind:open>
  <Dialog.Content
    class="w-[min(960px,calc(100%-2rem))] max-w-[min(960px,calc(100%-2rem))] sm:max-w-[min(960px,calc(100%-2rem))] gap-0 p-0 overflow-hidden"
  >
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>{dialogTitle}</Dialog.Title>
      <Dialog.Description class="sr-only">
        Edit the map's metadata: name, description, options, lighting, camera bounds.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex min-h-[480px] max-h-[calc(100vh-12rem)]">
      <Tabs.Root
        bind:value={activeTab}
        orientation="vertical"
        class="flex-1 flex-row gap-0 min-h-0"
      >
        <Tabs.List
          variant="line"
          class="w-48 shrink-0 bg-muted/30 border-r rounded-none p-2 gap-1 items-stretch"
        >
          {#each TABS as t (t.id)}
            <Tabs.Trigger value={t.id} class="justify-start">
              {t.label}
            </Tabs.Trigger>
          {/each}
        </Tabs.List>

        <div class="flex-1 overflow-y-auto p-5 min-w-0">
          {#if loading}
            <div class="text-center text-muted-foreground py-16 text-sm">
              Loading…
            </div>
          {:else}
            <!-- =============== DESCRIPTION =============== -->
            <Tabs.Content value="description" class="mt-0">
              <div class="flex flex-col gap-4 max-w-xl">
                <div class="flex flex-col gap-1.5">
                  <Label for="mi-name">Name</Label>
                  <Input
                    id="mi-name"
                    type="text"
                    maxlength={NAME_MAX * 2}
                    bind:value={pending.name}
                    oninput={onNameInput}
                    aria-invalid={!!nameError}
                    bind:ref={firstFocusableEl}
                  />
                  <div class="flex items-center gap-3 text-xs text-muted-foreground">
                    <span class="font-mono" class:text-destructive={nameCountOver} class:font-medium={nameCountOver}>
                      {nameCount}/{NAME_MAX}
                    </span>
                    {#if nameError}<span class="text-destructive">{nameError}</span>{/if}
                  </div>
                </div>

                <div class="flex flex-col gap-1.5">
                  <Label for="mi-author">Author</Label>
                  <Input id="mi-author" type="text" bind:value={pending.author} />
                </div>

                <div class="flex flex-col gap-1.5">
                  <Label for="mi-desc">Description</Label>
                  <Textarea
                    id="mi-desc"
                    rows={6}
                    bind:value={pending.description}
                    oninput={onDescInput}
                    aria-invalid={!!descError}
                  />
                  <div class="flex items-center gap-3 text-xs text-muted-foreground">
                    <span class="font-mono" class:text-destructive={descCountOver} class:font-medium={descCountOver}>
                      {descCount}/{DESC_MAX}
                    </span>
                    {#if descError}<span class="text-destructive">{descError}</span>{/if}
                  </div>
                </div>

                <div class="flex flex-col gap-1.5">
                  <Label for="mi-players">Suggested Players</Label>
                  <Input
                    id="mi-players"
                    type="text"
                    placeholder='e.g. "4 (2v2)" — free-form'
                    bind:value={pending.suggestedPlayers}
                  />
                </div>

                <details class="mt-2 border-t pt-3" bind:open={showMetadata}>
                  <summary class="cursor-pointer text-xs text-muted-foreground hover:text-foreground select-none">
                    Show editor metadata
                  </summary>
                  <dl class="grid grid-cols-[max-content_1fr] gap-x-3.5 gap-y-1.5 mt-2.5 text-xs">
                    <dt class="text-muted-foreground">Map Version</dt>
                    <dd class="font-mono">{mapVersion}</dd>
                    <dt class="text-muted-foreground">Editor Version</dt>
                    <dd class="font-mono">{editorVersion}</dd>
                    <dt class="text-muted-foreground">File Version</dt>
                    <dd class="font-mono">{fileVersion}</dd>
                    <dt class="text-muted-foreground">Game Version</dt>
                    <dd class="font-mono">{gameVersionLabel(gameVersion)}</dd>
                  </dl>
                </details>
              </div>
            </Tabs.Content>

            <!-- =============== LOADING SCREEN =============== -->
            <Tabs.Content value="loading" class="mt-0">
              <div class="flex flex-col gap-4 max-w-2xl">
                <fieldset class="flex flex-col gap-2 border rounded p-3">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Source</legend>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <input
                      type="radio"
                      name="ls-mode"
                      value="default"
                      checked={pending.loadingMode === 'default'}
                      onchange={() => setLoadingMode('default')}
                    />
                    <span>Default (engine picks based on tileset)</span>
                  </label>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <input
                      type="radio"
                      name="ls-mode"
                      value="campaign"
                      checked={pending.loadingMode === 'campaign'}
                      onchange={() => setLoadingMode('campaign')}
                    />
                    <span>Campaign loading screen</span>
                  </label>
                  {#if pending.loadingMode === 'campaign'}
                    <select
                      class="ml-6 border border-input bg-background rounded px-2.5 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-ring max-w-md"
                      value={String(pending.loadingScreenNumber)}
                      onchange={(e) => pending.loadingScreenNumber = parseInt((e.target as HTMLSelectElement).value, 10)}
                    >
                      {#if campaignLoadingScreens.length === 0}
                        <option value="-1">(none available)</option>
                      {/if}
                      {#each campaignLoadingScreens as cls (cls.index)}
                        <option value={String(cls.index)}>{cls.label}</option>
                      {/each}
                    </select>
                  {/if}

                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <input
                      type="radio"
                      name="ls-mode"
                      value="imported"
                      checked={pending.loadingMode === 'imported'}
                      onchange={() => setLoadingMode('imported')}
                    />
                    <span>Imported model (custom .mdx in the map archive)</span>
                  </label>
                  {#if pending.loadingMode === 'imported'}
                    {#if importedLoadingScreens.length > 0}
                      <select
                        class="ml-6 border border-input bg-background rounded px-2.5 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-ring max-w-md"
                        value={pending.loadingScreenModel}
                        onchange={(e) => pending.loadingScreenModel = (e.target as HTMLSelectElement).value}
                      >
                        {#each importedLoadingScreens as p (p)}
                          <option value={p}>{p}</option>
                        {/each}
                      </select>
                    {:else}
                      <Input
                        class="ml-6 max-w-md"
                        type="text"
                        placeholder={'e.g. LoadingScreen\\MyCustom.mdx'}
                        bind:value={pending.loadingScreenModel}
                      />
                      <p class="ml-6 text-xs text-muted-foreground">
                        No .mdx files found in the map root. Type a relative path
                        manually if you've imported one elsewhere, or import
                        through the Import Manager.
                      </p>
                    {/if}
                  {/if}
                </fieldset>

                <div class="flex flex-col gap-1.5">
                  <Label for="mi-ls-title">Title</Label>
                  <Input id="mi-ls-title" type="text" bind:value={pending.loadingScreenTitle} />
                </div>
                <div class="flex flex-col gap-1.5">
                  <Label for="mi-ls-subtitle">Subtitle</Label>
                  <Input id="mi-ls-subtitle" type="text" bind:value={pending.loadingScreenSubtitle} />
                </div>
                <div class="flex flex-col gap-1.5">
                  <Label for="mi-ls-text">Text</Label>
                  <Textarea id="mi-ls-text" rows={6} bind:value={pending.loadingScreenText} />
                </div>
              </div>
            </Tabs.Content>

            <!-- =============== OPTIONS =============== -->
            <Tabs.Content value="options" class="mt-0">
              <div class="grid grid-cols-2 gap-x-6 gap-y-4 max-w-3xl">
                <fieldset class="col-span-1 flex flex-col gap-2 border rounded p-3">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Map flags</legend>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={pending.meleeMap} onCheckedChange={(v) => pending.meleeMap = !!v} />
                    <span>Melee map</span>
                  </label>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={pending.hideMinimapPreview} onCheckedChange={(v) => pending.hideMinimapPreview = !!v} />
                    <span>Hide minimap preview in lobby</span>
                  </label>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={pending.maskedAreaPartiallyVisible} onCheckedChange={(v) => pending.maskedAreaPartiallyVisible = !!v} />
                    <span>Masked area partially visible</span>
                  </label>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={pending.itemClassification} onCheckedChange={(v) => pending.itemClassification = !!v} />
                    <span>Item classification</span>
                  </label>
                </fieldset>

                <fieldset class="col-span-1 flex flex-col gap-2 border rounded p-3">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Shore effects</legend>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={pending.cliffShoreWaves} onCheckedChange={(v) => pending.cliffShoreWaves = !!v} />
                    <span>Cliff-shore waves</span>
                  </label>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={pending.rollingShoreWaves} onCheckedChange={(v) => pending.rollingShoreWaves = !!v} />
                    <span>Rolling-shore waves</span>
                  </label>
                </fieldset>

                <fieldset class="col-span-2 flex flex-col gap-3 border rounded p-3">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Water</legend>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={pending.waterTinting} onCheckedChange={(v) => pending.waterTinting = !!v} />
                    <span>Use water tinting</span>
                  </label>
                  <div class="flex items-center gap-3 ml-6">
                    <Label class="text-sm text-muted-foreground">Color</Label>
                    <input
                      type="color"
                      class="h-8 w-16 cursor-pointer border border-input rounded bg-background"
                      value={colorToHex(pending.waterColor)}
                      oninput={(e) => pending.waterColor = hexToColor((e.target as HTMLInputElement).value, pending.waterColor.a)}
                      disabled={!pending.waterTinting}
                    />
                    <span class="font-mono text-xs text-muted-foreground">{colorToHex(pending.waterColor)}</span>
                  </div>
                </fieldset>

                <fieldset class="col-span-2 flex flex-col gap-3 border rounded p-3">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Terrain fog</legend>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={pending.fogEnabled} onCheckedChange={(v) => pending.fogEnabled = !!v} />
                    <span>Enable terrain fog</span>
                  </label>
                  <div class="grid grid-cols-2 gap-3 ml-6" class:opacity-50={!pending.fogEnabled}>
                    <div class="flex flex-col gap-1">
                      <Label class="text-xs">Style</Label>
                      <select
                        class="border border-input bg-background rounded px-2 py-1 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                        value={String(pending.fogStyle)}
                        onchange={(e) => pending.fogStyle = parseInt((e.target as HTMLSelectElement).value, 10)}
                        disabled={!pending.fogEnabled}
                      >
                        {#each FOG_STYLES as fs (fs.value)}
                          <option value={String(fs.value)}>{fs.label}</option>
                        {/each}
                      </select>
                    </div>
                    <div class="flex flex-col gap-1">
                      <Label class="text-xs">Color</Label>
                      <div class="flex items-center gap-2">
                        <input
                          type="color"
                          class="h-8 w-16 cursor-pointer border border-input rounded bg-background"
                          value={colorToHex(pending.fogColor)}
                          oninput={(e) => pending.fogColor = hexToColor((e.target as HTMLInputElement).value, pending.fogColor.a)}
                          disabled={!pending.fogEnabled}
                        />
                        <span class="font-mono text-xs text-muted-foreground">{colorToHex(pending.fogColor)}</span>
                      </div>
                    </div>
                    <div class="flex flex-col gap-1">
                      <Label class="text-xs">Start Z</Label>
                      <Input
                        type="number"
                        step={1}
                        value={pending.fogStartZ}
                        oninput={(e) => pending.fogStartZ = parseFloat((e.target as HTMLInputElement).value)}
                        disabled={!pending.fogEnabled}
                      />
                    </div>
                    <div class="flex flex-col gap-1">
                      <Label class="text-xs">End Z</Label>
                      <Input
                        type="number"
                        step={1}
                        value={pending.fogEndZ}
                        oninput={(e) => pending.fogEndZ = parseFloat((e.target as HTMLInputElement).value)}
                        disabled={!pending.fogEnabled}
                      />
                    </div>
                    <div class="flex flex-col gap-1 col-span-2">
                      <Label class="text-xs">Density</Label>
                      <Input
                        type="number"
                        step={0.01}
                        value={pending.fogDensity}
                        oninput={(e) => pending.fogDensity = parseFloat((e.target as HTMLInputElement).value)}
                        disabled={!pending.fogEnabled}
                      />
                    </div>
                  </div>
                </fieldset>
              </div>
            </Tabs.Content>

            <!-- =============== LIGHTING & WEATHER =============== -->
            <Tabs.Content value="lighting" class="mt-0">
              <div class="flex flex-col gap-5 max-w-2xl">
                <div class="flex flex-col gap-1.5">
                  <Label for="map-info-sky">Sky model</Label>
                  <select
                    id="map-info-sky"
                    class="border border-input bg-background rounded px-2.5 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                    value={skySelected}
                    onchange={(e) => void onSkyPicked((e.target as HTMLSelectElement).value)}
                  >
                    {#each skyOptions as opt (opt.path)}
                      <option value={opt.path}>{opt.display_name}</option>
                    {/each}
                  </select>
                  <p class="text-xs text-muted-foreground leading-relaxed">
                    Picks update the editor preview immediately by loading the
                    chosen SkyModel mdx. On Save, a
                    <code class="font-mono">SetSkyModel(...)</code> call is
                    written into <code class="font-mono">war3map.j</code> or
                    <code class="font-mono">war3map.lua</code> so the sky also
                    shows in-game. Pick <em>None</em> to drop the dome.
                  </p>
                  {#if skyChanged}
                    <p class="text-xs text-amber-500">
                      Unsaved change — applies in editor now; commits to script on Save.
                    </p>
                  {/if}
                </div>

                <div class="flex flex-col gap-1.5">
                  <Label for="map-info-weather">Global weather</Label>
                  <select
                    id="map-info-weather"
                    class="border border-input bg-background rounded px-2.5 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                    value={pending.weatherID}
                    onchange={(e) => pending.weatherID = (e.target as HTMLSelectElement).value}
                  >
                    {#each weatherOptions as w (w.key + w.label)}
                      <option value={w.key}>{w.label}</option>
                    {/each}
                  </select>
                  <p class="text-xs text-muted-foreground">
                    Applies a built-in weather effect (rain, snow, wind, …)
                    across the entire playable area at runtime.
                  </p>
                </div>

                <div class="flex flex-col gap-1.5">
                  <Label for="map-info-sound">Custom sound environment</Label>
                  <select
                    id="map-info-sound"
                    class="border border-input bg-background rounded px-2.5 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                    value={pending.customSoundEnv}
                    onchange={(e) => pending.customSoundEnv = (e.target as HTMLSelectElement).value}
                  >
                    {#each soundEnvOptions as s (s.key + s.label)}
                      <option value={s.key}>{s.label}</option>
                    {/each}
                  </select>
                  <p class="text-xs text-muted-foreground">
                    Overrides the ambient sound bank with a themed environment.
                  </p>
                </div>

                <div class="flex flex-col gap-1.5">
                  <Label for="map-info-light">Custom light tileset</Label>
                  <select
                    id="map-info-light"
                    class="border border-input bg-background rounded px-2.5 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                    value={pending.customLightTileset}
                    onchange={(e) => pending.customLightTileset = (e.target as HTMLSelectElement).value}
                  >
                    {#each tilesetOptions as t (t.key + t.label)}
                      <option value={t.key}>{t.label}</option>
                    {/each}
                  </select>
                  <p class="text-xs text-muted-foreground">
                    Use a different tileset's lighting profile (sun color, ambient,
                    fog) than the map's terrain tileset would imply.
                  </p>
                </div>
              </div>
            </Tabs.Content>

            <!-- =============== SIZE & CAMERA BOUNDS =============== -->
            <Tabs.Content value="size" class="mt-0">
              <div class="flex flex-col gap-5 max-w-2xl">
                <div class="border rounded p-3 flex flex-col gap-1.5">
                  <h3 class="text-sm font-medium">Map dimensions</h3>
                  <dl class="grid grid-cols-[max-content_1fr] gap-x-3.5 gap-y-1 text-xs">
                    <dt class="text-muted-foreground">Total size</dt>
                    <dd class="font-mono">{mapCellsX} × {mapCellsY} cells {mapSizeLabel ? `(${mapSizeLabel})` : ''}</dd>
                    <dt class="text-muted-foreground">Playable area</dt>
                    <dd class="font-mono">{playableWidth} × {playableHeight} cells</dd>
                  </dl>
                  <p class="text-xs text-muted-foreground leading-relaxed mt-2">
                    Resizing the total map (adding or trimming cells) is done from
                    the Terrain Palette — it requires rewriting <code class="font-mono">war3map.w3e</code>
                    and isn't safe from this dialog. The fields below adjust the
                    <em>camera bounds</em> — how much of the map sits in the
                    unplayable border.
                  </p>
                </div>

                <fieldset class="border rounded p-3 flex flex-col gap-3">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Unplayable border (cells)</legend>
                  <div class="grid grid-cols-2 gap-3">
                    {#each [
                      { key: 'left' as const, label: 'Left' },
                      { key: 'right' as const, label: 'Right' },
                      { key: 'bottom' as const, label: 'Bottom' },
                      { key: 'top' as const, label: 'Top' },
                    ] as side (side.key)}
                      <div class="flex flex-col gap-1">
                        <Label class="text-xs">{side.label}</Label>
                        <div class="flex items-center gap-1">
                          <Button variant="outline" size="sm" onclick={() => nudgeBound(side.key, -1)} disabled={pending.cameraComplements[side.key] <= 0}>−</Button>
                          <Input
                            class="text-center"
                            type="number"
                            min="0"
                            value={pending.cameraComplements[side.key]}
                            oninput={(e) => {
                              const v = Math.max(0, parseInt((e.target as HTMLInputElement).value, 10) || 0)
                              pending.cameraComplements = { ...pending.cameraComplements, [side.key]: v }
                              validateCameraComplements()
                            }}
                          />
                          <Button variant="outline" size="sm" onclick={() => nudgeBound(side.key, 1)}>+</Button>
                        </div>
                      </div>
                    {/each}
                  </div>
                  <div class="flex justify-between items-center mt-2">
                    <Button variant="outline" size="sm" onclick={resetBounds}>Reset to default (6/6/4/8)</Button>
                    {#if camBoundsError}
                      <span class="text-destructive text-xs">{camBoundsError}</span>
                    {/if}
                  </div>
                </fieldset>
              </div>
            </Tabs.Content>

            <!-- =============== ADVANCED =============== -->
            <Tabs.Content value="advanced" class="mt-0">
              <div class="flex flex-col gap-5 max-w-2xl">
                <fieldset class="border rounded p-3 flex flex-col gap-2">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Script language</legend>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={pending.lua} onCheckedChange={(v) => pending.lua = !!v} />
                    <span>Primary script is Lua (Reforged 1.31+)</span>
                  </label>
                  <p class="text-xs text-muted-foreground">
                    Affects which script file <code class="font-mono">war3map.j</code>
                    vs <code class="font-mono">war3map.lua</code> the engine executes
                    at runtime. Toggling this does <em>not</em> convert your existing
                    script — make sure the corresponding file exists.
                  </p>
                </fieldset>

                <fieldset class="border rounded p-3 flex flex-col gap-3">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Game data set</legend>
                  <select
                    class="border border-input bg-background rounded px-2.5 py-1.5 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                    value={String(pending.gameDataSet)}
                    onchange={(e) => pending.gameDataSet = parseInt((e.target as HTMLSelectElement).value, 10)}
                  >
                    {#each GAME_DATA_SETS as g (g.value)}
                      <option value={String(g.value)}>{g.label}</option>
                    {/each}
                  </select>
                </fieldset>

                <fieldset class="border rounded p-3 flex flex-col gap-2">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Supported modes (Reforged v31+)</legend>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={getSupportedMode(0)} onCheckedChange={(v) => setSupportedMode(0, !!v)} />
                    <span>SD (Classic)</span>
                  </label>
                  <label class="flex items-center gap-2 text-sm cursor-pointer">
                    <Checkbox checked={getSupportedMode(1)} onCheckedChange={(v) => setSupportedMode(1, !!v)} />
                    <span>HD (Reforged)</span>
                  </label>
                  <p class="text-xs text-muted-foreground">
                    Which client graphics modes the map is authored for. With
                    neither selected, the lobby falls back to default behaviour.
                  </p>
                </fieldset>

                <fieldset class="border rounded p-3 flex flex-col gap-3">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Camera distance (Reforged v32+)</legend>
                  <div class="grid grid-cols-3 gap-3">
                    <div class="flex flex-col gap-1">
                      <Label class="text-xs">Default</Label>
                      <Input
                        type="number"
                        min="0"
                        value={pending.camDistanceDefault}
                        oninput={(e) => pending.camDistanceDefault = Math.max(0, parseInt((e.target as HTMLInputElement).value, 10) || 0)}
                      />
                    </div>
                    <div class="flex flex-col gap-1">
                      <Label class="text-xs">Max</Label>
                      <Input
                        type="number"
                        min="0"
                        value={pending.camDistanceMax}
                        oninput={(e) => pending.camDistanceMax = Math.max(0, parseInt((e.target as HTMLInputElement).value, 10) || 0)}
                      />
                    </div>
                    <div class="flex flex-col gap-1">
                      <Label class="text-xs">Min (v33+)</Label>
                      <Input
                        type="number"
                        min="0"
                        value={pending.camDistanceMin}
                        oninput={(e) => pending.camDistanceMin = Math.max(0, parseInt((e.target as HTMLInputElement).value, 10) || 0)}
                      />
                    </div>
                  </div>
                  <div class="flex flex-col gap-2">
                    <label class="flex items-center gap-2 text-sm cursor-pointer">
                      <Checkbox checked={pending.forceDefaultZoom} onCheckedChange={(v) => pending.forceDefaultZoom = !!v} />
                      <span>Force default zoom</span>
                    </label>
                    <label class="flex items-center gap-2 text-sm cursor-pointer">
                      <Checkbox checked={pending.forceMaxZoom} onCheckedChange={(v) => pending.forceMaxZoom = !!v} />
                      <span>Force max zoom</span>
                    </label>
                    <label class="flex items-center gap-2 text-sm cursor-pointer">
                      <Checkbox checked={pending.forceMinZoom} onCheckedChange={(v) => pending.forceMinZoom = !!v} />
                      <span>Force min zoom</span>
                    </label>
                  </div>
                </fieldset>

                <fieldset class="border rounded p-3 flex flex-col gap-3">
                  <legend class="px-1 text-xs font-medium text-muted-foreground">Game data version</legend>
                  <div class="flex flex-col gap-1">
                    <Label class="text-xs">Reforged v31+ data version (uint32)</Label>
                    <Input
                      type="number"
                      min="0"
                      value={pending.gameDataVersion}
                      oninput={(e) => pending.gameDataVersion = Math.max(0, parseInt((e.target as HTMLInputElement).value, 10) || 0)}
                    />
                  </div>
                </fieldset>
              </div>
            </Tabs.Content>
          {/if}
        </div>
      </Tabs.Root>
    </div>

    <Dialog.Footer class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0">
      <span class="text-xs text-muted-foreground self-center mr-auto">
        {#if hasErrors}
          <span class="text-destructive">Fix the highlighted issues to enable Apply.</span>
        {:else if isChanged}
          Unsaved changes
        {/if}
      </span>
      <Button variant="ghost" onclick={onCancel} disabled={applying}>Cancel</Button>
      <Button onclick={() => applyDiff(true)} disabled={hasErrors || applying || loading}>
        OK
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
