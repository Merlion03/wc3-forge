package main

import (
	"log"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/StephenSHorton/wc3-forge/internal/forge"
)

// SkyModelOption is one row of UI/WorldEditData.txt's [SkyModels] section.
// Path is normalized to forward-slash + lowercase + .mdx extension (the form
// the asset handler and mdx-m3-viewer both want). NameKey is the WESTRING
// token, DisplayName is its resolved value (or the key verbatim if the
// strings table didn't resolve it).
type SkyModelOption struct {
	Path        string `json:"path"`
	NameKey     string `json:"name_key"`
	DisplayName string `json:"display_name"`
}

// No engine-default sky table here on purpose. WC3 hardcodes a per-tileset
// default in the engine that we cannot inspect from editor data, and an
// earlier guess (L→LordaeronSummerSky etc.) turned out to be over-eager —
// many real maps render NO sky in-game unless they explicitly call
// SetSkyModel("…"). Instead of pretending to mirror the engine, wc3-forge
// renders a custom horizon gradient (sky-gradient.ts) whenever no script
// call resolves a path. Phase 2's Map Info picker is how users opt into a
// specific stock SkyModel.

// setSkyModelRE matches `SetSkyModel(<whitespace>?<quote>...<quote>)` in
// either JASS (`call SetSkyModel("path")`) or Lua (`SetSkyModel("path")`,
// also allowing single quotes).
//
// We deliberately don't try to handle every Lua-string oddity (long brackets,
// concatenation) — those are vanishingly rare for sky paths, and a missed
// match just means we fall back to the tileset default, which is a sensible
// failure mode.
var setSkyModelRE = regexp.MustCompile(`(?i)SetSkyModel\s*\(\s*(?:"([^"]*)"|'([^']*)')\s*\)`)

// scanSetSkyModel returns the last SetSkyModel("...") argument found in the
// JASS and Lua script bytes (Lua wins ties since modern maps use Lua). The
// `found` return distinguishes "no call at all" (callers should fall back to
// tileset default) from "explicit empty call" (a map disabling the sky on
// purpose).
//
// Path returned is normalized to lowercase + forward-slash + .mdx extension.
// Backslashes in the source are interpreted as path separators, NOT as
// escape sequences — that's how WC3 paths are authored in practice (`"a\\b"`
// in JASS source IS `a\b` since JASS doesn't process the escape, and Lua
// authors typically write the same `"a\\b"` form deliberately).
func scanSetSkyModel(jBytes, luaBytes []byte) (raw string, found bool) {
	// Lua scanned last so a Lua override beats a stale JASS call (would only
	// happen on a partially-converted map).
	for _, src := range [][]byte{jBytes, luaBytes} {
		if len(src) == 0 {
			continue
		}
		// Strip JASS // and Lua -- line comments so a commented-out example
		// SetSkyModel(...) in someone's code doesn't accidentally win. We
		// don't bother with /* */ or --[[ ]] block comments — they're
		// rare and a false positive there just means the editor preview
		// is off by one sky.
		stripped := stripLineComments(src)
		matches := setSkyModelRE.FindAllSubmatch(stripped, -1)
		if len(matches) == 0 {
			continue
		}
		last := matches[len(matches)-1]
		// Group 1 = double-quoted body, group 2 = single-quoted. One is
		// always non-nil if the regex matched.
		var s string
		if last[1] != nil {
			s = string(last[1])
		} else {
			s = string(last[2])
		}
		raw = s
		found = true
	}
	return raw, found
}

