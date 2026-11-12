// Package worktree maps checkout lifecycle operations to CLI input and output.
package worktree

import (
	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/spf13/cobra"
)

func NewCommand(opts *command.Options) *cobra.Command {
	root := &cobra.Command{Use: "worktree", Short: "Maintain linked worktrees"}
	root.AddCommand(newEnsureCommand(opts), newObserveCommand(opts), newCleanCommand(opts))
	return root
}
