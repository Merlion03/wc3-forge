// gizmo.ts — Phase B move-only transform gizmo renderer + picker + drag math.
//
// Architecture: stateless renderer overlay, same shape as sloc-markers.ts,
// cell-highlight.ts, and path-blockers.ts. Owns its own WebGL1 shader.
// Drawn AFTER all other overlays in the RAF loop in scene-instances.ts.
//
// Phase B scope: 3-axis move arrows only (X=red, Y=green, Z=blue). Geometry
// is authored in a unit-1 reference frame and scaled by a per-frame
// `u_handleScale` uniform so the arrows subtend a fixed screen-space size
// regardless of zoom (Blender-style). Each axis is drawn as cylinder+cone.
//
// CRITICAL — shader-cache invalidation (see feedback-mdx-viewer-shader-cache):
// Always set `(viewer as any).webgl.currentShader = null` BEFORE our draw call
// so the lib's cache doesn't get confused. We reset it again AFTER as well.
//
// State restoration discipline: mirror path-blockers.ts:
//   - save DEPTH_TEST, BLEND, CULL_FACE state BEFORE our draw
//   - restore ALL of them AFTER our draw, unconditionally
//   - depthMask must be restored to true
// A missed restore = next-frame rendering breaks silently.

import { MoveUnit, MoveDoodad } from '../wailsjs/go/main/App.js'
import { flog } from './debuglog'

// ─── Axis colors (design §3.7) ────────────────────────────────────────────
const X_COLOR: [number, number, number] = [1.0, 0.2, 0.2]  // red
const Y_COLOR: [number, number, number] = [0.2, 1.0, 0.2]  // green
const Z_COLOR: [number, number, number] = [0.3, 0.4, 1.0]  // blue
const HOVER_COLOR: [number, number, number] = [1.0, 0.95, 0.3] // yellow

// ─── Screen-space size (design §1.2) ──────────────────────────────────────
// size_world = distance(camera, gizmo) * SCREEN_SIZE_CONSTANT
// Calibrated so the arrow subtends ~80 px at default zoom (~6000-stud
// distance, FOV=60°, 800-px-tall canvas).
const SCREEN_SIZE_CONSTANT = 0.115

// ─── Pick zone inflation factor (design §1.3) ──────────────────────────────
const PICK_RADIUS_INFLATE = 1.5

// ─── Geometry: cylinder stem + cone head in unit-1 local space ─────────────
// Each arrow is drawn along the +Z axis in local space. Per-axis draws apply
// a rotation matrix to reorient (Z→X for X-axis, Z→Y for Y-axis, Z→Z for Z).

const CYL_SEGS = 12
const CONE_SEGS = 12
const CYL_RADIUS = 0.05
const CONE_RADIUS = 0.10
const CYL_TOP = 0.75   // cylinder spans 0..CYL_TOP
const CONE_BASE = 0.75 // cone base at CYL_TOP
const CONE_TIP = 1.00  // cone tip at 1.0

function buildCylinder(): Float32Array {
  const verts: number[] = []
  const addTri = (ax:number,ay:number,az:number, bx:number,by:number,bz:number, cx:number,cy:number,cz:number) => {
    verts.push(ax,ay,az, bx,by,bz, cx,cy,cz)
  }
  for (let i = 0; i < CYL_SEGS; i++) {
    const a = (i / CYL_SEGS) * Math.PI * 2
    const b = ((i + 1) / CYL_SEGS) * Math.PI * 2
    const ax = Math.cos(a) * CYL_RADIUS, ay = Math.sin(a) * CYL_RADIUS
    const bx = Math.cos(b) * CYL_RADIUS, by = Math.sin(b) * CYL_RADIUS
    addTri(ax,ay,0,        bx,by,0,        bx,by,CYL_TOP)
    addTri(ax,ay,0,        bx,by,CYL_TOP,  ax,ay,CYL_TOP)
    addTri(0,0,CYL_TOP,    ax,ay,CYL_TOP,  bx,by,CYL_TOP)
  }
  return new Float32Array(verts)
}

