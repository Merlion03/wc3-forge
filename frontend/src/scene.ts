// Three.js scene wrapper for the wc3-forge viewport.
//
// Coordinate mapping: WC3 game coords are (X right, Y north, Z up).
// Three.js default is Y-up. We map (gx, gy, gz) → (gx, gz, -gy) so the WC3
// XY plane lies flat on Three's XZ plane and elevation is Three's Y.

import * as THREE from 'three'
import { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js'

export interface TerrainData {
  width: number
  height: number
  center_offset: [number, number]
  heights: number[]
  ground_tex: number[]
  palette: string[]
}

export interface UnitData {
  creation_number: number
  type_id: string
  player: number
  position: [number, number, number]
  scale: [number, number, number]
}

export interface SceneAPI {
  setTerrain(t: TerrainData | null): void
  setUnits(units: UnitData[]): void
  setSelection(creationNumbers: number[]): void
  onPick(cb: (creationNumber: number | null) => void): void
  dispose(): void
}

// Classic Warcraft III 24-slot player palette (R=0, B=1, … brown=11; UD
// brown / black / etc. start at 12 in Reforged). Hex 0xRRGGBB.
const PLAYER_COLORS: number[] = [
  0xff0303, 0x0042ff, 0x1ce6b9, 0x540081, 0xfffc01, 0xfeba0e, 0x20c000, 0xe55bb0,
  0x959697, 0x7ebff1, 0x106246, 0x4e2a04,
  0x9b0000, 0x0000c3, 0x00ebff, 0xbd00ff, 0xece1d4, 0x494b6a, 0xb500b5, 0x99ffff,
  0xae6c00, 0xcce0a8, 0xebd460, 0xb09d7e,
]
const NEUTRAL_PASSIVE = 0x282828

function playerColor(p: number): number {
  if (p === 15) return NEUTRAL_PASSIVE // wc3 neutral passive slot
  if (p < 0 || p >= PLAYER_COLORS.length) return 0xcccccc
  return PLAYER_COLORS[p]
}

// Loose per-tileset-index tints. Real implementation would sample the
// tileset BLP textures; this is enough to distinguish dirt vs grass vs
// rock visually.
const GROUND_TINTS = [
  0x6a5a3a, 0x8a7a4a, 0x7a8a3a, 0x6a6a6a, 0x4a7530, 0x6a8050,
  0x707070, 0x607050, 0x4f8030, 0x404040, 0x4f8030, 0x4f8030,
  0x4f8030, 0x4f8030, 0x4f8030, 0x4f8030,
]

export function createScene(canvas: HTMLCanvasElement): SceneAPI {
  const scene = new THREE.Scene()
  scene.background = new THREE.Color(0x121214)

  const camera = new THREE.PerspectiveCamera(
    45,
    canvas.clientWidth / canvas.clientHeight,
    10,
    50000
  )
  camera.position.set(4000, 4000, 4000)

  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true })
  renderer.setPixelRatio(window.devicePixelRatio)
  renderer.setSize(canvas.clientWidth, canvas.clientHeight, false)

  const controls = new OrbitControls(camera, renderer.domElement)
  controls.target.set(0, 0, 0)
  controls.update()

  scene.add(new THREE.AmbientLight(0xffffff, 0.55))
  const sun = new THREE.DirectionalLight(0xffffff, 0.9)
  sun.position.set(2000, 5000, 2000)
  scene.add(sun)

  // Axis helper at origin for orientation cue (small, fades into terrain).
  const axes = new THREE.AxesHelper(200)
  scene.add(axes)

  // State buckets.
  let terrainMesh: THREE.Mesh | null = null
  const unitMeshes = new Map<number, THREE.Mesh>()
  const unitOriginalColor = new Map<number, number>()
  const selected = new Set<number>()

  // Shared unit geometry (cheaper than per-unit Box).
  const unitGeom = new THREE.BoxGeometry(80, 160, 80)

  // Raycaster for click picking.
  const raycaster = new THREE.Raycaster()
  let pickCallback: ((cn: number | null) => void) | null = null

  function onCanvasClick(ev: MouseEvent) {
    if (ev.button !== 0) return
    const rect = canvas.getBoundingClientRect()
    const mouse = new THREE.Vector2(
      ((ev.clientX - rect.left) / rect.width) * 2 - 1,
      -((ev.clientY - rect.top) / rect.height) * 2 + 1
    )
    raycaster.setFromCamera(mouse, camera)
    const hits = raycaster.intersectObjects(Array.from(unitMeshes.values()), false)
    if (pickCallback) {
      if (hits.length > 0) {
        const cn = hits[0].object.userData.creation_number as number
        pickCallback(cn)
      } else {
        pickCallback(null)
      }
    }
  }
  canvas.addEventListener('click', onCanvasClick)

  function onResize() {
    const w = canvas.clientWidth
    const h = canvas.clientHeight
    if (w === 0 || h === 0) return
    camera.aspect = w / h
    camera.updateProjectionMatrix()
    renderer.setSize(w, h, false)
  }
  const ro = new ResizeObserver(onResize)
  ro.observe(canvas)

  let rafId = 0
  function loop() {
    controls.update()
    renderer.render(scene, camera)
    rafId = requestAnimationFrame(loop)
  }
  loop()

  function clearTerrain() {
    if (!terrainMesh) return
    scene.remove(terrainMesh)
    terrainMesh.geometry.dispose()
    ;(terrainMesh.material as THREE.Material).dispose()
    terrainMesh = null
  }

  function clearUnits() {
    for (const m of unitMeshes.values()) {
      scene.remove(m)
      ;(m.material as THREE.Material).dispose()
    }
    unitMeshes.clear()
    unitOriginalColor.clear()
    selected.clear()
  }

  function applyUnitColor(cn: number) {
    const mesh = unitMeshes.get(cn)
    if (!mesh) return
    const mat = mesh.material as THREE.MeshLambertMaterial
    if (selected.has(cn)) {
      mat.color.set(0xffff00)
      mat.emissive.set(0x444400)
    } else {
      mat.color.set(unitOriginalColor.get(cn) ?? 0xcccccc)
      mat.emissive.set(0x000000)
    }
  }

  return {
    setTerrain(t) {
      clearTerrain()
      if (!t) return
      const tileStep = 128
      const xExtent = (t.width - 1) * tileStep
      const yExtent = (t.height - 1) * tileStep

      // PlaneGeometry: t.width segments along X = t.width+1 vertices? No —
      // PlaneGeometry(widthExt, heightExt, widthSegs, heightSegs) produces
      // (widthSegs+1) * (heightSegs+1) vertices. We have t.width vertices
      // along X, so use widthSegs = t.width - 1.
      const geom = new THREE.PlaneGeometry(xExtent, yExtent, t.width - 1, t.height - 1)
      // After construction, vertices are arranged row-major in the XY plane
      // (Y is the second axis). We rebuild positions to land in our game-coord
      // frame (gx, gz, -gy).
      const pos = geom.attributes.position as THREE.BufferAttribute
      const colors = new Float32Array(pos.count * 3)
      for (let row = 0; row < t.height; row++) {
        for (let col = 0; col < t.width; col++) {
          const i = row * t.width + col
          const gx = t.center_offset[0] + col * tileStep
          const gy = t.center_offset[1] + row * tileStep
          const gz = t.heights[i]
          // PlaneGeometry's natural vertex order is row 0 at top in its
          // local +Y; we want row 0 at bottom (game-south). Mirror the row
          // index when reading from our heights array.
          // Actually: we generate vertex i = row * width + col below, and
          // we control which game vertex maps to it, so no mirroring needed
          // as long as setXYZ is called by the same (row, col) loop.
          pos.setXYZ(i, gx, gz, -gy)
          const tex = t.ground_tex[i] & 0x0f
          const tint = GROUND_TINTS[tex] ?? 0x4a7530
          colors[i * 3] = ((tint >> 16) & 0xff) / 255
          colors[i * 3 + 1] = ((tint >> 8) & 0xff) / 255
          colors[i * 3 + 2] = (tint & 0xff) / 255
        }
      }
      pos.needsUpdate = true
      geom.setAttribute('color', new THREE.BufferAttribute(colors, 3))
      geom.computeVertexNormals()

      const mat = new THREE.MeshLambertMaterial({
        vertexColors: true,
        side: THREE.DoubleSide,
      })
      terrainMesh = new THREE.Mesh(geom, mat)
      scene.add(terrainMesh)

      // Reframe camera roughly on the map center.
      const cx = t.center_offset[0] + xExtent / 2
      const cy = t.center_offset[1] + yExtent / 2
      const tx = cx
      const tz = -cy
      controls.target.set(tx, 0, tz)
      const d = Math.max(xExtent, yExtent) * 0.9
      camera.position.set(tx + d * 0.5, d * 0.6, tz + d * 0.5)
      controls.update()
    },

    setUnits(units) {
      clearUnits()
      for (const u of units) {
        const color = playerColor(u.player)
        const mat = new THREE.MeshLambertMaterial({ color })
        const mesh = new THREE.Mesh(unitGeom, mat)
        mesh.position.set(u.position[0], u.position[2] + 80, -u.position[1])
        mesh.userData.creation_number = u.creation_number
        scene.add(mesh)
        unitMeshes.set(u.creation_number, mesh)
        unitOriginalColor.set(u.creation_number, color)
      }
    },

    setSelection(creationNumbers) {
      const next = new Set(creationNumbers)
      // Restore previously selected items.
      for (const cn of selected) {
        if (!next.has(cn)) {
          selected.delete(cn)
          applyUnitColor(cn)
        }
      }
      // Apply highlight to newly selected.
      for (const cn of next) {
        if (!selected.has(cn)) {
          selected.add(cn)
          applyUnitColor(cn)
        }
      }
    },

    onPick(cb) {
      pickCallback = cb
    },

    dispose() {
      cancelAnimationFrame(rafId)
      ro.disconnect()
      canvas.removeEventListener('click', onCanvasClick)
      clearTerrain()
      clearUnits()
      unitGeom.dispose()
      renderer.dispose()
    },
  }
}
