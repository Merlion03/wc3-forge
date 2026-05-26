<script lang="ts">
  // Modal dialog for editing war3map.w3i metadata. v1 ships Tab 1
  // (Description) with full edit/apply/save plumbing; tabs 2-6 are
  // placeholders so the framework is validated and future agents can drop
  // content in without touching the modal skeleton.
  //
  // Open/close is controlled by the parent (App.svelte) via the bindable
  // `open` prop. On open, MapInfoGet() is called to load the full Info
  // struct into a local pending-edits store; on Apply/OK, the diff is sent
  // through MapInfoApply (so only changed fields cross the wire); on
  // Cancel/Esc/click-outside, pending edits are discarded.
  //
  // Modal chrome (backdrop, Esc-to-close, click-outside, focus trap) is
  // delegated to shadcn-svelte's Dialog (bits-ui under the hood) — we no
  // longer hand-roll any of that.
  //
  // TRIGSTR handling: Option A (literal strings). The w3i Info struct
  // already carries resolved DISPLAY strings post-Session.Open; on save we
  // write them back verbatim per the existing w3i.Encode wire contract.

  import { tick } from 'svelte'
  import { MapInfoGet, MapInfoApply, ListSkyModels, SetSkyModel, GetTerrain, HistoryUndoCount, DiscardTo } from '../wailsjs/go/main/App.js'
  import { showToast } from './toast'
  import * as Dialog from '$lib/components/ui/dialog'
  import * as Tabs from '$lib/components/ui/tabs'
  import { Input } from '$lib/components/ui/input'
  import { Textarea } from '$lib/components/ui/textarea'
  import { Label } from '$lib/components/ui/label'
  import { Button } from '$lib/components/ui/button'

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

  // Tab definitions. v1 only populates 'description'; the rest render a
  // single "Coming soon" body so the tab framework is validated. Add new
  // tabs by appending to TABS and adding a matching <Tabs.Content> below.
  type TabId =
    | 'description'
    | 'options'
    | 'loading'
    | 'lighting'
    | 'size'
    | 'advanced'
  interface Tab {
    id: TabId
    label: string
  }
  const TABS: Tab[] = [
    { id: 'description', label: 'Description' },
    { id: 'options', label: 'Options' },
    { id: 'loading', label: 'Loading Screen' },
    { id: 'lighting', label: 'Lighting & Weather' },
    { id: 'size', label: 'Size & Camera Bounds' },
    { id: 'advanced', label: 'Advanced' },
  ]
  // Always defaults to Description on (re)open; the placeholder tabs are
  // diagnostic, so re-opening to a stub would be a surprising UX.
  let activeTab: string = $state('description')

  // Pending edits — the local mutable copy that the form binds to.
  // Initialized from the parsed Info on dialog open. On Apply/OK, we diff
  // against the baseline (snapshot at last open or last Apply) to send only
  // changed fields. On Cancel/Esc/backdrop, pending is restored from
  // baseline (effectively discarded).
  interface Pending {
    name: string
    author: string
    description: string
    suggestedPlayers: string
  }
  let pending: Pending = $state(blankPending())
  let baseline: Pending = $state(blankPending())

  // Readonly metadata pulled at open. Not edited.
  let mapVersion: number = $state(0)
  let editorVersion: number = $state(0)
  let fileVersion: number = $state(0)
  let gameVersion: number[] = $state([0, 0, 0, 0])

  // Validation state. Per-field error message; empty string = valid.
  let nameError: string = $state('')
  let descError: string = $state('')
  let hasErrors = $derived(!!nameError || !!descError)

  // Live-changed: pending differs from baseline (any field). Drives the
  // Apply button's enabled state. Sky changes count too — they live-preview
  // but should still let the user "lock them in" via Apply so a subsequent
  // dialog close doesn't revert.
  let isChanged = $derived(
    pending.name !== baseline.name ||
      pending.author !== baseline.author ||
      pending.description !== baseline.description ||
      pending.suggestedPlayers !== baseline.suggestedPlayers ||
      skySelected !== skyBaseline,
  )

  // Char counts. Limits per design doc / lobby behavior:
  //   - Name: 80 (lobby truncates)
  //   - Description: 512 (game UI clips longer)
  const NAME_MAX = 80
  const DESC_MAX = 512

  let loading: boolean = $state(false)
  let applying: boolean = $state(false)

  // Ref for autofocus on open.
  let firstFocusableEl: HTMLInputElement | null = $state(null)

  // Show-metadata disclosure (Game Version, File Version) — hidden by
  // default per design (it's diagnostic, not user-editable).
  let showMetadata: boolean = $state(false)

  function blankPending(): Pending {
    return { name: '', author: '', description: '', suggestedPlayers: '' }
  }

  // Sky picker state. Lives outside `Pending` because saving the sky model
  // doesn't go through MapInfoApply — it's a script-rewrite committed by
  // SetSkyModel on Save. Loaded on dialog open from ListSkyModels (stock
  // options sourced from WorldEditData.txt's [SkyModels]) and GetTerrain
  // (current resolved path for the loaded map).
  interface SkyOpt { path: string; name_key: string; display_name: string }
  let skyOptions: SkyOpt[] = $state([])
  let skySelected: string = $state('') // path of the currently-picked option
  let skyBaseline: string = $state('') // value at dialog-open; used to detect "did the user change it?"
  let skyChanged = $derived(skySelected !== skyBaseline)
  // Undo-stack depth captured at dialog open. Cancel calls UndoTo(this) to
  // revert every sky pick made during the dialog session in one RPC. Each
  // picker change records its own undo entry server-side (see history.go
  // setSkyModelCmd) so Ctrl+Z while the dialog is open also reverts a
  // single step.
  let skyHistoryAnchor: number = $state(0)

  async function loadSkyState() {
    try {
      const [opts, t, anchor] = await Promise.all([
        ListSkyModels(),
        GetTerrain(),
        HistoryUndoCount(),
      ])
      skyOptions = (opts ?? []) as SkyOpt[]
      skySelected = (t as any)?.sky_model ?? ''
      skyBaseline = skySelected
      skyHistoryAnchor = anchor
    } catch (e) {
      // Non-fatal: picker stays empty, other tabs still work.
      showToast('Could not load sky options: ' + String(e), 'error')
    }
  }

  async function onSkyPicked(path: string) {
    skySelected = path
    try {
      // Tell Go to queue the override + return the normalized path. The
      // parent receives that path and rebuilds the scene's sky immediately.
      const resolved = await SetSkyModel(path)
      onSkyChanged?.(resolved)
    } catch (e) {
      showToast('Failed to set sky: ' + String(e), 'error')
    }
  }

  // Open transition: pull current Info, populate pending + baseline. Errors
  // surface as a toast and close the dialog (we have nothing to render).
  async function loadInfo() {
    loading = true
    try {
      const info = await MapInfoGet()
      pending = {
        name: info?.Name ?? '',
        author: info?.Author ?? '',
        description: info?.Description ?? '',
        suggestedPlayers: info?.SuggestedPlayers ?? '',
      }
      baseline = { ...pending }
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

  // React to `open` transitions. On false→true: reset to Description tab,
  // load Info, focus the first input. On true→false: notify the parent
  // and discard pending edits (shadcn closes on Esc/backdrop without
  // calling onCancel, so we must reset here).
  // Track open-transitions. The OPEN side fires reliably here. The CLOSE
  // side is mirrored in onCancel for the Cancel-button path because Svelte 5
  // doesn't retrigger the child's $effect when the child itself writes to a
  // $bindable prop (the write propagates through the parent, not through
  // the child's local reactivity tracker). This branch handles Esc /
  // click-outside, where the parent's binding flips open to false from
  // outside the child — that path DOES retrigger the effect.
  let lastOpen = $state(false)
  $effect(() => {
    if (open && !lastOpen) {
      lastOpen = true
      activeTab = 'description'
      void loadInfo().then(() => tick().then(() => firstFocusableEl?.focus()))
      void loadSkyState()
    } else if (!open && lastOpen) {
      lastOpen = false
      // Discard any pending edits so a re-open starts clean. Mirrors the
      // same cleanup onCancel does — see that function for the sky-revert
      // rationale (DiscardTo reverts state AND drops the picks from history).
      pending = { ...baseline }
      validateAll()
      if (skySelected !== skyBaseline) {
        skySelected = skyBaseline
        void DiscardTo(skyHistoryAnchor)
      }
      onClose?.()
    }
  })

  function validateAll() {
    validateName()
    validateDescription()
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

  // Diff helper: compute the partial-updates map to send to MapInfoApply.
  // Only changed fields are included so unrelated parts of the Info struct
  // never see a write.
  function buildDiff(): Record<string, any> {
    const out: Record<string, any> = {}
    if (pending.name !== baseline.name) out.name = pending.name
    if (pending.author !== baseline.author) out.author = pending.author
    if (pending.description !== baseline.description)
      out.description = pending.description
    if (pending.suggestedPlayers !== baseline.suggestedPlayers)
      out.suggestedPlayers = pending.suggestedPlayers
    return out
  }

  async function applyDiff(closeAfter: boolean) {
    if (applying) return
    if (hasErrors) return
    const diff = buildDiff()
    if (Object.keys(diff).length === 0) {
      // No w3i changes — but if the sky picker moved, the user clicking
      // Apply still has a purpose: lock in the sky pick so a subsequent
      // dialog close doesn't revert it. (The Go-side override is already
      // set live; we just update skyBaseline.)
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
      // Pull the fresh Info back to update baseline and reflect any Go-side
      // normalization.
      const info = await MapInfoGet()
      pending = {
        name: info?.Name ?? '',
        author: info?.Author ?? '',
        description: info?.Description ?? '',
        suggestedPlayers: info?.SuggestedPlayers ?? '',
      }
      baseline = { ...pending }
      // Lock in the sky pick too — Apply commits the dialog's whole state
      // to the "this is what's pending in the session" line, so a later
      // dialog close shouldn't revert the user's sky choice. The script
      // rewrite still happens on the next "Save Map".
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
    // Inline the close-path cleanup that the $effect would otherwise do.
    // Svelte 5's $effect doesn't reliably retrigger when the child writes
    // to its own $bindable prop (the write goes through the parent's
    // reactivity, not the child's local tracker), so doing the revert here
    // makes it deterministic. The $effect's close branch still fires for
    // Esc / click-outside (parent-driven open=false) — it's idempotent
    // because skySelected==skyBaseline after this runs, so DiscardTo
    // short-circuits to a no-op.
    if (skySelected !== skyBaseline) {
      skySelected = skyBaseline
      void DiscardTo(skyHistoryAnchor)
    }
    pending = { ...baseline }
    validateAll()
    open = false
  }

  // Live header title — pulls from pending.name so the user sees their
  // edits reflected immediately.
  let dialogTitle = $derived(
    `Map Info — ${(pending.name || '').trim() || '(untitled)'}`,
  )

  // Char-count helpers. Past the limit, switch the counter to red.
  let nameCount = $derived((pending.name ?? '').length)
  let descCount = $derived((pending.description ?? '').length)
  let nameCountOver = $derived(nameCount > NAME_MAX)
  let descCountOver = $derived(descCount > DESC_MAX)

  function onFieldInput(field: keyof Pending) {
    if (field === 'name') validateName()
    if (field === 'description') validateDescription()
  }

  function gameVersionLabel(v: number[]): string {
    if (!v || v.length < 4) return '(unknown)'
    return `${v[0]}.${v[1]}.${v[2]}.${v[3]}`
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content
    class="w-[min(880px,calc(100%-2rem))] max-w-[min(880px,calc(100%-2rem))] sm:max-w-[min(880px,calc(100%-2rem))] gap-0 p-0 overflow-hidden"
  >
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>{dialogTitle}</Dialog.Title>
      <Dialog.Description class="sr-only">
        Edit the map's name, author, description, and other metadata.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex min-h-[420px] max-h-[calc(100vh-12rem)]">
      <Tabs.Root
        bind:value={activeTab}
        orientation="vertical"
        class="flex-1 flex-row gap-0 min-h-0"
      >
        <Tabs.List
          variant="line"
          class="w-44 shrink-0 bg-muted/30 border-r rounded-none p-2 gap-1 items-stretch"
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
            <Tabs.Content value="description" class="mt-0">
              <div class="flex flex-col gap-4 max-w-xl">
                <div class="flex flex-col gap-1.5">
                  <Label for="mi-name">Name</Label>
                  <Input
                    id="mi-name"
                    type="text"
                    maxlength={NAME_MAX * 2}
                    bind:value={pending.name}
                    oninput={() => onFieldInput('name')}
                    aria-invalid={!!nameError}
                    bind:ref={firstFocusableEl}
                  />
                  <div class="flex items-center gap-3 text-xs text-muted-foreground">
                    <span
                      class="font-mono"
                      class:text-destructive={nameCountOver}
                      class:font-medium={nameCountOver}
                    >
                      {nameCount}/{NAME_MAX}
                    </span>
                    {#if nameError}
                      <span class="text-destructive">{nameError}</span>
                    {/if}
                  </div>
                </div>

                <div class="flex flex-col gap-1.5">
                  <Label for="mi-author">Author</Label>
                  <Input
                    id="mi-author"
                    type="text"
                    bind:value={pending.author}
                  />
                </div>

                <div class="flex flex-col gap-1.5">
                  <Label for="mi-desc">Description</Label>
                  <Textarea
                    id="mi-desc"
                    rows={6}
                    bind:value={pending.description}
                    oninput={() => onFieldInput('description')}
                    aria-invalid={!!descError}
                  />
                  <div class="flex items-center gap-3 text-xs text-muted-foreground">
                    <span
                      class="font-mono"
                      class:text-destructive={descCountOver}
                      class:font-medium={descCountOver}
                    >
                      {descCount}/{DESC_MAX}
                    </span>
                    {#if descError}
                      <span class="text-destructive">{descError}</span>
                    {/if}
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

                <details
                  class="mt-2 border-t pt-3"
                  bind:open={showMetadata}
                >
                  <summary
                    class="cursor-pointer text-xs text-muted-foreground hover:text-foreground select-none"
                  >
                    Show editor metadata
                  </summary>
                  <dl
                    class="grid grid-cols-[max-content_1fr] gap-x-3.5 gap-y-1.5 mt-2.5 text-xs"
                  >
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

            <Tabs.Content value="lighting" class="mt-0">
              <div class="px-1 py-2 flex flex-col gap-4">
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
                    Changing this updates the in-editor preview immediately
                    by reading the new SkyModel mdx. On Save, a
                    <code class="font-mono">SetSkyModel(...)</code> call is
                    written into <code class="font-mono">war3map.j</code> or
                    <code class="font-mono">war3map.lua</code> so the chosen
                    sky also shows in-game. Pick <em>None</em> to drop the
                    dome and use only the editor's gradient backdrop.
                  </p>
                  {#if skyChanged}
                    <p class="text-xs text-amber-500">
                      Unsaved change — applies in editor now; commits to
                      script on Save.
                    </p>
                  {/if}
                </div>
              </div>
            </Tabs.Content>

            {#each TABS.filter((t) => t.id !== 'description' && t.id !== 'lighting') as t (t.id)}
              <Tabs.Content value={t.id} class="mt-0">
                <div
                  class="text-center text-muted-foreground py-16 text-sm flex flex-col gap-1.5"
                >
                  <p><strong class="text-foreground">{t.label}</strong></p>
                  <p>Coming soon.</p>
                  <p
                    class="text-muted-foreground/70 text-xs max-w-sm mx-auto leading-relaxed mt-3"
                  >
                    This tab is a placeholder while the Map Info Editor
                    framework is being built out. The Description tab is fully
                    wired; other tabs will land in follow-up commits per the
                    design doc.
                  </p>
                </div>
              </Tabs.Content>
            {/each}
          {/if}
        </div>
      </Tabs.Root>
    </div>

    <Dialog.Footer
      class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0"
    >
      <Button variant="ghost" onclick={onCancel} disabled={applying}>
        Cancel
      </Button>
      <Button
        onclick={() => applyDiff(true)}
        disabled={hasErrors || applying || loading}
      >
        OK
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
