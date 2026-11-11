package cli

import (
	"context"
	"errors"
	"fmt"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/apps/cli/internal/cli/forgeaccess"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func newWorktreeCommand(opts *cmdutil.Options) *cobra.Command {
	root := &cobra.Command{Use: "worktree", Short: "Maintain linked worktrees"}
	root.AddCommand(newWorktreeEnsureCommand(opts))
	var dryRun bool
	var count, prepared int
	var connection forgeaccess.Options
	clean := &cobra.Command{Use: "clean", Short: "Remove old worktrees and release old dependency environments", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := connection.Validate(opts); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
		defer cancel()
		policy, err := state.LoadWorktreePolicy(ctx, opts.Repository)
		if err != nil {
			return executionFailure(err)
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
		home, err := repositoryHome(opts.Home)
		if err != nil {
			return executionFailure(err)
		}
		request := state.WorktreeCleanOptions{Policy: &policy, DryRun: dryRun, Home: home}
		prContext, connectionErr := connection.WorktreePullRequests(ctx, opts.Repository, opts.Home)
		if connectionErr == nil {
			request.PullRequests = prContext
		}
		report, err := state.CleanWorktrees(ctx, opts.Repository, request)
		if err != nil {
			return executionFailure(err)
		}
		if connectionErr != nil && !errors.Is(connectionErr, forgeaccess.ErrNoOrigin) {
			report.Errors = append(report.Errors, "PR status unavailable: "+connectionErr.Error())
		}
		if !opts.JSON {
			cmdutil.Record(cmd.OutOrStdout(), report)
		}
		if opts.JSON {
			if err = cmdutil.JSON(cmd.OutOrStdout(), report); err != nil {
				return executionFailure(err)
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
			return executionFailure(&resultFailure{"worktree maintenance incomplete"})
		}
		return nil
	}}
	clean.Flags().BoolVar(&dryRun, "dry-run", false, "show decisions without deleting resources")
	clean.Flags().IntVar(&count, "max-count", 10, "maximum linked worktrees (repository config by default)")
	clean.Flags().IntVar(&prepared, "max-prepared", 3, "maximum prepared worktrees (repository config by default)")
	connection.Flags(clean)
	var reconcile bool
	observe := &cobra.Command{Use: "observe", Short: "Record local branches and checkout locations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if opts.Timeout <= 0 {
			return errors.New("--timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
		defer cancel()
		home, err := repositoryHome(opts.Home)
		if err != nil {
			return executionFailure(err)
		}
		directory, err := state.Observe(ctx, opts.Repository, home, reconcile)
		if err != nil {
			return executionFailure(err)
		}
		if !opts.JSON {
			cmdutil.Record(cmd.OutOrStdout(), map[string]string{"directory": directory})
		}
		if opts.JSON {
			return cmdutil.JSON(cmd.OutOrStdout(), map[string]string{"directory": directory})
		}
		fmt.Fprintln(cmd.OutOrStdout(), directory)
		return nil
	}}
	observe.Flags().BoolVar(&reconcile, "reconcile", false, "retire absent checkout observations without deleting source directories")
	root.AddCommand(observe)
	root.AddCommand(clean)
	return root
}

func repositoryHome(explicit string) (string, error) { return state.ResolveHome(explicit) }
