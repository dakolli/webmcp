// Package tools implements the Model Context Protocol (MCP) tools that expose a
// stateful, session-based browser to LLM agents via go-rod. Each tool is a
// struct satisfying the Tool interface; a shared SessionManager owns the
// *rod.Browser and the session map that all tools operate on.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"webmcp/pkg/mcp/models"
)

// Tool represents a Model Context Protocol tool implementation.
//
// A tool is a stateless struct (typically holding only a pointer to the shared
// SessionManager); every invocation arrives through Execute with the raw JSON
// arguments of the call and returns a string (plain text or JSON) for the LLM.
type Tool interface {
	// Name returns the tool's MCP name, e.g. "fetch_page".
	Name() string
	// Description returns a customer/LLM-facing description of what the tool
	// does and when an agent should call it.
	Description() string
	// InputSchema returns the JSON Schema describing the tool's parameters.
	InputSchema() models.InputSchema
	// Execute runs the tool with the given raw JSON arguments.
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry manages a collection of tools and dispatches execution calls.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry constructs a Registry and registers all available tools,
// wiring them to a single shared SessionManager.
func NewRegistry() *Registry {
	r := NewEmptyRegistry()
	sm := NewSessionManager()
	r.Register(NewFetch(sm))
	r.Register(NewPage(sm))
	r.Register(NewAct(sm))
	r.Register(NewSessionClose(sm))
	r.Register(NewSessionList(sm))
	return r
}

// NewEmptyRegistry constructs a Registry without registering any tools.
func NewEmptyRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry, keyed by its Name.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// List returns all registered tools as models.MCPTool.
func (r *Registry) List() []models.MCPTool {
	list := make([]models.MCPTool, 0, len(r.tools))
	for _, t := range r.tools {
		list = append(list, models.MCPTool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return list
}

// Execute runs a call to the registered tool with the given name.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unsupported tool: %s", name)
	}
	return t.Execute(ctx, args)
}
