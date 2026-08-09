package mcpserver

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
)

// agentCatalogFS contains the fork-owned additive MCP catalogs. Keeping these
// separate from generated tools.json means upstream catalog regeneration stays
// conflict-free while fork features can ship independently.
//
//go:embed agent_tools*.json
var agentCatalogFS embed.FS

func init() {
	var base []json.RawMessage
	if err := json.Unmarshal(catalogJSON, &base); err != nil {
		panic(fmt.Errorf("parse embedded upstream MCP catalog: %w", err))
	}

	names, err := fs.Glob(agentCatalogFS, "agent_tools*.json")
	if err != nil {
		panic(fmt.Errorf("list embedded agent MCP catalogs: %w", err))
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := agentCatalogFS.ReadFile(name)
		if err != nil {
			panic(fmt.Errorf("read embedded agent MCP catalog %s: %w", name, err))
		}
		var extra []json.RawMessage
		if err := json.Unmarshal(raw, &extra); err != nil {
			panic(fmt.Errorf("parse embedded agent MCP catalog %s: %w", name, err))
		}
		base = append(base, extra...)
	}

	merged, err := json.Marshal(base)
	if err != nil {
		panic(fmt.Errorf("merge embedded MCP catalogs: %w", err))
	}
	catalogJSON = merged
}
