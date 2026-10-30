package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
	"github.com/dakolli/webmcp/pkg/recon"
	recontools "github.com/dakolli/webmcp/pkg/recon/tools"
)

// NewReconCmd constructs the "recon" command tree: list the pinned scanners and
// run one. Both run in this process, never through the serve daemon.
func NewReconCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recon",
		Short: "List and run pinned external recon scanners",
		Long: `List and run the pinned external recon scanners (subfinder, dnsx, httpx,
naabu, katana, ffuf, jsluice, nuclei) from the private recon GOBIN.

Recon runs IN THIS PROCESS, never through the serve daemon: a scan can take
minutes, while the IPC socket caps every request at 30 seconds. Running a
scanner does not require, start, or touch a browser.

Subcommands:
  list  List every registered scanner with its binary, pin and install state
  run   Run one scanner with typed flags or a raw JSON argument object`,
		Example: `  browser recon list
  browser recon run recon_subdomains --authorized --target example.com
  browser recon run recon_dns --json '{"authorized":true,"targets":["example.com"]}'
  browser recon run recon_http --authorized --target https://example.com --json '{"title":true}'`,
		PersistentPreRunE: silenceRuntimeUsage,
	}
	cmd.AddCommand(newReconListCmd(), newReconRunCmd())
	return cmd
}

// newReconListCmd builds `recon list`: one row per registered scanner with the
// binary name, the pinned version and whether it is installed. An empty registry
// prints only the header and exits 0.
func newReconListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered recon scanners",
		Long: `Lists every registered scanner as ID, BINARY, PIN and STATE rows. STATE is
"installed" when the pinned binary already exists under <config dir>/recon/bin,
"missing" when a run would bootstrap it, and "unknown" when the bin dir cannot be
resolved. With no scanner registered the table holds only its header and the
command still exits 0.`,
		Example: `  browser recon list`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, dirErr := recon.BinDir()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tBINARY\tPIN\tSTATE")
			for _, spec := range recon.All() {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", spec.ID, spec.BinName, spec.Pin, reconState(dir, dirErr, spec))
			}
			return w.Flush()
		},
	}
}

// reconState reports whether a scanner's pinned binary is installed.
func reconState(dir string, dirErr error, spec recon.Spec) string {
	if dirErr != nil {
		return "unknown"
	}
	info, err := os.Stat(filepath.Join(dir, spec.BinName))
	if err != nil || info.IsDir() {
		return "missing"
	}
	return "installed"
}

// reconRunFlags holds the friendly flags shared by every typed recon tool.
type reconRunFlags struct {
	rawJSON    string
	targets    []string
	scope      []string
	authorized bool
	outputFile string
	maxEntries int
	timeoutMS  int
	extraArgs  []string
}

