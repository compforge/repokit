package code

import (
	"context"
	"fmt"
	"io"
	"strings"

	cg "github.com/compforge/codegraph"
	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newCalleesCommand(opts *cmdutil.Options) *cobra.Command {
	return symbolCommand(opts, "callees", "List direct outgoing static call sites for a declaration", callHelp, "callable", func(ctx context.Context, w io.Writer, req repokit.SymbolPosition) error {
		report, err := repokit.FindCallees(ctx, req)
		return deliverLookup(w, opts.JSON, report, report.Snapshot.Snapshot, err, writeCallees)
	})
}

func newHierarchyCommand(opts *cmdutil.Options) *cobra.Command {
	var direction string
	cmd := symbolCommand(opts, "hierarchy", "Show type ancestors and descendants", "Follow static extends and implements relations recursively from the selected type. Shared types are expanded once per direction. Static analysis may omit or infer relationships.", "type", func(ctx context.Context, w io.Writer, pos repokit.SymbolPosition) error {
		dir := repokit.HierarchyDirection(direction)
		if direction == "" {
			return fmt.Errorf("--direction must be parents, children, or both")
		}
		if err := dir.Validate(); err != nil {
			return err
		}
		report, err := repokit.TypeHierarchy(ctx, repokit.HierarchyRequest{Position: pos, Direction: dir})
		return deliverLookup(w, opts.JSON, report, report.Snapshot.Snapshot, err, writeHierarchy)
	})
	cmd.Flags().StringVar(&direction, "direction", string(repokit.HierarchyBoth), "traversal direction: parents, children, or both")
	return cmd
}

func declarationText(n cg.Node) string {
	name := n.QualifiedName
	if name == "" {
		name = n.Name
	}
	if loc := n.NameLocation; loc != nil {
		return fmt.Sprintf("%s (%s:%d:%d)", name, loc.Path, loc.Line, loc.Column)
	}
	return name
}

func writeCallees(w io.Writer, r repokit.CalleesReport) error {
	if _, err := fmt.Fprintf(w, "%s — %d outgoing static call site(s)\n", declarationText(r.Target), len(r.Callees)); err != nil {
		return err
	}
	for _, site := range r.Callees {
		loc := site.Call.Location
		if _, err := fmt.Fprintf(w, "  %s:%d:%d -> %s [%s]\n", loc.Path, loc.Line, loc.Column, declarationText(site.Callee), site.Call.Confidence); err != nil {
			return err
		}
	}
	return writeCoverage(w, r.Snapshot.Complete, len(r.Diagnostics))
}

func writeHierarchy(w io.Writer, r repokit.HierarchyReport) error {
	if _, err := fmt.Fprintln(w, declarationText(r.Target)); err != nil {
		return err
	}
	if r.Direction != repokit.HierarchyChildren {
		if err := writeTypes(w, "Parents", r.Parents, false); err != nil {
			return err
		}
	}
	if r.Direction != repokit.HierarchyParents {
		if err := writeTypes(w, "Children", r.Children, true); err != nil {
			return err
		}
	}
	return writeCoverage(w, r.Snapshot.Complete, len(r.Diagnostics))
}

func writeTypes(w io.Writer, label string, entries []repokit.HierarchyEntry, children bool) error {
	if _, err := fmt.Fprintf(w, "%s (%d)\n", label, len(entries)); err != nil {
		return err
	}
	for _, entry := range entries {
		kind := string(entry.Relation.Kind)
		if children {
			switch entry.Relation.Kind {
			case cg.Extends:
				kind = "extended by"
			case cg.Implements:
				kind = "implemented by"
			}
		}
		marker := ""
		if entry.AlreadyShown {
			marker = " (already shown)"
		}
		if _, err := fmt.Fprintf(w, "%s%s %s [%s]%s\n", strings.Repeat("  ", entry.Depth), kind, declarationText(entry.Type), entry.Relation.Confidence, marker); err != nil {
			return err
		}
	}
	return nil
}
