<script lang="ts">
  // Drag-resize splitter handle. Defaults to the original "horizontal bar that
  // drags vertically" mode (between stacked sections in a column flexbox) and
  // also supports a "vertical bar that drags horizontally" mode (between
  // side-by-side sections in a row layout, e.g. between the viewport and the
  // right column).
  //
  // The component is purely the gesture surface — the parent owns the pixel
  // sizes and decides how to apportion the delta.
  //
  // Design choices:
  //  - 4px thick strip; visible 1px line via inset background, expands on hover
  //    so the drag affordance is obvious-but-not-loud (matches VS Code's
  //    sidebar-section dividers).
  //  - cursor: ns-resize / ew-resize on the strip + globally during drag (so
  //    the cursor doesn't flicker when the pointer briefly leaves the 4px
  //    strip while dragging fast).
  //  - PointerEvents (not Mouse) — captures the pointer so move/up fire even
  //    if the cursor leaves the window. setPointerCapture is critical: without
  //    it, Wails/WebView2 stops delivering pointer events outside the strip
  //    bounds when the drag goes fast.
  //
  // Direction prop:
  //  - 'horizontal' (default): the SEPARATOR line is horizontal, drag axis is
  //    vertical (ns-resize). Callback receives dy.
  //  - 'vertical': the separator line is vertical, drag axis is horizontal
  //    (ew-resize). Callback receives dx.

  export let onDrag: (delta: number) => void
  // Optional callback fired on drag-end so consumers can persist final sizes
  // (e.g. snap-to-min, save to localStorage). Not used yet but cheap to plumb.
  export let onDragEnd: (() => void) | undefined = undefined
  export let direction: 'horizontal' | 'vertical' = 'horizontal'

  let dragging = false
  let lastCoord = 0

  $: cursor = direction === 'vertical' ? 'ew-resize' : 'ns-resize'

  function onPointerDown(e: PointerEvent) {
    if (e.button !== 0) return
    dragging = true
    lastCoord = direction === 'vertical' ? e.clientX : e.clientY
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
    // Lock cursor + disable text-selection during drag so the user doesn't
    // accidentally select panel text while dragging. Cleared on pointerup.
    document.body.style.cursor = cursor
    document.body.style.userSelect = 'none'
    e.preventDefault()
  }
  function onPointerMove(e: PointerEvent) {
    if (!dragging) return
    const cur = direction === 'vertical' ? e.clientX : e.clientY
    const d = cur - lastCoord
    lastCoord = cur
    if (d !== 0) onDrag(d)
  }
  function onPointerUp(e: PointerEvent) {
    if (!dragging) return
    dragging = false
    try { (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId) } catch {}
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
    onDragEnd?.()
  }
</script>

<div class="splitter"
     class:dragging
     class:vertical={direction === 'vertical'}
     role="separator"
     aria-orientation={direction}
     on:pointerdown={onPointerDown}
     on:pointermove={onPointerMove}
     on:pointerup={onPointerUp}
     on:pointercancel={onPointerUp}>
  <div class="line"></div>
</div>

<style>
  .splitter {
    flex: 0 0 auto;
    height: 4px;
    cursor: ns-resize;
    background: transparent;
    position: relative;
    z-index: 2;
  }
  .splitter.vertical {
    height: auto;
    width: 4px;
    align-self: stretch;
    cursor: ew-resize;
  }
  .splitter .line {
    position: absolute;
    inset: 0;
    background: #27272a;
    opacity: 0.6;
    transition: opacity 120ms ease, background 120ms ease;
  }
  .splitter:hover .line,
  .splitter.dragging .line {
    background: #3b82f6;
    opacity: 1;
  }
</style>
