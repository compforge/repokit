// Package git adapts explicit RepoKit Git operations to Cobra commands.
package git

import (
	"context"
	"errors"
	"fmt"

	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	repogit "github.com/compforge/repokit/toolkit/go/git"
	"github.com/spf13/cobra"
)

func NewCommand(opts *command.Options) *cobra.Command {
	root := &cobra.Command{Use: "git", Short: "Perform explicit repository operations"}
	tag := &cobra.Command{Use: "tag", Short: "Manage remote Git tags"}
	var target, remote string
	publish := &cobra.Command{Use: "publish <tag>...", Short: "Publish immutable tags at one explicit commit", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if target == "" || opts.Timeout <= 0 {
			return errors.New("--target is required and --timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
		defer cancel()
		report, err := repogit.PublishTags(ctx, opts.Repository, repogit.PublishTagsOptions{Remote: remote, Target: target, Tags: args})
		if opts.JSON {
			if outputErr := command.JSON(cmd.OutOrStdout(), report); outputErr != nil {
				return outputErr
			}
		} else {
			command.Record(cmd.OutOrStdout(), report)
			fmt.Fprintf(cmd.OutOrStdout(), "Tags on %s at %s; confirmed=%t\n", report.Remote, report.TargetCommit, report.Confirmed)
			for _, tag := range report.Tags {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s\n", tag.Status, tag.Name, tag.ObjectID)
			}
			for _, phase := range report.Phases {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %.3fs\n", phase.Name, phase.DurationSeconds)
			}
		}
		return err
	}}
	publish.Flags().StringVar(&target, "target", "", "explicit local commit or ref to resolve once")
	publish.Flags().StringVar(&remote, "remote", "origin", "configured remote with one push destination")
	tag.AddCommand(publish)
	root.AddCommand(tag, newRebaseCommand(opts), newBranchCommand(opts))
	return root
}
