// MDX/MDL → Three.js BufferGeometry converter.
//
// war3-model parses both formats into a common Model structure. We walk the
// model's Geosets and convert each to a BufferGeometry that Three.js can
// render as a Mesh. Geometry-only: bones, animations, particles, ribbons,
// attachments — all the rigging — are dropped. Static silhouette is enough
// for editor visualization.

import * as THREE from 'three'
import { parseMDL, parseMDX, type Model, type Geoset } from 'war3-model'

// Convert one Geoset to a BufferGeometry. WC3 models use Z-up; the scene
// uses Y-up. We DON'T rotate here — the caller's transform handles that
// (see scene.ts where unit meshes are placed: game (x, y, z) → three (x, z, -y)).
function geosetToGeometry(g: Geoset): THREE.BufferGeometry {
  const geom = new THREE.BufferGeometry()
  geom.setAttribute('position', new THREE.BufferAttribute(g.Vertices, 3))

  // Normals are optional in MDL; some hand-crafted models omit them.
  if (g.Normals && g.Normals.length === g.Vertices.length) {
    geom.setAttribute('normal', new THREE.BufferAttribute(g.Normals, 3))
  }

  // First texture coordinate channel only — WC3 models occasionally have
  // a second channel for environment mapping; we ignore it.
  if (g.TVertices && g.TVertices.length > 0 && g.TVertices[0]) {
    const expected = (g.Vertices.length / 3) * 2
    if (g.TVertices[0].length === expected) {
      geom.setAttribute('uv', new THREE.BufferAttribute(g.TVertices[0], 2))
    }
  }

  geom.setIndex(new THREE.BufferAttribute(g.Faces, 1))
  if (!geom.getAttribute('normal')) {
    geom.computeVertexNormals()
  }
  return geom
}

// Returns one geometry per Geoset, in source order. For models with only one
// geoset (most placeholders, simple props), use modelToGeometry instead.
export function modelToGeometries(model: Model): THREE.BufferGeometry[] {
  return (model.Geosets ?? []).map(geosetToGeometry)
}

// Returns the FIRST geoset's geometry. Suitable for placeholder / single-mesh
// models. Throws if the model has no geosets.
export function modelToGeometry(model: Model): THREE.BufferGeometry {
  const list = modelToGeometries(model)
  if (list.length === 0) throw new Error('MDX/MDL has no geosets')
  return list[0]
}

export function loadMdx(buffer: ArrayBuffer): Model {
  return parseMDX(buffer)
}

export function loadMdl(text: string): Model {
  return parseMDL(text)
}

// A hand-written minimal MDL — a 5-vertex pyramid with apex pointing up
// in WC3's +Z. Used as the default model template for any entity whose
// real MDX isn't available. Self-authored to avoid license concerns;
// covers the same parse path real MDX files would take.
//
// Shape: square base at Z=0 (corners at ±64), apex at (0, 0, 128).
export const PLACEHOLDER_PYRAMID_MDL = `Version {
	FormatVersion 800,
}
Model "wc3forge-placeholder-pyramid" {
	NumGeosets 1,
	BlendTime 150,
	MinimumExtent { -64, -64, 0 },
	MaximumExtent { 64, 64, 128 },
}
Sequences 1 {
	Anim "Stand" {
		Interval { 0, 1000 },
		MinimumExtent { -64, -64, 0 },
		MaximumExtent { 64, 64, 128 },
		BoundsRadius 128,
	}
}
Textures 1 {
	Bitmap {
		Image "",
	}
}
Materials 1 {
	Material {
		Layer {
			FilterMode None,
			static TextureID 0,
		}
	}
}
Geoset {
	Vertices 5 {
		{ 0, 0, 128 },
		{ -64, -64, 0 },
		{ 64, -64, 0 },
		{ 64, 64, 0 },
		{ -64, 64, 0 },
	}
	Normals 5 {
		{ 0, 0, 1 },
		{ -0.577, -0.577, 0.577 },
		{ 0.577, -0.577, 0.577 },
		{ 0.577, 0.577, 0.577 },
		{ -0.577, 0.577, 0.577 },
	}
	TVertices 5 {
		{ 0.5, 0.5 },
		{ 0, 0 },
		{ 1, 0 },
		{ 1, 1 },
		{ 0, 1 },
	}
	VertexGroup {
		0,
		0,
		0,
		0,
		0,
	}
	Faces 1 12 {
		Triangles {
			{ 0, 1, 2, 0, 2, 3, 0, 3, 4, 0, 4, 1 },
		}
	}
	Groups 1 1 {
		Matrices { 0 },
	}
	MinimumExtent { -64, -64, 0 },
	MaximumExtent { 64, 64, 128 },
	BoundsRadius 128,
	MaterialID 0,
	SelectionGroup 0,
}
Bone "root" {
	ObjectId 0,
	GeosetId 0,
}
`
