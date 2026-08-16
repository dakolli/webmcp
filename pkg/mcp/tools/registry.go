// Package tools implements the Model Context Protocol (MCP) tools that expose a
// stateful, session-based browser to LLM agents. Each tool is a struct
// satisfying the Tool interface and translates MCP calls into the core engine
// in pkg/browsers; a shared browsers.Manager owns the single Browser whose
// pages are the MCP sessions that all tools operate on. This package never
// touches go-rod directly.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"webmcp/pkg/browsers"
	"webmcp/pkg/mcp/models"
)

// Tool represents a Model Context Protocol tool implementation.
//
// A tool is a stateless struct (typically holding only a pointer to the shared
// browsers.Manager); every invocation arrives through Execute with the raw JSON
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

// Registry manages a collection of tools and prompts, and dispatches execution calls.
type Registry struct {
	tools   map[string]Tool
	prompts map[string]Prompt
}

// NewRegistry constructs a Registry and registers all available tools and prompts,
// wiring them to a single shared browsers.Manager.
func NewRegistry() *Registry {
	r := NewEmptyRegistry()
	mgr := browsers.NewManager()
	r.Register(NewFetch(mgr))
	r.Register(NewPage(mgr))
	r.Register(NewAct(mgr))
	r.Register(NewSessionClose(mgr))
	r.Register(NewSessionList(mgr))
	r.Register(NewBrowserCreate(mgr))
	r.Register(NewBrowserList(mgr))
	r.Register(NewBrowserClose(mgr))
	r.Register(NewPageList(mgr))

	r.RegisterPrompt(NewFetchAndSummarizePrompt())
	r.RegisterPrompt(NewInspectPageDecompositionPrompt())
	r.RegisterPrompt(NewFormSearchAndSubmitPrompt())
	r.RegisterPrompt(NewBrowserSetupAndEmulatePrompt())
	r.RegisterPrompt(NewManageSessionLifecyclePrompt())
	return r
}

// NewEmptyRegistry constructs a Registry without registering any tools or prompts.
func NewEmptyRegistry() *Registry {
	return &Registry{
		tools:   make(map[string]Tool),
		prompts: make(map[string]Prompt),
	}
}

// Register adds a tool to the registry, keyed by its Name.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// RegisterPrompt adds a prompt template to the registry, keyed by its Name.
func (r *Registry) RegisterPrompt(p Prompt) {
	r.prompts[p.Name()] = p
}

// List returns all registered tools as models.MCPTool, sorted
// deterministically by tool name so that tools/list output is reproducible
// across calls (map iteration order is otherwise random).
func (r *Registry) List() []models.MCPTool {
	list := make([]models.MCPTool, 0, len(r.tools))
	for _, t := range r.tools {
		list = append(list, models.MCPTool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

// ListPrompts returns all registered prompts as models.MCPPrompt, sorted
// deterministically by prompt name.
func (r *Registry) ListPrompts() []models.MCPPrompt {
	list := make([]models.MCPPrompt, 0, len(r.prompts))
	for _, p := range r.prompts {
		list = append(list, models.MCPPrompt{
			Name:        p.Name(),
			Description: p.Description(),
			Arguments:   p.Arguments(),
		})
	}
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

// GetPrompt builds a GetPromptResult for the prompt with the given name and arguments.
func (r *Registry) GetPrompt(name string, args map[string]string) (*models.GetPromptResult, error) {
	p, ok := r.prompts[name]
	if !ok {
		return nil, fmt.Errorf("prompt not found: %s", name)
	}
	msgs, err := p.GetMessages(args)
	if err != nil {
		return nil, fmt.Errorf("building prompt %s: %w", name, err)
	}
	return &models.GetPromptResult{
		Description: p.Description(),
		Messages:    msgs,
	}, nil
}

// Execute runs a call to the registered tool with the given name.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unsupported tool: %s", name)
	}
	return t.Execute(ctx, args)
}
