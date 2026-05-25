// Probe an MDX file with the wc3-forge MDX parser patch applied (Reforged v1100+
// layer format + safety-belt readAnimations). Use to inspect bones / PE2s /
// sequences / animations of a model before vs after a fix.

const fs = require('fs')
const path = require('path')

const NM = path.join(__dirname, '..', 'frontend', 'node_modules')
const Model = require(path.join(NM, 'mdx-m3-viewer/dist/cjs/parsers/mdlx/model.js')).default
const Layer = require(path.join(NM, 'mdx-m3-viewer/dist/cjs/parsers/mdlx/layer.js')).default
const Material = require(path.join(NM, 'mdx-m3-viewer/dist/cjs/parsers/mdlx/material.js')).default
const animationMap = require(path.join(NM, 'mdx-m3-viewer/dist/cjs/parsers/mdlx/animationmap.js')).default

// Replicated patch — keep in sync with frontend/src/mdx-parser-patch.ts
const origLayerReadMdx = Layer.prototype.readMdx
function readLayerPostV1100(stream, version) {
  const start = stream.index
  const size = stream.readUint32()
  this.filterMode = stream.readUint32()
  this.flags = stream.readUint32()
  stream.readInt32()
  this.textureId = -1
  this.textureAnimationId = stream.readInt32()
  this.coordId = stream.readUint32()
  this.alpha = stream.readFloat32()
  this.emissiveGain = stream.readFloat32()
  stream.readFloat32Array(this.fresnelColor)
  this.fresnelOpacity = stream.readFloat32()
  this.fresnelTeamColor = stream.readFloat32()
  stream.readUint32() // hd
  const texsCount = stream.readUint32()
  let firstTexId = -1
  for (let t = 0; t < texsCount; t++) {
    const id = stream.readInt32()
    stream.readInt32()
    if (t === 0) firstTexId = id
  }
  if (firstTexId >= 0) this.textureId = firstTexId
  this.readAnimations(stream, size - (stream.index - start))
}
Layer.prototype.readMdx = function(stream, version) {
  if (version >= 1100) readLayerPostV1100.call(this, stream, version)
  else origLayerReadMdx.call(this, stream, version)
}
Material.prototype.readMdx = function(stream, version) {
  stream.readUint32()
  this.priorityPlane = stream.readInt32()
  this.flags = stream.readUint32()
  const u = stream.uint8array, i = stream.index
  let peekLAYS = false
  if (i + 4 <= u.length) {
    peekLAYS = u[i] === 0x4C && u[i+1] === 0x41 && u[i+2] === 0x59 && u[i+3] === 0x53
  }
  if (!peekLAYS && version > 800) this.shader = stream.read(80)
  else this.shader = ''
  stream.skip(4)
  const numLayers = stream.readUint32()
  for (let n = 0; n < numLayers; n++) {
    const layer = new Layer()
    layer.readMdx(stream, version)
    this.layers.push(layer)
  }
}
const AnimatedObjectProto = Object.getPrototypeOf(Layer.prototype)
AnimatedObjectProto.readAnimations = function(stream, size) {
  const end = stream.index + size
  while (stream.index < end) {
    const name = stream.readBinary(4)
    if (typeof name !== 'string' || name.length < 1 || name.charCodeAt(0) !== 0x4B) { stream.index = end; return }
    const entry = animationMap[name]
    if (entry) {
      const animation = new entry[1]()
      animation.readMdx(stream, name)
      this.animations.push(animation)
    } else { stream.index = end; return }
  }
}

const file = process.argv[2]
if (!file) { console.error('usage: probe-statue.cjs <file.mdx>'); process.exit(1) }
const buf = fs.readFileSync(file)
const model = new Model()
try { model.load(new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength)) } catch(e) { console.error('load:', e.message) }

console.log('FILE:', file)
console.log('Version:', model.version)
console.log('Bones:', model.bones.length, 'PE2:', model.particleEmitters2.length, 'PE:', model.particleEmitters.length, 'Ribbons:', model.ribbonEmitters.length, 'Helpers:', model.helpers.length)
console.log('Pivots[0..15]:')
for (let i = 0; i < Math.min(model.pivotPoints.length, 16); i++) {
  console.log(`  ${i}: [${model.pivotPoints[i]?.map(v=>v.toFixed(2)).join(',')}]`)
}
console.log('Sequences:')
for (const s of model.sequences) {
  console.log(`  "${s.name}" interval=[${s.interval[0]}..${s.interval[1]}] dur=${s.interval[1]-s.interval[0]}ms rarity=${s.rarity} nonLooping=${s.nonLooping}`)
}
console.log('\nParticle Emitters 2:')
for (const pe of model.particleEmitters2) {
  console.log(`  name="${pe.name}" objId=${pe.objectId} parent=${pe.parentId} flags=0x${pe.flags.toString(16)} squirt=${pe.squirt}`)
  console.log(`    speed=${pe.speed} latitude=${pe.latitude} gravity=${pe.gravity} lifeSpan=${pe.lifeSpan} emissionRate=${pe.emissionRate}`)
  console.log(`    width=${pe.width} length=${pe.length}`)
  console.log(`    headOrTail=${pe.headOrTail} numAnimations=${pe.animations.length}`)
  for (const a of pe.animations) {
    console.log(`      anim ${a.name} interp=${a.interpolationType ?? '?'} tracks=${a.tracks?.length ?? '?'}`)
  }
}
console.log('\nBones (all):')
for (let i=0; i<model.bones.length; i++) {
  const b = model.bones[i]
  const flags = b.flags
  const desc = []
  if (flags & 0x100) desc.push('Bone')
  if (flags & 0x8) desc.push('Billboarded')
  if (flags & 0x10) desc.push('BillboardedLockX')
  if (flags & 0x20) desc.push('BillboardedLockY')
  if (flags & 0x40) desc.push('BillboardedLockZ')
  console.log(`  ${i}: "${b.name}" objId=${b.objectId} parent=${b.parentId} flags=0x${flags.toString(16)} (${desc.join(',')}) anims=${b.animations.length}`)
  for (const a of b.animations) {
    console.log(`      anim ${a.name} interp=${a.interpolationType} tracks=${a.tracks?.length}`)
  }
}
console.log('\nHelpers:')
for (let i=0; i<model.helpers.length; i++) {
  const b = model.helpers[i]
  console.log(`  ${i}: "${b.name}" objId=${b.objectId} parent=${b.parentId} flags=0x${b.flags.toString(16)}`)
}
