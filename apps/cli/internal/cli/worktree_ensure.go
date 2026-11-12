package cli

import (
	"context"
	"errors"
	"fmt"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/compforge/repokit/toolkit/go/worktree"
	"github.com/spf13/cobra"
)

func newWorktreeEnsureCommand(opts *cmdutil.Options) *cobra.Command {
	var branch, base, remote string
	var prepare bool
	cmd := &cobra.Command{Use: "ensure <path>", Short: "Create or reuse an exact checkout and branch without resetting work", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if opts.Timeout <= 0 || branch == "" || base == "" {
			return errors.New("--branch, --base and a positive --timeout are required")
		}
		home, err := repositoryHome(opts.Home)
		if err != nil {
			return executionFailure(err)
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
			return &resultFailure{Message: "checkout confirmed; dependency preparation incomplete"}
		}
		return executionFailure(err)
	}}
	cmd.Flags().BoolVar(&prepare, "prepare", false, "Prepare declared dependency Bins after confirming the checkout")
	cmd.Flags().StringVar(&branch, "branch", "", "Exact local branch to create or resume")
	cmd.Flags().StringVar(&base, "base", "", "Creation base: local revision, or branch on --remote")
	cmd.Flags().StringVar(&remote, "remote", "", "Configured remote to refresh before creating a new branch")
	return cmd
}
