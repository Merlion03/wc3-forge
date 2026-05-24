package bridge

import "encoding/json"

// RegisterDefaults wires the baseline handler set. Phase 0 = bridge.ping only.
// Subsequent phases extend this from their own packages by calling Register
// on the live Bridge.
func RegisterDefaults(b *Bridge) {
	b.Register("bridge.ping", handlePing)
}

type pingResponse struct {
	OK        bool   `json:"ok"`
	Version   string `json:"version"`
	MapLoaded bool   `json:"map_loaded"`
}

func handlePing(_ json.RawMessage) (any, error) {
	// Phase 0: no map can be loaded yet. Match the C++ shape so the existing
	// HiveWE Node MCP client doesn't notice the swap.
	return pingResponse{
		OK:        true,
		Version:   Version,
		MapLoaded: false,
	}, nil
}
