<script lang="ts">
  // Lazy 3D thumbnail for one doodad palette entry.
  //
  // Shows the 2D editor icon immediately (cheap, cached) as a placeholder, then
  // — once the entry scrolls into view — asks the shared offscreen renderer
  // (model-thumbnail.ts) for an actual 3D render of the model and swaps it in.
  // If the model can't be rendered (no MDX / load failure) it stays on the 2D
  // icon. An IntersectionObserver gates the render so off-screen entries in a
  // long catalog never spin up a render they don't need.
  //
  // Uses onMount (not $effect) for the observer + icon fetch: it's one-time
  // imperative setup, and the props are fixed for this component's lifetime
  // (the parent {#each} keys by type_id, so a new entry = a new component).
  import { onMount } from 'svelte'
  import { loadIconURL } from './icon-loader'
  import { loadModelThumbnail } from './model-thumbnail'

  let {
    typeId,
    modelPaths,
    iconArt = '',
    reforged = false,
    alt = '',
  }: {
    // Cache identity for the rendered thumbnail (the doodad type_id).
    typeId: string
    // Ordered candidate model paths (variant → unsuffixed → other-extension).
    modelPaths: string[]
    // 2D editor icon asset path, used as the immediate placeholder + fallback.
    iconArt?: string
    reforged?: boolean
    alt?: string
  } = $props()

  let el: HTMLDivElement
  let thumbURL = $state('') // rendered 3D thumbnail (preferred once ready)
  let iconURL = $state('')  // 2D icon placeholder / fallback
  let rendering = $state(false)

  onMount(() => {
    let alive = true

    // Placeholder icon — fire immediately; icon-loader caches + dedupes.
    if (iconArt) loadIconURL(iconArt).then((u) => { if (alive) iconURL = u })

    // Defer the (relatively expensive) 3D render until the entry is near the
    // viewport. rootMargin pre-warms a screenful ahead so scrolling feels
    // populated rather than racing the renderer.
    let io: IntersectionObserver | null = null
    if (modelPaths.length) {
      io = new IntersectionObserver(
        (entries) => {
          if (!entries.some((e) => e.isIntersecting)) return
          io?.disconnect()
          io = null
          if (!alive) return
          rendering = true
          loadModelThumbnail(typeId, modelPaths, reforged)
            .then((u) => { if (alive && u) thumbURL = u })
            .finally(() => { if (alive) rendering = false })
        },
        { rootMargin: '200px' },
      )
      io.observe(el)
    }

    return () => { alive = false; io?.disconnect() }
  })
</script>

<div bind:this={el} class="thumb" class:rendering={rendering && !thumbURL}>
  {#if thumbURL}
    <img class="img" src={thumbURL} {alt} />
  {:else if iconURL}
    <img class="img icon" src={iconURL} {alt} />
  {:else}
    <span class="placeholder" aria-hidden="true"></span>
  {/if}
</div>

<style>
  .thumb {
    position: relative;
    width: 44px;
    height: 44px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 4px;
    background: color-mix(in oklch, var(--background) 70%, #000 30%);
    overflow: hidden;
  }
  .img {
    width: 100%;
    height: 100%;
    object-fit: contain;
  }
  /* 2D icons look better with a tiny inset than edge-to-edge. */
  .img.icon {
    width: 90%;
    height: 90%;
  }
  .placeholder {
    width: 100%;
    height: 100%;
    background: var(--muted);
  }
  /* Faint pulse while the 3D render is in flight and we're still on no image. */
  .thumb.rendering .placeholder {
    animation: thumb-pulse 1s ease-in-out infinite;
  }
  @keyframes thumb-pulse {
    0%, 100% { opacity: 0.6; }
    50% { opacity: 0.9; }
  }
</style>
