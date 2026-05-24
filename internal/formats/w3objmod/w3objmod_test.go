package w3objmod

import (
	"os"
	"path/filepath"
	"testing"
)

// Sanity-parse the extracted Enfos w3d/w3b if present. The fixture lives
// outside the repo so this test skips when it's absent.
func TestParseEnfos(t *testing.T) {
	const dir = `C:\Users\4step\AppData\Local\Temp\enfos-extracted`
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no fixture at %s", dir)
	}

	for _, c := range []struct {
		name string
		file string
		opt  bool
	}{
		{"doodads", "war3map.w3d", true},
		{"destructibles", "war3map.w3b", false},
	} {
		p := filepath.Join(dir, c.file)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Logf("%s: skip (read: %v)", c.name, err)
			continue
		}
		f, err := Parse(data, c.opt, nil)
		if err != nil {
			t.Errorf("%s parse: %v", c.name, err)
			continue
		}
		t.Logf("%s v=%d: %d original edits, %d customs",
			c.name, f.Version, len(f.OriginalEdits), len(f.Customs))
		for _, e := range f.OriginalEdits {
			t.Logf("  edit on %s overrides=%v", e.BaseID, e.Overrides)
		}
		for i, co := range f.Customs {
			if i >= 6 {
				break
			}
			t.Logf("  custom %s ← %s overrides=%v", co.ID, co.BaseID, co.Overrides)
		}
	}
}
