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
	"slices"
	"sort"
	"strings"

	"github.com/dakolli/webmcp/pkg/browsers"
	caidotools "github.com/dakolli/webmcp/pkg/caido/tools"
	"github.com/dakolli/webmcp/pkg/config"
	"github.com/dakolli/webmcp/pkg/mcp/models"
)

// Registry manages a collection of tools and prompts, and dispatches execution calls.
type Registry struct {
	tools   map[string]Tool
	prompts map[string]Prompt
}

// NewRegistry constructs a Registry and registers all available tools and prompts,
// wiring them to a single shared browsers.Manager. The manager defaults to
// ephemeral profiles and honors the configured browser idle TTL.
func NewRegistry() *Registry {
	r := NewEmptyRegistry()
	mgr := browsers.NewManager(mcpManagerOptions()...)
	r.Register(NewFetch(mgr))
	r.Register(NewPage(mgr))
	r.Register(NewAct(mgr))
	r.Register(NewSessionClose(mgr))
	r.Register(NewSessionList(mgr))
	r.Register(NewBrowserCreate(mgr))
	r.Register(NewBrowserList(mgr))
	r.Register(NewBrowserClose(mgr))
	r.Register(NewPageList(mgr))
	if config.ActiveCaido().Enabled {
		r.Register(caidotools.NewCaidoSearch())
		r.Register(caidotools.NewCaidoStatus())
		r.Register(caidotools.NewCaidoSitemap())
		r.Register(caidotools.NewCaidoFinding())
	}

	r.RegisterPrompt(NewFetchAndSummarizePrompt())
	r.RegisterPrompt(NewInspectPageDecompositionPrompt())
	r.RegisterPrompt(NewFormSearchAndSubmitPrompt())
	r.RegisterPrompt(NewBrowserSetupAndEmulatePrompt())
	r.RegisterPrompt(NewManageSessionLifecyclePrompt())
	return r
}

// mcpManagerOptions returns the browser-manager options for an MCP-owned pool:
// ephemeral profiles by default (so a forgotten close cannot leak profiles) and
// the configured browser idle TTL from config.json (browser_ttl).
func mcpManagerOptions() []browsers.Option {
	ttl := browsers.DefaultBrowserTTL
	if cfg, err := config.LoadConfig(); err == nil {
		ttl = cfg.BrowserIdleTTL(ttl)
	}
	return []browsers.Option{
		browsers.WithPersistentDefault(false),
		browsers.WithBrowserTTL(ttl),
	}
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

// Execute runs a call to the registered tool with the given name. A supplied
// argument the tool's InputSchema does not declare is refused as invalid_args
// before dispatch, so a plausible-but-unknown key fails instead of silently
// doing nothing. Tool failures travel inside the tool's result envelope; an
// error is returned only when the tool itself is unknown.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unsupported tool: %s", name)
	}
	if msg := unknownArgument(t, args); msg != "" {
		return marshalEnvelope(resultEnvelope{
			Error: &envelopeError{
				Type:    errInvalidArgs,
				Message: msg,
			},
		})
	}
	return t.Execute(ctx, args)
}

// unknownArgument returns the invalid_args sentence for the first supplied
// argument the tool's InputSchema does not declare, and names the sorted valid
// arguments. It returns "" when every key is known or the arguments are absent,
// empty, or not a JSON object; decodeArgs reports those shapes itself.
func unknownArgument(t Tool, args json.RawMessage) string {
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal(args, &supplied); err != nil || len(supplied) == 0 {
		return ""
	}

	valid := make([]string, 0, len(t.InputSchema().Properties))
	for key := range t.InputSchema().Properties {
		valid = append(valid, key)
	}
	sort.Strings(valid)

	keys := make([]string, 0, len(supplied))
	for key := range supplied {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if slices.Contains(valid, key) {
			continue
		}
		if len(valid) == 0 {
			return fmt.Sprintf(
				"%s: unknown argument %q — %s takes no arguments",
				t.Name(),
				key,
				t.Name(),
			)
		}
		return fmt.Sprintf(
			"%s: unknown argument %q — valid arguments are %s",
			t.Name(),
			key,
			strings.Join(valid, ", "),
		)
	}
	return ""
}
