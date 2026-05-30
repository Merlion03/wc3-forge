# war3map.imp test fixture

## Provenance

`war3map.imp` here is a **real, unmodified** import table lifted verbatim from
HiveWE's bundled sample map:

    C:\Users\4step\projects\HiveWE\data\test map\war3map.imp

It is 140 bytes: `version = 1`, `count = 8`. It was chosen because it is small
*and* exercises both flag-byte conventions in one file.

## Hex layout

```
01 00 00 00              version = 1
08 00 00 00              count   = 8
1D "Crystal.mdx\0"        flag 0x1D  (bare filename, custom variant)
0D "war3mapSkin.w3a\0"    flag 0x0D  (standard / full-path)
0D "war3mapSkin.w3b\0"
0D "war3mapSkin.w3d\0"
0D "war3mapSkin.w3h\0"
0D "war3mapSkin.w3q\0"
0D "war3mapSkin.w3t\0"
0D "war3mapSkin.w3u\0"
```

## Flag-byte semantics (confirmed empirically, not guessed)

The design spec's guessed `5/8` (standard) vs `10/13` (custom) split does **not**
match reality. Surveying this fixture plus two much larger real import tables
(an Enfo FFB map with 2,295 entries incl. 914 `war3mapImported\*.mdx` paths, and
an 11,594-byte custom-skin table) gives this picture:

| flag | meaning | seen on |
|------|---------|---------|
| `0x0D` (13) | **custom / fully-qualified path** — the World-Editor default | `war3mapImported\Foo.mdx`, `ReplaceableTextures\...`, `abilities\Spells\...`, `war3mapSkin.w3*` (vast majority of all entries) |
| `0x08` (8)  | **default directory** — bare filename, editor implicitly prepends `war3mapImported\` | `OrbOfFire.mdx`, `Crystal.mdx` |
| `0x05` (5)  | historically-documented sibling of `0x08`; not seen in surveyed fixtures | (preserved verbatim if encountered) |
| `0x0A` (10) | documented custom sibling; not seen in surveyed fixtures | (preserved verbatim) |
| `0x1D` (29) | another custom variant | `Crystal.mdx` in this fixture |

Takeaways baked into the package:

- The engine/editor treats anything with bit `0x08` set as a literal/custom
  path; the exact byte is otherwise cosmetic. The canonical value the World
  Editor writes for a fully-qualified `war3mapImported\` path is **`0x0D`**, so
  that is `imp.StandardFlag` and what `File.Add` mints.
- The package's only hard guarantee is **byte-faithful round-tripping**: Parse
  preserves each entry's original flag and Encode writes it back unchanged, so
  resaving a map never rewrites its existing import flags.

The byte-identical round-trip on this real fixture is asserted by
`TestRoundTripByteIdentical` in `imp_test.go`.
