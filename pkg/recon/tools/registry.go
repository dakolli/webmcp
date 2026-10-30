package tools

import (
	"sort"
	"sync"

	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
)

// typed holds every wired recon tool under its MCP name.
var (
	typedMu sync.RWMutex
	typed   = make(map[string]toolenv.Tool)
)

// Register adds one typed recon tool under its Name. A later registration with
// the same name replaces the earlier one.
func Register(tool toolenv.Tool) {
	typedMu.Lock()
	typed[tool.Name()] = tool
	typedMu.Unlock()
}

// Get returns the typed tool registered under name.
func Get(name string) (toolenv.Tool, bool) {
	typedMu.RLock()
	defer typedMu.RUnlock()
	tool, ok := typed[name]
	return tool, ok
}

// All returns every registered typed tool sorted by name, so tool listings stay
// deterministic. An empty registry returns an empty slice, never an error.
func All() []toolenv.Tool {
	typedMu.RLock()
	out := make([]toolenv.Tool, 0, len(typed))
	for _, tool := range typed {
		out = append(out, tool)
	}
	typedMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func init() {
	Register(NewSubdomains())
	Register(NewDNS())
	Register(NewHTTP())
	Register(NewPorts())
	Register(NewURLs())
	Register(NewFuzz())
	Register(NewJS())
	Register(NewTemplates())
	Register(NewRun())
}
