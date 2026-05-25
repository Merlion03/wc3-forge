<script lang="ts">
  // Horizontal drag-resize handle for stacked vertical sections. Sits between
  // two flex children inside a column flexbox; while dragged, calls onDrag(dy)
  // each frame with the pointer's delta since the last call. Parent owns the
  // pixel sizes and decides how to apportion the delta — this component is
  // purely the gesture surface.
  //
  // Design choices:
  //  - 4px tall strip; visible 1px line via background-clip, expands on hover
  //    so the drag affordance is obvious-but-not-loud (matches VS Code's
  //    sidebar-section dividers).
  //  - cursor: ns-resize on the strip + globally during drag (so the cursor
  //    doesn't flicker when the pointer briefly leaves the 4px strip while
  //    dragging fast).
  //  - PointerEvents (not Mouse) — captures the pointer so move/up fire even
  //    if the cursor leaves the window. setPointerCapture is critical: without
  //    it, Wails/WebView2 stops delivering pointer events outside the strip
  

  // Optional callback fired on drag-end so consumers can persist final sizes
  
  interface Props {
    //    bounds when the drag goes fast.
    onDrag: (dy: number) => void;
    // (e.g. snap-to-min, save to localStorage). Not used yet but cheap to plumb.
    onDragEnd?: (() => void) | undefined;
  }

  let { onDrag, onDragEnd = undefined }: Props = $props();

  let dragging = $state(false)
  let lastY = 0

  function onPointerDown(e: PointerEvent) {
    if (e.button !== 0) return
    dragging = true
    lastY = e.clientY
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
    // Lock cursor + disable text-selection during drag so the user doesn't
    // accidentally select panel text while dragging. Cleared on pointerup.
    document.body.style.cursor = 'ns-resize'
    document.body.style.userSelect = 'none'
    e.preventDefault()
  }
  function onPointerMove(e: PointerEvent) {
    if (!dragging) return
    const dy = e.clientY - lastY
    lastY = e.clientY
    if (dy !== 0) onDrag(dy)
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
     role="separator"
     aria-orientation="horizontal"
     onpointerdown={onPointerDown}
     onpointermove={onPointerMove}
     onpointerup={onPointerUp}
     onpointercancel={onPointerUp}>
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
