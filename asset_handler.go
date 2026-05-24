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
	return `C:\Program Files (x86)\Warcraft III`
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
	candidates := []string{requested}
	if alt := siblingExtension(requested); alt != "" {
		candidates = append(candidates, alt)
	}

	for i, candidate := range candidates {
		// Try the current map's source first.
		data, ok, err := forge.Current.ReadFile(candidate)
		if err != nil {
			http.Error(w, "read error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if ok {
			via := "map source"
			if i > 0 {
				via = "map source (sibling-ext)"
			}
			log.Printf("asset: %s -> %s as %s (%d bytes)", requested, via, candidate, len(data))
			serveBytes(w, candidate, data)
			return
		}

		// CASC fallback for stock WC3 assets.
		if c, err := getCASC(); err == nil && c != nil {
			data, ok, err = c.ReadFile(candidate)
			if err != nil {
				log.Printf("asset: %s -> CASC error: %v", candidate, err)
				http.Error(w, "casc read error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if ok {
				via := "CASC"
				if i > 0 {
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
// swapped (BLP↔DDS for textures, MDX↔MDL for models). Returns "" if the
// extension isn't one of those pairs.
func siblingExtension(p string) string {
	ext := path.Ext(p)
	stem := p[:len(p)-len(ext)]
	switch strings.ToLower(ext) {
	case ".blp":
		return stem + ".dds"
	case ".dds":
		return stem + ".blp"
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
