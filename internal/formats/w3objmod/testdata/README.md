# Object-modification table test fixtures

## Provenance

Both files are **real, unmodified**, lifted verbatim from the user's GPL custom
map:

    C:\Users\4step\projects\wc3-survival-game\map\extracted\war3map.w3b  (52 bytes)
    C:\Users\4step\projects\wc3-survival-game\map\extracted\war3map.w3a  (12 bytes)

## Files + the two wire shapes

All seven object-data kinds share two on-wire shapes; one real fixture per shape
is the byte-equal proof for all of them:

| file | kind | wire shape | content |
|------|------|------------|---------|
| `wc3_survival_v1_6.w3b` | destructibles | `opt=false` (no per-mod level/dataPointer slots) — also w3u / w3t / w3h | version 3, one original-table edit: `LTbr` overriding `bfil` = `"Crystal"` (string-type modification) |
| `wc3_survival_v1_6.w3a` | abilities | `opt=true` (per-mod level/dataPointer slots) — also w3d / w3q | version 3, empty table (0 originals, 0 customs) — guards the opt-format header round-trip |

## Why they're committed

`TestRoundTrip_RealFixtures` asserts `Parse → Encode` is byte-equal on these
real files. Before they were committed, the only file-based test
(`TestParseEnfos`) `Skipf`'d on an out-of-repo temp directory and was
parse-only — a clean checkout / CI never ran a real-file *round-trip* on object
data. The synthetic tests in `w3objmod_encode_test.go` cover the
int/float/string + leveled-field value paths; these real fixtures add the
on-disk byte-equality guarantee.
