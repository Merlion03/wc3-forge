package main

import (
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/casc"
	"github.com/StephenSHorton/wc3-forge/internal/forge"
)

// assetHandler serves /asset/<path> requests from Go-side asset sources.
// mdx-m3-viewer's pathSolver returns "/asset/<src>" strings; the viewer
// then XHR-fetches via Wails' embedded HTTP server, which routes here.
//
// Resolution order (first hit wins):
//  1. The currently-loaded map's archive/folder (custom imports + per-map
//     overrides + the map's own files like war3map.w3i if requested).
//  2. CASC mount on the user's WC3 install — for stock unit/doodad
//     models, tileset textures, team-color BLPs, etc.
type assetHandler struct{}

func newAssetHandler() http.Handler {
	return &assetHandler{}
}

// Lazy-init the WC3 CASC storage. We don't fail wc3-forge to start if
// CASC isn't available — folder-based maps still work, and the user may
// not have WC3 installed at the expected path.
var (
	cascStorage *casc.Storage
	cascOnce    sync.Once
	cascErr     error
)

func wc3InstallPath() string {
	if p := os.Getenv("WC3FORGE_WC3_PATH"); p != "" {
		return p
	}
	return wc3InstallPathDefault
}

func getCASC() (*casc.Storage, error) {
	cascOnce.Do(func() {
		path := wc3InstallPath()
		log.Printf("CASC: opening storage at %q", path)
		cascStorage, cascErr = casc.Open(path)
		if cascErr != nil {
			log.Printf("CASC: open failed: %v", cascErr)
		} else {
			log.Printf("CASC: storage open")
		}
	})
	return cascStorage, cascErr
}

func (h *assetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/asset/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	requested := r.URL.Path[len(prefix):]
	if requested == "" {
		http.Error(w, "missing asset path", http.StatusBadRequest)
		return
	}
	// Normalize: lowercase + forward-slash. WC3 asset paths are
	// conventionally case-insensitive; archives and CASC are stored
	// lowercase. Backslash <-> forward-slash interchangeable.
	requested = strings.ToLower(path.Clean(strings.ReplaceAll(requested, "\\", "/")))

	// Build the candidate path list:
	//   1. The requested path itself.
	//   2. If it ends in .blp/.dds/.mdx/.mdl, ALSO try the sibling extension.
	// Reforged CASC ships textures as DDS only (no BLP), and many custom
	// maps reference imported models with the "wrong" extension. mdx-m3-viewer's
	// handlers auto-detect format by magic bytes, so serving DDS through a
	// .blp URL (or MDX through a .mdl URL) works — the lib picks the right
	// resource class from the data.
	//
	// In HD mode we flip the preference when the requested texture is .blp:
	// HD models tend to reference DDS directly, and the SD .blp variant may
	// not even exist in a Reforged install. So under HD + a requested .blp,
	// try .dds FIRST. For requested .dds (the HD case) we keep that first.
	// SD mode always tries the requested extension first. Model extensions
	// (MDX↔MDL) are never flipped.
	candidates := []string{requested}
	if alt := siblingExtension(requested); alt != "" {
		ext := strings.ToLower(path.Ext(requested))
		if ReforgedMode() && ext == ".blp" {
			candidates = []string{alt, requested}
		} else {
			candidates = append(candidates, alt)
		}
	}

	// Try ALL candidates against the map source FIRST, before falling back
	// to CASC. This matters when a map imports a stock-path texture via
	// sibling extension (e.g. ships terrainart/cityscape/City_GrassTrim.blp
	// to override the stock City_GrassTrim.dds). With the old per-candidate
	// interleaving, the requested .dds would resolve from CASC before we'd
	// ever try the map's .blp, hiding the override. Map wins for ANY
	// extension match before CASC ever runs.
	for _, candidate := range candidates {
		data, ok, err := forge.Current.ReadFile(candidate)
		if err != nil {
			http.Error(w, "read error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if ok {
			via := "map source"
			if candidate != requested {
				via = "map source (sibling-ext)"
			}
			log.Printf("asset: %s -> %s as %s (%d bytes)", requested, via, candidate, len(data))
			serveBytes(w, candidate, data)
			return
		}
	}

	// CASC fallback for stock WC3 assets — only after exhausting the map.
	if c, err := getCASC(); err == nil && c != nil {
		for _, candidate := range candidates {
			data, ok, err := c.ReadFile(candidate)
			if err != nil {
				log.Printf("asset: %s -> CASC error: %v", candidate, err)
				http.Error(w, "casc read error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if ok {
				via := "CASC"
				if candidate != requested {
					via = "CASC (sibling-ext)"
				}
				log.Printf("asset: %s -> %s as %s (%d bytes)", requested, via, candidate, len(data))
				serveBytes(w, candidate, data)
				return
			}
		}
	}

	log.Printf("asset: %s -> 404", requested)
	http.NotFound(w, r)
}

// siblingExtension returns the same path with the format-pair extension
// swapped (BLP↔DDS for textures, MDX↔MDL for models, .tif→.dds for the
// Reforged HD PBR-texture references). Returns "" if the extension isn't
// one of those pairs.
//
// Reforged HD MDX files reference their PBR textures as `.tif` paths
// (e.g. `units/orc/rokhan/orc_rokhan_main_diffuse.tif`) but CASC actually
// ships the bytes as `.dds`. Without this swap, every HD-only unit
// (Shadow Hunter, Beastmaster, several Reforged-era heroes) renders
// invisible in any preview / scene that loads its MDX — the geometry is
// there but every texture binding is null. mdx-m3-viewer's DDS handler
// claims the bytes by magic regardless of URL extension, so serving DDS
// through a .tif URL works. The same swap covers stock sky models like
// IcecrownGlacierSky.mdx that also reference .tif. One-way map: .dds
// doesn't fall back to .tif because nothing in the wild stores it the
// other way.
func siblingExtension(p string) string {
	ext := path.Ext(p)
	stem := p[:len(p)-len(ext)]
	switch strings.ToLower(ext) {
	case ".blp":
		return stem + ".dds"
	case ".dds":
		return stem + ".blp"
	case ".tif":
		return stem + ".dds"
	case ".mdx":
		return stem + ".mdl"
	case ".mdl":
		return stem + ".mdx"
	}
	return ""
}

func serveBytes(w http.ResponseWriter, name string, data []byte) {
	if ct := contentTypeFor(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-cache") // map changes mean stale bytes; no caching while we iterate
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func contentTypeFor(name string) string {
	switch path.Ext(name) {
	case ".mdx", ".mdl", ".blp", ".dds", ".tga":
		return "application/octet-stream"
	case ".txt", ".ini", ".slk":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json"
	}
	return "application/octet-stream"
}
