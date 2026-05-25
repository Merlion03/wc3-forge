// gizmo.ts — transform gizmo renderer + picker + drag math.
//
// Phase B = move arrows.
// Phase C adds:
//   - rotate ring (Z-axis only — X/Y rotations have no on-disk storage)
//   - scale cubes (3 axes; visual feedback is uniform via scale[0] since
//     the render path uses uniformScale, but the on-disk per-axis values
//     are written by ScaleUnit/ScaleDoodad)
//   - mode switching: 'move' | 'rotate' | 'scale' via setMode()
//   - new drag math for rotate (plane angle) and scale (signed-distance ratio)
//
// Architecture: stateless renderer overlay, same shape as sloc-markers.ts,
// cell-highlight.ts, and path-blockers.ts. Owns its own WebGL1 shader.
// Drawn AFTER all other overlays in the RAF loop in scene-instances.ts.
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

import {
  MoveUnit, MoveDoodad,
  RotateUnit, RotateDoodad,
  ScaleUnit, ScaleDoodad,
} from '../wailsjs/go/main/App.js'
import { flog } from './debuglog'

// ─── Axis colors (design §3.7) ────────────────────────────────────────────
const X_COLOR: [number, number, number] = [1.0, 0.2, 0.2]  // red
const Y_COLOR: [number, number, number] = [0.2, 1.0, 0.2]  // green
const Z_COLOR: [number, number, number] = [0.3, 0.4, 1.0]  // blue

// ─── Screen-space size (design §1.2) ──────────────────────────────────────
const SCREEN_SIZE_CONSTANT = 0.115

// ─── Pick zone inflation factor (design §1.3) ──────────────────────────────
const PICK_RADIUS_INFLATE = 1.5

// ─── Move-arrow geometry (Phase B, unchanged) ──────────────────────────────
const CYL_SEGS = 12
const CONE_SEGS = 12
const CYL_RADIUS = 0.05
const CONE_RADIUS = 0.10
const CYL_TOP = 0.75
const CONE_BASE = 0.75
const CONE_TIP = 1.00

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

// ─── Rotate-ring geometry (Phase C) ────────────────────────────────────────
// Flat annulus in the XY plane (z=0). Drawn with the identity rotation
// matrix (ROT_Z) so the visible ring orbits the world-Z axis through the
// gizmo origin. Edge-on viewing makes a flat ring disappear, which is fine
// for a top-down RTS editor — the dominant camera angle keeps it visible.
const RING_SEGS = 64
const RING_R_INNER = 0.78
const RING_R_OUTER = 0.92
const RING_R_MID = (RING_R_INNER + RING_R_OUTER) / 2
const RING_R_HALF_WIDTH = (RING_R_OUTER - RING_R_INNER) / 2

function buildRing(): Float32Array {
  const verts: number[] = []
  const push = (x:number, y:number) => verts.push(x, y, 0)
  for (let i = 0; i < RING_SEGS; i++) {
    const a = (i / RING_SEGS) * Math.PI * 2
    const b = ((i + 1) / RING_SEGS) * Math.PI * 2
    const ax = Math.cos(a), ay = Math.sin(a)
    const bx = Math.cos(b), by = Math.sin(b)
    // tri 1: outer-a, inner-a, outer-b
    push(ax * RING_R_OUTER, ay * RING_R_OUTER)
    push(ax * RING_R_INNER, ay * RING_R_INNER)
    push(bx * RING_R_OUTER, by * RING_R_OUTER)
    // tri 2: outer-b, inner-a, inner-b
    push(bx * RING_R_OUTER, by * RING_R_OUTER)
    push(ax * RING_R_INNER, ay * RING_R_INNER)
    push(bx * RING_R_INNER, by * RING_R_INNER)
  }
  return new Float32Array(verts)
}

const RING_VERTS = buildRing()

// ─── Scale-cube geometry (Phase C) ─────────────────────────────────────────
// Cube at the tip of the cylinder stem.
const BOX_R = 0.08
const BOX_BASE_Z = CYL_TOP
const BOX_TIP_Z = CYL_TOP + 2 * BOX_R

