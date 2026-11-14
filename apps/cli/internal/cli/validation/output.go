package validation

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
)

// Presentation is shared by text rendering and the root's final JSON envelope.
type Presentation struct {
	Summary     string
	Outcome     string
	Diagnostics []command.Diagnostic
	Cost        Cost
}

func Describe(v repokit.ValidationReport) Presentation {
	r := Presentation{}
	executed, reused, skipped, failed, passed := 0, 0, 0, 0, 0
	cost := &r.Cost
	cost.Steps = []CheckCost{}
	for _, part := range []struct {
		name   string
		groups []repokit.ComponentValidationResult
	}{{"preparation", v.Preparation}, {"components", v.Components}} {
		for i, g := range part.groups {
			for j, c := range g.Checks {
				step := currentCheckCost(g.Component.Root, c, fmt.Sprintf("/data/%s/%d/checks/%d", part.name, i, j))
				cost.Steps = append(cost.Steps, step)
				cost.ExecutionSeconds += step.ExecutionSeconds
				cost.LookupSeconds += step.LookupSeconds
				if c.Reused {
					reused++
				} else if c.Status == "skipped" {
					skipped++
				} else if c.Status == "passed" || c.Status == "failed" || c.Status == "uncertain" {
					executed++
				}
				switch c.Status {
				case "passed":
					passed++
				case "skipped":
				default:
					failed++
					code := "validation.check_failed"
					switch c.Status {
					case "unavailable":
						code = "validation.check_unavailable"
					case "cancelled":
						code = "validation.check_canceled"
					case "uncertain":
						code = "validation.check_uncertain"
					}
					message := c.Diagnostic
					if message == "" {
						message = c.Reason
					}
					r.Diagnostics = append(r.Diagnostics, command.Diagnostic{Code: code, Severity: "error", Phase: c.Check, Subject: g.Component.Root, Message: message, Evidence: fmt.Sprintf("/data/%s/%d/checks/%d", part.name, i, j)})
				}
			}
		}
	}
	r.Summary = fmt.Sprintf("Validation passed=%t complete=%t inputsConsistent=%t; %d executed, %d reused, %d skipped, %d unsuccessful", v.Passed, v.Complete, v.InputsConsistent, executed, reused, skipped, failed)
	if executed == 0 && reused == 0 && failed == 0 {
		r.Summary = fmt.Sprintf("No checks executed; %d skipped", skipped)
	}
	if !v.Passed || !v.Complete || (!v.InputsConsistent && len(v.Components) > 0) {
		r.Outcome = "failed"
		if passed > 0 {
			r.Outcome = "partial"
		}
	}
	if !v.InputsConsistent && len(v.Components) > 0 {
		r.Diagnostics = append(r.Diagnostics, command.Diagnostic{Code: "validation.inputs_changed", Severity: "error", Phase: "validation", Message: "Inputs changed during validation; passing checks are not consistent evidence", Evidence: "/data/inputsConsistent"})
	}
	if !v.Complete {
		r.Diagnostics = append(r.Diagnostics, command.Diagnostic{Code: "validation.incomplete", Severity: "error", Phase: "validation", Message: "Not all selected checks reached a known terminal state", Evidence: "/data/complete"})
	}
	for i, d := range v.Diagnostics {
		r.Diagnostics = append(r.Diagnostics, command.Diagnostic{Code: "validation.diagnostic", Severity: "warning", Phase: "validation", Message: d, Evidence: fmt.Sprintf("/data/diagnostics/%d", i)})
	}
	return r
}

// +spec=Text validation output leads with the verdict and bounded failure evidence; JSON retains captured logs
func WriteText(w io.Writer, report repokit.ValidationReport) error {
	r := Describe(report)
	var b strings.Builder
	fmt.Fprintln(&b, r.Summary)
	if wall, phases, ok := command.Timing(w); ok {
		fmt.Fprintf(&b, "Cost: %.3fs wall so far; %.3fs summed execution; %.3fs summed lookup (concurrent checks overlap)\n", wall, r.Cost.ExecutionSeconds, r.Cost.LookupSeconds)
		for _, phase := range phases {
			fmt.Fprintf(&b, "  phase %s: %.3fs\n", phase.Name, phase.DurationSeconds)
		}
	}
	for _, d := range r.Diagnostics {
		fmt.Fprintf(&b, "[%s] %s %s: %s (evidence %s)\n", d.Severity, d.Code, d.Subject, d.Message, d.Evidence)
	}
	for _, groups := range [][]repokit.ComponentValidationResult{report.Preparation, report.Components} {
		for _, group := range groups {
			for _, check := range group.Checks {
				status := check.Status
				if check.Reused {
					status = "reused"
				}
				cost := currentCheckCost(group.Component.Root, check, "")
				fmt.Fprintf(&b, "%s %s: %s (%s, execute=%.3fs lookup=%.3fs) — %s; %s\n", group.Component.Root, check.Check, status, check.Scope, cost.ExecutionSeconds, cost.LookupSeconds, check.Reason, check.ReuseReason)
				writeTestTimings(&b, check)
				if check.Status != "passed" && check.Status != "skipped" {
					for _, stream := range []struct{ name, text string }{{"stdout", check.Stdout}, {"stderr", check.Stderr}} {
						if stream.text == "" {
							continue
						}
						excerpt, truncated := logExcerpt(stream.text)
						fmt.Fprintf(&b, "  %s:\n%s\n", stream.name, excerpt)
						if truncated {
							fmt.Fprintln(&b, "  [display truncated; captured output is retained in the JSON report]")
						}
					}
				}
			}
		}
	}
	fmt.Fprintln(&b, "Use --json or --output <path> for the report and captured check output.")
	_, err := io.WriteString(w, b.String())
	return err
}

// Bound both line count and characters; a single minified line must not flood output.
func logExcerpt(s string) (string, bool) {
	truncated := false
	lines := strings.Split(s, "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
		truncated = true
	}
	s = strings.Join(lines, "\n")
	runes := []rune(s)
	if len(runes) > 2000 {
		s = string(runes[len(runes)-2000:])
		truncated = true
	}
	return s, truncated
}

func writeTestTimings(w io.Writer, check repokit.CheckResult) {
	if len(check.TestTimings) == 0 {
		return
	}
	label := "runner elapsed; parallel tests and subtests overlap"
	if check.Reused {
		label = "historical runner elapsed; this check was reused"
	}
	fmt.Fprintf(w, "  Test timings (%s):\n", label)
	rows := slices.Clone(check.TestTimings)
	slices.SortStableFunc(rows, func(a, b repokit.TestTiming) int {
		if a.Cached != b.Cached {
			if a.Cached {
				return 1
			}
			return -1
		}
		if a.DurationSeconds > b.DurationSeconds {
			return -1
		}
		if a.DurationSeconds < b.DurationSeconds {
			return 1
		}
		return strings.Compare(a.Package+"/"+a.Test, b.Package+"/"+b.Test)
	})
	packages, tests := 0, 0
	for _, timing := range rows {
		kind, name := "package", timing.Package
		if timing.Test == "" {
			if packages == 3 {
				continue
			}
			packages++
		} else {
			if tests == 10 {
				continue
			}
			tests++
			kind, name = "test", timing.Package+"/"+timing.Test
		}
		suffix := ""
		if timing.Cached {
			suffix = " (Go cache; historical result)"
		}
		fmt.Fprintf(w, "    %s %.3fs %s %s%s\n", kind, timing.DurationSeconds, timing.Status, name, suffix)
	}
}
