# war3map.w3e test fixtures

## Provenance

Both fixtures here are **synthetic and self-contained** — they carry no
real-map data. Each was produced by constructing a minimal `w3e.File` in code
and round-tripping it through the package's own `w3e.Encode`. Generating the
bytes via `Encode` makes them (a) anonymized and (b) a true encoder regression
guard: if the on-wire bit-packing in `Encode`/`Parse` ever drifts, the
committed bytes no longer round-trip and `TestRoundTrip_Fixtures` fails.

Before these fixtures existed, the byte-for-byte round-trip tests
(`TestRoundTrip_Fixtures`, `TestRoundTrip_AfterSwap`) `Skipf`'d on an out-of-repo
path (`C:\Users\4step\projects\wc3-survival-game\map\extracted\war3map.w3e`),
so a clean checkout / CI never ran the terrain-save losslessness proof. A
regression that corrupted a tileset swap could have shipped green.

## Files

| file | version | layout | size | dims |
|------|---------|--------|------|------|
| `reforged_v12.w3e` | 12 | Reforged, 8 bytes/vertex | 717 B | 9×9 vertices (8-tile span) |
| `classic_v11.w3e`  | 11 | Classic/TFT, 7 bytes/vertex | 624 B | 9×9 vertices (8-tile span) |

Both fixtures spread per-tile values deterministically across the legal ranges
so the encoded byte stream is varied (not a flat repeat): ground-height wobble
around the 0x2000 baseline, water levels, every `Flags` bit (ramp / blight /
water / boundary), the separate water-word boundary bit, ground-texture indices
across the full per-version range (0..63 on v12, 0..15 on v11), cliff-texture
indices, layer heights, and 6+2 / 4+1 ground/cliff tileset palettes.

## Regenerating

These bytes are stable as long as the encoder is. If you intentionally change
the wire format, regenerate by constructing the same `w3e.File` shape and
calling `w3e.Encode` (the generator that produced these is not committed; the
field values are documented above and in `w3e_test.go`).
