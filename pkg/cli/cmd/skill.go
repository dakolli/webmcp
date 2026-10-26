package cmd

import (
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"webmcp/pkg/skill"
)

// NewSkillCmd constructs the "skill" command: it installs the embedded web-mcp
// agent skill into a harness preset, the current project, or an explicit path.
func NewSkillCmd() *cobra.Command {
	var force, dryRun bool
	var dir string

	cmd := &cobra.Command{
		Use:   "skill [harness|path|.]",
		Short: "Install the web-mcp agent skill into a harness",
		Long: `Installs the agent skill carried inside this binary — SKILL.md plus the
references/tools and references/examples trees — into a harness config
directory. With no argument it prints the supported presets and writes nothing.

  claude    ~/.claude/skills/web-mcp
  opencode  ~/.config/opencode/skills/web-mcp
  codex     ~/.codex/skills/web-mcp
  pi        ~/.pi/skills/web-mcp

Pass '.' to install into the current project's .agents/skills/web-mcp (the
project must already have .agents/skills), or a path with a separator, or an
existing directory, to install there. --dir overrides any preset.

An existing non-empty destination is refused unless --force is passed; --force
overwrites skill files but never deletes unrelated files. --dry-run prints the
file list and writes nothing. AGENTS.md is never installed.`,
		Example: `  browser skill                      # list presets and resolved paths, write nothing
  browser skill claude               # install into ~/.claude/skills/web-mcp
  browser skill opencode --dry-run   # show the files without writing
  browser skill .                    # install into ./.agents/skills/web-mcp
  browser skill ./tmp/skill --force  # install to a path and overwrite skill files`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			if dir == "" && len(args) == 0 {
				return printSkillTargets(out)
			}

			dst := dir
			if dst == "" {
				resolved, err := skill.Resolve(args[0])
				if err != nil {
					return fmt.Errorf("browser skill: %w", err)
				}
				dst = resolved
			}

			res, err := skill.Install(dst, force, dryRun)
			if err != nil {
				return fmt.Errorf("browser skill: %w", err)
			}

			if dryRun {
				files, err := skill.List()
				if err != nil {
					return fmt.Errorf("browser skill: %w", err)
				}
				for _, rel := range files {
					fmt.Fprintln(out, filepath.Join(res.Dir, filepath.FromSlash(rel)))
				}
				fmt.Fprintln(out, "dry run, wrote nothing")
				return nil
			}

			fmt.Fprintf(out, "installed %d files → %s\n", res.Files, res.Dir)
			fmt.Fprintln(out, "start a new agent session to load it")
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite skill files in a non-empty destination")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the file list without writing anything")
	cmd.Flags().StringVar(&dir, "dir", "", "install directory, overriding any preset")
	return cmd
}

// printSkillTargets renders the preset table and the usage line.
func printSkillTargets(out io.Writer) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPATH\tNOTE")
	for _, t := range skill.Targets() {
		dir, err := skill.Resolve(t.Name)
		if err != nil {
			dir = t.Dir
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, dir, t.Note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "browser skill <name|path|.>")
	return nil
}