// newReconRunCmd builds `recon run <id>`: it turns the friendly flags into the
// typed tool's JSON argument object and calls the same Execute path MCP uses.
func newReconRunCmd() *cobra.Command {
	var f reconRunFlags
	cmd := &cobra.Command{
		Use:   "run <id> [-- extra args...]",
		Short: "Run one recon scanner in process",
		Long: `Runs one registered typed recon tool IN THIS PROCESS, never through the serve
daemon: the IPC socket caps every request at 30 seconds and a recon run can take
minutes.

The raw --json object is handed to the tool's own argument decoder unchanged,
with only the flags you set overlaid, so every typed field is reachable and
validation stays in one place. An argument the tool's schema does not declare
fails there with the same sentence MCP returns, so a typo never passes silently.

Consent, scope and caps are the tool's own rules:
  --authorized        set true only for targets you are permitted to scan;
                      without it the call fails with the tool's own error
  --target <t>        target (repeatable); mapped to the tool's targets, target,
                      url or urls field
  --scope <host>      host allowlist (repeatable; a bare host covers subdomains,
                      "*." is accepted, "*" allows all)

Everything after "--" is appended to the tool's extra_args, so any scanner flag
is reachable. The result is the tool's data payload printed as indented JSON.`,
		Example: `  browser recon run recon_subdomains --authorized --target example.com
  browser recon run recon_dns --json '{"authorized":true,"targets":["example.com"],"record_types":["a","mx"]}'
  browser recon run recon_http --authorized --target https://example.com --json '{"title":true,"tech":true}'
  browser recon run recon_urls --authorized --target https://example.com -- -d 3`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			tool, ok := recontools.Get(id)
			if !ok {
				return fmt.Errorf("recon: no typed tool %q — %s", id, knownReconIDs())
			}
			f.extraArgs = append(f.extraArgs, dashedArgs(cmd, args)...)

			raw, err := buildReconArgs(tool, f, cmd)
			if err != nil {
				return err
			}
			if err := rejectUndeclaredArgs(tool, raw); err != nil {
				return err
			}
			out, err := tool.Execute(cmdCtx(cmd), raw)
			if err != nil {
				return fmt.Errorf("recon: %s: %w", tool.Name(), err)
			}
			return printReconResult(cmd.OutOrStdout(), tool.Name(), out)
		},
	}
	cmd.Flags().StringVar(&f.rawJSON, "json", "", "raw JSON argument object handed to the tool (every typed field is reachable)")
	cmd.Flags().StringArrayVar(&f.targets, "target", nil, "target to scan (repeatable): a URL, a port number, or a host")
	cmd.Flags().StringArrayVar(&f.scope, "scope", nil, "host allowlist entry (repeatable); a bare host covers its subdomains, \"*.\" is accepted")
	cmd.Flags().BoolVar(&f.authorized, "authorized", false, "confirm you are permitted to scan the targets (the tool rejects the call without it)")
	cmd.Flags().StringVar(&f.outputFile, "output-file", "", "absolute path for the raw report (mode 0600)")
	cmd.Flags().StringArrayVar(&f.extraArgs, "extra-arg", nil, "argument appended to extra_args (repeatable; arguments after -- work too)")
	cmd.Flags().IntVar(&f.maxEntries, "max-entries", 0, "maximum decoded entries returned")
	cmd.Flags().IntVar(&f.timeoutMS, "timeout-ms", 0, "run budget in milliseconds")
	return cmd
}

// buildReconArgs merges the raw --json object with the friendly flags that were
// explicitly set and returns the tool's argument JSON.
func buildReconArgs(
	tool toolenv.Tool,
	f reconRunFlags,
	cmd *cobra.Command,
) ([]byte, error) {
	obj := make(map[string]any)
	if strings.TrimSpace(f.rawJSON) != "" {
		if err := json.Unmarshal([]byte(f.rawJSON), &obj); err != nil {
			return nil, fmt.Errorf("recon: --json must be one JSON object: %w", err)
		}
		if obj == nil {
			obj = make(map[string]any)
		}
	}

	flags := cmd.Flags()
	if flags.Changed("target") && len(f.targets) > 0 {
		if err := assignReconTargets(tool, obj, f.targets); err != nil {
			return nil, err
		}
	}
	if flags.Changed("scope") && len(f.scope) > 0 {
		obj["scope"] = f.scope
	}
	if flags.Changed("authorized") {
		obj["authorized"] = f.authorized
	}
	if flags.Changed("output-file") && strings.TrimSpace(f.outputFile) != "" {
		obj["output_file"] = strings.TrimSpace(f.outputFile)
	}
	if flags.Changed("max-entries") && f.maxEntries > 0 {
		obj["max_entries"] = f.maxEntries
	}
	if flags.Changed("timeout-ms") && f.timeoutMS > 0 {
		obj["timeout_ms"] = f.timeoutMS
	}
	if len(f.extraArgs) > 0 {
		obj["extra_args"] = append(reconExtraArgs(obj["extra_args"]), f.extraArgs...)
	}

	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("recon: encode arguments: %w", err)
	}
	return raw, nil
}

