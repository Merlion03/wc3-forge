# Changelog

All notable changes to wc3-forge are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-05-29

### Added

- **Native JASS map support.** Older JASS maps now open, edit, save, and Test
  Map directly against `war3map.j` — no conversion to Lua required. Hand-rolled
  script maps round-trip verbatim; GUI-backed JASS maps get a full GUI→JASS
  codegen backend mirroring the Lua emitter (typed `globals` block, `call`/`set`
  statements, `if/then/else/endif`, `loop/exitwhen/endloop`, `'xxxx'` rawcodes).
  The Trigger Editor highlights script triggers as JASS in JASS maps. (#14)
- **Convert-to-Lua review & repair.** The conversion dialog surfaces failed /
  untranslatable sections at the top and lets you hand-write the Lua for them,
  written verbatim into the output. Per-failure gap markers, prev/next
  navigation, and inline error decorations show exactly where to fill in.
  (#11, #13)
- **Cross-trigger vJASS module inlining** in Convert-to-Lua, unblocking maps
  whose modules span multiple triggers. (#7)
- **3D model import** — bring in OBJ / glTF / STL and auto-convert to MDX. (#12)
- **Native macOS support**, plus the [Mead](https://github.com/StephenSHorton/mead)
  Wine-bottle workflow for installing Warcraft III. (#1)
- **MPQ patch-chain stock-asset source** for Classic / non-CASC installs
  (layered `War3Patch.mpq` over `War3.mpq`). (#2)
- **WC3 install detection** — prompts you to locate Warcraft III when it isn't
  at the conventional path. (#3)
- Pure-Go MPQ writer with lossless `.w3x` repack (preserves unlisted custom
  imports), terrain `set_tile` / `set_height` brushes, entity create + delete
  for units and doodads, and a full MCP wire surface across triggers, all seven
  object kinds, terrain, and entity lifecycle.

### Changed

- Test Map preserves an imported, unedited `war3map.j` / `war3map.lua` instead
  of regenerating it — old maps launch with their original compiled script, and
  regeneration only happens when you actually edit triggers. (#14)
- Convert-to-Lua transpiler hardened across a wide map corpus (most now convert
  to zero errors): multi-line string literals, `keyword` forward-declaration
  stripping, dangling `requires` clauses, integer-division typing, typed array
  defaults, and block-aware error recovery.

### Fixed

- `save_script` no longer clobbers a hand-authored script — it refuses without
  `overwrite`, and backs up to a `.bak` sidecar when overwriting.
- MPQ `.w3x` repack guarded against dropping unlisted custom files; `unitsdoo`
  subver-11 skin-id presence resolved per-file by trial rather than by
  subversion.
- CASC symbol loading split so Windows builds (purego has no `Dlopen` there).
- Save-failure toast restored after the MPQ error-sentinel rename.

## [0.2.0] - 2026-05-28

### Added

- Object Editor for all seven definition kinds (units, items, abilities, buffs,
  destructables, doodads, upgrades) — read + write, custom and stock objects.
- Trigger Editor — GUI tree + Monaco code view + WC3 IntelliSense + GUI→Lua
  codegen + Test Map.
- Convert Map to Lua — full vJASS preprocessor (textmacros, library/scope,
  structs, modules) with a side-by-side diff preview before committing.

## [0.1.0] - 2026-05-26

### Added

- Initial alpha: native `.w3x` / MPQ rendering (terrain, cliffs, water,
  doodads), read-only Object Editor, and the CASC stock-asset pipeline.
- Relicensed to GPL-3.0-or-later with `CREDITS.md` enumerating the ported
  HiveWE subsystems.

[0.3.0]: https://github.com/StephenSHorton/wc3-forge/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/StephenSHorton/wc3-forge/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/StephenSHorton/wc3-forge/releases/tag/v0.1.0
