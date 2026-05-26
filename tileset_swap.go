package main

import (
	"sort"
	"strings"

	"github.com/StephenSHorton/wc3-forge/internal/forge"
)

// TileInfoDTO is one entry in a tileset's ground or cliff palette, exposed to
// the frontend for the Swap Tileset dialog. Texture is the dir/file stem (no
// extension) so the JS side can probe `.dds` / `.blp` the same way the rest
// of the editor does.
type TileInfoDTO struct {
	FourCC  string   `json:"fourcc"`  // e.g. "Lgrs"
	Name    string   `json:"name"`    // SLK comment column or FourCC fallback
	Texture string   `json:"texture"` // resolved "dir/file" stem, "" if SLK lookup failed
	Color   [3]uint8 `json:"color"`   // representative RGB color sampled from the texture; gray fallback on miss
	// Thumb is a data-URL PNG of the tile's first sub-tile, downsampled to
	// 48×48. Empty string when the texture can't be decoded — caller falls
	// back to Color. Baked into ListTilesets so the dialog renders the
	// whole palette in one round-trip; cached per process.
	Thumb string `json:"thumb"`
}

// TilesetInfoDTO is one tileset (Lordaeron Summer, Ashenvale, …) enumerated
// from CASC. Letter is the single-byte code stored in war3map.w3i+w3e.
type TilesetInfoDTO struct {
	Letter string        `json:"letter"` // "L"
	Name   string        `json:"name"`   // "Lordaeron Summer" or "Tileset (L)" fallback
	Ground []TileInfoDTO `json:"ground"`
	Cliff  []TileInfoDTO `json:"cliff"`
}

// Canonical tileset display names keyed by the FourCC's first letter. Sourced
// from the WC3 World Editor's TilesetData.slk + WorldEditStrings — we don't
// pull from CASC at runtime because the names live in TRIGSTR-resolved
// localization strings that have no machine-friendly path. Unknown letters
// fall back to "Tileset (X)" so an unrecognized tileset still picks up its
// FourCCs without breaking the UI.
var tilesetNames = map[byte]string{
	'A': "Ashenvale",
	'B': "Barrens",
	'C': "Felwood",
	'D': "Dungeon",
	'F': "Lordaeron Fall",
	'G': "Underground",
	'I': "Icecrown Glacier",
	'J': "Dalaran Ruins",
	'K': "Sunken Ruins",
	'L': "Lordaeron Summer",
	'N': "Northrend",
	'O': "Outland",
	'Q': "Village Fall",
	'V': "Village",
	'W': "Lordaeron Winter",
	'X': "Cityscape",
	'Y': "Dalaran",
	'Z': "Black Citadel",
}

