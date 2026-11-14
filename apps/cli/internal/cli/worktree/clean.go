package worktree

import (
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
	cmd := &cobra.Command{Use: "clean", Short: "Remove old worktrees and release old dependency environments", Long: "Remove merged or excess worktrees with all local contents, including uncommitted and ignored files. Branch refs remain; primary/current checkouts and Git locks remain protected. Release managed environments from surviving worktrees according to the prepared limit.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := connection.Validate(opts); err != nil {
			return err
		}
		ctx := cmd.Context()
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
		request := state.WorktreeCleanOptions{Timeout: opts.Timeout, Policy: &policy, DryRun: dryRun, Home: home}
		prContext, connectionErr := connection.WorktreePullRequests(ctx, opts.Repository, opts.Home)
		if connectionErr == nil {
			request.PullRequests = prContext
		}
		report, err := state.CleanWorktrees(ctx, opts.Repository, request)
		if connectionErr != nil && !errors.Is(connectionErr, forgeaccess.ErrNoOrigin) {
			report.Errors = append(report.Errors, "PR status unavailable: "+connectionErr.Error())
		}
		if !opts.JSON {
			cmdutil.Record(cmd.OutOrStdout(), report)
		}
		if opts.JSON {
			if outputErr := cmdutil.JSON(cmd.OutOrStdout(), report); outputErr != nil {
				return cmdutil.ExecutionFailure(outputErr)
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
		if err != nil {
			return cmdutil.ExecutionFailure(err)
		}
		if len(report.Errors) > 0 {
			return cmdutil.ExecutionFailure(&cmdutil.ResultFailure{Message: "worktree maintenance incomplete"})
		}
		return nil
	}}
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", opts.Timeout, "cumulative cleanup time budget per worktree")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show decisions without deleting resources")
	cmd.Flags().IntVar(&count, "max-count", 10, "maximum linked worktrees (repository config by default)")
	cmd.Flags().IntVar(&prepared, "max-prepared", 3, "maximum prepared worktrees (repository config by default)")
	connection.Flags(cmd)
	return cmd
}
