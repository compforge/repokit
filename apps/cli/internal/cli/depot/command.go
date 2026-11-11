// Package depot maps RepoKit Depot operations to CLI input and output.
package depot

import (
	"context"
	"errors"
	"fmt"
	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/depot"
	"github.com/spf13/cobra"
)

func NewCommand(opts *command.Options) *cobra.Command {
	root := &cobra.Command{Use: "depot", Short: "Observe and prepare Component dependency Bins"}
	for _, operation := range []string{"inspect", "prepare"} {
		var components []string
		cmd := &cobra.Command{Use: operation, Args: cobra.NoArgs}
		cmd.Flags().StringArrayVar(&components, "component", nil, "Component root relative to checkout; repeat to select several")
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			if opts.Timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			var report depot.Report
			var err error
			if operation == "inspect" {
				report, err = depot.Inspect(ctx, opts.Repository, depot.Options{Components: components})
			} else {
				report, err = depot.Prepare(ctx, opts.Repository, depot.Options{Components: components})
			}
			if opts.JSON {
				if e := command.JSON(cmd.OutOrStdout(), report); e != nil {
					return e
				}
			} else {
				command.Record(cmd.OutOrStdout(), report)
				fmt.Fprintf(cmd.OutOrStdout(), "%d Component(s), %d Bin(s); complete=%t\n", len(report.Components), len(report.Bins), report.Complete)
				for _, bin := range report.Bins {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s (%s) %s\n", bin.Path, bin.Status, bin.Action, bin.Manager, bin.Reason)
					if bin.Stdout != "" {
						fmt.Fprintln(cmd.OutOrStdout(), bin.Stdout)
					}
					if bin.Stderr != "" {
						fmt.Fprintln(cmd.OutOrStdout(), bin.Stderr)
					}
				}
				for _, phase := range report.Phases {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %.3fs\n", phase.Name, phase.Bin, phase.DurationSeconds)
				}
			}
			return err
		}
		root.AddCommand(cmd)
	}
	return root
}
