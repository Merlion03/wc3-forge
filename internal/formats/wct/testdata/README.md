# war3map.wct / war3map.wtg test fixtures

## Provenance

Both files are **real, unmodified**, lifted verbatim from the user's GPL custom
map:

    C:\Users\4step\projects\wc3-survival-game\map\extracted\war3map.wct  (310 bytes)
    C:\Users\4step\projects\wc3-survival-game\map\extracted\war3map.wtg  (678 bytes)

The `.wtg` is committed alongside the `.wct` because the `wct` encoder needs the
`wtg`'s category-then-trigger walk to recover the per-trigger custom-text blob
order. (It is the same `.wtg` as `internal/formats/wtg/testdata/`.)

## Why they're committed

`TestEncodeWCTRoundTripSurvival` asserts `Parse(.wct) → Encode` is byte-equal on
this real file. Before it was committed, the `.wct` round-trip tests `Skipf`'d
on out-of-repo `.w3x` paths, so a clean checkout / CI only ran the synthetic
`TestEncodeWCTMinimal`. A regression that corrupted the custom-script (Lua /
JASS) save would have shipped green.
