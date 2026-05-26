<script lang="ts">
  // Modal dialog for editing per-map gameplay-constants overrides
  // (war3mapMisc.txt). Mirrors MapInfoEditor's shape: shadcn Dialog,
  // pending/baseline pattern, Cancel/Apply/OK footer.
  //
  // v1 scope: edit the keys CURRENTLY in war3mapMisc.txt + let the user
  // add new key=value rows. Stock-defaults visibility (the ~240 keys
  // shipped by Blizzard's MiscData.txt) is a v2 — it needs the SLK
  // metadata + WorldEditStrings parsing that wc3-forge doesn't ship yet.
  //
  // The wire contract is: GameplayConstantsGet returns the FULL current
  // override list; GameplayConstantsApply receives the FULL post-edit
  // list (snapshot replace, not a diff). The file is small (< a few
  // hundred rows in practice) so wholesale replace keeps the Go side
  // trivial. Section is always "Misc" for now — the column is kept on
  // the wire so a future tabs-by-section UI can ride on the same DTO.

  import { tick } from 'svelte'
  import {
    GameplayConstantsGet,
    GameplayConstantsApply,
  } from '../wailsjs/go/main/App.js'
  import { showToast } from './toast'
  import * as Dialog from '$lib/components/ui/dialog'
  import { Input } from '$lib/components/ui/input'
  import { Label } from '$lib/components/ui/label'
  import { Button } from '$lib/components/ui/button'

  let {
    open = $bindable(false),
    onClose,
  }: {
    open?: boolean
    onClose?: () => void
  } = $props()

  interface Row {
    section: string
    key: string
    value: string
  }

  let pending: Row[] = $state([])
  let baseline: Row[] = $state([])
  let search: string = $state('')
  let loading: boolean = $state(false)
  let applying: boolean = $state(false)

  // Pending row for the inline "add new" form. Kept separate from
  // `pending` until the user confirms — adding to `pending` on every
  // keystroke would make the diff/isChanged logic ambiguous.
  let newKey: string = $state('')
  let newValue: string = $state('')
  let addError: string = $state('')

  let firstFocusableEl: HTMLInputElement | null = $state(null)

  // Bit-exact equality check between pending and baseline. Rows are
  // simple {section, key, value} triples — comparing serialized JSON is
  // fine for ~hundreds-of-rows lists and avoids hand-writing a deep
  // compare for what's basically a value type.
  let isChanged = $derived(
    JSON.stringify(pending) !== JSON.stringify(baseline),
  )

  // Filter rows by case-insensitive substring match against key. Empty
  // search shows everything. We never filter on value because numeric
  // searches like "1.0" would match almost every row and aren't useful.
  let filteredIndexes = $derived.by(() => {
    const q = search.trim().toLowerCase()
    const out: number[] = []
    for (let i = 0; i < pending.length; i++) {
      if (!q) {
        out.push(i)
      } else if (pending[i].key.toLowerCase().includes(q)) {
        out.push(i)
      }
    }
    return out
  })

  async function loadConstants() {
    loading = true
    try {
      const rows = (await GameplayConstantsGet()) ?? []
      // Defensive copies — Wails returns a fresh array each call, but the
      // pending/baseline split needs them not to alias.
      pending = rows.map((r: Row) => ({ ...r }))
      baseline = rows.map((r: Row) => ({ ...r }))
    } catch (e) {
      showToast('Could not load gameplay constants: ' + String(e), 'error')
      open = false
    } finally {
      loading = false
    }
  }

  let lastOpen = $state(false)
  $effect(() => {
    if (open && !lastOpen) {
      lastOpen = true
      search = ''
      newKey = ''
      newValue = ''
      addError = ''
      void loadConstants().then(() =>
        tick().then(() => firstFocusableEl?.focus()),
      )
    } else if (!open && lastOpen) {
      lastOpen = false
      // Discard any pending edits so a re-open starts clean.
      pending = baseline.map((r) => ({ ...r }))
      onClose?.()
    }
  })

  function deleteRow(i: number) {
    pending = pending.filter((_, idx) => idx !== i)
  }

  function updateValue(i: number, v: string) {
    // Bind:value isn't reactive on an indexed array element in Svelte 5
    // without rebuilding the array, so we explicitly clone-and-replace.
    const next = pending.slice()
    next[i] = { ...next[i], value: v }
    pending = next
  }

  function tryAddRow() {
    addError = ''
    const k = newKey.trim()
    if (!k) {
      addError = 'Key is required.'
      return
    }
    // Reject duplicates within Misc — first wins on parse, and the
    // GameplayConstantsApply path will silently drop the dupe; better
    // to surface it here.
    if (pending.some((r) => r.section === 'Misc' && r.key === k)) {
      addError = `Key "${k}" is already in the table.`
      return
    }
    pending = [
      ...pending,
      { section: 'Misc', key: k, value: newValue },
    ]
    newKey = ''
    newValue = ''
  }

  async function applyEdits(closeAfter: boolean) {
    if (applying) return
    if (!isChanged && closeAfter) {
      open = false
      return
    }
    if (!isChanged) return
    applying = true
    try {
      await GameplayConstantsApply(pending)
      // Re-pull so the baseline reflects what's actually in memory after
      // any normalization on the Go side (trimmed keys, dropped blanks).
      const rows = (await GameplayConstantsGet()) ?? []
      pending = rows.map((r: Row) => ({ ...r }))
      baseline = rows.map((r: Row) => ({ ...r }))
      showToast('Gameplay constants updated', 'info')
      if (closeAfter) open = false
    } catch (e) {
      showToast('Gameplay constants update failed: ' + String(e), 'error')
    } finally {
      applying = false
    }
  }

  function onCancel() {
    open = false
  }

  let dialogTitle = $derived(
    `Gameplay Constants${pending.length ? ` — ${pending.length} override${pending.length === 1 ? '' : 's'}` : ''}`,
  )
