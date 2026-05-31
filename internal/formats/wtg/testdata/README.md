# war3map.wtg test fixture

## Provenance

`wc3_survival_v1_6.wtg` is a **real, unmodified** trigger file (678 bytes)
lifted verbatim from the user's GPL custom map:

    C:\Users\4step\projects\wc3-survival-game\map\extracted\war3map.wtg

It is a Reforged-era Lua map's trigger tree (version `0x80000004`, sub_version
7). It's small but real, so it exercises the actual category/variable/trigger
walk rather than only the hand-built minimal case.

## Why it's committed

`TestEncodeRoundTripSurvival` asserts `Parse → Encode` is byte-equal on this
file. Before it was committed, that test (and the Enfo FFB / Secret Valley
ones) `Skipf`'d on out-of-repo `.w3x` paths, so a clean checkout / CI never ran
a real-map `.wtg` losslessness proof — only the synthetic
`TestEncodeMinimalRoundTrip`. A regression that corrupted a trigger save would
have shipped green.
