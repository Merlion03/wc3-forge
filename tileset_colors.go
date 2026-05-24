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

// PaletteTexturePaths returns the `dir/file` asset path stem (no extension)
// for each palette FourCC, suitable for `viewer.load(path + ".dds", solver)`
// on the JS side. Empty string for entries whose Terrain.slk lookup failed.
// Same lookup path used by sampleTileColor — kept separate so each surface
// (color stand-in vs real texture) can fail independently.
func PaletteTexturePaths(palette []string) []string {
	out := make([]string, len(palette))
	m, err := loadTerrainSLK()
	if err != nil || m == nil {
		return out
	}
	for i, fc := range palette {
		row := m.Row(fc)
		if row == nil {
			log.Printf("tileset paths: %s not in Terrain.slk", fc)
			continue
		}
		dir := row.String("dir")
		file := row.String("file")
		if dir == "" || file == "" {
			log.Printf("tileset paths: %s missing dir/file in Terrain.slk", fc)
			continue
		}
		out[i] = dir + "/" + file
	}
	return out
}

// WaterInfo holds the per-tileset water rendering parameters HiveWE pulls
// from TerrainArt/Water.slk's `<tileset>Sha` row. The JS-side water shader
// uses these to compute the depth-based blend (shallow_min↔max for water
// thinner than the deep threshold, deep_min↔max for deeper water).
//
// All colors are RGBA 0..255, sourced verbatim from the SLK. WaterOffset is
// the additional Z offset applied per corner — different tilesets have
// the water surface sitting at slightly different heights above the
// raw `corner_water_height`. (Lordaeron: -0.5 typical; Icecrown: ice level.)
type WaterInfo struct {
	OK          bool     `json:"ok"`           // false if Water.slk lookup failed; JS uses fallback colors
	Offset      float64  `json:"offset"`       // added to per-vertex water_z (HiveWE units; JS multiplies by 128 for studs)
	ShallowMin  [4]uint8 `json:"shallow_min"`  // shallow water color at zero depth
	ShallowMax  [4]uint8 `json:"shallow_max"`  // shallow water color near the deeplevel boundary
	DeepMin     [4]uint8 `json:"deep_min"`     // deep water color just below deeplevel
	DeepMax     [4]uint8 `json:"deep_max"`     // deep water color at full saturation
	// TextureFile is the base path of the animated water frames; each frame is
	// `<TextureFile>NN.blp` (or .dds) where NN is the zero-padded 2-digit
	// frame index 0..NumTextures-1. e.g. "ReplaceableTextures\\Water\\LSWat"
	// → "ReplaceableTextures\\Water\\LSWat00.blp" through "...44.blp" for
	// Lordaeron (45 frames).
	TextureFile string `json:"texture_file"`
	NumTextures int    `json:"num_textures"` // animation frame count
	TextureRate int    `json:"texture_rate"` // frames per second
}

var (
	waterSLK     *slk.Mapped
	waterSLKErr  error
	waterSLKOnce sync.Once
)

func loadWaterSLK() (*slk.Mapped, error) {
	waterSLKOnce.Do(func() {
		data, ok, err := readBaseAsset("TerrainArt/Water.slk")
		if err != nil || !ok {
			waterSLKErr = err
			log.Printf("tileset: Water.slk read failed (ok=%v err=%v)", ok, err)
			return
		}
		m := slk.New()
		if err := m.Load(data); err != nil {
			waterSLKErr = err
			log.Printf("tileset: Water.slk parse: %v", err)
			return
		}
		waterSLK = m
		log.Printf("tileset: Water.slk loaded, %d rows", len(m.Rows))
	})
	return waterSLK, waterSLKErr
}

// WaterColors returns the water rendering parameters for the given tileset
// letter (e.g. 'L' for Lordaeron, 'I' for Icecrown). Falls back to a
// hardcoded plausible blue if Water.slk isn't accessible or the tileset
// has no water row — better to show SOME water than crash.
func WaterColors(tileset byte) WaterInfo {
	fallback := WaterInfo{
		OK:         false,
		Offset:     0,
		ShallowMin: [4]uint8{135, 170, 200, 100},
		ShallowMax: [4]uint8{100, 140, 180, 140},
		DeepMin:    [4]uint8{60, 100, 150, 180},
		DeepMax:    [4]uint8{30, 60, 110, 210},
	}
	m, err := loadWaterSLK()
	if err != nil || m == nil {
		return fallback
	}
	key := string([]byte{tileset, 'S', 'h', 'a'})
	row := m.Row(key)
	if row == nil {
		log.Printf("tileset: Water.slk has no row for %s", key)
		return fallback
	}
	rgba := func(prefix string) [4]uint8 {
		return [4]uint8{
			uint8(clampU8(row.Number(prefix + "_r"))),
			uint8(clampU8(row.Number(prefix + "_g"))),
			uint8(clampU8(row.Number(prefix + "_b"))),
			uint8(clampU8(row.Number(prefix + "_a"))),
		}
	}
	return WaterInfo{
		OK:          true,
		Offset:      row.Number("height"),
		ShallowMin:  rgba("smin"),
		ShallowMax:  rgba("smax"),
		DeepMin:     rgba("dmin"),
		DeepMax:     rgba("dmax"),
		TextureFile: row.String("texfile"),
		NumTextures: int(row.Number("numtex")),
		TextureRate: int(row.Number("texrate")),
	}
}

func clampU8(v float64) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return int(v)
}
