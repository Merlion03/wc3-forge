package forge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/bridge"
)

type agentToolCatalogEntry struct {
	Name   string `json:"name"`
	Method string `json:"method"`
}

func readToolCatalogForTest(t *testing.T, name string) []agentToolCatalogEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "mcpserver", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var tools []agentToolCatalogEntry
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return tools
}

func readAgentToolCatalogsForTest(t *testing.T) []agentToolCatalogEntry {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "mcpserver", "agent_tools*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var tools []agentToolCatalogEntry
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var part []agentToolCatalogEntry
		if err := json.Unmarshal(raw, &part); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		tools = append(tools, part...)
	}
	return tools
}

func TestAgentToolsCatalogParity(t *testing.T) {
	b := bridge.New()
	RegisterAgentHandlers(b)
	registered := map[string]bool{}
	for _, method := range b.ListHandlers() {
		registered[method] = true
	}

	tools := readAgentToolCatalogsForTest(t)
	if len(tools) == 0 {
		t.Fatal("agent tool catalogs must not be empty")
	}

	seenNames := map[string]bool{}
	seenMethods := map[string]bool{}
	for _, tool := range tools {
		if tool.Name == "" || tool.Method == "" {
			t.Fatalf("agent tool has empty name/method: %#v", tool)
		}
		if seenNames[tool.Name] {
			t.Errorf("duplicate agent tool name %q", tool.Name)
		}
		if seenMethods[tool.Method] {
			t.Errorf("duplicate agent bridge method %q", tool.Method)
		}
		seenNames[tool.Name] = true
		seenMethods[tool.Method] = true
		if !registered[tool.Method] {
			t.Errorf("agent tool %q forwards to unregistered method %q", tool.Name, tool.Method)
		}
	}

	var missingCatalog []string
	for method := range registered {
		if !seenMethods[method] {
			missingCatalog = append(missingCatalog, method)
		}
	}
	sort.Strings(missingCatalog)
	if len(missingCatalog) > 0 {
		t.Errorf("registered agent methods missing from agent tool catalogs: %v", missingCatalog)
	}

	// The additive catalogs are merged into the upstream generated catalog at
	// startup. A duplicate MCP tool name would make tools/list ambiguous even
	// if the bridge methods differed, so guard the namespace explicitly.
	for _, tool := range readToolCatalogForTest(t, "tools.json") {
		if seenNames[tool.Name] {
			t.Errorf("agent tool name %q collides with upstream tools.json", tool.Name)
		}
	}
}
