package tools_test

import (
	"testing"

	mcptools "github.com/dakolli/webmcp/pkg/mcp/tools"
	"github.com/dakolli/webmcp/pkg/recon"
	recontools "github.com/dakolli/webmcp/pkg/recon/tools"
)

// TestRegistryWiresEveryScanner asserts every registered scanner spec resolves
// to a typed tool named after its spec ID.
func TestRegistryWiresEveryScanner(t *testing.T) {
	specs := recon.All()
	if len(specs) == 0 {
		t.Fatal("recon.All() = 0 specs, want the wired scanner set")
	}
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		tool, ok := recontools.Get(spec.ID)
		if !ok {
			t.Errorf("recontools.Get(%q) missing, want a typed tool", spec.ID)
			continue
		}
		name := tool.Name()
		if name != spec.ID {
			t.Errorf("typed tool name = %q, want %q", name, spec.ID)
		}
		if _, dup := seen[name]; dup {
			t.Errorf("duplicate typed tool name %q", name)
		}
		seen[name] = struct{}{}
	}
}

// TestRegistryAllIsSorted asserts All returns a deterministic, name-ordered set.
func TestRegistryAllIsSorted(t *testing.T) {
	tools := recontools.All()
	for i := 1; i < len(tools); i++ {
		if tools[i-1].Name() > tools[i].Name() {
			t.Fatalf("All()[%d] = %q before %q, want sorted order", i, tools[i-1].Name(), tools[i].Name())
		}
	}
}

// TestReconToolsHaveNoDuplicateNames asserts the MCP-facing slice carries each
// scanner exactly once and does not drop a wired typed tool.
func TestReconToolsHaveNoDuplicateNames(t *testing.T) {
	seen := make(map[string]struct{})
	for _, tool := range mcptools.ReconTools() {
		if _, dup := seen[tool.Name()]; dup {
			t.Errorf("ReconTools() duplicate name %q", tool.Name())
		}
		seen[tool.Name()] = struct{}{}
	}
	for _, spec := range recon.All() {
		if _, ok := seen[spec.ID]; !ok {
			t.Errorf("ReconTools() missing %q", spec.ID)
		}
	}
}
