package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

const (
	// mcpEntryName is the config key the web-mcp server is registered under.
	mcpEntryName = "web-mcp"
	// defaultMCPURL is the SSE endpoint printed by `serve mcp` on its default port.
	defaultMCPURL = "http://127.0.0.1:4099/sse"
	// tomlMCPTable is the TOML table Codex reads remote MCP servers from.
	tomlMCPTable = "mcp_servers." + mcpEntryName
)

// mcpFormat is the config syntax of one harness preset.
type mcpFormat int

const (
	formatJSON mcpFormat = iota
	formatTOML
)

// mcpHarness is one harness preset: where its MCP config lives, how the file is
// parsed, and how the web-mcp entry is shaped inside it.
type mcpHarness struct {
	Name      string
	Note      string
	Path      string
	Format    mcpFormat
	Key       string
	Transport string
}

// NewMcpCmd constructs the "mcp" command tree, which registers the web-mcp MCP
// server entry in a supported agent harness config.
func NewMcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Register the MCP server in an agent harness config",
		Long: `Writes the web-mcp MCP server entry into an agent harness config file, so
the harness connects to the live server started by 'browser serve mcp'.`,
	}
	cmd.AddCommand(newMcpAddCmd())
	return cmd
}

// newMcpAddCmd constructs "mcp add": it merges one server entry into one
// harness config, keeping every other key, and backs the file up first.
func newMcpAddCmd() *cobra.Command {
	var url, dir string
	var dryRun, force, remove bool

	cmd := &cobra.Command{
		Use:   "add [harness]",
		Short: "Add the web-mcp server entry to a harness config",
		Long: `Merges the web-mcp MCP server entry into one harness config file. Every
existing key at every level is kept; the file is copied to <file>.bak before
the first change. With no harness it prints the presets and writes nothing.

  claude    ~/.claude.json              mcpServers."web-mcp"  (type sse)
  opencode  ~/.config/opencode/opencode.json  mcp."web-mcp"  (type remote)
  codex     ~/.codex/config.toml        [mcp_servers.web-mcp]
  pi        ~/.pi/agent/mcp.json        mcpServers."web-mcp"

A harness whose config file does not exist is not touched: the command prints
the path and the entry to paste, then exits 0. --dir overrides the file path,
--url overrides the endpoint, --dry-run prints the merged file and writes
nothing, --remove deletes only the entry, and --force replaces an existing
entry instead of refusing.`,
		Example: `  browser mcp add                            # list the presets, write nothing
  browser mcp add opencode --dry-run         # show the merged file
  browser mcp add claude                     # merge into ~/.claude.json
  browser mcp add codex --url http://127.0.0.1:4089/sse
  browser mcp add pi --remove                # delete only the entry`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 && dir == "" {
				return printMcpTargets(out)
			}
			if len(args) == 0 {
				return fmt.Errorf("browser mcp add: --dir needs a harness argument, e.g. browser mcp add opencode --dir ./opencode.json")
			}

			h, err := mcpHarnessFor(args[0])
			if err != nil {
				return fmt.Errorf("browser mcp add: %w", err)
			}
			path := dir
			if path == "" {
				if path, err = expandHome(h.Path); err != nil {
					return fmt.Errorf("browser mcp add: %w", err)
				}
			}

			data, err := os.ReadFile(path)
			if errors.Is(err, fs.ErrNotExist) {
				if remove {
					fmt.Fprintf(out, "%s has no config; nothing to remove\n", path)
					return nil
				}
				return printMcpSnippet(out, h, path, url)
			}
			if err != nil {
				return fmt.Errorf("browser mcp add: reading %s: %w", path, err)
			}

			merged, changed, err := h.merge(data, path, url, remove, force)
			if err != nil {
				return fmt.Errorf("browser mcp add: %w", err)
			}
			if !changed {
				fmt.Fprintf(out, "%s has no %q entry under %s\n", path, mcpEntryName, h.container())
				return nil
			}
			if dryRun {
				fmt.Fprintf(out, "%s (merged, dry run)\n%s", path, merged)
				fmt.Fprintln(out, "dry run, wrote nothing")
				return nil
			}
			if err := writeMCPConfig(path, data, merged); err != nil {
				return fmt.Errorf("browser mcp add: %w", err)
			}

			change := fmt.Sprintf("added %s → %s", h.container(), url)
			if remove {
				change = "removed " + h.container()
			}
			fmt.Fprintf(out, "wrote  %s\nentry  %s\nbackup %s.bak\n", path, change, path)
			return nil
		},
	}

	cmd.Flags().StringVar(&url, "url", defaultMCPURL, "MCP endpoint the harness connects to")
	cmd.Flags().StringVar(&dir, "dir", "", "config file to write, overriding the harness preset")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the merged file and write nothing")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing web-mcp entry")
	cmd.Flags().BoolVar(&remove, "remove", false, "delete the web-mcp entry and keep the rest of the file")
	return cmd
}

