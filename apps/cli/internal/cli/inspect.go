package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newInspectCommand(opts *options) *cobra.Command {
	var head string
	var staged bool
	command := &cobra.Command{
		Use:   "inspect",
		Short: "Describe repository components, languages and package tools",
		Long: `Inspect repository organization from tracked and non-ignored untracked paths.
Read only configuration needed for layout and tool evidence. No source parsing,
full-content digest, or submodule capture is performed. Use snapshot for content identity.`,
		Example: `  repocli inspect --json
  repocli inspect --staged --json
  repocli inspect --head HEAD --json`,
		Args: cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if opts.timeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			report, err := repokit.Inspect(ctx, repokit.InputRequest{Repository: opts.repository, Head: head, Staged: staged})
			if err != nil {
				return executionFailure(err)
			}
			if err := writeInspect(cmd.OutOrStdout(), report, opts.json); err != nil {
				return executionFailure(err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&head, "head", "", "inspect this exact commit/ref instead of the working tree")
	command.Flags().BoolVar(&staged, "staged", false, "inspect the index instead of the working tree")
	command.MarkFlagsMutuallyExclusive("head", "staged")
	return command
}

func writeInspect(output io.Writer, report repokit.InspectReport, asJSON bool) error {
	if !asJSON {
		recordResult(output, report)
	}
	if asJSON {
		return writeJSON(output, report)
	}
	if _, err := fmt.Fprintf(output, "input=%s complete=%t components=%d\n", report.Input, report.Complete, len(report.Components)); err != nil {
		return err
	}
	if err := writeLayout(output, report.Layout); err != nil {
		return err
	}
	for _, diagnostic := range report.Diagnostics {
		if _, err := fmt.Fprintf(output, "  %s: %s\n", diagnostic.Code, diagnostic.Message); err != nil {
			return err
		}
	}
	return nil
}

func writeLayout(output io.Writer, layout repokit.Layout) error {
	if layout.Repository != nil {
		if _, err := fmt.Fprintf(output, "  repository %s/%s\n", layout.Repository.Forge.Name, layout.Repository.Path); err != nil {
			return err
		}
	}
	for _, component := range layout.Components {
		if _, err := fmt.Fprintf(output, "  component %s (%s, %s)\n", component.Name, component.Root, component.Language); err != nil {
			return err
		}
		if component.Depot != nil {
			for _, bin := range component.Depot.Bins {
				if _, err := fmt.Fprintf(output, "    bin %s [%s]\n", bin.Path, bin.Kind); err != nil {
					return err
				}
			}
		}
		if component.Tools != nil {
			for _, tool := range *component.Tools {
				if _, err := fmt.Fprintf(output, "    tool %s %s [%s]\n", tool.Name, tool.Version, strings.Join(component.ToolEvidence[tool.Name], ", ")); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
