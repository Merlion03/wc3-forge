package mcpserver

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// agentCatalogJSON is the fork-owned additive MCP catalog. The upstream
// tools.json remains generated exclusively from mcp/src/tools.ts, so upstream
// catalog regeneration stays conflict-free while fork-specific tools can ship
// independently.
//
//go:embed agent_tools.json
var agentCatalogJSON []byte

func init() {
	var base []json.RawMessage
	if err := json.Unmarshal(catalogJSON, &base); err != nil {
		panic(fmt.Errorf("parse embedded upstream MCP catalog: %w", err))
	}
	var extra []json.RawMessage
	if err := json.Unmarshal(agentCatalogJSON, &extra); err != nil {
		panic(fmt.Errorf("parse embedded agent MCP catalog: %w", err))
	}
	merged, err := json.Marshal(append(base, extra...))
	if err != nil {
		panic(fmt.Errorf("merge embedded MCP catalogs: %w", err))
	}
	catalogJSON = merged
}