</script>

<Dialog.Root bind:open>
  <Dialog.Content
    class="w-[min(960px,calc(100%-2rem))] max-w-[min(960px,calc(100%-2rem))] sm:max-w-[min(960px,calc(100%-2rem))] gap-0 p-0 overflow-hidden"
  >
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>{dialogTitle}</Dialog.Title>
      <Dialog.Description class="sr-only">
        Edit per-map gameplay-constants overrides stored in
        war3mapMisc.txt.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex flex-col min-h-[480px] max-h-[calc(100vh-12rem)]">
      <!-- Search row -->
      <div class="px-4 py-3 border-b flex items-center gap-3">
        <Input
          type="text"
          placeholder="Search by key (e.g. MaxHeroLevel)…"
          bind:value={search}
          bind:ref={firstFocusableEl}
          class="max-w-sm"
        />
        <span class="text-xs text-muted-foreground">
          {filteredIndexes.length} / {pending.length} rows
        </span>
      </div>

      <!-- Scrollable table area -->
      <div class="flex-1 overflow-y-auto">
        {#if loading}
          <div class="text-center text-muted-foreground py-16 text-sm">
            Loading…
          </div>
        {:else if pending.length === 0}
          <div
            class="text-center text-muted-foreground py-16 text-sm flex flex-col gap-2 px-6"
          >
            <p>
              <strong class="text-foreground">No overrides yet.</strong>
            </p>
            <p class="max-w-md mx-auto leading-relaxed">
              This map inherits all gameplay constants from Warcraft III's
              defaults. Add a key=value row below to override one (e.g.
              <code class="font-mono">MaxHeroLevel = 10</code>).
            </p>
          </div>
        {:else}
          <table class="w-full text-sm">
            <thead
              class="sticky top-0 bg-background border-b text-xs text-muted-foreground"
            >
              <tr>
                <th class="text-left font-medium px-4 py-2 w-[34%]">Key</th>
                <th class="text-left font-medium px-4 py-2">Value</th>
                <th class="px-2 py-2 w-10"></th>
              </tr>
            </thead>
            <tbody>
              {#each filteredIndexes as i (pending[i].section + ':' + pending[i].key)}
                <tr class="border-b last:border-b-0 hover:bg-muted/30">
                  <td class="px-4 py-1.5 font-mono text-xs">
                    {pending[i].key}
                  </td>
                  <td class="px-4 py-1.5">
                    <Input
                      type="text"
                      value={pending[i].value}
                      oninput={(e) =>
                        updateValue(
                          i,
                          (e.currentTarget as HTMLInputElement).value,
                        )}
                      class="h-7 font-mono text-xs"
                    />
                  </td>
                  <td class="px-2 py-1.5 text-center">
                    <Button
                      variant="ghost"
                      size="sm"
                      onclick={() => deleteRow(i)}
                      title="Remove this override"
                      class="h-6 w-6 p-0 text-muted-foreground hover:text-destructive"
                    >
                      ×
                    </Button>
                  </td>
                </tr>
              {/each}
              {#if filteredIndexes.length === 0 && search}
                <tr>
                  <td
                    colspan="3"
                    class="text-center text-muted-foreground py-8 text-xs"
                  >
                    No keys match "{search}".
                  </td>
                </tr>
              {/if}
            </tbody>
          </table>
        {/if}
      </div>

      <!-- Add-row form -->
      <div class="border-t bg-muted/20 px-4 py-3">
        <div class="flex items-end gap-2">
          <div class="flex flex-col gap-1 w-[34%]">
            <Label for="gc-new-key" class="text-xs">New key</Label>
            <Input
              id="gc-new-key"
              type="text"
              placeholder="e.g. MaxHeroLevel"
              bind:value={newKey}
              onkeydown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  tryAddRow()
                }
              }}
              class="h-8 font-mono text-xs"
            />
          </div>
          <div class="flex flex-col gap-1 flex-1">
            <Label for="gc-new-val" class="text-xs">Value</Label>
            <Input
              id="gc-new-val"
              type="text"
              placeholder='e.g. "10" or "1.0,2.0,1.0,…"'
              bind:value={newValue}
              onkeydown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  tryAddRow()
                }
              }}
              class="h-8 font-mono text-xs"
            />
          </div>
          <Button
            variant="outline"
            size="sm"
            onclick={tryAddRow}
            disabled={!newKey.trim()}
            class="h-8"
          >
            Add
          </Button>
        </div>
        {#if addError}
          <div class="text-xs text-destructive mt-1.5">{addError}</div>
        {/if}
      </div>
    </div>

    <Dialog.Footer
      class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0"
    >
      <Button variant="ghost" onclick={onCancel} disabled={applying}>
        Cancel
      </Button>
      <Button
        variant="outline"
        onclick={() => applyEdits(false)}
        disabled={!isChanged || applying || loading}
      >
        Apply
      </Button>
      <Button
        onclick={() => applyEdits(true)}
        disabled={applying || loading}
      >
        OK
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
