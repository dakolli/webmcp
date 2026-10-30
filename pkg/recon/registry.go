package recon

import (
	"sort"
	"sync"
)

// registry holds the scanner specs that each tool file registers from init().
var (
	registryMu sync.RWMutex
	registry   = make(map[string]Spec)
)

// Register adds one scanner spec under its ID. Each tool file calls it from its
// own init(), so no central table exists and a new file needs no shared edit.
func Register(spec Spec) {
	registryMu.Lock()
	registry[spec.ID] = spec
	registryMu.Unlock()
}

// Get returns the spec registered under id.
func Get(id string) (Spec, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	spec, ok := registry[id]
	return spec, ok
}

// All returns every registered spec sorted by ID, so listings and tool output
// stay deterministic. An empty registry returns an empty slice, never an error.
func All() []Spec {
	registryMu.RLock()
	specs := make([]Spec, 0, len(registry))
	for _, spec := range registry {
		specs = append(specs, spec)
	}
	registryMu.RUnlock()
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs
}
