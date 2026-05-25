// Test the mdx-m3-viewer parser on extracted MDX files WITH the wc3-forge
// runtime patch applied. Reports which files crash, with the actual error
// + byte offset. Mirrors the TS patch in frontend/src/mdx-parser-patch.ts
// in CommonJS form so we can run it directly with Node without bundling.
//
// Usage: node scripts/test-mdx-parse-with-patch.cjs <file1.mdx> [<file2.mdx> ...]

const fs = require('fs');
const path = require('path');

const MAIN_NM = 'C:/Users/4step/projects/wc3-forge/frontend/node_modules';
const Model = require(path.join(MAIN_NM, 'mdx-m3-viewer/dist/cjs/parsers/mdlx/model.js')).default;
const Layer = require(path.join(MAIN_NM, 'mdx-m3-viewer/dist/cjs/parsers/mdlx/layer.js')).default;
const Material = require(path.join(MAIN_NM, 'mdx-m3-viewer/dist/cjs/parsers/mdlx/material.js')).default;
const animationMap = require(path.join(MAIN_NM, 'mdx-m3-viewer/dist/cjs/parsers/mdlx/animationmap.js')).default;

// --- BEGIN: replicated patch ---
const origLayerReadMdx = Layer.prototype.readMdx;

function readLayerPostV1100(stream, version) {
  const start = stream.index;
  const size = stream.readUint32();
  this.filterMode = stream.readUint32();
  this.flags = stream.readUint32();
  stream.readInt32(); // ignored texture_id
  this.textureId = -1;
  this.textureAnimationId = stream.readInt32();
  this.coordId = stream.readUint32();
  this.alpha = stream.readFloat32();
  this.emissiveGain = stream.readFloat32();
  stream.readFloat32Array(this.fresnelColor);
  this.fresnelOpacity = stream.readFloat32();
  this.fresnelTeamColor = stream.readFloat32();
  const hd = stream.readUint32();
  const texsCount = stream.readUint32();
  this.hd = hd;
  let firstTexId = -1;
  for (let t = 0; t < texsCount; t++) {
    const id = stream.readInt32();
    stream.readInt32(); // slot (garbage)
    if (t === 0) firstTexId = id;
  }
  if (firstTexId >= 0) this.textureId = firstTexId;
  this.readAnimations(stream, size - (stream.index - start));
}

Layer.prototype.readMdx = function (stream, version) {
  if (version >= 1100) {
    readLayerPostV1100.call(this, stream, version);
  } else {
    origLayerReadMdx.call(this, stream, version);
  }
};

Material.prototype.readMdx = function (stream, version) {
  stream.readUint32();
  this.priorityPlane = stream.readInt32();
  this.flags = stream.readUint32();
  const u = stream.uint8array;
  const i = stream.index;
  let peekLAYS = false;
  if (i + 4 <= u.length) {
    peekLAYS = u[i] === 0x4C && u[i+1] === 0x41 && u[i+2] === 0x59 && u[i+3] === 0x53;
  }
  if (!peekLAYS && version > 800) {
    this.shader = stream.read(80);
  } else {
    this.shader = '';
  }
  stream.skip(4); // LAYS
  const numLayers = stream.readUint32();
  for (let n = 0; n < numLayers; n++) {
    const layer = new Layer();
    layer.readMdx(stream, version);
    this.layers.push(layer);
  }
};

const AnimatedObjectProto = Object.getPrototypeOf(Layer.prototype);
AnimatedObjectProto.readAnimations = function (stream, size) {
  const end = stream.index + size;
  while (stream.index < end) {
    const name = stream.readBinary(4);
    if (typeof name !== 'string' || name.length < 1 || name.charCodeAt(0) !== 0x4B) {
      stream.index = end;
      return;
    }
    const entry = animationMap ? animationMap[name] : undefined;
    if (entry) {
      const animation = new entry[1]();
      animation.readMdx(stream, name);
      this.animations.push(animation);
    } else {
      stream.index = end;
      return;
    }
  }
};
// --- END: replicated patch ---

const files = process.argv.slice(2);
if (files.length === 0) {
  console.error('Usage: node test-mdx-parse-with-patch.cjs <file.mdx> [...]');
  process.exit(1);
}

for (const file of files) {
  console.log('\n=== ' + file + ' ===');
  const data = fs.readFileSync(file);
  const view = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  const m = new Model();
  try {
    m.load(view);
    console.log(`  parsed OK. version=${m.version}`);
    console.log(`  sequences=${m.sequences.length} globalSequences=${m.globalSequences.length}`);
    console.log(`  materials=${m.materials.length} textures=${m.textures.length}`);
    console.log(`  geosets=${m.geosets.length} bones=${m.bones.length}`);
    console.log(`  unknownChunks=${m.unknownChunks.length}`);
  } catch (e) {
    console.log(`  PARSE ERROR: ${e.message}`);
    console.log(`  state at error:`);
    console.log(`    version=${m.version} materials=${m.materials.length}`);
    console.log(`    sequences=${m.sequences.length} textures=${m.textures.length}`);
    console.log(`    geosets=${m.geosets.length} bones=${m.bones.length}`);
    console.log(`  stack: ${e.stack.split('\n').slice(0,5).join('\n         ')}`);
  }
}