// assignReconTargets stores targets under the field the tool's own schema
// exposes, so one --target flag serves every typed tool.
func assignReconTargets(
	tool toolenv.Tool,
	obj map[string]any,
	targets []string,
) error {
	key := reconTargetKey(tool)
	if key == "targets" || key == "urls" {
		obj[key] = targets
		return nil
	}
	if len(targets) > 1 {
		return fmt.Errorf(
			"recon: %s: --target maps to the single %q field — pass one target or use --json",
			tool.Name(),
			key,
		)
	}
	obj[key] = targets[0]
	return nil
}

// reconTargetKey picks the target field from the tool's schema, preferring a
// plural list over a singular one.
func reconTargetKey(tool toolenv.Tool) string {
	props := tool.InputSchema().Properties
	for _, key := range []string{"targets", "target", "url", "urls"} {
		if _, ok := props[key]; ok {
			return key
		}
	}
	return "targets"
}

// reconExtraArgs normalizes a decoded extra_args value into a string slice.
func reconExtraArgs(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// dashedArgs returns the arguments after "--" so they reach the tool's
// extra_args verbatim.
func dashedArgs(cmd *cobra.Command, args []string) []string {
	cut := cmd.ArgsLenAtDash()
	if cut <= 0 || cut >= len(args) {
		return nil
	}
	return args[cut:]
}

// rejectUndeclaredArgs refuses the first supplied argument the tool's schema
// does not declare and names the sorted valid ones. It is the gate the MCP
// registry applies before dispatch, so the CLI cannot silently drop a typo MCP
// would refuse.
func rejectUndeclaredArgs(tool toolenv.Tool, raw json.RawMessage) error {
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal(raw, &supplied); err != nil || len(supplied) == 0 {
		return nil
	}

	props := tool.InputSchema().Properties
	valid := make([]string, 0, len(props))
	for key := range props {
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
			return fmt.Errorf(
				"%s: unknown argument %q — %s takes no arguments",
				tool.Name(),
				key,
				tool.Name(),
			)
		}
		return fmt.Errorf(
			"%s: unknown argument %q — valid arguments are %s",
			tool.Name(),
			key,
			strings.Join(valid, ", "),
		)
	}
	return nil
}

// printReconResult prints the tool's success payload and turns a failure
// envelope into an error carrying the tool's own message.
func printReconResult(
	w io.Writer,
	tool string,
	out string,
) error {
	var env toolenv.ResultEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return fmt.Errorf("recon: decode result: %w", err)
	}
	if !env.OK {
		if env.Error == nil {
			return errors.New("recon: tool returned no result")
		}
		msg := toolFailure(tool, env.Error.Message)
		if env.Error.Hint != "" {
			return fmt.Errorf("%s\nhint: %s", msg, env.Error.Hint)
		}
		return errors.New(msg)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, env.Data, "", "  "); err != nil {
		return fmt.Errorf("recon: format result: %w", err)
	}
	fmt.Fprintln(w, buf.String())
	return nil
}

// toolFailure retargets the recon guard's generic prefix onto the tool that made
// the call, so a scope refusal names the caller a grep looks for while the
// message body and hint stay untouched.
func toolFailure(tool, message string) string {
	const (
		guardPrefix = "recon: "
		guardBody   = "invalid arguments: "
	)
	if strings.HasPrefix(message, guardPrefix+guardBody) {
		return tool + ": " + strings.TrimPrefix(message, guardPrefix)
	}
	return message
}

// knownReconIDs renders the wired typed tool names for a lookup failure.
func knownReconIDs() string {
	tools := recontools.All()
	if len(tools) == 0 {
		return "no recon tools are registered"
	}
	ids := make([]string, 0, len(tools))
	for _, tool := range tools {
		ids = append(ids, tool.Name())
	}
	return "known ids: " + strings.Join(ids, ", ")
}
