<script lang="ts">
  // Modal picker for *code-typed ECA parameters (unitcode, itemcode,
  // abilcode, buffcode, destructablecode, doodadcode, upgradecode). Reuses
  // the Object Editor's ListObjects(kind) Wails surface to enumerate
  // available rows; the user picks one and we return its FourCC ID.
  //
  // The picker is intentionally lightweight: search + flat list, no icon
  // rendering (icon thumbnails would require per-row CASC lookups; the
  // 2b1 frontend keeps that out of the picker's hot path — Phase 2b2 can
  // upgrade if users ask for it).

  import * as Dialog from '$lib/components/ui/dialog'
  import { Input } from '$lib/components/ui/input'
  import { Button } from '$lib/components/ui/button'
  import { ListObjects, type ObjectKind } from './object-editor-bindings'

  let {
    open = $bindable(false),
    kind,
    currentValue = '',
    onPick,
    onClose,
  }: {
    open?: boolean
    kind: ObjectKind
    currentValue?: string
    onPick: (id: string) => void
    onClose?: () => void
  } = $props()

  type Row = { id: string; name: string }

  let rows: Row[] = $state([])
  let loaded = $state(false)
  let loading = $state(false)
  let search = $state('')

  // Load (or reload) when the picker opens with a new kind.
  let lastLoadedFor = $state<string>('')
  $effect(() => {
    if (!open) return
    if (lastLoadedFor === kind) return
    lastLoadedFor = kind
    loaded = false
    rows = []
    loading = true
    ;(async () => {
      try {
        // ListObjects returns the raw entity rows; the shape is per-kind but
        // ID + Name are consistent. We cast through any to handle the per-kind
        // type variance — the picker only needs ID + Name strings.
        const r = (await ListObjects(kind)) as unknown as Array<{ ID?: string; id?: string; Name?: string; name?: string }>
        rows = r.map((e) => ({
          id: e.ID ?? e.id ?? '',
          name: e.Name ?? e.name ?? '',
        }))
        loaded = true
      } catch (e) {
        console.error('TriggerEntityPicker ListObjects failed', e)
        rows = []
      } finally {
        loading = false
      }
    })()
  })

  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase()
    if (!q) return rows
    return rows.filter((r) => r.id.toLowerCase().includes(q) || r.name.toLowerCase().includes(q))
  })

  function commitPick(id: string) {
    onPick(id)
    open = false
    onClose?.()
  }

  function dismiss(o: boolean) {
    if (!o) onClose?.()
    open = o
  }

  function kindLabel(): string {
    return kind.charAt(0).toUpperCase() + kind.slice(1)
  }
</script>

<Dialog.Root bind:open={open} onOpenChange={dismiss}>
  <Dialog.Content class="!max-w-[620px] !w-[85vw] h-[70vh] flex flex-col p-0 gap-0">
    <Dialog.Header class="px-4 py-3 border-b border-border">
      <Dialog.Title>Pick {kindLabel()}</Dialog.Title>
      <Dialog.Description class="text-xs text-muted-foreground">
        Pick a {kind.replace(/s$/, '')} by FourCC. Current value: <code>{currentValue || '(none)'}</code>
      </Dialog.Description>
    </Dialog.Header>

    <div class="px-3 py-2 border-b border-border">
      <Input type="search" placeholder="Filter by ID or name…" bind:value={search} class="h-8 text-sm" />
      <div class="mt-1 text-xs text-muted-foreground">
        {filtered.length} of {rows.length}
      </div>
    </div>

    <div class="flex-1 overflow-y-auto tep-list">
      {#if loading}
        <div class="px-3 py-2 text-zinc-500 text-xs">Loading…</div>
      {/if}
      {#each filtered as r (r.id)}
        <button
          class="tep-row {r.id === currentValue ? 'tep-row-sel' : ''}"
          onclick={() => commitPick(r.id)}
          title={r.id + ' — ' + r.name}
        >
          <span class="tep-id">{r.id}</span>
          <span class="tep-name">{r.name}</span>
        </button>
      {/each}
      {#if !loading && filtered.length === 0}
        <div class="px-3 py-2 text-zinc-500 text-xs">(no matches)</div>
      {/if}
    </div>

    <Dialog.Footer class="px-4 py-2 border-t border-border">
      <Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<style>
  .tep-list { background: #18181b; }
  .tep-row {
    display: flex;
    gap: 10px;
    align-items: center;
    width: 100%;
    padding: 4px 12px;
    background: none;
    border: 0;
    border-bottom: 1px solid rgba(255,255,255,0.04);
    color: #d4d4d8;
    text-align: left;
    cursor: pointer;
    font-size: 13px;
  }
  .tep-row:hover { background: rgba(255, 255, 255, 0.06); }
  .tep-row-sel { background: rgba(96, 165, 250, 0.18); color: #fff; }
  .tep-id { font-family: "Consolas", "Courier New", monospace; color: #a5b4fc; min-width: 50px; }
  .tep-name { color: #e4e4e7; }
</style>
