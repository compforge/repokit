package worktree

import (
	"context"
	"errors"
	"fmt"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/compforge/repokit/toolkit/go/worktree"
	"github.com/spf13/cobra"
)

func newEnsureCommand(opts *cmdutil.Options) *cobra.Command {
	var branch, base, remote string
	var prepare bool
	cmd := &cobra.Command{Use: "ensure <name>", Short: "Create or reuse a named worktree under the primary checkout", Long: "Create or reuse <primary-checkout>/.worktrees/<name>. --repo is required and resolves the owning repository even from a linked checkout or symlink. The name is a single directory name, independent of --branch; paths are not accepted.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
		defer cancel()
		report, err := worktree.Ensure(ctx, opts.Repository, args[0], worktree.EnsureOptions{EnsureWorktreeOptions: state.EnsureWorktreeOptions{Branch: branch, Base: base, Remote: remote, Home: home}, Prepare: prepare})
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
	cmd.Flags().BoolVar(&prepare, "prepare", false, "Prepare declared dependency Bins after confirming the checkout")
	cmd.Flags().StringVar(&branch, "branch", "", "Exact local branch to create or resume")
	cmd.Flags().StringVar(&base, "base", "", "Creation base: local revision, or branch on --remote")
	cmd.Flags().StringVar(&remote, "remote", "", "Configured remote to refresh before creating a new branch")
	return cmd
}