// ListTilesets enumerates every tileset present in the CASC Terrain.slk +
// CliffTypes.slk pair, grouping FourCCs by their first byte (the tileset
// letter). Tiles are sorted by FourCC inside each group so the dialog has
// a deterministic listing.
//
// Heavy lift on first call (SLKs load + parse + cache); cheap thereafter.
func (a *App) ListTilesets() []TilesetInfoDTO {
	terrainSLK, _ := loadTerrainSLK()
	cliffSLK, _ := loadCliffTypesSLK()
	byLetter := map[byte]*TilesetInfoDTO{}
	get := func(letter byte) *TilesetInfoDTO {
		if t, ok := byLetter[letter]; ok {
			return t
		}
		name, ok := tilesetNames[letter]
		if !ok {
			name = "Tileset (" + string(letter) + ")"
		}
		t := &TilesetInfoDTO{Letter: string(letter), Name: name}
		byLetter[letter] = t
		return t
	}

	if terrainSLK != nil {
		for fourCC, row := range terrainSLK.Rows {
			if len(fourCC) != 4 {
				continue
			}
			letter := fourCC[0]
			dir := row.String("dir")
			file := row.String("file")
			texture := ""
			if dir != "" && file != "" {
				texture = dir + "/" + file
			}
			name := row.String("comment")
			if name == "" {
				name = fourCC
			}
			t := get(letter)
			t.Ground = append(t.Ground, TileInfoDTO{
				FourCC:  fourCC,
				Name:    name,
				Texture: texture,
				Color:   tileColor(fourCC),
				Thumb:   tileThumbnail(fourCC),
			})
		}
	}
	if cliffSLK != nil {
		for fourCC, row := range cliffSLK.Rows {
			if len(fourCC) != 4 {
				continue
			}
			// CliffTypes.slk FourCCs are like "CLgr" — the cliff-family
			// letter is the SECOND byte, not the first (the 'C' prefix is
			// shared by every cliff). HiveWE uses the same convention to
			// group cliff tiles under their tileset letter.
			letter := fourCC[1]
			dir := row.String("texdir")
			file := row.String("texfile")
			texture := ""
			if dir != "" && file != "" {
				texture = dir + "/" + file
			}
			name := row.String("comment")
			if name == "" {
				name = fourCC
			}
			t := get(letter)
			// Cliffs share the ground-texture color sampler's data layout but
			// live in CliffTypes.slk's texdir/texfile cols instead of
			// Terrain.slk's dir/file. tileColor() reads Terrain.slk so it'd
			// fall back here — use a neutral stone color instead so the
			// dialog's cliff swatches at least look like cliffs.
			t.Cliff = append(t.Cliff, TileInfoDTO{
				FourCC:  fourCC,
				Name:    name,
				Texture: texture,
				Color:   [3]uint8{120, 110, 100},
				Thumb:   cliffThumbnail(fourCC),
			})
		}
	}

	out := make([]TilesetInfoDTO, 0, len(byLetter))
	for _, t := range byLetter {
		sort.Slice(t.Ground, func(i, j int) bool { return t.Ground[i].FourCC < t.Ground[j].FourCC })
		sort.Slice(t.Cliff, func(i, j int) bool { return t.Cliff[i].FourCC < t.Cliff[j].FourCC })
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		// Stock tilesets first, alphabetized by display name; unknowns last.
		_, ai := tilesetNames[out[i].Letter[0]]
		_, aj := tilesetNames[out[j].Letter[0]]
		if ai != aj {
			return ai
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// SwapTilesetRequest is the JS-facing shape for the SwapTileset RPC. Mirror of
// forge.SwapTilesetRequest but with JSON-friendly types (NewLetter is a string
// because Wails serialises byte as base64 otherwise).
type SwapTilesetRequest struct {
	NewLetter         string   `json:"newLetter"`         // single-char string, e.g. "A"
	NewGroundTilesets []string `json:"newGroundTilesets"` // ordered FourCCs
	NewCliffTilesets  []string `json:"newCliffTilesets"`  // ordered FourCCs
	GroundFromTo      []int    `json:"groundFromTo"`      // groundFromTo[oldIdx] = newIdx
	CliffFromTo       []int    `json:"cliffFromTo"`       // cliffFromTo[oldIdx]  = newIdx
}

// SwapTileset retiles the currently-loaded map and returns the refreshed
// TerrainDTO so the JS renderer can rebuild its atlases without a full Open
// cycle. Errors out (without mutating) if no map is loaded or the request is
// malformed; see forge.Session.SwapTileset for the validation list.
func (a *App) SwapTileset(req SwapTilesetRequest) (TerrainDTO, error) {
	if len(req.NewLetter) != 1 {
		return TerrainDTO{}, errSwapBadLetter
	}
	internal := forge.SwapTilesetRequest{
		NewLetter:         req.NewLetter[0],
		NewGroundTilesets: req.NewGroundTilesets,
		NewCliffTilesets:  req.NewCliffTilesets,
		GroundFromTo:      req.GroundFromTo,
		CliffFromTo:       req.CliffFromTo,
	}
	if err := forge.Current.SwapTileset(internal); err != nil {
		return TerrainDTO{}, err
	}
	return a.GetTerrain()
}

// errSwapBadLetter is an exported sentinel-via-helper so the validation error
// stays consistent if more callers (MCP, tests) want to check for it later.
var errSwapBadLetter = badLetterError{}

type badLetterError struct{}

func (badLetterError) Error() string {
	return "SwapTileset: NewLetter must be exactly one character"
}
