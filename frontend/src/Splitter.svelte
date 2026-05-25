<script lang="ts">
  // Horizontal drag-resize handle for stacked vertical sections. Sits between
  // two flex children inside a column flexbox; while dragged, calls onDrag(dy)
  // each frame with the pointer's delta since the last call. Parent owns the
  // pixel sizes and decides how to apportion the delta — this component is
  // purely the gesture surface.
  //
  // Design choices:
  //  - 4px tall strip; visible 1px centerline that brightens on hover/drag
  //    so the drag affordance is obvious-but-not-loud (matches VS Code's
  //    sidebar-section dividers).
  //  - cursor: ns-resize on the strip + globally during drag (so the cursor
  //    doesn't flicker when the pointer briefly leaves the 4px strip while
  //    dragging fast).
  //  - PointerEvents (not Mouse) — captures the pointer so move/up fire even
  //    if the cursor leaves the window. setPointerCapture is critical: without
  //    it, Wails/WebView2 stops delivering pointer events outside the strip
  //    bounds when the drag goes fast.

  interface Props {
    onDrag: (dy: number) => void;
    // Optional callback fired on drag-end so consumers can persist final sizes
    // (e.g. snap-to-min, save to localStorage). Not used yet but cheap to plumb.
    onDragEnd?: () => void;
  }

  let { onDrag, onDragEnd }: Props = $props();

  let dragging = $state(false);
  let lastY = 0;

  function handlePointerDown(e: PointerEvent) {
    if (e.button !== 0) return;
    dragging = true;
    lastY = e.clientY;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    // Lock cursor + disable text-selection during drag so the user doesn't
    // accidentally select panel text while dragging. Cleared on pointerup.
    document.body.style.cursor = 'ns-resize';
    document.body.style.userSelect = 'none';
    e.preventDefault();
  }

  function handlePointerMove(e: PointerEvent) {
    if (!dragging) return;
    const dy = e.clientY - lastY;
    lastY = e.clientY;
    if (dy !== 0) onDrag(dy);
  }

  function handlePointerUp(e: PointerEvent) {
    if (!dragging) return;
    dragging = false;
    try {
      (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
    } catch {}
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
    onDragEnd?.();
  }
</script>

<div
  class="relative z-[2] h-1 flex-none cursor-ns-resize group"
  role="separator"
  aria-orientation="horizontal"
  data-dragging={dragging ? '' : undefined}
  onpointerdown={handlePointerDown}
  onpointermove={handlePointerMove}
  onpointerup={handlePointerUp}
  onpointercancel={handlePointerUp}
>
  <div
    class="absolute inset-x-0 top-1/2 h-px -translate-y-px bg-border transition-colors group-hover:bg-foreground/40 group-data-[dragging]:bg-foreground/60"
  ></div>
</div>
