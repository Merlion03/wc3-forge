<script lang="ts">
  // View menu — dropdown sibling to File menu in the header. Hosts visibility
  // toggles for doodad categories (and any future "show / hide" affordances).
  //
  // Currently shows:
  //   - Doodads → All        (checkbox; toggle every doodad instance)
  //   - Doodads → <Category> (checkbox per category present in the loaded map)
  //
  // Visibility state is OWNED by the parent (App.svelte). This component
  // dispatches `toggle` events with the affected category (or "*" for all) +
  // desired visibility. The parent flips its `doodadVisibility` Record and
  // forwards to scene.setDoodadCategoryVisible.
  //
  // The "All" checkbox is tri-state:
  //   - checked         all categories visible
  //   - unchecked       all categories hidden
  //   - indeterminate   mixed
  //
  // Sub-categories follow the order defined in App.svelte (DOODAD_CAT_ORDER)
  // — passed in via `categories` so we don't duplicate the curation here.

  import { createEventDispatcher } from 'svelte'

  export let categories: string[] = []  // present-in-map ordered list
  export let visibility: Record<string, boolean> = {}  // category → visible
  // Overlays section — non-entity layered toggles that sit on top of the 3D
  // view. New overlays slot in here as the editor grows (cell-highlight always-
  // on, path-blocker checkers, etc.). Pass current on/off via this object;
  // parent reacts to overlay-toggle events the same way it reacts to the
  // doodad-category toggles above.
  export let overlays: Record<string, boolean> = {}  // overlay-id → visible
  // Top-level toggles — flat checkboxes at the menu root (no sub-menu).
  // Use these for editor-wide modes that don't belong inside Doodads or
  // Overlays — e.g. Reforged Graphics, Agent Console. Parent supplies the
  // ordered list + current checked state; we dispatch `extra-toggle`.
  export let extras: { id: string; label: string; checked: boolean; title?: string; disabled?: boolean }[] = []

  const dispatch = createEventDispatcher<{
    toggle: { category: string; visible: boolean }
    'overlay-toggle': { overlay: string; visible: boolean }
    'extra-toggle': { id: string; checked: boolean }
  }>()

  let open = false
  let menuEl: HTMLDivElement | null = null

  function toggleMenu() { open = !open }
  function onDocClick(e: MouseEvent) {
    if (!open) return
    if (menuEl && menuEl.contains(e.target as Node)) return
    open = false
  }
  function onDocKey(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) {
      open = false
      e.stopPropagation()
    }
  }

  // Sub-menu (Doodads) hover-open. Click also toggles, for keyboard / non-hover.
  let doodadOpen = false
  function toggleDoodadSub() { doodadOpen = !doodadOpen }
  let overlaysOpen = false
  function toggleOverlaysSub() { overlaysOpen = !overlaysOpen }

  // Curated list of overlays surfaced in the menu. id is the wire key; label
  // is the human-readable name. Extend as new overlays are added.
  const OVERLAYS: { id: string; label: string; title: string }[] = [
    { id: 'pathing', label: 'Pathing', title: 'Toggle the static pathing-map overlay (red=unwalkable, blue=unflyable, yellow=unbuildable).' },
  ]
  function setOverlay(id: string, v: boolean) {
    dispatch('overlay-toggle', { overlay: id, visible: v })
  }
  function onOverlayChange(ev: Event, id: string) {
    const el = ev.currentTarget as HTMLInputElement
    setOverlay(id, el.checked)
  }
  $: overlaysOnCount = OVERLAYS.filter(o => overlays[o.id] === true).length

  function onExtraChange(ev: Event, id: string) {
    const el = ev.currentTarget as HTMLInputElement
    dispatch('extra-toggle', { id, checked: el.checked })
  }

  $: allVisibleCount = categories.filter(c => visibility[c] !== false).length
  $: allChecked = categories.length > 0 && allVisibleCount === categories.length
  $: allIndeterminate = allVisibleCount > 0 && allVisibleCount < categories.length

  function setAll(v: boolean) {
    dispatch('toggle', { category: '*', visible: v })
  }
  function setCat(c: string, v: boolean) {
    dispatch('toggle', { category: c, visible: v })
  }
  // Wrapper to keep the inline `on:change` handlers free of complex TS
  // expressions (Svelte's parser chokes on `(e.currentTarget as HTMLInput…)`
  // inside attribute values — it interprets the `<` of the generic-looking
  // type as a tag boundary).
  function onAllChange(ev: Event) {
    const el = ev.currentTarget as HTMLInputElement
    setAll(el.checked)
  }
  function onCatChange(ev: Event, cat: string) {
    const el = ev.currentTarget as HTMLInputElement
    setCat(cat, el.checked)
  }

  // Bind doc listeners while menu is open. We add/remove on `open` change
  // rather than always-on so we don't pay the cost when the menu is closed.
  $: if (open) {
    document.addEventListener('mousedown', onDocClick, true)
    document.addEventListener('keydown', onDocKey)
  } else {
    document.removeEventListener('mousedown', onDocClick, true)
    document.removeEventListener('keydown', onDocKey)
    doodadOpen = false
    overlaysOpen = false
  }