function buildCone(): Float32Array {
  const verts: number[] = []
  const addTri = (ax:number,ay:number,az:number, bx:number,by:number,bz:number, cx:number,cy:number,cz:number) => {
    verts.push(ax,ay,az, bx,by,bz, cx,cy,cz)
  }
  for (let i = 0; i < CONE_SEGS; i++) {
    const a = (i / CONE_SEGS) * Math.PI * 2
    const b = ((i + 1) / CONE_SEGS) * Math.PI * 2
    const ax = Math.cos(a) * CONE_RADIUS, ay = Math.sin(a) * CONE_RADIUS
    const bx = Math.cos(b) * CONE_RADIUS, by = Math.sin(b) * CONE_RADIUS
    addTri(ax,ay,CONE_BASE,  bx,by,CONE_BASE,  0,0,CONE_TIP)
    addTri(0,0,CONE_BASE,    bx,by,CONE_BASE,  ax,ay,CONE_BASE)
  }
  return new Float32Array(verts)
}

const CYL_VERTS = buildCylinder()
const CONE_VERTS = buildCone()

// ─── Shader ───────────────────────────────────────────────────────────────
// vertex: position in local space, transformed by (rotation matrix * handleScale)
//         then translated to gizmo origin and projected.
//
// The axis rotation is expressed as three vec3 uniforms (the three rows of
// the 3x3 rotation matrix). This is more compatible with WebGL1 than a
// uniform float array.
//
// u_rotRow0, u_rotRow1, u_rotRow2: rows of the 3×3 axis rotation matrix.
//   To rotate +Z to +X: row0=(0,0,1), row1=(0,1,0), row2=(-1,0,0)
//   To rotate +Z to +Y: row0=(1,0,0), row1=(0,0,1), row2=(0,-1,0)
//   Identity (+Z stays +Z): row0=(1,0,0), row1=(0,1,0), row2=(0,0,1)
// u_handleScale: world-space scale factor (computed from camera distance).
// u_origin: gizmo world-space center (centroid of selection).
// u_color: axis base color.
// u_hovered: 1.0 when this draw is the hovered/active axis, else 0.0.

const VERT_SHADER = `
attribute vec3 a_position;
uniform mat4 u_viewProj;
uniform vec3 u_origin;
uniform float u_handleScale;
uniform vec3 u_rotRow0;
uniform vec3 u_rotRow1;
uniform vec3 u_rotRow2;
void main() {
  float x = dot(u_rotRow0, a_position);
  float y = dot(u_rotRow1, a_position);
  float z = dot(u_rotRow2, a_position);
  vec3 worldPos = u_origin + vec3(x, y, z) * u_handleScale;
  gl_Position = u_viewProj * vec4(worldPos, 1.0);
}
`.trim()

const FRAG_SHADER = `
precision mediump float;
uniform vec3 u_color;
uniform float u_hovered;
void main() {
  vec3 hoverCol = vec3(1.0, 0.95, 0.3);
  vec3 col = mix(u_color, hoverCol, u_hovered * 0.5);
  gl_FragColor = vec4(col, 1.0);
}
`.trim()

// ─── Rotation matrices (row-major 3x3, stored as three vec3 rows) ──────────
// ROT_X: rotate +Z arrow to +X direction
//   row0 = (0, 0, 1), row1 = (0, 1, 0), row2 = (-1, 0, 0)
const ROT_X_R0 = new Float32Array([0, 0, 1])
const ROT_X_R1 = new Float32Array([0, 1, 0])
const ROT_X_R2 = new Float32Array([-1, 0, 0])
// ROT_Y: rotate +Z arrow to +Y direction
//   row0 = (1, 0, 0), row1 = (0, 0, 1), row2 = (0, -1, 0)
const ROT_Y_R0 = new Float32Array([1, 0, 0])
const ROT_Y_R1 = new Float32Array([0, 0, 1])
const ROT_Y_R2 = new Float32Array([0, -1, 0])
// ROT_Z: identity (arrow already along +Z)
const ROT_Z_R0 = new Float32Array([1, 0, 0])
const ROT_Z_R1 = new Float32Array([0, 1, 0])
const ROT_Z_R2 = new Float32Array([0, 0, 1])

