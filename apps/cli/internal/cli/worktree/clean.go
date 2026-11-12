package worktree

import (
	"context"
	"errors"
	"fmt"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/apps/cli/internal/cli/forgeaccess"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func newCleanCommand(opts *cmdutil.Options) *cobra.Command {
	var dryRun bool
	var count, prepared int
	var connection forgeaccess.Options
	cmd := &cobra.Command{Use: "clean", Short: "Remove old worktrees and release old dependency environments", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := connection.Validate(opts); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
		defer cancel()
		policy, err := state.LoadWorktreePolicy(ctx, opts.Repository, opts.Home)
		if err != nil {
			return cmdutil.ExecutionFailure(err)
		}
		if cmd.Flags().Changed("max-count") {
			policy.MaxCount = count
		}
		if cmd.Flags().Changed("max-prepared") {
			policy.MaxPrepared = prepared
		}
		if err = policy.Validate(); err != nil {
			return err
		}
		home, err := state.ResolveHome(opts.Home)
		if err != nil {
			return cmdutil.ExecutionFailure(err)
		}
		request := state.WorktreeCleanOptions{Policy: &policy, DryRun: dryRun, Home: home}
		prContext, connectionErr := connection.WorktreePullRequests(ctx, opts.Repository, opts.Home)
		if connectionErr == nil {
			request.PullRequests = prContext
		}
		report, err := state.CleanWorktrees(ctx, opts.Repository, request)
		if err != nil {
			return cmdutil.ExecutionFailure(err)
		}
		if connectionErr != nil && !errors.Is(connectionErr, forgeaccess.ErrNoOrigin) {
			report.Errors = append(report.Errors, "PR status unavailable: "+connectionErr.Error())
		}
		if !opts.JSON {
			cmdutil.Record(cmd.OutOrStdout(), report)
		}
		if opts.JSON {
			if err = cmdutil.JSON(cmd.OutOrStdout(), report); err != nil {
				return cmdutil.ExecutionFailure(err)
			}
		} else {
			for _, action := range report.Actions {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s (%s)\n", action.Status, action.Action, action.Path, action.Reason)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Remaining: %d worktrees\n", report.RemainingCount)
			for _, issue := range report.Errors {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", issue)
			}
		}
		if len(report.Errors) > 0 {
			return cmdutil.ExecutionFailure(&cmdutil.ResultFailure{Message: "worktree maintenance incomplete"})
		}
		return nil
	}}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show decisions without deleting resources")
	cmd.Flags().IntVar(&count, "max-count", 10, "maximum linked worktrees (repository config by default)")
	cmd.Flags().IntVar(&prepared, "max-prepared", 3, "maximum prepared worktrees (repository config by default)")
	connection.Flags(cmd)
	return cmd
}
