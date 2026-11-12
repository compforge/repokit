package git

import (
	"context"
	"errors"
	"fmt"

	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	repogit "github.com/compforge/repokit/toolkit/go/git"
	"github.com/spf13/cobra"
)

func newPushCommand(opts *command.Options) *cobra.Command {
	var input repogit.PushOptions
	cmd := &cobra.Command{Use: "push", Short: "Publish one frozen branch commit and verify the remote destination", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if opts.Timeout <= 0 {
			return errors.New("--timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
		defer cancel()
		report, err := repogit.Push(ctx, opts.Repository, input)
		if opts.JSON {
			if e := command.JSON(cmd.OutOrStdout(), report); e != nil {
				return e
			}
		} else {
			command.Record(cmd.OutOrStdout(), report)
			fmt.Fprintf(cmd.OutOrStdout(), "Push %s to %s/%s at %s: %s; confirmed=%t pushAttempted=%t\n", report.Branch, report.Remote, report.Target, report.TargetCommit, report.RemoteState, report.Confirmed, report.PushAttempted)
			if report.RemoteCommit != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Remote commit: %s\n", report.RemoteCommit)
			}
			if report.UpstreamRequested {
				fmt.Fprintf(cmd.OutOrStdout(), "Upstream confirmed=%t\n", report.UpstreamConfirmed)
			}
			for _, d := range report.Diagnostics {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", d.Code, d.Message)
			}
			for _, p := range report.Phases {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %.3fs\n", p.Name, p.DurationSeconds)
			}
		}
		return err
	}}
	cmd.Flags().StringVar(&input.Branch, "branch", "", "explicit local source branch")
	cmd.Flags().StringVar(&input.Commit, "commit", "", "full source commit ID frozen before this operation")
	cmd.Flags().StringVar(&input.Remote, "remote", "origin", "configured remote with one push destination")
	cmd.Flags().StringVar(&input.Target, "target", "", "remote destination branch (default: source branch)")
	cmd.Flags().BoolVar(&input.SetUpstream, "set-upstream", false, "configure source branch tracking after confirmed publication")
	cmd.Flags().BoolVar(&input.Check, "check", false, "only observe the original commit at the destination; never push")
	_ = cmd.MarkFlagRequired("branch")
	_ = cmd.MarkFlagRequired("commit")
	return cmd
}
