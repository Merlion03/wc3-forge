package forge

import "github.com/StephenSHorton/wc3-forge/internal/bridge"

// RegisterAgentHandlers wires fork-specific high-level agent methods that are
// intentionally kept out of the upstream RegisterAll body. Keeping these
// registrations additive makes upstream syncs substantially less conflict-prone.
func RegisterAgentHandlers(b *bridge.Bridge) {
	b.Register("scene.query", instrumented("scene.query", handleSceneQuery))
	b.Register("map.diff", instrumented("map.diff", handleMapDiff))
	b.Register("map.apply_patch", instrumented("map.apply_patch", handleMapApplyPatch))
	b.Register("map.validate", instrumented("map.validate", handleMapValidate))
}
