package cli

import (
	"context"
	"fmt"
	"io"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newSnapshotCommand(opts *cmdutil.Options) *cobra.Command {
	var head string
	var staged bool
	command := &cobra.Command{
		Use:   "snapshot",
		Short: "Identify repository contents without change analysis",
		Long: `Read a content digest of tracked and non-ignored untracked working-tree files.
Use --staged for the index, or --head for an exact commit/ref. No commit, backup,
or filesystem snapshot is created. Use inspect for repository identity and component metadata.
Always check capture completeness before comparing digests.`,
		Example: `  repocli snapshot --json
  repocli snapshot --staged --json
  repocli snapshot --head HEAD --json`,
		Args: cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if opts.Timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(command.Context(), opts.Timeout)
			defer cancel()
			result, err := repokit.Snapshot(ctx, repokit.InputRequest{Repository: opts.Repository, Head: head, Staged: staged})
			if err != nil {
				return executionFailure(err)
			}
			if err := writeSnapshot(command.OutOrStdout(), result, opts.JSON); err != nil {
				return executionFailure(err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&head, "head", "", "read this exact commit/ref instead of the working tree")
	command.Flags().BoolVar(&staged, "staged", false, "read the index instead of the working tree")
	command.MarkFlagsMutuallyExclusive("head", "staged")
	return command
}

func writeSnapshot(output io.Writer, report repokit.SnapshotReport, asJSON bool) error {
	if !asJSON {
		cmdutil.Record(output, report)
	}
	if asJSON {
		return cmdutil.JSON(output, report)
	}
	if _, err := fmt.Fprintf(output, "%s input=%s complete=%t files=%d\n", report.Snapshot, report.Input, report.Complete, report.FileCount); err != nil {
		return err
	}
	for _, diagnostic := range report.Diagnostics {
		if _, err := fmt.Fprintf(output, "  %s: %s\n", diagnostic.Code, diagnostic.Message); err != nil {
			return err
		}
	}
	return nil
}
