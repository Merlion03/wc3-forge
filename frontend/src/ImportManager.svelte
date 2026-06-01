<script lang="ts">
  // Modal dialog for the general Import Manager — list/add/remove/rename the
  // arbitrary files imported into the current map (registered in war3map.imp).
  // The model-specific "Import 3D Model" flow lives in ImportModelDialog.svelte;
  // this is the World-Editor-style Import Manager for ANY file: custom icons
  // (BLP), loading screens, sounds, replacement textures, etc.
  //
  // Unlike GameplayConstantsEditor (which stages a pending list and commits on
  // OK), every operation here commits IMMEDIATELY to the backend — they're
  // file-system ops on the map archive, not staged field edits. So there's no
  // Apply/OK footer: Add opens the native file picker + imports on return,
  // Remove deletes on click, Rename commits on confirm. After each mutation we
  // re-list so the table reflects the real archive state.
  //
  // All four bindings (ListImports / AddImportFile / RemoveImport /
  // RenameImport) are thin adapters over the SAME forge.Session mutators the
  // imports.* MCP verbs call, so the GUI and agent surfaces stay in lockstep.

  import { tick } from 'svelte'
  import {
    ListImports,
    AddImportFile,
    RemoveImport,
    RenameImport,
  } from '../wailsjs/go/main/App.js'
  import { showToast } from './toast'
  import * as Dialog from '$lib/components/ui/dialog'
  import { Input } from '$lib/components/ui/input'
  import { Button } from '$lib/components/ui/button'

  let {
    open = $bindable(false),
    onClose,
    onChanged,
  }: {
    open?: boolean
    onClose?: () => void
    // Fires after any successful add/remove/rename so the caller can refresh
    // assets (the new/renamed file resolves; a removed one stops resolving).
    onChanged?: () => void | Promise<void>
  } = $props()

  interface ImportRow {
    path: string
    flag: number
    size: number
    present: boolean
  }

  let rows: ImportRow[] = $state([])
  let search: string = $state('')
  let loading: boolean = $state(false)
  let busy: boolean = $state(false)

  // Inline-rename state: the path of the row being renamed + its edit buffer.
  // null = no active rename.
  let renamingPath: string | null = $state(null)
  let renameBuffer: string = $state('')

  let firstFocusableEl: HTMLInputElement | null = $state(null)

  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase()
    if (!q) return rows
    return rows.filter((r) => r.path.toLowerCase().includes(q))
  })

  async function reload() {
    loading = true
    try {
      const list = (await ListImports()) ?? []
      rows = list.map((r) => ({
        path: r.path,
        flag: r.flag,
        size: r.size,
        present: r.present,
      }))
    } catch (e) {
      showToast('Could not load imports: ' + String(e), 'error')
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
      renamingPath = null
      void reload().then(() => tick().then(() => firstFocusableEl?.focus()))
    } else if (!open && lastOpen) {
      lastOpen = false
      onClose?.()
    }
  })

  async function doAdd() {
    if (busy) return
    busy = true
    try {
      // AddImportFile opens the native file picker itself and returns an entry
      // with an empty path when the user cancels.
      const entry = await AddImportFile('')
      if (!entry || !entry.path) return // cancelled
      showToast(`Imported ${entry.path}`, 'success')
      await reload()
      await onChanged?.()
    } catch (e) {
      showToast('Import failed: ' + String(e), 'error')
    } finally {
      busy = false
    }
  }

  async function doRemove(path: string) {
    if (busy) return
    busy = true
    try {
      await RemoveImport(path)
      showToast(`Removed ${path}`, 'info')
      await reload()
      await onChanged?.()
    } catch (e) {
      showToast('Remove failed: ' + String(e), 'error')
    } finally {
      busy = false
    }
  }

  function startRename(path: string) {
    renamingPath = path
    renameBuffer = path
  }

  function cancelRename() {
    renamingPath = null
    renameBuffer = ''
  }

  async function confirmRename() {
    if (busy || renamingPath === null) return
    const oldPath = renamingPath
    const newPath = renameBuffer.trim()
    if (!newPath || newPath === oldPath) {
      cancelRename()
      return
    }
    busy = true
    try {
      await RenameImport(oldPath, newPath)
      showToast(`Renamed to ${newPath}`, 'info')
      cancelRename()
      await reload()
      await onChanged?.()
    } catch (e) {
      showToast('Rename failed: ' + String(e), 'error')
    } finally {
      busy = false
    }
  }

  function fmtSize(n: number): string {
    if (n <= 0) return '0 B'
    if (n < 1024) return n + ' B'
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
    return (n / (1024 * 1024)).toFixed(1) + ' MB'
  }

  function onCancel() {
    open = false
  }

  let dialogTitle = $derived(
    `Import Manager${rows.length ? ` — ${rows.length} file${rows.length === 1 ? '' : 's'}` : ''}`,
  )
