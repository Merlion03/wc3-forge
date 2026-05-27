<script lang="ts">
  // Modal grid for picking a WC3 command-button icon. Open with `open=true`
  // and pass `currentValue` to pre-select the user's existing pick. On commit,
  // fires `onPick(path)` with the chosen asset path (lowercase, forward-slash,
  // .blp/.dds). On dismiss, fires `onClose()` without firing onPick.
  //
  // Performance: a real CASC install has ~5000 command-button icons. We render
  // them in an 8-column grid with viewport-based lazy mounting — only the rows
  // currently visible (plus a small over-scan) get their <img> rendered, so the
  // grid stays smooth even at 5k entries. Lazy mount is driven by a scroll
  // listener on the modal body; the underlying entries array stays in memory
  // for fast filter+seek.

  import { onMount } from 'svelte'
  import * as Dialog from '$lib/components/ui/dialog'
  import { Input } from '$lib/components/ui/input'
  import { Button } from '$lib/components/ui/button'
  import { loadIconURL } from './icon-loader'
  import { ListCommandButtons, type CommandButtonEntry } from './object-editor-bindings'

  let {
    open = $bindable(false),
    currentValue = '',
    onPick,
    onClose,
  }: {
    open?: boolean
    currentValue?: string
    onPick: (path: string) => void
    onClose?: () => void
  } = $props()

  let entries: CommandButtonEntry[] = $state([])
  let loaded = $state(false)
  let loading = $state(false)
  let search = $state('')
  let scrollY = $state(0)
  let viewportH = $state(600)
  // Tracks the body element so we can read its scrollTop directly — bind:this
  // gives us a non-reactive ref but we update scrollY via the onscroll handler.
  let body: HTMLDivElement | null = $state(null)

  // Grid metrics. ROW_H is the height of one grid row (icon + label); the
  // virtual-render math uses this to convert scrollTop → row index. COLS is
  // the fixed column count.
  const COLS = 8
  const ROW_H = 80
  const OVERSCAN_ROWS = 4

  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase()
    if (!q) return entries
    return entries.filter((e) => e.name.toLowerCase().includes(q))
  })

  // Virtual-render slice. We compute the first/last visible row index from
  // scrollY + viewportH, expand by OVERSCAN_ROWS, then slice the filtered
  // array into a flat list of items to mount + a padded spacer height before
  // it so absolute positions stay correct.
  let totalRows = $derived(Math.ceil(filtered.length / COLS))
  let firstRow = $derived(Math.max(0, Math.floor(scrollY / ROW_H) - OVERSCAN_ROWS))
  let lastRow = $derived(Math.min(totalRows, Math.ceil((scrollY + viewportH) / ROW_H) + OVERSCAN_ROWS))
  let visibleStart = $derived(firstRow * COLS)
  let visibleEnd = $derived(Math.min(filtered.length, lastRow * COLS))
  let visibleItems = $derived(filtered.slice(visibleStart, visibleEnd))
  let topPadding = $derived(firstRow * ROW_H)
  let bottomPadding = $derived((totalRows - lastRow) * ROW_H)

  // Load + cache once on first open. CASC enumeration is ~10ms server-side and
  // ~5k entries fit in a megabyte of JSON; we don't need a streaming source.
  $effect(() => {
    if (!open || loaded || loading) return
    loading = true
    ;(async () => {
      try {
        entries = await ListCommandButtons()
        loaded = true
      } catch (e) {
        console.error('ListCommandButtons failed', e)
        entries = []
      } finally {
        loading = false
      }
    })()
  })

  function commitPick(path: string) {
    onPick(path)
    closeAndNotify(false)
  }

  function closeAndNotify(o: boolean) {
    if (!o) onClose?.()
    open = o
  }

  function onBodyScroll(e: Event) {
    const el = e.currentTarget as HTMLDivElement
    scrollY = el.scrollTop
    viewportH = el.clientHeight
  }

  onMount(() => {
    if (body) viewportH = body.clientHeight
  })
</script>

<Dialog.Root bind:open={open} onOpenChange={closeAndNotify}>
  <Dialog.Content class="!max-w-[820px] !w-[90vw] h-[85vh] flex flex-col p-0 gap-0">
    <Dialog.Header class="px-4 py-3 border-b border-border">
      <Dialog.Title>Pick Icon</Dialog.Title>
      <Dialog.Description class="text-xs text-muted-foreground">
        WC3 command-button icons from CASC. Click an icon to apply it.
      </Dialog.Description>
    </Dialog.Header>
    <div class="px-4 py-2 border-b border-border">
      <Input
        type="search"
        placeholder="Filter icons by name…"
        bind:value={search}
        class="h-8 text-sm"
      />
      <div class="mt-1 text-xs text-muted-foreground">
        {filtered.length} of {entries.length} icons
      </div>
    </div>
    <div
      bind:this={body}
      onscroll={onBodyScroll}
      class="flex-1 overflow-auto px-3 py-2 ip-body"
    >
      {#if loading}
        <div class="p-3 text-muted-foreground text-sm">Loading icons from CASC…</div>
      {:else if entries.length === 0}
        <div class="p-3 text-muted-foreground text-sm">
          No icons found. CASC isn't reachable — check that the WC3 install is
          available (set WC3FORGE_WC3_PATH).
        </div>
      {:else}
        <!-- Top spacer so absolute row positions stay aligned. -->
        <div style="height: {topPadding}px"></div>
        <div class="ip-grid">
          {#each visibleItems as e (e.path)}
            <button
              type="button"
              class="ip-cell"
              class:ip-cell-active={e.path === currentValue}
              onclick={() => commitPick(e.path)}
              title={e.name + '\n' + e.path}
            >
              {#await loadIconURL(e.path) then iconURL}
                {#if iconURL}
                  <img src={iconURL} alt="" class="ip-icon" />
                {:else}
                  <span class="ip-icon ip-icon-placeholder"></span>
                {/if}
              {/await}
              <span class="ip-name truncate">{e.name}</span>
            </button>
          {/each}
        </div>
        <div style="height: {bottomPadding}px"></div>
      {/if}
    </div>
    <Dialog.Footer class="px-4 py-2 border-t border-border">
      <Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<style>
  .ip-grid {
    display: grid;
    grid-template-columns: repeat(8, 1fr);
    gap: 4px;
  }
  .ip-cell {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: flex-start;
    padding: 4px 2px;
    border: 1px solid transparent;
    border-radius: 4px;
    background: transparent;
    cursor: pointer;
    height: 76px;
    overflow: hidden;
  }
  .ip-cell:hover {
    background: rgb(var(--accent));
    border-color: rgb(var(--border));
  }
  .ip-cell-active {
    border-color: rgb(var(--primary));
    background: rgb(var(--accent));
  }
  .ip-icon {
    width: 48px;
    height: 48px;
    object-fit: cover;
    border-radius: 3px;
    flex: none;
  }
  .ip-icon-placeholder {
    background: rgb(var(--muted) / 0.4);
    display: inline-block;
  }
  .ip-name {
    font-size: 0.65rem;
    margin-top: 2px;
    width: 100%;
    text-align: center;
    color: rgb(var(--muted-foreground));
    line-height: 1.1;
  }
</style>
