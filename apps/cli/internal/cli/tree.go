package cli

import (
	"context"
	"fmt"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newTreeCommand(opts *cmdutil.Options) *cobra.Command {
	var head string
	var staged bool
	cmd := &cobra.Command{Use: "tree", Short: "List repository directories, files and known file roles", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.Timeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			report, err := repokit.Tree(ctx, repokit.InputRequest{Repository: opts.Repository, Head: head, Staged: staged})
			if err != nil {
				return executionFailure(err)
			}
			if !opts.JSON {
				cmdutil.Record(cmd.OutOrStdout(), report)
			}
			if opts.JSON {
				return cmdutil.JSON(cmd.OutOrStdout(), report)
			}
			for _, dir := range report.Directories {
				if _, err = fmt.Fprintf(cmd.OutOrStdout(), "directory %s\n", dir.Path); err != nil {
					return err
				}
			}
			for _, file := range report.Files {
				if _, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", file.Kind, file.Path, file.Role); err != nil {
					return err
				}
			}
			if !report.Complete {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "incomplete: repository entries changed during observation")
			}
			return err
		}}
	cmd.Flags().StringVar(&head, "head", "", "read this exact commit/ref")
	cmd.Flags().BoolVar(&staged, "staged", false, "read the index")
	cmd.MarkFlagsMutuallyExclusive("head", "staged")
	return cmd
}