function buildCube(): Float32Array {
  const verts: number[] = []
  const v = (x:number, y:number, z:number) => verts.push(x, y, z)
  const xMin = -BOX_R, xMax = BOX_R
  const yMin = -BOX_R, yMax = BOX_R
  const zMin = BOX_BASE_Z, zMax = BOX_TIP_Z
  // +Z face
  v(xMin, yMin, zMax); v(xMax, yMin, zMax); v(xMax, yMax, zMax)
  v(xMin, yMin, zMax); v(xMax, yMax, zMax); v(xMin, yMax, zMax)
  // -Z face
  v(xMin, yMin, zMin); v(xMax, yMax, zMin); v(xMax, yMin, zMin)
  v(xMin, yMin, zMin); v(xMin, yMax, zMin); v(xMax, yMax, zMin)
  // +X face
  v(xMax, yMin, zMin); v(xMax, yMax, zMin); v(xMax, yMax, zMax)
  v(xMax, yMin, zMin); v(xMax, yMax, zMax); v(xMax, yMin, zMax)
  // -X face
  v(xMin, yMin, zMin); v(xMin, yMax, zMax); v(xMin, yMax, zMin)
  v(xMin, yMin, zMin); v(xMin, yMin, zMax); v(xMin, yMax, zMax)
  // +Y face
  v(xMin, yMax, zMin); v(xMin, yMax, zMax); v(xMax, yMax, zMax)
  v(xMin, yMax, zMin); v(xMax, yMax, zMax); v(xMax, yMax, zMin)
  // -Y face
  v(xMin, yMin, zMin); v(xMax, yMin, zMax); v(xMin, yMin, zMax)
  v(xMin, yMin, zMin); v(xMax, yMin, zMin); v(xMax, yMin, zMax)
  return new Float32Array(verts)
}

const CUBE_VERTS = buildCube()

// ─── Shader ───────────────────────────────────────────────────────────────
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
const ROT_X_R0 = new Float32Array([0, 0, 1])
const ROT_X_R1 = new Float32Array([0, 1, 0])
const ROT_X_R2 = new Float32Array([-1, 0, 0])
const ROT_Y_R0 = new Float32Array([1, 0, 0])
const ROT_Y_R1 = new Float32Array([0, 0, 1])
const ROT_Y_R2 = new Float32Array([0, -1, 0])
const ROT_Z_R0 = new Float32Array([1, 0, 0])
const ROT_Z_R1 = new Float32Array([0, 1, 0])
const ROT_Z_R2 = new Float32Array([0, 0, 1])

// ─── Public types ──────────────────────────────────────────────────────────
export type GizmoAxis = 'x' | 'y' | 'z'
export type GizmoMode = 'move' | 'rotate' | 'scale'

export interface GizmoPickResult {
  axis: GizmoAxis
  mode: GizmoMode
}

const AXIS_ROT: Record<GizmoAxis, [Float32Array, Float32Array, Float32Array]> = {
  x: [ROT_X_R0, ROT_X_R1, ROT_X_R2],
  y: [ROT_Y_R0, ROT_Y_R1, ROT_Y_R2],
  z: [ROT_Z_R0, ROT_Z_R1, ROT_Z_R2],
}

const AXIS_COLOR: Record<GizmoAxis, [number, number, number]> = {
  x: X_COLOR, y: Y_COLOR, z: Z_COLOR,
}

const AXIS_DIRS: Record<GizmoAxis, [number,number,number]> = {
  x: [1, 0, 0],
  y: [0, 1, 0],
  z: [0, 0, 1],
}

interface EntityOrig {
  kind: 'unit' | 'doodad'
  cn: number
  posOrig: [number, number, number]
  rotOrig: number                 // Z-axis rotation in radians
  scaleOrig: [number, number, number]
  modelScale: number              // info.model_scale; for live-preview reconstruction with uniformScale
  inst: any
  moveHeight: number
}