// ─── Public types ──────────────────────────────────────────────────────────
export type GizmoAxis = 'x' | 'y' | 'z'

export interface GizmoPickResult {
  axis: GizmoAxis
}

interface EntityOrig {
  kind: 'unit' | 'doodad'
  cn: number
  posOrig: [number, number, number]
  inst: any
  moveHeight: number
}

export interface GizmoDragState {
  axis: GizmoAxis
  origin: [number, number, number]
  anchorParam: number
  entities: EntityOrig[]
}

export interface SelectionItem {
  kind: 'unit' | 'doodad'
  id: number
}

export interface GizmoRenderer {
  /**
   * Draw the gizmo. Called after viewer.render() and after
   * (viewer as any).webgl.currentShader = null in scene-instances.ts.
   * eyePos = world-space camera position (from camera controller state).
   */
  draw(
    gl: WebGLRenderingContext,
    viewer: any,
    scene: any,
    canvas: HTMLCanvasElement,
    selectionItems: SelectionItem[],
    unitInstances: Map<number, any>,
    doodadInstances: Map<number, any>,
    eyePos: [number, number, number],
  ): void

  rayPick(
    px: number,
    py: number,
    scene: any,
    canvas: HTMLCanvasElement,
  ): GizmoPickResult | null

  beginDrag(
    axis: GizmoAxis,
    px: number,
    py: number,
    scene: any,
    canvas: HTMLCanvasElement,
    selectionItems: SelectionItem[],
    unitInstances: Map<number, any>,
    doodadInstances: Map<number, any>,
    unitTypeIndexCache: Record<string, { move_height: number }> | null,
  ): void

  onDrag(
    px: number,
    py: number,
    scene: any,
    canvas: HTMLCanvasElement,
  ): void

  onDragEnd(
    px: number,
    py: number,
    scene: any,
    canvas: HTMLCanvasElement,
  ): void

  cancelDrag(): void
  isDragging(): boolean
  dragAxis(): GizmoAxis | null
  dispose(): void
}

// ─── GL helpers ──────────────────────────────────────────────────────────
function compileShader(gl: WebGLRenderingContext, type: number, src: string): WebGLShader {
  const sh = gl.createShader(type)!
  gl.shaderSource(sh, src)
  gl.compileShader(sh)
  if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
    const log = gl.getShaderInfoLog(sh)
    gl.deleteShader(sh)
    throw new Error('gizmo shader compile: ' + log)
  }
  return sh
}

function buildProgram(gl: WebGLRenderingContext) {
  const program = gl.createProgram()!
  gl.attachShader(program, compileShader(gl, gl.VERTEX_SHADER, VERT_SHADER))
  gl.attachShader(program, compileShader(gl, gl.FRAGMENT_SHADER, FRAG_SHADER))
  gl.linkProgram(program)
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
    const log = gl.getProgramInfoLog(program)
    gl.deleteProgram(program)
    throw new Error('gizmo program link: ' + log)
  }
  return {
    program,
    aPosition: gl.getAttribLocation(program, 'a_position'),
    uViewProj: gl.getUniformLocation(program, 'u_viewProj')!,
    uOrigin: gl.getUniformLocation(program, 'u_origin')!,
    uHandleScale: gl.getUniformLocation(program, 'u_handleScale')!,
    uRotRow0: gl.getUniformLocation(program, 'u_rotRow0')!,
    uRotRow1: gl.getUniformLocation(program, 'u_rotRow1')!,
    uRotRow2: gl.getUniformLocation(program, 'u_rotRow2')!,
    uColor: gl.getUniformLocation(program, 'u_color')!,
    uHovered: gl.getUniformLocation(program, 'u_hovered')!,
  }
}

