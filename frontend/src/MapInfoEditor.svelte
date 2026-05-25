<script lang="ts">
  import { run } from 'svelte/legacy';

  // Modal dialog for editing war3map.w3i metadata. v1 ships Tab 1
  // (Description) with full edit/apply/save plumbing; tabs 2–6 are
  // placeholders so the framework is validated and future agents can drop
  // content in without touching the modal skeleton.
  //
  // Open/close is controlled by the parent (App.svelte) via `open` prop.
  // On open, MapInfoGet() is called to load the full Info struct into a
  // local pending-edits store; on Apply/OK, the diff is sent through
  // MapInfoApply (so only changed fields cross the wire); on Cancel/Esc,
  // pending edits are discarded.
  //
  // Modal semantics:
  //   - Semi-transparent backdrop, click-outside acts as Cancel
  //   - Esc key acts as Cancel (treated as discard)
  //   - Focus trap inside dialog while open (Tab/Shift+Tab cycle)
  //   - OK is disabled while any field is invalid (inline red border +
  //     helper text)
  //
  // TRIGSTR handling: Option A (literal strings). The w3i Info struct
  // already carries resolved DISPLAY strings post-Session.Open; on save we
  // write them back verbatim per the existing w3i.Encode wire contract.
  // Re-introducing TRIGSTR round-trip support is a future arc (see design
  // doc §5.1 + Open Questions).

  import { createEventDispatcher, onMount, onDestroy, tick } from 'svelte'
  import { MapInfoGet, MapInfoApply } from '../wailsjs/go/main/App.js'
  import { showToast } from './toast'

  interface Props {
    open?: boolean;
  }

  let { open = $bindable(false) }: Props = $props();

  const dispatch = createEventDispatcher<{ close: void }>()

  // Tab definitions. v1 only populates 'description'; the rest render a
  // single "Coming soon" body so the tab framework is validated. Add new
  // tabs by appending to TABS and gating the matching {#if} branch in the
  // tab-body switch below.
  type TabId = 'description' | 'options' | 'loading' | 'lighting' | 'size' | 'advanced'
  interface Tab { id: TabId; label: string }
  const TABS: Tab[] = [
    { id: 'description', label: 'Description' },
    { id: 'options',     label: 'Options' },
    { id: 'loading',     label: 'Loading Screen' },
    { id: 'lighting',    label: 'Lighting & Weather' },
    { id: 'size',        label: 'Size & Camera Bounds' },
    { id: 'advanced',    label: 'Advanced' },
  ]
  let activeTab: TabId = $state('description')

  // Pending edits — the local mutable copy that the form binds to. Initialized
  // from the parsed Info on dialog open. On Apply/OK, we diff against the
  // baseline (the snapshot at the last open or last Apply) to send only the
  // changed fields. On Cancel/Esc, pending is restored from baseline (effectively
  // discarded).
  //
  // The Pending shape is intentionally a subset of Info — only the fields the
  // dialog currently surfaces. Fields parsed by the Go side but NOT in this
  // shape are preserved on the Go side because MapInfoApply only mutates keys
  // present in the updates map. So we don't have to round-trip the full Info
  // through JS to keep currently-unedited fields intact.
  interface Pending {
    name: string
    author: string
    description: string
    suggestedPlayers: string
  }
  let pending: Pending = $state(blankPending())
  let baseline: Pending = $state(blankPending())

  // Readonly metadata pulled at open. Surfaced in the dialog header (Map
  // Version on disclosure) and in the body's metadata disclosure. Not edited.
  let mapVersion: number = $state(0)
  let editorVersion: number = $state(0)
  let fileVersion: number = $state(0)
  let gameVersion: number[] = $state([0, 0, 0, 0])

  // Validation state. Per-field error message; empty string = valid. The
  // helper-text under each input renders this; the input gets a red border
  // when non-empty. OK button is disabled while any field is invalid.
  let nameError: string = $state('')
  let descError: string = $state('')
  let hasErrors = $derived(!!nameError || !!descError)

  // Live-changed: pending differs from baseline (any field). Drives the
  // Apply button's enabled state — no point Apply'ing an empty diff.
  let isChanged =
    $derived(pending.name !== baseline.name ||
    pending.author !== baseline.author ||
    pending.description !== baseline.description ||
    pending.suggestedPlayers !== baseline.suggestedPlayers)

  // Char counts. Limits per design doc / lobby behavior:
  //   - Name: 80 (lobby truncates)
  //   - Description: 512 (game UI clips longer)
  // Past the limit, the counter turns red AND the field is marked invalid;
  // the field can still grow (we don't hard-clamp the input) so the user
  // can see what they typed and trim it down.
  const NAME_MAX = 80
  const DESC_MAX = 512

  let loading: boolean = $state(false)
  let applying: boolean = $state(false)

  // Refs for focus trap + autofocus on open.
  let dialogEl: HTMLDivElement | null = $state(null)
  let firstFocusableEl: HTMLInputElement | null = $state(null)

  // Show-metadata disclosure (Game Version, File Version) — hidden by
  // default per design (it's diagnostic, not user-editable).
  let showMetadata: boolean = $state(false)

  function blankPending(): Pending {
    return { name: '', author: '', description: '', suggestedPlayers: '' }
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
      doClose()
    } finally {
      loading = false
    }
  }

  // Re-run on `open` transitioning false→true. Svelte runs $: reactives in
  // dependency order; this gate prevents double-load on re-render.
  let lastOpen = $state(false)
  run(() => {
    if (open && !lastOpen) {
      lastOpen = true
      void loadInfo().then(() => tick().then(() => firstFocusableEl?.focus()))
    } else if (!open && lastOpen) {
      lastOpen = false
    }
  });

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
  // never see a write (which would slightly muddy dirty-tracking and
  // pointlessly bloat the wire payload).
  function buildDiff(): Record<string, any> {
    const out: Record<string, any> = {}
    if (pending.name !== baseline.name)                         out.name = pending.name
    if (pending.author !== baseline.author)                     out.author = pending.author
    if (pending.description !== baseline.description)           out.description = pending.description
    if (pending.suggestedPlayers !== baseline.suggestedPlayers) out.suggestedPlayers = pending.suggestedPlayers
    return out
  }

  async function applyDiff(closeAfter: boolean) {
    if (applying) return
    if (hasErrors) return
    const diff = buildDiff()
    if (Object.keys(diff).length === 0) {
      // Nothing changed; OK still closes, Apply is a no-op (the button is
      // already disabled in this state via isChanged).
      if (closeAfter) doClose()
      return
    }
    applying = true
    try {
      await MapInfoApply(diff)
      // Pull the fresh Info back. This keeps the dialog state accurate
      // even if the Go side normalized any values (e.g. trimmed whitespace
      // — not done today but conceivable). Also updates the baseline so
      // a subsequent Apply with no further edits is a no-op.
      const info = await MapInfoGet()
      pending = {
        name: info?.Name ?? '',
        author: info?.Author ?? '',
        description: info?.Description ?? '',
        suggestedPlayers: info?.SuggestedPlayers ?? '',
      }
      baseline = { ...pending }
      validateAll()
      showToast('Map info updated', 'info')
      if (closeAfter) doClose()
    } catch (e) {
      showToast('Map info update failed: ' + String(e), 'error')
    } finally {
      applying = false
    }
  }

  function onCancel() {
    // Discard pending edits — reset to baseline, then close. The Go-side
    // Info is untouched (we never called MapInfoApply for these edits),
    // so cancel is a true no-op from the session's perspective.
    pending = { ...baseline }
    validateAll()
    doClose()
  }

  function doClose() {
    open = false
    dispatch('close')
  }

  // ----- Modal chrome: backdrop click + Esc + focus trap -----

  function onBackdropMouseDown(e: MouseEvent) {
    // Only treat clicks on the backdrop itself (not bubbled from inside
    // the dialog) as cancel.
    if (e.target === e.currentTarget) {
      onCancel()
    }
  }

  function onKeyDown(e: KeyboardEvent) {
    if (!open) return
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      onCancel()
      return
    }
    if (e.key === 'Tab') {
      // Focus trap. Collect tabbable elements inside the dialog; if focus
      // is on the first and shift-tab pressed, wrap to last (and vice versa).
      if (!dialogEl) return
      const focusables = Array.from(
        dialogEl.querySelectorAll<HTMLElement>(
          'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
        )
      ).filter(el => el.offsetParent !== null) // skip hidden
      if (focusables.length === 0) return
      const first = focusables[0]
      const last = focusables[focusables.length - 1]
      const active = document.activeElement as HTMLElement | null
      if (e.shiftKey && active === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && active === last) {
        e.preventDefault()
        first.focus()
      }
    }
  }

  onMount(() => {
    document.addEventListener('keydown', onKeyDown, true)
  })
  onDestroy(() => {
    document.removeEventListener('keydown', onKeyDown, true)
  })

  // Live header title — pulls from pending.name so the user sees their
  // edits reflected immediately (matches design doc §2.1 UX improvement).
  let dialogTitle = $derived(`Map Info — ${(pending.name || '').trim() || '(untitled)'}`)

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

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div class="modal-backdrop"
       onmousedown={onBackdropMouseDown}
       role="presentation">
    <div class="dialog"
         bind:this={dialogEl}
         role="dialog"
         aria-modal="true"
         aria-labelledby="mapinfo-title">
      <header class="dialog-header">
        <h2 id="mapinfo-title">{dialogTitle}</h2>
      </header>

      <div class="dialog-body">
        <!-- Tab strip — vertical on the left so tab labels can be longer
             without crowding the form area. -->
        <div class="tab-strip" role="tablist" aria-orientation="vertical">
          {#each TABS as t (t.id)}
            <button class="tab-btn"
                    class:active={activeTab === t.id}
                    role="tab"
                    aria-selected={activeTab === t.id}
                    onclick={() => activeTab = t.id}>
              {t.label}
            </button>
          {/each}
        </div>

        <div class="tab-body" role="tabpanel" aria-labelledby={activeTab}>
          {#if loading}
            <div class="empty">Loading…</div>
          {:else if activeTab === 'description'}
            <!-- Tab 1: Description. Per design doc §2.1. -->
            <div class="form">
              <div class="field">
                <label for="mi-name">Name</label>
                <input id="mi-name"
                       type="text"
                       maxlength={NAME_MAX * 2}
                       bind:value={pending.name}
                       oninput={() => onFieldInput('name')}
                       class:invalid={!!nameError}
                       bind:this={firstFocusableEl} />
                <div class="field-meta">
                  <span class="count" class:over={nameCountOver}>{nameCount}/{NAME_MAX}</span>
                  {#if nameError}
                    <span class="error-text">{nameError}</span>
                  {/if}
                </div>
              </div>

              <div class="field">
                <label for="mi-author">Author</label>
                <input id="mi-author"
                       type="text"
                       bind:value={pending.author} />
              </div>

              <div class="field">
                <label for="mi-desc">Description</label>
                <textarea id="mi-desc"
                          rows="6"
                          bind:value={pending.description}
                          oninput={() => onFieldInput('description')}
                          class:invalid={!!descError}></textarea>
                <div class="field-meta">
                  <span class="count" class:over={descCountOver}>{descCount}/{DESC_MAX}</span>
                  {#if descError}
                    <span class="error-text">{descError}</span>
                  {/if}
                </div>
              </div>

              <div class="field">
                <label for="mi-players">Suggested Players</label>
                <input id="mi-players"
                       type="text"
                       placeholder='e.g. "4 (2v2)" — free-form'
                       bind:value={pending.suggestedPlayers} />
              </div>

              <details class="metadata-disclosure" bind:open={showMetadata}>
                <summary>Show editor metadata</summary>
                <dl class="metadata-grid">
                  <dt>Map Version</dt>     <dd class="mono">{mapVersion}</dd>
                  <dt>Editor Version</dt>  <dd class="mono">{editorVersion}</dd>
                  <dt>File Version</dt>    <dd class="mono">{fileVersion}</dd>
                  <dt>Game Version</dt>    <dd class="mono">{gameVersionLabel(gameVersion)}</dd>
                </dl>
              </details>
            </div>
          {:else}
            <!-- Tabs 2–6 placeholder. Future agents replace this body with
                 the actual tab content per the design doc §2.2–§2.6. The
                 modal framework (header, footer, tab strip, Apply/OK/Cancel,
                 focus trap, validation) stays as-is. -->
            <div class="empty placeholder">
              <p><strong>{TABS.find(t => t.id === activeTab)?.label}</strong></p>
              <p>Coming soon.</p>
              <p class="hint">This tab is a placeholder while the Map Info Editor framework is being built out. The Description tab is fully wired; other tabs will land in follow-up commits per the design doc.</p>
            </div>
          {/if}
        </div>
      </div>

      <footer class="dialog-footer">
        <button class="btn cancel" onclick={onCancel} type="button">
          Cancel
        </button>
        <button class="btn apply"
                onclick={() => applyDiff(false)}
                type="button"
                disabled={hasErrors || !isChanged || applying || loading}>
          Apply
        </button>
        <button class="btn primary ok"
                onclick={() => applyDiff(true)}
                type="button"
                disabled={hasErrors || applying || loading}>
          OK
        </button>
      </footer>
    </div>
  </div>
{/if}

<style>
  /* Modal backdrop — fixed full-viewport, dims background. Click outside
     dialog triggers cancel (mousedown so it fires before the button blur
     focus-loss). z-index above toasts (1000) so a stuck toast doesn't bleed
     over the dialog. */
  .modal-backdrop {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.55);
    z-index: 2000;
    display: flex; align-items: center; justify-content: center;
    padding: 32px;
  }

  .dialog {
    background: #18181b;
    border: 1px solid #3f3f46;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.6);
    border-radius: 6px;
    width: min(880px, 100%);
    max-height: calc(100vh - 64px);
    display: flex; flex-direction: column;
    min-height: 420px;
    color: #e4e4e7;
    font-size: 13px;
    /* Re-trigger animation on open. */
    animation: dialog-in 140ms ease-out;
  }
  @keyframes dialog-in {
    from { opacity: 0; transform: translateY(-8px) scale(0.98); }
    to   { opacity: 1; transform: translateY(0) scale(1); }
  }

  .dialog-header {
    padding: 12px 18px;
    border-bottom: 1px solid #27272a;
    background: #1c1c1f;
    flex: 0 0 auto;
  }
  .dialog-header h2 {
    margin: 0; font-size: 14px; font-weight: 600; color: #e4e4e7;
  }

  /* Body: left tab strip + right form area. Min-height ensures placeholder
     tabs don't visually collapse the dialog. */
  .dialog-body {
    flex: 1 1 auto; display: flex;
    min-height: 0; overflow: hidden;
  }

  .tab-strip {
    flex: 0 0 auto;
    width: 180px;
    background: #16161a;
    border-right: 1px solid #27272a;
    display: flex; flex-direction: column;
    padding: 8px 0;
    overflow-y: auto;
  }
  .tab-btn {
    background: transparent;
    color: #a1a1aa;
    border: 0; border-left: 3px solid transparent;
    padding: 8px 14px;
    text-align: left;
    font-size: 12px; font-weight: 500;
    cursor: pointer;
    transition: background 120ms ease, color 120ms ease;
  }
  .tab-btn:hover { background: #1f1f23; color: #e4e4e7; }
  .tab-btn.active {
    background: #1f1f23;
    color: #e4e4e7;
    border-left-color: #2563eb;
  }

  .tab-body {
    flex: 1 1 auto;
    overflow-y: auto;
    padding: 18px 22px;
    min-width: 0;
  }

  .empty {
    text-align: center; color: #71717a;
    padding: 60px 24px; font-size: 12.5px;
  }
  .empty.placeholder p { margin: 6px 0; }
  .empty .hint {
    color: #52525b; font-size: 11.5px;
    max-width: 360px; margin: 12px auto 0;
    line-height: 1.5;
  }

  /* Tab 1 (Description) form. Each field is label-on-top with helper
     metadata (counts + error) below. */
  .form {
    display: flex; flex-direction: column; gap: 16px;
    max-width: 560px;
  }
  .field {
    display: flex; flex-direction: column; gap: 4px;
  }
  .field label {
    color: #a1a1aa; font-size: 11px; font-weight: 600;
    text-transform: uppercase; letter-spacing: 0.06em;
  }
  .field input,
  .field textarea {
    background: #0f0f12;
    color: #e4e4e7;
    border: 1px solid #3f3f46;
    border-radius: 3px;
    padding: 6px 9px;
    font-size: 13px;
    font-family: inherit;
    width: 100%;
    box-sizing: border-box;
    transition: border-color 120ms ease;
  }
  .field textarea {
    resize: vertical;
    min-height: 80px;
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    line-height: 1.4;
  }
  .field input:focus,
  .field textarea:focus {
    outline: none;
    border-color: #2563eb;
  }
  .field input.invalid,
  .field textarea.invalid {
    border-color: #b91c1c;
  }
  .field input.invalid:focus,
  .field textarea.invalid:focus {
    border-color: #dc2626;
  }
  .field-meta {
    display: flex; align-items: center; gap: 12px;
    font-size: 11px; color: #71717a;
  }
  .count {
    font-family: 'Cascadia Mono', Consolas, monospace;
  }
  .count.over { color: #f87171; font-weight: 500; }
  .error-text { color: #f87171; }

  .metadata-disclosure {
    margin-top: 8px;
    border-top: 1px solid #27272a;
    padding-top: 12px;
  }
  .metadata-disclosure summary {
    cursor: pointer;
    color: #a1a1aa; font-size: 11.5px;
    user-select: none;
  }
  .metadata-disclosure summary:hover { color: #e4e4e7; }
  .metadata-grid {
    display: grid; grid-template-columns: max-content 1fr;
    gap: 6px 14px; margin: 10px 0 0; padding: 0;
    font-size: 12px;
  }
  .metadata-grid dt {
    color: #71717a; font-size: 11px; padding-top: 1px;
  }
  .metadata-grid dd {
    margin: 0; color: #e4e4e7;
  }
  .mono { font-family: 'Cascadia Mono', Consolas, monospace; }

  /* Footer with right-aligned buttons. Cancel left, then Apply, then OK
     (primary). Matches HiveWE-style ordering most users expect; primary
     action sits in the bottom-right corner as is dialog convention. */
  .dialog-footer {
    flex: 0 0 auto;
    display: flex; justify-content: flex-end; gap: 8px;
    padding: 12px 18px;
    border-top: 1px solid #27272a;
    background: #16161a;
  }
  .btn {
    background: #2c2c30;
    color: #e4e4e7;
    border: 1px solid #3f3f46;
    border-radius: 3px;
    padding: 6px 16px;
    font-size: 12px; font-weight: 500;
    cursor: pointer;
    transition: background 120ms ease, border-color 120ms ease;
  }
  .btn:hover:not(:disabled) {
    background: #3a3a40; border-color: #52525b;
  }
  .btn:disabled {
    opacity: 0.5; cursor: not-allowed;
  }
  .btn.primary {
    background: #2563eb; border-color: #2563eb; color: #fff;
  }
  .btn.primary:hover:not(:disabled) {
    background: #1d4ed8; border-color: #1d4ed8;
  }
</style>