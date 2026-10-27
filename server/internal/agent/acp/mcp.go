package acp

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	types "mindfs/server/internal/agent/types"
)

// toACPMcpServers converts MindFS' agent config into the ACP session payload.
// Invalid entries are skipped instead of failing the whole session startup:
// an unusable MCP server should not make the agent unreachable.
func toACPMcpServers(defs []types.MCPServer) []acpsdk.McpServer {
	if len(defs) == 0 {
		return []acpsdk.McpServer{}
	}
	out := make([]acpsdk.McpServer, 0, len(defs))
	for _, def := range defs {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(def.Type))
		if kind == "" {
			if strings.TrimSpace(def.URL) != "" {
				kind = "sse"
			} else {
				kind = "stdio"
			}
		}
		switch kind {
		case "http", "sse":
			url := strings.TrimSpace(def.URL)
			if url == "" {
				continue
			}
			if kind == "http" {
				out = append(out, acpsdk.McpServer{Http: &acpsdk.McpServerHttpInline{
					Name:    name,
					Type:    "http",
					Url:     url,
					Headers: toACPHeaders(def.Headers),
				}})
				continue
			}
			out = append(out, acpsdk.McpServer{Sse: &acpsdk.McpServerSseInline{
				Name:    name,
				Type:    "sse",
				Url:     url,
				Headers: toACPHeaders(def.Headers),
			}})
		default:
			command := strings.TrimSpace(def.Command)
			if command == "" {
				continue
			}
			out = append(out, acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
				Name:    name,
				Command: command,
				Args:    append([]string{}, def.Args...),
				Env:     toACPEnv(def.Env),
			}})
		}
	}
	return out
}

func toACPHeaders(headers map[string]string) []acpsdk.HttpHeader {
	if len(headers) == 0 {
		return []acpsdk.HttpHeader{}
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]acpsdk.HttpHeader, 0, len(keys))
	for _, key := range keys {
		out = append(out, acpsdk.HttpHeader{Name: key, Value: headers[key]})
	}
	return out
}

func toACPEnv(env map[string]string) []acpsdk.EnvVariable {
	if len(env) == 0 {
		return []acpsdk.EnvVariable{}
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]acpsdk.EnvVariable, 0, len(keys))
	for _, key := range keys {
		out = append(out, acpsdk.EnvVariable{Name: key, Value: env[key]})
	}
	return out
}

// mcpConfigFile is the shared "mcpServers" JSON shape used by omp/Claude/Cursor.
type mcpConfigFile struct {
	MCPServers map[string]mcpConfigEntry `json:"mcpServers"`
	Servers    map[string]mcpConfigEntry `json:"servers"`
}

type mcpConfigEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Headers map[string]string `json:"headers"`
}

// resolveMCPServers merges MCP servers declared in config files with the ones
// inlined in agents.json. Inline entries win when the names collide, so a user
// can override a single server without editing the agent's own config file.
func resolveMCPServers(inline []types.MCPServer, configFiles []string) []types.MCPServer {
	var merged []types.MCPServer
	index := make(map[string]int)
	add := func(server types.MCPServer) {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			return
		}
		server.Name = name
		if position, ok := index[name]; ok {
			merged[position] = server
			return
		}
		index[name] = len(merged)
		merged = append(merged, server)
	}
	for _, path := range configFiles {
		servers, err := loadMCPConfigFile(path)
		if err != nil {
			// A missing or broken MCP config must never block the agent.
			log.Printf("[agent/acp] mcp.config_skip file=%s err=%v", path, err)
			continue
		}
		for _, server := range servers {
			add(server)
		}
	}
	for _, server := range inline {
		add(server)
	}
	return merged
}

func loadMCPConfigFile(path string) ([]types.MCPServer, error) {
	resolved, err := expandHomePath(path)
	if err != nil {
		return nil, err
	}
	payload, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	var doc mcpConfigFile
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, err
	}
	entries := doc.MCPServers
	if len(entries) == 0 {
		entries = doc.Servers
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	servers := make([]types.MCPServer, 0, len(names))
	for _, name := range names {
		entry := entries[name]
		servers = append(servers, types.MCPServer{
			Name:    name,
			Type:    entry.Type,
			Command: entry.Command,
			Args:    entry.Args,
			Env:     entry.Env,
			URL:     entry.URL,
			Headers: entry.Headers,
		})
	}
	return servers, nil
}

func expandHomePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("empty path")
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if trimmed == "~" {
			return home, nil
		}
		return filepath.Join(home, trimmed[2:]), nil
	}
	return trimmed, nil
}
