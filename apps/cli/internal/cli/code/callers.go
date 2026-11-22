package code

import (
	"context"
	"fmt"
	"io"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

const callHelp = "Use the results as a guide. Static analysis may miss callers or report calls to the wrong declaration."

func newCallersCommand(opts *cmdutil.Options) *cobra.Command {
	return symbolCommand(opts, "callers", "List incoming static call sites for a declaration", callHelp, "callable", func(ctx context.Context, w io.Writer, req repokit.SymbolPosition) error {
		report, err := repokit.FindCallers(ctx, req)
		return deliverLookup(w, opts.JSON, report, report.Snapshot.Snapshot, err, writeCallers)
	})
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
	return writeCoverage(w, report.Snapshot.Complete, len(report.Diagnostics))
}

func writeCoverage(w io.Writer, complete bool, diagnostics int) error {
	if !complete || diagnostics > 0 {
		_, err := fmt.Fprintf(w, "Capture complete=%t; %d graph diagnostic(s). See --json for coverage details.\n", complete, diagnostics)
		return err
	}
	return nil
}
