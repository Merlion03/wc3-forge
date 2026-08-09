package forge

import (
	"encoding/json"
	"fmt"
)

func handleMapApplyPatch(params json.RawMessage) (any, error) {
	var req MapApplyPatchRequest
	if err := decodePatchStrict(params, &req); err != nil {
		return nil, err
	}
	if req.Operations == nil {
		return nil, fmt.Errorf("operations is required")
	}
	return Current.ApplyMapPatch(req)
}

func handleMapDiff(params json.RawMessage) (any, error) {
	var req MapDiffRequest
	if err := decodePatchStrict(params, &req); err != nil {
		return nil, err
	}
	if req.Operations == nil {
		return nil, fmt.Errorf("operations is required")
	}
	return Current.PreviewMapPatch(req)
}

func handleMapValidate(_ json.RawMessage) (any, error) {
	result, err := Current.ValidateMap()
	if err != nil {
		return nil, fmt.Errorf("validate map: %w", err)
	}
	return result, nil
}