// mcpHarnesses returns the supported harness presets in display order.
func mcpHarnesses() []mcpHarness {
	return []mcpHarness{
		{
			Name:      "claude",
			Note:      "Claude Code (user scope)",
			Path:      "~/.claude.json",
			Format:    formatJSON,
			Key:       "mcpServers",
			Transport: "sse",
		},
		{
			Name:      "opencode",
			Note:      "OpenCode",
			Path:      "~/.config/opencode/opencode.json",
			Format:    formatJSON,
			Key:       "mcp",
			Transport: "remote",
		},
		{
			Name:   "codex",
			Note:   "OpenAI Codex",
			Path:   "~/.codex/config.toml",
			Format: formatTOML,
		},
		{
			Name:   "pi",
			Note:   "pi",
			Path:   piConfigPath(),
			Format: formatJSON,
			Key:    "mcpServers",
		},
	}
}

// piConfigPath returns pi's user-level MCP config, honouring the documented
// PI_CODING_AGENT_DIR override of its agent directory.
func piConfigPath() string {
	if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
		return filepath.Join(dir, "mcp.json")
	}
	return "~/.pi/agent/mcp.json"
}

// mcpHarnessFor resolves the harness preset named on the command line.
func mcpHarnessFor(name string) (mcpHarness, error) {
	for _, h := range mcpHarnesses() {
		if h.Name == name {
			return h, nil
		}
	}
	names := make([]string, 0, len(mcpHarnesses()))
	for _, h := range mcpHarnesses() {
		names = append(names, h.Name)
	}
	return mcpHarness{}, fmt.Errorf("unknown harness %q — use one of %s", name, strings.Join(names, ", "))
}

// container names the config location the entry lives in, for messages.
func (h mcpHarness) container() string {
	if h.Format == formatTOML {
		return "[" + tomlMCPTable + "]"
	}
	return h.Key + "." + strconv.Quote(mcpEntryName)
}

// entry builds the harness-shaped server entry.
func (h mcpHarness) entry(url string) map[string]any {
	entry := map[string]any{"url": url}
	switch h.Transport {
	case "remote":
		entry["type"] = "remote"
		entry["enabled"] = true
	case "sse":
		entry["type"] = "sse"
	}
	return entry
}

// merge applies the add or remove to a config file body, returning the new
// bytes and whether the file changed.
func (h mcpHarness) merge(data []byte, path, url string, remove, force bool) ([]byte, bool, error) {
	if h.Format == formatTOML {
		return h.mergeTOML(data, path, url, remove, force)
	}
	return h.mergeJSON(data, path, url, remove, force)
}

// mergeJSON merges the entry into the harness's JSON container. The decode uses
// json.Number so unrelated numeric values are re-encoded exactly; the rewrite
// normalises indentation, which is the only formatting the format requires.
func (h mcpHarness) mergeJSON(data []byte, path, url string, remove, force bool) ([]byte, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var top map[string]any
	if err := dec.Decode(&top); err != nil {
		return nil, false, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("parsing %s: unexpected data after the top-level object", path)
	}
	if top == nil {
		top = map[string]any{}
	}

	servers := map[string]any{}
	if raw, ok := top[h.Key]; ok && raw != nil {
		existing, ok := raw.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("parsing %s: %q is not an object", path, h.Key)
		}
		servers = existing
	}

	if remove {
		if _, ok := servers[mcpEntryName]; !ok {
			return nil, false, nil
		}
		delete(servers, mcpEntryName)
	} else {
		if _, ok := servers[mcpEntryName]; ok && !force {
			return nil, false, fmt.Errorf("%s already has a %q entry under %q — pass --force to replace it", path, mcpEntryName, h.Key)
		}
		servers[mcpEntryName] = h.entry(url)
	}

	top[h.Key] = servers
	merged, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("encoding %s: %w", path, err)
	}
	return append(merged, '\n'), true, nil
}

