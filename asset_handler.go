package main

import (
	"net/http"
	"path"
	"strings"

	"github.com/StephenSHorton/wc3-forge/internal/forge"
)

// assetHandler serves /asset/<path> requests from Go-side asset sources.
// mdx-m3-viewer's pathSolver returns "/asset/<src>" strings; the viewer
// then XHR-fetches via Wails' embedded HTTP server, which routes here.
//
// Resolution order (first hit wins):
//  1. The currently-loaded map's archive/folder (custom imports + per-map
//     overrides + the map's own files like war3map.w3i if requested).
//  2. (TODO) CASC mount on the user's WC3 install — for stock unit/doodad
//     models, tileset textures, team-color BLPs, etc.
//
// Without (2), most asset requests return 404 and mdx-m3-viewer can't
// render textured terrain or stock models. That's the next session's work.
type assetHandler struct{}

func newAssetHandler() http.Handler {
	return &assetHandler{}
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

	// Try the current map's source first.
	data, ok, err := forge.Current.ReadFile(requested)
	if err != nil {
		http.Error(w, "read error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if ok {
		serveBytes(w, requested, data)
		return
	}

	// TODO: try CASC.

	http.NotFound(w, r)
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
