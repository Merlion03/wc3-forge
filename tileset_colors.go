package main

import (
	"bytes"
	"log"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/formats/dds"
	"github.com/StephenSHorton/wc3-forge/internal/formats/slk"
	"github.com/nielsAD/gowarcraft3/file/blp"
)

// Per-tile-FourCC representative color, sampled from the actual texture in
// CASC. Matches HiveWE's data flow: TerrainArt/Terrain.slk maps the FourCC
// to (dir, file), the texture file lives at that path with either .blp or
// .dds extension (Reforged ships DDS), and we sample one color from the
// decoded image rather than rendering the full UV-tiled texture (deferred
// work). This gives "real-looking" terrain color until full BLP/DDS
// rendering with per-tile UVs lands.

var (
	tileColorMu    sync.Mutex
	tileColorCache = map[string][3]uint8{}
	terrainSLK     *slk.Mapped
	terrainSLKErr  error
	terrainSLKOnce sync.Once
)

func loadTerrainSLK() (*slk.Mapped, error) {
	terrainSLKOnce.Do(func() {
		data, ok, err := readBaseAsset("TerrainArt/Terrain.slk")
		if err != nil || !ok {
			terrainSLKErr = err
			log.Printf("tileset: Terrain.slk read failed (ok=%v err=%v)", ok, err)
			return
		}
		m := slk.New()
		if err := m.Load(data); err != nil {
			terrainSLKErr = err
			log.Printf("tileset: Terrain.slk parse: %v", err)
			return
		}
		terrainSLK = m
		log.Printf("tileset: Terrain.slk loaded, %d rows", len(m.Rows))
	})
	return terrainSLK, terrainSLKErr
}

// tileColor returns the representative RGB color for the given tile FourCC,
// sampled from the actual CASC texture (BLP or DDS). Cached per process.
// Returns a fallback gray on lookup or decode failure so we don't fail the
// whole terrain render over one missing texture.
func tileColor(fourCC string) [3]uint8 {
	tileColorMu.Lock()
	if c, ok := tileColorCache[fourCC]; ok {
		tileColorMu.Unlock()
		return c
	}
	tileColorMu.Unlock()

	col := sampleTileColor(fourCC)

	tileColorMu.Lock()
	tileColorCache[fourCC] = col
	tileColorMu.Unlock()
	return col
}

func sampleTileColor(fourCC string) [3]uint8 {
	fallback := [3]uint8{102, 102, 110} // neutral medium gray
	m, err := loadTerrainSLK()
	if err != nil || m == nil {
		return fallback
	}
	row := m.Row(fourCC)
	if row == nil {
		log.Printf("tileset: %s not in Terrain.slk", fourCC)
		return fallback
	}
	dir := row.String("dir")
	file := row.String("file")
	if dir == "" || file == "" {
		log.Printf("tileset: %s missing dir/file in Terrain.slk", fourCC)
		return fallback
	}
	// Reforged CASC ships these as DDS; older SD installs may have BLP. Try
	// both extensions before giving up.
	for _, ext := range []string{".dds", ".blp"} {
		data, ok, err := readBaseAsset(dir + "/" + file + ext)
		if err != nil || !ok {
			continue
		}
		if r, g, b, err := dds.AverageColor(data); err == nil {
			return [3]uint8{r, g, b}
		}
		if img, err := blp.Decode(bytes.NewReader(data)); err == nil {
			// Sample center pixel — for tile textures that's a decent
			// representative pixel. If this turns out to bias toward
			// per-tile-detail features we'll switch to averaging.
			b := img.Bounds()
			cx, cy := b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2
			r, g, bl, _ := img.At(cx, cy).RGBA()
			return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)}
		}
	}
	log.Printf("tileset: %s decode failed for %s/%s.{dds,blp}", fourCC, dir, file)
	return fallback
}

// PaletteColors returns one RGB triplet per palette FourCC, in the same
// order as the input. Suitable for the JS terrain shader to color each
// tile by its real WC3 texture color.
func PaletteColors(palette []string) [][3]uint8 {
	out := make([][3]uint8, len(palette))
	for i, fc := range palette {
		out[i] = tileColor(fc)
	}
	return out
}
