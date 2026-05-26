package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"log"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/formats/dds"
	"github.com/nielsAD/gowarcraft3/file/blp"
)

// thumbSize is the edge length of each tile thumbnail in CSS pixels. 96 px
// is large enough to actually recognise patterns (grass blades, brick
// joints) at a glance — the previous 48-px crop was hard to read. 200
// thumbnails at 96² PNG ≈ 1.5 MB of base64; still a fine one-shot payload
// from Go → JS over the Wails channel.
const thumbSize = 96

var (
	tileThumbMu    sync.Mutex
	tileThumbCache = map[string]string{} // FourCC → "data:image/png;base64,…", "" = miss
)

// tileThumbnail returns a data-URL PNG thumbnail of the given ground-tile
// FourCC's first sub-tile (top-left 64×64 quadrant of the WC3 tileset
// texture). Empty string on lookup or decode failure — caller should fall
// back to the per-tile color swatch.
//
// Cached per process. First call per FourCC is ~3 ms; subsequent are a map
// hit. We deliberately bake thumbnails into ListTilesets so the dialog can
// render the entire palette without a per-tile RPC round-trip.
func tileThumbnail(fourCC string) string {
	tileThumbMu.Lock()
	if s, ok := tileThumbCache[fourCC]; ok {
		tileThumbMu.Unlock()
		return s
	}
	tileThumbMu.Unlock()

	url := buildTileThumbnail(fourCC, "TerrainArt/Terrain.slk", "dir", "file")

	tileThumbMu.Lock()
	tileThumbCache[fourCC] = url
	tileThumbMu.Unlock()
	return url
}

// cliffThumbnail is the same idea as tileThumbnail but resolves through
// CliffTypes.slk's (texdir, texfile) columns instead of Terrain.slk's
// (dir, file). Cliff textures live in a separate part of CASC so we keep
// the two paths distinct rather than overloading tileThumbnail.
func cliffThumbnail(fourCC string) string {
	tileThumbMu.Lock()
	if s, ok := tileThumbCache["cliff:"+fourCC]; ok {
		tileThumbMu.Unlock()
		return s
	}
	tileThumbMu.Unlock()

	url := buildTileThumbnail(fourCC, "TerrainArt/CliffTypes.slk", "texdir", "texfile")

	tileThumbMu.Lock()
	tileThumbCache["cliff:"+fourCC] = url
	tileThumbMu.Unlock()
	return url
}

// buildTileThumbnail resolves the FourCC through the given SLK + columns to
// a CASC texture path, decodes the texture (DDS first, BLP fallback), crops
// the top-left sub-tile, downsamples to thumbSize, and returns a PNG data
// URL.
func buildTileThumbnail(fourCC, slkAsset, dirCol, fileCol string) string {
	var (
		m *struct{} // placeholder; real loader returns *slk.Mapped via the right helper
	)
	_ = m

	// Branch on which SLK we want. Two-line dispatch is clearer than passing
	// the loader func around — there are only ever two cases here.
	var dir, file string
	switch slkAsset {
	case "TerrainArt/Terrain.slk":
		ts, _ := loadTerrainSLK()
		if ts == nil {
			return ""
		}
		row := ts.Row(fourCC)
		if row == nil {
			return ""
		}
		dir = row.String(dirCol)
		file = row.String(fileCol)
	case "TerrainArt/CliffTypes.slk":
		ts, _ := loadCliffTypesSLK()
		if ts == nil {
			return ""
		}
		row := ts.Row(fourCC)
		if row == nil {
			return ""
		}
		dir = row.String(dirCol)
		file = row.String(fileCol)
	default:
		return ""
	}
	if dir == "" || file == "" {
		return ""
	}

	// Try .dds first (Reforged), then .blp (Classic).
	var img image.Image
	for _, ext := range []string{".dds", ".blp"} {
		data, ok, err := readBaseAsset(dir + "/" + file + ext)
		if err != nil || !ok {
			continue
		}
		if ext == ".dds" {
			if decoded, err := dds.Decode(data); err == nil {
				img = decoded
				break
			} else {
				log.Printf("thumb: %s DDS decode failed: %v", fourCC, err)
			}
		} else {
			if decoded, err := blp.Decode(bytes.NewReader(data)); err == nil {
				img = decoded
				break
			}
		}
	}
	if img == nil {
		log.Printf("thumb: %s decode failed for %s/%s.{dds,blp}", fourCC, dir, file)
		return ""
	}

	thumb := cropAndResize(img)

	var buf bytes.Buffer
	if err := png.Encode(&buf, thumb); err != nil {
		log.Printf("thumb: %s PNG encode failed: %v", fourCC, err)
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// cropAndResize takes the top-left sub-tile of src (one quarter of the
// smaller axis, since WC3 ground-tile atlases are a 4×N or 8×N grid of
// equal-size sub-tiles) and resamples it to thumbSize × thumbSize via
// nearest-neighbor.
//
// The crop is ALWAYS exactly one sub-tile, regardless of how big thumbSize
// is — earlier code auto-grew the crop to ≥ thumbSize and ended up spanning
// sub-tile boundaries, which made each preview look like two adjacent
// variants stitched together. If the source sub-tile is smaller than
// thumbSize, we upsample (stretch) to fill the swatch; if it's bigger, we
// downsample.
//
// Nearest-neighbor is fast (one inner loop) and looks fine at 96×96 for
// detail-heavy tile textures. Box-filter would be marginally cleaner but
// pulls in golang.org/x/image as a dep we don't otherwise need.
func cropAndResize(src image.Image) image.Image {
	b := src.Bounds()
	srcW := b.Dx()
	srcH := b.Dy()
	sub := srcW
	if srcH < sub {
		sub = srcH
	}
	sub = sub / 4
	if sub < 1 {
		sub = 1
	}
	if sub > srcW {
		sub = srcW
	}
	if sub > srcH {
		sub = srcH
	}

	out := image.NewRGBA(image.Rect(0, 0, thumbSize, thumbSize))
	for dy := 0; dy < thumbSize; dy++ {
		sy := b.Min.Y + (dy*sub)/thumbSize
		for dx := 0; dx < thumbSize; dx++ {
			sx := b.Min.X + (dx*sub)/thumbSize
			r, g, bl, a := src.At(sx, sy).RGBA()
			off := dy*out.Stride + dx*4
			out.Pix[off+0] = uint8(r >> 8)
			out.Pix[off+1] = uint8(g >> 8)
			out.Pix[off+2] = uint8(bl >> 8)
			out.Pix[off+3] = uint8(a >> 8)
		}
	}
	return out
}
