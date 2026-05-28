<script lang="ts">
  // Phase 2b2 — modal picker for placed map instances (units, destructables,
  // regions, cameras). Surfaces ListTrigger*Instances() / ListTriggerRegions()
  // / ListTriggerCameras() from the bindings shim. The user clicks a row; we
  // return the row's gg_ref string (gg_unit_*, gg_dest_*, gg_rct_*, gg_cam_*)
  // for SetParamValue to commit verbatim.

  import * as Dialog from '$lib/components/ui/dialog'
  import { Input } from '$lib/components/ui/input'
  import { Button } from '$lib/components/ui/button'
  import {
    ListTriggerUnitInstances,
    ListTriggerDestructableInstances,
    ListTriggerRegions,
    ListTriggerCameras,
    type TriggerUnitInstance,
    type TriggerDestructableInstance,
    type TriggerRegionInfo,
    type TriggerCameraInfo,
  } from './trigger-editor-bindings'

  export type InstanceKind = 'unit' | 'destructable' | 'region' | 'camera'

  let {
    open = $bindable(false),
    kind,
    onPick,
    onClose,
  }: {
    open?: boolean
    kind: InstanceKind
    onPick: (ggRef: string, label: string) => void
    onClose?: () => void
  } = $props()

  type Row = { ggRef: string; label: string; detail: string }
  let rows: Row[] = $state([])
  let loading = $state(false)
  let loadedFor = $state<string>('')
  let search = $state('')

  // Reload whenever the dialog opens for a new kind.
  $effect(() => {
    if (!open) return
    const key = kind
    if (loadedFor === key) return
    loadedFor = key
    loading = true
    rows = []
    void (async () => {
      try {
        rows = await loadKind(kind)
      } catch (e) {
        console.error('TriggerInstancePicker load failed', e)
        rows = []
      } finally {
        loading = false
      }
    })()
  })

  async function loadKind(k: InstanceKind): Promise<Row[]> {
    switch (k) {
      case 'unit': {
        const data: TriggerUnitInstance[] = await ListTriggerUnitInstances()
        return data.map((u) => ({
          ggRef: u.gg_ref,
          label: `${u.name || u.type_id} (${u.creation_number})`,
          detail: `player=${u.player} @ (${u.x.toFixed(0)}, ${u.y.toFixed(0)})`,
        }))
      }
      case 'destructable': {
        const data: TriggerDestructableInstance[] = await ListTriggerDestructableInstances()
        return data.map((d) => ({
          ggRef: d.gg_ref,
          label: `${d.name || d.type_id} (${d.creation_number})`,
          detail: `@ (${d.x.toFixed(0)}, ${d.y.toFixed(0)})`,
        }))
      }
      case 'region': {
        const data: TriggerRegionInfo[] = await ListTriggerRegions()
        return data.map((r) => ({
          ggRef: r.gg_ref,
          label: r.name,
          detail: `[${r.left.toFixed(0)}…${r.right.toFixed(0)}, ${r.bottom.toFixed(0)}…${r.top.toFixed(0)}]`,
        }))
      }
      case 'camera': {
        const data: TriggerCameraInfo[] = await ListTriggerCameras()
        return data.map((c) => ({
          ggRef: c.gg_ref,
          label: c.name,
          detail: `target=(${c.target_x.toFixed(0)}, ${c.target_y.toFixed(0)}) dist=${c.distance.toFixed(0)}`,
        }))
      }
    }
  }

  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase()
    if (!q) return rows
    return rows.filter((r) => r.label.toLowerCase().includes(q) || r.ggRef.toLowerCase().includes(q))
  })

  function commit(r: Row) {
    onPick(r.ggRef, r.label)
    open = false
    onClose?.()
  }
  function dismiss(o: boolean) {
    if (!o) onClose?.()
    open = o
  }

  function kindLabel(): string {
    switch (kind) {
      case 'unit': return 'Unit'
      case 'destructable': return 'Destructable'
      case 'region': return 'Region'
      case 'camera': return 'Camera'
    }
  }
</script>

<Dialog.Root bind:open={open} onOpenChange={dismiss}>
  <Dialog.Content class="!max-w-[620px] !w-[85vw] h-[70vh] flex flex-col p-0 gap-0">
    <Dialog.Header class="px-4 py-3 border-b border-border">
      <Dialog.Title>Pick {kindLabel()}</Dialog.Title>
      <Dialog.Description class="text-xs text-muted-foreground">
        Pick a placed {kindLabel().toLowerCase()} from the loaded map.
        {#if rows.length === 0 && !loading}
          <span class="text-amber-400">No {kindLabel().toLowerCase()}s present in this map.</span>
        {/if}
      </Dialog.Description>
    </Dialog.Header>

    <div class="px-3 py-2 border-b border-border">
      <Input type="search" placeholder="Filter by name or ref…" bind:value={search} class="h-8 text-sm" />
      <div class="mt-1 text-xs text-muted-foreground">{filtered.length} of {rows.length}</div>
    </div>

    <div class="flex-1 overflow-y-auto tip-list">
      {#if loading}
        <div class="px-3 py-2 text-zinc-500 text-xs">Loading…</div>
      {/if}
      {#each filtered as r (r.ggRef)}
        <button class="tip-row" onclick={() => commit(r)} title={r.ggRef}>
          <span class="tip-label">{r.label}</span>
          <span class="tip-detail">{r.detail}</span>
          <span class="tip-ref">{r.ggRef}</span>
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
  .tip-list { background: #18181b; }
  .tip-row {
    display: grid;
    grid-template-columns: 1fr auto;
    grid-template-rows: auto auto;
    column-gap: 12px;
    width: 100%;
    padding: 6px 12px;
    background: none;
    border: 0;
    border-bottom: 1px solid rgba(255,255,255,0.04);
    color: #d4d4d8;
    text-align: left;
    cursor: pointer;
  }
  .tip-row:hover { background: rgba(255, 255, 255, 0.06); }
  .tip-label { grid-column: 1; grid-row: 1; color: #e4e4e7; font-size: 13px; }
  .tip-detail { grid-column: 1; grid-row: 2; color: #71717a; font-size: 11px; }
  .tip-ref { grid-column: 2; grid-row: 1 / span 2; color: #a5b4fc; font-family: "Consolas", "Courier New", monospace; font-size: 11px; align-self: center; }
</style>
