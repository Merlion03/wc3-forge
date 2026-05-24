# wc3-forge

A Warcraft III map editor written in Go. Successor to a C++ HiveWE fork,
rewritten from scratch around a cleaner selection model and a programmable
[MCP](https://modelcontextprotocol.io) bridge for AI-driven map authoring.

**Status:** pre-alpha. Design phase. Nothing usable yet.

## Why

The author maintained a fork of [HiveWE](https://github.com/stijnherfst/HiveWE)
(C++ / Qt / OpenGL / Bullet) and added an MCP bridge to drive map authoring
programmatically. The fork shipped enough to build a playable co-op map
([wc3-survival-game](https://github.com/StephenSHorton)), but the editor
itself accumulated friction:

- Selection was owned by the brush system, not by the editor — viewport
  clicks didn't propagate cleanly to side panels.
- C++20 modules + Qt + OpenGL + Bullet in one process meant every new
  feature touched three subsystems.
- 1-3 minute rebuilds on every change.

wc3-forge starts over with:

- **Go**, for fast builds and a simpler concurrency story.
- **Selection as a first-class editor concept**, decoupled from any tool.
- **The MCP bridge contract preserved verbatim** so existing clients
  (Node MCP server, Python build pipelines) keep working unchanged.

## Preserved contracts (from the C++ fork)

1. **MCP wire format:** JSON-RPC 2.0 over TCP, per-pid lockfile, NDJSON
   framing, token auth.
2. **Game coordinates on the wire** for all entity tools. WC3 game coords
   are centered at (0, 0); the wire MUST be these (the C++ fork burned
   hours getting this wrong).
3. **Preserve-script marker:** if `war3map.lua` starts with a known marker
   comment, the editor will not regenerate it on save. This lets external
   build pipelines own the script file.
4. **Multi-instance support** via per-pid lockfiles + `sessions_list` /
   `session_select` tools.

## License

MIT.
