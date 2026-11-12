package worktree

import (
	"context"
	"errors"
	"fmt"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func newObserveCommand(opts *cmdutil.Options) *cobra.Command {
	var reconcile bool
	cmd := &cobra.Command{Use: "observe", Short: "Record local branches and checkout locations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if opts.Timeout <= 0 {
			return errors.New("--timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
		defer cancel()
		home, err := state.ResolveHome(opts.Home)
		if err != nil {
			return cmdutil.ExecutionFailure(err)
		}
		directory, err := state.Observe(ctx, opts.Repository, home, reconcile)
		if err != nil {
			return cmdutil.ExecutionFailure(err)
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
	cmd.Flags().BoolVar(&reconcile, "reconcile", false, "retire absent checkout observations without deleting source directories")
	return cmd
}
