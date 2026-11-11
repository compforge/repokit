package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newDeadcodeCommand(opts *cmdutil.Options) *cobra.Command {
	var head, base, mode string
	var files, roots []string
	var excludeTests, excludeEntrypoints bool
	var staged bool
	var maxNodes, maxRelations int
	command := &cobra.Command{
		Use:   "deadcode",
		Short: "Find unused declarations and deadcode affected by a diff",
		Long: `Analyze whole-snapshot usage evidence, optionally reporting only selected files.
With --base, use changed graph nodes in both snapshots as analysis entrypoints:
report affected historical candidates as well as declarations that became dead.
Unreachable mode follows declared roots and reports test-only uses separately.
Results carry reasons and risks; they are not safe-deletion verdicts.`,
		Example: "  repocli deadcode --json\n  repocli deadcode --base HEAD --json\n  repocli deadcode --base main --head HEAD --mode unreachable --json\n  repocli deadcode --file internal/service.go --json",
		Args:    cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if opts.Timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			return (repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Head: head, Staged: staged}, MaxNodes: maxNodes, MaxRelations: maxRelations, Base: base, Files: files, Mode: mode, Roots: roots}).Validate()
		},
		RunE: func(command *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(command.Context(), opts.Timeout)
			defer cancel()
			report, err := repokit.AnalyzeDeadcode(ctx, repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: opts.Repository, Head: head, Staged: staged}, MaxNodes: maxNodes, MaxRelations: maxRelations, Base: base, Files: files, Mode: mode, Roots: roots, ExcludeTests: excludeTests, ExcludeEntrypoints: excludeEntrypoints})
			if err != nil {
				return executionFailure(err)
			}
			if err := writeDeadcode(command.OutOrStdout(), report, opts.JSON); err != nil {
				return executionFailure(err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "", "compare against this ref; follow dependencies from changed documents in both graphs")
	// +spec=Each --file occurrence preserves one literal path; an empty argument is invalid.
	command.Flags().StringArrayVar(&files, "file", nil, "limit reported candidates to exact snapshot-relative files (repeatable)")
	command.Flags().StringVar(&mode, "mode", "unreferenced", "analysis rule: unreferenced or unreachable")
	command.Flags().StringArrayVar(&roots, "root", nil, "additional unreachable-mode root: exact node ID or unique qualified name (repeatable)")
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
		cmdutil.Record(output, report)
	}
	if asJSON {
		return cmdutil.JSON(output, report)
	}
	if _, err := fmt.Fprintf(output, "Deadcode candidates: %d mode=%s scope=%s\n%s input=%s capture_complete=%t documents=%d graph_diagnostics=%d\n", len(report.Nodes), report.Mode, report.Scope.Kind, report.Snapshot.Snapshot, report.Snapshot.Input, report.Snapshot.Complete, report.Documents, len(report.Diagnostics)); err != nil {
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
		if _, err := fmt.Fprintf(output, "%s\t%s\t%s\treason=%s change=%s risks=%s\n", location, node.Kind, name, node.Reason, node.Change, strings.Join(node.Risks, ",")); err != nil {
			return err
		}
	}
	if report.Baseline != nil {
		if _, err := fmt.Fprintf(output, "Compared with %s; before_graph_diagnostics=%d; comparison_complete=%t\n", report.Baseline.Snapshot.Head, len(report.Baseline.Diagnostics), report.Scope.Complete); err != nil {
			return err
		}
	}
	for _, diagnostic := range report.Scope.Diagnostics {
		if _, err := fmt.Fprintf(output, "scope: %s: %s\n", diagnostic.Code, diagnostic.Message); err != nil {
			return err
		}
	}
	for _, diagnostic := range report.Snapshot.Diagnostics {
		if _, err := fmt.Fprintf(output, "capture: %s: %s\n", diagnostic.Code, diagnostic.Message); err != nil {
			return err
		}
	}
	if len(report.Diagnostics) > 0 || (report.Baseline != nil && len(report.Baseline.Diagnostics) > 0) {
		if _, err := fmt.Fprintln(output, "Use --json to inspect graph diagnostics and candidate details."); err != nil {
			return err
		}
	}
	return nil
}
