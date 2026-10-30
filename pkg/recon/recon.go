// Package recon runs pinned external recon scanners as child processes. It owns
// binary bootstrap, target guards, JSONL decoding and the per-scanner registry.
// It depends on pkg/config only; it never imports pkg/mcp or rod.
package recon

// Spec describes one external scanner: the Go module to install, the pinned
// version, the binary name under the private GOBIN, the flags that make it emit
// JSON, and the flags that reveal its identity.
type Spec struct {
	// ID is the registry key and the MCP tool name, e.g. "recon_subdomains".
	ID string
	// Module is the Go module path passed to go install, e.g.
	// "github.com/projectdiscovery/subfinder/v2/cmd/subfinder".
	Module string
	// Pin is the module version, e.g. "v2.6.7".
	Pin string
	// BinName is the installed file name under BinDir, e.g. "subfinder".
	BinName string
	// VersionArgs are the flags that make the scanner print its identity.
	VersionArgs []string
	// JSONArgs are the flags that make the scanner emit JSONL, e.g. ["-json"].
	JSONArgs []string
	// CGO installs this module with CGO_ENABLED=1 so a cgo-only dependency
	// compiles, as jsluice's Tree-sitter does. The default false keeps the fast
	// static build that naabu, subfinder, dnsx, httpx, katana, ffuf and nuclei want.
	CGO bool
	// RequireOutput makes an exit-0 run with empty stdout a failure rather than
	// an empty result. httpx sets it because a reachable host that produced no
	// JSON means the argv never reached the scanner. Scanners whose empty answer
	// is real (nuclei, subfinder, dnsx, ffuf, katana, naabu) leave it false.
	RequireOutput bool
	// Doc is the scanner-specific description shown to an LLM by its MCP tool.
	Doc string
}

// Result is the bounded outcome of one scanner run. Entries are the decoded
// JSON objects, Count is how many were returned, Truncated is 1 when the entry
// cap cut the run, Dropped is how many entries the cap omitted, and OutputFile
// is the absolute raw report path when the caller asked for one.
type Result struct {
	Raw        []byte           `json:"-"`
	Entries    []map[string]any `json:"entries"`
	Count      int              `json:"count"`
	Truncated  int              `json:"truncated"`
	Dropped    int              `json:"dropped"`
	OutputFile string           `json:"output_file,omitempty"`
}
