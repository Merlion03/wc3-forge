package forge

import (
	"encoding/json"
	"fmt"
)

// handleSceneQuery exposes Session.QueryScene on the bridge as scene.query.
// It is strictly read-only: no dirty state, history entry, or UI event is
// produced by this handler.
func handleSceneQuery(params json.RawMessage) (any, error) {
	var q SceneQuery
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &q); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	return Current.QueryScene(q)
}
