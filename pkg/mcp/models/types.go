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
	Instructions    string             `json:"instructions,omitempty"`
}

// ServerInfo contains metadata details about this server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCapabilities defines features the server supports.
type ServerCapabilities struct {
	Tools   map[string]any `json:"tools"`
	Prompts map[string]any `json:"prompts,omitempty"`
}

// MCPPrompt represents a reusable prompt template exposed to the client.
type MCPPrompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument defines an argument accepted by a prompt template.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// ListPromptsResult represents the list of prompts returned to the client.
type ListPromptsResult struct {
	Prompts []MCPPrompt `json:"prompts"`
}

// GetPromptRequest represents parameters to fetch a prompt template.
type GetPromptRequest struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// GetPromptResult represents the generated prompt messages payload.
type GetPromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// PromptMessage represents a message in a prompt conversation template.
type PromptMessage struct {
	Role    string      `json:"role"`
	Content TextContent `json:"content"`
}

// TextContent represents a simple text content block within a prompt message.
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
