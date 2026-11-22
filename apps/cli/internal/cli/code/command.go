// Package code maps RepoKit code analysis capabilities to CLI commands.
package code

import (
	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/spf13/cobra"
)

// NewCommand groups code analysis operations under a single domain entrypoint.
// +spec=Deadcode analysis is exposed as code dead, with shared repository and output options inherited from the root command.
// +link=component://docs/code.md
func NewCommand(opts *cmdutil.Options) *cobra.Command {
	root := &cobra.Command{Use: "code", Short: "Analyze code usage, structure and quality"}
	root.AddCommand(newDeadCommand(opts), newCallersCommand(opts), newCalleesCommand(opts), newHierarchyCommand(opts))
	return root
}
