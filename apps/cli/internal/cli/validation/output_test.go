package validation

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
)

// +case=Test details distinguish Go cache and whole-check reuse, bound text output, and never inflate current command cost.
func TestValidationDetailedTimingPresentation(t *testing.T) {
	check := repokit.CheckResult{Status: "passed", DurationSeconds: 2, TestTimings: []repokit.TestTiming{
		{Package: "example/p", Status: "passed", DurationSeconds: 1.8},
		{Package: "example/p", Test: "TestParent/child", Status: "passed", DurationSeconds: 0.125},
		{Package: "example/cached", Test: "TestHistorical", Status: "passed", DurationSeconds: 99, Cached: true},
	}}
	var out bytes.Buffer
	writeTestTimings(&out, check)
	for _, want := range []string{"package 1.800s", "TestParent/child", "Go cache; historical result", "overlap"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
	if check.TestTimings[0].Test != "" {
		t.Fatal("presentation reordered evidence")
	}
	check.Reused = true
	out.Reset()
	writeTestTimings(&out, check)
	if !strings.Contains(out.String(), "historical runner elapsed") || currentCheckCost(".", check, "").ExecutionSeconds != 0 {
		t.Fatal(out.String())
	}
	check.TestTimings = nil
	for i := 0; i < 20; i++ {
		check.TestTimings = append(check.TestTimings, repokit.TestTiming{Package: "example/p", Test: fmt.Sprintf("Test%d", i), DurationSeconds: float64(i)})
	}
	out.Reset()
	writeTestTimings(&out, check)
	if strings.Count(out.String(), "    test ") != 10 {
		t.Fatal(out.String())
	}
}
