package cli

import (
	"fmt"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/spf13/cobra"
)

func newVersionCommand(opts *cmdutil.Options, version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the installed binary version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			var err error
			if opts.JSON {
				err = cmdutil.JSON(command.OutOrStdout(), map[string]string{"version": version})
			} else {
				_, err = fmt.Fprintf(command.OutOrStdout(), "repocli version %s\n", version)
			}
			if err != nil {
				return executionFailure(err)
			}
			return nil
		},
	}
}
