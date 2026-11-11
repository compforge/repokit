package cli

import (
	"fmt"
	"io"
	"strings"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
)

func writeUnits(w io.Writer, r repokit.UnitReport, structured bool) error {
	if !structured {
		cmdutil.Record(w, r)
	}
	if structured {
		return cmdutil.JSON(w, r)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Diff -> Fragment -> Unit: %d files, %d fragments, %d units\n", len(r.Changes), len(r.Fragments), len(r.Units))
	fmt.Fprintf(&b, "Input: %s; base: %s; complete: %t; count limit exceeded: %t\n", r.Input, r.Base, r.Complete, r.LimitExceeded)
	references := map[string]int{}
	for _, u := range r.Units {
		for _, id := range u.FragmentIDs {
			references[id]++
		}
	}
	for _, ch := range r.Changes {
		fmt.Fprintf(&b, "\nFile %s (+%d -%d)\n", ch.Path(), ch.Insertions, ch.Deletions)
		if len(ch.Tags) > 0 {
			fmt.Fprintf(&b, "  tags: %v\n", ch.Tags)
		}
		for _, f := range r.Fragments {
			if f.Path != ch.Path() {
				continue
			}
			fmt.Fprintf(&b, "  %s [%s] +%d -%d", f.ID, fmt.Sprintf("before=%v after=%v", f.Counts().Before, f.Counts().After), f.Insertions, f.Deletions)
			if references[f.ID] > 1 {
				fmt.Fprintf(&b, " shared by %d units", references[f.ID])
			}
			b.WriteByte('\n')
			for side, anchors := range [][]repokit.SourceElement{f.Before, f.After} {
				label := "before"
				if side == 1 {
					label = "after"
				}
				for _, a := range anchors {
					fmt.Fprintf(&b, "    %s: %s %s:%d-%d %s\n", label, a.Kind, a.Path, a.Span.Start, a.Span.End, a.Name)
				}
			}
			if len(f.Before) == 0 && len(f.After) == 0 {
				b.WriteString("    unbound edits (retained)\n")
			}
		}
	}
	for _, u := range r.Units {
		fmt.Fprintf(&b, "\nUnit %s [%s] bytes=%d over-budget=%t\n  fragments: %s\n", u.ID, strings.Join(u.Paths, ", "), u.DiffSize, u.BudgetExceeded, strings.Join(u.FragmentIDs, ", "))
		fmt.Fprintf(&b, "  counts: before=%v after=%v; imports-only=%t\n", u.Counts.Before, u.Counts.After, u.Counts.Only(repokit.ElementImport))
		for _, e := range u.Relations {
			fmt.Fprintf(&b, "  relation: %s -> %s (%s, %s, snapshot %s)\n", e.FromFragment, e.ToFragment, e.Link.Kind, e.Link.Confidence, e.Link.Snapshot)
		}
		for _, e := range u.Boundaries {
			fmt.Fprintf(&b, "  boundary: %s -> %s (%s)\n", e.FromFragment, e.ToFragment, e.Link.Kind)
		}
	}
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "\nGrouping %s: %d -> %d; budget blocked=%d\n", s.Strategy, s.InputUnits, s.OutputUnits, s.BudgetBlocked)
	}
	for _, d := range r.Decisions {
		fmt.Fprintf(&b, "Merge %s: %s\n", d.Strategy, strings.Join(d.FragmentIDs, ", "))
	}
	for _, m := range r.Merges {
		fmt.Fprintf(&b, "Namespace %s (%s): %s\n", m.Namespace, m.Snapshot, strings.Join(m.FragmentIDs, ", "))
	}
	for _, d := range r.Diagnostics {
		fmt.Fprintf(&b, "Gap %s %s: %s\n", d.Code, d.Path, d.Message)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
