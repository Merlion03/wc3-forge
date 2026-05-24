// WC3-style RTS camera. Operates directly on a mdx-m3-viewer Camera.
//
// The camera orbits a pivot point in the world XY plane (the ground). The
// editor user moves the pivot (pan), changes the orbit radius (zoom), and
// changes orbit angles (rotate). Pitch is clamped so the user can't look up
// from underneath the world; yaw is unbounded.
//
// Conventions match Warcraft 3:
//   - +Y is north. yaw=0 → camera looks toward +Y.
//   - +Z is up.
//   - pitch is the angle below the horizon. 0 = looking flat across the
//     map; π/2 = straight down. WC3's default sits around 45° (≈ 0.78 rad).
//
// Input bindings (default; subject to tuning):
//   - mouse near screen edge → pan in world XY
//   - mouse wheel             → zoom (change distance)
//   - middle-mouse drag       → rotate (yaw + pitch)
//   - right-mouse drag        → also rotate (more discoverable than MMB)
//   - WASD / arrow keys       → pan (keyboard fallback)
//
// Mouse-edge panning is intentionally subtle (50px deadband from canvas
// edge) so it doesn't fight with hover-pickable units. Pan speed scales
// with distance so zoomed-out panning feels fast and zoomed-in feels slow,
// matching WC3's behavior.

const PITCH_MIN = 0.18  // ~10°, prevents the floor-eye view
const PITCH_MAX = 1.45  // ~83°, prevents flipping past straight down
const DIST_MIN = 200
const DIST_MAX = 50000
const EDGE_PAN_DEADBAND = 50          // px from canvas edge
const EDGE_PAN_SPEED = 0.5            // world-units per pixel-per-second per stud of distance ratio
const KEY_PAN_SPEED = 1.0
const WHEEL_ZOOM_STEP = 1.1
const DRAG_YAW_SENS = 0.005           // radians per pixel
const DRAG_PITCH_SENS = 0.004
const FOV = Math.PI / 3

export interface RTSCamera {
  /** Re-center on a map: set pivot to (cx, cy) on the ground plane and frame the given span. */
  frame(centerX: number, centerY: number, span: number): void
  /** Direct setter for pivot if you have a precise spot to focus on. */
  setPivot(x: number, y: number, z?: number): void
  /** Update the projection aspect ratio. Cheap no-op if unchanged. */
  setAspect(aspect: number): void
  /** Detach all input listeners. */
  dispose(): void
}

