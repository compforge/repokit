package validation

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/compforge/repokit/toolkit/go"
)

// +spec=Planning and history select content, not encoding; text never implies checks executed.
// +link=../../../../../docs/cli-output.md
func writePlanText(w io.Writer, plan repokit.ValidationPlan) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Planned checks for %d Components; no checks executed\n", len(plan.Components))
	fmt.Fprintf(&b, "Checkout: %s\nSnapshot: %s\nConcurrency: %d\n", plan.Checkout, plan.Snapshot, plan.Concurrency)
	if plan.Impact != nil {
		fmt.Fprintf(&b, "Impact: base=%s complete=%t\n", plan.Impact.Base, plan.Impact.Complete)
		for _, d := range plan.Impact.Diagnostics {
			fmt.Fprintf(&b, "  %s\n", d.Message)
		}
	}
	format := false
	for _, group := range plan.Components {
		for _, check := range group.Checks {
			fmt.Fprintf(&b, "%s %s: %s — %s\n", group.Component.Root, check.Check, check.Scope, check.Reason)
			if len(check.Command) > 0 {
				fmt.Fprintf(&b, "  Command argv: %q\n", check.Command)
			}
			fmt.Fprintf(&b, "  Selected files: %d; input files: %d — %s\n", len(check.Files), len(check.InputFiles), check.InputReason)
			format = format || check.Check == "format"
		}
	}
	if format {
		fmt.Fprintln(&b, "Checks after format are previews; execution replans from formatted contents.")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeHistorySummaryText(w io.Writer, history repokit.ValidationHistory) error {
	var b strings.Builder
	fmt.Fprintln(&b, "Retained validation history; no checks executed by this query")
	fmt.Fprintf(&b, "Repository: %s\n", history.Repository)
	if history.From.IsZero() {
		fmt.Fprintln(&b, "No retained records.")
	} else {
		fmt.Fprintf(&b, "Window: %s — %s (retained records, not lifetime totals)\n", history.From.Format(time.RFC3339), history.To.Format(time.RFC3339))
	}
	for _, key := range slices.Sorted(maps.Keys(history.Checks)) {
		c := history.Checks[key]
		fmt.Fprintf(&b, "%s: %d executed (%d passed, %d failed, %d unfinished, %d other), %d reused; historical execution=%.3fs lookup=%.3fs\n", key, c.Executions, c.Passed, c.Failed, c.Unfinished, c.Other, c.Reuses, c.ExecutionSeconds, c.LookupSeconds)
		if !c.LastCheckedAt.IsZero() {
			fmt.Fprintf(&b, "  Last checked: %s\n", c.LastCheckedAt.Format(time.RFC3339))
		}
	}
	fmt.Fprintln(&b, "Use validate history list/show for retained run evidence; history does not establish a current pass.")
	_, err := io.WriteString(w, b.String())
	return err
}
