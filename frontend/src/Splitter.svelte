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
  //  - 4px thick strip; visible 1px centerline that brightens on hover/drag
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

  interface Props {
    onDrag: (delta: number) => void;
    // Optional callback fired on drag-end so consumers can persist final sizes
    // (e.g. snap-to-min, save to localStorage).
    onDragEnd?: () => void;
    direction?: 'horizontal' | 'vertical';
  }

  let { onDrag, onDragEnd, direction = 'horizontal' }: Props = $props();

  let dragging = $state(false);
  let lastCoord = 0;

  const cursor = $derived(direction === 'vertical' ? 'ew-resize' : 'ns-resize');

  function handlePointerDown(e: PointerEvent) {
    if (e.button !== 0) return;
    dragging = true;
    lastCoord = direction === 'vertical' ? e.clientX : e.clientY;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    // Lock cursor + disable text-selection during drag so the user doesn't
    // accidentally select panel text while dragging. Cleared on pointerup.
    document.body.style.cursor = cursor;
    document.body.style.userSelect = 'none';
    e.preventDefault();
  }

  function handlePointerMove(e: PointerEvent) {
    if (!dragging) return;
    const cur = direction === 'vertical' ? e.clientX : e.clientY;
    const d = cur - lastCoord;
    lastCoord = cur;
    if (d !== 0) onDrag(d);
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

{#if direction === 'vertical'}
  <div
    class="relative z-[2] w-1 flex-none cursor-ew-resize self-stretch group"
    role="separator"
    aria-orientation="vertical"
    data-dragging={dragging ? '' : undefined}
    onpointerdown={handlePointerDown}
    onpointermove={handlePointerMove}
    onpointerup={handlePointerUp}
    onpointercancel={handlePointerUp}
  >
    <div
      class="absolute inset-y-0 left-1/2 w-px -translate-x-px bg-border transition-colors group-hover:bg-foreground/40 group-data-[dragging]:bg-foreground/60"
    ></div>
  </div>
{:else}
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
{/if}
