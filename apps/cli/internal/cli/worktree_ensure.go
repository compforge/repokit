package cli

import (
	"context"
	"errors"
	"fmt"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func newWorktreeEnsureCommand(opts *cmdutil.Options) *cobra.Command {
	var branch, base, remote string
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
		report, err := state.EnsureWorktree(ctx, opts.Repository, args[0], state.EnsureWorktreeOptions{Branch: branch, Base: base, Remote: remote, Home: home})
		if opts.JSON {
			if e := cmdutil.JSON(cmd.OutOrStdout(), report); e != nil {
				return e
			}
		} else {
			cmdutil.Record(cmd.OutOrStdout(), report)
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s branch=%s confirmed=%t\n", report.Action, report.Checkout, report.Branch, report.Confirmed)
			for _, d := range report.Diagnostics {
				fmt.Fprintf(cmd.OutOrStdout(), "warning: %s: %s\n", d.Code, d.Message)
			}
			for _, p := range report.Phases {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %.3fs\n", p.Name, p.DurationSeconds)
			}
		}
		return executionFailure(err)
	}}
	cmd.Flags().StringVar(&branch, "branch", "", "Exact local branch to create or resume")
	cmd.Flags().StringVar(&base, "base", "", "Creation base: local revision, or branch on --remote")
	cmd.Flags().StringVar(&remote, "remote", "", "Configured remote to refresh before creating a new branch")
	return cmd
}