export function createCamera(canvas: HTMLCanvasElement, viewerCamera: any): RTSCamera {
  // Mutable camera state. Pivot is the ground-anchored focus point; the
  // viewer camera sits on a sphere of radius `distance` centered on pivot.
  const pivot = [0, 0, 0]
  let distance = 6000
  // Editor-style "looking down at the work" pitch. ~60° down feels right
  // for spatial inspection — close to top-down but with enough horizon to
  // see doodad/unit silhouettes. WC3's in-game cam sits around ~50°; we
  // tilt slightly steeper for editor work where you're locating things
  // more than experiencing them.
  let pitch = Math.PI / 3
  let yaw = 0
  let aspect = 1

  function applyToViewer() {
    const cp = Math.cos(pitch)
    const sp = Math.sin(pitch)
    const cy = Math.cos(yaw)
    const sy = Math.sin(yaw)
    // viewDir at (yaw=0, pitch=0) is +Y. Tilting down rotates around the local
    // right axis: viewDir = (sy*cp, cy*cp, -sp). Camera sits opposite, so:
    const ox = -sy * cp * distance
    const oy = -cy * cp * distance
    const oz = sp * distance
    const eye = [pivot[0] + ox, pivot[1] + oy, pivot[2] + oz]
    viewerCamera.perspective(FOV, aspect,
      Math.max(8, distance * 0.001),
      Math.max(50000, distance * 10))
    viewerCamera.moveToAndFace(eye, pivot, [0, 0, 1])
    viewerCamera.update()
  }

  // Pan in world XY, with the offset given relative to camera's screen axes.
  // dxScreen = +right, dyScreen = +up (intuitive). Translates so dragging the
  // map feels like it sticks under the cursor.
  function pan(dxScreen: number, dyScreen: number) {
    const cy = Math.cos(yaw)
    const sy = Math.sin(yaw)
    // Screen-right axis in world XY (yaw rotation, no Z component): (cy, sy, 0)
    // Screen-up axis in world XY (forward, projected to ground): (-sy, cy, 0)
    pivot[0] += dxScreen * cy - dyScreen * sy
    pivot[1] += dxScreen * sy + dyScreen * cy
    applyToViewer()
  }

  function zoom(factor: number) {
    distance = Math.max(DIST_MIN, Math.min(DIST_MAX, distance * factor))
    applyToViewer()
  }

  function rotate(dYaw: number, dPitch: number) {
    yaw += dYaw
    pitch = Math.max(PITCH_MIN, Math.min(PITCH_MAX, pitch + dPitch))
    applyToViewer()
  }

  // ---- Input plumbing ----

  // Edge-pan needs the latest pointer position; we update it on mousemove
  // and read it from the RAF loop. Set to null when the cursor leaves the
  // canvas so we don't keep panning when focus is elsewhere.
  let pointer: { x: number; y: number } | null = null
  const keysDown = new Set<string>()

  // MMB / RMB drag state.
  let dragging: { button: number; lastX: number; lastY: number } | null = null

  function onMouseMove(e: MouseEvent) {
    const r = canvas.getBoundingClientRect()
    pointer = { x: e.clientX - r.left, y: e.clientY - r.top }
    if (dragging) {
      const dx = e.clientX - dragging.lastX
      const dy = e.clientY - dragging.lastY
      dragging.lastX = e.clientX
      dragging.lastY = e.clientY
      // Drag yaw with horizontal motion, pitch with vertical. Negative pitch
      // sign so dragging "up" tilts camera up — matches Blender / Maya.
      rotate(-dx * DRAG_YAW_SENS, -dy * DRAG_PITCH_SENS)
    }
  }
  function onMouseLeave() { pointer = null }

  function onMouseDown(e: MouseEvent) {
    if (e.button === 1 || e.button === 2) {
      dragging = { button: e.button, lastX: e.clientX, lastY: e.clientY }
      e.preventDefault()
    }
  }
  function onMouseUp(e: MouseEvent) {
    if (dragging && e.button === dragging.button) {
      dragging = null
    }
  }
  function onContextMenu(e: MouseEvent) {
    // RMB is a rotate gesture; block the native menu.
    e.preventDefault()
  }
  function onWheel(e: WheelEvent) {
    e.preventDefault()
    zoom(e.deltaY > 0 ? WHEEL_ZOOM_STEP : 1 / WHEEL_ZOOM_STEP)
  }
  function onKeyDown(e: KeyboardEvent) { keysDown.add(e.key.toLowerCase()) }
  function onKeyUp(e: KeyboardEvent) { keysDown.delete(e.key.toLowerCase()) }

  canvas.addEventListener('mousemove', onMouseMove)
  canvas.addEventListener('mouseleave', onMouseLeave)
  canvas.addEventListener('mousedown', onMouseDown)
  window.addEventListener('mouseup', onMouseUp) // window-level so we catch release outside canvas
  canvas.addEventListener('contextmenu', onContextMenu)
  canvas.addEventListener('wheel', onWheel, { passive: false })
  window.addEventListener('keydown', onKeyDown)
  window.addEventListener('keyup', onKeyUp)

  // Per-frame: apply edge-pan + keyboard pan. Driven by RAF, throttled to
  // ~16ms by the browser; we compute pan distance in world units per frame.
  let rafId = 0
  let lastTs = performance.now()
  function tick(ts: number) {
    const dt = Math.min(0.1, (ts - lastTs) / 1000)
    lastTs = ts
    let dx = 0, dy = 0

    if (pointer) {
      const w = canvas.clientWidth
      const h = canvas.clientHeight
      if (pointer.x < EDGE_PAN_DEADBAND) dx -= 1
      if (pointer.x > w - EDGE_PAN_DEADBAND) dx += 1
      // Note: screen Y grows downward; our pan dy is screen-up positive, so
      // top edge = +dy and bottom = -dy.
      if (pointer.y < EDGE_PAN_DEADBAND) dy += 1
      if (pointer.y > h - EDGE_PAN_DEADBAND) dy -= 1
    }
    if (keysDown.has('w') || keysDown.has('arrowup'))    dy += 1
    if (keysDown.has('s') || keysDown.has('arrowdown'))  dy -= 1
    if (keysDown.has('a') || keysDown.has('arrowleft'))  dx -= 1
    if (keysDown.has('d') || keysDown.has('arrowright')) dx += 1

    if (dx !== 0 || dy !== 0) {
      // Speed scales with distance so panning feels constant in screen-space.
      // Edge pan and keyboard pan share the same speed knob for now; split
      // if the feel needs to differ.
      const speed = (keysDown.size > 0 ? KEY_PAN_SPEED : EDGE_PAN_SPEED) * distance
      pan(dx * speed * dt, dy * speed * dt)
    }
    rafId = requestAnimationFrame(tick)
  }
  rafId = requestAnimationFrame(tick)

  applyToViewer() // initial state

  return {
    frame(cx: number, cy: number, mapSpan: number) {
      pivot[0] = cx
      pivot[1] = cy
      pivot[2] = 0
      // Editor default: see a portion of the map by default rather than the
      // whole thing. WC3 maps run from tiny (~6000 studs) to huge (~30000+
      // studs). Fitting the full span makes everything 2-pixel-sized on
      // large maps; fitting a fixed slice makes tiny maps fit-to-screen
      // automatically. Compromise: target a "default view width" that's
      // a fraction of the map but clamped to a usable minimum.
      const viewedSpan = Math.max(8000, mapSpan * 0.45)
      // half-span / tan(FOV/2) = head-on fit distance; 1/cos(pitch) factor
      // because tilting down compresses ground coverage in screen-Y, so
      // we need more distance to fit the same ground span vertically.
      distance = Math.max(DIST_MIN, Math.min(DIST_MAX,
        (viewedSpan * 0.55) / Math.tan(FOV / 2) / Math.max(0.4, Math.cos(pitch))))
      applyToViewer()
    },
    setPivot(x: number, y: number, z = 0) {
      pivot[0] = x
      pivot[1] = y
      pivot[2] = z
      applyToViewer()
    },
    setAspect(a: number) {
      if (!isFinite(a) || a <= 0) return
      if (Math.abs(a - aspect) < 1e-3) return
      aspect = a
      applyToViewer()
    },
    dispose() {
      cancelAnimationFrame(rafId)
      canvas.removeEventListener('mousemove', onMouseMove)
      canvas.removeEventListener('mouseleave', onMouseLeave)
      canvas.removeEventListener('mousedown', onMouseDown)
      window.removeEventListener('mouseup', onMouseUp)
      canvas.removeEventListener('contextmenu', onContextMenu)
      canvas.removeEventListener('wheel', onWheel)
      window.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('keyup', onKeyUp)
    },
  }
}
