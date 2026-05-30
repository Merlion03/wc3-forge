<script lang="ts">
  // Modal dialog for "Import 3D Model" — convert an external mesh file
  // (.obj / .gltf / .glb / .stl) into a WC3 .mdx (+ .blp textures) and import
  // it into the currently-loaded map's archive.
  //
  // Flow: the user first picks conversion options (uniform scale, up-axis,
  // flip-V), then clicks Import. ConvertAndImportModel opens the NATIVE OS
  // file dialog (so the .obj/.gltf/.glb/.stl is chosen AFTER confirming
  // options), converts + imports, and returns the in-archive ModelPath plus
  // any texture paths and warnings. On success we surface the warnings via the
  // app's toast mechanism and embed a live AssetPreview of the freshly-imported
  // model so the user sees it immediately.
  //
  // An empty ModelPath in the result means the user cancelled the native file
  // dialog — treat as a no-op (no error toast, dialog stays open with options).
  //
  // ConvertAndImportModel is a Wails-bound Go method added in parallel; its TS
  // binding regenerates on the next `wails build`. If svelte-check runs before
  // that, the only expected error here is the missing-binding import below.

  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import { Label } from '$lib/components/ui/label'
  import { Checkbox } from '$lib/components/ui/checkbox'
  import { ConvertAndImportModel } from '../wailsjs/go/main/App.js'
  import AssetPreview from './AssetPreview.svelte'
  import { showToast } from './toast'

  // Mirror the SwapTilesetDialog / ConvertToLuaDialog binding convention:
  //   - `open` is bindable so App.svelte controls visibility.
  //   - onClose fires on the open→closed transition.
  //   - onImported fires once after a successful (non-cancelled) import so the
  //     caller can refresh assets so the new file is visible.
  let {
    open = $bindable(false),
    reforged = false,
    onClose,
    onImported,
  }: {
    open?: boolean
    reforged?: boolean
    onClose?: () => void
    onImported?: () => void | Promise<void>
  } = $props()

  // Conversion options. Defaults match the backend contract:
  //   - Scale 1 (positive float, uniform).
  //   - Up-axis "y" — most OBJ / glTF exports are Y-up.
  //   - Flip-V on — flips the V texture coordinate (common between exporters
  //     and WC3's convention).
  let scale: number = $state(1)
  let upAxis: 'y' | 'z' | 'auto' = $state('y')
  let flipV: boolean = $state(true)

  let importing: boolean = $state(false)

  // Result of the last successful import. modelPath drives the embedded
  // AssetPreview + the "how to use it" hint. Cleared when the dialog reopens.
  let importedModelPath: string = $state('')
  let importedTexturePaths: string[] = $state([])

  // Reset transient result state on each open so a prior import's preview /
  // hint doesn't linger when the dialog is reopened. Mirrors the
  // open-transition effect pattern used by SwapTilesetDialog.
  let lastOpen = $state(false)
  $effect(() => {
    if (open && !lastOpen) {
      lastOpen = true
      importedModelPath = ''
      importedTexturePaths = []
    } else if (!open && lastOpen) {
      lastOpen = false
      onClose?.()
    }
  })

  // Guard the numeric input: Scale must be a positive float. Disable Import
  // when it isn't (e.g. the user cleared the field → NaN, or typed 0 / a
  // negative).
  let scaleValid: boolean = $derived(
    Number.isFinite(scale) && scale > 0,
  )

  async function doImport() {
    if (importing || !scaleValid) return
    importing = true
    try {
      const result = await ConvertAndImportModel({
        scale: scale,
        upAxis: upAxis,
        flipV: flipV,
      })
      // Empty modelPath ⇒ the user cancelled the native file dialog. No-op:
      // leave the dialog open on the options view, no error toast.
      if (!result || !result.modelPath) {
        return
      }
      importedModelPath = result.modelPath
      importedTexturePaths = result.texturePaths ?? []
      // Surface each backend warning (non-fatal conversion notes) as a toast.
      for (const w of result.warnings ?? []) {
        showToast(w, 'warning')
      }
      showToast(`Imported ${result.modelPath}`, 'success')
      // Let the caller refresh assets so the new in-archive file resolves.
      await onImported?.()
    } catch (e) {
      showToast('Import failed: ' + String(e), 'error')
    } finally {
      importing = false
    }
  }

  function onCancel() {
    open = false
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content
    class="w-[min(720px,calc(100%-2rem))] max-w-[min(720px,calc(100%-2rem))] sm:max-w-[min(720px,calc(100%-2rem))] gap-0 p-0 overflow-hidden"
  >
    <Dialog.Header class="px-4 py-3 border-b">
      <Dialog.Title>Import 3D Model</Dialog.Title>
      <Dialog.Description class="sr-only">
        Convert an external .obj / .gltf / .glb / .stl mesh into a WC3 .mdx and
        import it into the current map.
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex flex-col gap-4 p-5 max-h-[calc(100vh-12rem)] overflow-y-auto">
      <p class="text-sm text-muted-foreground">
        Set conversion options, then click Import — you'll pick the source mesh
        (<code class="text-xs bg-muted px-1 py-0.5 rounded">.obj</code>,
        <code class="text-xs bg-muted px-1 py-0.5 rounded">.gltf</code>,
        <code class="text-xs bg-muted px-1 py-0.5 rounded">.glb</code>,
        <code class="text-xs bg-muted px-1 py-0.5 rounded">.stl</code>) in the
        next dialog. The mesh is converted to a WC3
        <code class="text-xs bg-muted px-1 py-0.5 rounded">.mdx</code> (plus any
        textures) and imported into the current map.
      </p>

      <div class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-3 items-center">
        <Label for="import-scale" class="shrink-0">Scale</Label>
        <input
          id="import-scale"
          type="number"
          min="0"
          step="0.1"
          class="flex h-9 w-32 rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 {scaleValid ? '' : 'border-destructive'}"
          bind:value={scale}
          disabled={importing}
        />

        <Label for="import-upaxis" class="shrink-0">Up axis</Label>
        <select
          id="import-upaxis"
          class="flex h-9 w-40 rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
          bind:value={upAxis}
          disabled={importing}
        >
          <option value="y">Y-up</option>
          <option value="z">Z-up</option>
          <option value="auto">Auto</option>
        </select>

        <span class="shrink-0 text-sm">Flip V</span>
        <label class="flex items-center gap-2 cursor-pointer">
          <Checkbox bind:checked={flipV} disabled={importing} />
          <span class="text-xs text-muted-foreground">
            Flip the V texture coordinate (recommended for most exporters).
          </span>
        </label>
      </div>

      {#if !scaleValid}
        <p class="text-xs text-destructive">Scale must be a positive number.</p>
      {/if}

      {#if importedModelPath}
        <section class="flex flex-col gap-2 border-t pt-4">
          <h3 class="text-sm font-semibold">Imported model</h3>
          <AssetPreview modelPath={importedModelPath} {reforged} />
          <p class="text-xs text-muted-foreground">
            Imported as
            <code class="text-xs bg-muted px-1 py-0.5 rounded break-all">{importedModelPath}</code>.
            To use it, open the <strong>Object Editor</strong>, select a unit /
            doodad / destructable, and set its
            <strong>Art - Model File</strong> field to this path.
          </p>
          {#if importedTexturePaths.length > 0}
            <p class="text-xs text-muted-foreground">
              Textures: {importedTexturePaths.length} file{importedTexturePaths.length === 1 ? '' : 's'} imported.
            </p>
          {/if}
        </section>
      {/if}
    </div>

    <Dialog.Footer class="px-4 py-3 border-t bg-muted/30 rounded-none mx-0 mb-0">
      <Button variant="ghost" onclick={onCancel} disabled={importing}>
        {importedModelPath ? 'Close' : 'Cancel'}
      </Button>
      <Button onclick={doImport} disabled={importing || !scaleValid}>
        {importing ? 'Importing…' : importedModelPath ? 'Import another…' : 'Import…'}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
