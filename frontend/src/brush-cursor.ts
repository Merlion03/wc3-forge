// brush-cursor.ts — terrain BRUSH footprint ring, drawn on the ground under the
// cursor while the Terrain Palette is armed. A circle (line loop) or square
// outline at the brush radius, sampled to terrain height so it hugs the surface,
// rendered always-on-top after the main pass (same overlay playbook as
// cell-highlight.ts / sloc-markers.ts).
//
// Lifecycle: buildBrushCursor(gl) once per scene. setBrush({cx,cy,radius,shape,
// sampleZ}) on hover-move in brush mode; setBrush(null) to hide. draw(viewProj)
// every frame. dispose() on teardown.
//
// radius is in WORLD units (studs): the palette's corner-radius maps to
// (cornerRadius + 0.5) * 128 so the ring visually matches the footprint the Go
// brush actually edits (its circle threshold is (r+0.5)²).

import { flog } from './debuglog'

const VERT_SHADER = `
attribute vec3 a_position;
uniform mat4 u_viewProj;
void main() {
  gl_Position = u_viewProj * vec4(a_position, 1.0);
}
`.trim()

const FRAG_SHADER = `
precision mediump float;
uniform vec3 u_color;
uniform float u_alpha;
void main() {
  gl_FragColor = vec4(u_color, u_alpha);
}
`.trim()

function compileShader(gl: WebGLRenderingContext, type: number, source: string): WebGLShader {
  const sh = gl.createShader(type)!
  gl.shaderSource(sh, source)
  gl.compileShader(sh)
  if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
    const log = gl.getShaderInfoLog(sh)
    gl.deleteShader(sh)
    throw new Error('brush-cursor shader compile: ' + log)
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
    throw new Error('brush-cursor program link: ' + log)
  }
  return {
    program,
    aPosition: gl.getAttribLocation(program, 'a_position'),
    uViewProj: gl.getUniformLocation(program, 'u_viewProj')!,
    uColor: gl.getUniformLocation(program, 'u_color')!,
    uAlpha: gl.getUniformLocation(program, 'u_alpha')!,
  }
}

export interface BrushCursorState {
  cx: number                       // center world X (game coords)
  cy: number                       // center world Y
  radius: number                   // ring radius in world units (studs)
  shape: 'circle' | 'square'
  sampleZ: (x: number, y: number) => number // terrain height sampler
  color?: [number, number, number] // ring color (default cyan)
}

export interface BrushCursor {
  /** Position + size the ring (or null to hide). */
  setBrush(state: BrushCursorState | null): void
  /** Draw the ring (no-op when hidden). */
  draw(viewProj: Float32Array): void
  /** Release GL resources. */
  dispose(): void
}

// Lift the ring slightly above the sampled surface so it doesn't z-fight the
// terrain quad it traces.
const Z_LIFT = 6
// Circle segment count — smooth enough at any zoom without being wasteful.
const CIRCLE_SEGMENTS = 48

export function buildBrushCursor(gl: WebGLRenderingContext): BrushCursor | null {
  let prog: ReturnType<typeof buildProgram>
  try {
    prog = buildProgram(gl)
  } catch (e) {
    flog('[brush-cursor] program build failed:', e instanceof Error ? e.message : String(e))
    return null
  }

  const vbo = gl.createBuffer()!
  let vertCount = 0
  let color: [number, number, number] = [0.2, 0.85, 1.0]
  let visible = false

  // Build the ring positions for the current state and upload them. We sample
  // terrain Z per ring vertex so the outline hugs hills and cliffs.
  function rebuild(state: BrushCursorState) {
    const pts: number[] = []
    if (state.shape === 'square') {
      const r = state.radius
      const corners: [number, number][] = [
        [state.cx - r, state.cy - r],
        [state.cx + r, state.cy - r],
        [state.cx + r, state.cy + r],
        [state.cx - r, state.cy + r],
      ]
      // Walk the square as a closed loop, subdividing each edge so the height
      // sampling follows the terrain along the edge rather than just at corners.
      const SUBDIV = 8
      for (let e = 0; e < 4; e++) {
        const [ax, ay] = corners[e]
        const [bx, by] = corners[(e + 1) % 4]
        for (let s = 0; s < SUBDIV; s++) {
          const f = s / SUBDIV
          const x = ax + (bx - ax) * f
          const y = ay + (by - ay) * f
          pts.push(x, y, state.sampleZ(x, y) + Z_LIFT)
        }
      }
    } else {
      for (let i = 0; i < CIRCLE_SEGMENTS; i++) {
        const a = (i / CIRCLE_SEGMENTS) * Math.PI * 2
        const x = state.cx + Math.cos(a) * state.radius
        const y = state.cy + Math.sin(a) * state.radius
        pts.push(x, y, state.sampleZ(x, y) + Z_LIFT)
      }
    }
    vertCount = pts.length / 3
    gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(pts), gl.STREAM_DRAW)
  }

  return {
    setBrush(state: BrushCursorState | null) {
      if (!state) {
        visible = false
        return
      }
      visible = true
      color = state.color ?? [0.2, 0.85, 1.0]
      rebuild(state)
    },
    draw(viewProj: Float32Array) {
      if (!visible || vertCount === 0) return
      gl.useProgram(prog.program)
      gl.bindBuffer(gl.ARRAY_BUFFER, vbo)
      const maxAttribs = gl.getParameter(gl.MAX_VERTEX_ATTRIBS) | 0
      for (let i = 0; i < maxAttribs; i++) gl.disableVertexAttribArray(i)
      gl.enableVertexAttribArray(prog.aPosition)
      gl.vertexAttribPointer(prog.aPosition, 3, gl.FLOAT, false, 12, 0)
      gl.uniformMatrix4fv(prog.uViewProj, false, viewProj)
      gl.uniform3f(prog.uColor, color[0], color[1], color[2])
      gl.uniform1f(prog.uAlpha, 0.9)
      // Always-on-top, matching cell-highlight's confident-marker convention.
      gl.disable(gl.DEPTH_TEST)
      gl.depthMask(false)
      gl.disable(gl.CULL_FACE)
      gl.disable(gl.BLEND)
      gl.lineWidth(2)
      // LINE_LOOP closes the ring back to vertex 0 in one call.
      gl.drawArrays(gl.LINE_LOOP, 0, vertCount)
      gl.disableVertexAttribArray(prog.aPosition)
      gl.enable(gl.DEPTH_TEST)
      gl.depthMask(true)
    },
    dispose() {
      gl.deleteBuffer(vbo)
      gl.deleteProgram(prog.program)
      visible = false
    },
  }
}
