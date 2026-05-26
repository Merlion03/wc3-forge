package main

import "testing"

func TestScanSetSkyModel(t *testing.T) {
	cases := []struct {
		name      string
		j, lua    string
		wantPath  string
		wantFound bool
	}{
		{
			name:      "empty inputs",
			wantFound: false,
		},
		{
			// scanSetSkyModel returns raw source bytes (preserves doubled-
			// backslash convention used by both JASS and Lua authors), so the
			// downstream normalizer can re-emit identical replacement strings.
			name:      "jass single call",
			j:         `function config takes nothing returns nothing` + "\n" + `call SetSkyModel("Environment\\Sky\\NorthrendSky\\NorthrendSky.mdl")` + "\nendfunction",
			wantPath:  `Environment\\Sky\\NorthrendSky\\NorthrendSky.mdl`,
			wantFound: true,
		},
		{
			name:      "lua single call",
			lua:       `SetSkyModel("Environment\\Sky\\Outland_Sky\\Outland_Sky.mdl")`,
			wantPath:  `Environment\\Sky\\Outland_Sky\\Outland_Sky.mdl`,
			wantFound: true,
		},
		{
			name:      "lua wins over jass when both present",
			j:         `call SetSkyModel("a\\b.mdl")`,
			lua:       `SetSkyModel("c\\d.mdl")`,
			wantPath:  `c\\d.mdl`,
			wantFound: true,
		},
		{
			name:      "lua single quotes",
			lua:       `SetSkyModel('c\\d.mdl')`,
			wantPath:  `c\\d.mdl`,
			wantFound: true,
		},
		{
			name:      "multiple calls last wins",
			lua:       "SetSkyModel(\"first.mdl\")\nSetSkyModel(\"last.mdl\")\n",
			wantPath:  "last.mdl",
			wantFound: true,
		},
		{
			name:      "commented call ignored (jass)",
			j:         "// call SetSkyModel(\"ignored.mdl\")\n",
			wantFound: false,
		},
		{
			name:      "commented call ignored (lua)",
			lua:       "-- SetSkyModel('ignored.mdl')\n",
			wantFound: false,
		},
		{
			name:      "explicit empty call disables sky",
			lua:       `SetSkyModel("")`,
			wantPath:  ``,
			wantFound: true,
		},
		{
			name:      "whitespace in call",
			lua:       `SetSkyModel  (   "x.mdl"   )`,
			wantPath:  `x.mdl`,
			wantFound: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := scanSetSkyModel([]byte(c.j), []byte(c.lua))
			if found != c.wantFound {
				t.Errorf("found = %v, want %v", found, c.wantFound)
			}
			if got != c.wantPath {
				t.Errorf("path = %q, want %q", got, c.wantPath)
			}
		})
	}
}

func TestNormalizeSkyPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{`Environment\Sky\NorthrendSky\NorthrendSky.mdl`, "environment/sky/northrendsky/northrendsky.mdx"},
		{`Environment\Sky\Outland_Sky\Outland_Sky.mdx`, "environment/sky/outland_sky/outland_sky.mdx"},
		{`Environment\Sky\Foo\Foo`, "environment/sky/foo/foo.mdx"},
	}
	for _, c := range cases {
		got := normalizeSkyPath(c.in)
		if got != c.want {
			t.Errorf("normalizeSkyPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Realistic end-to-end: typical map source bytes → final asset-handler path.
// This is what the renderer actually consumes.
func TestScanThenNormalize(t *testing.T) {
	jSrc := []byte(`call SetSkyModel("Environment\\Sky\\LordaeronSummerSky\\LordaeronSummerSky.mdl")`)
	luaSrc := []byte(`SetSkyModel("Environment\\Sky\\NorthrendSky\\NorthrendSky.mdl")`)
	raw, ok := scanSetSkyModel(jSrc, luaSrc)
	if !ok {
		t.Fatalf("expected a hit")
	}
	got := normalizeSkyPath(raw)
	want := "environment/sky/northrendsky/northrendsky.mdx" // Lua wins, doubled-slash collapses, .mdl→.mdx
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
