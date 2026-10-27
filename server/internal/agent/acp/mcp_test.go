package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	types "mindfs/server/internal/agent/types"
)

func TestToACPMcpServersMapsTransports(t *testing.T) {
	servers := toACPMcpServers([]types.MCPServer{
		{Name: "idea", Type: "sse", URL: "http://127.0.0.1:64342/sse", Headers: map[string]string{"X-Test": "1"}},
		{Name: "web", URL: "http://127.0.0.1:9000/mcp", Type: "http"},
		{Name: "local", Command: "npx", Args: []string{"-y", "demo"}, Env: map[string]string{"KEY": "v"}},
		{Name: "  "},
		{Name: "broken", Type: "sse"},
	})
	if len(servers) != 3 {
		t.Fatalf("expected 3 valid servers, got %d", len(servers))
	}
	if servers[0].Sse == nil || servers[0].Sse.Url != "http://127.0.0.1:64342/sse" || servers[0].Sse.Type != "sse" {
		t.Fatalf("sse server not mapped: %+v", servers[0])
	}
	if len(servers[0].Sse.Headers) != 1 || servers[0].Sse.Headers[0].Name != "X-Test" {
		t.Fatalf("headers not mapped: %+v", servers[0].Sse.Headers)
	}
	if servers[1].Http == nil || servers[1].Http.Type != "http" {
		t.Fatalf("http server not mapped: %+v", servers[1])
	}
	if servers[2].Stdio == nil || servers[2].Stdio.Command != "npx" || len(servers[2].Stdio.Args) != 2 {
		t.Fatalf("stdio server not mapped: %+v", servers[2])
	}
	if len(servers[2].Stdio.Env) != 1 || servers[2].Stdio.Env[0].Name != "KEY" {
		t.Fatalf("stdio env not mapped: %+v", servers[2].Stdio.Env)
	}
}

func TestToACPMcpServersEmptyReturnsEmptySlice(t *testing.T) {
	if got := toACPMcpServers(nil); got == nil || len(got) != 0 {
		t.Fatalf("expected non-nil empty slice, got %#v", got)
	}
}

func TestACPMcpServerJSONShape(t *testing.T) {
	payload, err := json.Marshal(toACPMcpServers([]types.MCPServer{
		{Name: "idea", Type: "sse", URL: "http://127.0.0.1:64342/sse"},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 server, got %d", len(decoded))
	}
	if decoded[0]["type"] != "sse" || decoded[0]["url"] != "http://127.0.0.1:64342/sse" || decoded[0]["name"] != "idea" {
		t.Fatalf("unexpected payload: %s", payload)
	}
	// omp's ACP bridge iterates `headers` unconditionally; a missing key crashes
	// session/new with "undefined is not an object", so the field must always exist.
	if _, ok := decoded[0]["headers"]; !ok {
		t.Fatalf("headers must be present even when empty: %s", payload)
	}
}

func TestResolveMCPServersMergesConfigFileAndInline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	content := `{"mcpServers":{"from-file":{"type":"http","url":"http://127.0.0.1:9000/mcp","headers":{"A":"b"}},"shared":{"type":"sse","url":"http://from-file/sse"}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	servers := resolveMCPServers([]types.MCPServer{
		{Name: "shared", Type: "sse", URL: "http://inline/sse"},
		{Name: "idea-mcp", Type: "sse", URL: "http://127.0.0.1:64342/sse"},
	}, []string{path, filepath.Join(dir, "missing.json")})
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d: %+v", len(servers), servers)
	}
	if servers[0].Name != "from-file" || servers[0].URL != "http://127.0.0.1:9000/mcp" {
		t.Fatalf("file server not loaded: %+v", servers[0])
	}
	if servers[1].Name != "shared" || servers[1].URL != "http://inline/sse" {
		t.Fatalf("inline server should win: %+v", servers[1])
	}
	if servers[2].Name != "idea-mcp" {
		t.Fatalf("unexpected order: %+v", servers)
	}
}
