<script lang="ts">
  // Top-right view-orbit gizmo. Blender-style gnomon: three coloured axes
  // (X red, Y green, Z blue) with labelled balls at +/- ends. The widget
  // projects the world axes through the camera's current yaw + pitch so it
  // rotates live with the viewport. Interactions:
  //   - drag empty space inside the widget  → orbit the camera (yaw + pitch)
  //   - click an axis ball                  → snap camera to look down that axis
  //   - double-click anywhere               → reset to editor default
  //
  // The widget polls camera.getOrbit() every animation frame so it stays
  // synced with MMB-drag and other inputs without an explicit subscribe API.

  import { onMount } from 'svelte'
  import type { RTSCamera } from './camera'

  let { camera }: { camera: RTSCamera | null } = $props()

  const SIZE = 90                 // widget pixel size (square)
  const CENTER = SIZE / 2
  const RADIUS = 30                // axis length in widget pixels at unit projection
  const BALL_R = 7                 // axis-end ball radius
  const DRAG_YAW_SENS = 0.012      // radians per pixel of horizontal drag (tighter than canvas — easier control in small widget)
  const DRAG_PITCH_SENS = 0.009

  // Current orbit, polled from the camera each RAF tick. Drives reactive SVG.
  let yaw = $state(0)
  let pitch = $state(Math.PI / 3)
  // Hover state — controls overall opacity.
  let hover = $state(false)
  // Drag state. We capture startYaw/Pitch + startMouse and recompute absolute
  // values on each move, rather than accumulating, so even if a frame is
  // skipped the orbit stays correct.
  let drag: { startX: number; startY: number; startYaw: number; startPitch: number } | null = null

  onMount(() => {
    let rafId = requestAnimationFrame(tick)
    function tick() {
      rafId = requestAnimationFrame(tick)
      if (!camera) return
      const o = camera.getOrbit()
      // Only assign when value actually changes — avoids superfluous Svelte
      // rerenders when the camera is idle.
      if (o.yaw !== yaw) yaw = o.yaw
      if (o.pitch !== pitch) pitch = o.pitch
    }
    return () => cancelAnimationFrame(rafId)
  })

  // Project a world-axis vector into the widget's 2D plane + a depth value
  // for painter's-algorithm sorting.
  //
  // Camera basis (derived to match camera.ts::applyToViewer):
  //   view   = ( sin(yaw)*cos(pitch),  cos(yaw)*cos(pitch), -sin(pitch))
  //   right  = ( cos(yaw),            -sin(yaw),             0       )
  //   up     = ( sin(yaw)*sin(pitch),  cos(yaw)*sin(pitch),  cos(pitch))
  //
  // Sanity checks:
  //   yaw=0, pitch=π/2 (top-down) → up = (0,1,0): world +Y points to top of screen. ✓
  //   yaw=0, pitch=0   (horizon)  → up = (0,0,1): world +Z points to top of screen. ✓
  function project(wx: number, wy: number, wz: number): { x: number; y: number; depth: number } {
    const cy = Math.cos(yaw),  sy = Math.sin(yaw)
    const cp = Math.cos(pitch), sp = Math.sin(pitch)
    const rx = cy,          ry = -sy,        rz = 0
    const ux = sy * sp,     uy = cy * sp,    uz = cp
    const vx = sy * cp,     vy = cy * cp,    vz = -sp
    const sxLocal = wx * rx + wy * ry + wz * rz
    const syLocal = wx * ux + wy * uy + wz * uz
    const depthLocal = wx * vx + wy * vy + wz * vz
    // SVG Y grows downward — flip screen-up so +up-axis appears toward the top.
    return {
      x: CENTER + sxLocal * RADIUS,
      y: CENTER - syLocal * RADIUS,
      depth: depthLocal,
    }
  }

  type Axis = { id: string; color: string; label: string; tip: { x: number; y: number; depth: number }; world: [number, number, number] }
  let axes = $derived.by((): Axis[] => {
    // Recompute whenever yaw/pitch change (Svelte 5 reactive deps).
    void yaw; void pitch
    const X_COLOR = '#e84a4a', Y_COLOR = '#4ade80', Z_COLOR = '#5b8cff'
    return [
      { id: '+x', color: X_COLOR, label: 'X', tip: project( 1,  0,  0), world: [ 1,  0,  0] },
      { id: '-x', color: X_COLOR, label: '',  tip: project(-1,  0,  0), world: [-1,  0,  0] },
      { id: '+y', color: Y_COLOR, label: 'Y', tip: project( 0,  1,  0), world: [ 0,  1,  0] },
      { id: '-y', color: Y_COLOR, label: '',  tip: project( 0, -1,  0), world: [ 0, -1,  0] },
      { id: '+z', color: Z_COLOR, label: 'Z', tip: project( 0,  0,  1), world: [ 0,  0,  1] },
      { id: '-z', color: Z_COLOR, label: '',  tip: project( 0,  0, -1), world: [ 0,  0, -1] },
    ]
  })

  // Painter's algorithm: balls farther from the camera get rendered FIRST so
  // closer balls overlap them. Depth in the projection is "along view dir"
  // — more negative depth = closer to camera. Sort ascending so closest is last.
  let axesSorted = $derived(axes.slice().sort((a, b) => a.tip.depth - b.tip.depth))

  // Snap-to-axis: clicking an axis ball aligns the camera so it looks along
  // that world axis from the opposite side. e.g. clicking +Z balls puts the
  // camera straight above looking down at the map (pitch → π/2, yaw → 0).
  // For +X / -X / +Y / -Y, the camera moves around the horizon and looks
  // horizontally toward the pivot (pitch → 0).
  function snapToAxis(world: [number, number, number]) {
    if (!camera) return
    const [wx, wy, wz] = world
    if (Math.abs(wz) > 0.5) {
      // Z-aligned snap. Looking down (+Z ball above) or up (-Z ball below).
      camera.setOrbit(yaw, wz > 0 ? Math.PI / 2 : 0)
      return
    }
    // Horizontal axes. yaw is measured so that yaw=0 points camera at +Y;
    // we want the camera to look from the *opposite* side of the axis ball
    // back toward the pivot. So if the ball is at +X (world), the camera
    // sits on -X looking toward +X — yaw = -π/2. The formula matches
    // viewer.applyToViewer's viewDir = (sin(yaw)*cos(pitch), cos(yaw)*cos(pitch), -sin(pitch)).
    // For pitch=0, viewDir reduces to (sin(yaw), cos(yaw), 0) — that's the
    // unit vector pointing along the world axis we want to LOOK at.
    const targetYaw = Math.atan2(wx, wy)
    camera.setOrbit(targetYaw, 0.05) // tiny pitch so the ground stays visible
  }

  // Drag-to-orbit. We attach the move/up listeners on window (not the widget)
  // so a fast drag that briefly leaves the widget doesn't strand the drag.
  function onPointerDown(e: PointerEvent) {
    // Only left mouse + only on the gizmo background; clicks on axis balls
    // are handled by their own onclick. e.target check is omitted because
    // pointer events on <svg> children bubble here, and the ball-click
    // handler calls stopPropagation.
    if (e.button !== 0) return
    if (!camera) return
    drag = {
      startX: e.clientX,
      startY: e.clientY,
      startYaw: yaw,
      startPitch: pitch,
    }
    e.preventDefault()
    window.addEventListener('pointermove', onPointerMove)
    window.addEventListener('pointerup', onPointerUp)
  }
  function onPointerMove(e: PointerEvent) {
    if (!drag || !camera) return
    const dx = e.clientX - drag.startX
    const dy = e.clientY - drag.startY
    // Sign on dy matches camera.ts's MMB-orbit (dragging down → pitch up
    // toward top-down). dx is direct yaw mapping.
    camera.setOrbit(
      drag.startYaw + dx * DRAG_YAW_SENS,
      drag.startPitch - dy * DRAG_PITCH_SENS,
    )
  }
  function onPointerUp() {
    drag = null
    window.removeEventListener('pointermove', onPointerMove)
    window.removeEventListener('pointerup', onPointerUp)
  }

  function onDoubleClick(e: MouseEvent) {
    e.preventDefault()
    camera?.resetOrbit()
  }
