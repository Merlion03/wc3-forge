# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Build the desktop app (this is the canonical build — `go build` alone produces a runnable binary but skips the embedded frontend and the postbuild DLL copy, leaving you with something that loads neither UI nor CASC):

```
wails build                               # production build → build/bin/wc3-forge.exe
wails dev                                 # dev build with Vite HMR → build/bin/wc3-forge-dev.exe
```

Run the existing binary directly (useful when you don't need a rebuild):

```
build/bin/wc3-forge.exe                   # GUI + bridge
build/bin/wc3-forge.exe --open <path>     # auto-load a map folder or .w3x on startup
build/bin/wc3-forge.exe --headless        # MCP bridge only, no window
build/bin/wc3-forge.exe --no-bridge       # GUI only, no MCP bridge
build/bin/wc3-forge.exe --reforged        # start in HD asset mode (default is SD/Classic)
build/bin/wc3-forge.exe --camera x,y,z,distance   # pin startup camera for verification
```

Tests + checks (mirror the CI flow in `.github/workflows/ci.yml`):

```
go vet ./...
go test ./...
go test ./internal/forge/...             # single package
go test ./internal/forge -run TestSessionSaveRoundTrip   # single test
cd frontend && npm ci                    # first-time + after package.json changes
cd frontend && npm run check             # svelte-check (typecheck)
cd frontend && npm run build             # vite build (also runs as part of `wails build`)
```

The `cmd/dumpw3e` parser-replay tool exists for cross-checking renderer divergences against HiveWE — `go run ./cmd/dumpw3e <map-path>`.

## Process etiquette for parallel agents

Multiple wc3-forge instances coexist by design — the MCP bridge auto-picks an unused port and each process writes its own lockfile at `~/.wc3-forge/mcp/<pid>.lock`. **Do not run `Stop-Process wc3-forge` or `Get-Process wc3-forge | Stop-Process`** — that kills sibling agents' verification windows and the user's interactive session.

When launching your own instance for testing, capture the PID and kill only that PID on cleanup:

```powershell
$proc = Start-Process -FilePath 'build\bin\wc3-forge.exe' -PassThru
# ... do work ...
Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
```

`wails dev` parents `wc3-forge-dev.exe` under a `wails.exe` process — when cleaning up after a dev build, filter `ProcessName -match '^wc3-forge'` so the child doesn't outlive the parent and hold `CascLib.dll` locked.

If you'd otherwise launch only to drive an MCP call, read an existing PID's lockfile (`{port, token}`) and connect over JSON-RPC instead.

## Architecture

Single Wails v2 executable. Go owns map parsing/I/O and the MCP bridge; TypeScript + Svelte owns the 3D viewport and panels. Two control surfaces (the GUI and external MCP clients) converge on the same `forge.Session` singleton, so a JSON-RPC `units.move` and a viewport drag end up in the same mutator and emit the same `entity-changed` / `dirty-changed` events.

```
                ┌────────────── Go process ──────────────┐
   MCP client ──┤ bridge (NDJSON / JSON-RPC over TCP) ───┤
                │                                        │
   GUI (Wails)──┤ App (bindings) ──┐                     │
                │                  ├─► forge.Session ◄───┤
                │ assetHandler ────┘   (map state +      │
                │ /asset/<path>        history + emits)  │
                │   1) map archive                       │
                │   2) CASC mount  ◄── WC3FORGE_WC3_PATH │
                └────────────────────────────────────────┘
```

### Go side (`internal/`)

- `internal/forge` — the editor core. `Session` is the singleton; `RegisterAll(b)` in `handlers.go` is the **single registration point** for every MCP method. Adding a new MCP tool means: write `handle<Foo>`, add `reg("foo.bar", handleFoo)` to `RegisterAll`, and add the matching mutator to `session.go` so the command flows through history. All session mutations go through `recordCommand` so undo/redo + `entity-changed` events stay coherent across both control surfaces.
- `internal/bridge` — wire transport only (JSON-RPC 2.0 over NDJSON on 127.0.0.1, ephemeral port, per-pid lockfile, token auth on `params._token`). The wire contract is preserved verbatim from the HiveWE C++ fork's MCP bridge so existing HiveWE MCP clients work against wc3-forge unchanged when pointed at our lockdir via `HIVEWE_MCP_LOCK_DIR`.
- `internal/formats` — pure-Go parsers for every WC3 file format the editor touches (`w3e`, `w3i`, `doodadsdoo`, `unitsdoo`, `shd`, `wts`, `w3objmod`, `wpm`, `slk`, `dds`, `miscdata`, `mpq`). Each has round-trip tests; `unitsdoo` and `w3e` have non-obvious round-trip invariants (raw-scale bits, set boundaries — see comments in the encode tests).
- `internal/casc` — CGo wrapper around vendored CascLib (`scripts/casclib/`). The DLLs are copied next to the binary by `postBuildHooks` in `wails.json`. Don't import CascLib directly — go through `internal/casc.Storage`.
- `internal/wc3launch` — locates the WC3 install and launches `Warcraft III.exe -launch -loadfile <abspath>` for "Test Map" flow.
- `cmd/dumpw3e` — standalone parser-replay tool used when wc3-forge's render diverges from HiveWE.

The top-level `*.go` files (`main.go`, `app.go`, `asset_handler.go`, `reforged.go`, `sky.go`, `tileset_*.go`, `typeindex.go`) are the Wails surface and the asset HTTP handler — they don't contain editor logic, just plumbing.

### Asset resolution

The frontend's `mdx-m3-viewer` issues XHRs against `/asset/<path>` to fetch models and textures. `assetHandler.ServeHTTP` resolves each request in this order, **first hit wins**:

1. The currently-loaded map's archive/folder (custom imports + per-map overrides + `war3map.*` files).
2. The CASC mount on the user's WC3 install.

Stock asset bases must be exhausted across every candidate file extension before falling back to CASC — sibling-extension overrides at the exact CASC path are how maps retexture stock tilesets, so the order is load-bearing. CASC location defaults to `C:\Program Files (x86)\Warcraft III` and can be overridden with `WC3FORGE_WC3_PATH`. The lazy-init path is gated so first failures don't crash the editor — folder-extracted maps still work without CASC.

### Frontend (`frontend/src/`)

Svelte 5 + TS + Vite, compiled into `frontend/dist/` and embedded into the Go binary via `//go:embed all:frontend/dist`. `App.svelte` is the only top-level component; everything else is mounted from it.

- `scene-instances.ts` is the heart of rendering. It uses `mdx-m3-viewer` **at the low level only** — `ModelViewer`, `Scene`, and the `mdx`/`blp`/`dds`/`tga` handlers. `War3MapViewer.loadMap` and the lib's terrain/cliff/water shaders are NOT used (they're brittle on modern Reforged W3X files). Terrain, cliffs, water, sky, and start-location markers are drawn by `terrain.ts`, `cliffs.ts`, `water.ts`, `sky.ts`, `sloc-markers.ts` between `viewer.startFrame()` and `viewer.render()`.
- `frontend/wailsjs/` is **generated** by `wails build` — never hand-edit. After changing any Go method bound to the Wails `App` struct, re-run `wails build` (or `wails generate module`) so the TS bindings under `frontend/wailsjs/go/main/App.js` regenerate.
- Selection is owned by `forge.Session`, not by any tool/panel. Viewport, side panels, and MCP all read from the same selection list and mutate it through the same API.

### Cross-cutting events

Go emits Wails events whose names are constants at the top of `app.go`:

- `wc3-forge:selection-changed`, `wc3-forge:entity-changed` — selection + entity mutation
- `wc3-forge:map-changed`, `wc3-forge:dirty-changed` — map lifecycle + unsaved-edits state
- `wc3-forge:history-changed` — undo/redo stack
- `wc3-forge:bridge-call` — every MCP dispatch (powers the in-page Agent Console)
- `wc3-forge:test-command`, `wc3-forge:startup-camera`, `wc3-forge:close-requested` — verification + window-close handshake

When adding a new event, declare the name as a constant in `app.go` next to the existing ones; the frontend imports the matching string in `App.svelte`.
