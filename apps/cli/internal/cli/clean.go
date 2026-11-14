package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/apps/cli/internal/cli/forgeaccess"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func newCleanCommand(opts *cmdutil.Options) *cobra.Command {
	var days int
	var dry bool
	var connection forgeaccess.Options
	command := &cobra.Command{Use: "clean", Short: "Apply worktree retention and reclaim inactive repository state",
		Long: "Apply worktree retention and reclaim inactive repository state. Without --repo, scan all recorded repositories in the selected home. Worktrees use the repository retention policy. Missing repository access times count as oldest; source and Git protections still apply.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if days < 1 || days > 36500 {
				return errors.New("--inactive-days must be between 1 and 36500")
			}
			if err := connection.Validate(opts); err != nil {
				return err
			}
			ctx := cmd.Context()
			cwd, err := os.Getwd()
			if err != nil {
				return executionFailure(err)
			}
			selected := ""
			if cmd.Flags().Changed("repo") {
				selected = opts.Repository
			}
			report, err := state.Clean(ctx, state.CleanOptions{Home: opts.Home, Repository: selected, InactiveDays: days, DryRun: dry, CurrentDirectory: cwd, RepositoryTimeout: opts.Timeout, PullRequests: func(ctx context.Context, repo string) (state.WorktreePullRequests, error) {
				return connection.WorktreePullRequests(ctx, repo, opts.Home)
			}})
			if opts.JSON {
				_ = cmdutil.JSON(cmd.OutOrStdout(), report)
			} else {
				cmdutil.Record(cmd.OutOrStdout(), report)
				fmt.Fprintf(cmd.OutOrStdout(), "Cleanup: %d actions; %d errors; estimated reclaimable bytes: %d (dry-run=%t)\n", len(report.Actions), len(report.Errors), report.EstimatedBytes, dry)
				for _, a := range report.Actions {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s (%s; %d estimated bytes)\n", a.Status, a.Action, a.Path, a.Reason, a.EstimatedBytes)
				}
				// +spec=Partial cleanup failures must expose their causes in text as well as JSON.
				for _, issue := range report.Errors {
					fmt.Fprintln(cmd.ErrOrStderr(), "error:", issue)
				}
			}
			if err != nil {
				return executionFailure(err)
			}
			if len(report.Errors) > 0 {
				return &resultFailure{Message: "cleanup incomplete"}
			}
			return nil
		}}
	command.Flags().DurationVar(&opts.Timeout, "timeout", opts.Timeout, "cleanup deadline per repository (shared by its clones and worktrees)")
	command.Flags().BoolVar(&dry, "dry-run", false, "preview decisions without deleting resources")
	command.Flags().IntVar(&days, "inactive-days", state.DefaultInactiveDays, "minimum days since repository state was accessed")
	connection.Flags(command)
	return command
}
