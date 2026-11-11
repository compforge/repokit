package git

import (
	"context"
	"errors"
	"fmt"

	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/git/rebase"
	"github.com/spf13/cobra"
)

func newRebaseCommand(opts *command.Options) *cobra.Command {
	root := &cobra.Command{Use: "rebase", Short: "Rewrite a branch with recoverable state and exact-lease publication"}
	for _, action := range []rebase.Action{rebase.Start, rebase.Status, rebase.Continue, rebase.Abort, rebase.Finish} {
		var input rebase.Options
		cmd := &cobra.Command{Use: string(action), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.Timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			report, err := rebase.Run(ctx, opts.Repository, action, input)
			if opts.JSON {
				if e := command.JSON(cmd.OutOrStdout(), report); e != nil {
					return e
				}
			} else {
				command.Record(cmd.OutOrStdout(), report)
				fmt.Fprintf(cmd.OutOrStdout(), "Rebase %s: %s; confirmed=%t\n", action, report.Phase, report.Confirmed)
				if r := report.Transaction; r != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s onto %s @ %s; lease=%s; HEAD=%s\n", r.Branch, r.Target, r.TargetCommit, r.ExpectedRemote, report.Head)
				}
				for _, p := range report.Conflicts {
					fmt.Fprintf(cmd.OutOrStdout(), "conflict: %s\n", p)
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
		if action == rebase.Start {
			cmd.Flags().StringVar(&input.Branch, "branch", "", "explicit currently checked-out source branch")
			cmd.Flags().StringVar(&input.Target, "target", "", "explicit remote target branch")
			cmd.Flags().StringVar(&input.Remote, "remote", "origin", "configured Git remote")
			_ = cmd.MarkFlagRequired("branch")
			_ = cmd.MarkFlagRequired("target")
		}
		root.AddCommand(cmd)
	}
	return root
}