</script>

<div class="view-menu" bind:this={menuEl}>
  <button class="view-btn" class:open
          on:click={toggleMenu}
          aria-haspopup="menu"
          aria-expanded={open}
          title="View menu">
    View <span class="caret">▾</span>
  </button>
  {#if open}
    <div class="view-dropdown" role="menu">
      {#if extras.length}
        {#each extras as x (x.id)}
          <label class="check-item top-level" title={x.title ?? ''}>
            <input type="checkbox"
                   checked={x.checked}
                   disabled={x.disabled === true}
                   on:change={(e) => onExtraChange(e, x.id)} />
            <span class="lbl">{x.label}</span>
          </label>
        {/each}
        <div class="sep"></div>
      {/if}
      <button class="view-item parent"
              on:click={toggleDoodadSub}
              aria-expanded={doodadOpen}>
        <span class="chev">{doodadOpen ? '▾' : '▸'}</span>
        <span class="lbl">Doodads</span>
        <span class="meta">{allVisibleCount}/{categories.length}</span>
      </button>
      {#if doodadOpen}
        <div class="sub" role="menu">
          <label class="check-item">
            <input type="checkbox"
                   checked={allChecked}
                   indeterminate={allIndeterminate}
                   on:change={onAllChange} />
            <span class="lbl">All</span>
          </label>
          <div class="sep"></div>
          {#each categories as cat (cat)}
            <label class="check-item">
              <input type="checkbox"
                     checked={visibility[cat] !== false}
                     on:change={(e) => onCatChange(e, cat)} />
              <span class="lbl">{cat}</span>
            </label>
          {/each}
          {#if categories.length === 0}
            <div class="empty">No doodads in map</div>
          {/if}
        </div>
      {/if}
      <button class="view-item parent"
              on:click={toggleOverlaysSub}
              aria-expanded={overlaysOpen}>
        <span class="chev">{overlaysOpen ? '▾' : '▸'}</span>
        <span class="lbl">Overlays</span>
        <span class="meta">{overlaysOnCount}/{OVERLAYS.length}</span>
      </button>
      {#if overlaysOpen}
        <div class="sub" role="menu">
          {#each OVERLAYS as o (o.id)}
            <label class="check-item" title={o.title}>
              <input type="checkbox"
                     checked={overlays[o.id] === true}
                     on:change={(e) => onOverlayChange(e, o.id)} />
              <span class="lbl">{o.label}</span>
            </label>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .view-menu { position: relative; }
  button.view-btn {
    background: transparent; color: #d4d4d8; font-weight: 500;
    padding: 5px 10px; border: 1px solid transparent; border-radius: 3px;
    display: inline-flex; align-items: center; gap: 4px;
    font-size: 12px; cursor: pointer;
  }
  button.view-btn:hover, button.view-btn.open { background: #27272a; }
  .view-btn .caret { color: #71717a; font-size: 10px; }
  .view-dropdown {
    position: absolute; top: calc(100% + 4px); left: 0;
    min-width: 220px; z-index: 100;
    background: #18181b; border: 1px solid #27272a;
    box-shadow: 0 8px 24px rgba(0,0,0,0.4);
    display: flex; flex-direction: column; padding: 4px 0;
  }
  .view-item.parent {
    background: transparent; color: #e4e4e7;
    border: 0; border-radius: 0;
    padding: 6px 12px; font-size: 12px; font-weight: 400;
    text-align: left; cursor: pointer;
    display: flex; align-items: center; gap: 6px;
  }
  .view-item.parent:hover { background: #27272a; color: #fff; }
  .view-item.parent .chev { color: #71717a; font-size: 10px; width: 10px; }
  .view-item.parent .lbl { flex: 1 1 auto; }
  .view-item.parent .meta { color: #71717a; font-size: 11px; font-family: 'Cascadia Mono', Consolas, monospace; }
  .sub {
    display: flex; flex-direction: column;
    padding: 2px 0 4px;
    background: #15151a;
    border-top: 1px solid #27272a;
    border-bottom: 1px solid #27272a;
    max-height: 320px; overflow-y: auto;
  }
  .check-item {
    display: flex; align-items: center; gap: 8px;
    padding: 4px 14px 4px 24px;
    cursor: pointer;
    color: #d4d4d8; font-size: 12px;
  }
  .check-item:hover { background: #27272a; color: #fff; }
  .check-item input { margin: 0; cursor: pointer; }
  .check-item .lbl { flex: 1 1 auto; }
  .check-item.top-level { padding-left: 14px; }
  .sep { height: 1px; background: #27272a; margin: 3px 0; }
  .empty { padding: 6px 18px; color: #71717a; font-size: 11px; font-style: italic; }
</style>