interface MoveDrag {
  mode: 'move'
  axis: GizmoAxis
  origin: [number, number, number]
  entities: EntityOrig[]
  anchorParam: number
}
interface RotateDrag {
  mode: 'rotate'
  axis: GizmoAxis
  origin: [number, number, number]
  entities: EntityOrig[]
  anchorAngle: number
  currentAngle: number
}
interface ScaleDrag {
  mode: 'scale'
  axis: GizmoAxis
  origin: [number, number, number]
  entities: EntityOrig[]
  anchorSignedDist: number
  currentFactor: number
}

export type GizmoDragState = MoveDrag | RotateDrag | ScaleDrag

export interface SelectionItem {
  kind: 'unit' | 'doodad'
  id: number
}

export interface UnitTypeInfo { move_height: number; model_scale?: number }

export interface GizmoRenderer {
  /** Set the active mode. Cancels any in-progress drag. */
  setMode(mode: GizmoMode): void
  getMode(): GizmoMode

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
    pick: GizmoPickResult,
    px: number,
    py: number,
    scene: any,
    canvas: HTMLCanvasElement,
    selectionItems: SelectionItem[],
    unitInstances: Map<number, any>,
    doodadInstances: Map<number, any>,
    unitTypeIndexCache: Record<string, UnitTypeInfo> | null,
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
  dragMode(): GizmoMode | null
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
function screenRay(px: number, py: number, scene: any, _canvas: HTMLCanvasElement): Float32Array | null {
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

/** Ray-plane intersection. Returns t (ray parameter) + hit point, or null. */
function rayPlane(
  rox: number, roy: number, roz: number,
  rdx: number, rdy: number, rdz: number,
  px: number, py: number, pz: number,
  nx: number, ny: number, nz: number,
): { t: number; hx: number; hy: number; hz: number } | null {
  const denom = rdx*nx + rdy*ny + rdz*nz
  if (Math.abs(denom) < 1e-10) return null
  const t = ((px - rox)*nx + (py - roy)*ny + (pz - roz)*nz) / denom
  if (t < 0) return null
  return { t, hx: rox + rdx * t, hy: roy + rdy * t, hz: roz + rdz * t }
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

/** Build a unit-axis Z-rotation quaternion [x, y, z, w]. */
function quatZ(angle: number): [number, number, number, number] {
  const h = angle / 2
  return [0, 0, Math.sin(h), Math.cos(h)]
}

/** Normalize an angle to (-π, π]. */
function wrapAngle(a: number): number {
  while (a >  Math.PI) a -= 2 * Math.PI
  while (a <= -Math.PI) a += 2 * Math.PI
  return a
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

  const ringVBO = gl.createBuffer()!
  gl.bindBuffer(gl.ARRAY_BUFFER, ringVBO)
  gl.bufferData(gl.ARRAY_BUFFER, RING_VERTS, gl.STATIC_DRAW)

  const cubeVBO = gl.createBuffer()!
  gl.bindBuffer(gl.ARRAY_BUFFER, cubeVBO)
  gl.bufferData(gl.ARRAY_BUFFER, CUBE_VERTS, gl.STATIC_DRAW)

  gl.bindBuffer(gl.ARRAY_BUFFER, null)

  let mode: GizmoMode = 'move'
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

  function bindVBO(vbo: WebGLBuffer) {
    gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
    gl.vertexAttribPointer(prog.aPosition, 3, gl.FLOAT, false, 12, 0)
  }

  function setAxisUniforms(
    origin: [number, number, number],
    handleScale: number,
    r0: Float32Array, r1: Float32Array, r2: Float32Array,
    color: [number, number, number],
    hovered: boolean,
    viewProj: Float32Array,
  ) {
    gl.uniform3f(prog.uOrigin, origin[0], origin[1], origin[2])
    gl.uniform1f(prog.uHandleScale, handleScale)
    gl.uniform3fv(prog.uRotRow0, r0)
    gl.uniform3fv(prog.uRotRow1, r1)
    gl.uniform3fv(prog.uRotRow2, r2)
    gl.uniform3f(prog.uColor, color[0], color[1], color[2])
    gl.uniform1f(prog.uHovered, hovered ? 1.0 : 0.0)
    gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)
  }

  function drawMoveArrow(
    viewProj: Float32Array,
    origin: [number, number, number], scale: number,
    axis: GizmoAxis, hovered: boolean,
  ) {
    const [r0, r1, r2] = AXIS_ROT[axis]
    setAxisUniforms(origin, scale, r0, r1, r2, AXIS_COLOR[axis], hovered, viewProj)
    bindVBO(cylVBO);  gl.drawArrays(gl.TRIANGLES, 0, CYL_VERTS.length / 3)
    bindVBO(coneVBO); gl.drawArrays(gl.TRIANGLES, 0, CONE_VERTS.length / 3)
  }

  function drawScaleHandle(
    viewProj: Float32Array,
    origin: [number, number, number], scale: number,
    axis: GizmoAxis, hovered: boolean,
  ) {
    const [r0, r1, r2] = AXIS_ROT[axis]
    setAxisUniforms(origin, scale, r0, r1, r2, AXIS_COLOR[axis], hovered, viewProj)
    bindVBO(cylVBO);  gl.drawArrays(gl.TRIANGLES, 0, CYL_VERTS.length / 3)
    bindVBO(cubeVBO); gl.drawArrays(gl.TRIANGLES, 0, CUBE_VERTS.length / 3)
  }

  function drawRotateRing(
    viewProj: Float32Array,
    origin: [number, number, number], scale: number,
    axis: GizmoAxis, hovered: boolean,
  ) {
    // Z ring uses identity rotation. (Future X/Y rings would use AXIS_ROT[axis]
    // but they have no on-disk destination, so we don't draw them.)
    const [r0, r1, r2] = AXIS_ROT[axis]
    setAxisUniforms(origin, scale, r0, r1, r2, AXIS_COLOR[axis], hovered, viewProj)
    bindVBO(ringVBO); gl.drawArrays(gl.TRIANGLES, 0, RING_VERTS.length / 3)
  }

  function pickMove(
    rox: number, roy: number, roz: number, rdx: number, rdy: number, rdz: number,
    origin: [number, number, number], scale: number,
  ): GizmoAxis | null {
    let bestT = Infinity
    let bestAxis: GizmoAxis | null = null
    for (const axis of ['x', 'y', 'z'] as GizmoAxis[]) {
      const [adx, ady, adz] = AXIS_DIRS[axis]
      const cylR = CYL_RADIUS * scale * PICK_RADIUS_INFLATE
      const cylT = rayCylinder(
        rox, roy, roz, rdx, rdy, rdz,
        origin[0], origin[1], origin[2],
        adx, ady, adz, cylR, 0, CYL_TOP * scale,
      )
      if (cylT !== null && cylT > 0 && cylT < bestT) { bestT = cylT; bestAxis = axis }
      const coneBaseX = origin[0] + adx * CONE_BASE * scale
      const coneBaseY = origin[1] + ady * CONE_BASE * scale
      const coneBaseZ = origin[2] + adz * CONE_BASE * scale
      const coneR = CONE_RADIUS * scale * PICK_RADIUS_INFLATE
      const coneLen = (CONE_TIP - CONE_BASE) * scale
      const coneT = rayCylinder(
        rox, roy, roz, rdx, rdy, rdz,
        coneBaseX, coneBaseY, coneBaseZ,
        adx, ady, adz, coneR, 0, coneLen,
      )
      if (coneT !== null && coneT > 0 && coneT < bestT) { bestT = coneT; bestAxis = axis }
    }
    return bestAxis
  }

  function pickScale(
    rox: number, roy: number, roz: number, rdx: number, rdy: number, rdz: number,
    origin: [number, number, number], scale: number,
  ): GizmoAxis | null {
    let bestT = Infinity
    let bestAxis: GizmoAxis | null = null
    for (const axis of ['x', 'y', 'z'] as GizmoAxis[]) {
      const [adx, ady, adz] = AXIS_DIRS[axis]
      const cylR = CYL_RADIUS * scale * PICK_RADIUS_INFLATE
      const cylT = rayCylinder(
        rox, roy, roz, rdx, rdy, rdz,
        origin[0], origin[1], origin[2],
        adx, ady, adz, cylR, 0, CYL_TOP * scale,
      )
      if (cylT !== null && cylT > 0 && cylT < bestT) { bestT = cylT; bestAxis = axis }
      // Cube approximated as a fat cylinder around its center for forgiving pick.
      const cubeCenterT = (BOX_BASE_Z + BOX_TIP_Z) / 2
      const cubeBaseX = origin[0] + adx * (cubeCenterT - (BOX_TIP_Z - BOX_BASE_Z)/2) * scale
      const cubeBaseY = origin[1] + ady * (cubeCenterT - (BOX_TIP_Z - BOX_BASE_Z)/2) * scale
      const cubeBaseZ = origin[2] + adz * (cubeCenterT - (BOX_TIP_Z - BOX_BASE_Z)/2) * scale
      const cubeR = BOX_R * scale * PICK_RADIUS_INFLATE * 1.2
      const cubeLen = (BOX_TIP_Z - BOX_BASE_Z) * scale
      const cubeT = rayCylinder(
        rox, roy, roz, rdx, rdy, rdz,
        cubeBaseX, cubeBaseY, cubeBaseZ,
        adx, ady, adz, cubeR, 0, cubeLen,
      )
      if (cubeT !== null && cubeT > 0 && cubeT < bestT) { bestT = cubeT; bestAxis = axis }
    }
    return bestAxis
  }

  function pickRotate(
    rox: number, roy: number, roz: number, rdx: number, rdy: number, rdz: number,
    origin: [number, number, number], scale: number,
  ): GizmoAxis | null {
    const hit = rayPlane(
      rox, roy, roz, rdx, rdy, rdz,
      origin[0], origin[1], origin[2],
      0, 0, 1,
    )
    if (!hit) return null
    const dx = hit.hx - origin[0]
    const dy = hit.hy - origin[1]
    const r = Math.hypot(dx, dy)
    const minR = (RING_R_MID - RING_R_HALF_WIDTH * PICK_RADIUS_INFLATE) * scale
    const maxR = (RING_R_MID + RING_R_HALF_WIDTH * PICK_RADIUS_INFLATE) * scale
    if (r < minR || r > maxR) return null
    return 'z'
  }

  function restoreEntity(ent: EntityOrig) {
    ;(ent.inst as any).setLocation([ent.posOrig[0], ent.posOrig[1], ent.posOrig[2]])
    if (typeof (ent.inst as any).setRotation === 'function') {
      ;(ent.inst as any).setRotation(quatZ(ent.rotOrig))
    }
    if (typeof (ent.inst as any).uniformScale === 'function') {
      ;(ent.inst as any).uniformScale(ent.scaleOrig[0] * ent.modelScale)
    }
  }

  return {
    setMode(m: GizmoMode) {
      if (m === mode) return
      // Cancel any active drag on mode switch.
      if (dragState) {
        for (const ent of dragState.entities) restoreEntity(ent)
        dragState = null
      }
      mode = m
      hoverAxis = null
    },

    getMode(): GizmoMode { return mode },

    draw(
      _gl, viewer, scene, _canvas,
      selectionItems, unitInstances, doodadInstances,
      eyePos,
    ) {
      const origin = dragState ? dragState.origin
        : computeOrigin(selectionItems, unitInstances, doodadInstances)
      lastVisible = origin !== null
      if (!origin) return
      lastOrigin = origin

      const dx = eyePos[0] - origin[0]
      const dy = eyePos[1] - origin[1]
      const dz = eyePos[2] - origin[2]
      const dist = Math.hypot(dx, dy, dz) || 6000
      const handleScale = Math.max(10, dist * SCREEN_SIZE_CONSTANT)
      lastHandleScale = handleScale

      const wasDepthTest = gl.isEnabled(gl.DEPTH_TEST)
      const wasBlend = gl.isEnabled(gl.BLEND)
      const wasCull = gl.isEnabled(gl.CULL_FACE)

      ;(viewer as any).webgl.currentShader = null
      gl.useProgram(prog.program)

      const maxAttribs = gl.getParameter(gl.MAX_VERTEX_ATTRIBS) | 0
      for (let i = 0; i < maxAttribs; i++) gl.disableVertexAttribArray(i)
      gl.enableVertexAttribArray(prog.aPosition)

      gl.disable(gl.DEPTH_TEST)
      gl.disable(gl.BLEND)
      gl.disable(gl.CULL_FACE)

      const vp = scene.camera.viewProjectionMatrix as Float32Array
      const activeAxis = dragState?.axis ?? hoverAxis

      try {
        if (mode === 'move') {
          drawMoveArrow(vp, origin, handleScale, 'x', activeAxis === 'x')
          drawMoveArrow(vp, origin, handleScale, 'y', activeAxis === 'y')
          drawMoveArrow(vp, origin, handleScale, 'z', activeAxis === 'z')
        } else if (mode === 'scale') {
          drawScaleHandle(vp, origin, handleScale, 'x', activeAxis === 'x')
          drawScaleHandle(vp, origin, handleScale, 'y', activeAxis === 'y')
          drawScaleHandle(vp, origin, handleScale, 'z', activeAxis === 'z')
        } else if (mode === 'rotate') {
          // Z-axis ring only (design §5 #9: X/Y rotations have no on-disk storage).
          drawRotateRing(vp, origin, handleScale, 'z', activeAxis === 'z')
        }
      } finally {
        gl.disableVertexAttribArray(prog.aPosition)
        if (wasDepthTest) gl.enable(gl.DEPTH_TEST); else gl.disable(gl.DEPTH_TEST)
        if (wasBlend) gl.enable(gl.BLEND); else gl.disable(gl.BLEND)
        if (wasCull) gl.enable(gl.CULL_FACE); else gl.disable(gl.CULL_FACE)
        gl.depthMask(true)
        gl.bindBuffer(gl.ARRAY_BUFFER, null)
        ;(viewer as any).webgl.currentShader = null
      }
    },

    rayPick(px, py, scene, canvas): GizmoPickResult | null {
      if (!lastVisible) return null
      const ray = screenRay(px, py, scene, canvas)
      if (!ray) return null
      const [rox, roy, roz, rdx, rdy, rdz] = ray

      let axis: GizmoAxis | null = null
      if (mode === 'move')        axis = pickMove(rox, roy, roz, rdx, rdy, rdz, lastOrigin, lastHandleScale)
      else if (mode === 'scale')  axis = pickScale(rox, roy, roz, rdx, rdy, rdz, lastOrigin, lastHandleScale)
      else if (mode === 'rotate') axis = pickRotate(rox, roy, roz, rdx, rdy, rdz, lastOrigin, lastHandleScale)

      if (axis === null) { hoverAxis = null; return null }
      hoverAxis = axis
      return { axis, mode }
    },

    beginDrag(pick, px, py, scene, canvas, selectionItems, unitInstances, doodadInstances, unitTypeIndexCache) {
      const origin = computeOrigin(selectionItems, unitInstances, doodadInstances)
      if (!origin) return

      const ray = screenRay(px, py, scene, canvas)
      if (!ray) return
      const [rox, roy, roz, rdx, rdy, rdz] = ray

      const entities: EntityOrig[] = []
      for (const item of selectionItems) {
        const inst = item.kind === 'unit' ? unitInstances.get(item.id) : doodadInstances.get(item.id)
        if (!inst) continue
        const wl = inst.worldLocation
        if (!wl) continue
        let moveHeight = 0
        let modelScale = 1
        if (item.kind === 'unit') {
          const typeId = (inst as any).__wc3ForgeTypeId as string | undefined
          const info = typeId ? unitTypeIndexCache?.[typeId] : undefined
          moveHeight = info?.move_height ?? 0
          modelScale = info?.model_scale ?? 1
        } else {
          modelScale = (inst as any).__wc3ForgeModelScale ?? 1
        }
        const rotOrig = (inst as any).__wc3ForgeRotation ?? 0
        const scaleOrig = ((inst as any).__wc3ForgeScale ?? [1, 1, 1]) as [number, number, number]
        entities.push({
          kind: item.kind,
          cn: item.id,
          posOrig: [wl[0], wl[1], wl[2]],
          rotOrig,
          scaleOrig,
          modelScale,
          inst,
          moveHeight,
        })
      }
      if (entities.length === 0) return

      if (pick.mode === 'move') {
        const [adx, ady, adz] = AXIS_DIRS[pick.axis]
        const nearest = rayLineNearest(
          rox, roy, roz, rdx, rdy, rdz,
          origin[0], origin[1], origin[2], adx, ady, adz,
        )
        if (!nearest) return
        dragState = {
          mode: 'move', axis: pick.axis, origin, entities,
          anchorParam: nearest.s,
        }
      } else if (pick.mode === 'scale') {
        const [adx, ady, adz] = AXIS_DIRS[pick.axis]
        const nearest = rayLineNearest(
          rox, roy, roz, rdx, rdy, rdz,
          origin[0], origin[1], origin[2], adx, ady, adz,
        )
        if (!nearest) return
        const anchor = Math.abs(nearest.s) < 1e-3 ? 1e-3 : nearest.s
        dragState = {
          mode: 'scale', axis: pick.axis, origin, entities,
          anchorSignedDist: anchor, currentFactor: 1,
        }
      } else if (pick.mode === 'rotate') {
        const hit = rayPlane(
          rox, roy, roz, rdx, rdy, rdz,
          origin[0], origin[1], origin[2],
          0, 0, 1,
        )
        if (!hit) return
        const anchorAngle = Math.atan2(hit.hy - origin[1], hit.hx - origin[0])
        dragState = {
          mode: 'rotate', axis: pick.axis, origin, entities,
          anchorAngle, currentAngle: anchorAngle,
        }
      }
    },

    onDrag(px, py, scene, canvas) {
      if (!dragState) return
      const ray = screenRay(px, py, scene, canvas)
      if (!ray) return
      const [rox, roy, roz, rdx, rdy, rdz] = ray

      const ds = dragState
      const origin = ds.origin

      if (ds.mode === 'move') {
        const [adx, ady, adz] = AXIS_DIRS[ds.axis]
        const nearest = rayLineNearest(
          rox, roy, roz, rdx, rdy, rdz,
          origin[0], origin[1], origin[2], adx, ady, adz,
        )
        if (!nearest) return
        const delta = nearest.s - ds.anchorParam
        for (const ent of ds.entities) {
          const nx = ent.posOrig[0] + adx * delta
          const ny = ent.posOrig[1] + ady * delta
          const nz = ent.posOrig[2] + adz * delta
          ;(ent.inst as any).setLocation([nx, ny, nz])
        }
      } else if (ds.mode === 'scale') {
        const [adx, ady, adz] = AXIS_DIRS[ds.axis]
        const nearest = rayLineNearest(
          rox, roy, roz, rdx, rdy, rdz,
          origin[0], origin[1], origin[2], adx, ady, adz,
        )
        if (!nearest) return
        let factor = nearest.s / ds.anchorSignedDist
        if (factor < 0.01) factor = 0.01
        if (factor > 100) factor = 100
        ds.currentFactor = factor
        for (const ent of ds.entities) {
          const newScale = ent.scaleOrig[0] * factor
          if (typeof (ent.inst as any).uniformScale === 'function') {
            ;(ent.inst as any).uniformScale(newScale * ent.modelScale)
          }
        }
      } else if (ds.mode === 'rotate') {
        const hit = rayPlane(
          rox, roy, roz, rdx, rdy, rdz,
          origin[0], origin[1], origin[2],
          0, 0, 1,
        )
        if (!hit) return
        const angle = Math.atan2(hit.hy - origin[1], hit.hx - origin[0])
        const delta = wrapAngle(angle - ds.anchorAngle)
        ds.currentAngle = ds.anchorAngle + delta
        for (const ent of ds.entities) {
          const newRot = ent.rotOrig + delta
          const q = quatZ(newRot)
          if (typeof (ent.inst as any).setRotation === 'function') {
            ;(ent.inst as any).setRotation(q)
          }
        }
      }
    },

    onDragEnd(px, py, scene, canvas) {
      if (!dragState) return
      const ds = dragState

      if (ds.mode === 'move') {
        let delta = 0
        const ray = screenRay(px, py, scene, canvas)
        if (ray) {
          const [rox, roy, roz, rdx, rdy, rdz] = ray
          const [adx, ady, adz] = AXIS_DIRS[ds.axis]
          const nearest = rayLineNearest(
            rox, roy, roz, rdx, rdy, rdz,
            ds.origin[0], ds.origin[1], ds.origin[2], adx, ady, adz,
          )
          if (nearest) delta = nearest.s - ds.anchorParam
        }
        const [adx, ady, adz] = AXIS_DIRS[ds.axis]
        const ents = ds.entities
        ;(async () => {
          for (const ent of ents) {
            const nx = ent.posOrig[0] + adx * delta
            const ny = ent.posOrig[1] + ady * delta
            const nz = ent.posOrig[2] + adz * delta
            ;(ent.inst as any).setLocation([nx, ny, nz])
            const gameZ = nz - ent.moveHeight
            try {
              if (ent.kind === 'unit') await MoveUnit(ent.cn, nx, ny, gameZ)
              else                     await MoveDoodad(ent.cn, nx, ny, gameZ)
            } catch (err) {
              flog(`[gizmo] move commit ${ent.kind} cn=${ent.cn} failed:`, err instanceof Error ? err.message : String(err))
            }
          }
        })()
      } else if (ds.mode === 'rotate') {
        const delta = ds.currentAngle - ds.anchorAngle
        const ents = ds.entities
        ;(async () => {
          for (const ent of ents) {
            const newRot = ent.rotOrig + delta
            try {
              if (ent.kind === 'unit') await RotateUnit(ent.cn, newRot)
              else                     await RotateDoodad(ent.cn, newRot)
            } catch (err) {
              flog(`[gizmo] rotate commit ${ent.kind} cn=${ent.cn} failed:`, err instanceof Error ? err.message : String(err))
            }
          }
        })()
      } else if (ds.mode === 'scale') {
        const factor = ds.currentFactor
        const ents = ds.entities
        ;(async () => {
          for (const ent of ents) {
            const newS = ent.scaleOrig[0] * factor
            try {
              // Visual is uniform via scale[0]; we write uniform on-disk too
              // so the saved file matches what the user sees. Per-axis editing
              // belongs in Properties, not the gizmo.
              if (ent.kind === 'unit') await ScaleUnit(ent.cn, newS, newS, newS)
              else                     await ScaleDoodad(ent.cn, newS, newS, newS)
            } catch (err) {
              flog(`[gizmo] scale commit ${ent.kind} cn=${ent.cn} failed:`, err instanceof Error ? err.message : String(err))
            }
          }
        })()
      }

      dragState = null
      hoverAxis = null
    },

    cancelDrag() {
      if (!dragState) return
      for (const ent of dragState.entities) restoreEntity(ent)
      dragState = null
      hoverAxis = null
    },

    isDragging() { return dragState !== null },
    dragAxis()   { return dragState?.axis ?? null },
    dragMode()   { return dragState?.mode ?? null },

    dispose() {
      gl.deleteBuffer(cylVBO)
      gl.deleteBuffer(coneVBO)
      gl.deleteBuffer(ringVBO)
      gl.deleteBuffer(cubeVBO)
      gl.deleteProgram(prog.program)
      dragState = null
    },
  }
}