// ─── Ray helpers ──────────────────────────────────────────────────────────
function screenRay(px: number, py: number, scene: any, canvas: HTMLCanvasElement): Float32Array | null {
  const vp = (scene as any).viewport as Float32Array
  if (!vp) return null
  const out = new Float32Array(6)
  const screen = new Float32Array([px, py])
  scene.camera.screenToWorldRay(out, screen, vp)
  const dx = out[3] - out[0], dy = out[4] - out[1], dz = out[5] - out[2]
  const len = Math.hypot(dx, dy, dz)
  if (len < 1e-10) return null
  return new Float32Array([out[0], out[1], out[2], dx/len, dy/len, dz/len])
}

function rayLineNearest(
  rox: number, roy: number, roz: number,
  rdx: number, rdy: number, rdz: number,
  lox: number, loy: number, loz: number,
  ldx: number, ldy: number, ldz: number,
): { s: number; t: number } | null {
  const wx = rox - lox, wy = roy - loy, wz = roz - loz
  const b = rdx*ldx + rdy*ldy + rdz*ldz
  const d = rdx*rdx + rdy*rdy + rdz*rdz
  const e = ldx*ldx + ldy*ldy + ldz*ldz
  const denom = d * e - b * b
  if (Math.abs(denom) < 1e-10) return null
  const c = rdx*wx + rdy*wy + rdz*wz
  const f = ldx*wx + ldy*wy + ldz*wz
  const sc = (b*f - e*c) / denom
  const tc = (d*f - b*c) / denom
  return { s: tc, t: sc }
}

const AXIS_DIRS: Record<GizmoAxis, [number,number,number]> = {
  x: [1, 0, 0],
  y: [0, 1, 0],
  z: [0, 0, 1],
}

function rayCylinder(
  rox: number, roy: number, roz: number,
  rdx: number, rdy: number, rdz: number,
  cox: number, coy: number, coz: number,
  cax: number, cay: number, caz: number,
  radius: number,
  minT: number,
  maxT: number,
): number | null {
  const wx = rox - cox, wy = roy - coy, wz = roz - coz
  const wdotA = wx*cax + wy*cay + wz*caz
  const wpx = wx - wdotA*cax, wpy = wy - wdotA*cay, wpz = wz - wdotA*caz
  const ddotA = rdx*cax + rdy*cay + rdz*caz
  const dpx = rdx - ddotA*cax, dpy = rdy - ddotA*cay, dpz = rdz - ddotA*caz
  const a = dpx*dpx + dpy*dpy + dpz*dpz
  if (a < 1e-10) return null
  const b2 = wpx*dpx + wpy*dpy + wpz*dpz
  const c = wpx*wpx + wpy*wpy + wpz*wpz - radius*radius
  const disc = b2*b2 - a*c
  if (disc < 0) return null
  const sq = Math.sqrt(disc)
  let t = (-b2 - sq) / a
  if (t < 0) t = (-b2 + sq) / a
  if (t < 0) return null
  const hitAx = wdotA + t * ddotA
  if (hitAx < minT || hitAx > maxT) return null
  return t
}

