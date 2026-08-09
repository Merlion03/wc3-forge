package forge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/bridge"
)

func TestAgentToolsCatalogParity(t *testing.T) {
	b := bridge.New()
	RegisterAgentHandlers(b)
	registered := map[string]bool{}
	for _, method := range b.ListHandlers() {
		registered[method] = true
	}

	raw, err := os.ReadFile(filepath.Join("..", "mcpserver", "agent_tools.json"))
	if err != nil {
		t.Fatalf("read agent_tools.json: %v", err)
	}
	var tools []struct {
		Name   string `json:"name"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("parse agent_tools.json: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("agent_tools.json must not be empty")
	}
	for _, tool := range tools {
		if tool.Name == "" || tool.Method == "" {
			t.Fatalf("agent tool has empty name/method: %#v", tool)
		}
		if !registered[tool.Method] {
			t.Errorf("agent tool %q forwards to unregistered method %q", tool.Name, tool.Method)
		}
	}
}
