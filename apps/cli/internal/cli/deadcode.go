package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newDeadcodeCommand(opts *options) *cobra.Command {
	var head string
	var excludeTests, excludeEntrypoints bool
	var staged bool
	var maxNodes, maxRelations int
	command := &cobra.Command{
		Use:   "deadcode",
		Short: "Find declarations with no incoming usage edges",
		Long: `Build a graph of all captured text documents and list declaration nodes
with no incoming non-structural relation. All confidence levels and self-references
count as uses. Results are candidates, not proof that deletion is safe; inspect
snapshot completeness and graph diagnostics. Entrypoints and public APIs may appear.`,
		Example: "  repocli deadcode --json\n  repocli deadcode --head HEAD --json",
		Args:    cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if opts.timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			return (repokit.DeadcodeRequest{MaxNodes: maxNodes, MaxRelations: maxRelations}).Validate()
		},
		RunE: func(command *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(command.Context(), opts.timeout)
			defer cancel()
			report, err := repokit.AnalyzeDeadcode(ctx, repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: opts.repository, Head: head, Staged: staged}, MaxNodes: maxNodes, MaxRelations: maxRelations, ExcludeTests: excludeTests, ExcludeEntrypoints: excludeEntrypoints})
			if err != nil {
				return executionFailure(err)
			}
			if err := writeDeadcode(command.OutOrStdout(), report, opts.json); err != nil {
				return executionFailure(err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&head, "head", "", "read this exact commit/ref instead of the working tree")
	command.Flags().BoolVar(&staged, "staged", false, "read the index instead of the working tree")
	command.Flags().IntVar(&maxNodes, "max-nodes", 0, "graph node budget (0 uses CodeGraph default)")
	command.Flags().IntVar(&maxRelations, "max-relations", 0, "graph relation budget (0 uses CodeGraph default)")
	command.Flags().BoolVar(&excludeTests, "exclude-tests", false, "omit test-file candidates while retaining their uses in the graph")
	command.Flags().BoolVar(&excludeEntrypoints, "exclude-entrypoints", false, "omit Go init and package-main main functions")
	command.MarkFlagsMutuallyExclusive("head", "staged")
	return command
}

func writeDeadcode(output io.Writer, report repokit.DeadcodeReport, asJSON bool) error {
	if !asJSON {
		recordResult(output, report)
	}
	if asJSON {
		return writeJSON(output, report)
	}
	if _, err := fmt.Fprintf(output, "Deadcode candidates: %d (no incoming usage edges)\n%s input=%s capture_complete=%t documents=%d graph_diagnostics=%d\n", len(report.Nodes), report.Snapshot.Snapshot, report.Snapshot.Input, report.Snapshot.Complete, report.Documents, len(report.Diagnostics)); err != nil {
		return err
	}
	for _, node := range report.Nodes {
		name := node.QualifiedName
		if name == "" {
			name = node.Name
		}
		location := "unknown"
		if node.Location != nil {
			location = fmt.Sprintf("%s:%d", node.Location.Path, node.Location.Line)
		}
		if _, err := fmt.Fprintf(output, "%s\t%s\t%s\n", location, node.Kind, name); err != nil {
			return err
		}
	}
	for _, diagnostic := range report.Snapshot.Diagnostics {
		if _, err := fmt.Fprintf(output, "capture: %s: %s\n", diagnostic.Code, diagnostic.Message); err != nil {
			return err
		}
	}
	if len(report.Diagnostics) > 0 {
		if _, err := fmt.Fprintln(output, "Use --json to inspect graph diagnostics and candidate details."); err != nil {
			return err
		}
	}
	return nil
}