</script>

<Dialog.Root bind:open>
  <Dialog.Content
    class="w-[min(960px,calc(100%-2rem))] max-w-[min(960px,calc(100%-2rem))] sm:max-w-[min(960px,calc(100%-2rem))] gap-0 p-0 overflow-hidden"
  >
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>{dialogTitle}</Dialog.Title>
      <Dialog.Description class="sr-only">
        Manage the files imported into the current map (war3map.imp): add a
        custom icon, loading screen, sound, or texture; remove or rename an
        existing import.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex flex-col min-h-[480px] max-h-[calc(100vh-12rem)]">
      <!-- Search + add row -->
      <div class="px-4 py-3 border-b flex items-center gap-3">
        <Input
          type="text"
          placeholder="Search by path (e.g. war3mapImported\BTN…)…"
          bind:value={search}
          bind:ref={firstFocusableEl}
          class="max-w-sm"
        />
        <span class="text-xs text-muted-foreground">
          {filtered.length} / {rows.length}
        </span>
        <div class="flex-1"></div>
        <Button size="sm" onclick={doAdd} disabled={busy}>Add file…</Button>
      </div>

      <!-- Scrollable table -->
      <div class="flex-1 overflow-y-auto">
        {#if loading}
          <div class="text-center text-muted-foreground py-16 text-sm">
            Loading…
          </div>
        {:else if rows.length === 0}
          <div
            class="text-center text-muted-foreground py-16 text-sm flex flex-col gap-2 px-6"
          >
            <p><strong class="text-foreground">No imported files.</strong></p>
            <p class="max-w-md mx-auto leading-relaxed">
              Click <strong>Add file…</strong> to import a custom icon, loading
              screen, sound, or texture into this map. Imported files are
              registered in <code class="font-mono">war3map.imp</code> and
              persist on save.
            </p>
          </div>
        {:else}
          <table class="w-full text-sm">
            <thead
              class="sticky top-0 bg-background border-b text-xs text-muted-foreground"
            >
              <tr>
                <th class="text-left font-medium px-4 py-2">Path</th>
                <th class="text-right font-medium px-3 py-2 w-24">Size</th>
                <th class="px-2 py-2 w-28"></th>
              </tr>
            </thead>
            <tbody>
              {#each filtered as r (r.path)}
                <tr class="border-b last:border-b-0 hover:bg-muted/30">
                  <td class="px-4 py-1.5 font-mono text-xs break-all">
                    {#if renamingPath === r.path}
                      <Input
                        type="text"
                        bind:value={renameBuffer}
                        onkeydown={(e) => {
                          if (e.key === 'Enter') {
                            e.preventDefault()
                            void confirmRename()
                          } else if (e.key === 'Escape') {
                            e.preventDefault()
                            cancelRename()
                          }
                        }}
                        class="h-7 font-mono text-xs"
                      />
                    {:else}
                      <span class={r.present ? '' : 'text-destructive'}>
                        {r.path}
                      </span>
                      {#if !r.present}
                        <span
                          class="ml-2 text-[10px] uppercase tracking-wide text-destructive"
                          title="Listed in war3map.imp but the backing file is missing"
                        >
                          missing
                        </span>
                      {/if}
                    {/if}
                  </td>
                  <td class="px-3 py-1.5 text-right text-xs text-muted-foreground">
                    {fmtSize(r.size)}
                  </td>
                  <td class="px-2 py-1.5 text-right whitespace-nowrap">
                    {#if renamingPath === r.path}
                      <Button
                        variant="ghost"
                        size="sm"
                        onclick={() => void confirmRename()}
                        disabled={busy}
                        class="h-6 px-2 text-xs"
                      >
                        Save
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onclick={cancelRename}
                        disabled={busy}
                        class="h-6 px-2 text-xs text-muted-foreground"
                      >
                        Cancel
                      </Button>
                    {:else}
                      <Button
                        variant="ghost"
                        size="sm"
                        onclick={() => startRename(r.path)}
                        disabled={busy}
                        title="Rename / move this import"
                        class="h-6 px-2 text-xs text-muted-foreground hover:text-foreground"
                      >
                        Rename
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onclick={() => void doRemove(r.path)}
                        disabled={busy}
                        title="Remove this import"
                        class="h-6 w-6 p-0 text-muted-foreground hover:text-destructive"
                      >
                        ×
                      </Button>
                    {/if}
                  </td>
                </tr>
              {/each}
              {#if filtered.length === 0 && search}
                <tr>
                  <td
                    colspan="3"
                    class="text-center text-muted-foreground py-8 text-xs"
                  >
                    No paths match "{search}".
                  </td>
                </tr>
              {/if}
            </tbody>
          </table>
        {/if}
      </div>
    </div>

    <Dialog.Footer
      class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0"
    >
      <Button variant="ghost" onclick={onCancel} disabled={busy}>Close</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