// mergeTOML inserts or removes the [mcp_servers.web-mcp] table by editing the
// file's lines, so unrelated tables keep their exact text and order. Validation
// is a structural sanity check, not a full TOML parser.
func (h mcpHarness) mergeTOML(data []byte, path, url string, remove, force bool) ([]byte, bool, error) {
	if err := validateTOML(data, path); err != nil {
		return nil, false, err
	}
	text := string(data)
	start, end := tomlSection(text)

	if remove {
		if start < 0 {
			return nil, false, nil
		}
		return []byte(withoutTOMLSection(text, start, end)), true, nil
	}

	if start >= 0 {
		if !force {
			return nil, false, fmt.Errorf("%s already has a [%s] table — pass --force to replace it", path, tomlMCPTable)
		}
		text = withoutTOMLSection(text, start, end)
	}
	if text != "" {
		text = strings.TrimRight(text, "\n") + "\n\n"
	}
	text += "[" + tomlMCPTable + "]\nurl = " + strconv.Quote(url) + "\n"
	return []byte(text), true, nil
}

// withoutTOMLSection drops the [mcp_servers.web-mcp] table lines and keeps the
// rest of the file's text untouched.
func withoutTOMLSection(text string, start, end int) string {
	lines := strings.Split(text, "\n")
	kept := append(append([]string{}, lines[:start]...), lines[end:]...)
	out := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if out != "" {
		out += "\n"
	}
	return out
}

// tomlSection locates the [mcp_servers.web-mcp] table, returning the line index
// of its header and the line index where the next table starts (or EOF, or -1
// when the table is absent).
func tomlSection(text string) (start, end int) {
	lines := strings.Split(text, "\n")
	start = -1
	for i, line := range lines {
		key, ok := tomlHeaderKey(line)
		if ok && key == tomlMCPTable {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	for i := start + 1; i < len(lines); i++ {
		if _, ok := tomlHeaderKey(lines[i]); ok {
			return start, i
		}
	}
	return start, len(lines)
}

// tomlHeaderKey parses a [table] header line into its key, ignoring quotes.
func tomlHeaderKey(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") || strings.HasPrefix(t, "[[") || !strings.HasSuffix(t, "]") {
		return "", false
	}
	key := strings.TrimSpace(t[1 : len(t)-1])
	key = strings.ReplaceAll(key, `"`, "")
	key = strings.ReplaceAll(key, `'`, "")
	return strings.TrimSpace(key), true
}

// validateTOML rejects a config whose non-blank lines are neither table headers
// nor key = value pairs, naming the offending line.
func validateTOML(data []byte, path string) error {
	for i, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "[") {
			if !strings.HasSuffix(t, "]") {
				return fmt.Errorf("parsing %s: line %d: unterminated table header", path, i+1)
			}
			continue
		}
		if !strings.Contains(t, "=") {
			return fmt.Errorf("parsing %s: line %d: expected key = value", path, i+1)
		}
	}
	return nil
}

// writeMCPConfig backs the original file up, then writes the merged body with
// the original permissions.
func writeMCPConfig(path string, original, merged []byte) error {
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(path+".bak", original, mode); err != nil {
		return fmt.Errorf("writing backup %s: %w", path+".bak", err)
	}
	if err := os.WriteFile(path, merged, mode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// printMcpTargets renders the harness preset table and the usage line.
func printMcpTargets(out io.Writer) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCONFIG\tENTRY\tNOTE")
	for _, h := range mcpHarnesses() {
		path, err := expandHome(h.Path)
		if err != nil {
			path = h.Path
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.Name, path, h.container(), h.Note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "browser mcp add <harness>")
	return nil
}

// printMcpSnippet prints a harness config path plus the exact entry to paste,
// for a harness that has no config file on this host. It never fails.
func printMcpSnippet(out io.Writer, h mcpHarness, path, url string) error {
	fmt.Fprintf(out, "%s\n", path)
	if h.Format == formatTOML {
		fmt.Fprintf(out, "[%s]\nurl = %s\n", tomlMCPTable, strconv.Quote(url))
	} else {
		snippet, err := json.MarshalIndent(map[string]any{
			h.Key: map[string]any{mcpEntryName: h.entry(url)},
		}, "", "  ")
		if err != nil {
			return fmt.Errorf("rendering the %s snippet: %w", h.Name, err)
		}
		fmt.Fprintf(out, "%s\n", snippet)
	}
	if h.Name == "pi" {
		if _, err := exec.LookPath("pi"); err != nil {
			fmt.Fprintln(out, "pi not found on PATH; the path above is its documented user config")
		}
	}
	return nil
}

// expandHome replaces a leading "~" with the current user's home directory.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory for %q: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}