// ─── Main builder ──────────────────────────────────────────────────────────
export function buildGizmo(gl: WebGLRenderingContext): GizmoRenderer | null {
  let prog: ReturnType<typeof buildProgram>
  try {
    prog = buildProgram(gl)
  } catch (e) {
    flog('[gizmo] program build failed:', e instanceof Error ? e.message : String(e))
    return null
  }

  const cylVBO = gl.createBuffer()!
  gl.bindBuffer(gl.ARRAY_BUFFER, cylVBO)
  gl.bufferData(gl.ARRAY_BUFFER, CYL_VERTS, gl.STATIC_DRAW)

  const coneVBO = gl.createBuffer()!
  gl.bindBuffer(gl.ARRAY_BUFFER, coneVBO)
  gl.bufferData(gl.ARRAY_BUFFER, CONE_VERTS, gl.STATIC_DRAW)

  // Unbind buffers after upload to avoid contaminating the lib's binding state
  gl.bindBuffer(gl.ARRAY_BUFFER, null)

  let lastOrigin: [number, number, number] = [0, 0, 0]
  let lastHandleScale: number = 1
  let lastVisible: boolean = false
  let hoverAxis: GizmoAxis | null = null
  let dragState: GizmoDragState | null = null

  function computeOrigin(
    selectionItems: SelectionItem[],
    unitInstances: Map<number, any>,
    doodadInstances: Map<number, any>,
  ): [number, number, number] | null {
    if (selectionItems.length === 0) return null
    let sx = 0, sy = 0, sz = 0, cnt = 0
    for (const item of selectionItems) {
      const inst = item.kind === 'unit' ? unitInstances.get(item.id) : doodadInstances.get(item.id)
      if (!inst) continue
      const wl = inst.worldLocation
      if (!wl) continue
      sx += wl[0]; sy += wl[1]; sz += wl[2]; cnt++
    }
    if (cnt === 0) return null
    return [sx/cnt, sy/cnt, sz/cnt]
  }

  function drawAxis(
    viewProj: Float32Array,
    origin: [number, number, number],
    handleScale: number,
    r0: Float32Array, r1: Float32Array, r2: Float32Array,
    color: [number, number, number],
    hovered: boolean,
  ) {
    gl.uniform3f(prog.uOrigin, origin[0], origin[1], origin[2])
    gl.uniform1f(prog.uHandleScale, handleScale)
    gl.uniform3fv(prog.uRotRow0, r0)
    gl.uniform3fv(prog.uRotRow1, r1)
    gl.uniform3fv(prog.uRotRow2, r2)
    gl.uniform3f(prog.uColor, color[0], color[1], color[2])
    gl.uniform1f(prog.uHovered, hovered ? 1.0 : 0.0)
    gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)

    gl.bindBuffer(gl.ARRAY_BUFFER, cylVBO)
    gl.vertexAttribPointer(prog.aPosition, 3, gl.FLOAT, false, 12, 0)
    gl.drawArrays(gl.TRIANGLES, 0, CYL_VERTS.length / 3)

    gl.bindBuffer(gl.ARRAY_BUFFER, coneVBO)
    gl.vertexAttribPointer(prog.aPosition, 3, gl.FLOAT, false, 12, 0)
    gl.drawArrays(gl.TRIANGLES, 0, CONE_VERTS.length / 3)
  }

  return {
    draw(
      gl, viewer, scene, canvas,
      selectionItems, unitInstances, doodadInstances,
      eyePos,
    ) {
      const origin = dragState ? dragState.origin
        : computeOrigin(selectionItems, unitInstances, doodadInstances)
      lastVisible = origin !== null
      if (!origin) return
      lastOrigin = origin

      // Compute world-space handle scale so the gizmo subtends a fixed
      // screen-space size. We use the caller-supplied eye position (from the
      // camera controller's state) rather than scene.camera.location, which
      // is not reliably exposed by the mdx-m3-viewer camera API.
      const dx = eyePos[0] - origin[0]
      const dy = eyePos[1] - origin[1]
      const dz = eyePos[2] - origin[2]
      const dist = Math.hypot(dx, dy, dz) || 6000
      const handleScale = Math.max(10, dist * SCREEN_SIZE_CONSTANT)
      lastHandleScale = handleScale

      // ── GL state save + setup ─────────────────────────────────────────
      // Snapshot WebGL booleans before we touch them so we can restore
      // exactly after. Mirror path-blockers.ts discipline.
      const wasDepthTest = gl.isEnabled(gl.DEPTH_TEST)
      const wasBlend = gl.isEnabled(gl.BLEND)
      const wasCull = gl.isEnabled(gl.CULL_FACE)

      // CRITICAL: reset the viewer's shader cache BEFORE our useProgram so
      // the next viewer.render() doesn't short-circuit its own useShader().
      // See feedback-mdx-viewer-shader-cache.
      ;(viewer as any).webgl.currentShader = null
      gl.useProgram(prog.program)

      // Disable all attrib arrays; enable only ours. Mirrors path-blockers.ts.
      const maxAttribs = gl.getParameter(gl.MAX_VERTEX_ATTRIBS) | 0
      for (let i = 0; i < maxAttribs; i++) gl.disableVertexAttribArray(i)
      gl.enableVertexAttribArray(prog.aPosition)

      // Always-on-top: gizmo must render over buildings/cliffs/units.
      gl.disable(gl.DEPTH_TEST)
      gl.disable(gl.BLEND)
      gl.disable(gl.CULL_FACE)

      const vp = scene.camera.viewProjectionMatrix as Float32Array
      const activeAxis = dragState?.axis ?? hoverAxis

      try {
        drawAxis(vp, origin, handleScale, ROT_X_R0, ROT_X_R1, ROT_X_R2, X_COLOR, activeAxis === 'x')
        drawAxis(vp, origin, handleScale, ROT_Y_R0, ROT_Y_R1, ROT_Y_R2, Y_COLOR, activeAxis === 'y')
        drawAxis(vp, origin, handleScale, ROT_Z_R0, ROT_Z_R1, ROT_Z_R2, Z_COLOR, activeAxis === 'z')
      } finally {
        // Restore GL state UNCONDITIONALLY (even if drawAxis throws).
        gl.disableVertexAttribArray(prog.aPosition)
        if (wasDepthTest) gl.enable(gl.DEPTH_TEST)
        else gl.disable(gl.DEPTH_TEST)
        if (wasBlend) gl.enable(gl.BLEND)
        else gl.disable(gl.BLEND)
        if (wasCull) gl.enable(gl.CULL_FACE)
        else gl.disable(gl.CULL_FACE)
        gl.depthMask(true)
        // Unbind buffers so we don't pollute the lib's next render.
        gl.bindBuffer(gl.ARRAY_BUFFER, null)
        // Reset viewer shader cache AFTER our draw — we are the last pass.
        ;(viewer as any).webgl.currentShader = null
      }
    },

    rayPick(px, py, scene, canvas): GizmoPickResult | null {
      if (!lastVisible) return null

      const ray = screenRay(px, py, scene, canvas)
      if (!ray) return null
      const [rox, roy, roz, rdx, rdy, rdz] = ray

      const origin = lastOrigin
      const scale = lastHandleScale

      const axes: GizmoAxis[] = ['x', 'y', 'z']
      const axDirs: [number, number, number][] = [AXIS_DIRS.x, AXIS_DIRS.y, AXIS_DIRS.z]

      let bestT = Infinity
      let bestAxis: GizmoAxis | null = null

      for (let ai = 0; ai < 3; ai++) {
        const axis = axes[ai]
        const [adx, ady, adz] = axDirs[ai]

        // Cylinder pick
        const cylR = CYL_RADIUS * scale * PICK_RADIUS_INFLATE
        const cylT = rayCylinder(
          rox, roy, roz, rdx, rdy, rdz,
          origin[0], origin[1], origin[2],
          adx, ady, adz,
          cylR, 0, CYL_TOP * scale,
        )
        if (cylT !== null && cylT > 0 && cylT < bestT) {
          bestT = cylT; bestAxis = axis
        }

        // Cone pick (use cylinder approximation for pick zone)
        const coneBaseX = origin[0] + adx * CONE_BASE * scale
        const coneBaseY = origin[1] + ady * CONE_BASE * scale
        const coneBaseZ = origin[2] + adz * CONE_BASE * scale
        const coneR = CONE_RADIUS * scale * PICK_RADIUS_INFLATE
        const coneLen = (CONE_TIP - CONE_BASE) * scale
        const coneT = rayCylinder(
          rox, roy, roz, rdx, rdy, rdz,
          coneBaseX, coneBaseY, coneBaseZ,
          adx, ady, adz,
          coneR, 0, coneLen,
        )
        if (coneT !== null && coneT > 0 && coneT < bestT) {
          bestT = coneT; bestAxis = axis
        }
      }

      if (bestAxis === null) {
        hoverAxis = null
        return null
      }
      hoverAxis = bestAxis
      return { axis: bestAxis }
    },

    beginDrag(axis, px, py, scene, canvas, selectionItems, unitInstances, doodadInstances, unitTypeIndexCache) {
      const origin = computeOrigin(selectionItems, unitInstances, doodadInstances)
      if (!origin) return

      const ray = screenRay(px, py, scene, canvas)
      if (!ray) return
      const [rox, roy, roz, rdx, rdy, rdz] = ray
      const [adx, ady, adz] = AXIS_DIRS[axis]
      const nearest = rayLineNearest(
        rox, roy, roz, rdx, rdy, rdz,
        origin[0], origin[1], origin[2], adx, ady, adz,
      )
      if (!nearest) return
      const anchorParam = nearest.s

      const entities: EntityOrig[] = []
      for (const item of selectionItems) {
        const inst = item.kind === 'unit' ? unitInstances.get(item.id) : doodadInstances.get(item.id)
        if (!inst) continue
        const wl = inst.worldLocation
        if (!wl) continue
        let moveHeight = 0
        if (item.kind === 'unit') {
          const typeId = (inst as any).__wc3ForgeTypeId as string | undefined
          const info = typeId ? unitTypeIndexCache?.[typeId] : undefined
          moveHeight = info?.move_height ?? 0
        }
        entities.push({
          kind: item.kind,
          cn: item.id,
          posOrig: [wl[0], wl[1], wl[2]],
          inst,
          moveHeight,
        })
      }
      if (entities.length === 0) return

      dragState = { axis, origin, anchorParam, entities }
    },

    onDrag(px, py, scene, canvas) {
      if (!dragState) return
      const ray = screenRay(px, py, scene, canvas)
      if (!ray) return
      const [rox, roy, roz, rdx, rdy, rdz] = ray
      const { axis, origin, anchorParam, entities } = dragState
      const [adx, ady, adz] = AXIS_DIRS[axis]

      const nearest = rayLineNearest(
        rox, roy, roz, rdx, rdy, rdz,
        origin[0], origin[1], origin[2], adx, ady, adz,
      )
      if (!nearest) return
      const delta = nearest.s - anchorParam

      for (const ent of entities) {
        const nx = ent.posOrig[0] + adx * delta
        const ny = ent.posOrig[1] + ady * delta
        const nz = ent.posOrig[2] + adz * delta
        ;(ent.inst as any).setLocation([nx, ny, nz])
      }
    },

    onDragEnd(px, py, scene, canvas) {
      if (!dragState) return
      const { axis, origin, anchorParam, entities } = dragState

      let delta = 0
      const ray = screenRay(px, py, scene, canvas)
      if (ray) {
        const [rox, roy, roz, rdx, rdy, rdz] = ray
        const [adx, ady, adz] = AXIS_DIRS[axis]
        const nearest = rayLineNearest(
          rox, roy, roz, rdx, rdy, rdz,
          origin[0], origin[1], origin[2], adx, ady, adz,
        )
        if (nearest) delta = nearest.s - anchorParam
      }

      const [adx, ady, adz] = AXIS_DIRS[axis]

      ;(async () => {
        for (const ent of entities) {
          const nx = ent.posOrig[0] + adx * delta
          const ny = ent.posOrig[1] + ady * delta
          const nz = ent.posOrig[2] + adz * delta
          ;(ent.inst as any).setLocation([nx, ny, nz])

          // game-space Z: subtract move_height (units have move_height baked in)
          const gameZ = nz - ent.moveHeight

          try {
            if (ent.kind === 'unit') {
              await MoveUnit(ent.cn, nx, ny, gameZ)
            } else {
              await MoveDoodad(ent.cn, nx, ny, gameZ)
            }
          } catch (err) {
            flog(`[gizmo] commit ${ent.kind} cn=${ent.cn} failed:`, err instanceof Error ? err.message : String(err))
          }
        }
      })()

      dragState = null
      hoverAxis = null
    },

    cancelDrag() {
      if (!dragState) return
      for (const ent of dragState.entities) {
        ;(ent.inst as any).setLocation(ent.posOrig)
      }
      dragState = null
      hoverAxis = null
    },

    isDragging() { return dragState !== null },
    dragAxis() { return dragState?.axis ?? null },

    dispose() {
      gl.deleteBuffer(cylVBO)
      gl.deleteBuffer(coneVBO)
      gl.deleteProgram(prog.program)
      dragState = null
    },
  }
}
