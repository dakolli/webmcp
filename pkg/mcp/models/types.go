// Package types defines the data structures used by the Model Context
// Protocol (MCP) server.
package models

import "encoding/json"

// JSONRPCRequest represents a standard JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents a standard JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

// JSONRPCError represents a standard JSON-RPC 2.0 error payload.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// InitializeResult represents the server config during handshake.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Capabilities    ServerCapabilities `json:"capabilities"`
}

// ServerInfo contains metadata details about this server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCapabilities defines features the server supports.
type ServerCapabilities struct {
	Tools map[string]any `json:"tools"`
}

// MCPTool represents an operational tool exposed to the AI client.
type MCPTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema defines parameters structure using JSON Schema syntax.
type InputSchema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required,omitempty"`
}

// ListToolsResult represents the list of tools returned to the client.
type ListToolsResult struct {
	Tools []MCPTool `json:"tools"`
}

// CallToolRequest represents parameters to invoke a tool.
type CallToolRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// CallToolResult represents the text output of executing a tool.
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
}

// ContentBlock contains a text output block for the client.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
