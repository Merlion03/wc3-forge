package w3i

import (
	"encoding/json"
	"os"
	"testing"
)

// Path to the wc3-survival v1.6 fixture. The map lives in a sibling project
// under ~/projects/wc3-survival-game/. We use an absolute path on purpose:
// the file is a real extracted-from-MPQ Reforged Lua map info blob (309 B,
// file_version 33) and not something we want to vendor into this repo.
const fixtureV1_6 = `C:\Users\4step\projects\wc3-survival-game\map\extracted\war3map.w3i`

func TestParse_v1_6(t *testing.T) {
	data, err := os.ReadFile(fixtureV1_6)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	info, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if info.FileVersion < FileVersionTFT {
		t.Errorf("file_version %d unexpectedly low — fixture should be a modern Reforged map", info.FileVersion)
	}
	if info.Name == "" {
		t.Errorf("map name is empty")
	}
	if info.Author == "" {
		t.Errorf("map author is empty")
	}
	if info.PlayableWidth <= 0 || info.PlayableWidth >= 1024 {
		t.Errorf("playable width %d outside sane bounds (0,1024)", info.PlayableWidth)
	}
	if info.PlayableHeight <= 0 || info.PlayableHeight >= 1024 {
		t.Errorf("playable height %d outside sane bounds (0,1024)", info.PlayableHeight)
	}
	if len(info.Players) < 1 {
		t.Errorf("expected at least 1 player slot, got %d", len(info.Players))
	}

	// Pretty-print for visual sanity; only emitted with -v.
	pretty, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatalf("marshal Info: %v", err)
	}
	t.Logf("Parsed war3map.w3i (file_version %d):\n%s", info.FileVersion, pretty)
}
