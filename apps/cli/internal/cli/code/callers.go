package code

import (
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func callersPosition(at string) (repokit.CallersRequest, error) {
	var req repokit.CallersRequest
	last := strings.LastIndexByte(at, ':')
	if last > 0 {
		prev := strings.LastIndexByte(at[:last], ':')
		if prev > 0 {
			line, e1 := strconv.Atoi(at[prev+1 : last])
			column, e2 := strconv.Atoi(at[last+1:])
			req.Path, req.Line, req.Column = path.Clean(at[:prev]), line, column
			if e1 == nil && e2 == nil && req.Validate() == nil {
				return req, nil
			}
		}
	}
	return req, fmt.Errorf("--at requires a repository-relative file:line:column (one-based byte column)")
}

func newCallersCommand(opts *cmdutil.Options) *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   "callers --at <file:line:column>",
		Short: "List incoming static call sites for a declaration",
		Long: "List incoming static call sites for a declaration.\n\n" +
			"Use the results as a guide. Static analysis may miss callers or report calls to the wrong declaration.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req, err := callersPosition(at)
			if err != nil {
				return err
			}
			if opts.Timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			req.Repository = opts.Repository
			report, err := repokit.FindCallers(ctx, req)
			if err != nil {
				if report.Snapshot.Snapshot != "" {
					cmdutil.Record(cmd.OutOrStdout(), report)
				}
				return cmdutil.ExecutionFailure(err)
			}
			if opts.JSON {
				return cmdutil.JSON(cmd.OutOrStdout(), report)
			}
			cmdutil.Record(cmd.OutOrStdout(), report)
			return cmdutil.ExecutionFailure(writeCallers(cmd.OutOrStdout(), report))
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "position on the declared function, method or constructor name")
	_ = cmd.MarkFlagRequired("at")
	return cmd
}

func writeCallers(w io.Writer, report repokit.CallersReport) error {
	loc := report.Target.NameLocation
	if _, err := fmt.Fprintf(w, "%s (%s:%d:%d) — %d static call site(s)\n", report.Target.QualifiedName, loc.Path, loc.Line, loc.Column, len(report.Callers)); err != nil {
		return err
	}
	for _, s := range report.Callers {
		loc := s.Call.Location
		name := s.Caller.QualifiedName
		if name == "" {
			name = s.Caller.Name
		}
		if _, err := fmt.Fprintf(w, "  %s  %s:%d:%d  [%s]\n", name, loc.Path, loc.Line, loc.Column, s.Call.Confidence); err != nil {
			return err
		}
	}
	if !report.Snapshot.Complete || len(report.Diagnostics) > 0 {
		_, err := fmt.Fprintf(w, "Capture complete=%t; %d graph diagnostic(s). See --json for coverage details.\n", report.Snapshot.Complete, len(report.Diagnostics))
		return err
	}
	return nil
}
