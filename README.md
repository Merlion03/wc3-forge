<p align="center">
  <img src="build/appicon.png" alt="wc3-forge logo" width="160">
</p>

<h1 align="center">wc3-forge</h1>

<p align="center">
  <i>A Warcraft III map editor designed to be driven by Claude Code as much as by hand.</i>
</p>

<p align="center">
  <img src="docs/screenshots/hero.png" alt="wc3-forge editor screenshot" width="900">
</p>

wc3-forge is a native Warcraft III map editor (Go + TypeScript, single [Wails](https://wails.io) binary) with an embedded MCP server. The GUI and Claude Code talk to the same editor session — every edit you make by hand can also be made by an agent, and vice versa, and they share one undo stack.

**Status:** pre-alpha. Read-only viewer is solid; editing surfaces (position / rotation / scale, map info, selection, view, camera, history) are wired through both the GUI and MCP. Object Editor is read-only.

## Get started with Claude Code

Three steps. Tested on Windows; should work on macOS/Linux once you replace the paths.

**1. Install wc3-forge.** Grab a release, or build from source:

```powershell
git clone https://github.com/StephenSHorton/wc3-forge
cd wc3-forge
wails build
```

The binary lands at `build/bin/wc3-forge.exe`.

**2. Build + register the MCP server.** It lives in this repo at [`mcp/`](mcp/):

```powershell
cd mcp
npm install
npm run build
claude mcp add wc3-forge --scope user -- node "$PWD\dist\index.js"
```

(`--scope` must come before `--` or it gets forwarded to node.) Verify with `claude mcp list` — `wc3-forge` should show ✓ Connected.

**3. Launch wc3-forge and talk to it.** Open the editor (`build/bin/wc3-forge.exe`), load a map (`File → Open Map…`), then in Claude Code:

```
What's the current map?
> map_status → "Fountain of Manipulation", 184 units, 2031 doodads

Move the gold mine at position 0,0 to where the player 1 start location is.
> units_list → finds creation_number 17 (typeid 'ngol' at -512, 384, 0)
> camera_set_view → pans to confirm
> units_move → applied

Save it.
> map_save → ok
```

The **Agent Console** (Ctrl+\` in the editor) streams every bridge call live — `bridge_ping`, `units_move`, `map_save`, durations, params, results. Use it to watch what an agent is doing in real time.

### What Claude can do today

| Surface | Tools |
|---|---|
| Map lifecycle | `map_open`, `map_close`, `map_status`, `map_save` |
| Map info | `map_info_get`, `map_info_set` (name, author, description, suggested players, lua flag) |
| Placed units | `units_list`, `units_get`, `units_move`, `units_rotate`, `units_scale` |
| Placed doodads | `doodads_list`, `doodads_get`, `doodads_move`, `doodads_rotate`, `doodads_scale` |
| Selection | `selection_get`, `selection_set`, `selection_clear` |
| View + camera | `view_set_mode`, `view_set_doodad_category_visible`, `camera_set_view` |
| Window | `window_set_title` (label your instance so parallel agents are distinguishable) |
| History | `history_undo`, `history_redo`, `history_list`, `history_begin_group`, `history_end_group` |
| Unit definitions | `objects_units_list`, `objects_units_get`, `objects_units_fields_meta` (read-only) |
| Multi-instance | `sessions_list`, `session_select` (point Claude at one of several running wc3-forges) |

All entity tools speak **game coordinates** (origin at map center, units = WC3 world units). All mutations flow through the same history stack the GUI uses, so `Ctrl+Z` in the editor undoes Claude's edits and vice versa.

### Multi-instance

You can run several wc3-forges at once — the bridge picks an unused port and each writes its own per-pid lockfile at `~/.wc3-forge/mcp/<pid>.lock`. `sessions_list` enumerates them; `session_select` pins subsequent tool calls to one. Use `window_set_title` to label them so parallel agents can tell instances apart at a glance.

## Building

```
wails build
```

`build/bin/wc3-forge.exe` is the output. The `postBuildHooks` entry in `wails.json` copies the vendored CASC DLLs next to the binary so it's self-contained.

## Design

- **Go backend + Svelte / TypeScript frontend** in one Wails binary; frontend bundle embedded via `go:embed`.
- **Two control surfaces, one editor**: the GUI and the MCP bridge both mutate the same `forge.Session` and emit the same change events. Adding a feature usually means adding it on both surfaces at once.
- **3D rendering** uses [mdx-m3-viewer](https://github.com/flowtsohg/mdx-m3-viewer) at the low level (MDX skeletal animation, BLP/DDS, batch shaders); terrain, cliffs, water, sky, and start-location markers are drawn by our own WebGL shaders layered into the same scene.
- **Asset resolution** is map-first, then CASC (Reforged/Classic install). Maps can override stock textures by importing files at the CASC path. Set `WC3FORGE_WC3_PATH` to point at a non-default install.
- **Multi-instance** is a primary use case: lockfile-per-pid, MCP `session_select` to pin, the OS window title carries the PID + a free-form agent label.

## MCP wire contract

JSON-RPC 2.0 over TCP, NDJSON-framed on 127.0.0.1. Per-pid lockfile at `~/.wc3-forge/mcp/<pid>.lock` (override with `WC3FORGE_MCP_LOCK_DIR`; `HIVEWE_MCP_LOCK_DIR` is honored for legacy compat). Every request carries `params._token` matching the lockfile token.

## License

MIT.
