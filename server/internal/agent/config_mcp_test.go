package agent

import (
	"testing"

	types "mindfs/server/internal/agent/types"
)

func TestMergeAgentDefinitionKeepsBaseMCPServers(t *testing.T) {
	base := Definition{
		Name: "omp",
		MCPServers: []types.MCPServer{
			{Name: "idea-mcp", Type: "sse", URL: "http://127.0.0.1:64342/sse"},
		},
	}
	override := Definition{Name: "omp", Command: "omp"}
	merged := mergeAgentDefinition(base, override)
	if len(merged.MCPServers) != 1 || merged.MCPServers[0].Name != "idea-mcp" {
		t.Fatalf("base mcp servers lost during merge: %+v", merged.MCPServers)
	}

	own := Definition{
		Name:       "omp",
		MCPServers: []types.MCPServer{{Name: "custom", Type: "http", URL: "http://127.0.0.1:9000/mcp"}},
	}
	merged = mergeAgentDefinition(base, own)
	if len(merged.MCPServers) != 1 || merged.MCPServers[0].Name != "custom" {
		t.Fatalf("override mcp servers not respected: %+v", merged.MCPServers)
	}
}

func TestDecodeConfigParsesMCPServers(t *testing.T) {
	payload := []byte(`{"agents":[{"name":"omp","command":"omp","protocol":"acp","mcpServers":[{"name":"idea","type":"sse","url":"http://127.0.0.1:64342/sse","headers":{"X-Test":"1"}}]}]}`)
	cfg, err := DecodeConfig(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cfg.Agents) != 1 || len(cfg.Agents[0].MCPServers) != 1 {
		t.Fatalf("mcp servers not decoded: %+v", cfg.Agents)
	}
	server := cfg.Agents[0].MCPServers[0]
	if server.Name != "idea" || server.Type != "sse" || server.Headers["X-Test"] != "1" {
		t.Fatalf("unexpected server: %+v", server)
	}
}