</script>

<!--
  Top-right anchored overlay. .viewport in App.svelte is position:relative
  so the absolute positioning here anchors to the viewport corner. Same
  pattern as Minimap.svelte (bottom-right). The widget is small + semi-
  transparent until hovered; pointer-events:auto so it captures clicks
  without stealing focus from the canvas.
-->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  class="camera-orbit-gizmo"
  style="--size: {SIZE}px;"
  class:hover
  onpointerenter={() => (hover = true)}
  onpointerleave={() => (hover = false)}
  onpointerdown={onPointerDown}
  ondblclick={onDoubleClick}
  title="Drag to orbit · click axis to snap · double-click to reset"
>
  <svg width={SIZE} height={SIZE} viewBox="0 0 {SIZE} {SIZE}" aria-hidden="true">
    <!-- Axis lines from center to each tip. Drawn before balls so balls
         occlude the line at the far ball's depth. Negative-end lines are
         drawn dashed so the user can tell front from back at a glance. -->
    {#each axes as a (a.id)}
      <line
        x1={CENTER} y1={CENTER}
        x2={a.tip.x} y2={a.tip.y}
        stroke={a.color}
        stroke-width="2"
        stroke-linecap="round"
        opacity={a.id.startsWith('-') ? 0.45 : 0.95}
        stroke-dasharray={a.id.startsWith('-') ? '2 3' : 'none'}
      />
    {/each}
    <!-- Center disc — visual anchor, also catches the double-click reset. -->
    <circle cx={CENTER} cy={CENTER} r="4" fill="#dcdce0" opacity="0.85" />
    <!-- Axis balls in depth-sorted order. Each ball is its own pointer
         target with a snap-to-axis click. cursor:pointer hint comes from
         the .ball CSS class. -->
    {#each axesSorted as a (a.id)}
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <g
        class="ball"
        role="button"
        tabindex="-1"
        aria-label="Snap view to {a.id} axis"
        onclick={(e) => { e.stopPropagation(); snapToAxis(a.world) }}
        onpointerdown={(e) => { e.stopPropagation() }}
      >
        <circle
          cx={a.tip.x} cy={a.tip.y} r={BALL_R}
          fill={a.id.startsWith('-') ? '#1a1a20' : a.color}
          stroke={a.color}
          stroke-width="1.5"
          opacity={a.id.startsWith('-') ? 0.75 : 1}
        />
        {#if a.label}
          <text
            x={a.tip.x} y={a.tip.y + 3}
            text-anchor="middle"
            font-size="9"
            font-family="ui-sans-serif, system-ui, sans-serif"
            font-weight="700"
            fill="#fff"
            pointer-events="none"
          >{a.label}</text>
        {/if}
      </g>
    {/each}
  </svg>
</div>

<style>
  .camera-orbit-gizmo {
    position: absolute;
    top: 8px;
    right: 8px;
    width: var(--size);
    height: var(--size);
    z-index: 20;
    opacity: 0.55;
    transition: opacity 120ms ease-out;
    cursor: grab;
    pointer-events: auto;
    /* Soft circular halo behind the gnomon so axes stay readable against
       both bright (snow) and dark (lava) terrain. */
    background: radial-gradient(circle at center, rgba(20, 20, 25, 0.55) 0%, rgba(20, 20, 25, 0.15) 70%, transparent 100%);
    border-radius: 50%;
    user-select: none;
  }
  .camera-orbit-gizmo.hover {
    opacity: 0.95;
  }
  .camera-orbit-gizmo:active {
    cursor: grabbing;
  }
  .ball {
    cursor: pointer;
  }
</style>
