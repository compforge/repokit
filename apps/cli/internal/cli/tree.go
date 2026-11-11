package cli

import (
	"context"
	"fmt"

	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newTreeCommand(opts *options) *cobra.Command {
	var head string
	var staged bool
	cmd := &cobra.Command{Use: "tree", Short: "List repository directories, files and known file roles", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.timeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			report, err := repokit.Tree(ctx, repokit.InputRequest{Repository: opts.repository, Head: head, Staged: staged})
			if err != nil {
				return executionFailure(err)
			}
			if !opts.json {
				recordResult(cmd.OutOrStdout(), report)
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), report)
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