// stripLineComments removes JASS `//...` and Lua `--...` line comments. Keeps
// newlines so regex line semantics aren't disturbed. Does NOT respect string
// literals — but `//` and `--` inside a string is also vanishingly rare for
// SetSkyModel-style code.
func stripLineComments(src []byte) []byte {
	out := make([]byte, 0, len(src))
	i := 0
	for i < len(src) {
		c := src[i]
		// Look for // or --
		if i+1 < len(src) && ((c == '/' && src[i+1] == '/') || (c == '-' && src[i+1] == '-')) {
			// Skip to end of line.
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		out = append(out, c)
		i++
	}
	return out
}

// normalizeSkyPath turns a WC3 sky-model path string into the asset-handler-
// friendly form: lowercase, forward slashes, .mdx extension (mdx-m3-viewer
// can read MDX bytes through any URL since it sniffs magic, but normalizing
// the URL keeps logs grep-able and dedupes cache keys).
func normalizeSkyPath(p string) string {
	if p == "" {
		return ""
	}
	clean := strings.ToLower(path.Clean(strings.ReplaceAll(p, "\\", "/")))
	if strings.HasSuffix(clean, ".mdl") {
		clean = clean[:len(clean)-4] + ".mdx"
	} else if !strings.HasSuffix(clean, ".mdx") {
		clean = clean + ".mdx"
	}
	return clean
}

// ResolveSkyModel returns the path the renderer should use for the loaded
// map's sky. Source precedence:
//  1. Pending picker override (Session.SetSkyModel) — set when the user
//     changes the Sky picker in Map Info but hasn't saved yet. Lets the
//     editor preview the new sky immediately without round-tripping through
//     a script-rewrite + reparse.
//  2. SetSkyModel("…") call already in war3map.j / war3map.lua.
//  3. Empty — caller falls back to the horizon-gradient backdrop.
//
// `tileset` is currently unused; kept on the signature so a later engine-
// default lookup can slot in without changing call sites.
func ResolveSkyModel(tileset byte) (path string, fromScript bool) {
	_ = tileset
	if pending := forge.Current.PendingSkyModel(); pending != nil {
		// Pending picker change wins — the user just clicked it; show them
		// what they picked. fromScript=true because that's where this value
		// will land on the next Save (and the renderer doesn't distinguish
		// "pending" vs "committed" further).
		return normalizeSkyPath(*pending), true
	}
	jBytes, _, _ := forge.Current.ReadFile("war3map.j")
	luaBytes, _, _ := forge.Current.ReadFile("war3map.lua")
	if raw, ok := scanSetSkyModel(jBytes, luaBytes); ok {
		return normalizeSkyPath(raw), true
	}
	return "", false
}

// skyModelsList is the parsed [SkyModels] section of UI/WorldEditData.txt,
// loaded once and cached. Used by the Map Info editor's sky picker.
var (
	skyModelsCache []SkyModelOption
	skyModelsOnce  sync.Once
)

// loadSkyModels parses UI/WorldEditData.txt for the [SkyModels] section.
// Format per line: `NN=path,WESTRING_KEY`. WESTRING keys are resolved through
// the existing string table; unresolved keys fall back to the key itself.
func loadSkyModels() []SkyModelOption {
	skyModelsOnce.Do(func() {
		data, ok, err := readBaseAsset("UI/WorldEditData.txt")
		if err != nil || !ok {
			log.Printf("sky: WorldEditData.txt load skip: ok=%v err=%v", ok, err)
			return
		}
		if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
			data = data[3:]
		}
		strs := wesStrings()
		section := ""
		var out []SkyModelOption
		for _, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, ";") {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = line[1 : len(line)-1]
				continue
			}
			if !strings.EqualFold(section, "SkyModels") {
				continue
			}
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				continue
			}
			val := strings.TrimSpace(line[eq+1:])
			parts := strings.SplitN(val, ",", 2)
			if len(parts) < 2 {
				continue
			}
			pathPart := strings.TrimSpace(parts[0])
			nameKey := strings.TrimSpace(parts[1])
			if pathPart == "" {
				continue
			}
			display := strs.Resolve(nameKey) // Resolve returns the input if no WESTRING match — safe for any-key
			out = append(out, SkyModelOption{
				Path:        normalizeSkyPath(pathPart),
				NameKey:     nameKey,
				DisplayName: display,
			})
		}
		skyModelsCache = out
		log.Printf("sky: loaded %d SkyModels entries", len(out))
	})
	return skyModelsCache
}
