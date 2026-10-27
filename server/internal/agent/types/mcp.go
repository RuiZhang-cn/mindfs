package types

// MCPServer describes an MCP server that MindFS declares to an ACP agent when
// creating or resuming a session. The agent connects to these servers itself;
// MindFS only forwards the declaration.
//
// Exactly one transport is used:
//   - Type "stdio" (default when Command is set): Command/Args/Env.
//   - Type "http" or "sse": URL plus optional Headers.
type MCPServer struct {
	Name    string            `json:"name"`
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}
