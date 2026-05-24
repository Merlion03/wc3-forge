# wc3-forge

A Warcraft III map editor written in Go + TypeScript, with a built-in
[MCP](https://modelcontextprotocol.io) bridge for AI-driven map authoring.

**Status:** pre-alpha. Works as a read-only viewer; editing surfaces in
progress.

## What it is

- **Open WC3 maps natively** (`.w3x` / `.w3m` / extracted folders) via a
  pure-Go MPQ reader.
- **3D viewport** rendering terrain, cliffs, ramps, water (with animated
  wave textures + depth-based shading), shadow maps, and per-tile
  textured ground sampled from the WC3 install via CASC.
- **Doodads, destructibles, and custom map objects** rendered as MDX
  meshes — including types defined per-map via `war3map.w3d` /
  `war3map.w3b` modifications.
- **Start-location markers** rendered as colored pillars (one per
  player slot).
- **Selection model owned by the editor**, not by the brush system —
  viewport clicks propagate cleanly to side panels and to the MCP bridge.
- **Programmable via MCP**: a JSON-RPC 2.0 / NDJSON bridge over TCP lets
  Claude (or any MCP client) drive every editor subsystem — terrain,
  units, doodads, triggers, object data, imports.

## Design

- **Go backend + TypeScript / Svelte frontend** in a single
  [Wails](https://wails.io) executable.
- **3D rendering via [mdx-m3-viewer](https://github.com/flowtsohg/mdx-m3-viewer)**
  for MDX skeletal animation, BLP/DDS textures, batch shaders. Terrain,
  cliffs, water, and start-location markers are rendered by our own
  minimal WebGL shaders layered into the same scene.
- **Selection is a first-class editor concept**, owned by the session
  and mutated through a single API. Tools (brushes, palettes, MCP
  clients) read selection; they never own it. Mixed-kind multi-select
  is supported.
- **Multi-instance** by design: each running process writes its own
  lockfile and the MCP server can `sessions_list` / `session_select`
  between them.

## File-format coverage

Pure-Go parsers in `internal/formats/`:

| Format          | Status                                                |
|-----------------|-------------------------------------------------------|
| `war3map.w3i`   | full                                                   |
| `war3map.w3e`   | full (v11 + v12+ tilepoint layouts)                    |
| `war3map.doo`   | full (regular + special doodads)                       |
| `war3mapUnits.doo` | full                                                |
| `war3map.shd`   | full (read-only)                                       |
| `war3map.wts`   | full + TRIGSTR resolution                              |
| `war3map.w3d`   | full — per-map doodad modifications + custom types     |
| `war3map.w3b`   | full — destructible modifications + custom types       |
| `war3map.w3u`   | full (parsed; not yet rendered)                        |
| `war3map.w3t`   | full (parsed; not yet rendered)                        |
| MPQ archives    | read-only (HM3W auto-detect, ZLIB + PKWARE DCL)       |
| CASC storage    | read via vendored CascLib (HD/SD prefix toggle)        |
| SLK + INI       | full, with INI-merge for Reforged skin files           |
| BLP1            | via gowarcraft3                                        |
| DDS (DXT1/3/5)  | reference-color sampling for tilesets                  |
| MDX/MDL         | via mdx-m3-viewer                                      |

## MCP wire contract

- **JSON-RPC 2.0 over TCP**, NDJSON-framed, per-pid lockfile at
  `~/.wc3-forge/mcp/<pid>.lock` containing `{port, token}`.
- Every request must carry `params._token` matching the lockfile.
- All entity tools speak **game coordinates** (origin at map center,
  units = WC3 world units). Conversions to/from internal grid space stay
  inside the editor.
- **Preserve-script marker:** if `war3map.lua` starts with a known marker
  comment, the editor will not regenerate it on save. External build
  pipelines can own the script file.

## License

MIT.
