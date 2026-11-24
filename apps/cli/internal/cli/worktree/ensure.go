package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/compforge/repokit/toolkit/go/worktree"
	"github.com/spf13/cobra"
)

func newEnsureCommand(opts *cmdutil.Options) *cobra.Command {
	var branch, base, remote string
	var prepare bool
	cmd := &cobra.Command{Use: "ensure <name>", Short: "Create or reuse a named worktree under the primary checkout", Long: `Create or reuse <primary-checkout>/.worktrees/<name>. Pass the required --repo even when invoking ensure from a linked checkout or symlink. The name must be a single directory name and is independent of --branch.

To limit disk use, each confirmed creation or reuse automatically applies this clone's retention policy after optional preparation. When counts exceed the configured limits, ensure removes eligible older worktrees (max_count, default 10) and releases older prepared environments (max_prepared, default 3). Both limits use worktree creation time, oldest first. Explicit clean commands can also reclaim worktrees for verified merged PRs.

Removal includes uncommitted, untracked and ignored files. Local branches and commits remain. The invoking and ensured checkouts and explicitly locked worktrees are protected. Maintenance warnings leave the confirmed checkout in place.`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flag("repo").Changed || opts.Repository == "" {
			return errors.New("--repo is required")
		}
		if opts.Timeout <= 0 || branch == "" || base == "" {
			return errors.New("--branch, --base and a positive --timeout are required")
		}
		home, err := state.ResolveHome(opts.Home)
		if err != nil {
			return cmdutil.ExecutionFailure(err)
		}
		current, err := os.Getwd()
		if err == nil {
			current, err = filepath.EvalSymlinks(current)
		}
		if err != nil {
			return cmdutil.ExecutionFailure(err)
		}
		report, err := worktree.Ensure(cmd.Context(), opts.Repository, args[0], worktree.EnsureOptions{EnsureWorktreeOptions: state.EnsureWorktreeOptions{Branch: branch, Base: base, Remote: remote, Home: home}, Prepare: prepare, Timeout: opts.Timeout, ProtectedPaths: []string{current}})
		if opts.JSON {
			if e := cmdutil.JSON(cmd.OutOrStdout(), report); e != nil {
				return e
			}
		} else {
			cmdutil.Record(cmd.OutOrStdout(), report)
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s branch=%s confirmed=%t\n", report.Action, report.Checkout, report.Branch, report.Confirmed)
			if report.Preparation != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Dependencies ready=%t; complete=%t\n", report.Ready, report.Preparation.Complete)
				for _, bin := range report.Preparation.Bins {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s %s\n", bin.Path, bin.Status, bin.Action, bin.Reason)
				}
			}
			if report.Maintenance != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Maintenance: %d actions; %d errors\n", len(report.Maintenance.Actions), len(report.Maintenance.Errors))
				for _, a := range report.Maintenance.Actions {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s (%s)\n", a.Path, a.Action, a.Status, a.Reason)
				}
			}
			for _, d := range report.Diagnostics {
				fmt.Fprintf(cmd.OutOrStdout(), "warning: %s: %s\n", d.Code, d.Message)
			}
			for _, p := range report.Phases {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %.3fs\n", p.Name, p.DurationSeconds)
			}
		}
		if report.Confirmed && report.Preparation != nil && !report.Ready {
			return &cmdutil.ResultFailure{Message: "checkout confirmed; dependency preparation incomplete"}
		}
		return cmdutil.ExecutionFailure(err)
	}}
	cmd.Flags().String("output", "", "save the complete result as JSON; stdout format is unchanged")
	cmd.Flags().BoolVar(&prepare, "prepare", false, "Prepare declared dependency Bins after confirming the checkout")
	cmd.Flags().StringVar(&branch, "branch", "", "Exact local branch to create or resume")
	cmd.Flags().StringVar(&base, "base", "", "Creation base: local revision, or branch on --remote")
	cmd.Flags().StringVar(&remote, "remote", "", "Configured remote to refresh before creating a new branch")
	return cmd
}
