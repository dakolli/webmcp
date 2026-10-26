// Package tools implements the Model Context Protocol (MCP) tools that expose a
// stateful, session-based browser to LLM agents. Each tool is a struct
// satisfying the Tool interface and translates MCP calls into the core engine
// in pkg/browsers; a shared browsers.Manager owns the single Browser whose
// pages are the MCP sessions that all tools operate on. This package never
// touches go-rod directly.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"webmcp/pkg/browsers"
	"webmcp/pkg/config"
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

// toolExamples maps each tool to its shortest valid retry call. It is shown
// alongside an argument type mismatch so the agent can copy a working call.
var toolExamples = map[string]string{
	"browser_create": `{"proxy":"127.0.0.1:8080","ignore_certs":true}`,
	"browser_close":  `{"browser_id":"browser-1"}`,
	"page_list":      `{"browser_id":"browser-1"}`,
	"fetch_page":     `{"url":"https://example.com"}`,
	"page_fetch":     `{"session_id":"page-1","what":"text"}`,
	"page_act":       `{"session_id":"page-1","action":"reload"}`,
	"session_close":  `{"session_id":"page-1"}`,
	"browser_list":   `{}`,
	"session_list":   `{}`,
}

// Execute runs a call to the registered tool with the given name. Tool
// failures travel inside the tool's result envelope; an error is returned only
// when the tool itself is unknown.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unsupported tool: %s", name)
	}
	return t.Execute(ctx, args)
}

// decodeArgs decodes raw tool arguments into dst. Boolean strings and
// JSON-encoded arrays or objects are coerced, and a type mismatch is rewritten
// into a sentence naming the field, wanted type, received value and a working
// retry call.
func decodeArgs(tool string, args json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		return nil
	}
	normalized := coerceBooleanStrings(args)
	if err := json.Unmarshal(normalized, dst); err != nil {
		if retryArgs, ok := coerceJSONStringArgs(normalized, err); ok {
			if json.Unmarshal(retryArgs, dst) == nil {
				return nil
			}
		}
		if explained := explainArgTypeError(tool, normalized, err); explained != err {
			return explained
		}
		return fmt.Errorf("%s: invalid arguments: %w", tool, err)
	}
	return nil
}

// coerceJSONStringArgs rewrites the single argument that failed to decode from a
// JSON string into the array or object it encodes, so clients that stringify
// every non-scalar can still fill structured fields. It reports false unless the
// failure is a type error for a field that wants a slice, array, map or struct
// and the string decodes to exactly that JSON kind.
func coerceJSONStringArgs(args json.RawMessage, err error) (json.RawMessage, bool) {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		return nil, false
	}
	want := typeErr.Type
	for want != nil && want.Kind() == reflect.Pointer {
		want = want.Elem()
	}
	if want == nil {
		return nil, false
	}
	switch want.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
	default:
		return nil, false
	}

	raw, ok := fieldValue(args, typeErr.Field)
	if !ok {
		return nil, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil, false
	}
	switch parsed.(type) {
	case []any:
		if want.Kind() != reflect.Slice && want.Kind() != reflect.Array {
			return nil, false
		}
	case map[string]any:
		if want.Kind() != reflect.Map && want.Kind() != reflect.Struct {
			return nil, false
		}
	default:
		return nil, false
	}

	var set func(json.RawMessage, []string, json.RawMessage) (json.RawMessage, bool)
	set = func(cur json.RawMessage, path []string, val json.RawMessage) (json.RawMessage, bool) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false
		}
		child, ok := obj[path[0]]
		if !ok {
			return nil, false
		}
		if len(path) == 1 {
			obj[path[0]] = val
		} else {
			child, ok = set(child, path[1:], val)
			if !ok {
				return nil, false
			}
			obj[path[0]] = child
		}
		out, err := json.Marshal(obj)
		if err != nil {
			return nil, false
		}
		return out, true
	}
	return set(args, strings.Split(typeErr.Field, "."), json.RawMessage(strings.TrimSpace(s)))
}

// coerceBooleanStrings rewrites top-level arguments whose value is the JSON
// string "true" or "false" (case-insensitive, trimmed) into the JSON boolean
// literal. Clients that stringify every argument can then still set boolean
// fields; every other string is left untouched so type errors stay actionable.
func coerceBooleanStrings(args json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return args
	}
	changed := false
	for key, raw := range obj {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		switch {
		case strings.EqualFold(strings.TrimSpace(s), "true"):
			obj[key] = json.RawMessage("true")
			changed = true
		case strings.EqualFold(strings.TrimSpace(s), "false"):
			obj[key] = json.RawMessage("false")
			changed = true
		}
	}
	if !changed {
		return args
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return args
	}
	return out
}

// explainArgTypeError replaces a JSON type mismatch in tool arguments with a
// sentence naming the tool, field, wanted type and received value, followed by
// a working retry call for that tool. Non-type errors are returned unchanged.
func explainArgTypeError(
	tool string,
	args json.RawMessage,
	err error,
) error {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		return err
	}
	msg := fmt.Sprintf(
		"%s: %q must be %s, but the request sent %s.",
		tool,
		typeErr.Field,
		wantedType(typeErr.Type),
		sentValue(args, typeErr.Field),
	)
	if example, ok := toolExamples[tool]; ok {
		msg += "\nRetry with: " + example
	}
	return errors.New(msg)
}

// wantedType renders a JSON field's expected type in plain words.
func wantedType(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "the correct type"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "a boolean (true or false)"
	case reflect.String:
		return "a string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	default:
		return "a " + t.Kind().String()
	}
}

// sentValue describes the raw argument value that failed to decode. The field
// may be nested, so it is resolved segment by segment through the arguments.
func sentValue(args json.RawMessage, field string) string {
	raw, ok := fieldValue(args, field)
	if !ok {
		return "an unexpected value"
	}
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "" || s == "null":
		return "null"
	case s[0] == '"':
		return "the string " + clipValue(s)
	case s[0] == '{':
		return "the object " + clipValue(s)
	case s[0] == '[':
		return "the array " + clipValue(s)
	case s == "true" || s == "false":
		return "the boolean " + s
	default:
		return "the number " + clipValue(s)
	}
}

// fieldValue resolves a dotted JSON field path against the raw arguments.
func fieldValue(args json.RawMessage, field string) (json.RawMessage, bool) {
	cur := args
	for _, part := range strings.Split(field, ".") {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false
		}
		next, ok := obj[part]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// clipValue caps a rendered value so a large array or object cannot flood the
// error message.
func clipValue(s string) string {
	const limit = 80
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
