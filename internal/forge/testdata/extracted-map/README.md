# Committed extracted-map fixture

## Purpose

A small, in-repo folder-backed map used by the safety-critical save-pipeline
round-trip tests in `session_save_test.go` (move/rotate/scale a unit, mutate an
Info field, swap the tileset → Save → reopen → assert it survived on disk).

Before this fixture existed every one of those tests `Skipf`'d on an out-of-repo
path (`C:\Users\4step\projects\wc3-survival-game\map\extracted`), so a clean
checkout / CI never proved the end-to-end save pipeline was lossless. A
regression that corrupted a unit move, an Info field write, or a tileset swap
on Save could have shipped green.

The tests copy this folder to a fresh `t.TempDir()` (via
`copyCommittedMapFixture`) before mutating, so the committed bytes are never
modified.

## Files

| file | provenance | notes |
|------|------------|-------|
| `war3map.w3i` | real, from the user's GPL survival map (also committed at `internal/formats/w3i/testdata/wc3_survival_v1_6.w3i`) | REQUIRED by `Session.Open`; carries Info.Name/Tileset the MutateInfo + SwapTileset tests assert on |
| `war3mapUnits.doo` | real, from the same map (also at `internal/formats/unitsdoo/testdata/wc3_survival_v1_6.doo`) | 10 entities incl. non-sloc units the move/rotate/scale tests mutate |
| `war3map.w3e` | **synthetic** — the same `reforged_v12.w3e` generated for `internal/formats/w3e/testdata/` (see that dir's README) | v12 terrain, 6 ground + 2 cliff tilesets the SwapTileset test remaps; 9×9 grid |

Everything else (`war3map.doo`, triggers, `.wts`, object tables, etc.) is
optional for `Session.Open` and omitted to keep the fixture minimal — `Save`
only writes the files a test actually marks dirty.
