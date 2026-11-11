package validation

import (
	"encoding/json"
	"strings"
	"time"
)

// TestTiming is elapsed time reported by a test runner, not an additive portion
// of check wall time. An empty Test identifies a package; subtests retain their
// slash-separated names. Cached marks Go's replayed result, independently of
// CheckResult.Reused, which reuses the entire historical check.
type TestTiming struct {
	Package         string  `json:"package"`
	Test            string  `json:"test,omitempty"`
	Status          string  `json:"status"`
	DurationSeconds float64 `json:"durationSeconds"`
	Cached          bool    `json:"cached"`
}

// +spec=Runner timing observations never change check verdicts or selection. Only terminal events with reported elapsed time are retained; parallel and parent/subtest durations are not summed.
// +link=component://docs/validation.md
func goTestTimings(output string) []TestTiming {
	type event struct {
		Time                          *time.Time
		Action, Package, Test, Output string
		Elapsed                       *float64
	}
	type run struct {
		rows   []int
		cached bool
	}
	runs := map[string]*run{}
	var timings []TestTiming
	for line := range strings.SplitSeq(output, "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e event
		if json.Unmarshal([]byte(line), &e) != nil || e.Package == "" {
			continue
		}
		if e.Action == "start" && e.Test == "" {
			runs[e.Package] = &run{}
			continue
		}
		current := runs[e.Package]
		if current == nil {
			continue
		}
		if e.Action == "output" && e.Test == "" {
			fields := strings.Fields(e.Output)
			if len(fields) >= 3 && fields[0] == "ok" && fields[1] == e.Package && fields[2] == "(cached)" {
				current.cached = true
			}
			continue
		}
		status := ""
		switch e.Action {
		case "pass":
			status = "passed"
		case "fail":
			status = "failed"
		case "skip":
			status = "skipped"
		default:
			continue
		}
		if e.Elapsed != nil && *e.Elapsed >= 0 {
			current.rows = append(current.rows, len(timings))
			timings = append(timings, TestTiming{Package: e.Package, Test: e.Test, Status: status, DurationSeconds: *e.Elapsed, Cached: e.Time == nil})
		}
		if e.Test == "" {
			for _, index := range current.rows {
				timings[index].Cached = timings[index].Cached || current.cached
			}
			delete(runs, e.Package)
		}
	}
	return timings
}
